package gitlab

import (
	"cmp"
	"diffharbor/internal/diff"
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
	"diffharbor/internal/review"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
)

// pr is a merge request; the bot's token reaches every project its user is a member of.
type pr struct {
	e    *forge.Event
	api  string // the project, /projects/<id>
	mr   string // the MR, /projects/<id>/merge_requests/<iid>
	refs [3]string
	// patches are the diffs Others read, by MR, for Patches
	patches map[int]map[string]string
}

// Open needs no token per repo: GitLab's is one bot user's.
func (*Hub) Open(e *forge.Event) (forge.PR, error) {
	api := fmt.Sprintf("/projects/%d", e.Repository.ID)
	return &pr{e: e, api: api, mr: fmt.Sprintf("%s/merge_requests/%d", api, e.PullRequest.Number)}, nil
}

// Refresh also learns whether the author is a bot (Renovate, a project token) and the diff's base for positions.
func (p *pr) Refresh() error {
	var m mr
	if err := call("GET", p.mr, nil, &m); err != nil {
		return err
	}
	m.fill(p.e)
	if m.DiffRefs != nil {
		p.refs = [3]string{m.DiffRefs.Base, m.DiffRefs.Start, m.DiffRefs.Head}
	}
	var u struct {
		Bot bool `json:"bot"`
	}
	if err := call("GET", fmt.Sprintf("/users/%d", m.Author.ID), nil, &u); err != nil {
		return err
	}
	p.e.PullRequest.User.Type = ""
	if u.Bot {
		p.e.PullRequest.User.Type = "Bot"
	}
	return nil
}

var states = map[string]string{"pending": "running", "success": "success", "error": "failed"}

func (p *pr) Status(sha, state, desc string) {
	if err := call("POST", p.api+"/statuses/"+sha, map[string]string{"state": states[state], "name": "diffharbor", "description": desc}, nil); err != nil {
		log.Println("status:", err)
	}
}

func (p *pr) ReadToken() (string, error) { return cmp.Or(readToken, token), nil }

func (p *pr) File(ref string, names ...string) string {
	for _, n := range names {
		if s, err := raw(p.api, ref, n); err == nil {
			return s
		}
	}
	return ""
}

// NestedRules lists each dir above files, at most 40 of them: GitLab has no tree of the whole repo in one call.
func (p *pr) NestedRules(ref string, files []string) string {
	dirs := forge.RuleDirs(files)
	pick := map[string]string{}
	for _, d := range dirs[:min(len(dirs), 40)] {
		es, _ := tree(p.api, ref, d, false)
		for _, e := range es {
			if e.Type == "blob" && (e.Name == "AGENTS.md" || e.Name == "CLAUDE.md" && pick[d] == "") {
				pick[d] = e.Path
			}
		}
	}
	return forge.Rules(pick, func(f string) string { return p.File(ref, f) })
}

// Skills copies the repo's skills to dst/repo. GitLab has no owner-wide .github repo: no shared skills.
func (p *pr) Skills(ref string, dirs []string, dst string) {
	calls := 60 // API calls: bounds symlink loops and big skill folders
	for _, d := range dirs {
		if copyDir(p.api, ref, d, filepath.Join(dst, "repo"), &calls); proc.Exists(filepath.Join(dst, "repo")) {
			return
		}
	}
}

func (p *pr) Notes() ([]forge.Note, error) {
	ns, err := all[note](p.mr + "/notes?sort=asc&order_by=created_at&per_page=100")
	var out []forge.Note
	for _, n := range ns {
		if !n.System { // "added 1 commit", "changed the description"
			out = append(out, n.forge())
		}
	}
	return out, err
}

func (p *pr) Note(id int64, body string) (int64, error) {
	b := map[string]string{"body": body}
	if id != 0 {
		return id, call("PUT", fmt.Sprintf("%s/notes/%d", p.mr, id), b, nil)
	}
	var n note
	err := call("POST", p.mr+"/notes", b, &n)
	return n.ID, err
}

type thread struct {
	ID    string `json:"id"`
	Notes []note `json:"notes"`
}

