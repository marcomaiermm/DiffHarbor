package proc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapped(t *testing.T) {
	c := Capped{N: 4}
	c.Write([]byte("ab"))
	if c.String() != "ab" {
		t.Fatalf("short: %q", c.String())
	}
	for range 10 {
		c.Write([]byte("xyz"))
	}
	c.Write([]byte("end!"))
	if s := c.String(); !strings.HasPrefix(s, "abxy") || !strings.HasSuffix(s, "\n[… cut …]\nend!") {
		t.Fatalf("cut: %q", s)
	}
}

func TestFiles(t *testing.T) {
	f := filepath.Join(t.TempDir(), "f")
	if err := WriteFile(f, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadFile(f); err != nil || string(b) != "x" {
		t.Fatal(string(b), err)
	}
	for range 2 { // the second time it is gone already
		if Remove(f) != nil {
			t.Fatal("remove")
		}
	}
	if Exists(f) {
		t.Fatal("still there")
	}
}

// Each repo gets a uid of its own and keeps it; a hand-edited file doesn't make two repos share one, a broken one
// isn't started over.
func TestUIDOf(t *testing.T) {
	f := filepath.Join(t.TempDir(), "data", "uids.json")
	for _, c := range []struct {
		repo string
		uid  uint32
	}{{"github:api.github.com:1", firstUID}, {"gitlab:gitlab.com:1", firstUID + 1}, {"github:api.github.com:1", firstUID}} {
		if uid, err := uidOf(f, c.repo); err != nil || uid != c.uid {
			t.Fatalf("%s: %d %v, want %d", c.repo, uid, err, c.uid)
		}
	}
	_ = os.WriteFile(f, []byte(`{"o/a":10000,"o/b":10007}`), 0o600)
	if uid, _ := uidOf(f, "o/c"); uid != 10008 {
		t.Fatalf("past the highest: %d", uid)
	}
	_ = os.WriteFile(f, []byte(`{"o/a":`), 0o600)
	if _, err := uidOf(f, "o/d"); err == nil {
		t.Fatal("broken file: no error")
	}
	_ = os.WriteFile(f, []byte(`{"o/a":10000,"o/b":10000}`), 0o600)
	if _, err := uidOf(f, "o/a"); err == nil {
		t.Fatal("two repos share a uid: no error")
	}
	for _, bad := range []string{`null`, `{"o/a":0}`} {
		_ = os.WriteFile(f, []byte(bad), 0o600)
		if _, err := uidOf(f, "o/e"); err == nil {
			t.Fatal("no error for", bad)
		}
	}
}

// After the upgrade a repo is its id: a rename keeps its uid, a new repo of an old name (a legacy entry's) gets a uid
// and cache of its own, and the legacy uids are never handed out again. Restarts change nothing.
func TestLegacyUIDs(t *testing.T) {
	f := filepath.Join(t.TempDir(), "uids.json")
	legacy := `{"o/a":10000,"gitlab/g/p":10003}`
	_ = os.WriteFile(f, []byte(legacy), 0o600)
	a, b := "github:api.github.com:7", "github:api.github.com:8" // o/a, renamed later; a new o/a
	for range 2 {                                                // a restart in between
		if uid, err := uidOf(f, a); err != nil || uid != 10004 {
			t.Fatalf("repo id: %d %v, want past the legacy uids", uid, err)
		}
		if uid, err := uidOf(f, b); err != nil || uid != 10005 {
			t.Fatalf("new repo of the old name: %d %v", uid, err)
		}
	}
	m, err := uids(f)
	if err != nil || m["o/a"] != 10000 || m["gitlab/g/p"] != 10003 {
		t.Fatalf("legacy entries must stay taken: %v %v", m, err)
	}
	if k := kept(m); !k[1000] || k[10000] || k[10003] || !k[10004] || !k[10005] {
		t.Fatalf("Clean keeps %v", k)
	}
	defer func(pi bool, d, c string) { AsPi, DataDir, CacheDir = pi, d, c }(AsPi, DataDir, CacheDir)
	AsPi, DataDir, CacheDir = true, filepath.Dir(f), t.TempDir()
	if _, err := Workspace("o/a"); err == nil || !strings.Contains(err.Error(), "no repo id") {
		t.Fatalf("a name took a uid: %v", err)
	}
}

// A child gets PATH, HOME and what is passed, never the server's secrets.
func TestCommandEnv(t *testing.T) {
	t.Setenv("REPORT_S3_SECRET_KEY", "s3cret")
	for _, e := range Command(t.Context(), "", []string{"X=1"}, "true").Env {
		if strings.Contains(e, "s3cret") {
			t.Fatal(e)
		}
	}
}

// Clean empties CACHE_DIR as root: it refuses one holding DATA_DIR (CACHE_DIR=/tmp, the data's default parent).
func TestCleanOverlap(t *testing.T) {
	defer func(pi bool, c, d string) { AsPi, CacheDir, DataDir = pi, c, d }(AsPi, CacheDir, DataDir)
	AsPi, CacheDir = true, t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(CacheDir, link); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{CacheDir, filepath.Join(CacheDir, "diffharbor"), filepath.Join(CacheDir, "..data"), filepath.Join(link, "not", "yet")} {
		if DataDir = d; Clean() == nil || !strings.Contains(Clean().Error(), "holds DATA_DIR") { // not root's chown failing
			t.Error("cleaned a CACHE_DIR holding DATA_DIR", d)
		}
	}
}

func TestCleanTmpErrors(t *testing.T) {
	f := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if cleanTmp(filepath.Join(f, "..", "missing")) != nil || cleanTmp(f) == nil {
		t.Error("a missing dir is clean, an unreadable one isn't")
	}
}
