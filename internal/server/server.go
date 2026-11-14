// Package server takes GitHub and GitLab webhooks and runs their jobs, up to MAX_JOBS at once and a repo's one at a
// time: a review of a pushed PR, an answer in a thread, or deleting a closed PR's reports.
package server

import (
	"cmp"
	"compress/gzip"
	"context"
	"crypto/rand"
	"diffharbor/internal/agent"
	"diffharbor/internal/assets"
	"diffharbor/internal/forge"
	"diffharbor/internal/forge/github"
	"diffharbor/internal/forge/gitlab"
	"diffharbor/internal/job"
	"diffharbor/internal/proc"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// Review decides on drafts (review.json's drafts). Turned to draft brings no new code: a running review finishes.
	actions = map[string]bool{"opened": true, "synchronize": true, "reopened": true, "ready_for_review": true}
	trusted = map[string]bool{"OWNER": true, "MEMBER": true, "COLLABORATOR": true}
	// The code hosts the server takes webhooks from, at /webhook/<name>, and @bot in their comments.
	forges   = map[string]forge.Forge{}
	mentions = map[string]*regexp.Regexp{}
	latest   = struct {
		sync.Mutex
		m map[string]push
	}{m: map[string]push{}}
)

// push is a PR's review to run, in latest: a same-head event (retarget, ready) is a new one, its stopped run's cleanup
// leaves it. sha is read here: the job changes e (Refresh).
type push struct {
	sha string
	e   *forge.Event
}

// task is a running job: review spares a chat answer from a push's stop (pool.add).
type task struct {
	review bool
	cancel context.CancelFunc
}

// review runs reviewJob, a queued push's review, unless a newer push of the PR is queued. One that couldn't clean up
// before it started stays pending: the next push or a restart retries it. A head whose last maxStarts runs all stopped
// the server runs giveUp instead; one that failed stays pending too, its notice still owed.
func review(e *forge.Event, giveUp, reviewJob func() error) (err error) {
	key, queued := prKey(e), push{e.PullRequest.Head.SHA, e}
	latest.Lock()
	current := latest.m[key] == queued
	n, cerr := 0, error(nil)
	if current {
		n, cerr = countStart(pendingFile(key), e) // under latest: a close or push meanwhile doesn't get its file back
	}
	latest.Unlock()
	if !current {
		return nil
	}
	keep := false // pending: the next push or a restart retries
	defer func() {
		latest.Lock()
		if latest.m[key] == queued && !keep {
			delete(latest.m, key)
		}
		latest.Unlock()
	}()
	if cerr != nil { // not run uncounted: a review that stops the server would run on every start
		keep = true
		return fmt.Errorf("start count: %w", cerr)
	}
	if n > maxStarts {
		log.Println(e.Key(), "not reviewed: the server stopped during each of its last", maxStarts, "runs")
		if err = giveUp(); !errors.Is(err, job.ErrMoved) {
			keep = err != nil
			return err
		}
		n = 1 // the runs that stopped were on the old head: GiveUp's refresh put the new one in e
		latest.Lock()
		if latest.m[key] == queued {
			err = saveStarts(pendingFile(key), e, n)
		}
		latest.Unlock()
		if err != nil {
			keep = true
			return fmt.Errorf("start count: %w", err)
		}
	}
	if err = reviewJob(); errors.Is(err, proc.ErrCleanup) {
		keep = true
		latest.Lock()
		if latest.m[key] == queued { // it never ran
			_ = saveStarts(pendingFile(key), e, n-1) // a start too many at worst
		}
		latest.Unlock()
	}
	return err
}

// runJob runs e's job: a review, an answer in a thread, or deleting a closed PR's reports. A job that runs PR code
// does so in a proc.Workspace: it cleans up after its repo's last job, Close after this one.
func runJob(ctx context.Context, e *forge.Event) error {
	f := forges[e.Forge]
	switch {
	case closing(e):
		return drop(ctx, e)
	case e.Kind != "pull_request":
		return job.Chat(ctx, f, mentions[e.Forge], e)
	}
	return review(e, func() error { return job.GiveUp(f, e, maxStarts) }, func() error { return job.Review(ctx, f, e, "") })
}

