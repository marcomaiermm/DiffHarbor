package report

import (
	"context"
	"diffharbor/internal/proc"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// Days count to the PR's creation, renames are followed, a commit of 150 files isn't a change, a bot is no person,
// the mailmap merges an alias, and what happened after the PR opened doesn't count.
func TestLearn(t *testing.T) {
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi = false
	sb := &proc.Sandbox{Dir: t.TempDir()}
	dir := sb.Repo()
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(who, date string, args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		name, email, _ := strings.Cut(who, "|")
		c.Env = append(c.Environ(), "GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email, "GIT_COMMITTER_NAME="+name, "GIT_COMMITTER_EMAIL="+email,
			"GIT_AUTHOR_DATE="+date+"T12:00:00Z", "GIT_COMMITTER_DATE="+date+"T12:00:00Z")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	sh := func(script string) {
		c := exec.Command("sh", "-c", "set -e\n"+script)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	git("o|o@x", "2025-01-10", "init", "-q", "-b", "main")
	sh(`echo a > a.txt; echo b > b.txt; echo c > c.txt
echo 'Alice <alice@x> <alias@x>' > .mailmap
git add .`)
	git("o|o@x", "2025-01-10", "commit", "-qm", "one") // 15 months before: no person of the 12
	sh(`echo 2 >> a.txt`)
	git("Alias|alias@x", "2026-02-01", "commit", "-qam", "two")
	git("Alice|alice@x", "2026-03-01", "mv", "b.txt", "d.txt")
	git("Alice|alice@x", "2026-03-01", "commit", "-qm", "rename")
	sh(`echo 2 >> c.txt`)
	git("Zed|zed@x", "2026-03-15", "commit", "-qam", "zed")
	sh(`echo 3 >> c.txt`)
	git("dependabot[bot]|1+dependabot[bot]@users.noreply.github.com", "2026-04-01", "commit", "-qam", "bump")
	sh(`mkdir gen; i=0; while [ $i -lt 149 ]; do echo $i > gen/f$i; i=$((i+1)); done; echo 9 >> a.txt; git add .`)
	git("Bob|bob@x", "2026-05-01", "commit", "-qm", "bulk") // 150 files with a.txt
	sh(`echo 4 >> a.txt`)
	git("Bob|bob@x", "2026-07-01", "commit", "-qam", "after")
	sh(`git update-ref refs/base HEAD
git checkout -q -b pr; echo 1 > p1; git add .; git commit -q -m p1
git checkout -q -b side refs/base; echo 1 > s1; git add .; git commit -q -m s1
git checkout -q pr; git merge -q --no-ff side -m merge; echo 1 > p2; git add .; git commit -q -m p2`)

	created := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	ks := Learn(context.Background(), sb, []string{"a.txt", "d.txt", "c.txt", "gen/f0", "gone.txt"}, created)
	want := map[string]Know{
		"a.txt": {Days: 120, People: []string{"Alice"}},
		"d.txt": {Days: 92, People: []string{"Alice"}},
		"c.txt": {Days: 61, People: []string{"Zed"}},
	}
	if fmt.Sprint(ks) != fmt.Sprint(want) {
		t.Fatalf("Learn:\n%v\nwant\n%v", ks, want)
	}
	if g := Aggregate(ks, []string{"a.txt", "c.txt", "d.txt", "gone.txt"}); g.Known != 3 || g.Median != 92 || g.Oldest != 120 || !slices.Equal(g.People, []string{"Alice", "Zed"}) {
		t.Fatalf("Aggregate: %+v", g)
	}
	if h := Header(context.Background(), sb, PR{Author: "x"}); h.Commits != 3 || !h.Merges || h.Author != "x" {
		t.Fatalf("Header: %+v", h)
	}
}
