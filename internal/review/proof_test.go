package review

import (
	"context"
	"diffharbor/internal/proc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A test that passes on the base versions of the code files does not prove the change; one that fails there does.
func TestProofOnBase(t *testing.T) {
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi = false
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", "-c", `set -e
git init -q
printf 'module m\n\ngo 1.21\n' > go.mod
printf 'package m\n\nfunc F() int { return 1 }\n' > f.go
git add .; git commit -q -m base; git update-ref refs/base HEAD
printf 'package m\n\nfunc F() int { return 2 }\n' > f.go; printf 'package m\n\nfunc G() int { return 3 }\n' > g.go
printf 'package m\n\nimport "testing"\n\nfunc TestWeak(t *testing.T) { t.Run("sub", func(t *testing.T) { _ = F() }) }\n\nfunc TestStrong(t *testing.T) { if F() != 2 { t.Fatal(F()) } }\n' > f_test.go
git add .; git commit -q -m pr`)
	c.Dir = repo
	c.Env = append(c.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	weak := proofOnBase(context.Background(), &proc.Sandbox{Dir: t.TempDir()}, repo, "TestWeak/sub", nil, Settings{})
	if len(weak) != 1 || weak[0].Path != "f_test.go" || weak[0].Line != 5 || !strings.HasPrefix(weak[0].Evidence, Ran) {
		t.Fatalf("passes on base: %+v", weak)
	}
	for _, name := range []string{"TestStrong", "TestNone"} { // fails, not found
		if got := proofOnBase(context.Background(), &proc.Sandbox{Dir: t.TempDir()}, repo, name, nil, Settings{}); got != nil {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	if got := proofOnBase(context.Background(), &proc.Sandbox{Dir: t.TempDir()}, repo, "TestWeak/sub", map[string]string{"TestWeak/sub": "fail"}, Settings{}); got != nil {
		t.Fatalf("fails on head: %+v", got)
	}
	for _, j := range []string{`{"tests":false}`, `{"min_severity":"major"}`} { // review.json says no
		cfg, err := LoadSettings(j)
		if err != nil {
			t.Fatal(err)
		}
		if got := proofOnBase(context.Background(), &proc.Sandbox{Dir: t.TempDir()}, repo, "TestWeak/sub", nil, cfg); got != nil {
			t.Fatalf("%s: %+v", j, got)
		}
	}
	if out, _ := exec.Command("git", "-C", repo, "status", "--porcelain").Output(); len(out) > 0 {
		t.Fatalf("checkout touched: %s", out)
	}
}

// JS: the package's runner, here a vitest that prints a verbose line for the test it is told to run (-t), runs in a
// worktree at base with the checkout's node_modules linked in.
func TestProofOnBaseJS(t *testing.T) {
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi = false
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repo, "node_modules/.bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// the fake vitest passes the test when src/a.js at base says 1, and reads it through the linked node_modules
	fake := "#!/bin/sh\ntest -d node_modules || exit 3\nif grep -q 'return 1' src/a.js; then echo \" ✓ $6 > suite > weak 1ms\"; else echo \" × $6 > suite > weak 1ms\"; fi\n"
	if err := os.WriteFile(filepath.Join(repo, "node_modules/.bin/vitest"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", "-c", `set -e
git init -q
printf 'node_modules\n' > .gitignore
printf '{}' > package.json; mkdir src
printf 'export const f = () => { return 1 }\n' > src/a.js
git add .; git commit -q -m base; git update-ref refs/base HEAD
printf 'export const f = () => { return 2 }\n' > src/a.js
printf 'import { it } from "vitest"\nit("weak", () => {})\n' > src/a.test.js
git add .; git commit -q -m pr`)
	c.Dir = repo
	c.Env = append(c.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got := proofOnBase(context.Background(), &proc.Sandbox{Dir: t.TempDir()}, repo, "suite > weak", nil, Settings{})
	if len(got) != 1 || got[0].Path != "src/a.test.js" || got[0].Line != 2 || !strings.HasPrefix(got[0].Evidence, Ran) {
		t.Fatalf("passes on base: %+v", got)
	}
	// the title is not in any changed test file
	if got := proofOnBase(context.Background(), &proc.Sandbox{Dir: t.TempDir()}, repo, "suite > other", nil, Settings{}); got != nil {
		t.Fatalf("not found: %+v", got)
	}
}
