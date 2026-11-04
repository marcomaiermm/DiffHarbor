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
	}{{"o/a", firstUID}, {"gitlab/g/a", firstUID + 1}, {"o/a", firstUID}} {
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
	for _, d := range []string{CacheDir, filepath.Join(CacheDir, "diffharbor"), filepath.Join(link, "not", "yet")} {
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
