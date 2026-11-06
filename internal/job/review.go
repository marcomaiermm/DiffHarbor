// Package job runs what a webhook asks for on one PR: a review (incremental diff, walkthrough comment, inline
// findings, resolving fixed threads, the report) or an answer in a thread. It joins the review engine to a forge.
package job

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/rand"
	"diffharbor/internal/agent"
	"diffharbor/internal/checks"
	"diffharbor/internal/diff"
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
	"diffharbor/internal/review"
	"diffharbor/internal/store"
	"diffharbor/internal/text"
	"diffharbor/internal/ticket"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Review reviews the event's PR on f.
//
//nolint:gocognit,gocyclo // one job step by step, each step a few lines
func Review(ctx context.Context, f forge.Forge, e *forge.Event) (err error) {
	key := e.Key()
	p, err := f.Open(e)
	if err != nil {
		return err
	}
	// A redelivered or long-queued event can be stale: review the PR as it is now.
	if err := p.Refresh(); err != nil {
		return err
	}
	if e.PullRequest.State != "open" {
		return nil
	}
	pr, sha := e.PullRequest, e.PullRequest.Head.SHA
	manual := e.Action == "manual" || e.Action == "large"
	cfg, cfgErr := review.LoadSettings(p.File(pr.Base.SHA, "review.json"))
	p.Configure(cfg)
	// A draft's push reads the walkthrough only under opt_in, for whether someone asked.
	if pr.Draft && !cfg.ReviewsDraft(manual, cfg.Drafts == "opt_in") {
		if manual { // drafts: off, asked anyway: say why nothing comes
			return p.Reply("⏭️ Drafts get no review in this repo (`drafts` in review.json). Mark the PR ready for review for one.\n\n" + forge.Mark)
		}
		return nil // ready_for_review brings the review
	}
	s, err := forge.LoadSticky(p)
	if err != nil {
		return err
	}
	if s.State.SHA == sha && !manual {
		return nil // reviewed already, e.g. a redelivered event; `@app review` forces one
	}
	if pr.Draft && !cfg.ReviewsDraft(manual, s.State.Drafts) {
		return nil // opt_in, nobody asked yet
	}
	// Only under opt_in: a later switch to it doesn't start reviewing drafts asked about before.
	s.State.Drafts = s.State.Drafts || pr.Draft && manual && cfg.Drafts == "opt_in" // kept by the walkthrough's first write below
	// Like a bot author: green, and `@app review` still reviews. A broken review.json reviews, its note says why.
	if !manual && cfgErr == nil && !cfg.Reviews(pr.Base.Ref, e.Repository.DefaultBranch) {
		s.Write(fmt.Sprintf("⏭️ **Review skipped:** base branch `%s` is not the default branch or in `base_branches`. Comment `@%s review` for one.", pr.Base.Ref, f.Bot()), "")
		p.Status(sha, "success", "Skipped: base branch")
		return nil
	}
	// Dependency bumps and release PRs get no review unless asked for. Green, so a required check passes.
	// A self-hosted Renovate on a user token is a User, under any name: its branch or its description tells.
	if (pr.User.Type == "Bot" || botBranch.MatchString(pr.Head.Ref) || botBody.MatchString(pr.Body)) && !manual {
		s.Write(fmt.Sprintf("⏭️ **Review skipped:** bot author. Comment `@%s review` for one.", f.Bot()), "")
		p.Status(sha, "success", "Skipped: bot author")
		return nil
	}
	// The lighthouse comes from this server when it has a public URL, else from the docs site: the forge fetches it.
	// GitHub picks a <source> by its theme setting; elsewhere the <img> follows the OS's color scheme.
	img := cmp.Or(ReportURL, "https://marcomaiermm.github.io/DiffHarbor") + "/reviewing"
	s.Write(fmt.Sprintf(`<picture><source media="(prefers-color-scheme: dark)" srcset="%[1]s-dark.svg">`+
		`<source media="(prefers-color-scheme: light)" srcset="%[1]s-light.svg">`+
		`<img src="%[1]s.svg" width="440" alt="A lighthouse sweeping its beam"></picture><br>⏳ **Reviewing** `+"`%[2]s` …", img, sha[:7]), "")
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

	id, err := forge.RepoID(f, e)
	if err != nil {
		return err
	}
	dir, err := agent.Workspace(id)
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
	outline := diff.Analyze(raw)
	s.Outline = &outline
	guide := ReportURL != "" && cfg.Reports(len(prFiles)) // a large PR: a reading order and a report
	// A diff past diff.Max: in parts once someone asked for them on this PR, else code first up to diff.Max.
	s.State.Large = s.State.Large || e.Action == "large"
	parts, omitted := diff.Filter(raw, filepath.Join(dir, "repo"), only, cfg.Ignored, cfg.Parts(s.State.Large))
	d := strings.Join(parts, "")
	if strings.TrimSpace(d) == "" {
		log.Println(key, "nothing to review")
		desc = "Nothing to review"
		s.State = s.State.Update(review.Result{}, sha, prFiles, false)
		s.State.Patches = reviewedPatches(s.State.Patches, patches, nil)
		s.Write("", footer(fmt.Sprintf("Reviewed up to `%s` · nothing to review", sha[:7])))
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
		c := review.Comment{Path: t.Path, Line: cmp.Or(t.Line, t.Original), Body: text.Truncate(strings.ReplaceAll(forge.Redact(t.Body), forge.Mark, ""), 600)}
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
	var regs []checks.Registry
	if pr.Head.Repo.ID == e.Repository.ID { // a fork's author could name any ticket and read it in the review, or fetch the internal packages
		tid, tk = ticket.Lookup(ctx, cfg.Tickets, pr.Title, pr.Head.Ref, pr.Body)
		regs, _ = checks.Registries(e.Owner()) // main checked them
	}
	r, stats, err := review.Analyze(ctx, review.Input{
		Agent: agent.Pi{}, Dir: dir, Title: forge.Redact(pr.Title), Body: forge.Redact(pr.Body), Commits: forge.Redact(review.Messages(ctx, filepath.Join(dir, "repo"))), Ticket: tk, Parts: parts,
		Trusted: review.TrustedContext(rules, readLearnings(e.Owner(), e.Repo(), cfg.Learnings)), Checks: scripts, Registries: regs, Prior: prior, Closed: closed, Settings: cfg,
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
	if len(omitted) > 0 { // the rest only by name: findings cover part of the PR
		inline := len(slices.DeleteFunc(diff.Paths(d), func(f string) bool { return slices.Contains(omitted, f) }))
		kind += fmt.Sprintf(" · ⚠️ inline %d of %d files", inline, inline+len(omitted))
		if all, rest := diff.Filter(raw, filepath.Join(dir, "repo"), only, cfg.Ignored, cfg.Parts(true)); cfg.Asks() && len(all) > len(parts) {
			// a file with a piece still left out isn't one "review all" reviews
			full := slices.DeleteFunc(diff.Paths(strings.Join(all, "")), func(f string) bool { return slices.Contains(rest, f) })
			s.Ask = forge.AskLarge(len(full), inline+len(omitted), len(all), f.Boxes(), f.Bot())
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
	} else {
		p.Publish() // the answers to fixed threads
	}
	desc = text.Count(n, "finding")
	if len(r.Fixed) > 0 {
		desc += fmt.Sprintf(", %d fixed", len(r.Fixed))
	}
	if len(r.Failed) > 0 { // their part failed: not green, the next push or `@app review` reviews them
		desc, state = desc+", "+text.Count(len(r.Failed), "file")+" not reviewed", "error"
	}
	log.Printf("%s %s %s %s: %s", key, sha[:7], kind, stats, desc)
	s.State = s.State.Update(r, sha, prFiles, only == nil)
	// a file with a piece left out isn't reviewed: the next push reviews it again
	s.State.Patches = reviewedPatches(s.State.Patches, patches, slices.DeleteFunc(diff.Paths(d), func(f string) bool {
		return slices.Contains(r.Failed, f) || slices.Contains(omitted, f)
	}))
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
			Dropped: r.Dropped, Failed: r.Failed, Omitted: omitted, Tests: r.Tests, Sources: report.Sources(repo, raw),
		}
		// what the new code is for, where no doc comment or commit says: one call, the provider's small model
		if keys, texts := report.Asks(d); len(keys) > 0 {
			rm, vm := cfg.Models()
			d.Whys = map[string]string{}
			for i, w := range review.Explain(ctx, agent.Pi{}, dir, cmp.Or(agent.VerifyFor(rm), vm, rm), forge.Redact(pr.Title), texts) {
				d.Whys[keys[i]] = w
			}
		}
		s.State.Secret = reportSecret(s.State.Secret)
		s.State.Report = writeReport(e, s.State.Secret, d)
	}
	banner := ""
	if n == 0 {
		banner = "No actionable comments were generated in the latest review."
	}
	s.Write(banner, footer(fmt.Sprintf("Reviewed up to `%s` · %s", sha[:7], desc)))
	return nil
}

// ReportURL is where the server serves reports (REPORT_URL); "" = no reports.
var ReportURL string

// maxVersions is the reports a PR keeps, one per reviewed head: the newest.
const maxVersions = 10

// reportPath is a PR's folder in Reports: its forge's host, the repository, the number. Its reports are in {secret}/
// of it, as under /r/: a version is {sha}.html.gz, versions.json lists them newest first. Reports from before the
// secret, right in the folder, no longer resolve and go with the PR.
func reportPath(e *forge.Event) string {
	host, _, _ := strings.Cut(strings.TrimPrefix(e.PullRequest.HTMLURL, "https://"), "/")
	return cmp.Or(host, "github.com") + "/" + e.Repository.FullName + "/" + strconv.Itoa(e.PullRequest.Number)
}

// Secret is a report folder's secret, rand.Text's: 130 bits, URL-safe. Without it a report's URL is its PR's, which
// anyone guesses, and the app has no login: an exposed port or PR code on localhost read every report.
var Secret = regexp.MustCompile(`^[A-Z2-7]{26}$`)

// botBranch is the branch of a dependency bump, Renovate's or Dependabot's default prefix; botBody is the
// hidden comments Renovate puts in every description, whatever its branchPrefix.
var (
	botBranch = regexp.MustCompile(`^(renovate|dependabot)/`)
	botBody   = regexp.MustCompile(`<!--renovate-(debug|config-hash):`)
)

// reportSecret is the PR's secret: the one its walkthrough holds, a new one for its first report or one edited by hand.
func reportSecret(s string) string {
	if Secret.MatchString(s) {
		return s
	}
	return rand.Text()
}

// Hide is err's text without the report secret: the stores' errors name the key.
func Hide(err error, secret string) string { return strings.ReplaceAll(err.Error(), secret, "…") }

// ReportS3 holds the reports instead of DATA_DIR when set (REPORT_S3_URL).
var ReportS3 *store.S3

// Reports holds the reports.
func Reports() store.Store {
	if ReportS3 != nil {
		return ReportS3
	}
	return disk()
}

func disk() store.Disk { return store.Disk(filepath.Join(proc.DataDir, "reports")) }

// MoveReports moves the reports DATA_DIR still holds into ReportS3, after a switch to it.
// ponytail: one object at a time before the server listens; a worker pool if a big folder makes starts slow.
func MoveReports() error {
	if ReportS3 == nil {
		return nil
	}
	return disk().MoveTo(ReportS3)
}

// DeleteReports deletes a PR's reports: its code shouldn't outlive the PR. With S3, those a failed MoveReports
// left on disk too.
func DeleteReports(e *forge.Event) error {
	err := Reports().DeleteAll(reportPath(e))
	if ReportS3 != nil {
		err = errors.Join(err, disk().DeleteAll(reportPath(e)))
	}
	return err
}

// Versions is a PR's reports, newest first, by their folder (reportPath/secret); none when it has no list. Any other
// error is one: taken as none, the next report would drop the list.
func Versions(path string) ([]report.Version, error) {
	b, err := Reports().Get(path + "/versions.json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var vs []report.Version
	return vs, json.Unmarshal(b, &vs)
}

// writeReport renders the PR's report into its secret's folder as the version of its head, against the newest of
// another head, and returns its URL; "" when that fails, the review stands without it. d.Routine is this review's: an
// incremental one has none, and the last full one's line numbers may be stale.
// ponytail: two reviews of a PR finishing at once can drop one's entry from versions.json; a lock per PR if it shows.
func writeReport(e *forge.Event, secret string, d report.Data) string {
	dir, s := reportPath(e)+"/"+secret, Reports()
	vs, err := Versions(dir)
	if err != nil {
		log.Println(e.Key(), "report:", Hide(err, secret))
		return ""
	}
	vs = slices.DeleteFunc(vs, func(v report.Version) bool { return v.SHA == d.SHA })
	if len(vs) > 0 {
		d.Prev = &vs[0]
	}
	b, now, err := report.Render(d)
	now.At = time.Now()
	vs = slices.Insert(vs, 0, now)
	keep := vs[:min(len(vs), maxVersions)]
	if err == nil { // compressed once at its best: /r/ sends it as it is
		var z bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&z, gzip.BestCompression)
		_, _ = zw.Write(b)
		if err = zw.Close(); err == nil {
			err = s.Put(dir+"/"+d.SHA+".html.gz", z.Bytes())
		}
	}
	if err == nil { // after the page: a listed version always has one
		b, err = json.Marshal(keep)
	}
	if err == nil {
		err = s.Put(dir+"/versions.json", b)
	}
	if err != nil {
		log.Println(e.Key(), "report:", Hide(err, secret))
		return ""
	}
	for _, v := range vs[len(keep):] {
		_ = s.Delete(dir + "/" + v.SHA + ".html.gz")
	}
	return ReportURL + "/r/" + dir
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
			c := forge.Parse(forge.Clean(t.Body))
			c.Path, c.Line = t.Path, t.Line
			cs = append(cs, c)
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
