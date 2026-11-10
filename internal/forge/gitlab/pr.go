package gitlab

import (
	"cmp"
	"diffharbor/internal/diff"
	"diffharbor/internal/forge"
	"diffharbor/internal/knowledge"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
	"diffharbor/internal/review"
	"encoding/json"
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
	// findings is how this review posts (Findings, Configure), checked once batch looked at the drafts
	findings string
	checked  bool
	answers  []answer // the answers Resolve drafted, published with the review
}

// Open needs no token per repo: GitLab's is one bot user's.
func (*Hub) Open(e *forge.Event) (forge.PR, error) {
	api := fmt.Sprintf("/projects/%d", e.Repository.ID)
	return &pr{e: e, api: api, mr: fmt.Sprintf("%s/merge_requests/%d", api, e.PullRequest.Number), findings: Findings}, nil
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
	// the project as it is now, not as the event was queued (forge.Event.Private); unknown isn't private
	var proj struct{ Visibility string }
	if err := call("GET", p.api, nil, &proj); err != nil {
		log.Println("visibility:", err)
	}
	p.e.PullRequest.Base.Repo.Visibility = cmp.Or(proj.Visibility, "unknown")
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

// Wiki lists the markdown pages of the project's wiki with their content, the first 100 (the API has no page).
func (p *pr) Wiki() ([]knowledge.File, error) {
	var ws []struct{ Slug, Title, Content, Format string }
	if err := call("GET", p.api+"/wikis?with_content=1", nil, &ws); err != nil {
		return nil, err
	}
	web, _, _ := strings.Cut(p.e.PullRequest.HTMLURL, "/-/merge_requests/")
	var fs []knowledge.File
	for _, w := range ws[:min(len(ws), 100)] {
		if w.Format == "markdown" {
			fs = append(fs, knowledge.File{ID: "wiki/" + w.Slug, Title: "wiki: " + w.Title, URL: web + "/-/wikis/" + w.Slug, Text: w.Content, GitLab: true})
		}
	}
	return fs, nil
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
	b := map[string]string{"body": inert(body)}
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
		var rs []forge.Note
		for _, r := range d.Notes[1:] {
			if !r.System {
				rs = append(rs, r.forge())
			}
		}
		ts = append(ts, forge.Thread{
			ID: d.ID, Comment: n.ID, Resolved: n.Resolved, Path: n.Position.NewPath,
			Line: n.Position.NewLine, Original: n.Position.NewLine, Body: n.Body, Replies: forge.Answers(rs), Notes: rs,
			Addressed: forge.Addressed(rs),
		})
	}
	return ts, err
}

// Voted reads the award emoji of each of our finding notes, but of an addressed one: that outcome is set.
func (p *pr) Voted() ([]forge.Thread, error) {
	ts, err := p.Threads()
	for i, t := range ts {
		if t.Addressed {
			continue
		}
		as, cut, err := paged[struct {
			Name string `json:"name"`
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		}](fmt.Sprintf("%s/notes/%d/award_emoji?per_page=100", p.mr, t.Comment))
		if err == nil && cut { // a partial list mustn't count
			err = fmt.Errorf("note %d: award emoji cut off at %d", t.Comment, len(as))
		}
		if err != nil {
			return nil, err
		}
		for _, a := range as {
			switch a.Name {
			case "thumbsup":
				ts[i].Up = append(ts[i].Up, a.User.Username)
			case "thumbsdown":
				ts[i].Down = append(ts[i].Down, a.User.Username)
			}
		}
	}
	return ts, err
}

func (p *pr) resolve(id string) error {
	return call("PUT", fmt.Sprintf("%s/discussions/%s?resolved=true", p.mr, url.PathEscape(id)), nil, nil)
}

// answer is a thread's answer waiting in a draft.
type answer struct{ id, body string }

// Resolve answers the thread and resolves it. In a review the answer is a draft that resolves it once published.
func (p *pr) Resolve(t forge.Thread, sha string) {
	body := forge.AddressedIn(sha)
	if p.batch() {
		err := call("POST", p.mr+"/draft_notes", map[string]any{"note": body, "in_reply_to_discussion_id": t.ID, "resolve_discussion": true}, nil)
		if err == nil {
			p.answers = append(p.answers, answer{t.ID, body})
			return
		}
		// answered on its own: a timeout that drafted it anyway answers twice rather than not at all
		log.Println(p.e.Key(), "draft answer:", err)
	}
	p.answer(answer{t.ID, body})
}

