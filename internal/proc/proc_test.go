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

// What a job plants in the home goes, the image's files come back.
func TestResetHome(t *testing.T) {
	pristine, home := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(pristine, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(pristine, ".pi", "ext.js"), []byte("ours"), 0o644)
	tar := filepath.Join(t.TempDir(), "home.tar")
	if _, err := Run(Command(t.Context(), "/", nil, "tar", "-cf", tar, "-C", pristine, ".")); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(home, ".pi"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".pi", "ext.js"), []byte("theirs"), 0o644)
	_ = os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[core]\nhooksPath=/x"), 0o644)
	if err := resetHome(home, tar); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".pi", "ext.js")); string(b) != "ours" || Exists(filepath.Join(home, ".gitconfig")) {
		t.Fatalf("ext.js %q, .gitconfig left: %v", b, Exists(filepath.Join(home, ".gitconfig")))
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
