package report

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A run is lines the page folds: code the PR only moved, or a hunk that only touches imports, comments or
// whitespace. The changes inside moved code (a renamed variable) stay outside the run, so they show.
type run struct {
	What  string // "moved from", "imports only" …
	Where string // moved code: the other side, "path:line"
	Link  int    // moved code: the run on the other side
	Note  string // a hunk the review calls routine: why
}

type part struct {
	run        // zero: plain lines
	ID    int  // the run, 0 for plain lines
	Open  bool // folded, but a finding sits in it
	Lines []line
}

// minMove is the lines of substance (minChars and more) a block needs to count as moved: three closing
// braces are no move.
const minMove, minChars = 3, 4

var (
	// A Go import is a bare quoted path, optionally aliased: Go's trailing commas leave no other line like it.
	importLine  = regexp.MustCompile(`^(import\b|from\s+\S+\s+import\b|export\s.*\sfrom\s|require\(|use\s+\w+::|(?:[\w.]+\s+)?"[\w.\-/@]+"$|\)$)`)
	commentLine = regexp.MustCompile(`^(//|/\*|\*|#|--|<!--)`)
)

type at struct { // a line of a file
	f *file
	i int
}

// fold marks the runs in the PR's files and returns them by id: moved code, routine hunks (by their lines, and
// the ones the review calls routine: path -> new line -> why), and the bodies of long added or removed code.
func fold(order []string, files map[string]*file, skim map[string]map[int]string) map[int]run {
	removed := map[string][]at{} // trimmed text -> where the PR deletes it
	for _, p := range order {
		f := files[p]
		for i, l := range f.Lines {
			if k := strings.TrimSpace(l.Text); l.Class == "del" && len(k) >= minChars {
				removed[k] = append(removed[k], at{f, i})
			}
		}
	}
	runs := map[int]run{}
	id := 0
	for _, p := range order {
		f := files[p]
		for i := 0; i < len(f.Lines); i++ {
			l := f.Lines[i]
			if l.Class != "add" || l.Run != 0 {
				continue
			}
			best, from := 0, at{}
			for _, r := range removed[strings.TrimSpace(l.Text)] { // the longest block
				if n := block(f.Lines[i:], r.f.Lines[r.i:]); n > best {
					best, from = n, r
				}
			}
			if best == 0 {
				continue
			}
			to, back := id+1, id+2
			id += 2
			runs[to] = run{What: "moved from", Where: fmt.Sprintf("%s:%d", from.f.Path, from.f.Lines[from.i].Old), Link: back}
			runs[back] = run{What: "moved to", Where: f.Path + ":" + l.No, Link: to}
			for k := range best {
				f.Lines[i+k].Run, from.f.Lines[from.i+k].Run = to, back
			}
			f.Moved += best
			i += best - 1
		}
	}
	for _, p := range order {
		f := files[p]
		if f.Kind == "doc" { // a # is a heading there
			continue
		}
		id = skims(f.Lines, skim[p], runs, id)
		if f.Kind == "code" || f.Kind == "test" {
			id = outline(f.Lines, runs, id)
		}
	}
	return runs
}

// skims folds the hunks with nothing to read: routine ones, and the ones the review calls routine. It returns the
// last run id.
func skims(ls []line, skim map[int]string, runs map[int]run, id int) int {
	for start := 0; start < len(ls); {
		end := start + 1
		for end < len(ls) && ls[end].Class != "hunk" {
			end++
		}
		what, note := routine(ls[start:end]), ""
		if what == "" {
			what, note = said(ls[start:end], skim)
		}
		if what != "" {
			id++
			runs[id] = run{What: what, Note: note}
			for k := start; k < end; k++ {
				if ls[k].Class != "hunk" {
					ls[k].Run = id
				}
			}
		}
		start = end
	}
	return id
}

// said is "routine" and the review's why when it calls a line of the hunk routine and no line of it is folded.
func said(ls []line, skim map[int]string) (string, string) {
	why := ""
	for _, l := range ls {
		if l.Run != 0 {
			return "", ""
		}
		if n, _ := strconv.Atoi(l.No); why == "" && l.Class != "del" {
			why = skim[n]
		}
	}
	if why == "" {
		return "", ""
	}
	return "routine", why
}

// minOutline is the added or removed lines in a row that read as an outline: each declaration's line and its
// closing one, the body folded. A class's body is an outline of its own.
const minOutline, minBody = 30, 6

