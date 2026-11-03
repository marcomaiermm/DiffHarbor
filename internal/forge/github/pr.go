package github

import (
	"cmp"
	"crypto/hmac"
	"crypto/sha256"
	"diffharbor/internal/forge"
	"diffharbor/internal/report"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// Hub takes the App's webhooks.
type Hub struct{ secret string }

// Config is the App: GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY_B64 and GITHUB_WEBHOOK_SECRET.
type Config struct{ AppID, KeyB64, Secret string }

// Setup loads the App's key, checks the webhook secret and learns the App's slug. ok is false when no App is set.
func Setup(cfg Config) (h *Hub, ok bool, err error) {
	if cfg.KeyB64 == "" {
		return nil, false, nil
	}
	if AppKey, err = LoadKey(cfg.KeyB64); err != nil {
		return nil, true, fmt.Errorf("GITHUB_APP_PRIVATE_KEY_B64: %w", err)
	}
	AppID = cfg.AppID
	h = &Hub{cfg.Secret}
	if h.secret == "" {
		return nil, true, errors.New("GITHUB_WEBHOOK_SECRET is empty")
	}
	var app struct {
		Slug string `json:"slug"`
	}
	jwt, err := AppJWT()
	if err == nil {
		err = Call("GET", "/app", jwt, nil, &app)
	}
	if err != nil || app.Slug == "" {
		return nil, true, fmt.Errorf("GET /app: %w", err)
	}
	Slug = app.Slug
	return h, true, nil
}

// Bot is the App's slug.
func (*Hub) Bot() string { return Slug }

// Boxes is true: ticking a box edits our comment, which GitHub allows users with write access.
func (*Hub) Boxes() bool { return true }

func verify(secret string, body []byte, sig string) bool {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hmac.Equal([]byte(sig), []byte("sha256="+hex.EncodeToString(m.Sum(nil))))
}

// Parse checks the webhook's signature and reads PR and comment events.
func (h *Hub) Parse(hd http.Header, body []byte) (*forge.Event, error) {
	if !verify(h.secret, body, hd.Get("X-Hub-Signature-256")) {
		return nil, forge.ErrSignature
	}
	e := &forge.Event{Kind: hd.Get("X-GitHub-Event"), Forge: "github"}
	if e.Kind != "pull_request" && e.Kind != "pull_request_review_comment" && e.Kind != "issue_comment" {
		return nil, nil
	}
	if err := json.Unmarshal(body, e); err != nil {
		return nil, err
	}
	e.Comment.Ours = e.Comment.User.Login == Slug+"[bot]"
	return e, nil
}

// pr is a PR with an installation token for its repo.
type pr struct {
	e          *forge.Event
	api, token string
	statuses   bool // the App has "Commit statuses: write"
}

// Open mints the token the jobs use.
func (*Hub) Open(e *forge.Event) (forge.PR, error) {
	token, statuses, err := InstallToken(e)
	if err != nil {
		return nil, err
	}
	return &pr{e, "/repos/" + e.Repository.FullName, token, statuses}, nil
}

func (p *pr) Refresh() error {
	return Call("GET", fmt.Sprintf("%s/pulls/%d", p.api, p.e.PullRequest.Number), p.token, nil, &p.e.PullRequest)
}

func (p *pr) Status(sha, state, desc string) {
	if p.statuses {
		SetStatus(p.api, sha, p.token, state, desc)
	}
}

func (p *pr) ReadToken() (string, error) { return ReadToken(p.e) }

func (p *pr) File(ref string, names ...string) string { return BaseFile(p.api, p.token, ref, names...) }

func (p *pr) NestedRules(ref string, files []string) string {
	return NestedRules(p.api, p.token, ref, files)
}

func (p *pr) Skills(ref string, dirs []string, dst string) { Skills(p.e, p.token, ref, dirs, dst) }

type comment struct {
	ID        int64  `json:"id"`
	InReplyTo int64  `json:"in_reply_to_id"`
	Body      string `json:"body"`
	Path      string `json:"path"`
	DiffHunk  string `json:"diff_hunk"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (c comment) note() forge.Note {
	return forge.Note{ID: c.ID, Login: c.User.Login, Body: c.Body, Ours: c.User.Login == Slug+"[bot]"}
}

func (p *pr) Notes() ([]forge.Note, error) {
	cs, err := All[comment](fmt.Sprintf("%s/issues/%d/comments?per_page=100", p.api, p.e.PullRequest.Number), p.token)
	ns := make([]forge.Note, len(cs))
	for i, c := range cs {
		ns[i] = c.note()
	}
	return ns, err
}

func (p *pr) Note(id int64, body string) (int64, error) {
	b := map[string]string{"body": body}
	if id != 0 {
		return id, Call("PATCH", fmt.Sprintf("%s/issues/comments/%d", p.api, id), p.token, b, nil)
	}
	var c comment
	err := Call("POST", fmt.Sprintf("%s/issues/%d/comments", p.api, p.e.PullRequest.Number), p.token, b, &c)
	return c.ID, err
}

func (p *pr) Threads() ([]forge.Thread, error) {
	return Threads(p.e.Repository.FullName, p.e.PullRequest.Number, p.token)
}

func (p *pr) Resolve(t forge.Thread, sha string) {
	Resolve(p.e, p.api, p.e.PullRequest.Number, p.token, t, sha)
}

// root is the review comment's thread: GitHub points every reply at the thread's first comment.
func (p *pr) root() int64 { return cmp.Or(p.e.Comment.InReplyTo, p.e.Comment.ID) }

func (p *pr) Discussion() (forge.Discussion, error) {
	var d forge.Discussion
	cs, err := All[comment](fmt.Sprintf("%s/pulls/%d/comments?per_page=100", p.api, p.e.PullRequest.Number), p.token)
	if err != nil {
		return d, err
	}
	root := p.root()
	for _, c := range cs {
		if c.ID != root && c.InReplyTo != root {
			continue
		}
		if c.ID == root {
			d.Ours = strings.Contains(c.Body, forge.Mark)
			d.About = fmt.Sprintf("## `%s`\n\n```diff\n%s\n```\n\n", c.Path, c.DiffHunk)
		}
		d.Notes = append(d.Notes, c.note())
	}
	return d, nil
}

func (p *pr) Reply(body string) error {
	b := map[string]string{"body": body}
	if p.e.Kind == "issue_comment" {
		return Call("POST", fmt.Sprintf("%s/issues/%d/comments", p.api, p.e.PullRequest.Number), p.token, b, nil)
	}
	return Call("POST", fmt.Sprintf("%s/pulls/%d/comments/%d/replies", p.api, p.e.PullRequest.Number, p.root()), p.token, b, nil)
}

// ResolveDiscussion resolves the open thread that starts with the comment's root.
func (p *pr) ResolveDiscussion() error {
	ts, err := p.Threads()
	if err != nil {
		return err
	}
	for _, t := range ts {
		if t.Comment == p.root() && !t.Resolved {
			return ResolveThread(p.e, t.ID)
		}
	}
	return nil
}

func (p *pr) React() {
	kind := "pulls"
	if p.e.Kind == "issue_comment" {
		kind = "issues"
	}
	if err := Call("POST", fmt.Sprintf("%s/%s/comments/%d/reactions", p.api, kind, p.e.Comment.ID), p.token, map[string]string{"content": "eyes"}, nil); err != nil {
		log.Println(p.e.Key(), "reaction:", err)
	}
}

func (p *pr) CanWrite(login string) (bool, error) {
	var r struct {
		Permission string `json:"permission"`
	}
	err := Call("GET", fmt.Sprintf("%s/collaborators/%s/permission", p.api, url.PathEscape(login)), p.token, nil, &r)
	return r.Permission == "admin" || r.Permission == "write", err
}

func (p *pr) Others() ([]report.Other, error) {
	return OpenPRs(p.e.Repository.FullName, p.e.PullRequest.Number, p.token)
}

func (p *pr) Patches(n int) (map[string]string, error) { return Patches(p.api, n, p.token) }

func (p *pr) Links() report.Forge {
	pr := p.e.PullRequest
	web, _, _ := strings.Cut(pr.HTMLURL, "/pull/") // GitHub Enterprise has its own host
	host, _, _ := strings.Cut(strings.TrimPrefix(web, "https://"), "/")
	return report.Forge{
		Name: "GitHub", Ref: "#", Blob: web + "/blob/" + pr.Head.SHA + "/", Commit: web + "/commit/", PRCommit: pr.HTMLURL + "/commits/",
		Search: "https://" + host + "/search?type=code&q=" + url.QueryEscape("repo:"+p.e.Repository.FullName+" "),
	}
}
