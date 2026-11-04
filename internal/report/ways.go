package report

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
)

// maxWays is the ways in the page shows, maxWayRows the declarations one shows below its entry.
const maxWays, maxWayRows = 6, 12

// way is a way into the change: a route or a declaration nothing calls, and the calls down to the PR's
// declarations it runs, as a tree.
type way struct {
	Method, Route string // "GET", "/x/{id}"; "CALL", a declaration's name
	Sub           string // "outside the PR": the entry's file is not in the diff
	At            ref    // where the route is registered, the declaration declared
	Rows          []wrow
	Reach, New    int    // the PR's declarations it runs, how many of them new
	More          int    // of those, the ones past maxWayRows
	Sev           string // the heaviest finding on them: "critical", "major", "" lighter or none
	Finding       string // where that finding is
	Step          int    // the step whose card shows it, from 1; 0 none
	reach         []string
}

// wrow is a declaration on a way: D its depth below the entry, Cols the tree's guides left of it (pipe, blank, tee,
// end), Kids whether the row below is deeper. Tag is what the PR did to it, St how the row shows it: new, chg,
// major; "" for one the PR doesn't change.
type wrow struct {
	Name, Path string
	D          int
	Cols       []string
	Kids       bool
	Tag, St    string
	Sym        bool // the page knows its declaration: it opens its card
	Sev        *weighed
	At         ref // where the one above names it, else where it is declared
}

// ways is how the code reaches the PR's changed declarations, by the index's callers: routes first, then those that
// reach a heavier finding, more of the PR. A step's card shows the first way to a major finding in its files.
func ways(v *view, syms map[string][]Symbol) []way {
	all, byDecl := decls(v.Steps)
	type node struct {
		hop  Hop
		kids []*node
	}
	type entry struct {
		w      way
		root   node
		chains [][]Hop
		ends   map[string]bool
	}
	var es []*entry
	at := map[string]*entry{}
	weight := func(c changed) int {
		return map[string]int{"critical": 4, "major": 3, "minor": 1}[c.c.Sev] + map[bool]int{true: 1}[c.c.sig || c.c.State == "new"]
	}
	pr := func(h Hop) *changed {
		if ks := byDecl[h.Path+"\x00"+h.Name]; len(ks) > 0 {
			return &all[ks[0]]
		}
		return nil
	}
	for _, d := range all {
		for _, s := range syms[d.c.Name] {
			if s.Path != d.f.Path || !s.Exact {
				continue
			}
			for _, w := range s.Ways {
				k := w.Method + " " + w.Route + "\x00" + w.Path + "\x00" + strconv.Itoa(w.Line)
				e := at[k]
				if e == nil {
					e = &entry{w: way{Method: w.Method, Route: w.Route, At: v.place(w.Path, w.Line)}, ends: map[string]bool{}}
					if v.files[w.Path] == nil {
						e.w.Sub = "outside the PR"
					}
					at[k] = e
					es = append(es, e)
				}
				if end := d.f.Path + "\x00" + d.c.Name; !e.ends[end] {
					e.ends[end] = true
					e.chains = append(e.chains, w.Chain)
				}
			}
		}
	}
	var out []way
	for _, e := range es {
		slices.SortStableFunc(e.chains, func(a, b []Hop) int {
			return cmp.Or(cmp.Compare(weight(*pr(b[len(b)-1])), weight(*pr(a[len(a)-1]))), cmp.Compare(len(a), len(b)))
		})
		shown := 0
		for _, c := range e.chains {
			end := pr(c[len(c)-1])
			e.w.Reach++
			if end.c.State == "new" {
				e.w.New++
			}
			if heavier(end.c.Sev, e.w.Sev) && end.c.Sev != "minor" {
				e.w.Sev, e.w.Finding = end.c.Sev, findingOf(end.f, end.c.Sev)
			}
			cur, add := &e.root, 0
			for _, h := range c {
				if !slices.ContainsFunc(cur.kids, func(n *node) bool { return n.hop.Path == h.Path && n.hop.Name == h.Name }) {
					add++
				}
			}
			if shown+add > maxWayRows {
				e.w.More++
				continue
			}
			shown += add
			for _, h := range c {
				i := slices.IndexFunc(cur.kids, func(n *node) bool { return n.hop.Path == h.Path && n.hop.Name == h.Name })
				if i < 0 {
					i, cur.kids = len(cur.kids), append(cur.kids, &node{hop: h})
				}
				cur = cur.kids[i]
			}
		}
		var walk func(n *node, d int)
		walk = func(n *node, d int) {
			for _, k := range n.kids {
				h, r := k.hop, wrow{Name: k.hop.Name, Path: k.hop.Path, D: d}
				r.At = v.place(h.Path, h.Line)
				if at := v.place(h.In, h.At); h.At > 0 && h.In != "" && at != e.w.At { // the entry shows that one
					r.At = at
				}
				r.Sym = len(syms[h.Name]) > 0
				if c := pr(h); c != nil {
					r.Tag, r.St = "changed", "chg"
					switch {
					case c.c.State == "new":
						r.Tag, r.St = "new", "new"
					case c.c.sig:
						r.Tag = "signature"
					}
					if c.c.Sev == "critical" || c.c.Sev == "major" {
						r.St, r.Sev = "major", weigh(findingsOf(c.f, c.c.Sev))
					}
				}
				e.w.Rows = append(e.w.Rows, r)
				walk(k, d+1)
			}
		}
		walk(&e.root, 1)
		guides(e.w.Rows)
		out = append(out, e.w)
	}
	slices.SortStableFunc(out, func(a, b way) int {
		route := func(w way) int { return map[bool]int{true: 1}[w.Method != "CALL"] }
		sev := func(w way) int { return map[string]int{"critical": 2, "major": 1}[w.Sev] }
		return cmp.Or(cmp.Compare(route(b), route(a)), cmp.Compare(sev(b), sev(a)), cmp.Compare(b.Reach, a.Reach))
	})
	out = out[:min(len(out), maxWays)]
	for i := range out {
		if out[i].Sev == "" {
			continue
		}
		for si := range v.Steps {
			if v.Steps[si].Way == nil && slices.ContainsFunc(v.Steps[si].Files, func(f *file) bool {
				return slices.ContainsFunc(out[i].Rows, func(r wrow) bool { return r.St == "major" && r.Path == f.Path })
			}) {
				out[i].Step, v.Steps[si].Way = si+1, &out[i]
				break
			}
		}
	}
	return out
}

