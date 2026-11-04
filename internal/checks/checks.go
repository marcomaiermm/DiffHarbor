// Package checks runs linters, scanners and tests on the changed files, as the repo's user in the PR checkout. Their output goes to
// the model as hints (exit codes don't matter); tools that find something cost no tokens to find it.
package checks

import (
	"bytes"
	"cmp"
	"context"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"diffharbor/internal/text"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	jsFile    = regexp.MustCompile(`\.(c|m)?(j|t)sx?$`)
	manifest  = regexp.MustCompile(`(^|/)(package\.json|go\.mod|Cargo\.toml|pyproject\.toml|requirements[^/]*\.txt|Gemfile|composer\.json)$`)
	workflow  = regexp.MustCompile(`^\.github/workflows/[^/]+\.ya?ml$`)
	shellFile = regexp.MustCompile(`\.(sh|bash)$`)
	goFile    = regexp.MustCompile(`\.go$`)
	luaFile   = regexp.MustCompile(`\.lua$`)
	sqlFile   = regexp.MustCompile(`\.sql$`)
	testFile  = regexp.MustCompile(`(^|/)(tests?|__tests__)/|\.(test|spec)\.[cm]?[jt]sx?$`)
	// A CI job that runs tests: go test, vitest, `pnpm -r run test:unit`, `turbo run test`, `nx affected -t test` …
	ciTest = regexp.MustCompile(`\b(go test|gotestsum|vitest|jest|pytest|cargo (test|nextest)|make test)\b|\b(npm|pnpm|yarn|bun|turbo|nx)\b[^\n]*\stest(:\S*)?(\s|$)`)
	// A path heuristic for "touches the Drizzle schema", the configs' schema globs aren't read.
	drizzlePath  = regexp.MustCompile(`(?i)schema|drizzle|migration`)
	drizzleNoise = regexp.MustCompile(`^Reading config file|^\d+ tables$|^\S+ \d+ columns \d+ indexes \d+ fks$|^\s+at |^\s*$`)
	// Promises left floating on purpose: router navigation, query cache warmups and refetches.
	fireAndForget = regexp.MustCompile(`\b(navigate|prefetch\w*|invalidateQueries|refetch\w*|fetchNextPage)\(`)
	atLine        = regexp.MustCompile(`^([^:\s]+):(\d+)(?:-(\d+))?`) // a tool's path:line or path:start-end
	dupPrint      = regexp.MustCompile(`fingerprint=dup:\w+`)
	constName     = regexp.MustCompile(`\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+\b`) // env vars, keys, flags: AWS_ACCESS_KEY_ID
)

// golangciConfig, for a repo without its own: bug classes vet and staticcheck miss, like a request without a context
// (noctx), http.DefaultClient without a timeout, an ignored error, a switch missing an enum value, injection (gosec),
// copies (dupl) and functions too complex to review. Reported on added lines only: packages often have older hits.
const golangciConfig = `version: "2"
linters:
  default: none
  enable: [bodyclose, dupl, errcheck, errorlint, exhaustive, forbidigo, gocognit, gocritic, gosec, noctx, rowserrcheck, sqlclosecheck]
  settings:
    errcheck:
      exclude-functions: [fmt.Fprint, fmt.Fprintf, fmt.Fprintln, os.Remove, os.RemoveAll, (io.Closer).Close, (*os.File).Close]
    forbidigo:
      forbid:
        - pattern: ^http\.DefaultClient$
          msg: has no timeout; use an http.Client with Timeout
    gocritic:
      disabled-tags: [style, opinionated, performance, experimental]
    gosec:
      excludes: [G104, G301, G302, G304, G306] # errcheck's, and file modes and paths: mostly noise
  exclusions:
    rules:
      - path: _test\.go
        linters: [dupl, errcheck, gocognit, gosec]
`

// oxlintConfig: type-aware bug classes tsc and biome don't check (floating and misused promises, non-exhaustive
// switches over unions, hooks called conditionally). Reported on added lines only: a large TS repo had 49 older hits.
const oxlintConfig = `{"plugins":["typescript","react"],"categories":{"correctness":"off"},"rules":{
"typescript/no-floating-promises":["error",{"ignoreVoid":true,"ignoreIIFE":true}],
"typescript/no-misused-promises":["error",{"checksVoidReturn":{"attributes":false}}],
"typescript/await-thenable":"error",
"typescript/switch-exhaustiveness-check":["error",{"considerDefaultExhaustiveForUnions":true}],
"react/rules-of-hooks":"error"}}`

