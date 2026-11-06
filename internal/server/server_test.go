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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := newPool(1, nil)
	p.running["o/r#1"] = &task{review: false, cancel: cancel}
	p.stop("o/r#1", true) // a push spares a chat answer
	p.stop("o/r#2", false)
	if ctx.Err() != nil {
		t.Fatal("stopped the wrong job")
	}
	p.stop("o/r#1", false) // closing the PR stops everything on it
	if ctx.Err() == nil {
		t.Fatal("not stopped")
	}
}

// queue is a pool's add on q: false once q is full.
func queue(q chan *forge.Event) func(*forge.Event) bool {
	return func(e *forge.Event) bool {
		select {
		case q <- e:
			return true
		default:
			return false
		}
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
	resume(queue(q))
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

// A deletingFile a converted_to_draft saved before drafts kept the reports is still a delete, not a review that never
// drains it.
func TestRetryDropsOldDraft(t *testing.T) {
	proc.DataDir = t.TempDir()
	e := &forge.Event{Forge: "github", Kind: "pull_request", Action: "converted_to_draft"}
	e.Repository.FullName, e.PullRequest.Number = "o/r", 7
	latest.Lock()
	save(deletingFile(e.Key()), e)
	latest.Unlock()
	q := make(chan *forge.Event, 1)
	if retryDrops(queue(q)); len(q) != 1 || !closing(<-q) {
		t.Fatal("not queued as a delete")
	}
}

// A closed PR's reports go through a job; its deletingFile stays until they did, for retryDrops, and without
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
	retryDrops(queue(q)) // after a restart
	r := <-q
	if !closing(r) || r.Key() != e.Key() {
		t.Fatalf("%+v", r)
	}
	newPool(1, runJob).exec(t.Context(), r, &task{cancel: func() {}})
	if vs, err := job.Versions(dir); err != nil || len(vs) != 0 {
		t.Fatal("kept its reports:", vs, err)
	}
	if retryDrops(queue(q)); len(q) != 0 {
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

// fakeJobs is p's run that holds each job until its ctx ends or it is let go, and records what ran at once.
type fakeJobs struct {
	p       *pool
	mu      sync.Mutex
	repos   map[string]int // running jobs by repo
	most    int            // running jobs at once, at most
	twice   string         // a repo or PR that ran two jobs at once
	started chan string
	finish  chan error // a job ends with what it gets, or with its ctx
	panics  string     // the PR whose job panics
}

// newFakeJobs runs f's jobs in a pool of limit.
func newFakeJobs(limit int) *fakeJobs {
	f := &fakeJobs{repos: map[string]int{}, started: make(chan string, 100), finish: make(chan error)}
	f.p = newPool(limit, f.run)
	return f
}

func (f *fakeJobs) run(ctx context.Context, e *forge.Event) error {
	f.mu.Lock()
	f.repos[e.Repo()]++
	f.p.mu.Lock()
	if f.repos[e.Repo()] > 1 || f.p.running[e.Key()] == nil {
		f.twice = e.Key()
	}
	f.p.mu.Unlock()
	n := 0
	for _, c := range f.repos {
		n += c
	}
	f.most = max(f.most, n)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.repos[e.Repo()]--
		f.mu.Unlock()
	}()
	f.started <- e.Key()
	if e.Key() == f.panics {
		panic("bug")
	}
	select {
	case err := <-f.finish:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// next is the job that starts next, "" when none does within seconds: a loaded CI machine is slow.
func (f *fakeJobs) next() string { return f.within(10 * time.Second) }

// quiet is a job that starts within a moment, "": none, as it should be. Short: it waits whenever the test passes.
func (f *fakeJobs) quiet() string { return f.within(200 * time.Millisecond) }

func (f *fakeJobs) within(d time.Duration) string {
	select {
	case k := <-f.started:
		return k
	case <-time.After(d):
		return ""
	}
}

// waitFor waits until ok, under the pool's lock, for seconds at most.
func (f *fakeJobs) waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		f.p.mu.Lock()
		yes := ok()
		f.p.mu.Unlock()
		if yes {
			return
		}
	}
	t.Fatal("timed out")
}

// idle waits until no job runs, their done included: the next test sets proc.DataDir.
func (f *fakeJobs) idle(t *testing.T) { f.waitFor(t, func() bool { return len(f.p.running) == 0 }) }

// Jobs of different repos run at once, up to the limit; a repo's never do.
func TestPoolParallel(t *testing.T) {
	proc.DataDir = t.TempDir()
	f := newFakeJobs(2)
	for _, e := range []*forge.Event{ev("a", 1, ""), ev("a", 2, ""), ev("b", 1, ""), ev("c", 1, "")} {
		if !f.p.add(e) {
			t.Fatal("full")
		}
	}
	if got := []string{f.next(), f.next(), f.quiet()}; !slices.Contains(got, "a#1") || !slices.Contains(got, "b#1") || got[2] != "" {
		t.Fatal(got) // a#2 waits for a#1, c#1 for a slot
	}
	f.p.stop("a#1", false) // each one let go starts the next; by key: a started one doesn't take the other's finish
	f.p.stop("b#1", false)
	if got := []string{f.next(), f.next()}; !slices.Contains(got, "c#1") || !slices.Contains(got, "a#2") {
		t.Fatal(got)
	}
	f.finish <- nil
	f.finish <- nil
	f.idle(t)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.most != 2 || f.twice != "" {
		t.Fatal(f.most, f.twice)
	}
}

// A panicking job ends as a failed one: the job beside it runs on, its repo and slot are free, its review isn't
// pending for the next start.
func TestPoolPanic(t *testing.T) {
	proc.DataDir = t.TempDir()
	f := newFakeJobs(2)
	f.panics = "a#1"
	latest.Lock()
	save(pendingFile("a#1"), ev("a", 1, ""))
	latest.Unlock()
	f.p.add(ev("b", 1, ""))
	if b := f.next(); b != "b#1" {
		t.Fatal(b)
	}
	f.p.add(ev("a", 1, ""))
	f.p.add(ev("a", 2, ""))
	if a1, a2 := f.next(), f.next(); a1 != "a#1" || a2 != "a#2" {
		t.Fatal(a1, a2)
	}
	if _, err := os.Stat(pendingFile("a#1")); !os.IsNotExist(err) {
		t.Fatal("stays pending:", err)
	}
	f.p.mu.Lock()
	b := f.p.running["b#1"]
	f.p.mu.Unlock()
	if b == nil {
		t.Fatal("b's job ended with a's")
	}
	f.finish <- nil
	f.finish <- nil
	f.idle(t)
}

// stop cancels only its PR's job, a push's spares an answer.
func TestPoolStop(t *testing.T) {
	proc.DataDir = t.TempDir()
	f := newFakeJobs(3)
	f.p.add(ev("a", 1, ""))
	f.p.add(ev("b", 1, ""))
	f.p.add(ev("c", 1, "issue_comment"))
	for range 3 {
		f.next()
	}
	f.p.stop("c#1", true)
	f.p.stop("a#1", false)
	f.waitFor(t, func() bool { return f.p.running["a#1"] == nil }) // a's job ends on its ctx
	f.p.mu.Lock()
	b, c := f.p.running["b#1"], f.p.running["c#1"]
	f.p.mu.Unlock()
	if b == nil || c == nil {
		t.Fatal("stopped another job", b, c)
	}
	f.finish <- nil
	f.finish <- nil
	f.idle(t)
}

// A review that couldn't clean up before it started stays pending: the next push or a restart retries it. Any other
// failure is done.
func TestReviewCleanupKeepsPending(t *testing.T) {
	proc.DataDir = t.TempDir()
	e := ev("o/r", 9, "")
	e.PullRequest.Head.SHA = "abc"
	latest.Lock()
	latest.m[e.Key()] = "abc"
	save(pendingFile(e.Key()), e)
	latest.Unlock()
	if err := review(e, func() error { return fmt.Errorf("uid 10001: %w: busy", proc.ErrCleanup) }); err == nil {
		t.Fatal("no error")
	}
	done(e.Key())
	if _, err := os.Stat(pendingFile(e.Key())); err != nil {
		t.Fatal("not kept:", err)
	}
	review(e, func() error { return errors.New("failed") })
	done(e.Key())
	if _, err := os.Stat(pendingFile(e.Key())); !os.IsNotExist(err) {
		t.Fatal("kept:", err)
	}
}

// A job is stoppable from the moment it is picked: a close before its goroutine ran stops it too, an answer as well.
func TestPoolStopPicked(t *testing.T) {
	proc.DataDir = t.TempDir()
	f := newFakeJobs(2)
	f.p.add(ev("a", 1, ""))
	f.p.add(ev("b", 1, "issue_comment"))
	f.p.stop("a#1", false)
	f.p.stop("b#1", false)
	f.next()
	f.next()
	f.idle(t) // both end on their ctx, no finish sent
}

// Two pushes' handlers queue in the other order than they set latest: the newer review stays, the older is dropped.
func TestPoolOlderPush(t *testing.T) {
	proc.DataDir = t.TempDir()
	f := newFakeJobs(1)
	f.p.add(ev("a", 2, "")) // keeps a busy
	f.next()
	older, newer := ev("a", 1, ""), ev("a", 1, "")
	older.PullRequest.Head.SHA, newer.PullRequest.Head.SHA = "old", "new"
	latest.Lock()
	latest.m["a#1"] = "new"
	latest.Unlock()
	t.Cleanup(func() { latest.Lock(); delete(latest.m, "a#1"); latest.Unlock() })
	if !f.p.add(newer) || !f.p.add(older) {
		t.Fatal("full")
	}
	f.p.mu.Lock()
	q := f.p.s.waiting["a"]
	f.p.mu.Unlock()
	if len(q) != 1 || q[0] != newer {
		t.Fatal(q)
	}
	f.finish <- nil
	f.next()
	f.finish <- nil
	f.idle(t)
}

// An unset limit is one job at a time, not none.
func TestPoolNoLimit(t *testing.T) {
	proc.DataDir = t.TempDir()
	f := newFakeJobs(0)
	f.p.add(ev("a", 1, ""))
	if f.next() != "a#1" {
		t.Fatal("never started")
	}
	f.finish <- nil
	f.idle(t)
}
