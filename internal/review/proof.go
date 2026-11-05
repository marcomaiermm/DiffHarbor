package review

import (
	"context"
	"diffharbor/internal/proc"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Ran opens the evidence of a finding a run of the code made, not a model.
const Ran = "Ran on the base: "

// proofOnBase runs the test proof ("TestX/sub", Story.Proof) against the base versions of the PR's code files, in a
// worktree of head under dir: test files stay the PR's, the checkout is untouched. A test that still passes there does
// not prove the change: a finding on the test. Minor, not major: the run is exact, but the proof is the model's pick
// and the change may be covered elsewhere. Go only (go test names); tests, the run of head's tests, says whether
// proof already fails on head. A test not found once in the changed test files, review.json turning its tests off,
// or any error, skips it.
func proofOnBase(ctx context.Context, repo, dir, proof string, tests map[string]string, cfg Settings) []Comment {
	if proof == "" || tests[proof] == "fail" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	git := func(at string, args ...string) ([]byte, error) {
		return proc.Run(proc.Command(ctx, at, nil, "git", args...))
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
	top, subs, _ := strings.Cut(proof, "/")
	decl := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(top) + `\(`)
	var added, restore []string // code files the PR adds, and those it changes or deletes
	var testFile string
	var line, found int
	f := strings.Split(strings.TrimSuffix(string(st), "\x00"), "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		status, p := f[i], f[i+1]
		switch {
		case !strings.HasSuffix(p, "_test.go"):
			if status == "A" {
				added = append(added, p)
			} else {
				restore = append(restore, p)
			}
		case status != "D":
			if b, err := os.ReadFile(filepath.Join(repo, p)); err == nil { //nolint:gosec // a path of the diff, in the checkout
				if m := decl.FindIndex(b); m != nil {
					testFile, line, found = p, 1+strings.Count(string(b[:m[0]]), "\n"), found+1
				}
			}
		}
	}
	if found != 1 || !cfg.RunsTests(testFile) && !cfg.LeftToCI(testFile) {
		return nil
	}
	wt := filepath.Join(dir, "proof")
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
	run := "^" + regexp.QuoteMeta(top) + "$"
	for s := range strings.SplitSeq(subs, "/") {
		if s != "" {
			run += "/^" + regexp.QuoteMeta(s) + "$"
		}
	}
	out := proc.Capped{N: 1 << 20}
	c := proc.Command(ctx, filepath.Join(wt, filepath.Dir(testFile)), append(proc.ToolEnv(), "NO_COLOR=1"),
		"go", "test", "-v", "-short", "-count=1", "-timeout=90s", "-run", run, ".")
	c.Stdout, c.Stderr = &out, &out
	_ = c.Run() // a build or test failure is the proof holding
	// no tests to run prints "ok" too: only the test's own PASS line counts
	if !strings.Contains(out.String(), "--- PASS: "+proof+" (") {
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
