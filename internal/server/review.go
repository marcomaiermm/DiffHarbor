// The review job: incremental diff, walkthrough comment, review with inline findings, resolving fixed threads.

package server

import (
	"cmp"
	"context"
	"diffharbor/internal/agent"
	"diffharbor/internal/checks"
	"diffharbor/internal/diff"
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
	"diffharbor/internal/review"
	"diffharbor/internal/text"
	"diffharbor/internal/ticket"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

//nolint:gocognit,gocyclo // one job step by step, each step a few lines
func reviewPR(ctx context.Context, e *forge.Event) (err error) {
	key, queued := e.Key(), e.PullRequest.Head.SHA
	f := forges[e.Forge]
	latest.Lock()
	current := latest.m[key] == queued
	latest.Unlock()
	if !current {
		return nil // a newer push is queued
	}
	defer func() {
		latest.Lock()
		if latest.m[key] == queued {
			delete(latest.m, key)
		}
		latest.Unlock()
	}()

	p, err := f.Open(e)
	if err != nil {
		return err
	}
	// A redelivered or long-queued event can be stale: review the PR as it is now.
	if err := p.Refresh(); err != nil {
		return err
	}
	if e.PullRequest.State != "open" || e.PullRequest.Draft {
		return nil
	}
	pr, sha := e.PullRequest, e.PullRequest.Head.SHA
	s, err := forge.LoadSticky(p)
	if err != nil {
		return err
	}
	manual := e.Action == "manual" || e.Action == "large"
	if s.State.SHA == sha && !manual {
		return nil // reviewed already, e.g. a redelivered event; `@app review` forces one
	}
	// Dependency bumps and release PRs get no review unless asked for. Green, so a required check passes.
	// A self-hosted Renovate on a user token is a User, so its PRs get reviewed.
	if pr.User.Type == "Bot" && !manual {
		s.Write(fmt.Sprintf("⏭️ **Review skipped:** bot author. Comment `@%s review` for one.", f.Bot()), "")
		p.Status(sha, "success", "Skipped: bot author")
		return nil
	}
	s.Write(fmt.Sprintf("⏳ **Reviewing** `%s` …", sha[:7]), "")
	desc, state := "Review completed", "success"
	p.Status(sha, "pending", "Review in progress")
	defer func() {
		switch {
		case err != nil && ctx.Err() != nil: // PR closed or a newer push: that one gets the review
			log.Println(key, sha[:7], "stopped")
			s.Write(fmt.Sprintf("⏹️ Review of `%s` stopped.", sha[:7]), "")
			p.Status(sha, "error", "Review stopped")
			err = nil
		case err != nil:
			s.Write(fmt.Sprintf("⚠️ Review of `%s` failed. Push again or comment `@%s review`.", sha[:7], f.Bot()), "")
			p.Status(sha, "error", "Review failed")
		default:
			p.Status(sha, state, desc)
		}
	}()
	footer := func(what string) string {
		return fmt.Sprintf("%s · `@%s <question>` to ask, `@%s review` for a full review", what, f.Bot(), f.Bot())
	}

	dir, err := proc.Workspace()
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	read, err := p.ReadToken()
	if err != nil {
		return err
	}
	raw, err := review.Checkout(ctx, filepath.Join(dir, "repo"), e.Repository.CloneURL, sha, "refs/heads/"+pr.Base.Ref, read)
	if err != nil {
		return err
	}
	prFiles := diff.Paths(raw)
	// On push only files whose changed lines differ from the last review: a rebase changes none.
	patches := diff.PatchIDs(raw)
	var only map[string]bool
	if e.Action == "synchronize" && s.State.Patches != nil {
		only = map[string]bool{}
		for f, id := range patches {
			if s.State.Patches[f] != id {
				only[f] = true
			}
		}
	}
	cfg, cfgErr := review.LoadSettings(p.File(pr.Base.SHA, "review.json"))
	outline := diff.Analyze(raw)
	s.Outline = &outline
	guide := reportURL != "" && cfg.Reports(len(prFiles)) // a large PR: a reading order and a report
	// A diff past diff.Max: in parts once someone asked for them on this PR, else code first up to diff.Max.
	s.State.Large = s.State.Large || e.Action == "large"
	parts, omitted := diff.Filter(raw, only, cfg.IgnoreRe, cfg.Parts(s.State.Large))
	d := strings.Join(parts, "")
	if strings.TrimSpace(d) == "" {
		log.Println(key, "nothing to review")
		desc = "Nothing to review"
		s.State = s.State.Update(review.Result{}, sha, prFiles, false)
		s.State.Patches = reviewedPatches(s.State.Patches, patches, nil)
		s.Write("", footer(fmt.Sprintf("Reviewed up to `%s`, nothing to review", sha[:7])))
		return nil
	}

	threads, err := p.Threads()
	if err != nil {
		return err
	}
	// Earlier findings on the files under review: the model must not repeat them and may report open ones fixed.
	// Closed ones were fixed or declined; without them a declined finding comes back on the next push.
	files := diff.Paths(d)
	var open []forge.Thread
	var prior, closed []review.Comment
	for _, t := range threads {
		if !slices.Contains(files, t.Path) {
			continue
		}
		c := review.Comment{Path: t.Path, Line: cmp.Or(t.Line, t.Original), Body: text.Truncate(strings.ReplaceAll(t.Body, forge.Mark, ""), 600)}
		if t.Resolved {
			closed = append(closed, c)
		} else {
			open = append(open, t)
			prior = append(prior, c)
		}
	}

	var scripts []checks.Script
	for i, path := range cfg.CheckPaths() {
		if c := p.File(pr.Base.SHA, path); c != "" {
			f := filepath.Join(dir, fmt.Sprintf("checks-%d.sh", i))
			if err := proc.WriteFile(f, []byte(c)); err != nil {
				return err
			}
			scripts = append(scripts, checks.Script{Name: path, File: f})
		}
	}
	rules := text.Truncate(p.File(pr.Base.SHA, cfg.RulePaths()...), 8000) + p.NestedRules(pr.Base.SHA, files)
	p.Skills(pr.Base.SHA, cfg.SkillPaths(), filepath.Join(dir, "skills"))

	started := time.Now()
	var tid, tk string
	if pr.Head.Repo.ID == e.Repository.ID { // a fork's author could name any ticket and read it in the review
		tid, tk = ticket.Lookup(ctx, cfg.Tickets, pr.Title, pr.Head.Ref, pr.Body)
	}
	r, stats, err := review.Analyze(ctx, review.Input{
		Agent: agent.Pi{}, Dir: dir, Title: pr.Title, Body: pr.Body, Ticket: tk, Parts: parts,
		Trusted: review.TrustedContext(rules, readLearnings(e.Owner(), e.Repo())), Checks: scripts, Prior: prior, Closed: closed, Settings: cfg,
		Guide: guide && only == nil, // an incremental review sees a few files: the last full one's reading order stays
	})
	if err != nil {
		return err
	}
	for _, i := range r.Fixed {
		p.Resolve(open[i], sha)
	}

	kind := "full"
	if only != nil {
		kind = "incremental"
	}
	if agent.FallbackUntil.Load() > started.UnixMilli() { // some passes may have run on the fallback
		rm, _ := cfg.Models()
		kind += " · fallback " + agent.FallbackFor(rm, "review")
	}
	if omitted > 0 { // the rest only by name: findings cover part of the PR
		inline := len(diff.Paths(d))
		kind += fmt.Sprintf(" · ⚠️ inline %d of %d files", inline, inline+omitted)
		if all, _ := diff.Filter(raw, only, cfg.IgnoreRe, cfg.Parts(true)); cfg.Asks() && len(all) > len(parts) {
			s.Ask = forge.AskLarge(len(diff.Paths(strings.Join(all, ""))), inline+omitted, len(all), e.Forge == "github", f.Bot())
		}
	}
	if tid != "" {
		kind += " · ticket " + tid
	}
	if cfgErr != nil {
		kind += " · ⚠️ review.json ignored: " + text.OneLine(cfgErr.Error(), 120)
	}
	n := 0
	if only == nil || len(r.Comments) > 0 { // a full review always posts, so the bot shows up under Reviewers
		// Lines of the whole PR diff take inline comments, also in files this incremental review skipped: only
		// a thread can be resolved once fixed.
		if n, err = p.Post(sha, r, threads, raw,
			fmt.Sprintf("%s · %s · %s · %ds", stats, kind, sha[:7], int(time.Since(started).Round(time.Second).Seconds()))); err != nil {
			return err
		}
	}
	desc = fmt.Sprintf("%d findings", n)
	if len(r.Fixed) > 0 {
		desc += fmt.Sprintf(", %d fixed", len(r.Fixed))
	}
	if len(r.Failed) > 0 { // their part failed: not green, the next push or `@app review` reviews them
		desc, state = fmt.Sprintf("%s, %d files not reviewed", desc, len(r.Failed)), "error"
	}
	log.Printf("%s %s %s %s: %s", key, sha[:7], kind, stats, desc)
	s.State = s.State.Update(r, sha, prFiles, only == nil)
	s.State.Patches = reviewedPatches(s.State.Patches, patches, slices.DeleteFunc(diff.Paths(d), func(f string) bool { return slices.Contains(r.Failed, f) }))
	if guide {
		repo := filepath.Join(dir, "repo")
		st := s.State
		d := report.Data{
			Repo: e.Repository.FullName, Number: pr.Number, Title: pr.Title, URL: pr.HTMLURL, SHA: pr.Head.SHA,
			Summary: st.Summary, Diagram: st.Diagram, Effort: st.Effort, Decisions: st.Decisions, Story: st.Story, Flow: st.Flow,
			Walkthrough: st.Files, Commits: review.Commits(ctx, repo), Outline: outline, Findings: openFindings(threads, open, r),
			Diff: raw, Routine: r.Routine, Symbols: report.Symbols(repo, raw), Themes: cfg.SyntaxTheme,
			Others: others(p, e.Key(), raw), Behind: review.Behind(ctx, repo),
			Conflicts: pr.Mergeable != nil && !*pr.Mergeable, Forge: p.Links(),
		}
		// what the new code is for, where no doc comment or commit says: one call, the provider's small model
		if keys, texts := report.Asks(d); len(keys) > 0 {
			rm, vm := cfg.Models()
			d.Whys = map[string]string{}
			for i, w := range review.Explain(ctx, agent.Pi{}, dir, cmp.Or(agent.VerifyFor(rm), vm, rm), pr.Title, texts) {
				d.Whys[keys[i]] = w
			}
		}
		s.State.Report = writeReport(e, d)
	}
	banner := ""
	if n == 0 {
		banner = "No actionable comments were generated in the recent review."
	}
	s.Write(banner, footer(fmt.Sprintf("Reviewed up to `%s` · %s", sha[:7], desc)))
	return nil
}

// reportURL is where the server serves reports (REPORT_URL, behind SSO); "" = no reports.
var reportURL = strings.TrimSuffix(os.Getenv("REPORT_URL"), "/")

// maxVersions is the reports a PR keeps, one per reviewed head: the newest.
const maxVersions = 10

// reportPath is a PR's reports under /r/ and the reports folder: its forge's host, the repository, the number.
// A version is {sha}.html in it, versions.json lists them newest first.
func reportPath(e *forge.Event) string {
	host, _, _ := strings.Cut(strings.TrimPrefix(e.PullRequest.HTMLURL, "https://"), "/")
	return cmp.Or(host, "github.com") + "/" + e.Repository.FullName + "/" + strconv.Itoa(e.PullRequest.Number)
}

// reportDir is where a PR's reports live.
func reportDir(e *forge.Event) string {
	return filepath.Join(proc.DataDir, "reports", filepath.FromSlash(reportPath(e)))
}

// versions is a PR's reports, newest first, by its reportPath; none when it has no readable list.
func versions(path string) []report.Version {
	var vs []report.Version
	if b, err := fs.ReadFile(reports, path+"/versions.json"); err == nil { // os.DirFS: nothing outside the reports folder
		_ = json.Unmarshal(b, &vs)
	}
	return vs
}

// writeReport renders the PR's report as the version of its head, against the newest of another head, and returns
// its URL; "" when that fails, the review stands without it. d.Routine is this review's: an incremental one has
// none, and the last full one's line numbers may be stale.
// ponytail: two reviews of a PR finishing at once can drop one's entry from versions.json; a lock per PR if it shows.
func writeReport(e *forge.Event, d report.Data) string {
	dir := reportDir(e)
	vs := slices.DeleteFunc(versions(reportPath(e)), func(v report.Version) bool { return v.SHA == d.SHA })
	if len(vs) > 0 {
		d.Prev = &vs[0]
	}
	b, now, err := report.Render(d)
	now.At = time.Now()
	vs = slices.Insert(vs, 0, now)
	keep := vs[:min(len(vs), maxVersions)]
	if err == nil {
		err = os.MkdirAll(dir, 0o700)
	}
	if err == nil {
		err = atomicWrite(filepath.Join(dir, d.SHA+".html"), b)
	}
	if err == nil { // after the page: a listed version always has one
		b, err = json.Marshal(keep)
	}
	if err == nil {
		err = atomicWrite(filepath.Join(dir, "versions.json"), b)
	}
	if err != nil {
		log.Println(e.Key(), "report:", err)
		return ""
	}
	for _, v := range vs[len(keep):] {
		_ = os.Remove(filepath.Join(dir, v.SHA+".html"))
	}
	return reportURL + "/r/" + reportPath(e)
}

// atomicWrite writes f whole or not at all: a reader never gets half of it.
func atomicWrite(f string, b []byte) error {
	if err := os.WriteFile(f+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(f+".tmp", f)
}

// maxClash is the PRs others fetches the diff of, those sharing the most files first.
const maxClash = 8

// others is the open PRs that change files this one does, the most shared first; for the first maxClash also where
// both change the same lines. nil when the forge fails: the report goes without.
// ponytail: a PR's first 100 files only; past that a shared file can go unseen.
func others(p forge.PR, key, raw string) []report.Other {
	prs, err := p.Others()
	if err != nil {
		log.Println(key, "other PRs:", err)
		return nil
	}
	ours := map[string][][2]int{}
	read := func(p, f string) { ours[p] = diff.OldRanges(f) }
	diff.EachFile(raw, read)
	diff.EachDeleted(raw, read)
	var out []report.Other
	for _, o := range prs {
		o.Files = slices.DeleteFunc(o.Files, func(f string) bool { _, ok := ours[f]; return !ok })
		if len(o.Files) > 0 {
			out = append(out, o)
		}
	}
	slices.SortStableFunc(out, func(a, b report.Other) int { return len(b.Files) - len(a.Files) })
	for i := range out[:min(len(out), maxClash)] {
		ps, err := p.Patches(out[i].Number)
		if err != nil {
			log.Println(key, "other PRs:", err)
			break
		}
		for _, f := range out[i].Files {
			if diff.Overlap(ours[f], diff.OldRanges(ps[f])) {
				out[i].Clash = append(out[i].Clash, f)
			}
		}
	}
	return out
}

// openFindings is what the report shows inline: the threads still open after this review, and its new findings.
func openFindings(threads, open []forge.Thread, r review.Result) []review.Comment {
	fixed := map[string]bool{}
	for _, i := range r.Fixed {
		fixed[open[i].ID] = true
	}
	var cs []review.Comment
	for _, t := range threads {
		if !t.Resolved && !fixed[t.ID] {
			cs = append(cs, review.Comment{Path: t.Path, Line: t.Line, Body: forge.Clean(t.Body)})
		}
	}
	return append(cs, r.Comments...)
}

// reviewedPatches is the patch IDs to remember: now's for the files reviewed, the last reviewed one for a file left out
// (too large, a failed part, nothing reviewable), so the next push reviews it unless it is still unchanged.
func reviewedPatches(last, now map[string]string, reviewed []string) map[string]string {
	ids := map[string]string{}
	for f, id := range last {
		if _, ok := now[f]; ok {
			ids[f] = id
		}
	}
	for _, f := range reviewed {
		ids[f] = now[f]
	}
	return ids
}
