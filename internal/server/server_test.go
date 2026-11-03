package server

import (
	"context"
	"diffharbor/internal/forge"
	"diffharbor/internal/forge/github"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMention(t *testing.T) {
	m := mentionOf("diffharbor")
	for s, want := range map[string]bool{
		"@diffharbor review": true, "hey @DiffHarbor, why?": true, "@diffharbor": true,
		"@diffharbor-bot hi": false, "mail@diffharbor": false,
	} {
		if m.MatchString(s) != want {
			t.Errorf("%q", s)
		}
	}
	if got := strings.TrimSpace(m.ReplaceAllString("@diffharbor review", "")); got != "review" {
		t.Fatalf("%q", got)
	}
}

func TestStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	running.key, running.review, running.cancel = "o/r#1", false, cancel
	stop("o/r#1", true) // a push spares a chat answer
	stop("o/r#2", false)
	if ctx.Err() != nil {
		t.Fatal("stopped the wrong job")
	}
	stop("o/r#1", false) // closing the PR stops everything on it
	if ctx.Err() == nil {
		t.Fatal("not stopped")
	}
}

func TestPendingResume(t *testing.T) {
	proc.DataDir = t.TempDir()
	key := "o/r#7"
	forges["github"], forges["gitlab"] = &github.Hub{}, nil
	defer clear(forges)
	gh := &forge.Event{Forge: "github", Kind: "pull_request", Action: "synchronize"}
	gh.Repository.FullName, gh.PullRequest.Number, gh.PullRequest.Head.SHA = "o/r", 7, "abc"
	gl := &forge.Event{Forge: "gitlab", Action: "opened"} // its forge is off: stays on disk
	gl.Repository.FullName, gl.PullRequest.Number = "g/s/p", 1
	latest.Lock()
	keep(gh)
	keep(gl)
	latest.Unlock()
	q := make(chan *forge.Event, 2)
	resume(q)
	if e := <-q; e.Kind != "pull_request" || e.Key() != key || e.Action != "synchronize" || e.Forge != "github" || latest.m[key] != "abc" || len(q) != 0 {
		t.Fatalf("%+v", e)
	}
	if _, err := os.Stat(pendingFile("gitlab/g/s/p#1")); err != nil {
		t.Fatal(err)
	}
	latest.m[key] = "def" // a newer push waits
	done(key)
	if _, err := os.Stat(pendingFile(key)); err != nil {
		t.Fatal("dropped a waiting push:", err)
	}
	delete(latest.m, key)
	done(key)
	if _, err := os.Stat(pendingFile(key)); !os.IsNotExist(err) {
		t.Fatal("kept after its review:", err)
	}
}

// /r/ serves a PR's newest report, a version by its SHA, and their list; nothing outside a PR's folder.
func TestServeReport(t *testing.T) {
	proc.DataDir = t.TempDir()
	sha := func(i int) string { return fmt.Sprintf("%040x", i) }
	put := func(dir string, shas ...string) {
		d := filepath.Join(proc.DataDir, "reports", filepath.FromSlash(dir))
		var vs []report.Version
		for _, s := range shas {
			vs = append(vs, report.Version{SHA: s})
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(d, s+".html"), []byte(`<html data-sha="`+s+`">`), 0o600)
		}
		b, _ := json.Marshal(vs)
		os.WriteFile(filepath.Join(d, "versions.json"), b, 0o600)
	}
	put("github.com/o/r/7", sha(5), sha(3))
	put("gitlab.example.com/g/s/p/3", sha(1)) // a project in a subgroup
	mux := http.NewServeMux()
	mux.HandleFunc("GET /r/{path...}", serveReport)
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		return w
	}
	if w := get("/r/github.com/o/r/7"); w.Code != 200 || !strings.Contains(w.Body.String(), `data-sha="`+sha(5)) ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src 'self'") {
		t.Fatal(w.Code, w.Header())
	}
	if w := get("/r/github.com/o/r/7/" + sha(3)); w.Code != 200 || !strings.Contains(w.Body.String(), `data-sha="`+sha(3)) {
		t.Fatal("a version:", w.Code)
	}
	if w := get("/r/github.com/o/r/7/versions"); w.Code != 200 || strings.Count(w.Body.String(), `"sha"`) != 2 {
		t.Fatal("list:", w.Code, w.Body)
	}
	for _, p := range []string{"/r/github.com/o/r/7/" + sha(0), "/r/o/r/7", "/r/github.com/o/r/8", "/r/github.com/o/r/0", "/r/github.com/o/r/7/" + sha(5) + "/x"} {
		if w := get(p); w.Code != 404 {
			t.Fatal(p, w.Code)
		}
	}
	if w := get("/r/gitlab.example.com/g/s/p/3/" + sha(1)); w.Code != 200 {
		t.Fatal("gitlab:", w.Code)
	}
}
