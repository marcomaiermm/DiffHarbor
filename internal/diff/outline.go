package diff

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Outline is what a reader can take as a list instead of reading the diff: test cases, the test changes that
// weaken a test, edits that repeat across files or only move code.
type Outline struct {
	Added      []string `json:"added,omitempty"`   // test cases the PR adds
	Removed    []string `json:"removed,omitempty"` // test cases it deletes
	Weakened   []Flag   `json:"weakened,omitempty"`
	Snapshots  int      `json:"snapshots,omitempty"` // snapshot and golden files it updates
	Mechanical []Group  `json:"mechanical,omitempty"`
}

// Flag is a test change a person should look at: "path:line" or a path, and what happened there.
type Flag struct {
	At   string `json:"at"`
	What string `json:"what"`
}

// Group is files with one mechanical change: the same edit, whitespace only, moved, or generated.
type Group struct {
	What  string   `json:"what"`
	Files []string `json:"files"`
}

// sameEdit is the files that must share an edit for it to count as mechanical.
const sameEdit = 3

var (
	goTest   = regexp.MustCompile(`^func (Test\w+)\(`)
	subTest  = regexp.MustCompile(`\bt\.Run\(\s*"([^"]+)"`)
	jsTest   = regexp.MustCompile("\\b(?:it|test)(?:\\.(?:only|skip|todo|concurrent))?\\(\\s*['\"`]([^'\"`]+)")
	pyTest   = regexp.MustCompile(`^\s*(?:async\s+)?def (test_\w+)\(`)
	skipTest = regexp.MustCompile(`\.(skip|only|todo)\(|\bx(it|test|describe)\(|\bt\.Skip(Now|f)?\(|@pytest\.mark\.(skip|xfail)|@(Disabled|Ignore)\b`)
	assert   = regexp.MustCompile(`\b(expect|assert\w*|require\.\w+|should)\b|\bt\.(Error|Errorf|Fatal|Fatalf|Fail|FailNow)\(`)
	// A Go test checks with if and t.Fatal: a changed comparison is a changed expectation too.
	expects  = regexp.MustCompile(assert.String() + `|^if\b.*[!=]=`)
	literal  = regexp.MustCompile("\"(?:[^\"\\\\]|\\\\.)*\"|'(?:[^'\\\\]|\\\\.)*'|`[^`]*`|\\b\\d+(?:\\.\\d+)?\\b|\\b(?:true|false|nil|null|undefined|None|True|False)\\b")
	snapshot = regexp.MustCompile(`\.snap$|(^|/)__snapshots__/|(^|/)testdata/|\.golden$`)
	oldPath  = regexp.MustCompile(`(?m)^--- a/(.+)$`)
	moved    = regexp.MustCompile(`(?m)^similarity index 100%\nrename from (.+)\nrename to (.+)$`)
	space    = regexp.MustCompile(`\s+`)
)

// Kind sorts a path into code, test, data or doc.
func Kind(path string) string {
	switch {
	case docFile.MatchString(path):
		return "doc"
	case dataFile.MatchString(path):
		return "data"
	case testFile.MatchString(path):
		return "test"
	}
	return "code"
}

// Analyze outlines a whole PR diff. Deleted test files count too: their cases are gone.
func Analyze(diff string) Outline {
	var o Outline
	added, removed := map[string][]string{}, map[string][]string{} // test case name -> files
	type count struct {
		path       string
		gone, more int
	}
	var asserts []count
	gone, more := 0, 0
	edits := map[string][]string{} // fingerprint of a file's changed lines -> files
	desc := map[string]string{}
	var gen, blank []string
	idx := fileStart.FindAllStringIndex(diff, -1)
	for k, m := range idx {
		end := len(diff)
		if k+1 < len(idx) {
			end = idx[k+1][0]
		}
		f := diff[m[0]:end]
		p := ""
		if n := newPath.FindStringSubmatch(f); n != nil {
			p = n[1]
		} else if n := oldPath.FindStringSubmatch(f); n != nil {
			p = n[1]
		}
		if p == "" {
			continue // a pure move or mode change: moves are read below
		}
		if snapshot.MatchString(p) {
			o.Snapshots++
		}
		if Skip.MatchString(p) {
			gen = append(gen, p)
			continue
		}
		if Kind(p) == "test" {
			c := count{path: p}
			c.gone, c.more = testFlags(&o, p, f, added, removed)
			asserts, gone, more = append(asserts, c), gone+c.gone, more+c.more
		}
		var minus, plus []string
		for l := range strings.SplitSeq(f[strings.Index(f+"\n@@", "\n@@"):], "\n") {
			switch {
			case strings.HasPrefix(l, "+"):
				plus = append(plus, strings.TrimSpace(l[1:]))
			case strings.HasPrefix(l, "-"):
				minus = append(minus, strings.TrimSpace(l[1:]))
			}
		}
		if len(minus)+len(plus) == 0 {
			continue
		}
		strip := func(ls []string) string { return space.ReplaceAllString(strings.Join(ls, ""), "") }
		if len(minus) > 0 && strip(minus) == strip(plus) {
			blank = append(blank, p)
			continue
		}
		h := sha256.Sum256([]byte(strings.Join(minus, "\n") + "\x00" + strings.Join(plus, "\n")))
		key := string(h[:])
		edits[key] = append(edits[key], p)
		if _, ok := desc[key]; !ok {
			desc[key] = edit(minus, plus)
		}
	}
	// A case on both sides changed or moved to another file (a package split): neither new nor gone.
	for n := range added {
		if removed[n] != nil {
			delete(added, n)
			delete(removed, n)
		}
	}
	o.Added, o.Removed = cases(added), cases(removed)
	for _, n := range o.Removed {
		o.Weakened = append(o.Weakened, Flag{At: n, What: "test deleted"})
	}
	if gone > more { // across the PR: assertions that only moved files cancel out
		for _, c := range asserts {
			if c.gone > c.more {
				o.Weakened = append(o.Weakened, Flag{At: c.path, What: fmt.Sprintf("%d assertions removed, %d added", c.gone, c.more)})
			}
		}
	}
	var ms []Group
	for key, fs := range edits {
		if len(fs) >= sameEdit {
			ms = append(ms, Group{What: fmt.Sprintf("Same edit in %d files: %s", len(fs), desc[key]), Files: fs})
		}
	}
	slices.SortFunc(ms, func(a, b Group) int { return len(b.Files) - len(a.Files) })
	var mv []string
	for _, m := range moved.FindAllStringSubmatch(diff, -1) {
		mv = append(mv, m[1]+" → "+m[2])
	}
	for _, g := range []Group{{"Moved without changes", mv}, {"Whitespace only", blank}, {"Lockfiles and generated", gen}} {
		if len(g.Files) > 0 {
			ms = append(ms, g)
		}
	}
	o.Mechanical = ms
	return o
}

