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
	"strconv"
	"strings"
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

// firstUID is the first repo's uid and gid; the image's users are below it, node (1000) runs dry runs.
const firstUID = 10000

// user is who Command runs as when we're root, set by Workspace: the job's repo's uid, and the home and temp dir in
// its work dir. One per process will do: the server runs one job at a time (server.work), a dry run is a process of
// its own, and all of a job's checks run for its repo.
var user = struct {
	uid       uint32
	home, tmp string
}{1000, "/home/node", ""}

// jobUser reports whether uid runs PR code: a repo's user, or node (dry runs, and every job before a uid per repo).
func jobUser(uid int) bool { return uid == 1000 || uid >= firstUID }

func owner(fi os.FileInfo) int { return int(fi.Sys().(*syscall.Stat_t).Uid) }

func workDir() string { return filepath.Join(CacheDir, "work") }

// cacheDir is the tool caches; as root the user's own: a PR's code can't change what another repo's jobs run (a
// GOCACHE entry, a package in the pnpm store, corepack's pnpm).
func cacheDir() string {
	if !AsPi {
		return CacheDir
	}
	return filepath.Join(CacheDir, strconv.Itoa(int(user.uid)))
}

// ToolEnv points the caches for installs and builds the checks and Pi's bash run at cacheDir (pnpm store and work
// dirs share a volume: hardlinks).
func ToolEnv() []string {
	c := cacheDir()
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
func Command(ctx context.Context, dir string, extra []string, name string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir, c.WaitDelay = dir, 5*time.Second
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GIT_TERMINAL_PROMPT=0"}
	if AsPi {
		c.Env[1] = "HOME=" + user.home
		if user.tmp != "" {
			c.Env = append(c.Env, "TMPDIR="+user.tmp)
		}
		c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: user.uid, Gid: user.uid}}
	}
	c.Env = append(c.Env, extra...)
	return c
}

// Home is the job's home: Pi's config is in it.
func Home() string { return user.home }

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

// Workspace is a work dir for a job on repo that git, checks and Pi can write. As root it makes repo's user (node for
// "", a dry run) the one Command runs as from now on, in a fresh home (the image's: Pi's extensions) and temp dir in
// the work dir: what a PR's code leaves there (a git hook, Pi's config, `go env -w GOFLAGS=-toolexec=…`) goes with it.
func Workspace(repo string) (string, error) {
	if err := os.MkdirAll(workDir(), 0o755); err != nil {
		return "", err
	}
	if !AsPi {
		return os.MkdirTemp(workDir(), "review-")
	}
	uid := uint32(1000)
	if repo != "" {
		var err error
		if uid, err = uidOf(filepath.Join(DataDir, "uids.json"), repo); err != nil {
			return "", err
		}
		name(uid) // node has one
	}
	user.uid, user.home, user.tmp = uid, "/nonexistent", ""
	// Each job: its code may have closed its cache to itself or opened it to the other users. Chmod follows a
	// symlink, but only root writes CacheDir (Clean): none can stand in for the dir.
	c := cacheDir()
	if err := errors.Join(os.MkdirAll(c, 0o700), os.Lchown(c, int(uid), int(uid)), os.Chmod(c, 0o700)); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(workDir(), "review-")
	if err != nil {
		return "", err
	}
	home, tmp := filepath.Join(dir, "home"), filepath.Join(dir, "tmp")
	// As root, while the dirs are root's: their user gets them, never what a symlink points to.
	err = errors.Join(os.Mkdir(home, 0o700), os.Mkdir(tmp, 0o700))
	for _, d := range []string{home, tmp, dir} {
		err = errors.Join(err, os.Lchown(d, int(uid), int(uid)))
	}
	if err == nil {
		user.home, user.tmp = home, tmp
		// As the user: the files are its, and a symlink in the tar can't make root write.
		_, err = Run(Command(context.Background(), "/", nil, "tar", "-xf", "/opt/node-home.tar", "-C", home))
	}
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
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

// uidOf is repo's uid in the root-only file f, a new one for a repo f doesn't have: one past the highest, so no two
// repos share one, and a repo keeps its uid (and its cache) across restarts.
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
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return 0, err
	}
	if err := os.WriteFile(f+".tmp", b, 0o600); err != nil { // through a rename: never half a file
		return 0, err
	}
	return uid, os.Rename(f+".tmp", f)
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

// uids is the repos' uids in f; none when there is no f yet.
func uids(f string) (map[string]uint32, error) {
	m := map[string]uint32{}
	b, err := os.ReadFile(f) //nolint:gosec // DataDir's
	if err == nil {
		err = json.Unmarshal(b, &m)
	} else if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil { // never start over: the next repo would get a uid that is another's
		return nil, fmt.Errorf("repo uids: %w", err)
	}
	seen := map[uint32]string{} // a hand edit's slip: two repos would share a cache and each other's kills
	for r, u := range m {
		if o, ok := seen[u]; ok {
			return nil, fmt.Errorf("repo uids: %s and %s share uid %d", o, r, u)
		}
		seen[u] = r
	}
	return m, nil
}

