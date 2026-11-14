// Package proc runs git, checks and the agent as the job's repo's unprivileged user, in work dirs next to the tool
// caches.
package proc

import (
	"bytes"
	"context"
	"diffharbor/internal/text"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	// AsPi runs git, checks and Pi as a repo's user: the server runs as root so they can't read its environ, i.e.
	// the App key.
	AsPi = os.Getuid() == 0
	// DataDir is the data volume (DATA_DIR): logins, learnings, pending jobs, the repos' uids.
	DataDir = filepath.Join(os.TempDir(), "diffharbor")
	// CacheDir is the cache (CACHE_DIR): as root a dir per repo's user (pnpm store, Go caches), and the work dirs.
	CacheDir = filepath.Join(os.TempDir(), "diffharbor-cache") // never the temp dir itself: as root, Clean owns it
)

// ErrCleanup is a Workspace that couldn't clean up after a job, its user's last or another repo's: nothing of this
// job ran, a later attempt may get past it.
var ErrCleanup = errors.New("cleaning up after the last job failed")

// Recovered, deferred to run first in a job's goroutine, turns its panic into fail's error: a bug fails that job or
// part, not the server and the other repos' jobs running beside it. In proc: server, job and review all import it.
func Recovered(fail func(error)) {
	if v := recover(); v != nil {
		fail(fmt.Errorf("panic: %v\n%s", v, debug.Stack()))
	}
}

// firstUID is the first repo's uid and gid; the image's users are below it, node (1000) runs dry runs.
const firstUID = 10000

// Sandbox is a job's work dir and, when we're root, who its commands run as: the repo's uid, and the home and temp
// dir in Dir. A value per job, not one per process: jobs of different repos may run at once, and a call that should
// run as a repo's user can't fall back to another's.
type Sandbox struct {
	Dir       string // the work dir: the checkout in Dir/repo, Pi's sessions, the checks' configs
	uid       uint32
	home, tmp string
	pin       *os.Root // the checkout, from before the job's code ran (Pin)
}

// jobUser reports whether uid runs PR code: a repo's user, or node (dry runs, and every job before a uid per repo).
func jobUser(uid int) bool { return uid == 1000 || uid >= firstUID }

func owner(fi os.FileInfo) int { return int(fi.Sys().(*syscall.Stat_t).Uid) }

func workDir() string { return filepath.Join(CacheDir, "work") }

// cacheDir is the tool caches; as root the user's own: a PR's code can't change what another repo's jobs run (a
// GOCACHE entry, a package in the pnpm store, corepack's pnpm).
func (s *Sandbox) cacheDir() string {
	if !AsPi {
		return CacheDir
	}
	return filepath.Join(CacheDir, strconv.Itoa(int(s.uid)))
}

// ToolEnv points the caches for installs and builds the checks and Pi's bash run at cacheDir (pnpm store and work
// dirs share a volume: hardlinks).
func (s *Sandbox) ToolEnv() []string {
	c := s.cacheDir()
	return []string{
		"CI=1", "XDG_CACHE_HOME=" + c, "npm_config_cache=" + c + "/npm",
		"npm_config_store_dir=" + c + "/pnpm-store", "pnpm_config_store_dir=" + c + "/pnpm-store",
		"COREPACK_HOME=" + c + "/corepack", "COREPACK_ENABLE_DOWNLOAD_PROMPT=0",
		"GOPATH=" + c + "/go", "GOCACHE=" + c + "/go-build", "GOTOOLCHAIN=auto",
		"NODE_OPTIONS=--max-old-space-size=2048", "DO_NOT_TRACK=1",
	}
}