// drizzleRules: a Drizzle delete or update without .where() hits every row. Written for TypeScript, copied for Tsx.
const drizzleRules = `id: drizzle-delete-without-where
language: TypeScript
severity: warning
message: delete without .where() removes every row
rule:
  pattern: $DB.delete($T)
  not: {inside: {kind: member_expression, has: {field: property, regex: ^where$}}}
constraints:
  DB: {regex: '(^|\.)(db|tx|trx)$'}
---
id: drizzle-update-without-where
language: TypeScript
severity: warning
message: update without .where() changes every row
rule:
  pattern: $DB.update($T).set($$$)
  not: {inside: {kind: member_expression, has: {field: property, regex: ^where$}}}
constraints:
  DB: {regex: '(^|\.)(db|tx|trx)$'}
`

// squawkExclude: style preferences and session settings. What's left: locks, table rewrites, breaking changes.
const squawkExclude = "prefer-robust-stmts,prefer-bigint-over-int,prefer-bigint-over-smallint,prefer-identity,prefer-text-field," +
	"prefer-timestamptz,ban-char-field,require-lock-timeout,require-statement-timeout"

// maxRoots bounds the packages a review runs the JS and the Go checks in, each: all of them share timeout.
const (
	maxRoots = 5
	timeout  = 10 * time.Minute
)

// noScripts is an install command without lifecycle scripts. Yarn classic wants the flag, Berry rejects it and wants
// --mode=skip-build (YARN_ENABLE_SCRIPTS=false still runs the root's and a dependenciesMeta "built" one's): told
// apart by the yarn that runs (version, its last line after corepack's), not the PR's lockfile. Anything but a Berry
// version gets the flag: Berry fails, never runs scripts. The CLI flags beat a PR's .npmrc and .yarnrc.yml.
func noScripts(cmd []string, yarnVersion string) []string {
	v := strings.TrimSpace(yarnVersion)
	v = v[strings.LastIndex(v, "\n")+1:]
	if major, _, _ := strings.Cut(v, "."); cmd[0] != "yarn" || major < "2" || len(major) != 1 {
		return append(cmd, "--ignore-scripts")
	}
	return append(cmd, "--mode=skip-build")
}

// Script is a repo's own check: Name its path in the repo, File the copy that runs (from the base branch).
type Script struct{ Name, File string }

// Tests is what the test runs among the checks report: each test's outcome by its name as the runner prints it
// ("TestTotal", "TestTotal/discount"), "pass", "fail" or "skip", and the packages that do not build. Empty when
// no test ran. ponytail: go test only; vitest and jest print no per-test result here, a JSON reporter if wanted.
type Tests struct {
	Ran    map[string]string `json:"ran,omitempty"`
	Broken []string          `json:"broken,omitempty"`
}

