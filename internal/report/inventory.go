package report

import (
	"cmp"
	"diffharbor/internal/review"
	"diffharbor/internal/text"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// maxItems is the rows a group of the inventory lists, maxShown the lines a moved and changed one shows of what
// differs, maxAsk the lines of a declaration the model reads.
const maxItems, maxShown, maxAsk = 60, 3, 12

// span is a declaration of a code file the diff adds or removes lines of, moved ones too: what the inventory sorts.
type span struct {
	f             *file
	name          string
	head, oldHead string   // its signature, added and removed: see signature
	text, oldText string   // its declaring line, added and removed, trimmed: as a commit has it
	add, del      []string // its other added and removed lines of code, trimmed
	id, oldID     string   // its first added line, its first removed one
	line, oldLine int      // the new file's line of id, the old file's of oldID
	moved, n      int      // of its lines, how many moved and all
	fds           []*finding
}

// noCode is a line that changes no behaviour: blank, a closing bracket, a comment. It also keeps a new
// declaration's doc comment and the brace git aligns before it from counting as a change of the one above.
var noCode = regexp.MustCompile(`^([\])};,]*|(//|/\*|\*|#|--).*)$`)

// sigs is the signatures the declaring line f.Lines[i] starts, from its hunk's lines: before (context and removed
// lines) unless the line is added, now (context and added ones) unless it's removed; changed, whether either takes
// an added or removed line.
func sigs(f *file, i int) (before, now string, changed bool) {
	side := func(skip string) string {
		var ls []string
		var classes []string
		for j := i; j < len(f.Lines) && f.Lines[j].Class != "hunk" && len(ls) < maxSigLines; j++ {
			if c := f.Lines[j].Class; c != skip {
				ls, classes = append(ls, f.Lines[j].Text), append(classes, c)
			}
		}
		s, n := signature(ls)
		changed = changed || slices.ContainsFunc(classes[:n], func(c string) bool { return c != "ctx" })
		return s
	}
	if c := f.Lines[i].Class; c != "add" {
		before = side("add")
	}
	if c := f.Lines[i].Class; c != "del" {
		now = side("del")
	}
	return before, now, changed
}

// spans is a code file's changed declarations, by the same lines as changes, moved code included.
func spans(f *file, runs map[int]run) []*span {
	if f.Kind != "code" {
		return nil
	}
	var ss []*span
	at := map[string]*span{}
	cur := ""
	for i := range f.Lines {
		l := &f.Lines[i]
		if l.Class == "hunk" {
			cur = ""
			if m := hunk.FindStringSubmatch(l.Text); m != nil {
				if d := decl.FindStringSubmatch(m[3]); d != nil {
					cur = d[2]
				}
			}
			continue
		}
		head := false
		if d := decl.FindStringSubmatch(l.Text); d != nil {
			cur, head = d[2], true
		}
		t := strings.TrimSpace(l.Text)
		get := func() *span {
			s := at[cur]
			if s == nil {
				s = &span{f: f, name: cur}
				at[cur], ss = s, append(ss, s)
			}
			return s
		}
		if head && l.Class == "ctx" { // an unchanged declaring line, its parameters below it changed
			if before, now, changed := sigs(f, i); changed && !same(before, now) {
				if s := get(); s.head == "" && s.oldHead == "" {
					s.head, s.oldHead, s.text, s.oldText = now, before, t, t
				}
			}
			continue
		}
		if cur == "" || l.Class == "ctx" || !head && noCode.MatchString(t) {
			continue
		}
		s := get()
		if l.ID == "" {
			l.ID = fmt.Sprintf("%s~%d", f.Path, i)
		}
		add := l.Class == "add"
		switch {
		case add && s.id == "":
			s.id = l.ID
			s.line, _ = strconv.Atoi(l.No)
		case !add && s.oldID == "":
			s.oldID, s.oldLine = l.ID, l.Old
		}
		switch {
		case head && add && s.head == "":
			_, s.head, _ = sigs(f, i)
			s.text = t
		case head && !add && s.oldHead == "":
			s.oldHead, _, _ = sigs(f, i)
			s.oldText = t
		case add:
			s.add = append(s.add, t)
		default:
			s.del = append(s.del, t)
		}
		s.n++
		s.fds = append(s.fds, l.Findings...)
		if runs[l.Run].Where != "" {
			s.moved++
		}
	}
	return ss
}

// item is a row of the inventory.
type item struct {
	Name, Was     string // Was: its name before, renamed
	Path, From    string // From: the file it moved from
	File          string // Path in full: the symbol index's
	ID, OldID     string // where the diff shows it, moved: where it was
	OldLine       int    // moved: its line in the old file
	Doc           string // its doc comment's first sentence
	Why           string // the subject of the commit that brought it, else the model's line
	Commit        bool   // Why is a commit's
	SHA           string // that commit's
	Sig           string // its declaration without the body, on one line: now, removed before
	Line          int    // Sig's, in its file then
	Outside       int    // the places outside the diff's files that name it
	Sev           *weighed
	Head, OldHead string // signature changed, renamed: its declaring line now and before
	Plus, Minus   []string
	NPlus, NMinus int
	key           string   // path NUL name: Data.Whys
	ask           []string // for the model: its file, declaring line and first lines
}

// bucket is a kind of change in the inventory.
type bucket struct {
	Key, Title, Hint string
	Items            []item
	More             int // past maxItems
}

// inventory is what the PR did to its code's declarations, by kind: new, signature changed, behaviour changed,
// moved and changed, moved or renamed, removed. A new declaration and a removed one are the same moved when their
// lines match: in another file, renamed, its case changed (matrix → agent.Matrix).
func inventory(ss []*span, syms map[string][]Symbol, commits []review.Commit, title string, whys map[string]string) []bucket {
	gs := []bucket{
		{Key: "new", Title: "New", Hint: "Declarations the PR adds."},
		{Key: "signature", Title: "Signature changed", Hint: "Every caller sees the difference."},
		{Key: "body", Title: "Behaviour changed", Hint: "Same declaration, other code in it."},
		{Key: "reworked", Title: "Moved and changed", Hint: "Moved; only what differs from the old place is listed, the diff folds the rest."},
		{Key: "moved", Title: "Moved or renamed", Hint: "The same code in another place: nothing to read."},
		{Key: "removed", Title: "Removed"},
	}
	put := func(g int, it item) {
		if len(gs[g].Items) < maxItems {
			gs[g].Items = append(gs[g].Items, it)
		} else {
			gs[g].More++
		}
	}
	brought := map[string]review.Commit{} // "+line" -> the last commit that adds it
	if len(commits) > 1 {                 // one commit says what the title says
		for _, c := range commits {
			if s := strings.TrimSpace(c.Subject); !wip.MatchString(s) && s != title {
				for _, l := range c.Lines {
					brought[l] = review.Commit{SHA: c.SHA, Subject: s}
				}
			}
		}
	}
	row := func(s *span, line string) item {
		it := item{Name: s.name, Path: s.f.Short, File: s.f.Path, ID: cmp.Or(s.id, s.oldID), key: s.f.Path + "\x00" + s.name}
		it.Sig, it.Line, it.Sev = cmp.Or(s.head, s.oldHead), s.line, weigh(s.fds)
		if s.head == "" && s.id == "" { // removed
			it.Line = s.oldLine
		}
		for _, sym := range syms[s.name] {
			if sym.Path != s.f.Path || s.head == "" && s.id == "" {
				continue
			}
			if sym.Doc != "" {
				it.Doc = sentence(sym.Doc)
			}
			if sym.Exact {
				it.Outside = sym.NOutside
			}
			first, _, _ := strings.Cut(sym.Sig, "\n") // a type's: its fields are the diff's to show
			it.Sig, it.Line = cmp.Or(it.Sig, text.OneLine(first, 200)), sym.Line
		}
		it.Sig = cmp.Or(strings.TrimRight(it.Sig, " \t{"), s.name) // the body's brace: not what callers see
		if len(line) > 1 {                                         // "+" alone: no line to tell by
			c := brought[line]
			it.Why, it.SHA = c.Subject, c.SHA
		}
		it.Commit = it.Why != ""
		return it
	}
	var news, gone []*span
	for _, s := range ss {
		switch {
		case s.head != "" && s.oldHead != "" && !same(s.head, s.oldHead):
			it := row(s, "+"+s.text)
			it.Head, it.OldHead = s.head, s.oldHead
			it.ask = ask(s)
			put(1, it)
		case s.head != "" && s.oldHead != "" && s.moved == s.n: // moved within its file
			it := row(s, "+"+s.text)
			it.OldID, it.OldLine = s.oldID, s.oldLine
			put(4, it)
		case s.head != "" && s.oldHead == "":
			news = append(news, s)
		case s.oldHead != "" && s.head == "":
			gone = append(gone, s)
		default:
			it := row(s, "+"+strings.Join(s.add[:min(len(s.add), 1)], ""))
			it.NPlus, it.NMinus = len(s.add), len(s.del)
			if s.head != "" { // its declaring line changed, not what callers see of it: a value, a reflow
				it.NPlus, it.NMinus = it.NPlus+1, it.NMinus+1
			}
			put(2, it)
		}
	}
	paired := pair(news, gone)
	for _, s := range news {
		g := paired[s]
		if g == nil {
			it := row(s, "+"+s.text)
			it.NPlus = len(s.add) + 1
			it.ask = ask(s)
			put(0, it)
			continue
		}
		it := row(s, "+"+s.text)
		it.OldID, it.OldLine = g.oldID, g.oldLine
		if !same(s.head, g.oldHead) { // renamed or its signature changed: the card shows the one turn into the other
			it.Head, it.OldHead = s.head, g.oldHead
		}
		if g.name != s.name {
			it.Was = g.name
		}
		if g.f != s.f {
			it.From = g.f.Short
		}
		plus, minus := differ(s.add, g.del)
		if k := strings.ToLower; !same(k(strings.Replace(s.head, s.name, "", 1)), k(strings.Replace(g.oldHead, g.name, "", 1))) {
			plus, minus = append([]string{s.head}, plus...), append([]string{g.oldHead}, minus...)
		}
		if len(plus)+len(minus) == 0 {
			put(4, it)
			continue
		}
		it.NPlus, it.NMinus = len(plus), len(minus)
		it.Plus, it.Minus = plus[:min(len(plus), maxShown)], minus[:min(len(minus), maxShown)]
		put(3, it)
	}
	matched := map[*span]bool{}
	for _, g := range paired {
		matched[g] = true
	}
	for _, s := range gone {
		if !matched[s] {
			it := row(s, "-"+s.oldText)
			it.NMinus = len(s.del) + 1
			put(5, it)
		}
	}
	whyOf(gs, whys)
	return slices.DeleteFunc(gs, func(g bucket) bool { return len(g.Items) == 0 })
}

// whyOf settles each row's why: the commit's subject, unless that commit brought half the rows or more (then it's
// the PR's whole story and on every row says nothing), else the model's where no doc comment says what it's for.
func whyOf(gs []bucket, whys map[string]string) {
	n, by := 0, map[string]int{}
	for _, g := range gs {
		for _, it := range g.Items {
			n++
			by[it.Why]++
		}
	}
	for _, g := range gs {
		for i := range g.Items {
			it := &g.Items[i]
			if it.Commit && by[it.Why]*2 >= n {
				it.Why, it.Commit, it.SHA = "", false, ""
			}
			if it.Why == "" && it.Doc == "" {
				it.Why = whys[it.key]
			}
		}
	}
}

// pair matches new declarations to removed ones by their lines of substance, case aside: half of them alike (as a
// share of both), or a quarter with the same name, case aside; the most alike first. One-line declarations: by name.
func pair(news, gone []*span) map[*span]*span {
	key := strings.ToLower
	lines := func(ls []string) map[string]bool {
		u := map[string]bool{}
		for _, l := range ls {
			if len(l) >= minChars {
				u[key(l)] = true
			}
		}
		return u
	}
	byLine, byName := map[string][]int{}, map[string][]int{}
	sizes := make([]int, len(gone))
	for gi, g := range gone {
		u := lines(g.del)
		sizes[gi] = len(u)
		for l := range u {
			byLine[l] = append(byLine[l], gi)
		}
		byName[key(g.name)] = append(byName[key(g.name)], gi)
	}
	type cand struct {
		n, g  int
		score float64
		same  bool
	}
	var cs []cand
	for ni, n := range news {
		u := lines(n.add)
		common := map[int]int{}
		for l := range u { // ponytail: a line in many declarations ("return nil") costs news × gone; fine up to thousands
			for _, gi := range byLine[l] {
				common[gi]++
			}
		}
		for _, gi := range byName[key(n.name)] {
			common[gi] += 0 // the same name: a candidate with no line alike too
		}
		for gi, c := range common {
			same := key(n.name) == key(gone[gi].name)
			score := 0.0
			if both := len(u) + sizes[gi]; both > 0 {
				score = 2 * float64(c) / float64(both)
			} else if same {
				score = 1
			}
			if score >= 0.5 || same && score >= 0.25 {
				cs = append(cs, cand{ni, gi, score, same})
			}
		}
	}
	same := func(c cand) int { return map[bool]int{true: 1}[c.same] }
	slices.SortFunc(cs, func(a, b cand) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(same(b), same(a)), cmp.Compare(a.n, b.n), cmp.Compare(a.g, b.g))
	})
	out, taken := map[*span]*span{}, map[int]bool{}
	for _, c := range cs {
		if out[news[c.n]] == nil && !taken[c.g] {
			out[news[c.n]], taken[c.g] = gone[c.g], true
		}
	}
	return out
}

