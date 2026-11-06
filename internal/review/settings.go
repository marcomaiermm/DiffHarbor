package review

import (
	"cmp"
	"diffharbor/internal/agent"
	"diffharbor/internal/diff"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Settings is a repo's review.json at the root, from the base branch. Unknown keys are ignored, so
// "_comment" works.
type Settings struct {
	// Globs (globRe) of files not to review; a plain path takes its subtree, "!glob" takes paths back in.
	Ignore []string `json:"ignore"`
	pkgSettings
	// Overrides for parts of a monorepo, by path glob; the longest matching pattern wins.
	Packages map[string]pkgSettings `json:"packages"`
	// Models as provider/id[:level], unless the server pins them with REVIEW_MODEL / VERIFY_MODEL. Keys stay in
	// the server's env: a model whose provider has no key there fails the review.
	ReviewModel string `json:"review_model"`
	VerifyModel string `json:"verify_model"`
	// A diff past diff.Max reviews in parts of up to diff.Max, one review and verify pass each, at most MaxParts.
	// "all" (default): always in parts; "ask": code first up to diff.Max, the walkthrough offers a box to tick for
	// all parts; "off": never.
	LargePR  string `json:"large_pr"`
	MaxParts int    `json:"max_parts"` // default 4, at most 8
	// The finders per part, each with tools and then its second look, their findings verified together. Unset: one,
	// the review model's. At most 3; the first is the review model's and always runs.
	Passes []Pass `json:"passes"`
	// The HTML report to read a PR with (reading order, commits, tests), when the server has REPORT_URL.
	// "auto" (default): from ReportFiles changed files; "always"; "off".
	Report string `json:"report"`
	// From these changed lines or files of the reviewed diff, the guide asks whether the PR should be several; unset: never.
	SuggestSplit SplitAt `json:"suggest_split"`
	// The report's code colors: Shiki theme names (shiki.style/themes), by the reader's light or dark mode.
	// Default tokyo-night-day and tokyo-night.
	SyntaxTheme SyntaxTheme `json:"syntax_theme"`
	// The repo's review files, from the root. Set, a path replaces the default.
	Rules  string   `json:"rules"`  // rules in prose, default .review/rules.md, else AGENTS.md or CLAUDE.md
	Skills string   `json:"skills"` // dir of review skills, default .review/skills
	Checks []string `json:"checks"` // scripts run after the built-in checks, default .review/checks.sh
	// itsaplan projects whose tickets a PR may name ("WID"); unset = any, [] = none. Never for a PR from a fork.
	Tickets []string `json:"tickets"`
	// true: the checks' installs run lifecycle scripts (postinstall, native builds); default off, a dependency's
	// script would run as the repo's user next to the PR's code.
	InstallScripts bool `json:"install_scripts"`
	// Base branches besides the default branch whose PRs get reviewed without asking, as globs ("release/*").
	BaseBranches []string `json:"base_branches"`
	// Draft PRs: "manual" (default) reviewed when asked; "opt_in" once asked, every push too, like any PR;
	// "off" never.
	Drafts string `json:"drafts"`
	// How GitLab gets the findings: "review" as one review, one email; "discussions" an email each. Unset: the
	// server's GITLAB_FINDINGS.
	GitLabFindings string `json:"gitlab_findings"`
	// What reviews and chats of this repo follow of the learnings.
	Learnings Learnings `json:"learnings"`

	ignoreRe, keepRe *regexp.Regexp // ignore's globs, its "!" ones
	override         string         // Override: this review's profile, over the top and the packages
	CITests          string         // checks.CITests: the CI file running the tests; unless "tests" says otherwise, the review doesn't
	pkgs             []pkgRule      // longest pattern first
}

// SyntaxTheme is a Shiki theme for each color mode.
type SyntaxTheme struct {
	Light string `json:"light"`
	Dark  string `json:"dark"`
}

// Learnings is review.json's learnings, applied when they are read: a lower max hides the oldest, it deletes nothing.
type Learnings struct {
	Max        *int `json:"max"`          // the newest of the repo's and of the owner's each; unset 30, 0 = none, at most MaxLearnings
	MaxAgeDays int  `json:"max_age_days"` // older ones are left out; 0 = any age, an undated one never ages
}

// MaxLearnings is the most learnings kept per repo and per owner, and review.json's highest max.
const MaxLearnings = 100

// Limit returns how many of the newest learnings of each file a review follows.
func (l Learnings) Limit() int {
	if l.Max == nil {
		return 30
	}
	return *l.Max
}

// SplitAt is review.json's suggest_split: either count reached asks; 0 asks by the other only.
type SplitAt struct {
	Lines int      `json:"lines"`
	Files int      `json:"files"`
	Count []string `json:"count"` // the classes (diff.Classes) counted; unset: code, [] = only skip's "!" globs
	Skip  []string `json:"skip"`  // globs not counted; "!glob" counts a path whatever its class, in any order

	skipRe, keepRe *regexp.Regexp
}

// Counts reports whether path's changes count toward suggest_split.
func (s SplitAt) Counts(path string) bool {
	if s.keepRe != nil && s.keepRe.MatchString(path) {
		return true
	}
	count := s.Count
	if count == nil {
		count = []string{"code"}
	}
	return slices.Contains(count, diff.Class(path)) && (s.skipRe == nil || !s.skipRe.MatchString(path))
}

// Pass is a finder of review.json's passes.
type Pass struct {
	// provider/id[:level]; unset: agent.SecondFor the review model. A server's REVIEW_MODEL pins it like review_model.
	Model    string `json:"model"`
	Again    *bool  `json:"again"`     // false: no second look
	MinLines int    `json:"min_lines"` // runs on a PR with at least this many changed lines in the files it reviews
	MaxLines int    `json:"max_lines"` // and at most this many; 0 = any
}

type pkgSettings struct {
	MinSeverity string `json:"min_severity"` // minor|major|critical: post nothing below it
	Tests       *bool  `json:"tests"`        // false: don't run the tests of changed packages; unset = inherit, at the top run unless CI does
	// What goes inline and what folds into the review's summary: "quiet" folds minor ones and nits, "chill" (default)
	// nits, "assertive" none. min_severity drops what is below it first, "minor" the nits.
	Profile string `json:"profile"`
}

type pkgRule struct {
	re *regexp.Regexp
	s  pkgSettings
}

var (
	rank    = map[string]int{"nit": 1, "minor": 2, "major": 3, "critical": 4}
	modelRe = regexp.MustCompile(`^\w[\w.-]*/[\w.:/@-]+$`)
	themeRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	// a PR description's line "review: quiet", plain, a list item or a ticked box, "quiet" maybe bold or code; an
	// unticked box is no choice
	bodyProfile = regexp.MustCompile("(?im)^[ \\t>]*(?:(?:[-*+]|\\d+[.)])[ \\t]+(?:\\[[xX]\\][ \\t]+)?)?review:[ \\t]*[*_`]*(quiet|chill|assertive)[*_`]*(?:[ \\t.,;!)\\r]|$)")
	// what a PR description shows no one or only as an example: HTML comments, code blocks; unclosed, up to the end
	hidden = regexp.MustCompile("(?ms)<!--.*?(?:-->|\\z)|^[ \\t>]*```.*?(?:^[ \\t>]*```|\\z)|^[ \\t>]*~~~.*?(?:^[ \\t>]*~~~|\\z)")
)

// Profiles are the values of review.json's profile, least inline first.
var Profiles = []string{"quiet", "chill", "assertive"}

// BodyProfile is the profile a PR description names on a line of its own, "review: quiet", by hand or as a ticked
// box of its template; the first, "" for none. HTML comments don't count: a template's hint lists every profile.
func BodyProfile(body string) string {
	if m := bodyProfile.FindStringSubmatch(hidden.ReplaceAllString(body, "")); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// Override sets the profile of every file for this review, over review.json's top and packages; "" keeps theirs.
func (cfg *Settings) Override(profile string) { cfg.override = profile }

// Models returns the review and verify model: the server's env, else review.json, else agent.Matrix. An empty
// verify model means the review model verifies.
func (cfg Settings) Models() (review, verify string) {
	review = cmp.Or(agent.Model, cfg.ReviewModel, agent.Default())
	return review, cmp.Or(agent.VerifyModel, cfg.VerifyModel, agent.VerifyFor(review))
}

// Finders returns the passes a PR with lines changed lines gets, with their models.
func (cfg Settings) Finders(lines int) []Pass {
	review, _ := cfg.Models()
	ps := cfg.Passes
	if len(ps) == 0 {
		ps = []Pass{{}}
	}
	var out []Pass
	for k, p := range ps {
		switch {
		case k == 0:
			p.Model = review
		case p.Model == "" || agent.Model != "":
			p.Model = agent.SecondFor(review)
		}
		if lines >= p.MinLines && (p.MaxLines == 0 || lines <= p.MaxLines) {
			out = append(out, p)
		}
	}
	return out
}

// Parts returns the parts a review may split into; confirmed: someone asked for all of them (the box, `review all`).
func (cfg Settings) Parts(confirmed bool) int {
	if cfg.LargePR == "" || cfg.LargePR == "all" || confirmed && cfg.LargePR == "ask" {
		return cmp.Or(cfg.MaxParts, 4)
	}
	return 1
}

// SuggestsSplit reports whether a PR with diff d is asked whether it should be several: its changed lines or files
// that Counts.
func (cfg Settings) SuggestsSplit(d string) bool {
	s := cfg.SuggestSplit
	lines, files := 0, map[string]bool{}
	diff.EachFile(d, func(p, f string) {
		if s.Counts(p) {
			lines += diff.Changed(f)
			files[p] = true // Filter's pieces of one file count once
		}
	})
	return s.Lines > 0 && lines >= s.Lines || s.Files > 0 && len(files) >= s.Files
}

// ReportFiles is the changed files from which a PR gets a report unless review.json says otherwise.
const ReportFiles = 10

// Reports reports whether a PR with files changed files gets a report.
func (cfg Settings) Reports(files int) bool {
	return cfg.Report == "always" || cfg.Report != "off" && files >= ReportFiles
}

// Reviews reports whether a PR into base gets reviewed without asking: into the default branch (unknown: any)
// or one of BaseBranches.
func (cfg Settings) Reviews(base, defaultBranch string) bool {
	return defaultBranch == "" || base == defaultBranch || slices.ContainsFunc(cfg.BaseBranches, func(g string) bool {
		ok, _ := path.Match(g, base)
		return ok
	})
}

// ReviewsDraft reports whether a draft PR gets reviewed: asked for now (manual), or on a push once asked before.
func (cfg Settings) ReviewsDraft(manual, asked bool) bool {
	return cfg.Drafts != "off" && (manual || asked && cfg.Drafts == "opt_in")
}

// Asks reports whether a review cut short offers the box for all parts.
func (cfg Settings) Asks() bool { return cfg.LargePR == "ask" }

// globRe matches a path any of globs matches, or a path under it. The syntax is doublestar's
// (github.com/bmatcuk/doublestar), translated to a regexp, so linear: * and ? within a segment, ** as a whole
// segment any depth (else as *), [abc], [a-z], [!abc], {a,b} nested, \ escapes the next byte.
func globRe(globs ...string) (*regexp.Regexp, error) {
	var res []string
	for _, g := range globs {
		var b strings.Builder
		lit := func(c byte) {
			if strings.IndexByte(`\.+*?()|[]{}^$-`, c) >= 0 {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
		}
		depth, class := 0, false
		for i := 0; i < len(g); i++ {
			c := g[i]
			seg := i == 0 || g[i-1] == '/'
			switch {
			case c == '\\' && i+1 < len(g):
				i++
				lit(g[i])
			case class && c == ']':
				b.WriteByte(']')
				class = false
			case class && c == '-':
				b.WriteByte('-')
			case class:
				lit(c)
			case c == '[':
				b.WriteByte('[')
				class = true
				if i+1 < len(g) && (g[i+1] == '!' || g[i+1] == '^') {
					b.WriteString("^/")
					i++
				}
			case seg && strings.HasPrefix(g[i:], "**/"):
				b.WriteString("(.*/)?")
				i += 2
			case seg && g[i:] == "**":
				b.WriteString(".*")
				i++
			case c == '*':
				b.WriteString("[^/]*")
			case c == '?':
				b.WriteString("[^/]")
			case c == '{':
				b.WriteString("(?:")
				depth++
			case c == '}': // a stray one fails to compile below
				b.WriteString(")")
				depth--
			case c == ',' && depth > 0:
				b.WriteString("|")
			default:
				lit(c)
			}
		}
		if _, err := regexp.Compile(b.String()); err != nil {
			return nil, fmt.Errorf("glob %q: want closed [] and {}, ranges low-high", g)
		}
		res = append(res, b.String())
	}
	return regexp.Compile(`^(` + strings.Join(res, "|") + `)(/.*)?$`)
}

// globsRe compiles globs (globRe) apart from its "!glob" ones; none of a kind is nil.
func globsRe(globs []string) (in, out *regexp.Regexp, err error) {
	var plain, not []string
	for _, g := range globs {
		if k, ok := strings.CutPrefix(g, "!"); ok {
			not = append(not, k)
		} else {
			plain = append(plain, g)
		}
	}
	if len(plain) > 0 {
		if in, err = globRe(plain...); err != nil {
			return nil, nil, err
		}
	}
	if len(not) > 0 {
		if out, err = globRe(not...); err != nil {
			return nil, nil, err
		}
	}
	return in, out, nil
}

// LoadSettings parses s ("" = defaults). A broken file reviews with the defaults, the error says why.
func LoadSettings(s string) (Settings, error) {
	var cfg Settings
	if strings.TrimSpace(s) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(s), &cfg); err != nil {
		return Settings{}, err
	}
	for i, p := range append([]string{cfg.Rules, cfg.Skills}, cfg.Checks...) {
		// Paths go into API URLs and the checkout: in the repo, no query. Only rules and skills may be unset.
		if (p != "" || i > 1) && (!filepath.IsLocal(p) || strings.ContainsAny(p, "?#\\")) {
			return Settings{}, fmt.Errorf("path %q: want one inside the repo, from its root", p)
		}
	}
	if len(cfg.Passes) > 3 {
		return Settings{}, fmt.Errorf("passes: %d, want at most 3", len(cfg.Passes))
	}
	models := []string{cfg.ReviewModel, cfg.VerifyModel}
	for k, p := range cfg.Passes {
		if k == 0 && (p.Model != "" || p.MinLines != 0 || p.MaxLines != 0) {
			return Settings{}, fmt.Errorf("passes: the first is review_model's and always runs, it takes again only")
		}
		if p.MinLines < 0 || p.MaxLines < 0 || p.MaxLines > 0 && p.MaxLines < p.MinLines {
			return Settings{}, fmt.Errorf("passes %d: min_lines %d, max_lines %d: want 0 <= min_lines <= max_lines, max_lines 0 = any", k+1, p.MinLines, p.MaxLines)
		}
		models = append(models, p.Model)
	}
	for _, m := range models {
		if m != "" && !modelRe.MatchString(m) { // goes to pi's argv: no flag
			return Settings{}, fmt.Errorf("model %q: want provider/id[:level]", m)
		}
	}
	if !slices.Contains([]string{"", "ask", "all", "off"}, cfg.LargePR) {
		return Settings{}, fmt.Errorf("large_pr %q: want ask, all or off", cfg.LargePR)
	}
	if !slices.Contains([]string{"", "auto", "always", "off"}, cfg.Report) {
		return Settings{}, fmt.Errorf("report %q: want auto, always or off", cfg.Report)
	}
	for _, t := range []string{cfg.SyntaxTheme.Light, cfg.SyntaxTheme.Dark} {
		if t != "" && !themeRe.MatchString(t) { // goes into the page's script
			return Settings{}, fmt.Errorf("syntax_theme %q: want a Shiki theme name", t)
		}
	}
	for _, g := range cfg.BaseBranches {
		if _, err := path.Match(g, ""); err != nil {
			return Settings{}, fmt.Errorf("base_branches %q: %w", g, err)
		}
	}
	if !slices.Contains([]string{"", "manual", "opt_in", "off"}, cfg.Drafts) {
		return Settings{}, fmt.Errorf("drafts %q: want manual, opt_in or off", cfg.Drafts)
	}
	if f := cfg.GitLabFindings; f != "" && f != "review" && f != "discussions" {
		return Settings{}, fmt.Errorf("gitlab_findings %q: want review or discussions", f)
	}
	if cfg.MaxParts < 0 || cfg.MaxParts > 8 {
		return Settings{}, fmt.Errorf("max_parts %d: want 1 to 8", cfg.MaxParts)
	}
	if l := cfg.Learnings; l.Limit() < 0 || l.Limit() > MaxLearnings || l.MaxAgeDays < 0 {
		return Settings{}, fmt.Errorf("learnings: max %d, max_age_days %d: want max 0 to %d, max_age_days 0 (any) or more", l.Limit(), l.MaxAgeDays, MaxLearnings)
	}
	if s := cfg.SuggestSplit; s.Lines < 0 || s.Files < 0 {
		return Settings{}, fmt.Errorf("suggest_split: lines %d, files %d: want 0 (off) or more", s.Lines, s.Files)
	}
	for _, c := range cfg.SuggestSplit.Count {
		if !slices.Contains(diff.Classes, c) {
			return Settings{}, fmt.Errorf("suggest_split: count %q: want %s", c, strings.Join(diff.Classes, ", "))
		}
	}
	if err := cfg.pkgSettings.check(); err != nil {
		return Settings{}, err
	}
	var err error
	if cfg.ignoreRe, cfg.keepRe, err = globsRe(cfg.Ignore); err != nil {
		return Settings{}, fmt.Errorf("ignore: %w", err)
	}
	sp := &cfg.SuggestSplit
	if sp.skipRe, sp.keepRe, err = globsRe(sp.Skip); err != nil {
		return Settings{}, fmt.Errorf("suggest_split: skip: %w", err)
	}
	for _, p := range slices.SortedFunc(maps.Keys(cfg.Packages), func(a, b string) int { return cmp.Compare(len(b), len(a)) }) {
		ps := cfg.Packages[p]
		if err := ps.check(); err != nil {
			return Settings{}, fmt.Errorf("packages %q: %w", p, err)
		}
		re, err := globRe(strings.TrimSuffix(p, "/"))
		if err != nil {
			return Settings{}, fmt.Errorf("packages: %w", err)
		}
		cfg.pkgs = append(cfg.pkgs, pkgRule{re, ps})
	}
	return cfg, nil
}

// check validates the keys a package may override.
func (s pkgSettings) check() error {
	if s.MinSeverity != "" && rank[s.MinSeverity] < rank["minor"] {
		return fmt.Errorf("min_severity %q: want minor, major or critical", s.MinSeverity)
	}
	if s.Profile != "" && !slices.Contains(Profiles, s.Profile) {
		return fmt.Errorf("profile %q: want quiet, chill or assertive", s.Profile)
	}
	return nil
}

// RulePaths returns the candidates for the rules file, the first that exists holds.
func (cfg Settings) RulePaths() []string {
	if cfg.Rules != "" {
		return []string{cfg.Rules, "AGENTS.md", "CLAUDE.md"}
	}
	return []string{".review/rules.md", "AGENTS.md", "CLAUDE.md"}
}

// SkillPaths returns the candidates for the skills dir, the first that exists holds.
func (cfg Settings) SkillPaths() []string {
	if cfg.Skills != "" {
		return []string{cfg.Skills}
	}
	return []string{".review/skills"}
}

// CheckPaths returns the check scripts; all that exist run.
func (cfg Settings) CheckPaths() []string {
	if cfg.Checks != nil {
		return cfg.Checks
	}
	return []string{".review/checks.sh"}
}

// At returns the settings for path: the top level, overridden by the longest package pattern matching it.
func (cfg Settings) At(path string) (minSeverity string, tests bool) {
	s := cfg.at(path)
	if s.Tests == nil {
		return s.MinSeverity, cfg.CITests == ""
	}
	return s.MinSeverity, *s.Tests
}

func (cfg Settings) at(path string) pkgSettings {
	s := cfg.pkgSettings
	for _, r := range cfg.pkgs {
		if r.re.MatchString(path) {
			s.MinSeverity, s.Tests, s.Profile = cmp.Or(r.s.MinSeverity, s.MinSeverity), cmp.Or(r.s.Tests, s.Tests), cmp.Or(r.s.Profile, s.Profile)
			break
		}
	}
	return s
}

// LeftToCI reports whether path's tests are skipped because the CI runs them, not because review.json says so.
func (cfg Settings) LeftToCI(path string) bool {
	return cfg.CITests != "" && cfg.at(path).Tests == nil
}

// Ignored reports whether path is not reviewed: an ignore glob matches it and no "!" one does, in any order.
func (cfg Settings) Ignored(path string) bool {
	return cfg.ignoreRe != nil && cfg.ignoreRe.MatchString(path) && (cfg.keepRe == nil || !cfg.keepRe.MatchString(path))
}

// Keep reports whether c reaches the min_severity for its file.
func (cfg Settings) Keep(c Comment) bool {
	m, _ := cfg.At(c.Path)
	return m == "" || rank[c.Severity] >= rank[m]
}

// Fold reports whether c goes folded into the review's summary instead of inline, by its file's profile: a nit
// unless assertive, a minor one under quiet.
func (cfg Settings) Fold(c Comment) bool {
	switch cmp.Or(cfg.override, cfg.at(c.Path).Profile) {
	case "assertive":
		return false
	case "quiet":
		return rank[c.Severity] < rank["major"]
	}
	return c.Severity == "nit"
}

// RunsTests reports whether the tests of path's package run.
func (cfg Settings) RunsTests(path string) bool {
	_, t := cfg.At(path)
	return t
}
