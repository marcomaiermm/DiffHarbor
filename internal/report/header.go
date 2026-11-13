package report

import (
	"context"
	"diffharbor/internal/proc"
	"strings"
	"time"
)

// PR is what the header says of the pull request: from the forge's payload, the commits from git.
type PR struct {
	Created    time.Time
	Author     string
	Head, Base string // branch names
	Body       string // the description, uncut
	Commits    int    // without merges
	Merges     bool   // the branch holds merges: the header says "without merges" only then
}

// Header sets pr's commit count and whether merges were left out, from a Checkout's history: refs/base..HEAD.
// Both stay zero when git fails.
func Header(ctx context.Context, sb *proc.Sandbox, pr PR) PR {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := proc.Run(sb.Command(ctx, sb.Repo(), nil, "git", "log", "--format=%P", "refs/base..HEAD"))
	if err != nil {
		return pr
	}
	for l := range strings.Lines(string(out)) {
		if strings.Contains(strings.TrimSpace(l), " ") { // two parents
			pr.Merges = true
		} else {
			pr.Commits++
		}
	}
	return pr
}
