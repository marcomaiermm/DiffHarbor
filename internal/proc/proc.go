// Package proc runs git, checks and the agent as the unprivileged user, in work dirs next to the tool caches.
package proc

import (
	"bytes"
	"context"
	"diffharbor/internal/text"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	// AsPi runs git, checks and Pi as node: the server runs as root so they can't read its environ, i.e. the App key.
	AsPi = os.Getuid() == 0
	// DataDir is the data volume (DATA_DIR): logins, learnings, pending jobs.
	DataDir = filepath.Join(os.TempDir(), "diffharbor")
	// CacheDir is the cache (CACHE_DIR): pnpm store, work dirs.
	CacheDir = os.TempDir()
	// WorkDir is where Workspace makes its dirs; "" = the system temp dir.
	WorkDir = ""
)

// ToolEnv points the caches for installs and builds the checks and Pi's bash run at CacheDir (pnpm store and work
// dirs share a volume: hardlinks).
func ToolEnv() []string {
	return []string{
		"CI=1", "XDG_CACHE_HOME=" + CacheDir, "npm_config_cache=" + CacheDir + "/npm",
		"npm_config_store_dir=" + CacheDir + "/pnpm-store", "pnpm_config_store_dir=" + CacheDir + "/pnpm-store",
		"COREPACK_HOME=" + CacheDir + "/corepack", "COREPACK_ENABLE_DOWNLOAD_PROMPT=0",
		"GOPATH=" + CacheDir + "/go", "GOCACHE=" + CacheDir + "/go-build", "GOTOOLCHAIN=auto",
		"NODE_OPTIONS=--max-old-space-size=2048", "DO_NOT_TRACK=1",
	}
}

// Command runs as node (when we're root) with only PATH, HOME and extra env, so children never see the App key.
func Command(ctx context.Context, dir string, extra []string, name string, args ...string) *exec.Cmd {
	home := os.Getenv("HOME")
	if AsPi {
		home = "/home/node"
	}
	c := exec.CommandContext(ctx, name, args...)
	c.Dir, c.WaitDelay = dir, 5*time.Second
	c.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_TERMINAL_PROMPT=0"}, extra...)
	if AsPi {
		c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1000, Gid: 1000}}
	}
	return c
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

// Workspace is a temp dir that git, checks and Pi (uid node) can write.
func Workspace() (string, error) {
	dir, err := os.MkdirTemp(WorkDir, "review-")
	if err != nil {
		return "", err
	}
	if AsPi {
		if err := os.Chown(dir, 1000, 1000); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
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

// ReadFile reads p. ReadFile, WriteFile and Remove touch the checkout and work dir as node when we're root: PR code controls what is
// there, and a symlink to a root-only file (the server's /proc/1/environ, the ChatGPT login) must not resolve with
// root's rights.
func ReadFile(p string) ([]byte, error) {
	if !AsPi {
		return os.ReadFile(p) //nolint:gosec // not root: the OS checks access
	}
	return Run(Command(context.Background(), "/", nil, "cat", "--", p))
}

// WriteFile writes b to p, as node when we're root.
func WriteFile(p string, b []byte) error {
	if !AsPi {
		return os.WriteFile(p, b, 0o644)
	}
	c := Command(context.Background(), "/", nil, "sh", "-c", `cat > "$1"`, "sh", p)
	c.Stdin = bytes.NewReader(b)
	_, err := Run(c)
	return err
}

// Remove removes p, as node when we're root; a missing p is no error.
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

// KillOrphans kills what PR code left running as node (a daemon from an install script that could read the next
// job's git token or keys): node processes whose chain of node parents ends at init, i.e. outlived the process
// that started them. A dry run in another docker exec keeps its processes: their chain ends at a root process.
func KillOrphans() {
	if !AsPi {
		return
	}
	for range 5 { // a killed parent's children become orphans in turn
		type proc struct{ ppid, uid int }
		ps := map[int]proc{}
		ds, _ := os.ReadDir("/proc")
		for _, d := range ds {
			pid, err := strconv.Atoi(d.Name())
			b, _ := os.ReadFile("/proc/" + d.Name() + "/status")
			if err != nil || b == nil {
				continue
			}
			x := proc{uid: -1}
			for _, l := range strings.Split(string(b), "\n") {
				if f := strings.Fields(l); len(f) > 1 && f[0] == "PPid:" {
					x.ppid, _ = strconv.Atoi(f[1])
				} else if len(f) > 1 && f[0] == "Uid:" {
					x.uid, _ = strconv.Atoi(f[1])
				}
			}
			ps[pid] = x
		}
		killed := 0
		for pid, x := range ps {
			for depth := 0; x.uid == 1000 && depth < 64; depth++ {
				if x.ppid == 1 {
					if syscall.Kill(pid, syscall.SIGKILL) == nil {
						killed++
					}
					break
				}
				x = ps[x.ppid]
			}
		}
		if killed == 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