// Command runs as the job's user (when we're root) with only PATH, HOME, TMPDIR and extra env, so children never see
// the App key.
func (s *Sandbox) Command(ctx context.Context, dir string, extra []string, name string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir, c.WaitDelay = dir, 5*time.Second
	// In a process group of its own, killed whole when ctx ends: a test's children don't outlive a timed-out check.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return os.ErrProcessDone // exited on its own: Wait reports its own error, not the kill's
	}
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GIT_TERMINAL_PROMPT=0"}
	if AsPi {
		if !jobUser(int(s.uid)) { // a Sandbox not from Workspace: uid 0 would run PR code as root
			panic("proc: command without a job's user")
		}
		c.Env[1] = "HOME=" + s.home
		if s.tmp != "" {
			c.Env = append(c.Env, "TMPDIR="+s.tmp)
		}
		c.SysProcAttr.Credential = &syscall.Credential{Uid: s.uid, Gid: s.uid}
	}
	c.Env = append(c.Env, extra...)
	return c
}

// Home is the job's home: Pi's config is in it.
func (s *Sandbox) Home() string { return s.home }

// Repo is the job's checkout.
func (s *Sandbox) Repo() string { return filepath.Join(s.Dir, "repo") }

// Pin opens the checkout for root's reads (Checkout): code of the job that swaps Repo for a symlink to / later
// doesn't move them. Call it before that code runs.
func (s *Sandbox) Pin() error {
	r, err := os.OpenRoot(s.Repo())
	if err == nil {
		s.pin = r
	}
	return err
}

// Pinned is the checkout Pin opened, its symlinks kept inside it, for the caller to close.
func (s *Sandbox) Pinned() (*os.Root, error) {
	if s.pin == nil {
		return nil, errors.New("checkout not pinned")
	}
	return s.pin.OpenRoot(".")
}

// Run returns stdout; errors carry stderr but never the args.
func Run(c *exec.Cmd) ([]byte, error) {
	var out bytes.Buffer
	stderr := Capped{N: 4 << 10}
	c.Stdout, c.Stderr = &out, &stderr
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", filepath.Base(c.Path), err, text.Truncate(stderr.String(), 300))
	}
	return out.Bytes(), nil
}

// Exists reports whether p exists.
func Exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Workspace is a job's sandbox on repo, a forge.RepoID: a work dir git, checks and Pi can write. As root its commands
// run as repo's user (node for "", a dry run), in a fresh home (the image's: Pi's extensions) and temp dir in the
// work dir: what a PR's code leaves there (a git hook, Pi's config, `go env -w GOFLAGS=-toolexec=…`) goes with it.
// First it kills what the user's last job left running and removes what it left in the temp dirs all share. Close
// it when the job is done.
func Workspace(repo string) (*Sandbox, error) {
	if err := os.MkdirAll(workDir(), 0o755); err != nil {
		return nil, err
	}
	if !AsPi {
		dir, err := os.MkdirTemp(workDir(), "review-")
		if err != nil {
			return nil, err
		}
		return &Sandbox{Dir: dir, home: os.Getenv("HOME")}, nil
	}
	uid := uint32(1000)
	if repo != "" {
		if legacy(repo) { // a name: a new repo of an old one's name would get its uid and cache
			return nil, fmt.Errorf("repo uids: %s is no repo id", repo)
		}
		var err error
		if uid, err = repoUID(repo); err != nil {
			return nil, err
		}
	}
	// The user's last job's leftovers would get this job's tokens and checkout, or plant files for its code; no job
	// runs beside what another's couldn't clean up.
	if err := clean(uid); err != nil {
		return nil, err
	}
	s := &Sandbox{uid: uid, home: "/nonexistent"}
	// Each job: its code may have closed its cache to itself or opened it to the other users. Chmod follows a
	// symlink, but only root writes CacheDir (Clean): none can stand in for the dir.
	c := s.cacheDir()
	if err := errors.Join(os.MkdirAll(c, 0o700), os.Lchown(c, int(uid), int(uid)), os.Chmod(c, 0o700)); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(workDir(), "review-")
	if err != nil {
		return nil, err
	}
	home, tmp := filepath.Join(dir, "home"), filepath.Join(dir, "tmp")
	// As root, while the dirs are root's: their user gets them, never what a symlink points to.
	err = errors.Join(os.Mkdir(home, 0o700), os.Mkdir(tmp, 0o700))
	for _, d := range []string{home, tmp, dir} {
		err = errors.Join(err, os.Lchown(d, int(uid), int(uid)))
	}
	if err == nil {
		s.Dir, s.home, s.tmp = dir, home, tmp
		// As the user: the files are its, and a symlink in the tar can't make root write.
		_, err = Run(s.Command(context.Background(), "/", nil, "tar", "-xf", "/opt/node-home.tar", "-C", home))
	}
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return s, nil
}