// ReadFile reads p. ReadFile, WriteFile and Remove touch the checkout and work dir as the job's user when we're root:
// PR code controls what is there, and a symlink to a root-only file (the server's /proc/1/environ, the ChatGPT login) must not resolve with
// root's rights.
func ReadFile(p string) ([]byte, error) {
	if !AsPi {
		return os.ReadFile(p) //nolint:gosec // not root: the OS checks access
	}
	return Run(Command(context.Background(), "/", nil, "cat", "--", p))
}

// WriteFile writes b to p, as the job's user when we're root.
func WriteFile(p string, b []byte) error {
	if !AsPi {
		return os.WriteFile(p, b, 0o644)
	}
	c := Command(context.Background(), "/", nil, "sh", "-c", `cat > "$1"`, "sh", p)
	c.Stdin = bytes.NewReader(b)
	_, err := Run(c)
	return err
}

// Remove removes p, as the job's user when we're root; a missing p is no error.
func Remove(p string) error {
	if !AsPi {
		if err := os.Remove(p); !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	_, err := Run(Command(context.Background(), "/", nil, "rm", "-f", "--", p))
	return err
}

// Clean readies CacheDir at the server's start: what isn't a repo's cache goes, i.e. the work dirs a crash left and,
// as root, the cache all repos shared before each got its own uid (node's: any PR's code could have changed it).
func Clean() error {
	if !AsPi {
		return os.RemoveAll(workDir())
	}
	// Clean takes CacheDir over and empties it: never a dir with other things in it, e.g. DataDir, also by a symlink.
	if rel, err := filepath.Rel(resolved(CacheDir), resolved(DataDir)); err != nil || rel == "." || !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("CACHE_DIR %s holds DATA_DIR %s: give each its own dir", CacheDir, DataDir)
	}
	// CacheDir was node's: root's now, or a repo's user could swap another's cache dir for a symlink.
	if err := errors.Join(os.MkdirAll(CacheDir, 0o755), os.Lchown(CacheDir, 0, 0), os.Chmod(CacheDir, 0o755)); err != nil {
		return err
	}
	m, err := uids(filepath.Join(DataDir, "uids.json"))
	if err != nil {
		return err
	}
	keep := map[int]bool{1000: true}
	for _, u := range m {
		keep[int(u)] = true
	}
	ds, err := os.ReadDir(CacheDir)
	if err != nil {
		return err
	}
	var old []string
	for _, d := range ds {
		p := filepath.Join(CacheDir, d.Name())
		fi, err := os.Lstat(p)
		// A repo's cache, named for its owner (node couldn't make one for a repo's uid), and that uid still the repo's:
		// without uids.json (a lost DATA_DIR) a new repo would get it, and the old one's cache.
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
		log.Printf("removed the cache all repos shared from %s: %s", CacheDir, strings.Join(old, " "))
	}
	return nil
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

// CleanTmp drops what the users left in the temp dirs all share, before each job: the next job's code would find it.
func CleanTmp() error {
	if !AsPi {
		return nil
	}
	return cleanTmp("/tmp", "/var/tmp", "/dev/shm")
}

func cleanTmp(dirs ...string) error {
	for _, dir := range dirs {
		ds, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) { // what is left there would reach the next job
			return err
		}
		for _, d := range ds {
			p := filepath.Join(dir, d.Name())
			if fi, err := os.Lstat(p); err != nil || !jobUser(owner(fi)) {
				continue
			}
			if err := os.RemoveAll(p); err != nil { // as root: past a user's chmod 000, and never through a symlink
				return err
			}
		}
	}
	return nil
}

// KillLeftovers kills every process of the repos' users, before and after each job: what PR code left running (a
// daemon from an install script) would read the next job's environ and checkout, i.e. its tokens, or plant files for
// it. As each user, kill -9 -1: the kernel signals all of its processes at once, however deep they nest or fast they
// fork. Dry runs keep theirs: node reaches no repo's files.
func KillLeftovers() {
	if !AsPi {
		return
	}
	for range 5 { // the pids limit may leave the killer no pid: then one by one, and again
		left := map[uint32][]int{}
		ds, _ := os.ReadDir("/proc")
		for _, d := range ds {
			pid, err := strconv.Atoi(d.Name())
			b, _ := os.ReadFile("/proc/" + d.Name() + "/status")
			if err != nil || b == nil {
				continue
			}
			uid, zombie := 0, false
			for _, l := range strings.Split(string(b), "\n") {
				if f := strings.Fields(l); len(f) > 1 && f[0] == "Uid:" {
					uid, _ = strconv.Atoi(f[1])
				} else if len(f) > 1 && f[0] == "State:" {
					zombie = f[1] == "Z" // dead already, init reaps it
				}
			}
			if uid >= firstUID && !zombie {
				left[uint32(uid)] = append(left[uint32(uid)], pid)
			}
		}
		if len(left) == 0 {
			return
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