// differ is the lines of now that before lacks and those of before that now lacks, case aside, each line once
// per time it's there.
func differ(now, before []string) (plus, minus []string) {
	left := map[string]int{}
	for _, l := range before {
		left[strings.ToLower(l)]++
	}
	for _, l := range now {
		if k := strings.ToLower(l); left[k] > 0 {
			left[k]--
		} else {
			plus = append(plus, l)
		}
	}
	for _, l := range before {
		if k := strings.ToLower(l); left[k] > 0 {
			left[k]--
			minus = append(minus, l)
		}
	}
	return plus, minus
}

// ask is what the model reads of a declaration: its file, declaring line, first lines.
func ask(s *span) []string {
	return append([]string{s.f.Path, s.head}, s.add[:min(len(s.add), maxAsk)]...)
}

// sentence is a doc comment's first sentence, on one line.
func sentence(doc string) string {
	doc = strings.Join(strings.Fields(doc), " ")
	if i := strings.Index(doc, ". "); i >= 0 {
		doc = doc[:i+1]
	}
	return cut(doc, 200)
}

// apkg is a directory's rows of the inventory: its API before and after.
type apkg struct {
	Pkg   string
	Items []arow
}

// arow is a row with its mark: "+" new, "~" changed, "→" moved, "−" removed, and what to know of it at a glance.
type arow struct {
	item
	Mark, Word string
	Info       []string // "behaviour", "from a.go:3, unchanged"
}

