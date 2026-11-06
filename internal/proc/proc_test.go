package proc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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
	s, f := &Sandbox{}, filepath.Join(t.TempDir(), "f")
	if err := s.WriteFile(t.Context(), f, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if b, err := s.ReadFile(t.Context(), f); err != nil || string(b) != "x" {
		t.Fatal(string(b), err)
	}
	for range 2 { // the second time it is gone already
		if s.Remove(f) != nil {
			t.Fatal("remove")
		}
	}
	if Exists(f) {
		t.Fatal("still there")
	}
}

// A FIFO or symlink PR code planted where a file goes is replaced, not written through: the write never blocks.
func TestWriteFilePlanted(t *testing.T) {
	dir := t.TempDir()
	fifo, link, target := filepath.Join(dir, "fifo"), filepath.Join(dir, "link"), filepath.Join(dir, "target")
	if err := errors.Join(syscall.Mkfifo(fifo, 0o644), os.Symlink(target, link)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for _, f := range []string{fifo, link} {
		if err := (&Sandbox{}).WriteFile(ctx, f, []byte("x")); err != nil {
			t.Fatal(f, err)
		}
		if fi, err := os.Lstat(f); err != nil || !fi.Mode().IsRegular() {
			t.Fatal(f, fi, err)
		}
	}
	if Exists(target) {
		t.Fatal("wrote through the symlink")
	}
}

// A PR's file that would never end (a symlink to /dev/zero, a FIFO) is no read: an error, at once.
func TestReadFileEndless(t *testing.T) {
	dir := t.TempDir()
	zero, fifo := filepath.Join(dir, "zero.sql"), filepath.Join(dir, "fifo.sql")
	if err := errors.Join(os.Symlink("/dev/zero", zero), syscall.Mkfifo(fifo, 0o644)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for _, f := range []string{zero, fifo} {
		start := time.Now()
		if b, err := (&Sandbox{}).ReadFile(ctx, f); err == nil || len(b) > 0 || time.Since(start) > time.Second {
			t.Fatalf("%s: %d bytes, %v, in %s", f, len(b), err, time.Since(start))
		}
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

// Two new repos' jobs at once never get the same uid, and uids.json stays whole.
func TestRepoUIDConcurrent(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("as root name writes /etc/passwd")
	}
	defer func(d string) { DataDir = d }(DataDir)
	DataDir = t.TempDir()
	const n = 50
	got := make([]uint32, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			var err error
			if got[i], err = repoUID("github:api.github.com:" + strconv.Itoa(i)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	m, err := uids(filepath.Join(DataDir, "uids.json")) // errors on a uid two repos share
	if err != nil || len(m) != n {
		t.Fatalf("%d repos, %v", len(m), err)
	}
	for i, uid := range got {
		if m["github:api.github.com:"+strconv.Itoa(i)] != uid {
			t.Fatalf("repo %d got %d, file says %d", i, uid, m["github:api.github.com:"+strconv.Itoa(i)])
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
	for _, e := range (&Sandbox{uid: firstUID}).Command(t.Context(), "", []string{"X=1"}, "true").Env {
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
	all := func(int) bool { return true }
	if cleanTmp(all, filepath.Join(f, "..", "missing")) != nil || cleanTmp(all, f) == nil {
		t.Error("a missing dir is clean, an unreadable one isn't")
	}
}

// A job's cleanup removes only what its own user left: another repo's job may be running beside it.
func TestCleanTmpScoped(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	me := os.Getuid()
	if cleanTmp(func(uid int) bool { return uid == me+1 }, dir) != nil || !Exists(f) {
		t.Fatal("removed another user's file")
	}
	if cleanTmp(func(uid int) bool { return uid == me }, dir) != nil || Exists(f) {
		t.Fatal("kept its own file")
	}
}

// Without root a sandbox is a work dir and this process' identity; Close removes it.
func TestWorkspaceLocal(t *testing.T) {
	defer func(pi bool, c string) { AsPi, CacheDir = pi, c }(AsPi, CacheDir)
	AsPi, CacheDir = false, t.TempDir()
	s, err := Workspace("")
	if err != nil || !Exists(s.Dir) || filepath.Dir(s.Dir) != workDir() {
		t.Fatal(s, err)
	}
	if out, err := Run(s.Command(t.Context(), s.Dir, nil, "pwd")); err != nil || strings.TrimSpace(string(out)) != s.Dir {
		t.Fatalf("%q %v", out, err)
	}
	s.Close()
	if Exists(s.Dir) {
		t.Fatal("work dir left")
	}
}

// As root a Sandbox not from Workspace runs nothing: its zero uid is root's.
func TestCommandNoUser(t *testing.T) {
	defer func(pi bool) { AsPi = pi }(AsPi)
	AsPi = true
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	(&Sandbox{}).Command(t.Context(), "/", nil, "true")
}

// A uid whose cleanup failed after its job stays dirty: no job of any repo starts until a retry clears it.
func TestDirtyUID(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("as root Workspace's name writes /etc/passwd")
	}
	defer func(pi bool, c func(uint32) error, d string) { AsPi, clearUID, DataDir = pi, c, d }(AsPi, clearUID, DataDir)
	failing := true
	var cleared []uint32
	AsPi, DataDir, clearUID = true, t.TempDir(), func(uid uint32) error {
		cleared = append(cleared, uid)
		if failing && uid == 10001 {
			return errors.New("busy")
		}
		return nil
	}
	if err := clean(10003); err != nil || !slices.Equal(cleared, []uint32{10003}) || len(dirty) != 0 {
		t.Fatal(err, cleared, dirty) // its own uid, before the sandbox exists
	}
	if err := clean(10001); !errors.Is(err, ErrCleanup) || !dirty[10001] {
		t.Fatal("not dirty", err)
	}
	if err := clean(1000); !errors.Is(err, ErrCleanup) || dirty[1000] {
		t.Fatal("node's job started beside the leftovers, or node got cleared:", err)
	}
	if _, err := Workspace("github:2"); !errors.Is(err, ErrCleanup) || !strings.Contains(err.Error(), "uid 10001: busy") {
		t.Fatal("a job started beside the leftovers:", err)
	}
	failing = false
	if err := clean(1000); err != nil || len(dirty) != 0 {
		t.Fatal(err, dirty)
	}
	// Workspace's repo got uid 10000 and was cleared beside 10001, in either order.
	if !slices.Equal(cleared[:3], []uint32{10003, 10001, 10001}) || !slices.Contains(cleared[3:5], 10000) ||
		!slices.Contains(cleared[3:5], 10001) || !slices.Equal(cleared[5:], []uint32{10001}) {
		t.Fatal(cleared)
	}
}

// A rewritten file keeps its mode: a restored lockfile is the one the PR has.
func TestWriteFileMode(t *testing.T) {
	f := filepath.Join(t.TempDir(), "lock")
	if err := os.WriteFile(f, []byte("a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (&Sandbox{}).WriteFile(t.Context(), f, []byte("b")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(f); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatal(fi.Mode(), err)
	}
}

// A process list that can't be read is no "all dead": the uid stays dirty. One that ended since is no error.
func TestKillLeftoversFailsClosed(t *testing.T) {
	defer func(d string) { procDir = d }(procDir)
	none := func(int) bool { return false }
	procDir = filepath.Join(t.TempDir(), "missing")
	if killLeftovers(none) == nil {
		t.Fatal("no /proc, no error")
	}
	procDir = t.TempDir()
	os.Mkdir(filepath.Join(procDir, "7"), 0o755) // ended: no status
	if err := killLeftovers(none); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(procDir, "7", "status"), 0o755) // there, but unreadable
	if killLeftovers(none) == nil {
		t.Fatal("an unread status, no error")
	}
}

// A job's file in a world-writable dir root made goes, the dir stays.
func TestCleanTmpNested(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root to own the dir and chown the file")
	}
	dir := t.TempDir()
	shared, f := filepath.Join(dir, "shared"), filepath.Join(dir, "shared", "f")
	if err := errors.Join(os.Mkdir(shared, 0o777), os.WriteFile(f, nil, 0o600), os.Lchown(f, 12345, 12345)); err != nil {
		t.Fatal(err)
	}
	if err := cleanTmp(func(uid int) bool { return uid == 12345 }, dir); err != nil || Exists(f) || !Exists(shared) {
		t.Fatal(err, Exists(f), Exists(shared))
	}
}
