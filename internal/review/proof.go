package review

import (
	"context"
	"diffharbor/internal/checks"
	"diffharbor/internal/proc"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Ran opens the evidence of a finding a run of the code made, not a model.
const Ran = "Ran on the base: "

// proofOnBase runs the test proof ("TestX/sub", Story.Proof) against the base versions of the PR's code files, in a
// worktree of head in sb's dir: test files stay the PR's, the checkout is untouched. A test that still passes there does
// not prove the change: a finding on the test. Minor, not major: the run is exact, but the proof is the model's pick
// and the change may be covered elsewhere. Go (go test names) and JS ("suite > test", checks.TestCmd's runners);
// tests, the run of head's tests, says whether proof already fails on head (it only knows Go test names). A test not found once in the changed test
// files, review.json turning its tests off, no JS runner, or any error, skips it.
func proofOnBase(ctx context.Context, sb *proc.Sandbox, repo, proof string, tests map[string]string, cfg Settings) []Comment {
	if proof == "" || tests[proof] == "fail" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	git := func(at string, args ...string) ([]byte, error) {
		return proc.Run(sb.Command(ctx, at, nil, "git", args...))
	}
	mb, err := git(repo, "merge-base", "refs/base", "HEAD")
	if err != nil {
		return nil
	}
	base := strings.TrimSpace(string(mb))
	st, err := git(repo, "diff", "--name-status", "-z", "--no-renames", base, "HEAD")
	if err != nil {
		return nil
	}
	top, _, _ := strings.Cut(proof, "/")
	last := proof
	if i := strings.LastIndex(proof, " > "); i >= 0 {
		last = proof[i+3:]
	}
	goDecl := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(top) + `\(`)
	jsDecl := regexp.MustCompile("['\"`]" + regexp.QuoteMeta(last) + "['\"`]")
	var added, restore []string // code files the PR adds, and those it changes or deletes
	var testFile string
	var line, found int
	f := strings.Split(strings.TrimSuffix(string(st), "\x00"), "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		status, p := f[i], f[i+1]
		switch {
		case !checks.IsTest(p):
			if status == "A" {
				added = append(added, p)
			} else {
				restore = append(restore, p)
			}
		case status != "D":
			if b, err := sb.ReadFile(ctx, filepath.Join(repo, p)); err == nil { // as the repo's user: the PR's symlink reads nothing of root's
				decl := jsDecl
				if strings.HasSuffix(p, ".go") {
					decl = goDecl
				}
				if m := decl.FindIndex(b); m != nil {
					testFile, line, found = p, 1+strings.Count(string(b[:m[0]]), "\n"), found+1
				}
			}
		}
	}
	if found != 1 || !cfg.RunsTests(testFile) && !cfg.LeftToCI(testFile) {
		return nil
	}
	dir, cmd := checks.TestCmd(ctx, sb, repo, testFile, proof, 90*time.Second)
	if cmd == nil {
		return nil
	}
	wt := filepath.Join(sb.Dir, "proof")
	if _, err := git(repo, "worktree", "add", "-q", "--detach", wt, "HEAD"); err != nil {
		log.Println("proof:", err)
		return nil
	}
	defer func() { _, _ = git(repo, "worktree", "remove", "--force", wt) }()
	if len(added) > 0 {
		if _, err := git(wt, append([]string{"rm", "-q", "-f", "--"}, added...)...); err != nil {
			log.Println("proof:", err)
			return nil
		}
	}
	if len(restore) > 0 {
		if _, err := git(wt, append([]string{"checkout", "-q", base, "--"}, restore...)...); err != nil {
			log.Println("proof:", err)
			return nil
		}
	}
	if !strings.HasSuffix(testFile, ".go") {
		linkModules(ctx, sb, repo, wt, dir)
	}
	// no tests to run prints "ok" too: only the test's own pass line counts
	if checks.Outcome(runTest(ctx, sb, filepath.Join(wt, dir), cmd), proof) != "pass" {
		return nil
	}
	ev := Ran + proof + " passes without the change to the code, so it does not prove it."
	fd := Comment{
		Path: testFile, Line: line, Severity: "minor", Title: "Test passes without the change",
		Body: fmt.Sprintf("`%s` is the test for this change, but it still passes with the code files at their base versions: "+
			"it does not fail without the change. Assert the new behaviour.\n\n%s%s", proof, Verified, ev),
		Evidence: ev,
	}
	if !cfg.Keep(fd) {
		return nil
	}
	return []Comment{fd}
}

// runTest runs cmd in dir and returns its output: a build or test failure is a result, not an error.
func runTest(ctx context.Context, sb *proc.Sandbox, dir string, cmd []string) string {
	out := proc.Capped{N: 1 << 20}
	c := sb.Command(ctx, dir, append(sb.ToolEnv(), "NO_COLOR=1"), cmd[0], cmd[1:]...)
	c.Stdout, c.Stderr = &out, &out
	_ = c.Run()
	return out.String()
}

// linkModules links the checkout's node_modules, in dir and each parent, into the worktree wt: the checks installed
// them there. ponytail: a workspace package they link resolves to the checkout's (head) code, not wt's.
func linkModules(ctx context.Context, sb *proc.Sandbox, repo, wt, dir string) {
	for d := dir; ; d = filepath.Dir(d) {
		if m := filepath.Join(repo, d, "node_modules"); proc.Exists(m) {
			_, _ = proc.Run(sb.Command(ctx, wt, nil, "ln", "-s", "--", m, filepath.Join(wt, d, "node_modules")))
		}
		if d == "." {
			return
		}
	}
}
