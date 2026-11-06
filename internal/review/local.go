package review

import (
	"context"
	"diffharbor/internal/agent"
	"diffharbor/internal/checks"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"diffharbor/internal/vault"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Tree reviews `git diff HEAD` of the checkout cwd like a PR titled title, in sb
// and closed as the threads resolved before; large: in parts, as if the walkthrough's box was ticked.
func Tree(ctx context.Context, sb *proc.Sandbox, cwd, title string, closed []Comment, large bool) (Result, string, error) {
	if err := os.Symlink(cwd, filepath.Join(sb.Dir, "repo")); err != nil {
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
	rules := ""
	for _, n := range cfg.RulePaths() {
		if b, err := os.ReadFile(filepath.Join(cwd, n)); err == nil { //nolint:gosec // your own tree
			rules = string(b)
			break
		}
	}
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

// Checkout fetches the PR head and base into repo with history but without old file contents (blob:none) for
// co-change hints, checks out head (a commit) and returns the PR's diff as GitHub shows it:
// merge-base(base, head)..head, base a ref or commit of the remote. token reads the repo. As root git runs as the repo's user,
// like the PR's code: it fetches through the vault, whose remote works until ctx is done (blob:none fetches the
// blobs later commands need) and never holds token. Else token goes in the env (argv is world-readable).
func Checkout(ctx context.Context, sb *proc.Sandbox, repo, cloneURL, head, base, token string) (string, error) {
	remote, auth := cloneURL, []string{
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)),
	}
	if vault.URL() != "" {
		r, err := vault.Git(ctx, cloneURL, token)
		if err != nil {
			return "", err
		}
		remote, auth = r, nil
	}
	git := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		out, err := proc.Run(sb.Command(ctx, filepath.Dir(repo), auth, "git", append([]string{"-C", repo}, args...)...))
		if err != nil {
			return "", fmt.Errorf("git %s: %w", args[0], err)
		}
		return string(out), nil
	}
	if _, err := proc.Run(sb.Command(ctx, filepath.Dir(repo), nil, "git", "init", "-q", repo)); err != nil { // as the repo's user, when we're root
		return "", err
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
func Commits(ctx context.Context, sb *proc.Sandbox, repo string) []Commit {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := proc.Run(sb.Command(ctx, repo, nil, "git", "log", "--reverse", "--no-merges", "--no-renames",
		"--format=%x00%H%x09%an%x09%s", "-p", "-U0", "refs/base..HEAD"))
	if err != nil {
		return nil
	}
	return parseCommits(string(out))
}

// Messages is the PR's commit messages oldest first, from a Checkout, merges aside. "" when git fails.
func Messages(ctx context.Context, sb *proc.Sandbox, repo string) string {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := proc.Run(sb.Command(ctx, repo, nil, "git", "log", "--reverse", "--no-merges", "--format=- %B", "refs/base..HEAD"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Behind lists the base's commits the PR doesn't have yet, newest first, from a Checkout: what moved on under it.
// nil when git fails.
func Behind(ctx context.Context, sb *proc.Sandbox, repo string) []Commit {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := proc.Run(sb.Command(ctx, repo, nil, "git", "log", "--no-merges", "--no-renames", "--format=%x00%H%x09%s",
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
