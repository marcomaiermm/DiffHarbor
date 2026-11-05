// Package server takes GitHub and GitLab webhooks and runs one job at a time: a review of a pushed PR, an answer in a
// thread, or deleting a closed PR's reports.
package server

import (
	"cmp"
	"compress/gzip"
	"context"
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
	actions = map[string]bool{"opened": true, "synchronize": true, "reopened": true, "ready_for_review": true}
	trusted = map[string]bool{"OWNER": true, "MEMBER": true, "COLLABORATOR": true}
	// The code hosts the server takes webhooks from, at /webhook/<name>, and @bot in their comments.
	forges   = map[string]forge.Forge{}
	mentions = map[string]*regexp.Regexp{}
	latest   = struct {
		sync.Mutex
		m map[string]string
	}{m: map[string]string{}}
	// The job the worker runs now. Closing the PR stops it, a newer push stops its review: no tokens on a dead head.
	// Its context reaches every git, check and Pi call of the job.
	running = struct {
		sync.Mutex
		key    string
		review bool
		cancel context.CancelFunc
	}{cancel: func() {}}
)

// review runs a queued push's review unless a newer push of the PR is queued.
func review(ctx context.Context, f forge.Forge, e *forge.Event) error {
	key, queued := e.Key(), e.PullRequest.Head.SHA
	latest.Lock()
	current := latest.m[key] == queued
	latest.Unlock()
	if !current {
		return nil
	}
	defer func() {
		latest.Lock()
		if latest.m[key] == queued {
			delete(latest.m, key)
		}
		latest.Unlock()
	}()
	return job.Review(ctx, f, e)
}

// work runs the queue's jobs one at a time: a review, an answer in a thread, or deleting a closed PR's reports.
// Reports are only written here, so a review can't write one after its PR's were deleted.
func work(queue chan *forge.Event) {
	for e := range queue {
		f := forges[e.Forge]
		run := func(ctx context.Context) error { return review(ctx, f, e) }
		switch {
		case closing(e):
			run = func(context.Context) error { return drop(e) }
		case e.Kind != "pull_request":
			run = func(ctx context.Context) error { return job.Chat(ctx, f, mentions[e.Forge], e) }
		}
		proc.KillLeftovers()   // what an earlier job's PR code left running, e.g. listening for this job's token
		err := proc.CleanTmp() // and left in the temp dirs all share; home and caches are the job's (proc.Workspace)
		ctx, cancel := context.WithCancel(context.Background())
		running.Lock()
		running.key, running.review, running.cancel = e.Key(), e.Kind == "pull_request", cancel
		running.Unlock()
		switch {
		case err == nil:
			err = run(ctx)
		case e.Kind == "pull_request" && !closing(e): // stays pending (done keeps it): the next push or a restart retries
			if p, err := f.Open(e); err == nil {
				p.Status(e.PullRequest.Head.SHA, "error", "Review not started: cleanup failed, retried on the next push")
			}
		case e.Comment.Body != "" && mentions[e.Forge].MatchString(e.Comment.Body): // asked us: a chat isn't kept to retry
			if p, err := f.Open(e); err == nil {
				_ = p.Reply("⚠️ Couldn't start: cleaning up after the last job failed. Ask again in a while.\n\n" + forge.Mark)
			}
		}
		if err != nil {
			log.Println(e.Key(), err)
		}
		cancel() // revokes the job's vault tokens: git's remote, Pi's keys
		proc.KillLeftovers()
		running.Lock()
		running.key = "" // idle: a later push or close has nothing to stop
		running.Unlock()
		done(e.Key())
	}
}

// closing is a PR closed or turned to draft: its reports go.
func closing(e *forge.Event) bool {
	return e.Kind == "pull_request" && (e.Action == "closed" || e.Action == "converted_to_draft")
}

// drop deletes a closed PR's reports while its deletingFile is there: none means it was reopened or done since.
// The file stays until the delete worked, for retryDrops.
func drop(e *forge.Event) error {
	f := deletingFile(e.Key())
	if !proc.Exists(f) {
		return nil
	}
	if err := job.DeleteReports(e); err != nil {
		return fmt.Errorf("deleting reports: %w", err)
	}
	_ = os.Remove(f)
	return nil
}

// stop cancels the running job on key; reviewOnly spares a chat answer.
func stop(key string, reviewOnly bool) {
	running.Lock()
	defer running.Unlock()
	if running.key == key && (running.review || !reviewOnly) {
		log.Println(key, "stopping the running job")
		running.cancel()
	}
}