// Reopen answers the thread with the finding a commit brought back and reopens it, on its own: a draft can only
// resolve.
func (p *pr) Reopen(t forge.Thread, sha string, c forge.Finding) error {
	body := forge.BackIn(sha, c, func(c forge.Finding) string { return inert(forge.Format(c, "")) })
	if err := call("POST", fmt.Sprintf("%s/discussions/%s/notes", p.mr, url.PathEscape(t.ID)), map[string]string{"body": body}, nil); err != nil {
		return err
	}
	if err := call("PUT", fmt.Sprintf("%s/discussions/%s?resolved=false", p.mr, url.PathEscape(t.ID)), nil, nil); err != nil {
		log.Println(p.e.Key(), "reopen:", err) // answered: the finding is there, in a thread still resolved
	}
	return nil
}

// answer answers the thread on its own and resolves it.
func (p *pr) answer(a answer) {
	err := call("POST", fmt.Sprintf("%s/discussions/%s/notes", p.mr, url.PathEscape(a.id)), map[string]string{"body": a.body}, nil)
	if err == nil {
		err = p.resolve(a.id)
	}
	if err != nil {
		log.Println("resolve:", err)
	}
}

// Findings is GITLAB_FINDINGS, how a review posts unless review.json's gitlab_findings says: "review" as one
// review, one email, a suggestion's range GitLab refuses goes into the summary as code; "discussions" each finding
// and each answer on its own, an email each, a refused range retried without it.
var Findings = "review"

// Configure takes review.json's gitlab_findings over the server's.
func (p *pr) Configure(cfg review.Settings) { p.findings = cmp.Or(cfg.GitLabFindings, Findings) }

// batch reports whether this review posts as one review. First it drops the bot's drafts left over (a run that
// died, ones GitLab kept for not placing them), which publishing would send along; a person's drafts (a personal
// token), one it can't drop or no draft API make the review post discussions.
func (p *pr) batch() bool {
	if p.findings == "review" && !p.checked {
		p.checked = true
		if !p.dropDrafts() {
			log.Println(p.e.Key(), "drafts in the way: findings as discussions")
			p.findings = "discussions"
		}
	}
	return p.findings == "review"
}

// dropDrafts deletes the bot's drafts; false if it can't, or a person's are there, publishing would send them.
func (p *pr) dropDrafts() bool {
	var ds []struct {
		ID   int64  `json:"id"`
		Note string `json:"note"`
	}
	b, next, err := do("GET", p.mr+"/draft_notes?per_page=100", nil)
	if err == nil {
		err = json.Unmarshal(b, &ds)
	}
	if err != nil {
		log.Println(p.e.Key(), "drafts:", err)
		return false
	}
	if next != "" { // GitLab lists drafts on one page; past a page, one unseen could be a person's
		return false
	}
	ok := true
	for _, d := range ds {
		if !strings.HasSuffix(strings.TrimSpace(d.Note), forge.Mark) { // a person's quoting a note of ours stays
			ok = false
		} else if err := call("DELETE", fmt.Sprintf("%s/draft_notes/%d", p.mr, d.ID), nil, nil); err != nil {
			log.Println(p.e.Key(), "drafts:", err)
			ok = false
		}
	}
	return ok
}

// Publish sends the answers Resolve drafted when no Post follows.
func (p *pr) Publish() {
	if len(p.answers) > 0 {
		p.bulkPublish()
	}
}

// Post sends the summary and the findings on diff lines, with the answers Resolve drafted. A position GitLab
// refuses (outdated diff, a line it doesn't show) moves the finding into the summary.
func (p *pr) Post(sha string, r review.Result, threads []forge.Thread, raw, footer string) (int, error) {
	inline, rest, folded := forge.Split(r, threads, diff.NewLines(raw))
	if p.refs[2] != sha { // GitLab hasn't built the diff of this push yet: no positions on it
		log.Println(p.e.Key(), "diff of", sha[:7], "not ready, findings go in the note")
		rest, inline = append(rest, inline...), nil
	}
	if !p.batch() {
		return p.discussions(inline, rest, folded, raw, footer)
	}
	return p.publish(inline, rest, folded, threads, raw, footer)
}

