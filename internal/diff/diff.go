// Package diff reads unified git diffs: files, paths, new-file lines, fingerprints.
package diff

import (
	"crypto/sha256"
	"diffharbor/internal/text"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Max is the diff size a review gets; Filter cuts at whole files.
const Max = 60_000

var (
	// Skip matches lockfiles, generated and binary files: never reviewed.
	Skip = regexp.MustCompile(`(^|/)(pnpm-lock\.yaml|package-lock\.json|yarn\.lock|bun\.lockb?|Cargo\.lock|go\.sum|composer\.lock|poetry\.lock|uv\.lock|Gemfile\.lock|Pipfile\.lock|mix\.lock|pubspec\.lock|packages\.lock\.json|Package\.resolved|\.terraform\.lock\.hcl)$|\.(min\.(js|css)|map|snap|svg|png|jpe?g|gif|webp|ico|pdf|woff2?)$|(^|/)(dist|build|vendor|node_modules|__generated__)/|\.pb\.go$|_pb2\.py$|_gen\.go$|\.(gen|generated)\.[a-z]+$`)
	// Fill order for the budget: code, then tests, data and stylesheets, docs. #490: one 38k-char doc took most of
	// it; on #34 a 48k-char stylesheet took a part while code was left out.
	styleFile = regexp.MustCompile(`\.(css|scss|sass|less|styl)$`)
	testFile  = regexp.MustCompile(`(^|/)(tests?|__tests__|e2e)/|[._-](test|spec)\.[a-z]+$|_test\.go$|(^|/)test_[^/]+\.py$`)
	dataFile  = regexp.MustCompile(`\.(json|ya?ml|toml|csv|xml)$`)
	docFile   = regexp.MustCompile(`(?i)\.(md|mdx|rst|txt|adoc)$|(^|/)docs?/`)
	specFile  = regexp.MustCompile(`(?i)(openapi|swagger)[^/]*\.(ya?ml|json)$`)
	fileStart = regexp.MustCompile(`(?m)^diff --git `)
	newPath   = regexp.MustCompile(`(?m)^\+\+\+ b/(.+)$`)
	deleted   = regexp.MustCompile(`(?m)^--- a/(.+)\n\+\+\+ /dev/null$`)
	hunkStart = regexp.MustCompile(`\+(\d+)`)
	// declLine is an added line that declares something another file can name: a function, type, class, template.
	declLine = regexp.MustCompile(`^\+\s*(?:func |type |(?:export )?(?:async )?function |class |def |{{-? ?define )`)
)

// EachFile calls fn with each file's new path and its part of the diff. Deletions have no new path.
func EachFile(diff string, fn func(path, f string)) { each(diff, newPath, fn) }

// EachDeleted calls fn with each deleted file's path and its part of the diff.
func EachDeleted(diff string, fn func(path, f string)) { each(diff, deleted, fn) }

func each(diff string, path *regexp.Regexp, fn func(path, f string)) {
	idx := fileStart.FindAllStringIndex(diff, -1)
	for k, m := range idx {
		end := len(diff)
		if k+1 < len(idx) {
			end = idx[k+1][0]
		}
		if p := path.FindStringSubmatch(diff[m[0]:end]); p != nil {
			fn(p[1], diff[m[0]:end])
		}
	}
}

func rank(path string) int {
	if styleFile.MatchString(path) && Kind(path) == "code" {
		return 2
	}
	return map[string]int{"test": 1, "data": 2, "doc": 3}[Kind(path)]
}

// Filter drops lockfiles, generated/binary files (Skip) and what ignore matches; only (nil = all) limits to these
// paths. It packs whole files into at most n parts of up to Max each, code before tests, data, stylesheets and docs, a
// file next to others of its directory; in a part they keep the diff's order. The last part names the omitted files,
// their count returns, and with only nil the deleted files: their diffs are no use, what still relies on them is.
func Filter(diff string, only map[string]bool, ignore *regexp.Regexp, n int) ([]string, int) {
	type file struct {
		i          int
		path, diff string
	}
	var files []file
	EachFile(diff, func(p, f string) {
		if !Skip.MatchString(p) && (only == nil || only[p]) && (ignore == nil || !ignore.MatchString(p)) {
			files = append(files, file{len(files), p, f})
		}
	})
	slices.SortStableFunc(files, func(a, b file) int { return rank(a.path) - rank(b.path) })
	var bins [][]file
	var size []int
	var left []string
	for _, f := range files {
		k := slices.IndexFunc(bins, func(b []file) bool {
			return slices.ContainsFunc(b, func(o file) bool { return path.Dir(o.path) == path.Dir(f.path) })
		})
		if k < 0 || size[k]+len(f.diff) > Max {
			k = slices.IndexFunc(size, func(s int) bool { return s+len(f.diff) <= Max })
		}
		if k < 0 && len(bins) < n && len(f.diff) <= Max {
			bins, size, k = append(bins, nil), append(size, 0), len(bins)
		}
		if k < 0 {
			left = append(left, f.path)
			continue
		}
		bins[k], size[k] = append(bins[k], f), size[k]+len(f.diff)
	}
	var parts []string
	for _, b := range bins {
		slices.SortFunc(b, func(x, y file) int { return x.i - y.i })
		var out strings.Builder
		for _, f := range b {
			out.WriteString(f.diff)
		}
		parts = append(parts, out.String())
	}
	note := func(format string, fs []string) {
		if len(parts) == 0 {
			parts = []string{""}
		}
		parts[len(parts)-1] += fmt.Sprintf(format, len(fs), strings.Join(fs[:min(len(fs), 100)], ", "))
	}
	if len(left) > 0 { // named: the agent can read them in the repo
		note("\n# %d more file(s) omitted: diff too large. Changed too, read them in the repo: %s\n", left)
	}
	var gone []string
	for _, m := range deleted.FindAllStringSubmatch(diff, -1) {
		if only == nil && !Skip.MatchString(m[1]) && (ignore == nil || !ignore.MatchString(m[1])) {
			gone = append(gone, m[1])
		}
	}
	if len(gone) > 0 {
		note("\n# %d file(s) deleted: %s. Check that nothing still uses them.\n", gone)
	}
	return parts, len(left)
}

// Docs returns the changed docs and API specs of diff.
func Docs(diff string) string {
	var b strings.Builder
	EachFile(diff, func(p, f string) {
		if docFile.MatchString(p) || specFile.MatchString(p) {
			b.WriteString(f)
		}
	})
	return b.String()
}

// Map lists per file of diff the declarations its hunks sit in and those they add, one line each, at most 25:
// what a part changes, for the reviewer of another.
func Map(diff string) string {
	var b strings.Builder
	EachFile(diff, func(p, f string) {
		var ds []string
		for l := range strings.SplitSeq(f, "\n") {
			var d string
			switch {
			case strings.HasPrefix(l, "@@"):
				d = strings.TrimSpace(l[strings.LastIndex(l, "@@")+2:])
			case declLine.MatchString(l):
				d = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(l[1:]), "{"))
			}
			if d != "" && !slices.Contains(ds, d) && len(ds) < 25 {
				ds = append(ds, d)
			}
		}
		fmt.Fprintf(&b, "%s\n", p)
		for _, d := range ds {
			fmt.Fprintf(&b, "  %s\n", text.OneLine(d, 140))
		}
	})
	return b.String()
}

