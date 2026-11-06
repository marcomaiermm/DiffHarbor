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
