package store

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if f, err := d.Open("a/b/c.html"); err != nil {
		t.Fatal(err)
	} else if b, _ := io.ReadAll(f); string(b) != "x" || f.Close() != nil {
		t.Fatal("open:", string(b))
	}
	if _, err := d.Open("a/b/none.html"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("open none:", err)
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

// Puts of one key at once: a reader gets one of them whole, and no temp file stays.
func TestDiskConcurrentPut(t *testing.T) {
	d := Disk(t.TempDir())
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 20 {
				if err := d.Put("a/k", []byte(strings.Repeat(string(rune('a'+i)), 1<<16))); err != nil {
					t.Error(err)
				}
				if b, err := d.Get("a/k"); err != nil || len(b) != 1<<16 || strings.Count(string(b), string(b[:1])) != len(b) {
					t.Error("torn read:", len(b), err)
				}
			}
		})
	}
	wg.Wait()
	if es, _ := os.ReadDir(filepath.Join(string(d), "a")); len(es) != 1 {
		t.Fatal("left temp files:", len(es))
	}
}

// The folder's keys end up in the other store, and the folder is gone.
func TestDiskMoveTo(t *testing.T) {
	from, to := Disk(filepath.Join(t.TempDir(), "r")), Disk(t.TempDir())
	if err := from.MoveTo(to); err != nil {
		t.Fatal("no folder:", err)
	}
	from.Put("h/o/r/7/versions.json", []byte("[]"))
	from.Put("h/o/r/7/abc.html", []byte("x"))
	from.Put("h/o/r/8/versions.json", []byte("old"))
	to.Put("h/o/r/8/versions.json", []byte("new"))                                  // written after a failed move
	os.WriteFile(filepath.Join(string(from), "h/o/r/7/abc.html.X.tmp"), nil, 0o600) // a crashed put
	if err := from.MoveTo(to); err != nil {
		t.Fatal(err)
	}
	if b, err := to.Get("h/o/r/7/abc.html"); err != nil || string(b) != "x" {
		t.Fatal(string(b), err)
	}
	if b, err := to.Get("h/o/r/7/versions.json"); err != nil || string(b) != "[]" {
		t.Fatal("versions.json:", string(b), err)
	}
	if b, _ := to.Get("h/o/r/8/versions.json"); string(b) != "new" {
		t.Fatal("an older disk copy replaced:", string(b))
	}
	if _, err := to.Get("h/o/r/7/abc.html.X.tmp"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("moved a temp file:", err)
	}
	if _, err := os.Stat(string(from)); !os.IsNotExist(err) {
		t.Fatal("folder stayed:", err)
	}
}
