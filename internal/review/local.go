package review

import (
	"context"
	"diffharbor/internal/agent"
	"diffharbor/internal/checks"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"diffharbor/internal/vault"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Tree reviews `git diff HEAD` of the checkout cwd like a PR titled title, in sb, with closed as the threads resolved
// before; large: in parts, as if the walkthrough's box was ticked.
func Tree(ctx context.Context, sb *proc.Sandbox, cwd, title string, closed []Comment, large bool) (Result, string, error) {
	if err := errors.Join(os.Symlink(cwd, sb.Repo()), sb.Pin()); err != nil {
		return Result{}, "", err
	}
	out, err := proc.Run(sb.Command(ctx, cwd, nil, "git", "diff", "HEAD"))
	if err != nil {
		return Result{}, "", err
	}
	b, _ := os.ReadFile(filepath.Join(cwd, "review.json")) //nolint:gosec // the local run reads your own tree
	cfg, err := LoadSettings(string(b))
	if err != nil {
		return Result{}, "", fmt.Errorf("review.json: %w", err)
	}
	rules := cfg.ReadRules(func(names ...string) string {
		for _, n := range names {
			if b, err := os.ReadFile(filepath.Join(cwd, n)); err == nil { //nolint:gosec // your own tree
				return string(b)
			}
		}
		return ""
	})
	for _, d := range cfg.SkillPaths() {
		if rd := filepath.Join(cwd, d); proc.Exists(rd) { // -L: symlinked dev skills become real copies, as from the API
			if err := os.MkdirAll(filepath.Join(sb.Dir, "skills"), 0o755); err != nil {
				return Result{}, "", err
			}
			if _, err := proc.Run(sb.Command(ctx, cwd, nil, "cp", "-rL", rd, filepath.Join(sb.Dir, "skills", "repo"))); err != nil {
				return Result{}, "", err
			}
			break
		}
	}
	var scripts []checks.Script
	for _, p := range cfg.CheckPaths() {
		if f := filepath.Join(cwd, p); proc.Exists(f) {
			scripts = append(scripts, checks.Script{Name: p, File: f})
		}
	}
	parts, _ := diff.Filter(string(out), cwd, nil, cfg.Ignored, cfg.Parts(large))
	return Analyze(ctx, Input{Agent: agent.Pi{}, Sandbox: sb, Title: title, Parts: parts, Trusted: TrustedContext(rules, ""), Checks: scripts, Closed: closed, Settings: cfg})
}

// Checkout fetches the PR head and base into sb's Repo with history but without old file contents (blob:none) for
// co-change hints, checks out head (a commit) and returns the PR's diff as GitHub shows it:
// merge-base(base, head)..head, base a ref or commit of the remote. token reads the repo. As root git runs as the repo's user,
// like the PR's code: it fetches through the vault with a stand-in for token that works until ctx is done (blob:none
// fetches the blobs later commands need). Else token goes in the env. Never in argv: /proc/<pid>/cmdline is
// world-readable, also to another repo's job running at the same time.
func Checkout(ctx context.Context, sb *proc.Sandbox, cloneURL, head, base, token string) (string, error) {
	repo := sb.Repo()
	remote, auth := cloneURL, gitAuth(token)
	if _, err := proc.Run(sb.Command(ctx, sb.Dir, nil, "git", "init", "-q", repo)); err != nil { // as the repo's user, when we're root
		return "", err
	}
	if err := sb.Pin(); err != nil { // before the PR's code: a check could swap repo for a symlink
		return "", err
	}
	if vault.URL() != "" {
		r, tok, err := vault.Git(ctx, cloneURL, token)
		if err != nil {
			return "", err
		}
		// Every git in the checkout sends the token, the fetches Pi or the PR's code start too: from a file the repo's
		// config includes, written through stdin. Only its path is in argv.
		f := filepath.Join(sb.Dir, "git-auth")
		if err := sb.WriteFile(ctx, f, []byte(fmt.Sprintf("[http %q]\n\textraHeader = Authorization: Bearer %s\n", r, tok))); err != nil {
			return "", err
		}
		if _, err := proc.Run(sb.Command(ctx, repo, nil, "git", "config", "include.path", f)); err != nil {
			return "", err
		}
		remote = r
	}
	git := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		out, err := proc.Run(sb.Command(ctx, sb.Dir, auth, "git", append([]string{"-C", repo}, args...)...))
		if err != nil {
			return "", fmt.Errorf("git %s: %w", args[0], err)
		}
		return string(out), nil
	}
	for _, args := range [][]string{
		{"remote", "add", "origin", remote},
		{"fetch", "-q", "--filter=blob:none", "--depth=" + historyDepth, "origin", head, base + ":refs/base"},
		{"checkout", "-q", "--detach", head},
	} {
		if _, err := git(args...); err != nil {
			return "", err
		}
	}
	mb, err := git("merge-base", "refs/base", head)
	if err != nil { // the fork point is older than historyDepth on one side
		if _, err := git("fetch", "-q", "--filter=blob:none", "--unshallow", "origin", head, base+":refs/base"); err != nil {
			return "", err
		}
		if mb, err = git("merge-base", "refs/base", head); err != nil {
			return "", err
		}
	}
	return git("diff", "-M", strings.TrimSpace(mb), head)
}

// gitAuth is the env that lets git read the repo with token; none through the vault: the checkout's config sends its
// stand-in (Checkout).
func gitAuth(token string) []string {
	if vault.URL() != "" {
		return nil
	}
	return []string{
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)),
	}
}