func (p *pr) Threads() ([]forge.Thread, error) {
	ds, err := all[thread](p.mr + "/discussions?per_page=100")
	var ts []forge.Thread
	for _, d := range ds {
		if len(d.Notes) == 0 || !d.Notes[0].forge().Ours || d.Notes[0].Position == nil {
			continue
		}
		n := d.Notes[0]
		ts = append(ts, forge.Thread{
			ID: d.ID, Comment: n.ID, Resolved: n.Resolved, Path: n.Position.NewPath,
			Line: n.Position.NewLine, Original: n.Position.NewLine, Body: n.Body,
		})
	}
	return ts, err
}

func (p *pr) resolve(id string) error {
	return call("PUT", fmt.Sprintf("%s/discussions/%s?resolved=true", p.mr, url.PathEscape(id)), nil, nil)
}

func (p *pr) Resolve(t forge.Thread, sha string) {
	err := call("POST", fmt.Sprintf("%s/discussions/%s/notes", p.mr, url.PathEscape(t.ID)),
		map[string]string{"body": fmt.Sprintf("✅ Addressed in `%s`\n\n%s", sha[:7], forge.Mark)}, nil)
	if err == nil {
		err = p.resolve(t.ID)
	}
	if err != nil {
		log.Println("resolve:", err)
	}
}

// Post sends the summary note, then a discussion per finding on a diff line: the summary heads them, as a GitHub
// review does. A position GitLab refuses (outdated diff, a line it doesn't show) moves the finding into the note.
func (p *pr) Post(sha string, r review.Result, threads []forge.Thread, raw, footer string) (_ int, err error) {
	inline, rest := forge.Split(r, threads, diff.NewLines(raw))
	if p.refs[2] != sha { // GitLab hasn't built the diff of this push yet: no positions on it
		log.Println(p.e.Key(), "diff of", sha[:7], "not ready, findings go in the note")
		rest, inline = append(rest, inline...), nil
	}
	id, err := p.Note(0, forge.Summary(inline, rest, footer))
	if err != nil {
		return 0, err
	}
	lines := olds(raw)
	var posted []forge.Finding
	defer func() { // a refused or failed discussion: the note says what was posted
		if len(posted) < len(inline) {
			if _, e := p.Note(id, forge.Summary(posted, rest, footer)); err == nil {
				err = e
			}
		}
	}()
	for i, c := range inline {
		err := p.discuss(c, lines)
		if c.Start > 0 && rejected(err) { // the suggestion's range: the finding on its own line, the fix as plain code
			c.Start = 0
			err = p.discuss(c, lines)
		}
		switch {
		case rejected(err):
			c.Start = 0
			rest = append(rest, c)
		case err != nil: // the rest isn't tried: the note keeps them
			for _, c := range inline[i:] {
				c.Start = 0
				rest = append(rest, c)
			}
			return len(posted), err
		default:
			posted = append(posted, c)
		}
	}
	return len(posted) + len(rest), nil
}

func rejected(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.status >= 400 && ae.status < 500
}

// discuss starts the discussion of one finding: on the suggestion's last line, which it replaces up from there.
func (p *pr) discuss(c forge.Finding, lines map[string]old) error {
	line, tag := c.Line, "suggestion"
	if c.Start > 0 {
		line, tag = c.End, fmt.Sprintf("suggestion:-%d+0", c.End-c.Start)
	}
	o := lines[fmt.Sprintf("%s:%d", c.Path, line)]
	pos := map[string]any{
		"position_type": "text", "base_sha": p.refs[0], "start_sha": p.refs[1], "head_sha": p.refs[2],
		"new_path": c.Path, "old_path": cmp.Or(o.path, c.Path), "new_line": line,
	}
	if o.line > 0 {
		pos["old_line"] = o.line
	}
	return call("POST", p.mr+"/discussions", map[string]any{"body": forge.Format(c, tag) + "\n\n" + forge.Mark, "position": pos}, nil)
}