// Run runs the built-in checks for files, then the repo's own scripts with the files after "--". patch is
// the diff under review as a file. JS and Go checks run per package (nearest package.json or go.mod), so a
// monorepo's apps get theirs; tests(file) says whether the tests of file's package run. Installs skip lifecycle
// scripts unless installScripts: a dependency's postinstall would run as the repo's user next to the PR's code. They
// read from regs, through the vault until Run returns.
//
//nolint:gocognit,gocyclo // the checks step by step, each step a few lines
func Run(ctx context.Context, repo string, scripts []Script, patch string, files []string, tests func(string) bool, installScripts bool, regs []Registry) (string, Tests) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	var out strings.Builder
	var ran Tests
	env := append(proc.ToolEnv(), "NO_COLOR=1")
	if h := os.Getenv("DOCKER_HOST"); h != "" { // dind for testcontainers, see compose.yml
		env = append(env, "DOCKER_HOST="+h)
	}
	regEnv, regURLs, err := registryEnv(ctx, regs)
	if err != nil {
		fmt.Fprintf(&out, "\n### npm registries\n%s\n", err)
	}
	env = append(env, regEnv...)
	// in runs a tool in dir (repo-relative) and returns stdout and stderr.
	in := func(dir string) func(string, ...string) string {
		return func(name string, args ...string) string {
			b := proc.Capped{N: 1 << 20}
			c := proc.Command(ctx, filepath.Join(repo, dir), env, name, args...)
			c.Stdout, c.Stderr = &b, &b
			_ = c.Run()
			return b.String()
		}
	}
	output := in(".")
	// section adds s under title, cut to its first n lines (last -n: test summaries are at the end). A check without
	// output only goes in the closing "No findings" line, by its name and dir: a header each costs tokens in every pass.
	var clean []string
	section := func(title, s string, n int) {
		if s = strings.TrimSpace(s); s == "" {
			name, _, _ := strings.Cut(title, " (")
			if strings.HasSuffix(title, "/ (paths relative to it)") {
				name += strings.TrimSuffix(title[strings.LastIndex(title, " in "):], " (paths relative to it)")
			}
			clean = append(clean, name)
			return
		}
		fmt.Fprintf(&out, "\n### %s\n%s\n", title, lines(s, n))
	}
	sh := func(title string, n int, name string, args ...string) { section(title, output(name, args...), n) }
	var changed []string // changed files that still exist
	for _, f := range files {
		if proc.Exists(filepath.Join(repo, f)) {
			changed = append(changed, f)
		}
	}
	match := func(re *regexp.Regexp) []string {
		return slices.DeleteFunc(slices.Clone(changed), func(f string) bool { return !re.MatchString(f) })
	}
	has := func(f string) bool { return proc.Exists(filepath.Join(repo, f)) }
	// config writes a tool's config next to the checkout, outside the PR's files.
	config := func(name, content string) string {
		p := filepath.Join(filepath.Dir(repo), name)
		if err := proc.WriteFile(p, []byte(content)); err != nil {
			log.Println(name+":", err)
		}
		return p
	}
	pd, _ := proc.ReadFile(ctx, patch)
	added := diff.RightLines(string(pd), false)

	if len(changed) > 0 {
		var leaks string
		for _, f := range changed { // gitleaks dir takes one path
			leaks += output("gitleaks", "dir", "--no-banner", "--redact", "--log-level", "warn", "-v", f)
		}
		section("gitleaks (secrets)", leaks, 60)
	}

	installed := map[string]bool{}
	for _, root := range roots(repo, match(jsFile), "package.json", &out) {
		dir, js := root.dir, root.files
		run, rel, where := in(dir), relTo(dir, js), titled(dir)
		// Workspaces install once at their lockfile, separate apps each at their own.
		if lock := Up(repo, dir, "pnpm-lock.yaml", "yarn.lock", "package-lock.json"); lock != "" && !installed[lock] {
			installed[lock] = true
			cmd := map[string][]string{
				"pnpm-lock.yaml":    {"pnpm", "install", "--frozen-lockfile", "--prefer-offline", "--reporter=silent"},
				"yarn.lock":         {"yarn", "install", "--immutable", "--silent"},
				"package-lock.json": {"npm", "ci", "--no-audit", "--no-fund", "--loglevel=error"},
			}[filepath.Base(lock)]
			if !installScripts {
				v := ""
				if cmd[0] == "yarn" {
					v = in(filepath.Dir(lock))("yarn", "--version")
				}
				cmd = noScripts(cmd, v)
			}
			restore := pointAt(ctx, filepath.Join(repo, lock), regURLs)
			res := in(filepath.Dir(lock))(cmd[0], cmd[1:]...)
			if err := restore(); err != nil {
				res += "\nrestoring " + lock + " after pointing it at the registries: " + err.Error()
			}
			section("install"+titled(filepath.Dir(lock)), res, -20)
		}
		// bin finds a tool in the package's node_modules/.bin or a parent's (workspaces hoist them); "" = none.
		bin := func(t string) string {
			if p := Up(repo, dir, filepath.Join("node_modules/.bin", t)); p != "" {
				return filepath.Join(repo, p)
			}
			return ""
		}
		at := func(f string) bool { return has(filepath.Join(dir, f)) }
		if tsc := bin("tsc"); at("tsconfig.json") && tsc != "" {
			// The whole package gets checked; keep the lines about changed files.
			section("tsc --noEmit (lines about changed files)"+where, grep(run(tsc, "--noEmit", "-p", "."), func(l string) bool {
				return slices.ContainsFunc(rel, func(f string) bool { return strings.Contains(l, f) })
			}), 60)
		}
		if biome := bin("biome"); biome != "" && Up(repo, dir, "biome.json", "biome.jsonc") != "" {
			section("biome lint"+where, run(biome, append([]string{"lint", "--colors=off"}, rel...)...), 80)
		} else if eslint := bin("eslint"); eslint != "" {
			section("eslint"+where, run(eslint, append([]string{"--no-color"}, rel...)...), 80)
		}
		addedHere := under(dir, added)
		if at("tsconfig.json") {
			section("oxlint --type-aware (added lines)"+where, grep(run("oxlint", append([]string{
				"--type-aware", "--disable-nested-config",
				"-c", config("oxlintrc.json", oxlintConfig), "-f", "unix",
			}, rel...)...), func(l string) bool {
				m := atLine.FindStringSubmatch(l)
				return m != nil && addedHere[m[1]+":"+m[2]] &&
					(!strings.Contains(l, "no-floating-promises") || !fireAndForget.MatchString(lineOf(ctx, repo, filepath.Join(dir, m[1]), m[2])))
			}), 40)
		}
		if pkg, _ := proc.ReadFile(ctx, filepath.Join(repo, dir, "package.json")); bytes.Contains(pkg, []byte(`"drizzle-orm"`)) {
			if src := slices.DeleteFunc(slices.Clone(rel), testFile.MatchString); len(src) > 0 {
				rules := config("drizzle-rules.yml", drizzleRules+"---\n"+strings.ReplaceAll(drizzleRules, "language: TypeScript", "language: Tsx"))
				section("ast-grep (Drizzle)"+where, run("ast-grep", append([]string{"scan", "-r", rules, "--format", "github"}, src...)...), 40)
			}
			if kit := bin("drizzle-kit"); kit != "" && slices.ContainsFunc(js, drizzlePath.MatchString) {
				cfgs, _ := filepath.Glob(filepath.Join(repo, dir, "drizzle*.config.*"))
				for _, c := range cfgs {
					if s := drizzleDrift(ctx, repo, kit, filepath.Base(c), run); s != "" {
						section("drizzle-kit ("+filepath.Join(dir, filepath.Base(c))+")", s, 40)
					}
				}
			}
		}
		// Dead code, import cycles, copies and complex functions across the whole package; kept where the change added lines.
		section("fallow (unused code, import cycles, duplicates the change added)"+where, fallowNew(run("fallow",
			"--only", "dead-code,dupes,architecture,health", "--format", "compact", "--no-cache", "--quiet"), addedHere, relTo(dir, changed)), 40)
		section("fallow security (injection, SSRF, secrets on added lines; candidates)"+where, fallowSecurity(run("fallow", "security",
			"--format", "json", "--quiet"), addedHere), 40)
		// Tests that import the changed files; a failure is evidence, the summary is at the end.
		t := relTo(dir, slices.DeleteFunc(slices.Clone(js), func(f string) bool { return !tests(f) }))
		vitest, jest := bin("vitest"), bin("jest")
		switch {
		case len(t) == 0:
		case vitest != "":
			section("vitest related (tests of changed files)"+where, run(vitest, append([]string{"related", "--run", "--reporter=dot"}, t...)...), -60)
		case jest != "":
			section("jest --findRelatedTests"+where, run(jest, append([]string{"--ci", "--passWithNoTests", "--findRelatedTests"}, t...)...), -60)
		}
	}

	for _, root := range roots(repo, match(goFile), "go.mod", &out) {
		dir, run, where := root.dir, in(root.dir), titled(root.dir)
		section("gofmt -l"+where, run("gofmt", append([]string{"-l"}, relTo(dir, root.files)...)...), 80)
		var pkgs, tested []string
		for _, f := range root.files {
			p := "./" + filepath.Dir(relTo(dir, []string{f})[0])
			pkgs = append(pkgs, p)
			if tests(f) {
				tested = append(tested, p)
			}
		}
		slices.Sort(pkgs)
		pkgs = slices.Compact(pkgs)
		slices.Sort(tested)
		tested = slices.Compact(tested)
		section("go vet"+where, run("go", append([]string{"vet"}, pkgs...)...), 80)
		section("staticcheck"+where, run("staticcheck", pkgs...), 80)
		// onAdded keeps a tool's path:line lines (path relative to dir) on lines the change added.
		addedHere := under(dir, added)
		onAdded := func(l string) bool {
			m := atLine.FindStringSubmatch(l)
			return m != nil && addedHere[m[1]+":"+m[2]]
		}
		// The repo's own config, as for biome and eslint, else the bot's. Absolute paths, made relative to dir: a
		// config's relative-path-mode would make them relative to wherever it lives.
		abs := filepath.Join(repo, dir)
		resolved, _ := filepath.EvalSymlinks(abs)
		rel := strings.NewReplacer(abs+"/", "", resolved+"/", "")
		lint := func(cfg string) string {
			return rel.Replace(run("golangci-lint", append([]string{
				"run", "--config=" + cfg, "--path-mode=abs", "--max-issues-per-linter=0",
				"--max-same-issues=0", "--output.text.print-issued-lines=false", "--show-stats=false",
			}, pkgs...)...))
		}
		title, res := "golangci-lint (added lines)", ""
		own := Up(repo, dir, ".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json")
		if own != "" {
			res = lint(filepath.Join(repo, own))
		}
		if own == "" || strings.Contains(res, "can't load config") { // none, or a v1 config
			res = lint(config("golangci.yml", golangciConfig))
		} else {
			title += ", " + own
		}
		section(title+where, grep(res, onAdded), 80)
		// Functions nothing reaches from a main or a test, declared on added lines: new dead code, as fallow finds for JS.
		section("deadcode (unreachable functions the change added)"+where, grep(run("deadcode", "-test", "./..."), onAdded), 40)
		// -short skips what tests mark as slow; testcontainers get Docker from dind.
		if len(tested) > 0 {
			js := run("go", append([]string{"test", "-json", "-short", "-count=1", "-timeout=4m"}, tested...)...)
			section("go test -short (changed packages; failed tests only)"+where, testFailures(js), 80)
			testRuns(js, &ran)
		}
	}

	if lua := match(luaFile); len(lua) > 0 {
		args := []string{"--no-color", "--codes"}
		if !has(".luacheckrc") {
			args = append(args, "--no-global", "--no-max-line-length")
		}
		sh("luacheck", 80, "luacheck", append(args, lua...)...)
	}
	if sql := match(sqlFile); len(sql) > 0 {
		var b strings.Builder
		for _, f := range sql {
			src, err := proc.ReadFile(ctx, filepath.Join(repo, f))
			if err != nil {
				continue
			}
			up, _, _ := strings.Cut(string(src), "-- +goose Down") // a rollback doesn't run on deploy
			args := []string{"--reporter", "json", "--stdin-filepath", f, "--exclude", squawkExclude}
			if !strings.Contains(up, "+goose NO TRANSACTION") { // goose and drizzle-kit migrate run each file in one
				args = append(args, "--assume-in-transaction")
			}
			c := proc.Command(ctx, repo, env, "squawk", args...)
			c.Stdin = strings.NewReader(up)
			js, _ := c.Output()
			var vs []struct {
				File, Message, Help string
				Line                int
				Rule                string `json:"rule_name"`
			}
			if json.Unmarshal(js, &vs) != nil {
				b.Write(js)
			}
			for _, v := range vs {
				fmt.Fprintf(&b, "%s:%d: %s: %s %s\n", v.File, v.Line+1, v.Rule, v.Message, v.Help) // squawk counts from 0
			}
		}
		section("squawk (Postgres migration safety)", b.String(), 60)
	}
	if s := match(shellFile); len(s) > 0 {
		sh("shellcheck", 80, "shellcheck", append([]string{"-f", "gcc"}, s...)...)
	}
	if w := match(workflow); len(w) > 0 { // shellchecks the run: steps too
		sh("actionlint (GitHub workflows)", 80, "actionlint", append([]string{"-no-color"}, w...)...)
		// Security: credentials left in checkouts, template injection, unpinned actions. --offline: no GitHub token.
		sh("zizmor (workflow security)", 40, "zizmor", append([]string{"-q", "--offline", "--format", "github"}, w...)...)
	}
	if m := match(manifest); len(m) > 0 { // lockfiles are cut from the diff, so a vulnerable bump only shows here
		var dirs []string
		for _, f := range m {
			dirs = append(dirs, filepath.Dir(f))
		}
		slices.Sort(dirs)
		sh("osv-scanner (known vulnerabilities in the dependencies; check whether this PR added them)", 60,
			"osv-scanner", append([]string{"scan", "source", "--format", "table"}, slices.Compact(dirs)...)...)
	}

	if s := unlisted(ctx, repo, added, files, output); s != "" {
		section("names the change added beside existing ones, missing where those are listed elsewhere (do these lists need it?)", s, 10)
	}

	for _, c := range scripts {
		sh("repo checks ("+c.Name+")", 120, "sh", append([]string{c.File, "--"}, files...)...)
	}
	if len(clean) > 0 {
		fmt.Fprintf(&out, "\n### No findings\n%s\n", strings.Join(clean, ", "))
	}
	s := strings.TrimSpace(out.String())
	if ctx.Err() != nil {
		s += "\n(checks timed out)"
	}
	log.Printf("checks: %d bytes in %s", len(s), time.Since(started).Round(time.Second))
	return text.Truncate(s, 12_000), ran
}

