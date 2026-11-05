// Package diff reads unified git diffs: files, paths, new-file lines, fingerprints.
package diff

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Max is the diff size a review gets; Filter cuts at files, a file past it at hunks.
const Max = 60_000

var (
	// Skip matches lockfiles, generated, vendored and binary files: never reviewed.
	Skip = regexp.MustCompile(`(^|/)(pnpm-lock\.yaml|package-lock\.json|npm-shrinkwrap\.json|[^/]+\.lock(b|file)?|go\.sum|packages\.lock\.json|Package\.resolved|\.terraform\.lock\.hcl)$|\.(min\.(js|css)|map|snap|svg|png|jpe?g|gif|webp|ico|pdf|woff2?)$|(^|/)(dist|build|vendor|third_party|node_modules|__generated__)/|(^|/)meta/(_journal|\d+_snapshot)\.json$|(^|/)zz_generated[^/]*\.go$|\.(pb|sql)\.go$|_pb2(_grpc)?\.py$|_pb\.(js|ts)$|\.(g|freezed)\.dart$|_gen\.go$|\.(gen|generated)\.[a-z]+$`)
	// generated matches a file diff whose first lines, a license comment first, start a comment saying it is:
	// "Code generated … DO NOT EDIT.", "@generated" and the like. A changed file's first lines are in its diff only
	// when a hunk starts there; generatedAt reads the others.
	generated = regexp.MustCompile(`(?mi)^@@ -\d+(?:,\d+)? \+1(?:,\d+)? @@.*\n(?:[^@\n].*\n){0,20}[+ ]\s*(?://+|#+|/?\*+|<!--|--|;+)\s*(?:(?:this (?:file|code) (?:is|was|has been) )?(?:(?:auto-?)?generated|code generated)\b.*\bdo not (?:edit|modify)\b|@generated\b)`)
	// Fill order for the budget: code, then tests, data and bulk code (stylesheets, notebooks, translations,
	// seeds), docs. #490: one 38k-char doc took most of it; on #34 a 48k-char stylesheet took a part while code was
	// left out.
	bulkFile  = regexp.MustCompile(`\.(css|scss|sass|less|styl|ipynb|po|pot)$|(^|/)seeds?/|(^|/)seed[^/]*\.sql$`)
	testFile  = regexp.MustCompile(`(^|/)(tests?|__tests__|e2e|testdata|fixtures|__fixtures__)/|[._-](test|spec)\.[a-z]+$|_test\.go$|(^|/)test_[^/]+\.py$`)
	dataFile  = regexp.MustCompile(`\.(json|ya?ml|toml|csv|xml)$`)
	docFile   = regexp.MustCompile(`(?i)\.(md|mdx|rst|txt|adoc)$|(^|/)docs?/`)
	specFile  = regexp.MustCompile(`(?i)(openapi|swagger)[^/]*\.(ya?ml|json)$`)
	fileStart = regexp.MustCompile(`(?m)^diff --git `)
	newPath   = regexp.MustCompile(`(?m)^\+\+\+ b/(.+)$`)
	deleted   = regexp.MustCompile(`(?m)^--- a/(.+)\n\+\+\+ /dev/null$`)
	hunkStart = regexp.MustCompile(`\+(\d+)`)
	hunkHead  = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*\n?)`)
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
	if bulkFile.MatchString(path) && Kind(path) == "code" {
		return 2
	}
	return map[string]int{"test": 1, "data": 2, "doc": 3}[Kind(path)]
}

// split cuts a file's diff past Max into pieces of up to Max, each under the file's header: at hunks, a hunk past
// Max at a line under a header of its own. A line past Max is a piece of its own, past Max: Filter leaves it out.
func split(f string) []string {
	at := strings.Index(f, "\n@@")
	if len(f) <= Max || at < 0 {
		return []string{f}
	}
	head := f[:at+1]
	var pieces []string
	var cur, hunk strings.Builder
	o, n, on, nn, title := 0, 0, 0, 0, "" // the open hunk's first old and new line, its lines of each, its title
	// end writes the open hunk into cur, counted anew: a cut one has fewer lines than its header said. A side with
	// no lines names the line before, like git's "-0,0".
	end := func() {
		if hunk.Len() > 0 {
			o0, n0 := o, n
			if on == 0 {
				o0--
			}
			if nn == 0 {
				n0--
			}
			fmt.Fprintf(&cur, "@@ -%d,%d +%d,%d @@%s%s", o0, on, n0, nn, title, hunk.String())
			hunk.Reset()
			o, n, on, nn = o+on, n+nn, 0, 0
		}
	}
	for _, l := range strings.SplitAfter(f[at+1:], "\n") {
		if m := hunkHead.FindStringSubmatch(l); m != nil {
			end()
			o, _ = strconv.Atoi(m[1])
			n, _ = strconv.Atoi(m[3])
			if m[2] == "0" { // "-5,0": nothing removed, after line 5; o and n are the next line of each side
				o++
			}
			if m[4] == "0" {
				n++
			}
			title = cmp.Or(m[5], "\n")
			continue
		}
		if l == "" { // after the last newline
			continue
		}
		// 40: "@@ -o,n +o,n @@", 40 more: a "\ No newline at end of file" after the line, never cut off from it
		if cur.Len()+hunk.Len() > 0 && !strings.HasPrefix(l, "\\") && len(head)+cur.Len()+hunk.Len()+len(l)+len(title)+80 > Max {
			end()
			pieces = append(pieces, head+cur.String())
			cur.Reset()
		}
		hunk.WriteString(l)
		switch {
		case strings.HasPrefix(l, " "):
			on, nn = on+1, nn+1
		case strings.HasPrefix(l, "-"):
			on++
		case strings.HasPrefix(l, "+"):
			nn++
		}
	}
	end()
	return append(pieces, head+cur.String())
}

// Filter drops lockfiles, generated/binary files (Skip, generated, generatedAt in root; "" reads no file) and what
// ignore matches; only (nil = all) limits to these paths. It packs files into at most n parts of up to Max each, a file past Max in pieces (split), code
// before tests, data, bulk code and docs, a file next to others of its directory; in a part they keep the diff's
// order. The last part names the omitted files, also returned (a file with a piece left out too), and with only nil
// the deleted files: their diffs are no use, what still relies on them is.
func Filter(diff, root string, only map[string]bool, ignore func(string) bool, n int) ([]string, []string) {
	type file struct {
		i          int
		path, diff string
	}
	var files []file
	EachFile(diff, func(p, f string) {
		if !Skip.MatchString(p) && !generated.MatchString(f) && (only == nil || only[p]) && (ignore == nil || !ignore(p)) && !generatedAt(root, p) {
			for _, s := range split(f) {
				files = append(files, file{len(files), p, s})
			}
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
	left = slices.Compact(left) // a file's pieces are next to each other

	if len(left) > 0 { // named: the agent can read them in the repo
		note("\n# %d more file(s) omitted: diff too large. Changed too, read them in the repo: %s\n", left)
	}
	var gone []string
	for _, m := range deleted.FindAllStringSubmatch(diff, -1) {
		if only == nil && !Skip.MatchString(m[1]) && (ignore == nil || !ignore(m[1])) {
			gone = append(gone, m[1])
		}
	}
	if len(gone) > 0 {
		note("\n# %d file(s) deleted: %s. Check that nothing still uses them.\n", gone)
	}
	return parts, left
}

// generatedAt tells whether the file p in root starts with a generated header, its diff aside: a change lower in
// the file has no hunk at its first lines.
func generatedAt(root, p string) bool {
	if root == "" {
		return false
	}
	fp := filepath.Join(root, p)
	if fi, err := os.Lstat(fp); err != nil || !fi.Mode().IsRegular() { // a link may name a FIFO or a file outside
		return false
	}
	f, err := os.Open(fp) //nolint:gosec // a path of the diff, in its checkout
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 4096)
	n, _ := io.ReadFull(f, b)
	return generated.MatchString("@@ -1 +1 @@\n " + strings.ReplaceAll(string(b[:n]), "\n", "\n "))
}

// Reverse puts diff's files in reverse order: a second reader starts where the first ended.
func Reverse(diff string) string {
	idx := fileStart.FindAllStringIndex(diff, -1)
	if len(idx) < 2 {
		return diff
	}
	var b strings.Builder
	b.WriteString(diff[:idx[0][0]])
	for k := len(idx) - 1; k >= 0; k-- {
		end := len(diff)
		if k+1 < len(idx) {
			end = idx[k+1][0]
		}
		b.WriteString(diff[idx[k][0]:end])
	}
	return b.String()
}

// Changed counts the lines diff adds and removes, headers aside.
func Changed(diff string) int {
	n, hunk := 0, false
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			hunk = false
		case strings.HasPrefix(l, "@@"):
			hunk = true
		case hunk && (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-")):
			n++
		}
	}
	return n
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

// Paths lists the new paths of the changed files, each once (a file in pieces too); deletions have none.
func Paths(diff string) []string {
	var p []string
	seen := map[string]bool{}
	for _, m := range newPath.FindAllStringSubmatch(diff, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			p = append(p, m[1])
		}
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
