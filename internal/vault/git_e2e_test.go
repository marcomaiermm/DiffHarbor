package vault_test

import (
	"context"
	"diffharbor/internal/proc"
	"diffharbor/internal/review"
	"diffharbor/internal/vault"
	"encoding/base64"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestCheckoutThroughVault runs review.Checkout against git's own smart HTTP server behind the vault: the token
// reaches the forge on every request, a lazy blob:none fetch after the checkout's included, and is in no remote URL
// or argv.
func TestCheckoutThroughVault(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	exe, _ := exec.Command(git, "--exec-path").Output()
	backend := filepath.Join(strings.TrimSpace(string(exe)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("no git-http-backend")
	}
	root := t.TempDir()
	sh := func(dir, script string) string {
		c := exec.Command("sh", "-c", "set -e\n"+script)
		c.Dir = dir
		c.Env = append(c.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	head := sh(root, `git init -q -b main src; cd src
echo 1 > a.txt; echo 1 > b.txt; git add .; git commit -q -m base
git checkout -q -b pr; echo 2 > a.txt; git commit -q -am pr
git checkout -q main; echo later > c.txt; git add c.txt; git commit -q -m later
cd ..; git clone -q --bare src o.git; cd o.git
git config uploadpack.allowFilter true; git config uploadpack.allowAnySHA1InWant true
git rev-parse pr`)
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_read"))
	var reqs atomic.Int32
	h := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		if r.Header.Get("Authorization") != want {
			t.Errorf("forge got %s %s with %q", r.Method, r.URL, r.Header.Get("Authorization"))
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer up.Close()
	if vault.URL() == "" {
		if err := vault.Start(); err != nil {
			t.Fatal(err)
		}
	}
	work := t.TempDir()
	repo := filepath.Join(work, "repo")
	sb := &proc.Sandbox{Dir: work}
	if _, err := review.Checkout(context.Background(), sb, repo, up.URL+"/o.git", head, "refs/heads/main", "ghs_read"); err != nil {
		t.Fatal(err)
	}
	before := reqs.Load()
	// the base's c.txt is in no diff or checkout, so not fetched yet: git-remote-http gets it now, with the header
	// from the included file
	if b := sh(repo, "git cat-file -p refs/base:c.txt"); b != "later" || reqs.Load() == before {
		t.Fatalf("lazy fetch: %q, %d requests", b, reqs.Load()-before)
	}
	cfg, _ := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if url := sh(repo, "git remote get-url origin"); url != vault.URL()+"/git" || strings.Contains(string(cfg), "Bearer") {
		t.Fatalf("token in the remote or the repo's config: %s\n%s", url, cfg)
	}
}
