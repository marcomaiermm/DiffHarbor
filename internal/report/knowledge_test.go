package report

import (
	"context"
	"diffharbor/internal/proc"
	"diffharbor/internal/review"
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
		c.Env = append(c.Environ(), "GIT_AUTHOR_NAME=o", "GIT_AUTHOR_EMAIL=o@x", "GIT_COMMITTER_NAME=o", "GIT_COMMITTER_EMAIL=o@x") // no git identity in a build container
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	git("o|o@x", "2025-01-10", "init", "-q", "-b", "main")
	sh(`echo a > a.txt; echo b > b.txt; echo c > c.txt; echo h > h.txt
echo 'Alice <alice@x> <alias@x>' > .mailmap
git add .`)
	git("o|o@x", "2025-01-10", "commit", "-qm", "one") // 15 months before: no person of the 12
	sh(`echo 2 >> a.txt`)
	git("Alias|alias@x", "2026-02-01", "commit", "-qam", "two")
	sh(`echo e > d.txt; git add .`)
	git("Eve|eve@x", "2026-02-10", "commit", "-qm", "eve adds a d.txt that is gone before the rename")
	sh(`git rm -q d.txt`)
	git("Eve|eve@x", "2026-02-11", "commit", "-qm", "eve removes it")
	sh(`echo 2 >> b.txt`)
	git("Yan|yan@x", "2026-02-15", "commit", "-qam", "yan")
	git("Alice|alice@x", "2026-03-01", "mv", "b.txt", "d.txt")
	git("Alice|alice@x", "2026-03-01", "commit", "-qm", "rename")
	sh(`echo g > g.txt; echo r > r.txt; git add .`)
	git("Zed Old|zed@x", "2026-03-10", "commit", "-qm", "g")
	sh(`echo 2 >> c.txt`)
	git("Zed|zed@x", "2026-03-15", "commit", "-qam", "zed")
	sh(`echo 4 >> c.txt`)
	git("Zed|Zed@X", "2026-03-20", "commit", "-qam", "same email, other case")
	sh(`echo 3 >> c.txt`)
	git("dependabot[bot]|1+dependabot[bot]@users.noreply.github.com", "2026-04-01", "commit", "-qam", "bump")
	sh(`mkdir gen; i=0; while [ $i -lt 149 ]; do echo $i > gen/f$i; i=$((i+1)); done; echo 9 >> a.txt; git add .`)
	git("Bob|bob@x", "2026-05-01", "commit", "-qm", "bulk") // 150 files with a.txt
	sh(`echo 5 >> r.txt`)
	git("Rae|rae@x", "2026-04-10", "commit", "-qam", "rae")
	sh(`echo 4 >> a.txt`)
	git("Bob|bob@x", "2026-07-01", "commit", "-qam", "after")
	sh(`git update-ref refs/base HEAD
git checkout -q -b pr; echo 1 > p1; git add .; git commit -q -m p1
git checkout -q -b side refs/base; echo 1 > s1; git add .; git commit -q -m s1
git checkout -q pr; git merge -q --no-ff side -m merge; echo 1 > p2; git add .; git commit -q -m p2
echo 'Zork <zork@x> <zed@x>' > .mailmap`) // the PR's worktree doesn't rename anyone

	created := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	raw := "+++ b/a.txt\n+++ b/d.txt\n+++ b/c.txt\n+++ b/g.txt\n+++ b/h.txt\n+++ b/gen/f0\n+++ b/gone.txt\n+++ b/r2.txt\n" +
		"diff --git a/r.txt b/r2.txt\nsimilarity index 100%\nrename from r.txt\nrename to r2.txt\n"
	ks := Learn(context.Background(), sb, raw, created)
	want := map[string]Know{
		"a.txt":  {Days: 120, People: []string{"Alice"}},
		"d.txt":  {Days: 92, People: []string{"Alice", "Yan"}}, // Yan: before the rename
		"c.txt":  {Days: 61, People: []string{"Zed"}},          // Zed@X is zed@x
		"g.txt":  {Days: 83, People: []string{"Zed"}},          // one email, two names: the newest
		"r2.txt": {Days: 52, People: []string{"Rae", "Zed"}},   // renamed by the PR: its old name's history
	} // h.txt: only the first commit touched it, which isn't a change
	if fmt.Sprint(ks) != fmt.Sprint(want) {
		t.Fatalf("Learn:\n%v\nwant\n%v", ks, want)
	}
	if g := Aggregate(ks, []string{"a.txt", "c.txt", "d.txt", "g.txt", "gone.txt"}); g.Known != 4 || g.Median != 87 || g.Oldest != 120 || !slices.Equal(g.People, []string{"Alice", "Yan", "Zed"}) {
		t.Fatalf("Aggregate: %+v", g)
	}
	if g := Aggregate(ks, []string{"a.txt", "c.txt"}); g.Median != 90 || g.Oldest != 120 { // even: the mean of the middle two
		t.Fatalf("Aggregate even: %+v", g)
	}
	if h := Header(context.Background(), sb, PR{Author: "x"}); h.Commits != 3 || !h.Merges || h.Author != "x" {
		t.Fatalf("Header: %+v", h)
	}
}