// PatchIDs fingerprints each file's changed lines, like git patch-id: a rebase or a merge of the base moves
// line numbers, context and blob ids, not the change.
func PatchIDs(diff string) map[string]string {
	ids := map[string]string{}
	EachFile(diff, func(p, f string) {
		h := sha256.New()
		for _, l := range strings.SplitAfter(f, "\n") {
			if strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") {
				h.Write([]byte(l))
			}
		}
		ids[p] = hex.EncodeToString(h.Sum(nil)[:8])
	})
	return ids
}

// Paths lists the new paths of the changed files; deletions have none.
func Paths(diff string) []string {
	var p []string
	for _, m := range newPath.FindAllStringSubmatch(diff, -1) {
		p = append(p, m[1])
	}
	return p
}

// RightLines returns "path:line" for every new-file line GitHub accepts an inline comment on; the added ones only without context.
func RightLines(diff string, context bool) map[string]bool {
	ok := map[string]bool{}
	EachNewLine(diff, func(at, l string) {
		if context || l[0] == '+' {
			ok[at] = true
		}
	})
	return ok
}

// NewLines maps "path:line" to the text of every new-file line in the diff.
func NewLines(diff string) map[string]string {
	text := map[string]string{}
	EachNewLine(diff, func(at, l string) { text[at] = l[1:] })
	return text
}

// EachNewLine calls fn with "path:line" and the diff line ('+' or ' ' first) of every new-file line in the diff.
func EachNewLine(diff string, fn func(at, l string)) {
	file, line, hunk := "", 0, false
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "): // a deleted file's "+++ /dev/null" keeps it ""
			file, hunk = "", false
		case !hunk && strings.HasPrefix(l, "+++ b/"):
			file = l[6:]
		case strings.HasPrefix(l, "@@"):
			hunk = true
			if m := hunkStart.FindStringSubmatch(l); m != nil {
				line, _ = strconv.Atoi(m[1])
			}
		case hunk && file != "" && (strings.HasPrefix(l, "+") || strings.HasPrefix(l, " ")):
			fn(fmt.Sprintf("%s:%d", file, line), l)
			line++
		}
	}
}

var oldRange = regexp.MustCompile(`(?m)^@@ -(\d+)(?:,(\d+))?`)

// OldRanges is the lines of the old file a file's diff (or a patch of hunks) covers, context included: [from, to).
func OldRanges(f string) [][2]int {
	var rs [][2]int
	for _, m := range oldRange.FindAllStringSubmatch(f, -1) {
		from, _ := strconv.Atoi(m[1])
		n := 1 // "-5" alone: one line; "-5,0": an insertion after line 5, it touches 5 too
		if m[2] != "" {
			n, _ = strconv.Atoi(m[2])
		}
		rs = append(rs, [2]int{from, from + max(n, 1)})
	}
	return rs
}

// Overlap tells whether two diffs of the same file change lines close enough for git to see a conflict: their
// hunks, context included, share a line. Both are read against their own base, so a base that moved shifts this.
func Overlap(a, b [][2]int) bool {
	for _, x := range a {
		for _, y := range b {
			if x[0] < y[1] && y[0] < x[1] {
				return true
			}
		}
	}
	return false
}