// testRuns adds what go test -json reports to t: a test's outcome, a package that does not build. A test that ran
// in two packages under one name fails if either fails.
func testRuns(js string, t *Tests) {
	for l := range strings.SplitSeq(js, "\n") {
		var e struct{ Action, Package, ImportPath, Test string }
		if json.Unmarshal([]byte(l), &e) != nil {
			continue
		}
		switch {
		case e.Action == "build-fail" || e.Action == "fail" && e.Test == "" && strings.Contains(e.Package, "[build failed]"):
			if p, _, _ := strings.Cut(cmp.Or(e.ImportPath, e.Package), " "); !slices.Contains(t.Broken, p) { // "q [q.test]"
				t.Broken = append(t.Broken, p)
			}
		case e.Test != "" && (e.Action == "pass" || e.Action == "fail" || e.Action == "skip"):
			if t.Ran == nil {
				t.Ran = map[string]string{}
			}
			if t.Ran[e.Test] != "fail" {
				t.Ran[e.Test] = e.Action
			}
		}
	}
}

// testFailures keeps what go test -json printed for failed tests, builds and packages: a failed package prints
// the logs of every test, the passed ones too. Tests failing with the same message (a database the sandbox
// lacks) show once.
func testFailures(js string) string {
	out, seen := map[string]string{}, map[string]int{}
	var rest, failed []string
	var more []int
	for _, l := range strings.Split(js, "\n") {
		var e struct{ Action, Package, ImportPath, Test, Output string }
		if strings.TrimSpace(l) == "" {
			continue
		} else if json.Unmarshal([]byte(l), &e) != nil { // the go command's own errors
			rest = append(rest, l)
			continue
		}
		k := cmp.Or(e.ImportPath, e.Package) + " " + e.Test
		switch e.Action {
		case "output", "build-output":
			if !strings.HasPrefix(e.Output, "=== ") {
				out[k] += e.Output
			}
		case "fail", "build-fail":
			o := strings.TrimRight(out[k], "\n")
			msg := strings.TrimSpace(testFrame.ReplaceAllString(o, ""))
			if i, ok := seen[msg]; ok && msg != "" && e.Test != "" {
				more[i]++
				continue
			}
			seen[msg] = len(failed)
			failed, more = append(failed, o), append(more, 0)
		}
	}
	for i, n := range more {
		if n > 0 {
			failed[i] += fmt.Sprintf("\n(%d more tests failed the same way)", n)
		}
	}
	return strings.Join(append(rest, failed...), "\n")
}

