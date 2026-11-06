// Package gitlab is the GitLab side of the bot: a bot user's token, REST API v4, merge request webhooks,
// commit status, discussions for the findings and the walkthrough note.
package gitlab

import (
	"bytes"
	"cmp"
	"crypto/subtle"
	"diffharbor/internal/forge"
	"diffharbor/internal/text"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// APIURL is GITLAB_URL's REST API.
	APIURL = "https://gitlab.com/api/v4"
	// Bot is the token's user: @Bot in an MR comment talks to the bot.
	Bot string
	// Name is its display name: people type @Name when they skip the autocomplete.
	Name             string
	token, readToken string
	client           = &http.Client{Timeout: time.Minute}
	errNotFound      = errors.New("not found")
	// project and group access tokens and service accounts: bots that aren't us
	otherBot = regexp.MustCompile(`^(project|group)_\d+_bot|^service_account_`)
)

// Hub takes GitLab's webhooks.
type Hub struct{ secret string }

// Config is the bot user: GITLAB_URL, GITLAB_TOKEN, GITLAB_READ_TOKEN and GITLAB_WEBHOOK_SECRET, and
// GITLAB_FINDINGS: review (default) or discussions.
type Config struct{ URL, Token, ReadToken, Secret, Findings string }

// Setup checks cfg and learns the bot's username. ok is false when no GITLAB_TOKEN is set.
func Setup(cfg Config) (h *Hub, ok bool, err error) {
	if token = cfg.Token; token == "" {
		return nil, false, nil
	}
	APIURL = strings.TrimSuffix(cmp.Or(cfg.URL, "https://gitlab.com"), "/") + "/api/v4"
	h = &Hub{cfg.Secret}
	if h.secret == "" {
		return nil, true, errors.New("GITLAB_WEBHOOK_SECRET is empty")
	}
	if Findings = cmp.Or(cfg.Findings, "review"); Findings != "review" && Findings != "discussions" {
		return nil, true, fmt.Errorf("GITLAB_FINDINGS %q: want review or discussions", Findings)
	}
	readToken = cfg.ReadToken // unset: git fetches with token, which stays in the vault like this one
	var u struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if err := call("GET", "/user", nil, &u); err != nil || u.Username == "" {
		return nil, true, fmt.Errorf("gitlab GET /user: %w", err)
	}
	Bot, Name = u.Username, u.Name
	return h, true, nil
}

// Bot is the token user's username.
func (*Hub) Bot() string { return Bot }

// Boxes is false: GitLab lets only a comment's author tick its boxes.
func (*Hub) Boxes() bool { return false }

// Instance is GITLAB_URL's host and path.
func (*Hub) Instance() string {
	u, _ := url.Parse(APIURL)
	return u.Host + strings.TrimSuffix(u.Path, "/api/v4")
}

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

// do calls the API and returns the body and the next page ("" on the last).
func do(method, url string, body any) ([]byte, string, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, "", err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, APIURL+url, rd)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "diffharbor")
	res, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", err
	}
	if res.StatusCode == http.StatusNotFound {
		return nil, "", fmt.Errorf("%s %s: %w", method, url, errNotFound)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, "", &apiError{res.StatusCode, fmt.Sprintf("%s %s: %d %s", method, url, res.StatusCode, text.Truncate(string(b), 300))}
	}
	return b, res.Header.Get("X-Next-Page"), nil
}