// repoOf is the repo the scheduler runs e's job on: the forge.RepoID its sandbox's uid belongs to.
func repoOf(e *forge.Event) string {
	if f := forges[e.Forge]; f != nil {
		if id, err := forge.RepoID(f, e); err == nil {
			return id
		}
	}
	return e.Repo() // no id: the job fails before it runs PR code
}

// prKey names e's PR in latest, the pending and deleting files and running: its repo (repoOf) and number. A repo
// recreated under the old name has PRs of the old numbers, a late close of the old one's must not touch them. An event
// without the repo's id (every webhook has one), or of a forge that is off, has its name's: Event.Key.
func prKey(e *forge.Event) string { return fmt.Sprintf("%s#%d", repoOf(e), e.PullRequest.Number) }

// pool runs the queued jobs. Reports are only written by jobs, and a closed PR's delete and its review share a repo:
// a review can't write a report after its PR's were deleted.
type pool struct {
	mu sync.Mutex
	s  *scheduler
	// The running jobs by repo and PR (prKey), at most one each: a repo's jobs run one at a time. Closing the PR stops its job, a newer
	// push stops its review: no tokens on a dead head. The context reaches every git, check and Pi call of the job.
	running map[string]*task
	run     func(context.Context, *forge.Event) error // runJob, a fake in tests
}

const jobDeadline = 4 * time.Hour // 8 parts, 2 at once, ~30 min of passes each, plus checks

// newPool runs up to limit jobs at once, at least one, and queues up to 100 more.
func newPool(limit int, run func(context.Context, *forge.Event) error) *pool {
	return &pool{s: newScheduler(max(limit, 1), 100), running: map[string]*task{}, run: run}
}

// add queues e's job; false when the queue is full. A push's review stops the PR's running one: no tokens on a dead
// head.
func (p *pool) add(e *forge.Event) bool {
	repo, key := repoOf(e), prKey(e)
	p.mu.Lock()
	defer p.mu.Unlock()
	if isReview(e) {
		// Under mu: a newer push's handler that queued first is neither replaced (scheduler.add) nor stopped by an
		// older one's. This one would skip (review).
		latest.Lock()
		cur, known := latest.m[key]
		latest.Unlock()
		if known && cur.e != e { // a newer event of the PR, of this head too (retarget), is in latest
			return true
		}
		if t := p.running[key]; t != nil && t.review {
			log.Println(e.Key(), "stopping the running review")
			t.cancel()
		}
	}
	ok := p.s.add(repo, e)
	p.start()
	return ok
}

// start starts the jobs the scheduler lets run; the caller holds mu. Each is in running before mu is free: a close or
// push stops it also before it ran. A finished job frees its repo and starts the next.
func (p *pool) start() {
	for repo, e, ok := p.s.next(); ok; repo, e, ok = p.s.next() {
		// ponytail: one deadline for any job, a backstop for a hung call, not a budget
		ctx, cancel := context.WithTimeout(context.Background(), jobDeadline)
		t := &task{review: e.Kind == "pull_request", cancel: cancel}
		p.running[prKey(e)] = t
		go func() {
			p.exec(ctx, e, t)
			p.mu.Lock()
			defer p.mu.Unlock()
			p.s.free(repo)
			p.start()
		}()
	}
}