// testFrame matches what differs between tests failing the same way: the --- FAIL line and the file:line prefix.
var testFrame = regexp.MustCompile(`(?m)^\s*--- FAIL: .*$|^\s*\S+\.go:\d+: `)

// drizzleDrift runs drizzle-kit (kit) generate with cfg in the package output runs in: files it writes are a
// migration the schema change lacks, and get removed again. Without a TTY a rename question fails, which means
// the same. Then drizzle-kit check. git status names files from the repo root, wherever it runs.
func drizzleDrift(ctx context.Context, repo, kit, cfg string, output func(string, ...string) string) string {
	clean := func(s string) string { return grep(s, func(l string) bool { return !drizzleNoise.MatchString(l) }) }
	before := strings.Split(output("git", "status", "--porcelain", "-uall"), "\n")
	gen := output(kit, "generate", "--config="+cfg)
	var made []string
	var sql string
	for _, l := range strings.Split(output("git", "status", "--porcelain", "-uall"), "\n") {
		if len(l) < 4 || slices.Contains(before, l) {
			continue
		}
		f := l[3:]
		made = append(made, f)
		if strings.HasSuffix(f, ".sql") {
			b, _ := proc.ReadFile(ctx, filepath.Join(repo, f))
			sql += string(b)
		}
		if l[:2] == "??" {
			_ = proc.Remove(filepath.Join(repo, f))
		} else {
			output("git", "checkout", "-q", "--", ":/"+f)
		}
	}
	var msg []string
	if len(made) > 0 {
		msg = append(msg, "The schema has changes no migration covers; drizzle-kit generate would write "+strings.Join(made, ", ")+":", sql)
	} else if !strings.Contains(gen, "No schema changes") {
		if strings.Contains(gen, "TTY") {
			msg = append(msg, "drizzle-kit generate stopped at a rename question: the schema has changes no migration covers.")
		}
		msg = append(msg, clean(gen))
	}
	if chk := output(kit, "check", "--config="+cfg); !strings.Contains(chk, "Everything's fine") {
		msg = append(msg, clean(chk))
	}
	return strings.TrimSpace(strings.Join(msg, "\n"))
}

