// Package forge is what the review and chat jobs need from a code host: GitHub (forge/github) or GitLab
// (forge/gitlab). It also holds what both render the same: the walkthrough comment and the findings.
package forge

import (
	"diffharbor/internal/report"
	"diffharbor/internal/review"
	"diffharbor/internal/text"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path"
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
	// Notes are the PR's top-level comments, oldest first.
	Notes() ([]Note, error)
	// Note posts a top-level comment (id 0) or edits ours, and returns its id.
	Note(id int64, body string) (int64, error)
	// Threads are our inline findings on the PR.
	Threads() ([]Thread, error)
	// Resolve answers a thread whose finding sha fixed, then resolves it.
	Resolve(t Thread, sha string)
	// Post sends the findings of a review of sha: on lines of the PR's diff raw inline, the rest in one comment.
	// Returns how many it posted.
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
}

// Thread is one of our inline review threads.
type Thread struct {
	ID       string // GitHub: GraphQL node id; GitLab: discussion id
	Comment  int64  // id of the first comment
	Resolved bool
	Path     string
	Line     int // 0 when the line is outdated
	Original int
	Body     string
}

// Discussion is a review thread with what it is about.
type Discussion struct {
	Ours  bool   // we started it: chat may resolve it
	About string // markdown: the file and its hunk
	Notes []Note
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

// Clean drops our hidden markers and state from a comment before it goes into a prompt.
func Clean(s string) string {
	return text.Truncate(strings.TrimSpace(stateTag.ReplaceAllString(strings.NewReplacer(Mark, "", stickyMark, "").Replace(s), "")), 3000)
}
