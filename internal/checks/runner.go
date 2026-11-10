package checks

import (
	"context"
	"diffharbor/internal/proc"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// IsTest reports whether path is a Go or JS test file.
func IsTest(path string) bool {
	return strings.HasSuffix(path, "_test.go") || jsFile.MatchString(path) && testFile.MatchString(path)
}

// binUp finds a tool in dir's node_modules/.bin or a parent's (workspaces hoist them); "" = none.
func binUp(repo, dir, tool string) string {
	if p := Up(repo, dir, filepath.Join("node_modules/.bin", tool)); p != "" {
		return filepath.Join(repo, p)
	}
	return ""
}

// TestCmd runs the one test proof names in file (repo-relative) verbosely: its dir (repo-relative) and the command.
// Go: go test, proof "TestX/sub". JS: the runner of file's package as Run finds it (vitest, jest, else bun test or
// node --test as the package's test script or lockfile says), proof "suite > test" or just "test". nil = no runner.
func TestCmd(ctx context.Context, sb *proc.Sandbox, repo, file, proof string, limit time.Duration) (string, []string) {
	dir := filepath.Dir(file)
	if goFile.MatchString(file) {
		top, subs, _ := strings.Cut(proof, "/")
		run := "^" + regexp.QuoteMeta(top) + "$"
		for s := range strings.SplitSeq(subs, "/") {
			if s != "" {
				run += "/^" + regexp.QuoteMeta(s) + "$"
			}
		}
		return dir, []string{"go", "test", "-v", "-short", "-count=1", "-timeout=" + limit.String(), "-run=" + run, "."}
	}
	pj := Up(repo, dir, "package.json")
	if !jsFile.MatchString(file) || pj == "" {
		return "", nil
	}
	pkg := filepath.Dir(pj)
	f, _ := filepath.Rel(pkg, file)
	pat := regexp.QuoteMeta(strings.ReplaceAll(proof, " > ", " ")) // runners match a test's name with its suites' joined by spaces
	script, _ := sb.ReadFile(ctx, filepath.Join(repo, pj))
	bun := Up(repo, pkg, "bun.lock", "bun.lockb", "bunfig.toml") != "" || strings.Contains(string(script), "bun test")
	switch vitest, jest := binUp(repo, pkg, "vitest"), binUp(repo, pkg, "jest"); {
	case vitest != "":
		return pkg, []string{vitest, "run", "--reporter=verbose", "--testNamePattern=" + pat, f}
	case jest != "":
		return pkg, []string{jest, "--ci", "--verbose", "--testNamePattern=" + pat, "--runTestsByPath", f}
	case bun:
		return pkg, []string{"bun", "test", "--test-name-pattern=" + pat, f}
	case strings.Contains(string(script), "node --test"):
		return pkg, []string{"node", "--test", "--test-name-pattern=" + pat, f}
	}
	return "", nil
}

var (
	passMark = []string{"--- PASS: ", "✓ ", "✔ ", "√ ", "(pass) "}
	failMark = []string{"--- FAIL: ", "× ", "✕ ", "✖ ", "(fail) "}
)

// Outcome reads the verbose output of TestCmd for proof: "fail", "pass" or "" when the test did not run (a build
// error, no test of that name, skipped). The runners mark a test by its own title, the last of a "suite > test".
func Outcome(out, proof string) string {
	if i := strings.LastIndex(proof, " > "); i >= 0 {
		proof = proof[i+3:]
	}
	res := ""
	for l := range strings.Lines(out) {
		l = strings.TrimSpace(l)
		if !strings.Contains(l, proof) {
			continue
		}
		for _, m := range failMark {
			if strings.HasPrefix(l, m) {
				return "fail"
			}
		}
		for _, m := range passMark {
			if strings.HasPrefix(l, m) {
				res = "pass"
			}
		}
	}
	return res
}