// fallowNew keeps fallow's findings (compact: kind:path[:line[-end]]:detail) on lines the change added: a new unused
// export, a new file nothing imports, a copy (with its original), an import cycle through a changed file.
func fallowNew(out string, added map[string]bool, changed []string) string {
	keep, dups := map[string]bool{}, map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		kind, rest, _ := strings.Cut(l, ":")
		m := atLine.FindStringSubmatch(rest)
		switch {
		case kind == "unused-file":
			keep[l] = added[rest+":1"]
		case kind == "circular-dependency":
			keep[l] = slices.ContainsFunc(changed, func(f string) bool { return strings.Contains(rest, f) })
		case m != nil:
			a, _ := strconv.Atoi(m[2])
			b, _ := strconv.Atoi(cmp.Or(m[3], m[2]))
			for n := a; n <= b && !keep[l]; n++ {
				keep[l] = added[fmt.Sprintf("%s:%d", m[1], n)]
			}
			if p := dupPrint.FindString(l); keep[l] && p != "" {
				dups[p] = true
			}
		}
	}
	return grep(out, func(l string) bool { return keep[l] || dups[dupPrint.FindString(l)] })
}

// fallowSecurity turns fallow security's JSON into a line per candidate sink on an added line.
func fallowSecurity(out string, added map[string]bool) string {
	var r struct {
		Findings []struct {
			Path, Category, Severity, Evidence string
			Line, CWE                          int
		} `json:"security_findings"`
	}
	if i := strings.IndexByte(out, '{'); i >= 0 { // warnings on stderr come first; Decode stops after the object
		_ = json.NewDecoder(strings.NewReader(out[i:])).Decode(&r)
	}
	var b strings.Builder
	for _, f := range r.Findings {
		if added[fmt.Sprintf("%s:%d", f.Path, f.Line)] {
			e, _, _ := strings.Cut(f.Evidence, ". ") // the rest is the same "Candidate for verification" boilerplate
			fmt.Fprintf(&b, "%s:%d: %s %s (CWE-%d): %s\n", f.Path, f.Line, f.Severity, f.Category, f.CWE, e)
		}
	}
	return b.String()
}

