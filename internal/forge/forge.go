// Package forge is what the review and chat jobs need from a code host: GitHub (forge/github) or GitLab
// (forge/gitlab). It also holds what both render the same: the walkthrough comment and the findings.
package forge

import (
	"diffharbor/internal/knowledge"
	"diffharbor/internal/report"
	"diffharbor/internal/review"
	"diffharbor/internal/text"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Mark tags everything the bot posts: its threads, replies and reviews.
const Mark = "<!-- diffharbor -->"

// ErrSignature is a webhook whose signature or token doesn't match.
var ErrSignature = errors.New("bad webhook signature")

// Forge is one code host the server takes webhooks from.
type Forge interface {
	// Bot is the bot's handle: @Bot in a comment talks to it.
	Bot() string
	// Parse checks a webhook and reads it into an Event; nil, nil for events the bot ignores.
	Parse(h http.Header, body []byte) (*Event, error)
	// Open gets the access the jobs need for the event's PR.
	Open(e *Event) (PR, error)
	// Boxes reports whether a maintainer can tick a box in our comment (AskLarge); GitLab lets only its author.
	Boxes() bool
	// Instance is the host the forge's API is on: two GitLabs can each have a project 5.
	Instance() string
}

// RepoID names e's repo for good: its forge, the forge's instance and the repo's ID, which survive a rename and
// differ for a new repo of the old name.
func RepoID(f Forge, e *Event) (string, error) {
	if e.Repository.ID == 0 {
		return "", fmt.Errorf("%s: no repository id", e.Repo())
	}
	return fmt.Sprintf("%s:%s:%d", e.Forge, f.Instance(), e.Repository.ID), nil
}

// PR is one pull or merge request, opened for a job.
type PR interface {
	// Refresh reloads the event's PullRequest: a redelivered or long-queued event can be stale.
	Refresh() error
	// Status sets the commit status: pending, success or error. Failures only log.
	Status(sha, state, desc string)
	// ReadToken reads the repo: what git fetches with, through the vault as root (git runs next to the PR's code).
	ReadToken() (string, error)
	// File returns the first of names that exists at ref (the base, so the PR can't change it).
	File(ref string, names ...string) string
	// NestedRules returns the AGENTS.md (or CLAUDE.md) at ref in the dirs above files, below the root.
	NestedRules(ref string, files []string) string
	// Skills copies the review skills at ref, from the first of dirs that exists, under dst.
	Skills(ref string, dirs []string, dst string)
	// Wiki returns the repo's wiki pages (markdown), none when the forge can't read them.
	Wiki() ([]knowledge.File, error)
	// Notes are the PR's top-level comments, oldest first.
	Notes() ([]Note, error)
	// Note posts a top-level comment (id 0) or edits ours, and returns its id.
	Note(id int64, body string) (int64, error)
	// Threads are our inline findings on the PR.
	Threads() ([]Thread, error)
	// Voted is Threads with Up and Down filled: who reacted 👍 and 👎 on each finding. Only the outcome collection
	// reads reactions; they cost a call per thread on GitLab.
	Voted() ([]Thread, error)
	// Configure takes the repo's review.json, before Resolve and Post.
	Configure(cfg review.Settings)
	// Resolve answers a thread whose finding sha fixed, then resolves it. On GitLab the answer may wait for Post or
	// Publish, to go out with the review.
	Resolve(t Thread, sha string)
	// Reopen answers a resolved thread with c, a finding a commit (sha) brought back, and reopens it. An error:
	// nothing was posted.
	Reopen(t Thread, sha string, c Finding) error
	// Publish sends what Resolve held back when no Post follows.
	Publish()
	// Post sends the findings of a review of sha: on lines of the PR's diff raw inline, the rest in one comment.
	// Returns how many it posted, the folded ones (Comment.Fold) not.
	Post(sha string, r review.Result, threads []Thread, raw, footer string) (int, error)
	// Discussion is the thread of the event's review comment.
	Discussion() (Discussion, error)
	// Reply answers the event's comment: in its thread, or on the PR.
	Reply(body string) error
	// ResolveDiscussion resolves the thread of the event's review comment.
	ResolveDiscussion() error
	// React puts 👀 on the event's comment.
	React()
	// CanWrite reports whether login may push to the repo.
	CanWrite(login string) (bool, error)
	// Others are the repo's open PRs but this one, the 50 updated last, with the files they change (no Clash).
	Others() ([]report.Other, error)
	// Patches is PR n's diff by file: hunks only, none for binary or huge files.
	Patches(n int) (map[string]string, error)
	// Links is how the forge links what a report points to.
	Links() report.Forge
}

// Note is a comment.
type Note struct {
	ID    int64
	Login string
	Body  string
	Ours  bool
	// Edited after it was posted: chat skips edits, so what it says now was never queued.
	Edited bool
}

// Thread is one of our inline review threads.
type Thread struct {
	ID       string // GitHub: GraphQL node id; GitLab: discussion id
	Comment  int64  // id of the first comment
	Resolved bool
	Resolver string // who resolved it; blank for us (the chat pass) or when unknown
	Path     string
	Line     int // 0 when the line is outdated
	Original int
	Body     string
	Replies  string // Answers(): the last replies, where a maintainer declines what the finding or our answer said
	// Addressed(): resolved by our "✅ Addressed in", nobody replied since. A commit fixed it: one bringing it back
	// reopens it.
	Addressed bool
	Up, Down  []string // Voted(): logins that reacted 👍 and 👎 on the finding
}

// AddressedIn is Resolve's answer: sha fixed the thread's finding.
func AddressedIn(sha string) string { return fmt.Sprintf("%s`%s`\n\n%s", addressed, sha[:7], Mark) }

const addressed = "✅ Addressed in "

// Addressed reports whether a thread's last reply (ns) is our AddressedIn.
func Addressed(ns []Note) bool {
	return len(ns) > 0 && ns[len(ns)-1].Ours && strings.HasPrefix(ns[len(ns)-1].Body, addressed)
}

// BackIn is Reopen's answer: the finding c, back in sha. format is the forge's Format, made safe for it.
func BackIn(sha string, c Finding, format func(Finding) string) string {
	return fmt.Sprintf("🔁 Back in `%s`, at `%s:%d`\n\n%s\n\n%s", sha[:7], c.Path, c.Line, format(c), Mark)
}

// Answers are a thread's last replies after its first comment, cleaned and cut short for a prompt.
func Answers(ns []Note) string {
	var b strings.Builder
	for _, n := range ns[max(0, len(ns)-3):] {
		fmt.Fprintf(&b, "\n\n**@%s**: %s", n.Login, text.Truncate(Clean(n.Body), 300))
	}
	return b.String()
}

// Discussion is a review thread with what it is about.
type Discussion struct {
	Ours     bool   // we started it: chat may resolve it
	Resolved bool   // someone resolved it already
	About    string // markdown: the file and its hunk
	Notes    []Note
}

// RuleDirs are the dirs above files, below the root, whose AGENTS.md applies to them.
func RuleDirs(files []string) []string {
	want := map[string]bool{}
	for _, f := range files {
		for d := path.Dir(f); d != "."; d = path.Dir(d) {
			want[d] = true
		}
	}
	return slices.Sorted(maps.Keys(want))
}

// Rules joins the rules files of pick (dir -> its AGENTS.md or CLAUDE.md): at most 5 files and 6000 chars.
func Rules(pick map[string]string, read func(string) string) string {
	var b strings.Builder
	for _, d := range slices.Sorted(maps.Keys(pick))[:min(len(pick), 5)] {
		if s := strings.TrimSpace(read(pick[d])); s != "" {
			fmt.Fprintf(&b, "\n\n### %s (for changes under %s/)\n\n%s", pick[d], d, s)
		}
	}
	return text.Truncate(b.String(), 6000)
}

// reportSecret is a report link's secret (job.Secret) in a comment, also percent-encoded: /r/ decodes it.
var reportSecret = regexp.MustCompile(`(?:/|%2[Ff])(?:[A-Z2-7]|%[0-9A-Fa-f]{2}){26}\b`)

// Redact cuts report links' secrets from text for a prompt: they are the PR's readers', not the model's.
func Redact(s string) string { return reportSecret.ReplaceAllString(s, "/…") }

// Clean drops our hidden markers and state from a comment before it goes into a prompt, and report links' secrets.
func Clean(s string) string {
	s = Redact(s)
	return text.Truncate(strings.TrimSpace(stateTag.ReplaceAllString(strings.NewReplacer(Mark, "", stickyMark, "").Replace(s), "")), 3000)
}

// MentionOf matches @handle for any of handles as a whole login (not @handle-other).
func MentionOf(handles ...string) *regexp.Regexp {
	for i, h := range handles {
		handles[i] = regexp.QuoteMeta(h)
	}
	// dots end a sentence, not a GitLab username: @bot. yes, @bot.ann no
	return regexp.MustCompile(`(?i)(^|[^\w.+-])@(` + strings.Join(handles, "|") + `)\.*([^\w.-]|$)`)
}