// exec runs e's job, t in running. A panic ends only this job, as a failed one: done drops its pending review, so a
// restart doesn't run into it again.
func (p *pool) exec(ctx context.Context, e *forge.Event, t *task) {
	key, rk := e.Key(), prKey(e) // before the job changes e
	chat, isChat := chats.LoadAndDelete(e)
	drop := isChat
	defer func() {
		t.cancel() // revokes the job's vault tokens: git's remote, Pi's keys
		done(rk)
		if drop {
			_ = proc.Remove(chat.(string)) //nolint:forcetypeassert // only asked and resume store, strings
		}
		p.mu.Lock()
		if p.running[rk] == t {
			delete(p.running, rk)
		}
		p.mu.Unlock()
	}()
	defer proc.Recovered(func(err error) { log.Println(key, err) })
	if isChat {
		switch n, err := countStart(chat.(string), e); { //nolint:forcetypeassert // as above
		case err != nil: // not run uncounted, as a review; kept for the next start
			drop = false
			log.Println(key, "not answered now: start count:", err)
			return
		case n > maxStarts:
			log.Println(key, "not answered: the server stopped during each of its last", maxStarts, "runs")
			return
		}
	}
	if err := p.run(ctx, e); err != nil {
		log.Println(key, err)
	}
}

// reviews reports whether a PR event asks for a review: new code, or a new base (retarget). A stacked PR skipped for
// its base, then moved to main, would merge unreviewed; Review skips a head it reviewed already.
func reviews(e *forge.Event) bool {
	return e.Kind == "pull_request" && (actions[e.Action] || e.Action == "edited" && e.Changes.Base.Ref.From != "")
}

// closing is a PR closed: its reports go.
func closing(e *forge.Event) bool {
	return e.Kind == "pull_request" && e.Action == "closed"
}

// drop deletes a closed PR's reports while its deletingFile is there: none means it was reopened or done since.
// The file stays until the delete worked, for retryDrops.
func drop(ctx context.Context, e *forge.Event) error {
	f := deletingFile(prKey(e))
	if !proc.Exists(f) {
		return nil
	}
	if err := job.DeleteReports(e); err != nil {
		return fmt.Errorf("deleting reports: %w", err)
	}
	_ = os.Remove(f)
	// after the cleanup, best effort: what the PR's findings came to, counted once however often it closes
	if fg := forges[e.Forge]; fg != nil {
		if err := job.Outcomes(ctx, fg, e); err != nil {
			log.Println(e.Key(), "outcomes:", err)
		}
	}
	return nil
}

// stop cancels e's PR's running job, a review or an answer: the PR was closed.
func (p *pool) stop(e *forge.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	latest.Lock()
	_, open := latest.m[prKey(e)]
	reopened := open && !proc.Exists(deletingFile(prKey(e))) // since the close: pushed removed its file
	latest.Unlock()
	if reopened {
		return
	}
	if t := p.running[prKey(e)]; t != nil {
		log.Println(e.Key(), "stopping the running job")
		t.cancel()
	}
}

// Config is the forges the server takes webhooks from, one without credentials stays off, and how many jobs run at
// once (MAX_JOBS).
type Config struct {
	GitHub  github.Config
	GitLab  gitlab.Config
	MaxJobs int
}