// API is the inventory by directory, each in the order new, changed, moved, removed.
func (v view) API() []apkg {
	marks := map[string][2]string{"new": {"+", "new"}, "signature": {"~", "changed"}, "body": {"~", "changed"},
		"reworked": {"→", "moved"}, "moved": {"→", "moved"}, "removed": {"−", "removed"}}
	var ps []apkg
	at := map[string]int{}
	for _, g := range v.Inventory {
		for _, it := range g.Items {
			r := arow{item: it, Mark: marks[g.Key][0], Word: marks[g.Key][1]}
			switch g.Key {
			case "signature":
				r.Info = []string{"signature"}
			case "body":
				r.Info = []string{"behaviour"}
			case "reworked", "moved":
				from := "from line " + strconv.Itoa(it.OldLine)
				if it.From != "" {
					from = "from " + path.Base(it.From) + ":" + strconv.Itoa(it.OldLine)
				}
				if it.Was != "" {
					from += " as " + it.Was
				}
				r.Info = []string{from + map[bool]string{true: ", changed", false: ", unchanged"}[g.Key == "reworked"]}
			}
			p := path.Dir(it.File)
			k, ok := at[p]
			if !ok {
				k, at[p] = len(ps), len(ps)
				ps = append(ps, apkg{Pkg: p})
			}
			ps[k].Items = append(ps[k].Items, r)
		}
	}
	slices.SortStableFunc(ps, func(a, b apkg) int { return strings.Compare(a.Pkg, b.Pkg) })
	return ps
}

// APIMore is the rows past maxItems the inventory leaves out.
func (v view) APIMore() (n int) {
	for _, g := range v.Inventory {
		n += g.More
	}
	return n
}