var (
	// a type's body is its definition: it stays
	typeDecl  = regexp.MustCompile(`^(?:export\s+)?(?:pub\s+)?(type|interface|struct|enum|trait)\b`)
	container = regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:pub\s+)?(?:abstract\s+)?(class|impl|trait|object|namespace|module)\b`)
	closing   = regexp.MustCompile(`^[})\]]|^end\b`)
)

// outline folds the bodies in each long run of added or removed lines and returns the last run id.
func outline(ls []line, runs map[int]run, id int) int {
	for start := 0; start < len(ls); {
		end := start + 1
		for end < len(ls) && ls[end].Class == ls[start].Class {
			end++
		}
		if (ls[start].Class == "add" || ls[start].Class == "del") && end-start >= minOutline {
			id = bodies(ls[start:end], runs, id)
		}
		start = end
	}
	return id
}

// bodies folds what follows each declaration at the run's outer indentation, up to its closing line.
func bodies(ls []line, runs map[int]run, id int) int {
	base := -1
	for _, l := range ls {
		if strings.TrimSpace(l.Text) != "" && (base < 0 || indent(l.Text) < base) {
			base = indent(l.Text)
		}
	}
	for i := 0; i < len(ls); i++ {
		t := strings.TrimSpace(ls[i].Text)
		if t == "" || indent(ls[i].Text) != base || commentLine.MatchString(t) || closing.MatchString(t) {
			continue
		}
		end := i + 1
		for end < len(ls) && (strings.TrimSpace(ls[end].Text) == "" || indent(ls[end].Text) > base) {
			end++
		}
		for end > i+1 && strings.TrimSpace(ls[end-1].Text) == "" { // blank lines after the body stay out
			end--
		}
		switch {
		case end-i-1 < minBody, typeDecl.MatchString(t):
		case container.MatchString(t):
			id = bodies(ls[i+1:end], runs, id)
		case slices.ContainsFunc(ls[i+1:end], func(l line) bool { return l.Run != 0 }):
			// moved here and edited: the lines that didn't move are the change
		default:
			name := strings.Fields(t)[0] // import (, var (, a call: its first word
			if m := decl.FindStringSubmatch(t); m != nil {
				name = m[2]
			}
			id++
			runs[id] = run{What: "in " + name}
			for k := i + 1; k < end; k++ {
				ls[k].Run = id
			}
		}
		i = end - 1
	}
	return id
}

// indent is a line's leading whitespace, a tab as 4.
func indent(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// block is how many added lines from the start of add repeat the removed ones from the start of del,
// indentation aside: 0 when fewer than minMove of them have substance.
func block(add, del []line) int {
	n, solid := 0, 0
	for n < len(add) && n < len(del) {
		a, b := add[n], del[n]
		t := strings.TrimSpace(a.Text)
		if a.Class != "add" || b.Class != "del" || a.Run != 0 || b.Run != 0 || t != strings.TrimSpace(b.Text) {
			break
		}
		if len(t) >= minChars {
			solid++
		}
		n++
	}
	if solid < minMove {
		return 0
	}
	return n
}

// routine says what a hunk only touches: imports, comments or whitespace; "" when it changes code.
func routine(ls []line) string {
	var minus, plus []string
	imports, comments, code := false, false, false
	for _, l := range ls {
		if l.Run != 0 {
			return ""
		}
		if l.Class != "add" && l.Class != "del" {
			continue
		}
		t := strings.TrimSpace(l.Text)
		if l.Class == "add" {
			plus = append(plus, t)
		} else {
			minus = append(minus, t)
		}
		switch {
		case t == "":
		case commentLine.MatchString(t):
			comments = true
		case importLine.MatchString(t) && !strings.HasPrefix(t, "return "):
			imports = true
		default:
			code = true
		}
	}
	squash := func(ss []string) string { return strings.Join(strings.Fields(strings.Join(ss, "")), "") }
	switch {
	case len(minus)+len(plus) == 0:
		return ""
	case squash(minus) == squash(plus):
		return "whitespace only"
	case code:
		return ""
	case imports && comments:
		return "imports and comments only"
	case imports:
		return "imports only"
	case comments:
		return "comments only"
	}
	return "" // blank lines: as cheap to read as the fold
}

// parts cuts a file's lines into plain and folded runs. A run with a finding in it stays open.
func parts(ls []line, runs map[int]run) []part {
	var ps []part
	for _, l := range ls {
		if len(ps) == 0 || ps[len(ps)-1].ID != l.Run {
			ps = append(ps, part{run: runs[l.Run], ID: l.Run})
		}
		p := &ps[len(ps)-1]
		p.Lines = append(p.Lines, l)
		p.Open = p.Open || p.ID != 0 && len(l.Findings) > 0
	}
	return ps
}