// unlisted finds the lists a change extended without their copies: an added line naming a new constant (no file
// outside the change has it) beside two or more existing ones, and the files that name two of those but not the
// new one, at their line naming the most: a key added to a workflow but not to the catalog or docs listing them.
func unlisted(ctx context.Context, repo string, added map[string]bool, files []string, output func(string, ...string) string) string {
	var out []string
	seen := map[string]bool{}
	tried := 0
	for _, k := range slices.Sorted(maps.Keys(added)) {
		i := strings.LastIndexByte(k, ':')
		names := slices.Compact(slices.Sorted(slices.Values(constName.FindAllString(lineOf(ctx, repo, k[:i], k[i+1:]), -1))))
		if len(names) < 3 {
			continue
		}
		if tried++; tried > 30 { // a git grep each
			break
		}
		args := []string{"grep", "-n", "-I", "-w", "-F"}
		for _, n := range names {
			args = append(args, "-e", n)
		}
		in := map[string]map[string]bool{}                  // name → the files naming it
		line, most := map[string]string{}, map[string]int{} // file → its line naming the most of them, and how many
		for _, l := range strings.Split(output("git", args...), "\n") {
			m := atLine.FindStringSubmatch(l)
			if m == nil || m[3] != "" {
				continue
			}
			on := slices.DeleteFunc(constName.FindAllString(l[len(m[1])+len(m[2])+2:], -1), func(n string) bool { return !slices.Contains(names, n) })
			for _, n := range on {
				if in[n] == nil {
					in[n] = map[string]bool{}
				}
				in[n][m[1]] = true
			}
			if c := len(slices.Compact(slices.Sorted(slices.Values(on)))); c > most[m[1]] {
				line[m[1]], most[m[1]] = m[2], c
			}
		}
		var fresh, old []string
		for _, n := range names {
			if slices.ContainsFunc(slices.Collect(maps.Keys(in[n])), func(f string) bool { return !slices.Contains(files, f) }) {
				old = append(old, n)
			} else {
				fresh = append(fresh, n)
			}
		}
		if len(fresh) == 0 || len(old) < 2 {
			continue
		}
		var hits []string
		for _, f := range slices.Sorted(maps.Keys(line)) {
			has := func(n string) bool { return in[n][f] }
			missing := slices.DeleteFunc(slices.Clone(fresh), has)
			named := slices.DeleteFunc(slices.Clone(old), func(n string) bool { return !has(n) })
			if len(missing) > 0 && len(named) >= 2 && !seen[f+" "+strings.Join(missing, " ")] {
				seen[f+" "+strings.Join(missing, " ")] = true
				hits = append(hits, fmt.Sprintf("%s:%s names %s but not %s (added beside them at %s)",
					f, line[f], strings.Join(named, ", "), strings.Join(missing, ", "), k))
			}
		}
		if len(hits) <= 3 { // more are names in common use, not a list kept in copies
			out = append(out, hits...)
		}
	}
	return strings.Join(out, "\n")
}

// lineOf returns line n (1-based, as text) of the repo's file f.
func lineOf(ctx context.Context, repo, f, n string) string {
	b, _ := proc.ReadFile(ctx, filepath.Join(repo, f))
	i, _ := strconv.Atoi(n)
	if l := strings.Split(string(b), "\n"); i > 0 && i <= len(l) {
		return l[i-1]
	}
	return ""
}

