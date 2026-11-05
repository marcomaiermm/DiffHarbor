package server

import (
	"bytes"
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
	dir := "github.com/o/r/7/" + secret
	job.Reports().Put(dir+"/versions.json", []byte(`[{"sha":"a"}]`))
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
	if vs, err := job.Versions(dir); err != nil || len(vs) != 0 {
		t.Fatal("kept its reports:", vs, err)
	}
	if retryDrops(q); len(q) != 0 {
		t.Fatal("retries a done delete")
	}
	job.Reports().Put(dir+"/versions.json", []byte(`[{"sha":"a"}]`)) // reopened, reviewed again
	if err := drop(r); err != nil {
		t.Fatal(err)
	}
	if vs, _ := job.Versions(dir); len(vs) != 1 {
		t.Fatal("a stale close deleted the reopened PR's report")
	}
}

// A report folder's secret, as job makes them.
const secret = "S5Z7AW3ODLIRWVDOPHJJLO772I"

// /r/ serves a PR's newest report, a version by its SHA, and their list, under its secret; nothing outside its
// folder, nothing without the secret or with another, nothing at a path from before the secret.
func TestServeReport(t *testing.T) {
	proc.DataDir = t.TempDir()
	sha := func(i int) string { return fmt.Sprintf("%040x", i) }
	put := func(dir string, shas ...string) {
		var vs []report.Version
		for _, s := range shas {
			vs = append(vs, report.Version{SHA: s})
			var z bytes.Buffer
			zw := gzip.NewWriter(&z)
			zw.Write([]byte(`<html data-sha="` + s + `">`))
			zw.Close()
			job.Reports().Put(dir+"/"+s+".html.gz", z.Bytes())
		}
		b, _ := json.Marshal(vs)
		job.Reports().Put(dir+"/versions.json", b)
	}
	gh, gl := "/github.com/o/r/7/"+secret, "/gitlab.example.com/g/s/p/3/"+secret
	put(gh[1:], sha(5), sha(3))
	job.Reports().Put(gl[1:]+"/"+sha(1)+".html", []byte(`<html data-sha="`+sha(1)+`">`)) // from before .html.gz
	job.Reports().Put(gl[1:]+"/versions.json", []byte(`[{"sha":"`+sha(1)+`"}]`))         // a project in a subgroup
	put("github.com/o/r/7", sha(4))                                                      // from before the secret
	put("github.com/o/123/45", sha(4))                                                   // the same, in a repository named like a number
	mux := http.NewServeMux()
	mux.HandleFunc("GET /r/{path...}", serveReport)
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		return w
	}
	if w := get("/r" + gh); w.Code != 200 || !strings.Contains(w.Body.String(), `data-sha="`+sha(5)) ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src 'self'") || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal(w.Code, w.Header())
	}
	if w := get("/r" + gh + "/" + sha(3)); w.Code != 200 || !strings.Contains(w.Body.String(), `data-sha="`+sha(3)) {
		t.Fatal("a version:", w.Code)
	}
	gzipped := func(p, want string) {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Header.Set("Accept-Encoding", "gzip, br")
		mux.ServeHTTP(w, req)
		if zr, err := gzip.NewReader(w.Body); err != nil || w.Header().Get("Content-Encoding") != "gzip" {
			t.Fatal("gzip:", p, err, w.Header())
		} else if b, _ := io.ReadAll(zr); !strings.Contains(string(b), `data-sha="`+want) {
			t.Fatal("gzip:", p, string(b))
		}
	}
	gzipped("/r"+gh, sha(5)) // as stored
	gzipped("/r"+gl, sha(1)) // compressed on the way
	if w := get("/r" + gh + "/versions"); w.Code != 200 || strings.Count(w.Body.String(), `"sha"`) != 2 {
		t.Fatal("list:", w.Code, w.Body)
	}
	if w := get("/r" + gh + "/" + sha(0)); w.Code != 404 {
		t.Fatal("a version not kept:", w.Code)
	}
	missing := get("/r/github.com/o/r/8/" + secret)
	other := "/r/github.com/o/r/7/" + strings.Repeat("A", len(secret))
	for _, p := range []string{
		"/r/o/r/7/" + secret, "/r/github.com/o/r/0/" + secret, "/r" + gh + "/" + sha(5) + "/x",
		other, other + "/versions", other + "/" + sha(5), "/r/github.com/o/r/" + secret, // another secret, none
		"/r/github.com/o/r/7", "/r/github.com/o/r/7/versions", "/r/github.com/o/r/7/" + sha(4), "/r/github.com/o/123/45", // before the secret
	} {
		if w := get(p); w.Code != 404 || w.Body.String() != missing.Body.String() || fmt.Sprint(w.Header()) != fmt.Sprint(missing.Header()) {
			t.Fatal(p, w.Code, w.Header())
		}
	}
	if missing.Code != 404 {
		t.Fatal("missing:", missing.Code)
	}
	if w := get("/r" + gl + "/" + sha(1)); w.Code != 200 || !strings.Contains(w.Body.String(), `data-sha="`+sha(1)) {
		t.Fatal("gitlab:", w.Code)
	}
	job.Reports().Put("github.com/o/r/9/"+secret+"/versions.json", []byte("{")) // unreadable is no "none"
	if w := get("/r/github.com/o/r/9/" + secret); w.Code != http.StatusBadGateway {
		t.Fatal("unreadable list:", w.Code)
	}
}

// A secret ends the reports' folder: before it the PR's, whatever repository path it sits in.
func TestReportRoute(t *testing.T) {
	for p, want := range map[string]string{
		"gitlab.example.com/g/s/p/3/" + secret + "/versions": "gitlab.example.com/g/s/p/3/" + secret + " versions",
		"github.com/o/r/7/" + secret + "/":                   "github.com/o/r/7/" + secret + " ",
		"github.com/o/r/7/../7/" + secret:                    "!",
		"github.com/o/r/x/" + secret:                         "!",
	} {
		dir, sha, ok := reportRoute(p)
		if got := dir + " " + sha; !ok && want != "!" || ok && got != want {
			t.Error(p, got, ok)
		}
	}
}

// Each lighthouse file is an SVG with a CSP: GitHub picks -light or -dark by its theme, the rest gets the plain one.
func TestServeLighthouse(t *testing.T) {
	for _, name := range []string{"reviewing.svg", "reviewing-light.svg", "reviewing-dark.svg"} {
		w := httptest.NewRecorder()
		serveLighthouse(w, httptest.NewRequest(http.MethodGet, "/"+name, nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" || w.Header().Get("Content-Security-Policy") == "" ||
			!strings.HasPrefix(w.Body.String(), "<svg") {
			t.Fatal(name, w.Code, w.Header())
		}
	}
}