// discussions sends the summary, then a discussion per finding: the summary heads them, as a GitHub review does.
func (p *pr) discussions(inline, rest, folded []forge.Finding, raw, footer string) (_ int, err error) {
	id, err := p.Note(0, forge.Summary(inline, rest, folded, footer))
	if err != nil {
		return 0, err
	}
	lines := olds(raw)
	var posted []forge.Finding
	defer func() { // a refused or failed discussion: the note says what was posted
		if len(posted) < len(inline) {
			if _, e := p.Note(id, forge.Summary(posted, rest, folded, footer)); err == nil {
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
			rest = append(rest, c)
		case err != nil: // the rest isn't tried: the note keeps them
			rest = append(rest, plain(inline[i:])...)
			return len(posted), err
		default:
			posted = append(posted, c)
		}
	}
	return len(posted) + len(rest), nil
}

// publish publishes the findings as one review with the summary and the answers as drafts, one email: GitLab's
// own summary (bulk_publish's note) is a note of its own, an email more. GitLab checks a draft's position only on
// publishing and drops one it can't place: a finding with no discussion afterwards goes into the summary.
func (p *pr) publish(inline, rest, folded []forge.Finding, threads []forge.Thread, raw, footer string) (int, error) {
	// the summary's draft first: GitLab publishes drafts in the order they were made, the summary heads the findings
	summary := forge.Summary(inline, rest, folded, footer)
	var d struct {
		ID int64 `json:"id"`
	}
	ok := true                         // the summary is drafted as it goes out
	bodies := []string{inert(summary)} // what it may go out as
	if err := call("POST", p.mr+"/draft_notes", map[string]string{"note": bodies[0]}, &d); err != nil {
		log.Println(p.e.Key(), "draft summary:", err) // a note of its own then
		ok = false
	}
	lines := olds(raw)
	var drafted []forge.Finding
	var keys []string
	for i, c := range inline {
		line, body, pos := p.place(c, lines)
		err := call("POST", p.mr+"/draft_notes", map[string]any{"note": body, "position": pos}, nil)
		if rejected(err) {
			c.Start = 0
			rest = append(rest, c)
			continue
		}
		drafted, keys = append(drafted, c), append(keys, key(c.Path, line, body))
		if err != nil { // drafted or not (a timeout): its discussion tells. The rest isn't tried: the summary keeps them
			log.Println(p.e.Key(), "draft:", err)
			rest = append(rest, plain(inline[i+1:])...)
			break
		}
	}
	if s := forge.Summary(drafted, rest, folded, footer); ok && s != summary { // a refused finding: the summary has it
		bodies = append(bodies, inert(s)) // a failed update may have gone through (a timeout)
		if err := call("PUT", fmt.Sprintf("%s/draft_notes/%d", p.mr, d.ID), map[string]string{"note": bodies[1]}, nil); err != nil {
			log.Println(p.e.Key(), "draft summary:", err) // published as either, edited after
			ok = false
		}
	}
	before, beforeErr := p.newest()
	pubErr := p.bulkPublish()
	posted, lost := p.published(drafted, keys, threads)
	if pubErr == nil && ok && len(lost) == 0 {
		return len(posted) + len(rest), nil
	}
	// publishing failed, the summary isn't right, or GitLab dropped some: the summary says what was posted
	var id int64
	err := beforeErr
	if err == nil {
		id, err = p.ours(before, bodies...)
	}
	if err != nil {
		log.Println(p.e.Key(), "notes:", err) // a second summary rather than an older one edited
	}
	rest = append(rest, plain(lost)...)
	_, err = p.Note(id, forge.Summary(posted, rest, folded, footer))
	return len(posted) + len(rest), err
}

// bulkPublish publishes the bot's drafts as one review, which marks the bot reviewed when it is a reviewer. Failing,
// it answers the threads on their own: a timeout that published them anyway answers twice rather than not at all.
func (p *pr) bulkPublish() error {
	answers := p.answers
	p.answers = nil
	err := call("POST", p.mr+"/draft_notes/bulk_publish", map[string]string{"reviewer_state": "reviewed"}, nil)
	if err != nil {
		log.Println(p.e.Key(), "publish:", err)
		for _, a := range answers {
			p.answer(a)
		}
	}
	return err
}

// published splits the drafted findings by whether GitLab made a discussion of them: a thread of ours, not one of
// before, on their line with their body. Unread threads count all as lost: the summary repeats them.
func (p *pr) published(drafted []forge.Finding, keys []string, old []forge.Thread) (posted, lost []forge.Finding) {
	if len(drafted) == 0 {
		return nil, nil
	}
	ts, err := p.Threads()
	if err != nil {
		log.Println(p.e.Key(), "threads:", err)
		return nil, drafted
	}
	known := map[string]bool{}
	for _, t := range old {
		known[t.ID] = true
	}
	got := map[string]int{}
	for _, t := range ts {
		if !known[t.ID] {
			got[key(t.Path, t.Line, t.Body)]++
		}
	}
	for i, c := range drafted {
		if got[keys[i]] > 0 {
			got[keys[i]]--
			posted = append(posted, c)
		} else {
			lost = append(lost, c)
		}
	}
	return posted, lost
}

// key is a discussion's place and body, to tell a published draft by.
func key(path string, line int, body string) string {
	return fmt.Sprintf("%s:%d:%s", path, line, strings.TrimSpace(body))
}

// plain is the findings without their suggestion's range: the note shows the fix as code.
func plain(fs []forge.Finding) []forge.Finding {
	out := slices.Clone(fs)
	for i := range out {
		out[i].Start = 0
	}
	return out
}

// newest is the id of the MR's newest note, 0 if there is none.
func (p *pr) newest() (int64, error) {
	var ns []note
	if err := call("GET", p.mr+"/notes?sort=desc&order_by=created_at&per_page=1", nil, &ns); err != nil || len(ns) == 0 {
		return 0, err
	}
	return ns[0].ID, nil
}

// ours is the id of our note with one of these bodies after note after, 0 if there is none.
func (p *pr) ours(after int64, bodies ...string) (int64, error) {
	for page := 1; page <= 10; page++ { // a review's notes: up to 80 findings and their answers
		var ns []note
		if err := call("GET", fmt.Sprintf("%s/notes?sort=desc&order_by=created_at&per_page=100&page=%d", p.mr, page), nil, &ns); err != nil {
			return 0, err
		}
		for _, n := range ns {
			if n.ID <= after {
				return 0, nil
			}
			if n.forge().Ours && slices.ContainsFunc(bodies, func(b string) bool { return strings.TrimSpace(b) == strings.TrimSpace(n.Body) }) {
				return n.ID, nil
			}
		}
		if len(ns) < 100 {
			break
		}
	}
	return 0, nil
}

func rejected(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.status >= 400 && ae.status < 500
}

// place is the discussion of one finding: its line, body and position, on the suggestion's last line, which it
// replaces up from there.
func (p *pr) place(c forge.Finding, lines map[string]old) (int, string, map[string]any) {
	// A fix with a line inert would escape goes as plain code: a suggestion is committed as written.
	if slices.ContainsFunc(strings.Split(c.Fix, "\n"), command) {
		c.Start = 0
	}
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
	return line, inert(forge.Format(c, tag) + "\n\n" + forge.Mark), pos
}

// discuss starts the discussion of one finding.
func (p *pr) discuss(c forge.Finding, lines map[string]old) error {
	_, body, pos := p.place(c, lines)
	return call("POST", p.mr+"/discussions", map[string]any{"body": body, "position": pos}, nil)
}

func (p *pr) Discussion() (forge.Discussion, error) {
	var d forge.Discussion
	ns, err := discussion(p.api, p.e.PullRequest.Number, p.e.Comment.Discussion)
	if err != nil || len(ns) == 0 {
		return d, err
	}
	first := ns[0]
	d.Ours, d.Resolved = first.forge().Ours && strings.Contains(first.Body, forge.Mark), first.Resolved
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
	b := map[string]string{"body": inert(body)}
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