// grep keeps the lines of s that keep returns true for.
func grep(s string, keep func(string) bool) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if keep(l) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// lines keeps the first n lines of s, or the last -n.
func lines(s string, n int) string {
	l := strings.Split(s, "\n")
	if n >= 0 && len(l) > n {
		return strings.Join(l[:n], "\n") + "\n…"
	}
	if n < 0 && len(l) > -n {
		return "…\n" + strings.Join(l[len(l)+n:], "\n")
	}
	return s
}

// root is a package dir (repo-relative, "." = the repo root) and the changed files in it.
type root struct {
	dir   string
	files []string
}

// roots groups files by the nearest dir up from each that holds marker; files with none drop out. The maxRoots
// packages with the most changed files come first; the rest are named in out as not checked.
func roots(repo string, files []string, marker string, out *strings.Builder) []root {
	by := map[string][]string{}
	for _, f := range files {
		if m := Up(repo, filepath.Dir(f), marker); m != "" {
			by[filepath.Dir(m)] = append(by[filepath.Dir(m)], f)
		}
	}
	var rs []root
	for _, d := range slices.Sorted(maps.Keys(by)) {
		rs = append(rs, root{d, by[d]})
	}
	slices.SortStableFunc(rs, func(a, b root) int { return cmp.Compare(len(b.files), len(a.files)) })
	if len(rs) > maxRoots {
		var skipped []string
		for _, r := range rs[maxRoots:] {
			skipped = append(skipped, r.dir)
		}
		fmt.Fprintf(out, "\n### %s checks skipped (more than %d packages)\n%s\n", marker, maxRoots, strings.Join(skipped, ", "))
		rs = rs[:maxRoots]
	}
	return rs
}

// Up returns the first of names in dir or a parent of it up to the repo root, repo-relative; "" = none.
func Up(repo, dir string, names ...string) string {
	for d := dir; ; d = filepath.Dir(d) {
		for _, n := range names {
			if proc.Exists(filepath.Join(repo, d, n)) {
				return filepath.Join(d, n)
			}
		}
		if d == "." || d == "/" {
			return ""
		}
	}
}

// relTo makes repo-relative files relative to dir, dropping those outside it.
func relTo(dir string, files []string) []string {
	var out []string
	for _, f := range files {
		if r, err := filepath.Rel(dir, f); err == nil && filepath.IsLocal(r) {
			out = append(out, r)
		}
	}
	return out
}

// under keeps the "path:line" keys under dir, relative to it: tools in a package print paths relative to it.
func under(dir string, m map[string]bool) map[string]bool {
	if dir == "." {
		return m
	}
	out := map[string]bool{}
	for k, v := range m {
		if r, ok := strings.CutPrefix(k, dir+"/"); ok {
			out[r] = v
		}
	}
	return out
}

// titled names a package in a check's title; the root gets nothing.
func titled(dir string) string {
	if dir == "." {
		return ""
	}
	return " in " + dir + "/ (paths relative to it)"
}

var (
	yamlComment = regexp.MustCompile(`(?m)(^|\s)#.*$`)
	// A workflow's top-level on: with the indented lines under it.
	onBlock = regexp.MustCompile(`(?m)^["']?on["']?:(.*(?:\n(?:[ \t]+.*)?)*)`)
	// push and pull request events as a key or list item: "on: push", "[push, pull_request]", "  pull_request:".
	prEvent = regexp.MustCompile(`(?m)(^|[\s\[,{])(push|pull_request|pull_request_target|merge_group)\s*([:,\]}]|$)`)
	// A reusable workflow from the same repo: "uses: ./.github/workflows/tests.yml".
	localCall = regexp.MustCompile(`uses:\s*["']?\./\.github/workflows/([^\s"']+)`)
)

// CITests is the CI file that runs tests on pull requests (a GitHub workflow, GitLab CI), "" = none: then the
// review runs them. A workflow counts if a push or pull request starts it, with the reusable workflows it calls.
func CITests(ctx context.Context, repo string) string {
	read := func(f string) string {
		b, _ := proc.ReadFile(ctx, f)
		return yamlComment.ReplaceAllString(string(b), "")
	}
	if ciTest.MatchString(read(filepath.Join(repo, ".gitlab-ci.yml"))) {
		return ".gitlab-ci.yml"
	}
	dir := filepath.Join(repo, ".github", "workflows")
	fs, _ := filepath.Glob(filepath.Join(dir, "*.y*ml"))
	for _, f := range fs {
		w := read(f)
		if on := onBlock.FindStringSubmatch(w); on == nil || !prEvent.MatchString(on[1]) {
			continue
		}
		for _, c := range localCall.FindAllStringSubmatch(w, -1) {
			w += read(filepath.Join(dir, filepath.Base(c[1])))
		}
		if ciTest.MatchString(w) {
			return filepath.Base(f)
		}
	}
	return ""
}