// Config is the forges the server takes webhooks from; one without credentials stays off.
type Config struct {
	GitHub github.Config
	GitLab gitlab.Config
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
	if err := proc.Clean(); err != nil { // a crash's work dirs, the cache all repos shared before
		log.Fatal(err)
	}
	queue := make(chan *forge.Event, 100)
	go work(queue)
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
			latest.Lock()
			delete(latest.m, e.Key())             // queued reviews of this PR skip
			_ = os.Remove(pendingFile(e.Key()))   //nolint:gosec // eventFile escapes the key: no / left to leave the dir
			err := save(deletingFile(e.Key()), e) // a full queue, a restart or a failed delete: retryDrops
			latest.Unlock()
			if err != nil {
				log.Println(e.Key(), "deleting:", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			stop(e.Key(), false)
		case e.Kind == "pull_request" && actions[e.Action] && !e.PullRequest.Draft:
			latest.Lock()
			latest.m[e.Key()] = e.PullRequest.Head.SHA
			if e.Action == "reopened" || e.Action == "ready_for_review" { // not a late push: its close still deletes
				_ = os.Remove(deletingFile(e.Key())) //nolint:gosec // no retry deletes its next reports; eventFile escapes
			}
			err := save(pendingFile(e.Key()), e)
			latest.Unlock()
			if err != nil {
				log.Println(e.Key(), "pending:", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			stop(e.Key(), true)
		case e.Kind == "issue_comment" && human && e.Issue.PullRequest != nil && mention.MatchString(c.Body):
			e.PullRequest.Number = e.Issue.Number
		case e.Kind == "issue_comment" && e.Action == "edited" && e.Issue.PullRequest != nil && e.Sender.Type != "Bot" &&
			c.Ours && forge.LargeTicked(e.Changes.Body.From, c.Body):
			e.PullRequest.Number = e.Issue.Number // the walkthrough's box: chat starts a review in parts
		case e.Kind == "pull_request_review_comment" && human && (c.InReplyTo != 0 || mention.MatchString(c.Body)):
		default:
			return
		}
		select {
		case queue <- e:
			w.WriteHeader(http.StatusAccepted)
			// a reply without @bot may be in a thread between humans: Chat reacts once it knows the thread is ours
			if e.Kind != "pull_request" && (e.Kind != "pull_request_review_comment" || mention.MatchString(c.Body)) {
				go job.Seen(f, e)
			}
		default:
			log.Println(e.Key(), "queue full") // GitHub: Redeliver resends it
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	if job.ReportURL != "" {
		http.HandleFunc("GET /r/{path...}", serveReport)
		names, _ := fs.Glob(assets.Reviewing, "*.svg")
		for _, name := range names {
			http.HandleFunc("GET /"+name, serveLighthouse)
		}
	}
	resume(queue)
	go func() {
		for {
			retryDrops(queue)
			time.Sleep(30 * time.Minute)
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

// reportRoute splits a path under /r/ into the reports' folder ({host}/{repo…}/{n}/{secret}, see job.reportPath) and
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
	return dir, sha, job.Secret.MatchString(secret) && err == nil && n > 0 && strings.Count(pr, "/") >= 3 && fs.ValidPath(dir)
}

// A PR's queued review stays on disk until it ran, so a restart (deploy, crash) resumes it: GitHub got its 202.
// A review that kills the process repeats on every start: there is no attempt count.
func pendingFile(key string) string { return eventFile("pending", key) }

// deletingFile is a closed PR whose reports aren't deleted yet.
func deletingFile(key string) string { return eventFile("deleting", key) }

func eventFile(dir, key string) string {
	return filepath.Join(proc.DataDir, dir, url.PathEscape(key)+".json") // "/" too: one name per key
}

// save stores e in f, a pendingFile or deletingFile. Callers hold latest.
func save(f string, e *forge.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil { //nolint:gosec // eventFile escapes the key
		return err
	}
	// Through a rename: resume and retryDrops never read half a file.
	if err := os.WriteFile(f+".tmp", b, 0o600); err != nil { //nolint:gosec // eventFile escapes the key: no / left
		return err
	}
	return os.Rename(f+".tmp", f) //nolint:gosec // as above
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
func resume(queue chan *forge.Event) {
	fs, _ := filepath.Glob(filepath.Join(proc.DataDir, "pending", "*.json"))
	for _, f := range fs {
		e := &forge.Event{Kind: "pull_request"}
		b, err := os.ReadFile(f) //nolint:gosec // the server's pending dir
		if err != nil || json.Unmarshal(b, e) != nil {
			_ = os.Remove(f)
			continue
		}
		if f != pendingFile(e.Key()) { // named before eventFile escaped keys
			_ = os.Rename(f, pendingFile(e.Key()))
		}
		if forges[e.Forge] == nil { // the forge is no longer set up
			log.Println(e.Key(), "pending for", e.Forge, "which is off")
			continue
		}
		log.Println(e.Key(), "resuming the review a restart cut off")
		latest.Lock()
		latest.m[e.Key()] = e.PullRequest.Head.SHA
		latest.Unlock()
		select {
		case queue <- e:
		default: // stays on disk for the next start
			log.Println(e.Key(), "queue full")
		}
	}
}

// retryDrops queues the closed PRs whose reports a full queue, a restart or a failed delete left.
func retryDrops(queue chan *forge.Event) {
	fs, _ := filepath.Glob(filepath.Join(proc.DataDir, "deleting", "*.json"))
	for _, f := range fs {
		e := &forge.Event{Kind: "pull_request"}
		b, err := os.ReadFile(f) //nolint:gosec // the server's deleting dir
		if err != nil || json.Unmarshal(b, e) != nil {
			_ = os.Remove(f)
			continue
		}
		select {
		case queue <- e:
		default: // stays for the next round
			return
		}
	}
}
