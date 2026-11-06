package review

import (
	"context"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"os/exec"
	"strings"
	"testing"
)

// The fork point lies past historyDepth on the base side: the shallow fetch finds no merge-base, unshallowing does.
// The diff leaves out what the base changed since.
func TestCheckout(t *testing.T) {
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi = false
	origin := t.TempDir()
	sh := func(script string) string {
		c := exec.Command("sh", "-c", "set -e\n"+script)
		c.Dir = origin
		c.Env = append(c.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		var stderr strings.Builder
		c.Stderr = &stderr // git warnings stay out of the output (rev-parse)
		out, err := c.Output()
		if err != nil {
			t.Fatalf("%v: %s", err, stderr.String())
		}
		return strings.TrimSpace(string(out))
	}
	head := sh(`git init -q -b main
git config uploadpack.allowFilter true; git config uploadpack.allowAnySHA1InWant true # as GitHub and GitLab
git config maintenance.auto false; git config gc.auto 0 # no background rewrite of the commit graph while we fetch
echo 1 > a.txt; echo 1 > b.txt; git add .; git commit -q -m fork
git checkout -q -b pr; echo 2 > a.txt; git commit -q -am pr
git checkout -q main; echo 2 > b.txt; git commit -q -am base
i=0; while [ $i -lt 300 ]; do git commit -q --allow-empty -m $i; i=$((i+1)); done
git rev-parse pr`)
	sb := &proc.Sandbox{Dir: t.TempDir()}
	d, err := Checkout(context.Background(), sb, "file://"+origin, head, "refs/heads/main", "t")
	if err != nil {
		t.Fatal(err)
	}
	if p := diff.Paths(d); len(p) != 1 || p[0] != "a.txt" || !strings.Contains(d, "+2") {
		t.Fatalf("diff:\n%s", d)
	}
	// the base moved on: 300 empty commits, and the one before them changed b.txt
	if b := Behind(context.Background(), sb); len(b) != 301 || b[300].Subject != "base" || strings.Join(b[300].Paths, ",") != "b.txt" || len(b[0].Paths) > 0 {
		t.Fatalf("Behind: %d commits, last %+v", len(b), b[len(b)-1])
	}
	if m := Messages(context.Background(), sb); m != "- pr" {
		t.Fatalf("Messages: %q", m)
	}
	// a push on top: Since is its change only; a head it can't fetch, none
	next := sh("git checkout -q pr; echo 3 > b.txt; git commit -q -am push; git rev-parse HEAD")
	sb = &proc.Sandbox{Dir: t.TempDir()}
	if _, err := Checkout(context.Background(), sb, "file://"+origin, next, "refs/heads/main", "t"); err != nil {
		t.Fatal(err)
	}
	if d, err := Since(context.Background(), sb, head, "t"); err != nil || strings.Join(diff.Paths(d), ",") != "b.txt" || !strings.Contains(d, "+3") {
		t.Fatalf("Since:\n%s", d)
	}
	if d, err := Since(context.Background(), sb, strings.Repeat("ab", 20), "t"); d != "" || err != nil {
		t.Fatalf("Since an unknown head: %q", d)
	}
}