// guides sets each row's tree guides: a pipe where a row above it has a sibling further down, a tee or an end for its
// own branch.
func guides(rows []wrow) {
	later := func(k, i int) bool {
		for _, r := range rows[i+1:] {
			if r.D < k {
				return false
			}
			if r.D == k {
				return true
			}
		}
		return false
	}
	for i := range rows {
		r := &rows[i]
		for k := 1; k < r.D; k++ {
			r.Cols = append(r.Cols, map[bool]string{true: "pipe", false: "blank"}[later(k, i)])
		}
		r.Cols = append(r.Cols, map[bool]string{true: "tee", false: "end"}[later(r.D, i)])
		r.Kids = i+1 < len(rows) && rows[i+1].D > r.D
	}
}

// findingsOf is f's findings of severity sev.
func findingsOf(f *file, sev string) []*finding {
	var out []*finding
	for _, fd := range f.found() {
		if fd.Severity == sev {
			out = append(out, fd)
		}
	}
	return out
}

// findingOf is where the first of f's findings of severity sev is.
func findingOf(f *file, sev string) string {
	if fds := findingsOf(f, sev); len(fds) > 0 {
		return fds[0].Href
	}
	return ""
}

// Says is the way's line under its tree: what of the PR it runs.
func (w way) Says() string {
	s := "Runs " + plural(w.Reach, "declaration", "declarations") + " the PR changes"
	if w.New > 0 {
		s += ", " + strconv.Itoa(w.New) + " of them new"
	}
	if w.More > 0 {
		n := 0
		for _, r := range w.Rows {
			n += map[bool]int{true: 1}[r.Tag != ""]
		}
		s += "; the tree shows " + strconv.Itoa(n)
	}
	s += "."
	if w.Sev != "" {
		s += " A " + map[string]string{"critical": "blocker", "major": "major finding"}[w.Sev] + " is on the way."
	}
	return s
}

// Name is the way's name in a sentence: "GET /x", or the declaration's name alone.
func (w way) Name() string {
	if w.Method == "CALL" {
		return w.Route
	}
	return w.Method + " " + w.Route
}

// Label is the way's name for a screen reader: "GET /x: its path to the change".
func (w way) Label() string { return w.Name() + ": its path to the change" }

// WaysSay is the line beside "Where it runs": how many ways, and which a step shows.
func (v view) WaysSay() string {
	s := plural(len(v.Ways), "way in", "ways in")
	for _, w := range v.Ways {
		if w.Step > 0 {
			return s + " · the " + w.Name() + " path is in step " + fmt.Sprintf("%02d", w.Step)
		}
	}
	return s
}
