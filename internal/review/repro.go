package review

import (
	"context"
	"diffharbor/internal/checks"
	"diffharbor/internal/proc"
	"diffharbor/internal/text"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Repro opens the evidence of a finding a test the verifier wrote reproduced.
const Repro = "Ran a test for it: "

// maxRepros bounds the repro tests a review runs, reproTimeout one test's run.
const (
	maxRepros    = 3
	reproTimeout = 30 * time.Second
)

// reproSystem is added to the verify prompt where a changed file's tests run.
const reproSystem = `
For at most 3 findings you keep as major or critical, add "repro":{"file":"<new test file next to the code, named as the repo names tests; must not exist>","name":"<Go: TestX; JS: the test's title, no suite>","source":"<the whole file>"}:
a minimal test that FAILS if the finding is right (it asserts the correct behaviour). Do not run it: it is run for you in the checkout and removed after, a pass refutes the finding.
Leave it out where no unit test shows the finding (design, ordering, needs network or services).
`

// reproTest is a test that fails if its finding is right.
type reproTest struct {
	File   string `json:"file"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

// reproduce runs the repro test of each kept major or critical finding, as far as the review's budget allows, and
// settles it: reproduced (it fails as the finding predicts) keeps the finding with the run as evidence, refuted (it
// passes) lowers it to minor, anything else (no runner, tests off, build error, no budget) leaves it as it was.
func reproduce(ctx context.Context, in Input, kept []Comment) {
	for i := range kept {
		c := &kept[i]
		r := c.repro
		if r == nil || !Blocks(c.Severity) || r.Name == "" || r.Source == "" || !filepath.IsLocal(r.File) || !checks.IsTest(r.File) ||
			!defines(r) || !in.Settings.RunsTests(r.File) || in.repros.Add(1) > maxRepros {
			continue
		}
		var cmd, out, res string
		held(in.reproSlot, func() { cmd, out, res = runRepro(ctx, in.Sandbox, r) })
		switch res {
		case "fail":
			c.Evidence = Repro + "`" + cmd + "` fails, as the finding predicts."
			c.Body += "\n\n" + c.Evidence + "\n\n" + Fence(text.Tail(strings.TrimSpace(out)))
		case "pass":
			log.Printf("repro refuted %s:%d %s: `%s` passes", c.Path, c.Line, c.Title, cmd)
			c.Severity = "minor"
			c.Evidence = "A test for it passes: `" + cmd + "` passes though the finding predicts a failure; lowered to minor."
			c.Body += "\n\n" + c.Evidence
		default:
			log.Printf("repro not attempted %s:%d %s: %s", c.Path, c.Line, c.Title, text.Tail(strings.TrimSpace(out)))
		}
	}
}

// defines reports whether r's source defines the test it runs: a name of a test already in the checkout must not
// refute a finding by passing. Go: func Name(t *testing.T); JS: the name occurs in the source.
func defines(r *reproTest) bool {
	if !strings.HasSuffix(r.File, ".go") {
		return strings.Contains(r.Source, r.Name)
	}
	return regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(r.Name) + `\(\w+ \*testing\.T\)`).MatchString(r.Source)
}

// runRepro runs r in the checkout, the file removed after, and returns the command, its output and the outcome
// ("fail", "pass", "" = not run). Only a new file in an existing dir: the PR's files are never overwritten.
func runRepro(ctx context.Context, sb *proc.Sandbox, r *reproTest) (cmd, out, res string) {
	repo := sb.Repo()
	p := filepath.Join(repo, r.File)
	if _, err := os.Lstat(p); err == nil { // not proc.Exists: a dangling symlink the PR placed is a file too
		return "", "file exists", ""
	}
	dir, args := checks.TestCmd(ctx, sb, repo, r.File, r.Name, reproTimeout)
	if args == nil {
		return "", "no test runner", ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*reproTimeout)
	defer cancel()
	if err := sb.WriteFile(ctx, p, []byte(r.Source)); err != nil {
		return "", err.Error(), ""
	}
	defer func() { _ = sb.Remove(p) }()
	out = runTest(ctx, sb, filepath.Join(repo, dir), args)
	return fmt.Sprintf("%s %s", filepath.Base(args[0]), strings.Join(args[1:], " ")), out, checks.Outcome(out, r.Name)
}