// Since is the diff from an earlier head from to the head Checkout put in sb's Repo, with the same ctx and token: what
// a push changed. "" when from can't be fetched, e.g. gone in a force push; an error when it was and git still failed.
func Since(ctx context.Context, sb *proc.Sandbox, from, token string) (string, error) {
	git := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		out, err := proc.Run(sb.Command(ctx, sb.Dir, gitAuth(token), "git", append([]string{"-C", sb.Repo()}, args...)...))
		return string(out), err
	}
	if _, err := git("cat-file", "-e", from+"^{commit}"); err != nil { // older than the fetched history
		if _, err := git("fetch", "-q", "--filter=blob:none", "--depth=1", "origin", from); err != nil {
			return "", nil
		}
	}
	out, err := git("diff", "-M", from, "HEAD")
	if err != nil {
		return "", fmt.Errorf("git diff since %s: %w", from, err)
	}
	return out, nil
}

// Commit is one commit of the PR: its subject and size.
type Commit struct {
	SHA, Subject    string
	Author          string // its author's name
	Files, Add, Del int
	Lines           []string // "+" or "-" and the trimmed line, for each it adds or removes: which commit brought a declaration
	Paths           []string // Behind's: the files it changes
}

// maxCommits is the commits a report lists.
const maxCommits = 200

// Commits lists the PR's commits oldest first, from a Checkout: what head has and the base hasn't. Merges of the
// base into the branch say nothing about the change and stay out. nil when git fails: the report goes without.
func Commits(ctx context.Context, sb *proc.Sandbox) []Commit {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := proc.Run(sb.Command(ctx, sb.Repo(), nil, "git", "log", "--reverse", "--no-merges", "--no-renames",
		"--format=%x00%H%x09%an%x09%s", "-p", "-U0", "refs/base..HEAD"))
	if err != nil {
		return nil
	}
	return parseCommits(string(out))
}

// Messages is the PR's commit messages oldest first, from a Checkout, merges aside. "" when git fails.
func Messages(ctx context.Context, sb *proc.Sandbox) string {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := proc.Run(sb.Command(ctx, sb.Repo(), nil, "git", "log", "--reverse", "--no-merges", "--format=- %B", "refs/base..HEAD"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// aiAuthors match a commit (author line, then message) to the provider of the AI that wrote it. Only clear signals:
// Cursor and Copilot pick varying models. The commit is untrusted, it only picks a model.
var aiAuthors = []struct {
	provider string
	re       *regexp.Regexp
}{
	{"anthropic", regexp.MustCompile(`(?im)^co-authored-by:\s*claude|generated with \[claude code\]`)},
	{"openai", regexp.MustCompile(`(?im)^co-authored-by:\s*codex|\A[^\n]*codex`)},
	{"google", regexp.MustCompile(`(?im)^co-authored-by:\s*gemini|\A[^\n]*gemini-code-assist`)},
}

// Authors is the provider of the AI behind each of the PR's commits an AI wrote, from a Checkout. nil when git fails.
func Authors(ctx context.Context, sb *proc.Sandbox) []string {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := proc.Run(sb.Command(ctx, sb.Repo(), nil, "git", "log", "--no-merges", "--format=%an%n%B%x00", "refs/base..HEAD"))
	if err != nil {
		return nil
	}
	return authorsOf(string(out))
}

func authorsOf(log string) (ps []string) {
	for c := range strings.SplitSeq(log, "\x00") {
		for _, a := range aiAuthors {
			if a.re.MatchString(strings.TrimLeft(c, "\n")) {
				ps = append(ps, a.provider)
				break
			}
		}
	}
	return ps
}

// Behind lists the base's commits the PR doesn't have yet, newest first, from a Checkout: what moved on under it.
// nil when git fails.
func Behind(ctx context.Context, sb *proc.Sandbox) []Commit {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := proc.Run(sb.Command(ctx, sb.Repo(), nil, "git", "log", "--no-merges", "--no-renames", "--format=%x00%H%x09%s",
		"--name-only", "HEAD..refs/base"))
	if err != nil {
		return nil
	}
	var cs []Commit
	for c := range strings.SplitSeq(string(out), "\x00") {
		head, files, _ := strings.Cut(c, "\n")
		if sha, subject, ok := strings.Cut(head, "\t"); ok {
			cs = append(cs, Commit{SHA: sha, Subject: subject, Paths: strings.Fields(files)})
		}
	}
	return cs
}

func parseCommits(log string) []Commit {
	var cs []Commit
	for c := range strings.SplitSeq(log, "\x00") {
		head, patch, _ := strings.Cut(c, "\n")
		sha, rest, ok := strings.Cut(head, "\t")
		author, subject, ok2 := strings.Cut(rest, "\t")
		if !ok || !ok2 {
			continue
		}
		cm := Commit{SHA: sha, Author: author, Subject: subject}
		header := false // a file's header, up to its first hunk: "--- a/x" is no removed line
		for l := range strings.SplitSeq(patch, "\n") {
			switch {
			case strings.HasPrefix(l, "diff --git "):
				cm.Files, header = cm.Files+1, true
			case strings.HasPrefix(l, "@@"):
				header = false
			case header || l == "":
			case l[0] == '+':
				cm.Add++
				cm.Lines = append(cm.Lines, "+"+strings.TrimSpace(l[1:]))
			case l[0] == '-':
				cm.Del++
				cm.Lines = append(cm.Lines, "-"+strings.TrimSpace(l[1:]))
			}
		}
		cs = append(cs, cm)
	}
	return cs[max(0, len(cs)-maxCommits):]
}