// Close ends the job: its user's processes die and its files in the shared temp dirs and Dir go, so none reaches a
// job of another repo. When that fails no other job starts until a retry gets through (Workspace).
func (s *Sandbox) Close() {
	if s.pin != nil {
		s.pin.Close()
	}
	if err := clean(s.uid); err != nil {
		log.Print(err)
	}
	os.RemoveAll(s.Dir) // as root: past a user's chmod 000
}

var (
	dirtyMu sync.Mutex
	dirty   = map[uint32]bool{} // the uids a clean failed for: their leftovers may still run
	// clearUID kills uid's processes and removes its files in the temp dirs all share. A var: tests run without root.
	clearUID = func(uid uint32) error { return clearUsers(func(u int) bool { return u == int(uid) }) }
)

// clean clears uid (a repo's, as root) and every uid a clean failed for before; while one stays dirty no job starts.
// Node's isn't cleared: dry runs keep theirs, node reaches no repo's files.
func clean(uid uint32) error {
	dirtyMu.Lock()
	defer dirtyMu.Unlock()
	if AsPi && uid >= firstUID {
		dirty[uid] = true
	}
	var errs []error
	for u := range dirty {
		if err := clearUID(u); err != nil {
			errs = append(errs, fmt.Errorf("uid %d: %w", u, err))
		} else {
			delete(dirty, u)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", ErrCleanup, errors.Join(errs...))
	}
	return nil
}

// uidMu guards uids.json and /etc/passwd, /etc/group: two new repos' jobs at once must not get the same uid.
var uidMu sync.Mutex

// repoUID is repo's uid, named in /etc.
func repoUID(repo string) (uint32, error) {
	uidMu.Lock()
	defer uidMu.Unlock()
	uid, err := uidOf(filepath.Join(DataDir, "uids.json"), repo)
	if err == nil {
		name(uid) // node has one
	}
	return uid, err
}

// name gives uid a user and group in /etc: PR code asks who it runs as (Node's os.userInfo(), Go's user.Current()) and
// fails without. Best effort: on a read-only root fs only that fails.
func name(uid uint32) {
	id := strconv.Itoa(int(uid))
	for f, line := range map[string]string{
		"/etc/passwd": "u" + id + ":x:" + id + ":" + id + "::/nonexistent:/sbin/nologin\n",
		"/etc/group":  "u" + id + ":x:" + id + ":\n",
	} {
		b, err := os.ReadFile(f) //nolint:gosec // /etc/passwd or /etc/group
		if err != nil || bytes.HasPrefix(b, []byte("u"+id+":")) || bytes.Contains(b, []byte("\nu"+id+":")) {
			continue
		}
		if len(b) > 0 && b[len(b)-1] != '\n' {
			line = "\n" + line
		}
		if w, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0); err == nil { //nolint:gosec // as above
			_, _ = w.WriteString(line)
			_ = w.Close()
		}
	}
}

// uidOf is repo's uid in the root-only file f, a new one for a repo f doesn't have: one past the highest, the legacy
// entries' too, so no two repos share one, and a repo keeps its uid (and its cache) across restarts and renames.
func uidOf(f, repo string) (uint32, error) {
	m, err := uids(f)
	if err != nil {
		return 0, err
	}
	if uid, ok := m[repo]; ok {
		return uid, nil
	}
	uid := uint32(firstUID)
	for _, u := range m {
		uid = max(uid, u+1)
	}
	m[repo] = uid
	b, _ := json.Marshal(m)
	return uid, WriteAtomic(f, b)
}