// call calls the API with JSON in and out (out may be nil).
func call(method, url string, body, out any) error {
	b, _, err := do(method, url, body)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// all GETs every page of a list (url with a query), at most 30 pages.
func all[T any](url string) ([]T, error) {
	var out []T
	for page := "1"; page != ""; {
		b, next, err := do("GET", url+"&page="+page, nil)
		var p []T
		if err == nil {
			err = json.Unmarshal(b, &p)
		}
		if err != nil {
			return nil, err
		}
		if out = append(out, p...); page == "30" {
			break
		}
		page = next
	}
	return out, nil
}

// raw GETs a file of the project at ref.
func raw(project, ref, p string) (string, error) {
	b, _, err := do("GET", fmt.Sprintf("%s/repository/files/%s/raw?ref=%s", project, url.PathEscape(p), url.QueryEscape(ref)), nil)
	return string(b), err
}

type entry struct{ Path, Type, Mode, Name string }

// tree lists a dir of the project at ref.
func tree(project, ref, dir string, recursive bool) ([]entry, error) {
	return all[entry](fmt.Sprintf("%s/repository/tree?path=%s&ref=%s&recursive=%t&per_page=100", project, url.QueryEscape(dir), url.QueryEscape(ref), recursive))
}

// copyDir copies the file or dir p of the project at ref to dst and follows symlinks that stay inside the repo.
func copyDir(project, ref, p, dst string, calls *int) {
	if *calls--; *calls < 0 {
		return
	}
	es, err := tree(project, ref, p, true)
	if err != nil || len(es) == 0 { // a file, or nothing
		if s, err := raw(project, ref, p); err == nil {
			write(p, dst, s)
		}
		return
	}
	for _, e := range es {
		rel := strings.TrimPrefix(e.Path, p+"/")
		if e.Type != "blob" || !filepath.IsLocal(rel) {
			continue
		}
		if *calls--; *calls < 0 {
			return
		}
		s, err := raw(project, ref, e.Path)
		if err != nil {
			continue
		}
		if e.Mode != "120000" {
			write(e.Path, filepath.Join(dst, rel), s)
			continue
		}
		// A symlink, its blob the target: inside the repo and not an ancestor (a loop); other loops end with the budget.
		t := path.Join(path.Dir(e.Path), s)
		if !path.IsAbs(s) && t != ".." && !strings.HasPrefix(t, "../") && t != "." && !strings.HasPrefix(e.Path+"/", t+"/") {
			copyDir(project, ref, t, filepath.Join(dst, rel), calls)
		}
	}
}

func write(p, dst, s string) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		log.Println("skill", p+":", err)
	} else if err := os.WriteFile(dst, []byte(s), 0o644); err != nil {
		log.Println("skill", p+":", err)
	}
}

// access is a user's access level in the project, inherited ones included: 30 developer, 40 maintainer; 0 = none.
func access(project string, user int64) (int, error) {
	var m struct {
		Level int `json:"access_level"`
	}
	err := call("GET", fmt.Sprintf("%s/members/all/%d", project, user), nil, &m)
	if errors.Is(err, errNotFound) {
		return 0, nil
	}
	return m.Level, err
}

// association maps an access level onto GitHub's: maintainers may set rules for the group, developers for the repo.
func association(level int) string {
	switch {
	case level >= 40:
		return "MEMBER"
	case level >= 30:
		return "COLLABORATOR"
	}
	return ""
}

// mr is a merge request as webhooks and the API show it.
type mr struct {
	IID             int                 `json:"iid"`
	Title           string              `json:"title"`
	Description     string              `json:"description"`
	Draft           bool                `json:"draft"`
	WIP             bool                `json:"work_in_progress"` // GitLab before 14
	State           string              `json:"state"`
	URL             string              `json:"url"`     // webhooks
	WebURL          string              `json:"web_url"` // the API
	SHA             string              `json:"sha"`
	LastCommit      struct{ ID string } `json:"last_commit"`
	SourceBranch    string              `json:"source_branch"`
	TargetBranch    string              `json:"target_branch"`
	SourceProjectID int64               `json:"source_project_id"`
	HasConflicts    bool                `json:"has_conflicts"`
	Author          struct {
		ID int64 `json:"id"`
	} `json:"author"`
	DiffRefs *struct {
		Base  string `json:"base_sha"`
		Start string `json:"start_sha"`
		Head  string `json:"head_sha"`
	} `json:"diff_refs"`
}