func (p *pr) Discussion() (forge.Discussion, error) {
	var d forge.Discussion
	ns, err := discussion(p.api, p.e.PullRequest.Number, p.e.Comment.Discussion)
	if err != nil || len(ns) == 0 {
		return d, err
	}
	first := ns[0]
	d.Ours = first.forge().Ours && strings.Contains(first.Body, forge.Mark)
	if pos := first.Position; pos != nil {
		d.About = fmt.Sprintf("## `%s` line %d (read the file for context)\n\n", pos.NewPath, pos.NewLine)
	}
	for _, n := range ns {
		if !n.System {
			d.Notes = append(d.Notes, n.forge())
		}
	}
	return d, nil
}

func (p *pr) Reply(body string) error {
	b := map[string]string{"body": body}
	if p.e.Kind == "issue_comment" || p.e.Comment.Discussion == "" {
		return call("POST", p.mr+"/notes", b, nil)
	}
	return call("POST", fmt.Sprintf("%s/discussions/%s/notes", p.mr, url.PathEscape(p.e.Comment.Discussion)), b, nil)
}

func (p *pr) ResolveDiscussion() error { return p.resolve(p.e.Comment.Discussion) }

func (p *pr) React() {
	if err := call("POST", fmt.Sprintf("%s/notes/%d/award_emoji", p.mr, p.e.Comment.ID), map[string]string{"name": "eyes"}, nil); err != nil {
		log.Println(p.e.Key(), "reaction:", err)
	}
}

func (p *pr) CanWrite(login string) (bool, error) {
	var us []struct {
		ID int64 `json:"id"`
	}
	if err := call("GET", "/users?username="+url.QueryEscape(login), nil, &us); err != nil || len(us) == 0 {
		return false, err
	}
	level, err := access(p.api, us[0].ID)
	return level >= 30, err
}

// Others reads each MR's diff for its files, GitLab's list has none; Patches reuses them.
// ponytail: one call per open MR, up to 50 a report; a smaller page if big projects show it.
func (p *pr) Others() ([]report.Other, error) {
	var ms []struct {
		IID    int    `json:"iid"`
		Title  string `json:"title"`
		WebURL string `json:"web_url"`
		Draft  bool   `json:"draft"`
		Author struct {
			Username string `json:"username"`
		} `json:"author"`
	}
	if err := call("GET", p.api+"/merge_requests?state=opened&order_by=updated_at&per_page=50", nil, &ms); err != nil {
		return nil, err
	}
	p.patches = map[int]map[string]string{}
	var out []report.Other
	for _, m := range ms {
		if m.IID == p.e.PullRequest.Number {
			continue
		}
		ps, err := p.diffs(m.IID)
		if err != nil {
			return nil, err
		}
		p.patches[m.IID] = ps
		out = append(out, report.Other{Number: m.IID, Title: m.Title, URL: m.WebURL, Author: m.Author.Username, Draft: m.Draft, Files: slices.Sorted(maps.Keys(ps))})
	}
	return out, nil
}

func (p *pr) Patches(n int) (map[string]string, error) {
	if ps, ok := p.patches[n]; ok {
		return ps, nil
	}
	return p.diffs(n)
}

// diffs is MR iid's diff by file, the first 100 files, as GitHub's files API has it: hunks only.
func (p *pr) diffs(iid int) (map[string]string, error) {
	var ds []struct {
		Path string `json:"new_path"`
		Diff string `json:"diff"`
	}
	if err := call("GET", fmt.Sprintf("%s/merge_requests/%d/diffs?per_page=100", p.api, iid), nil, &ds); err != nil {
		return nil, err
	}
	ps := map[string]string{}
	for _, d := range ds {
		ps[d.Path] = d.Diff
	}
	return ps, nil
}

func (p *pr) Links() report.Forge {
	pr := p.e.PullRequest
	web, _, _ := strings.Cut(pr.HTMLURL, "/-/merge_requests/")
	return report.Forge{
		Name: "GitLab", Ref: "!", Blob: web + "/-/blob/" + pr.Head.SHA + "/", Commit: web + "/-/commit/", PRCommit: pr.HTMLURL + "/diffs?commit_id=",
		Search: fmt.Sprintf("%s/search?scope=blobs&project_id=%d&search=", strings.TrimSuffix(APIURL, "/api/v4"), p.e.Repository.ID),
	}
}