// Serve listens on :3000 until the process dies.
func Serve(cfg Config) {
	gh, on, err := github.Setup(cfg.GitHub)
	if err != nil {
		log.Fatal(err)
	}
	if on {
		forges["github"] = gh
		go func() { // once the server listens, then as a safety net
			for {
				time.Sleep(30 * time.Second)
				github.Redeliver()
				time.Sleep(30 * time.Minute)
			}
		}()
	}
	gl, on, err := gitlab.Setup(cfg.GitLab)
	if err != nil {
		log.Fatal(err)
	}
	if on {
		forges["gitlab"] = gl
	}
	if len(forges) == 0 {
		log.Fatal("no forge: set GITHUB_APP_PRIVATE_KEY_B64 or GITLAB_TOKEN")
	}
	for name, f := range forges {
		handles := []string{f.Bot()}
		if name == "gitlab" && gitlab.Name != "" {
			handles = append(handles, gitlab.Name)
		}
		mentions[name] = forge.MentionOf(handles...)
		log.Printf("%s as @%s on POST /webhook/%s", name, f.Bot(), name)
	}
	if err := proc.Clean(); err != nil { // before any job: a crash's work dirs and leftovers, the cache all repos shared before
		log.Fatal(err)
	}
	jobs := newPool(cfg.MaxJobs, runJob)
	http.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {}) // up: the image's HEALTHCHECK
	http.HandleFunc("POST /webhook/{forge}", func(w http.ResponseWriter, r *http.Request) {
		f := forges[r.PathValue("forge")]
		if f == nil {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 25<<20))
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		// 2xx only once the job is queued: GitHub doesn't retry, Redeliver resends what got no 2xx.
		e, err := f.Parse(r.Header, body)
		if errors.Is(err, forge.ErrSignature) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err != nil {
			log.Println("bad payload:", err)
			return
		}
		if e == nil {
			return
		}
		c, mention := e.Comment, mentions[e.Forge]
		human := e.Action == "created" && c.User.Type != "Bot" && !c.Ours && trusted[c.AuthorAssociation]
		switch {
		case closing(e):
			if err := closed(e); err != nil {
				log.Println(e.Key(), "deleting:", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			jobs.stop(e)
		case reviews(e):
			if err := pushed(e); err != nil {
				log.Println(e.Key(), "pending:", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		case e.Kind == "issue_comment" && human && e.Issue.PullRequest != nil && mention.MatchString(c.Body):
			e.PullRequest.Number = e.Issue.Number
		case e.Kind == "issue_comment" && e.Action == "edited" && e.Issue.PullRequest != nil && e.Sender.Type != "Bot" &&
			c.Ours && forge.LargeTicked(e.Changes.Body.From, c.Body):
			e.PullRequest.Number = e.Issue.Number // the walkthrough's box: chat starts a review in parts
		case e.Kind == "pull_request_review_comment" && human && (c.InReplyTo != 0 || mention.MatchString(c.Body)):
		default:
			return
		}
		seen := *e // the job changes e (Refresh, Chat's review): Seen reads its own
		if err := asked(e); err != nil || !jobs.add(e) {
			log.Println(e.Key(), "not queued:", cmp.Or(err, errFull)) // GitHub: Redeliver resends it
			unask(e)                                                  // the redelivery saves it again
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		// a reply without @bot may be in a thread between humans: Chat reacts once it knows the thread is ours
		if seen.Kind != "pull_request" && (seen.Kind != "pull_request_review_comment" || mention.MatchString(c.Body)) {
			go job.Seen(f, &seen)
		}
	})
	if job.ReportURL != "" {
		http.HandleFunc("GET /r/{path...}", serveReport)
		names, _ := fs.Glob(assets.Reviewing, "*.svg")
		for _, name := range names {
			http.HandleFunc("GET /"+name, serveLighthouse)
		}
	}
	resume(jobs.add)
	retryDrops(jobs.add) // before a webhook: a reopen must not miss a file it renames
	go func() {
		for {
			time.Sleep(30 * time.Minute)
			retryDrops(jobs.add)
		}
	}()
	log.Println("diffharbor on :3000 with", cmp.Or(agent.Model, agent.Default()+" (unless review.json picks one)"))
	go func() {
		if err := agent.Check(); err != nil {
			log.Println(err) // not fatal: FALLBACK_MODEL serves until someone logs in
		}
	}()
	// A client that sends its body slowly or idles holds a connection only so long. No WriteTimeout: a report is big.
	srv := &http.Server{Addr: ":3000", ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute, IdleTimeout: 2 * time.Minute}
	log.Fatal(srv.ListenAndServe())
}

// closed marks e's PR closed before its delete is queued: its queued reviews skip, its deletingFile is saved.
func closed(e *forge.Event) error {
	key := prKey(e)
	latest.Lock()
	defer latest.Unlock()
	delete(latest.m, key) // queued reviews of this PR skip
	_ = os.Remove(pendingFile(key))
	return save(deletingFile(key), e) // a full queue, a restart or a failed delete: retryDrops
}

// pushed makes e the PR's review to run before it is queued: older queued ones skip, its pendingFile is saved.
func pushed(e *forge.Event) error {
	key := prKey(e)
	latest.Lock()
	defer latest.Unlock()
	latest.m[key] = push{e.PullRequest.Head.SHA, e}
	if e.Action == "reopened" || e.Action == "ready_for_review" { // not a late push: its close still deletes
		_ = os.Remove(deletingFile(key)) //nolint:gosec // no retry deletes its next reports; eventFile escapes
		// one by the repo's name too: retryDrops' rename of it fails, it queues no stale close
		_ = os.Remove(deletingFile(e.Key())) //nolint:gosec // as above
	}
	return save(pendingFile(key), e)
}

// serveLighthouse serves the walkthrough's reviewing banner: GitHub's image proxy fetches it, on GitLab the browser.
func serveLighthouse(w http.ResponseWriter, r *http.Request) {
	b, err := assets.Reviewing.ReadFile(path.Base(r.URL.Path)) // only the routes registered for its files get here
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/svg+xml")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'") // opened on its own: no scripts
	h.Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(b)
}

// serveReport serves a PR's report to who has its link: a report holds the PR's code, and the app has no login, the
// secret in the path keeps everyone else out. No referrer: the page's links and CDN fetches must not carry it off.
func serveReport(w http.ResponseWriter, r *http.Request) {
	h := w.Header() // first: a path without a secret looks like a missing report
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-cache")
	dir, sha, ok := reportRoute(r.PathValue("path"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if to := job.Moved(dir); to != "" { // a link from before the repo id: its reports moved, or are moving
		http.Redirect(w, r, strings.TrimSuffix(job.ReportURL+"/r/"+to+"/"+sha, "/"), http.StatusMovedPermanently)
		return
	}
	pr := path.Dir(dir) // for the log: not the secret
	vs, err := job.Versions(dir)
	if err != nil {
		log.Println(pr, "report:", job.Hide(err, path.Base(dir)))
		http.Error(w, "the reports can't be read now", http.StatusBadGateway)
		return
	}
	switch {
	case len(vs) == 0:
		http.NotFound(w, r)
	case sha == "versions": // for the page's version picker
		type entry struct {
			SHA string    `json:"sha"`
			At  time.Time `json:"at"`
		}
		es := make([]entry, len(vs))
		for i, v := range vs {
			es[i] = entry{v.SHA, v.At}
		}
		h.Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(es)
	default:
		// The page's own script, and fetch for its versions; Shiki, mermaid, cytoscape, styles and fonts from jsdelivr; PR text in it is escaped, this is the second fence.
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline' https://cdn.jsdelivr.net; connect-src 'self'; "+
			"style-src 'unsafe-inline' https://cdn.jsdelivr.net; img-src data:; font-src data: https://cdn.jsdelivr.net; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		name := dir + "/" + cmp.Or(sha, vs[0].SHA) + ".html"
		f, err := job.Reports().Open(name + ".gz")
		plain := errors.Is(err, fs.ErrNotExist)
		if plain { // ponytail: a report from before .html.gz; drop once those PRs closed
			f, err = job.Reports().Open(name)
		}
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			log.Println(pr, sha, "report:", job.Hide(err, path.Base(dir)))
			http.Error(w, "the report can't be read now", http.StatusBadGateway)
			return
		}
		defer f.Close()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Vary", "Accept-Encoding")
		var in io.Reader = f
		var out io.Writer = w
		switch gz := strings.Contains(r.Header.Get("Accept-Encoding"), "gzip"); {
		case gz:
			h.Set("Content-Encoding", "gzip")
			if plain {
				zw := gzip.NewWriter(w)
				defer func() { _ = zw.Close() }() // its last write failing is the client gone, as io.Copy's below
				out = zw
			}
		case !plain: // a client without gzip, curl
			zr, err := gzip.NewReader(f)
			if err != nil {
				log.Println(pr, sha, "report:", job.Hide(err, path.Base(dir)))
				http.Error(w, "the report can't be read now", http.StatusBadGateway)
				return
			}
			in = zr
		}
		if _, err := io.Copy(out, in); err != nil {
			log.Println(pr, sha, "report:", job.Hide(err, path.Base(dir))) // the headers are out: the page ends short
		}
	}
}

// head is a commit's full SHA: SHA-1's or SHA-256's.
var head = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// reportRoute splits a path under /r/ into the reports' folder ({host}/id:{repo id}/{n}/{secret}, see job.reportPath,
// or {host}/{repo…}/{n}/{secret} from before the id: served, or redirected once job.adopt moved it) and
// what of it: a version's SHA, "versions" for their list, "" for the newest. A wrong secret is a folder that isn't
// there: a 404 like any missing report. A path without one, as before the secret, isn't a route.
func reportRoute(p string) (dir, sha string, ok bool) {
	dir = strings.Trim(p, "/")
	if i := strings.LastIndexByte(dir, '/'); i >= 0 && (dir[i+1:] == "versions" || head.MatchString(dir[i+1:])) {
		dir, sha = dir[:i], dir[i+1:]
	}
	pr, secret := path.Split(dir)
	pr = strings.TrimSuffix(pr, "/")
	n, err := strconv.Atoi(pr[strings.LastIndexByte(pr, '/')+1:])
	return dir, sha, job.Secret.MatchString(secret) && err == nil && n > 0 && strings.Count(pr, "/") >= 2 && fs.ValidPath(dir)
}

// A PR's queued review stays on disk until it ran, so a restart (deploy, crash) resumes it: GitHub got its 202.
func pendingFile(key string) string { return eventFile("pending", key) }

// maxStarts is how often one head's review starts: one that kills the process (a PR's tests eating the container's
// memory) would otherwise repeat, and be paid for, on every restart.
const maxStarts = 3

// pending is a pendingFile: the review's event and how often it started. A push saves a new one: it starts over.
type pending struct {
	*forge.Event
	Starts int `json:"starts,omitempty"`
}

// countStart counts one more start of e's job in f, a pending, and returns how many there were. f's one writer: a
// review's holds latest.
func countStart(f string, e *forge.Event) (int, error) {
	p := pending{Event: &forge.Event{}}
	b, err := os.ReadFile(f) //nolint:gosec // a pending or chat file
	if err != nil || json.Unmarshal(b, &p) != nil || p.PullRequest.Head.SHA != e.PullRequest.Head.SHA {
		p.Starts = 0
	}
	return p.Starts + 1, saveStarts(f, e, p.Starts+1)
}

func saveStarts(f string, e *forge.Event, n int) error { return save(f, pending{e, n}) }

var errFull = errors.New("queue full")

// chats are the queued comments' jobs by event: each delivery's own file in chats/, a pending, removed once it ran. A
// restart before then answers it, unless it started maxStarts times already (exec).
var chats sync.Map

// asked keeps a comment's job on disk like a review's (pushed).
func asked(e *forge.Event) error {
	if e.Kind == "pull_request" {
		return nil
	}
	// by the time first: resume answers in the order they came (Glob sorts)
	f := eventFile("chats", fmt.Sprintf("%d-%s-%s-%d-%s", time.Now().UnixNano(), e.Forge, e.Kind, e.Comment.ID, rand.Text()))
	if err := save(f, pending{Event: e}); err != nil {
		return err
	}
	chats.Store(e, f)
	return nil
}

// unask drops e's chat file: its job isn't queued.
func unask(e *forge.Event) {
	if f, ok := chats.LoadAndDelete(e); ok {
		_ = os.Remove(f.(string)) //nolint:forcetypeassert,gosec // asked's own file in chats/
	}
}

// deletingFile is a closed PR whose reports aren't deleted yet.
func deletingFile(key string) string { return eventFile("deleting", key) }

func eventFile(dir, key string) string {
	return filepath.Join(proc.DataDir, dir, url.PathEscape(key)+".json") // "/" too: one name per key
}

// save stores v, an event or a pending, in f, a pending, deleting or chat file. One writer of f at a time
// (proc.WriteAtomic): a pending or deleting file's holds latest, a chat file is its delivery's.
func save(f string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return proc.WriteAtomic(f, b) // resume and retryDrops never read half a file
}

// done drops key's stored review once no newer push for it waits.
func done(key string) {
	latest.Lock()
	defer latest.Unlock()
	if _, ok := latest.m[key]; !ok {
		_ = os.Remove(pendingFile(key))
	}
}

// resume queues the reviews a restart cut off.
func resume(add func(*forge.Event) bool) {
	fs, _ := filepath.Glob(filepath.Join(proc.DataDir, "pending", "*.json"))
	var es []*forge.Event // queued once all are read: a review done meanwhile would let its PR's older file pass
	for _, f := range fs {
		e := &forge.Event{Kind: "pull_request"}
		b, err := os.ReadFile(f) //nolint:gosec // the server's pending dir
		if err != nil || json.Unmarshal(b, e) != nil {
			_ = os.Remove(f)
			continue
		}
		if forges[e.Forge] == nil { // the forge is no longer set up
			log.Println(e.Key(), "pending for", e.Forge, "which is off")
			continue
		}
		key := prKey(e)
		if f != pendingFile(key) && proc.Exists(pendingFile(key)) { // only this version writes the id's: it is newer
			_ = os.Remove(f)
			continue
		}
		if f != pendingFile(key) { // named before eventFile escaped keys, or by the repo's name
			if err := os.Rename(f, pendingFile(key)); err != nil { // queued, done would leave f: run again on each start
				log.Println(e.Key(), "pending:", err)
				continue
			}
		}
		es = append(es, e)
	}
	for _, e := range es {
		log.Println(e.Key(), "resuming the review a restart cut off")
		latest.Lock()
		latest.m[prKey(e)] = push{e.PullRequest.Head.SHA, e}
		latest.Unlock()
		if !add(e) { // stays on disk for the next start
			log.Println(e.Key(), "queue full")
		}
	}
	cs, _ := filepath.Glob(filepath.Join(proc.DataDir, "chats", "*.json"))
	for _, f := range cs {
		e := &forge.Event{}
		b, err := os.ReadFile(f)                       //nolint:gosec // the server's chats dir
		if err != nil || json.Unmarshal(b, e) != nil { // a pending's starts aren't the event's
			_ = os.Remove(f)
			continue
		}
		if forges[e.Forge] == nil { // kept, as a pending review
			log.Println(e.Key(), "comment for", e.Forge, "which is off")
			continue
		}
		log.Println(e.Key(), "answering the comment a restart cut off")
		chats.Store(e, f)
		if !add(e) { // stays on disk for the next start
			chats.Delete(e)
			log.Println(e.Key(), "queue full")
		}
	}
}

// retryDrops queues the closed PRs whose reports a full queue, a restart or a failed delete left.
func retryDrops(add func(*forge.Event) bool) {
	fs, _ := filepath.Glob(filepath.Join(proc.DataDir, "deleting", "*.json"))
	for _, f := range fs {
		e := &forge.Event{Kind: "pull_request"}
		b, err := os.ReadFile(f) //nolint:gosec // the server's deleting dir
		if err != nil || json.Unmarshal(b, e) != nil {
			_ = os.Remove(f)
			continue
		}
		e.Action = "closed" // a delete whatever saved it: a converted_to_draft from before drafts kept the reports
		if f != deletingFile(prKey(e)) {
			// named by the repo's name: drop looks for it by prKey. Under latest: a reopen removes both (pushed), then
			// this fails.
			latest.Lock()
			err = os.Rename(f, deletingFile(prKey(e)))
			latest.Unlock()
			if err != nil {
				continue
			}
		}
		if !add(e) {
			return // stays for the next round
		}
	}
}
