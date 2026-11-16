package report

import (
	"slices"
	"strings"
)

// CI is what the forge's CI said of the head when the review ran, and the CI workflows on the base. The page shows a
// failed check as "Blocked" with "Open the run on GitHub", the tests left to CI ("Left to CI ({Tests})", "No CI
// workflow on the base branch.") and "Tests and linters" (spec 4, 5.3, 5.10, 7.5). It is read once: a pending check
// stays "pending" on this version.
type CI struct {
	Checks []Check
	// Err is why the forge's checks weren't read (a missing App permission, a timeout): the page says they
	// weren't read. Checks is empty then.
	Err       string
	Workflows []string // the CI files on the base branch, read with git: .github/workflows/*, .gitlab-ci.yml
	Tests     string   // of Workflows, the one that runs the tests on a pull request (checks.CITests); "" for none
	Edited    bool     // the PR changes the workflow that runs the tests: "Left to CI (test.yml), which this PR edits"
}

// Check is one check run (GitHub) or pipeline job (GitLab) at the head.
type Check struct {
	Name   string
	Status string // "pending" or "completed"
	// Conclusion of a completed check in GitHub's words: success, failure, timed_out, cancelled, skipped, neutral,
	// action_required; "" while pending.
	Conclusion string
	Workflow   string // the workflow or pipeline it ran in: "ci", "#812"; "" when the forge doesn't say
	Path       string // the workflow's file, ".github/workflows/test.yml"; "" when the forge doesn't say
	URL        string // the check's page: the failed run's link
}

// Failed is a check that blocks the merge: only a failed run does (spec 4).
func (c Check) Failed() bool { return c.Conclusion == "failure" || c.Conclusion == "timed_out" }

// Edits reports whether files, the PR's paths, change the workflow that runs the tests: tests, the one on the base
// (checks.CITests), else the workflow file of any check, else, when the forge names none, any CI file.
func Edits(files []string, checks []Check, tests string) bool {
	var want []string
	switch {
	case tests == ".gitlab-ci.yml":
		want = []string{tests}
	case tests != "":
		want = []string{".github/workflows/" + tests}
	default:
		for _, c := range checks {
			if c.Path != "" {
				want = append(want, c.Path)
			}
		}
	}
	return slices.ContainsFunc(files, func(f string) bool {
		if len(want) > 0 {
			return slices.Contains(want, f)
		}
		return f == ".gitlab-ci.yml" || strings.HasPrefix(f, ".github/workflows/")
	})
}

// Members are the people with access to the repo when the review ran, for "No current member changed or reviewed
// this code in 12 months" (5.11). Logins only, sorted by name: never a count per person, never an order of people.
type Members struct {
	Logins []string
	Err    string // why the forge's list wasn't read (a 403): the page leaves the line out
}