// WriteAtomic writes b to f (0600, its dir made 0700) through a rename, synced: a reader never sees half a file, and
// a host crash leaves the old one or the new one, never an empty one. One f.tmp: callers serialize their writes of f.
func WriteAtomic(f string, b []byte) error {
	dir := filepath.Dir(f)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	t, err := os.OpenFile(f+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the server's own files, not a PR's path
	if err != nil {
		return err
	}
	_, err = t.Write(b)
	if err = errors.Join(err, t.Sync(), t.Close()); err != nil {
		return err
	}
	if err := os.Rename(f+".tmp", f); err != nil {
		return err
	}
	d, err := os.Open(dir) //nolint:gosec // as above; syncs the rename itself
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// Capped keeps the first and the last N bytes written to it: PR code can print without end, and a check's
// errors come first, a test run's summary last.
type Capped struct {
	N          int
	head, tail []byte
	cut        bool
}

func (c *Capped) Write(p []byte) (int, error) {
	n := len(p)
	if room := c.N - len(c.head); room > 0 {
		k := min(room, len(p))
		c.head, p = append(c.head, p[:k]...), p[k:]
	}
	if c.tail = append(c.tail, p...); len(c.tail) > 2*c.N { // amortized: trim once it holds twice N
		c.tail, c.cut = append([]byte(nil), c.tail[len(c.tail)-c.N:]...), true
	}
	return n, nil
}

func (c *Capped) String() string {
	if t := c.tail; c.cut || len(t) > c.N {
		return string(c.head) + "\n[… cut …]\n" + string(t[len(t)-c.N:])
	}
	return string(c.head) + string(c.tail)
}

// legacy reports whether key is a repo's name, as uids.json had them before forge.RepoID (names have no colon). Who
// owned such an entry is unknown: the repo of that name now may be another. So its uid stays taken, never handed out
// again, and Clean drops its cache; the repo starts over with a uid of its own on its next job. The file stays as it
// is: nothing to migrate is nothing a crash can cut in half.
func legacy(key string) bool { return !strings.Contains(key, ":") }

// uids is the repos' uids in f; none when there is no f yet.
func uids(f string) (map[string]uint32, error) {
	m := map[string]uint32{}
	b, err := os.ReadFile(f) //nolint:gosec // DataDir's
	if err == nil {
		err = json.Unmarshal(b, &m)
	} else if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err == nil && m == nil {
		err = errors.New("null")
	}
	if err != nil { // never start over: the next repo would get a uid that is another's
		return nil, fmt.Errorf("repo uids: %w", err)
	}
	seen := map[uint32]string{} // a hand edit's slips: two repos would share a cache and each other's kills, 0 is root
	for r, u := range m {
		if u < firstUID {
			return nil, fmt.Errorf("repo uids: %s has uid %d, below %d", r, u, firstUID)
		}
		if o, ok := seen[u]; ok {
			return nil, fmt.Errorf("repo uids: %s and %s share uid %d", o, r, u)
		}
		seen[u] = r
	}
	return m, nil
}

// maxRead bounds what ReadFile reads: the PR picks what is there.
const maxRead = 64 << 20

// ReadFile reads p, a regular file of at most maxRead bytes: a symlink to /dev/zero or a FIFO would never end or fill
// the server's memory. ReadFile, WriteFile and Remove touch the checkout and work dir as the job's user when we're root:
// PR code controls what is there, and a symlink to a root-only file (the server's /proc/1/environ, the ChatGPT login) must not resolve with
// root's rights.
func (s *Sandbox) ReadFile(ctx context.Context, p string) ([]byte, error) {
	if fi, err := os.Lstat(p); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() || fi.Size() > maxRead {
		return nil, fmt.Errorf("%s: no regular file of at most %d bytes", p, maxRead)
	}
	// p may change after Lstat: head reads it bounded, under ctx
	b, err := Run(s.Command(ctx, "/", nil, "head", "-c", strconv.Itoa(maxRead+1), "--", p))
	if err == nil && len(b) > maxRead {
		return nil, fmt.Errorf("%s: over %d bytes", p, maxRead)
	}
	return b, err
}

// WriteFile writes b to p, as the job's user when we're root, a new file in its place: what PR code left there (a
// symlink, a FIFO that would block the write and the job) is neither followed nor opened. A regular file's mode stays.
func (s *Sandbox) WriteFile(ctx context.Context, p string, b []byte) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	// set -C creates with O_EXCL, but opens a FIFO planted after rm: the timeout ends that race's wait.
	c := s.Command(ctx, "/", nil, "sh", "-c", `m=; [ -f "$1" ] && [ ! -L "$1" ] && m=$(stat -c %a -- "$1")
rm -f -- "$1" && set -C && cat > "$1" && { [ -z "$m" ] || chmod -- "$m" "$1"; }`, "sh", p)
	c.Stdin = bytes.NewReader(b)
	_, err := Run(c)
	return err
}

// Remove removes p, as the job's user when we're root; a missing p is no error.
func (s *Sandbox) Remove(p string) error {
	if !AsPi {
		if err := os.Remove(p); !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	_, err := Run(s.Command(context.Background(), "/", nil, "rm", "-f", "--", p))
	return err
}

// Clean readies the server's start, before any job: what isn't a repo's cache goes, i.e. the work dirs a crash left
// and, as root, the cache all repos shared before each got its own uid (node's: any PR's code could have changed it),
// the legacy entries' caches, and every job user's processes and files in the shared temp dirs.
func Clean() error {
	if !AsPi {
		return os.RemoveAll(workDir())
	}
	// Clean takes CacheDir over and empties it: never a dir with other things in it, e.g. DataDir, also by a symlink.
	if rel, err := filepath.Rel(resolved(CacheDir), resolved(DataDir)); err != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("CACHE_DIR %s holds DATA_DIR %s: give each its own dir", CacheDir, DataDir)
	}
	// CacheDir was node's: root's now, or a repo's user could swap another's cache dir for a symlink.
	if err := errors.Join(os.MkdirAll(CacheDir, 0o755), os.Lchown(CacheDir, 0, 0), os.Chmod(CacheDir, 0o755)); err != nil {
		return err
	}
	// No job runs yet: every job user's processes and temp files go, also those of a repo with no job in a long while.
	if err := clearUsers(jobUser); err != nil {
		return err
	}
	m, err := uids(filepath.Join(DataDir, "uids.json"))
	if err != nil {
		return err
	}
	keep := kept(m)
	ds, err := os.ReadDir(CacheDir)
	if err != nil {
		return err
	}
	var old []string
	for _, d := range ds {
		p := filepath.Join(CacheDir, d.Name())
		fi, err := os.Lstat(p)
		// A repo's cache, named for its owner (node couldn't make one for a repo's uid), and that uid still the repo's:
		// without uids.json (a lost DATA_DIR) a new repo would get it, and the old one's cache. A legacy uid's goes too.
		if err == nil && fi.IsDir() && keep[owner(fi)] && d.Name() == strconv.Itoa(owner(fi)) {
			continue
		}
		if d.Name() != "work" {
			old = append(old, d.Name())
		}
		if err := os.RemoveAll(p); err != nil { // as root: past a user's chmod 000, and never through a symlink
			return err
		}
	}
	if len(old) > 0 {
		log.Printf("removed the caches no repo id owns from %s: %s", CacheDir, strings.Join(old, " "))
	}
	return nil
}

// kept is the uids whose caches Clean keeps: node's and the repo ids'.
func kept(m map[string]uint32) map[int]bool {
	keep := map[int]bool{1000: true}
	for r, u := range m {
		if !legacy(r) {
			keep[int(u)] = true
		}
	}
	return keep
}

// resolved is p with its symlinks resolved, of its nearest existing parent while it doesn't exist yet.
func resolved(p string) string {
	p, _ = filepath.Abs(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	} else if dir := filepath.Dir(p); dir != p {
		return filepath.Join(resolved(dir), filepath.Base(p))
	}
	return p
}

// procDir lists the processes killLeftovers checks. A var: tests can't break /proc.
var procDir = "/proc"

// tmpDirs are the temp dirs all users share: TMPDIR is the job's own, but code may write to any of them.
var tmpDirs = []string{"/tmp", "/var/tmp", "/dev/shm"}

// clearUsers kills the processes of the users whose uid is of and removes their files in the temp dirs all share and
// their work dirs: one Close couldn't remove goes before the user's next job.
func clearUsers(of func(uid int) bool) error {
	return errors.Join(killLeftovers(of), cleanTmp(of, append(slices.Clone(tmpDirs), workDir())...))
}

// cleanTmp removes what the users whose uid is of left in dirs, also inside a dir root or the image made
// world-writable (/tmp/.X11-unix): the next job's code would find it.
func cleanTmp(of func(uid int) bool, dirs ...string) error {
	cache, _ := os.Stat(CacheDir) // in /tmp by default: the users' caches stay, workDir in it is one of dirs
	for _, dir := range dirs {
		r, err := os.OpenRoot(dir) // a dir swapped for a symlink leads nowhere outside dir
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil { // what is left there would reach the next job
			return err
		}
		err = cleanIn(r, of, cache, 0)
		r.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func cleanIn(r *os.Root, of func(uid int) bool, cache os.FileInfo, depth int) error {
	f, err := r.Open(".")
	if err != nil {
		return err
	}
	ds, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return err
	}
	for _, d := range ds {
		fi, err := r.Lstat(d.Name())
		switch {
		case errors.Is(err, os.ErrNotExist): // gone since
		case err != nil:
			return err
		case cache != nil && os.SameFile(fi, cache):
		case of(owner(fi)):
			if err := r.RemoveAll(d.Name()); err != nil { // as root: past a user's chmod 000, and never through a symlink
				return err
			}
		// Not a job user's dir: those go with their own job. The depth bounds a loop of symlinks swapped in.
		case fi.IsDir() && owner(fi) < firstUID && depth < 8:
			sub, err := r.OpenRoot(d.Name())
			if errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
			err = cleanIn(sub, of, cache, depth+1)
			sub.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// killLeftovers kills every process of the users whose uid is of: what PR code left running (a daemon from an
// install script) would read a later job's environ and checkout, i.e. its tokens, or plant files for it. As each user,
// kill -9 -1: the kernel signals all of its processes at once, however deep they nest or fast they fork. An error when
// some still run after 5 rounds.
func killLeftovers(of func(uid int) bool) error {
	for round := 0; ; round++ { // the pids limit may leave the killer no pid: then one by one, and again
		left := map[uint32][]int{}
		ds, err := os.ReadDir(procDir) // none read: none checked, so never "all dead"
		if err != nil {
			return err
		}
		for _, d := range ds {
			pid, err := strconv.Atoi(d.Name())
			if err != nil {
				continue
			}
			b, err := os.ReadFile(filepath.Join(procDir, d.Name(), "status"))
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) { // ended since
				continue
			} else if err != nil {
				return err
			}
			uid, zombie := 0, false
			for _, l := range strings.Split(string(b), "\n") {
				if f := strings.Fields(l); len(f) > 1 && f[0] == "Uid:" {
					uid, _ = strconv.Atoi(f[1])
				} else if len(f) > 1 && f[0] == "State:" {
					zombie = f[1] == "Z" // dead already, init reaps it
				}
			}
			if uid >= firstUID && of(uid) && !zombie { // never root's or the image's users'
				left[uint32(uid)] = append(left[uint32(uid)], pid)
			}
		}
		if len(left) == 0 {
			return nil
		}
		if round == 5 {
			return errors.New("processes survived kill -9")
		}
		for uid, pids := range left {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			c := exec.CommandContext(ctx, "kill", "-9", "-1")
			c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
			if c.Run() != nil {
				for _, pid := range pids {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
			cancel()
		}
		time.Sleep(100 * time.Millisecond)
	}
}
