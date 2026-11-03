package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// A key stays inside the folder: no "..", no absolute path, no symlink out.
func TestDisk(t *testing.T) {
	d := Disk(filepath.Join(t.TempDir(), "reports"))
	if _, err := d.Get("a/b"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("before any put:", err)
	}
	if err := d.DeleteAll("a"); err != nil {
		t.Fatal("before any put:", err)
	}
	if err := d.Put("a/b/c.html", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if b, err := d.Get("a/b/c.html"); err != nil || string(b) != "x" {
		t.Fatal(string(b), err)
	}
	if err := d.Put("a/b/c.html", []byte("y")); err != nil {
		t.Fatal(err)
	}
	if b, _ := d.Get("a/b/c.html"); string(b) != "y" {
		t.Fatal("overwrite:", string(b))
	}
	if _, err := d.Get("a/b/c.html.tmp"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("left its temp file:", err)
	}

	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600)
	os.Symlink(outside, filepath.Join(string(d), "link"))
	for _, k := range []string{"../secret", "a/../../secret", "/etc/passwd", "link/secret"} {
		if _, err := d.Get(k); err == nil {
			t.Error("got", k)
		}
		if err := d.Put(k, []byte("z")); err == nil {
			t.Error("put", k)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret")); string(b) != "s" {
		t.Fatal("wrote outside:", string(b))
	}

	if err := d.Delete("a/b/c.html"); err != nil {
		t.Fatal(err)
	}
	d.Put("a/b/d.html", nil)
	d.Put("a/bb/e.html", nil)
	if err := d.DeleteAll("a/b"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Get("a/b/d.html"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("kept under a/b:", err)
	}
	if _, err := d.Get("a/bb/e.html"); err != nil {
		t.Fatal("a/bb isn't under a/b:", err)
	}
}