// In the blob-less checkout of the job, with the origin gone, an inexact rename in the history must not fail the log.
func TestLearnPartialClone(t *testing.T) {
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi = false
	origin := t.TempDir()
	c := exec.Command("sh", "-c", `set -e
git init -q -b main
git config uploadpack.allowFilter true; git config uploadpack.allowAnySHA1InWant true
git config maintenance.auto false; git config gc.auto 0
seq 1 20 > s1; git add .; git commit -q -m one
git mv s1 s2; echo 6 >> s2; git add .; git commit -q -m similar
git checkout -q -b pr; echo p > p.txt; git add .; git commit -q -m pr
git rev-parse pr`)
	c.Dir = origin
	c.Env = append(c.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(string(out))
	sb := &proc.Sandbox{Dir: t.TempDir()}
	if _, err := review.Checkout(context.Background(), sb, "file://"+origin, head, "refs/heads/main", "t"); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(origin) // a lazy fetch fails now, as it would without auth
	if ks := Learn(context.Background(), sb, "+++ b/s2\n", time.Now()); len(ks) != 1 {
		t.Fatalf("Learn: %v", ks)
	}
}

// learnRepo is a repo whose refs/base is where script leaves it, after a first commit that reads as no change, with
// proc.AsPi off until t ends; a commit's author as "A=name c …".
func learnRepo(t *testing.T, script string) *proc.Sandbox {
	t.Helper()
	pi := proc.AsPi
	t.Cleanup(func() { proc.AsPi = pi })
	proc.AsPi = false
	sb := &proc.Sandbox{Dir: t.TempDir()}
	c := exec.Command("sh", "-c", `set -e
git init -q "$0"; cd "$0"; git commit -q --allow-empty -m root
c() { GIT_AUTHOR_NAME=$A GIT_AUTHOR_EMAIL=$A@x git commit -q "$@"; }
`+script+`
git update-ref refs/base HEAD`, sb.Repo())
	c.Env = append(c.Environ(), "GIT_AUTHOR_NAME=o", "GIT_AUTHOR_EMAIL=o@x", "GIT_COMMITTER_NAME=o", "GIT_COMMITTER_EMAIL=o@x")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	return sb
}

// A name git would quote is the file's name as it is.
func TestLearnQuotedName(t *testing.T) {
	sb := learnRepo(t, `echo 1 > 'a"b.txt'; git add .; A=Quinn c -m add`)
	if ks := Learn(context.Background(), sb, "+++ b/a\"b.txt\n", time.Now().Add(time.Hour)); fmt.Sprint(ks) != `map[a"b.txt:{0 [Quinn]}]` {
		t.Fatalf("Learn: %v", ks)
	}
}

// A file's history ends where it was added: a former file of its name, deleted before, isn't it. One deleted at the
// base, which the PR adds anew, has none.
func TestLearnStopsAtAdd(t *testing.T) {
	sb := learnRepo(t, `echo 1 > x.txt; echo 1 > y.txt; git add .; A=Old c -m old
git rm -q x.txt y.txt; A=Old c -m gone
echo 2 > x.txt; git add .; A=New c -m new`)
	if ks := Learn(context.Background(), sb, "+++ b/x.txt\n+++ b/y.txt\n", time.Now().Add(time.Hour)); fmt.Sprint(ks) != `map[x.txt:{0 [New]}]` {
		t.Fatalf("Learn: %v", ks)
	}
}
