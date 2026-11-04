package vault_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"diffharbor/internal/checks"
	"diffharbor/internal/proc"
	"diffharbor/internal/vault"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tgz is an npm package tarball of name.
func tgz(name string) []byte {
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	w := tar.NewWriter(z)
	pj := []byte(`{"name":"` + name + `","version":"1.0.0"}`)
	w.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(pj))})
	w.Write(pj)
	w.Close()
	z.Close()
	return b.Bytes()
}

// TestNpmThroughVault runs the real npm ci of the checks against two registries that want their own tokens, one under
// the other's path: the lockfile's npmjs and registry URLs reach them through the vault, the PR's lockfile stays.
func TestNpmThroughVault(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("no npm")
	}
	tarballs := map[string][]byte{"/npm/a/-/a-1.0.0.tgz": tgz("a"), "/npm/internal/@corp/b/-/b-1.0.0.tgz": tgz("@corp/b")}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Bearer tokA"
		if strings.HasPrefix(r.URL.Path, "/npm/internal/") {
			want = "Bearer tokB"
		}
		b, ok := tarballs[r.URL.Path]
		if r.Header.Get("Authorization") != want || !ok {
			t.Errorf("registry got %s %s with %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write(b)
	}))
	defer up.Close()
	if vault.URL() == "" {
		if err := vault.Start(); err != nil {
			t.Fatal(err)
		}
	}
	proc.CacheDir = t.TempDir()
	t.Setenv("HOME", t.TempDir()) // no ~/.npmrc of yours
	integrity := func(p string) string {
		s := sha512.Sum512(tarballs[p])
		return "sha512-" + base64.StdEncoding.EncodeToString(s[:])
	}
	repo := t.TempDir()
	lock := `{"name":"app","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{
"":{"name":"app","version":"1.0.0","dependencies":{"a":"1.0.0","@corp/b":"1.0.0"}},
"node_modules/a":{"version":"1.0.0","resolved":"https://registry.npmjs.org/a/-/a-1.0.0.tgz","integrity":"` + integrity("/npm/a/-/a-1.0.0.tgz") + `"},
"node_modules/@corp/b":{"version":"1.0.0","resolved":"` + up.URL + `/npm/internal/@corp/b/-/b-1.0.0.tgz","integrity":"` + integrity("/npm/internal/@corp/b/-/b-1.0.0.tgz") + `"}}}`
	os.WriteFile(filepath.Join(repo, "package.json"), []byte(`{"name":"app","version":"1.0.0","dependencies":{"a":"1.0.0","@corp/b":"1.0.0"}}`), 0o644)
	os.WriteFile(filepath.Join(repo, "package-lock.json"), []byte(lock), 0o644)
	os.WriteFile(filepath.Join(repo, "a.js"), nil, 0o644)

	regs := []checks.Registry{{Scope: "", URL: up.URL + "/npm/", Token: "tokA"}, {Scope: "@corp", URL: up.URL + "/npm/internal/", Token: "tokB"}}
	out := checks.Run(context.Background(), repo, nil, "", []string{"a.js"}, func(string) bool { return false }, false, regs)
	for _, p := range []string{"node_modules/a/package.json", "node_modules/@corp/b/package.json"} {
		if _, err := os.Stat(filepath.Join(repo, p)); err != nil {
			t.Errorf("not installed: %s\n%s", p, out)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "package-lock.json")); string(b) != lock {
		t.Errorf("lockfile not restored:\n%s", b)
	}
	if strings.Contains(out, "tokA") || strings.Contains(out, "tokB") {
		t.Errorf("a token in the checks' output:\n%s", out)
	}
}
