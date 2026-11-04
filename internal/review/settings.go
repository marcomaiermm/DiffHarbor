package review

import (
	"cmp"
	"diffharbor/internal/agent"
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
	Ignore []string `json:"ignore"` // globs (*, **, ?) of files not to review; a plain path takes its subtree
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
	// The HTML report to read a PR with (reading order, commits, tests), when the server has REPORT_URL.
	// "auto" (default): from ReportFiles changed files; "always"; "off".
	Report string `json:"report"`
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

	IgnoreRe *regexp.Regexp
	CITests  string    // checks.CITests: the CI file running the tests; unless "tests" says otherwise, the review doesn't
	pkgs     []pkgRule // longest pattern first
}

// SyntaxTheme is a Shiki theme for each color mode.
type SyntaxTheme struct {
	Light string `json:"light"`
	Dark  string `json:"dark"`
}

type pkgSettings struct {
	MinSeverity string `json:"min_severity"` // minor|major|critical: post nothing below it
	Tests       *bool  `json:"tests"`        // false: don't run the tests of changed packages; unset = inherit, at the top run unless CI does
}

type pkgRule struct {
	re *regexp.Regexp
	s  pkgSettings
}

var (
	rank    = map[string]int{"minor": 1, "major": 2, "critical": 3}
	modelRe = regexp.MustCompile(`^\w[\w.-]*/[\w.:/@-]+$`)
	themeRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
)

// Models returns the review and verify model: the server's env, else review.json, else agent.Matrix. An empty
// verify model means the review model verifies.
func (cfg Settings) Models() (review, verify string) {
	review = cmp.Or(agent.Model, cfg.ReviewModel, agent.Default())
	return review, cmp.Or(agent.VerifyModel, cfg.VerifyModel, agent.VerifyFor(review))
}

// Parts returns the parts a review may split into; confirmed: someone asked for all of them (the box, `review all`).
func (cfg Settings) Parts(confirmed bool) int {
	if cfg.LargePR == "" || cfg.LargePR == "all" || confirmed && cfg.LargePR == "ask" {
		return cmp.Or(cfg.MaxParts, 4)
	}
	return 1
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

// Asks reports whether a review cut short offers the box for all parts.
func (cfg Settings) Asks() bool { return cfg.LargePR == "ask" }

// globRe matches a path any of globs (*, **, ?) matches, or a path under it.
func globRe(globs ...string) *regexp.Regexp {
	var res []string
	for _, g := range globs {
		var b strings.Builder
		for i := 0; i < len(g); i++ {
			switch {
			case strings.HasPrefix(g[i:], "**/"):
				b.WriteString("(.*/)?")
				i += 2
			case strings.HasPrefix(g[i:], "**"):
				b.WriteString(".*")
				i++
			case g[i] == '*':
				b.WriteString("[^/]*")
			case g[i] == '?':
				b.WriteString("[^/]")
			default:
				b.WriteString(regexp.QuoteMeta(g[i : i+1]))
			}
		}
		res = append(res, b.String())
	}
	return regexp.MustCompile(`^(` + strings.Join(res, "|") + `)(/.*)?$`)
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
	for _, m := range []string{cfg.ReviewModel, cfg.VerifyModel} {
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
	if cfg.MaxParts < 0 || cfg.MaxParts > 8 {
		return Settings{}, fmt.Errorf("max_parts %d: want 1 to 8", cfg.MaxParts)
	}
	if cfg.MinSeverity != "" && rank[cfg.MinSeverity] == 0 {
		return Settings{}, fmt.Errorf("min_severity %q: want minor, major or critical", cfg.MinSeverity)
	}
	if len(cfg.Ignore) > 0 {
		cfg.IgnoreRe = globRe(cfg.Ignore...)
	}
	for _, p := range slices.SortedFunc(maps.Keys(cfg.Packages), func(a, b string) int { return cmp.Compare(len(b), len(a)) }) {
		ps := cfg.Packages[p]
		if ps.MinSeverity != "" && rank[ps.MinSeverity] == 0 {
			return Settings{}, fmt.Errorf("packages %q: min_severity %q: want minor, major or critical", p, ps.MinSeverity)
		}
		cfg.pkgs = append(cfg.pkgs, pkgRule{globRe(strings.TrimSuffix(p, "/")), ps})
	}
	return cfg, nil
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
			s.MinSeverity, s.Tests = cmp.Or(r.s.MinSeverity, s.MinSeverity), cmp.Or(r.s.Tests, s.Tests)
			break
		}
	}
	return s
}

// LeftToCI reports whether path's tests are skipped because the CI runs them, not because review.json says so.
func (cfg Settings) LeftToCI(path string) bool {
	return cfg.CITests != "" && cfg.at(path).Tests == nil
}

// Keep reports whether c reaches the min_severity for its file.
func (cfg Settings) Keep(c Comment) bool {
	m, _ := cfg.At(c.Path)
	return m == "" || rank[c.Severity] >= rank[m]
}

// RunsTests reports whether the tests of path's package run.
func (cfg Settings) RunsTests(path string) bool {
	_, t := cfg.At(path)
	return t
}
