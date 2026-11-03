// Package server takes GitHub and GitLab webhooks and runs one job at a time: a review of a pushed PR or an answer in a thread.
package server

import (
	"cmp"
	"context"
	"diffharbor/internal/agent"
	"diffharbor/internal/forge"
	"diffharbor/internal/forge/github"
	"diffharbor/internal/forge/gitlab"
	"diffharbor/internal/proc"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxLearnings = 30
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

// stop cancels the running job on key; reviewOnly spares a chat answer.
func stop(key string, reviewOnly bool) {
	running.Lock()
	defer running.Unlock()
	if running.key == key && (running.review || !reviewOnly) {
		log.Println(key, "stopping the running job")
		running.cancel()
	}
}

// Serve listens on :3000 until the process dies.
func Serve() {
	gh, on, err := github.Setup()
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
	gl, on, err := gitlab.Setup()
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
		mentions[name] = mentionOf(f.Bot())
		log.Printf("%s as @%s on POST /webhook/%s", name, f.Bot(), name)
	}
	// Work dirs live next to the pnpm store (hardlinks); leftovers from a crash go.
	proc.WorkDir = filepath.Join(proc.CacheDir, "work")
	_ = os.RemoveAll(proc.WorkDir)
	if err := os.MkdirAll(proc.WorkDir, 0o755); err != nil {
		log.Fatal(err)
	}
	queue := make(chan *forge.Event, 100)
	go func() { // one job at a time
		for e := range queue {
			job := reviewPR
			if e.Kind != "pull_request" {
				job = chat
			}
			proc.KillOrphans() // what an earlier job's PR code left running, e.g. listening for this job's token
			ctx, cancel := context.WithCancel(context.Background())
			running.Lock()
			running.key, running.review, running.cancel = e.Key(), e.Kind == "pull_request", cancel
			running.Unlock()
			if err := job(ctx, e); err != nil {
				log.Println(e.Key(), err)
			}
			cancel()
			proc.KillOrphans()
			running.Lock()
			running.key = "" // idle: a later push or close has nothing to stop
			running.Unlock()
			done(e.Key())
		}
	}()
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
		case e.Kind == "pull_request" && (e.Action == "closed" || e.Action == "converted_to_draft"):
			latest.Lock()
			delete(latest.m, e.Key()) // queued reviews of this PR skip
			_ = os.Remove(pendingFile(e.Key()))
			latest.Unlock()
			_ = os.RemoveAll(reportDir(e)) // its code shouldn't outlive the PR
			stop(e.Key(), false)
			return
		case e.Kind == "pull_request" && actions[e.Action] && !e.PullRequest.Draft:
			latest.Lock()
			latest.m[e.Key()] = e.PullRequest.Head.SHA
			err := keep(e)
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
			if e.Kind != "pull_request" {
				go seen(e)
			}
		default:
			log.Println(e.Key(), "queue full") // GitHub: Redeliver resends it
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	if reportURL != "" {
		http.HandleFunc("GET /r/{path...}", serveReport)
	}
	resume(queue)
	log.Println("diffharbor on :3000 with", cmp.Or(agent.Model, agent.Default()+" (unless review.json picks one)"))
	go func() {
		if err := agent.Check(); err != nil {
			log.Println(err) // not fatal: FALLBACK_MODEL serves until someone logs in
		}
	}()
	log.Fatal((&http.Server{Addr: ":3000", ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
}

// reports holds the reports; DirFS refuses "..", so a path can't leave it.
var reports = os.DirFS(filepath.Join(proc.DataDir, "reports"))

// serveReport serves a PR's report. Only the proxy's SSO stands in front of it: a report holds the PR's code.
func serveReport(w http.ResponseWriter, r *http.Request) {
	dir, sha, ok := reportRoute(r.PathValue("path"))
	vs := versions(dir)
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-cache")
	switch {
	case !ok || len(vs) == 0:
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
		// The page's own script, and fetch for its versions; Tailwind, Shiki, mermaid, cytoscape, styles and fonts from jsdelivr; PR text in it is escaped, this is the second fence.
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline' https://cdn.jsdelivr.net; connect-src 'self'; "+
			"style-src 'unsafe-inline' https://cdn.jsdelivr.net; img-src data:; font-src data: https://cdn.jsdelivr.net; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		//nolint:gosec // os.DirFS refuses .. and absolute paths: nothing outside the reports folder
		http.ServeFileFS(w, r, reports, dir+"/"+cmp.Or(sha, vs[0].SHA)+".html")
	}
}

// head is a commit's full SHA: SHA-1's or SHA-256's.
var head = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// reportRoute splits a path under /r/ into the PR's folder ({host}/{repo…}/{n}, see reportPath) and what of it:
// a version's SHA, "versions" for their list, "" for the newest.
func reportRoute(p string) (dir, sha string, ok bool) {
	dir = strings.Trim(p, "/")
	if i := strings.LastIndexByte(dir, '/'); i >= 0 && (dir[i+1:] == "versions" || head.MatchString(dir[i+1:])) {
		dir, sha = dir[:i], dir[i+1:]
	}
	n, err := strconv.Atoi(dir[strings.LastIndexByte(dir, '/')+1:])
	return dir, sha, err == nil && n > 0 && strings.Count(dir, "/") >= 3 && fs.ValidPath(dir)
}

// A PR's queued review stays on disk until it ran, so a restart (deploy, crash) resumes it: GitHub got its 202.
// A review that kills the process repeats on every start: there is no attempt count.
func pendingFile(key string) string {
	return filepath.Join(proc.DataDir, "pending", strings.NewReplacer("/", "__", "#", "_").Replace(key)+".json")
}

// keep stores the event of a PR's latest push. Callers hold latest.
func keep(e *forge.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f := pendingFile(e.Key())
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	return os.WriteFile(f, b, 0o600) //nolint:gosec // pendingFile flattens the key: no / left to leave the dir
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

// mentionOf matches @slug as a whole login (not @slug-other).
func mentionOf(slug string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(^|\s)@` + regexp.QuoteMeta(slug) + `([^\w-]|$)`)
}