// Skippable is the files of the mechanical groups, by new path.
func (o Outline) Skippable() map[string]bool {
	s := map[string]bool{}
	for _, g := range o.Mechanical {
		for _, f := range g.Files {
			_, to, ok := strings.Cut(f, " → ")
			if !ok {
				to = f
			}
			s[to] = true
		}
	}
	return s
}

// cases lists "path: name" for each test case by name.
func cases(m map[string][]string) []string {
	var s []string
	for n, ps := range m {
		for _, p := range slices.Compact(slices.Sorted(slices.Values(ps))) {
			s = append(s, p+": "+n)
		}
	}
	slices.Sort(s)
	return s
}

// testFlags collects a test file's cases and what weakens it (skips, changed expectations) and counts the
// assertions it removes and adds.
func testFlags(o *Outline, p, f string, added, removed map[string][]string) (gone, more int) {
	name := func(l string) string {
		for _, re := range []*regexp.Regexp{goTest, subTest, jsTest, pyTest} {
			if m := re.FindStringSubmatch(l); m != nil {
				return m[1]
			}
		}
		return ""
	}
	line := 0
	var minus []string
	var plus []struct {
		at   int
		text string
	}
	pair := func() { // a run of - lines replaced by a run of + lines: same assertion, other literal
		for i := range min(len(minus), len(plus)) {
			a, b := minus[i], plus[i].text
			if a != b && expects.MatchString(a) && expects.MatchString(b) &&
				literal.ReplaceAllString(space.ReplaceAllString(a, ""), "_") == literal.ReplaceAllString(space.ReplaceAllString(b, ""), "_") {
				o.Weakened = append(o.Weakened, Flag{
					At:   fmt.Sprintf("%s:%d", p, plus[i].at),
					What: "expectation changed: `" + oneLine(a) + "` → `" + oneLine(b) + "`",
				})
			}
		}
		minus, plus = nil, nil
	}
	hunk := false
	for l := range strings.SplitSeq(f, "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
			pair()
			hunk = true
			if m := hunkStart.FindStringSubmatch(l); m != nil {
				line, _ = strconv.Atoi(m[1])
			}
		case !hunk:
		case strings.HasPrefix(l, "+"):
			t := strings.TrimSpace(l[1:])
			if n := name(t); n != "" {
				added[n] = append(added[n], p)
			}
			if skipTest.MatchString(t) {
				o.Weakened = append(o.Weakened, Flag{At: fmt.Sprintf("%s:%d", p, line), What: "skip/only: `" + oneLine(t) + "`"})
			}
			if assert.MatchString(t) {
				more++
			}
			plus = append(plus, struct {
				at   int
				text string
			}{line, t})
			line++
		case strings.HasPrefix(l, "-"):
			if len(plus) > 0 {
				pair()
			}
			t := strings.TrimSpace(l[1:])
			if n := name(t); n != "" {
				removed[n] = append(removed[n], p)
			}
			if assert.MatchString(t) {
				gone++
			}
			minus = append(minus, t)
		default:
			pair()
			line++
		}
	}
	pair()
	return gone, more
}

// edit describes a repeated edit by its first changed lines.
func edit(minus, plus []string) string {
	var s []string
	switch {
	case len(minus) > 0 && len(plus) > 0:
		s = append(s, "`"+oneLine(minus[0])+"` → `"+oneLine(plus[0])+"`")
	case len(minus) > 0:
		s = append(s, "`-"+oneLine(minus[0])+"`")
	case len(plus) > 0:
		s = append(s, "`+"+oneLine(plus[0])+"`")
	}
	if n := len(minus) + len(plus); n > 2 {
		s = append(s, fmt.Sprintf("(%d lines)", n))
	}
	return strings.Join(s, " ")
}

func oneLine(s string) string {
	s = strings.ReplaceAll(strings.Join(strings.Fields(s), " "), "`", "'")
	if r := []rune(s); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}
