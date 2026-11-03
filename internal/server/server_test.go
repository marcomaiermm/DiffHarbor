package server

import (
	"compress/gzip"
	"context"
	"diffharbor/internal/forge"
	"diffharbor/internal/forge/github"
	"diffharbor/internal/job"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	save(pendingFile(gh.Key()), gh)
	save(pendingFile(gl.Key()), gl)
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

// A closed PR's reports go through the worker; its deletingFile stays until they did, for retryDrops, and without
// it (reopened since) a close deletes nothing.
func TestDrop(t *testing.T) {
	proc.DataDir = t.TempDir()
	e := &forge.Event{Forge: "github", Kind: "pull_request", Action: "closed"}
	e.Repository.FullName, e.PullRequest.Number, e.PullRequest.HTMLURL = "o/r", 7, "https://github.com/o/r/pull/7"
	job.Reports().Put("github.com/o/r/7/versions.json", []byte("[]"))
	latest.Lock()
	save(deletingFile(e.Key()), e)
	latest.Unlock()
	q := make(chan *forge.Event, 1)
	retryDrops(q) // after a restart
	r := <-q
	if !closing(r) || r.Key() != e.Key() {
		t.Fatalf("%+v", r)
	}
	q <- r
	close(q)
	work(q)
	if vs, err := job.Versions("github.com/o/r/7"); err != nil || len(vs) != 0 {
		t.Fatal("kept its reports:", vs, err)
	}
	if retryDrops(q); len(q) != 0 {
		t.Fatal("retries a done delete")
	}
	job.Reports().Put("github.com/o/r/7/versions.json", []byte(`[{"sha":"a"}]`)) // reopened, reviewed again
	if err := drop(r); err != nil {
		t.Fatal(err)
	}
	if vs, _ := job.Versions("github.com/o/r/7"); len(vs) != 1 {
		t.Fatal("a stale close deleted the reopened PR's report")
	}
}

// /r/ serves a PR's newest report, a version by its SHA, and their list; nothing outside a PR's folder.
func TestServeReport(t *testing.T) {
	proc.DataDir = t.TempDir()
	sha := func(i int) string { return fmt.Sprintf("%040x", i) }
	put := func(dir string, shas ...string) {
		var vs []report.Version
		for _, s := range shas {
			vs = append(vs, report.Version{SHA: s})
			job.Reports().Put(dir+"/"+s+".html", []byte(`<html data-sha="`+s+`">`))
		}
		b, _ := json.Marshal(vs)
		job.Reports().Put(dir+"/versions.json", b)
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
	gz := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/r/github.com/o/r/7", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	mux.ServeHTTP(gz, req)
	if zr, err := gzip.NewReader(gz.Body); err != nil || gz.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("gzip:", err, gz.Header())
	} else if b, _ := io.ReadAll(zr); !strings.Contains(string(b), `data-sha="`+sha(5)) {
		t.Fatal("gzip:", string(b))
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
	job.Reports().Put("github.com/o/r/9/versions.json", []byte("{")) // unreadable is no "none"
	if w := get("/r/github.com/o/r/9"); w.Code != http.StatusBadGateway {
		t.Fatal("unreadable list:", w.Code)
	}
}
