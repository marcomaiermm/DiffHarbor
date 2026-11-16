package report

import (
	"context"
	"diffharbor/internal/proc"
	"diffharbor/internal/review"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// Commits count by the key in their subject, merges aside; a file two tickets' commits change is no ticket's own,
// a commit naming no ticket only counts as nameless, and the steps count by their files.
func TestSplitTickets(t *testing.T) {
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi = false
	sb := &proc.Sandbox{Dir: t.TempDir()}
	if err := os.Mkdir(sb.Repo(), 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", "-c", `set -e
export GIT_AUTHOR_NAME=a GIT_AUTHOR_EMAIL=a@x GIT_COMMITTER_NAME=a GIT_COMMITTER_EMAIL=a@x
git init -q -b main; echo 0 > base; git add .; git commit -qm base; git update-ref refs/base HEAD
git checkout -q -b pr
echo 1 > a; echo 1 > 'b c'; git add .; git commit -qm 'feat(abc-1): a'
echo 1 > shared; git add .; git commit -qm 'ABC-1 shared'
echo 2 > shared; echo 1 > d; git add .; git commit -qm 'fix: ABC-2 and XYZ-9 d'
echo 1 > e; git add .; git commit -qm 'chore: no ticket, UTF-8'
git checkout -q -b side refs/base; echo 1 > s; git add .; git commit -qm 'ABC-2 side'
git checkout -q pr; git merge -q --no-ff side -m 'Merge ABC-2'`)
	c.Dir = sb.Repo()
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	commits := review.Changes(context.Background(), sb)
	if len(commits) != 5 {
		t.Fatalf("commits: %+v", commits)
	}
	flow := []review.Step{{Files: []string{"a", "d"}}, {Files: []string{"shared"}}, {Files: []string{"e"}}}
	got := SplitTickets([]string{"ABC-1", "ABC-2", "ABC-3"}, commits, flow)
	want := "&{[{ABC-1 2 3 2 2} {ABC-2 2 3 2 2} {ABC-3 0 0 0 0}] 1 1}" // ABC-2: d, shared and s from the side branch
	if fmt.Sprint(got) != want {
		t.Fatalf("got  %v\nwant %s", got, want)
	}
	if SplitTickets(nil, commits, flow) != nil {
		t.Fatal("no keys")
	}
}
