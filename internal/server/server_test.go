package server

import (
	"context"
	"diffharbor/internal/forge"
	"diffharbor/internal/forge/github"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
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

func TestLearnings(t *testing.T) {
	proc.DataDir = t.TempDir()
	for i := range maxLearnings + 2 {
		addLearning("o/r", fmt.Sprintf("- r%d", i))
	}
	addLearning("o", "- shared")
	addLearning("x/y", "- other")
	got := readLearnings("o", "o/r")
	if !strings.HasPrefix(got, "- shared\n- r2\n") || strings.Contains(got, "- r1\n") || strings.Contains(got, "other") {
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

// A file left out keeps its last reviewed patch ID, one never reviewed gets none: the next push reviews both if changed.
func TestReviewedPatches(t *testing.T) {
	got := reviewedPatches(map[string]string{"a": "1", "b": "1", "gone": "1"}, map[string]string{"a": "2", "b": "2", "c": "2", "d": "2"}, []string{"a", "c"})
	if fmt.Sprint(got) != "map[a:2 b:1 c:2]" {
		t.Fatal(got)
	}
}

// A PR keeps its newest reports, one per head; /r/ serves the newest, a version by its SHA, and their list.
func TestReportVersions(t *testing.T) {
	proc.DataDir = t.TempDir()
	reports = os.DirFS(filepath.Join(proc.DataDir, "reports"))
	e := &forge.Event{}
	e.Repository.FullName, e.PullRequest.Number, e.PullRequest.HTMLURL = "o/r", 7, "https://github.com/o/r/pull/7"
	sha := func(i int) string { return fmt.Sprintf("%040x", i) }
	for i := range maxVersions + 2 {
		if u := writeReport(e, report.Data{Repo: "o/r", Number: 7, SHA: sha(i)}); u != reportURL+"/r/github.com/o/r/7" {
			t.Fatal(u)
		}
	}
	writeReport(e, report.Data{Repo: "o/r", Number: 7, SHA: sha(5)}) // reviewed again: the newest
	vs := versions(reportPath(e))
	if len(vs) != maxVersions || vs[0].SHA != sha(5) || vs[1].SHA != sha(maxVersions+1) || vs[0].At.IsZero() {
		t.Fatalf("%+v", vs)
	}
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
	if w := get("/r/github.com/o/r/7/versions"); w.Code != 200 || strings.Count(w.Body.String(), `"sha"`) != maxVersions {
		t.Fatal("list:", w.Code, w.Body)
	}
	for _, p := range []string{"/r/github.com/o/r/7/" + sha(0), "/r/o/r/7", "/r/github.com/o/r/8", "/r/github.com/o/r/0", "/r/github.com/o/r/7/" + sha(5) + "/x"} {
		if w := get(p); w.Code != 404 {
			t.Fatal(p, w.Code)
		}
	}
	gl := &forge.Event{Forge: "gitlab"} // a project in a subgroup
	gl.Repository.FullName, gl.PullRequest.Number, gl.PullRequest.HTMLURL = "g/s/p", 3, "https://gitlab.example.com/g/s/p/-/merge_requests/3"
	if u := writeReport(gl, report.Data{Repo: "g/s/p", Number: 3, SHA: sha(1)}); u != reportURL+"/r/gitlab.example.com/g/s/p/3" {
		t.Fatal(u)
	}
	if w := get("/r/gitlab.example.com/g/s/p/3/" + sha(1)); w.Code != 200 {
		t.Fatal("gitlab:", w.Code)
	}
}