// fill sets the event's PullRequest from m.
func (m *mr) fill(e *forge.Event) {
	p := &e.PullRequest
	p.Number, p.Title, p.Body, p.Draft = m.IID, m.Title, m.Description, m.Draft || m.WIP
	p.State = m.State
	if m.State == "opened" {
		p.State = "open"
	}
	p.HTMLURL = cmp.Or(m.WebURL, m.URL)
	p.Head.SHA, p.Head.Ref, p.Head.Repo.ID = cmp.Or(m.SHA, m.LastCommit.ID), m.SourceBranch, m.SourceProjectID
	p.Base.Ref = m.TargetBranch
	if m.DiffRefs != nil {
		p.Base.SHA = m.DiffRefs.Start // the target branch the diff is against
	}
	p.Base.SHA = cmp.Or(p.Base.SHA, m.TargetBranch)
	mergeable := !m.HasConflicts
	p.Mergeable = &mergeable
}

// Parse checks X-Gitlab-Token and reads merge request and note events.
func (h *Hub) Parse(hd http.Header, body []byte) (*forge.Event, error) {
	if subtle.ConstantTimeCompare([]byte(hd.Get("X-Gitlab-Token")), []byte(h.secret)) != 1 {
		return nil, forge.ErrSignature
	}
	var w struct {
		Kind string `json:"object_kind"`
		User struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
		Project struct {
			ID       int64  `json:"id"`
			Path     string `json:"path_with_namespace"`
			CloneURL string `json:"git_http_url"`
			Default  string `json:"default_branch"`
		} `json:"project"`
		Attrs   json.RawMessage `json:"object_attributes"`
		MR      mr              `json:"merge_request"`
		Changes struct {
			Draft *struct{ Previous, Current bool } `json:"draft"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}
	e := &forge.Event{Forge: "gitlab"}
	e.Repository.ID, e.Repository.FullName, e.Repository.CloneURL = w.Project.ID, w.Project.Path, w.Project.CloneURL
	e.Repository.DefaultBranch = w.Project.Default
	e.Sender.Login = w.User.Username
	switch w.Kind {
	case "merge_request":
		var a struct {
			mr
			Action string `json:"action"`
			OldRev string `json:"oldrev"`
		}
		if err := json.Unmarshal(w.Attrs, &a); err != nil {
			return nil, err
		}
		a.fill(e)
		e.Kind = "pull_request"
		d := w.Changes.Draft
		switch {
		case a.Action == "open":
			e.Action = "opened"
		case a.Action == "reopen":
			e.Action = "reopened"
		case a.Action == "close" || a.Action == "merge":
			e.Action = "closed"
		case a.Action == "update" && a.OldRev != "": // a push
			e.Action = "synchronize"
		case a.Action == "update" && d != nil && d.Previous && !d.Current:
			e.Action = "ready_for_review"
		case a.Action == "update" && d != nil && !d.Previous && d.Current:
			e.Action = "converted_to_draft"
		default: // title, labels, approvals: nothing to review
			return nil, nil
		}
		return e, nil
	case "note":
		var a struct {
			ID           int64  `json:"id"`
			Note         string `json:"note"`
			NoteableType string `json:"noteable_type"`
			Type         string `json:"type"` // DiffNote, DiscussionNote; "" for a top-level comment
			Discussion   string `json:"discussion_id"`
			Action       string `json:"action"` // create, update; "" before GitLab 15
			Position     *struct {
				NewPath string `json:"new_path"`
			} `json:"position"`
		}
		if err := json.Unmarshal(w.Attrs, &a); err != nil {
			return nil, err
		}
		if a.NoteableType != "MergeRequest" || a.Action == "update" {
			return nil, nil // GitLab lets only the author tick a box in a note: no edits to watch
		}
		w.MR.fill(e)
		e.Kind, e.Action = "pull_request_review_comment", "created"
		if a.Type == "" {
			e.Kind = "issue_comment"
			e.Issue.Number, e.Issue.PullRequest = w.MR.IID, &struct{}{}
		}
		c := &e.Comment
		c.ID, c.Body, c.Discussion, c.User.Login = a.ID, a.Note, a.Discussion, w.User.Username
		if a.Position != nil {
			c.Path = a.Position.NewPath
		}
		c.Ours = w.User.Username == Bot
		if c.Ours || otherBot.MatchString(w.User.Username) {
			c.User.Type = "Bot"
			return e, nil
		}
		if a.Type == "" && !strings.Contains(strings.ToLower(a.Note), "@"+strings.ToLower(Bot)) {
			return e, nil // not for us: no API calls
		}
		project := fmt.Sprintf("/projects/%d", w.Project.ID)
		level, err := access(project, w.User.ID)
		if err != nil {
			return nil, err
		}
		c.AuthorAssociation = association(level)
		if a.Type != "" { // the server answers replies in threads: InReplyTo is the thread's first note
			ns, err := discussion(project, w.MR.IID, a.Discussion)
			if err != nil {
				return nil, err
			}
			if len(ns) > 0 && ns[0].ID != a.ID {
				c.InReplyTo = ns[0].ID
			}
		}
		return e, nil
	}
	return nil, nil
}

type note struct {
	ID       int64  `json:"id"`
	Body     string `json:"body"`
	System   bool   `json:"system"`
	Resolved bool   `json:"resolved"`
	Created  string `json:"created_at"`
	Updated  string `json:"updated_at"` // resolving the thread moves it too: then chat may answer twice, not never
	Author   struct {
		Username string `json:"username"`
	} `json:"author"`
	Position *struct {
		NewPath string `json:"new_path"`
		NewLine int    `json:"new_line"`
	} `json:"position"`
}

func (n note) forge() forge.Note {
	return forge.Note{ID: n.ID, Login: n.Author.Username, Body: n.Body, Ours: n.Author.Username == Bot, Edited: n.Updated != n.Created}
}

func discussion(project string, iid int, id string) ([]note, error) {
	var d struct {
		Notes []note `json:"notes"`
	}
	err := call("GET", fmt.Sprintf("%s/merge_requests/%d/discussions/%s", project, iid, url.PathEscape(id)), nil, &d)
	return d.Notes, err
}

var hunkStart = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)`)

type old struct {
	path string
	line int // 0 for an added line
}

// olds maps each "path:line" of the diff's new side to the old path and, for an unchanged line, its old line: a
// GitLab position needs both on an unchanged line.
func olds(raw string) map[string]old {
	m := map[string]old{}
	from, file, o, n, hunk := "", "", 0, 0, false
	for _, l := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			from, file, hunk = "", "", false
		case !hunk && strings.HasPrefix(l, "--- a/"):
			from = l[6:]
		case !hunk && strings.HasPrefix(l, "+++ b/"):
			file = l[6:]
		case strings.HasPrefix(l, "@@"):
			hunk = true
			if s := hunkStart.FindStringSubmatch(l); s != nil {
				o, _ = strconv.Atoi(s[1])
				n, _ = strconv.Atoi(s[2])
			}
		case !hunk || file == "":
		case strings.HasPrefix(l, "+"):
			m[fmt.Sprintf("%s:%d", file, n)] = old{cmp.Or(from, file), 0}
			n++
		case strings.HasPrefix(l, "-"):
			o++
		case strings.HasPrefix(l, " "):
			m[fmt.Sprintf("%s:%d", file, n)] = old{cmp.Or(from, file), o}
			o++
			n++
		}
	}
	return m
}

// inert escapes each line that would run as a quick action (/merge, /approve) with the bot's rights: a finding's
// text comes from the PR's untrusted code. Every such line, fenced or not: guessing what GitLab's markdown parser
// calls a code block is how one slips through. discuss keeps them out of a suggestion, which must stay as written.
func inert(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if command(l) {
			lines[i] = `\` + l
		}
	}
	return strings.Join(lines, "\n")
}

// command is a line GitLab could read as a quick action: "/" and a name at its start, once it drops "\r".
// "//" and "/*" start no command, they are code comments.
func command(l string) bool {
	l = strings.ReplaceAll(l, "\r", "")
	return strings.HasPrefix(l, "/") && !strings.HasPrefix(l, "//") && !strings.HasPrefix(l, "/*")
}
