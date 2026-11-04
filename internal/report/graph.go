package report

import (
	"cmp"
	"fmt"
	"path"
	"slices"
	"strings"
)

// maxExternal is the changed declarations of other steps a step's graph shows; past it, none. Past maxStep
// declarations a step's graph is too large to read even zoomed, past maxDirs directories the PR's matrix: none.
// maxAffected is the changed declarations "outside the PR" lists, maxPairs the names a matrix cell spells out.
const maxExternal, maxStep, maxDirs, maxAffected, maxPairs = 12, 60, 40, 30, 8

// flow is a graph for the page's cytoscape: a box per file, in it the changed declarations, an edge from one to
// each it names.
type flow struct {
	Nodes []flowNode `json:"nodes"`
	Edges []flowEdge `json:"edges"`
}

type flowEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type flowNode struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Parent string `json:"parent,omitempty"` // a declaration's file
	Href   string `json:"href"`
	Name   string `json:"name,omitempty"`  // a declaration's, for its uses
	Path   string `json:"path"`            // its file's
	State  string `json:"state,omitempty"` // new, gone; ext: another step's
}

// graph is step i's changed declarations that name each other, a changed declaration of another step it names or
// that names it marked ext. Only those with an edge show; nil when none has one, or without codegraph's index: a
// guess by name draws arrows that aren't there.
func graph(steps []step, i int, syms map[string][]Symbol) *flow {
	all, byDecl := decls(steps)
	edges, used := arrows(all, byDecl, syms, i)
	if len(edges) == 0 || len(used) > maxStep {
		return nil
	}
	return build(all, used, edges, i)
}

// changed is a changed declaration of a step.
type changed struct {
	f    *file
	c    change
	step int
}

// decls is the steps' changed declarations, and where each is among them by path NUL name.
func decls(steps []step) ([]changed, map[string][]int) {
	var all []changed
	byDecl := map[string][]int{}
	for si, st := range steps {
		for _, f := range st.Files {
			for _, c := range f.Changes {
				byDecl[f.Path+"\x00"+c.Name] = append(byDecl[f.Path+"\x00"+c.Name], len(all))
				all = append(all, changed{f, c, si})
			}
		}
	}
	return all, byDecl
}

type edge struct{ a, b int }

// arrows is which of all name which, one end in step i (any when i < 0), and the declarations they join.
func arrows(all []changed, byDecl map[string][]int, syms map[string][]Symbol, i int) ([]edge, map[int]bool) {
	var edges []edge
	seen, used := map[edge]bool{}, map[int]bool{}
	external := 0
	for a, d := range all {
		for _, s := range syms[d.c.Name] {
			if s.Path != d.f.Path {
				continue
			}
			for _, to := range s.Calls {
				for _, b := range byDecl[to] {
					e := edge{a, b}
					if a == b || seen[e] || i >= 0 && all[a].step != i && all[b].step != i {
						continue
					}
					seen[e] = true
					edges = append(edges, e)
					for _, k := range []int{a, b} {
						if !used[k] && i >= 0 && all[k].step != i {
							external++
						}
						used[k] = true
					}
				}
			}
		}
	}
	if external > maxExternal { // a hub: the step's own arrows only
		var own []edge
		for _, e := range edges {
			if all[e.a].step == i && all[e.b].step == i {
				own = append(own, e)
			}
		}
		edges, used = own, map[int]bool{}
		for _, e := range edges {
			used[e.a], used[e.b] = true, true
		}
	}
	return edges, used
}

// build is the graph: files first, each before its declarations, as cytoscape wants a parent.
func build(all []changed, used map[int]bool, edges []edge, i int) *flow {
	g := &flow{}
	files := map[*file]string{}
	for k, d := range all {
		if !used[k] {
			continue
		}
		fid, ok := files[d.f]
		if !ok {
			fid = fmt.Sprintf("f%d", len(files))
			files[d.f] = fid
			g.Nodes = append(g.Nodes, flowNode{ID: fid, Label: d.f.Short, Href: "#" + d.f.Path, Path: d.f.Path})
		}
		n := flowNode{ID: fmt.Sprintf("n%d", k), Label: d.c.Name, Parent: fid, Href: "#" + d.c.ID, State: d.c.State, Name: d.c.Name, Path: d.f.Path}
		if i >= 0 && d.step != i {
			n.Label, n.State = fmt.Sprintf("%s · step %d", d.c.Name, d.step+1), "ext"
		}
		g.Nodes = append(g.Nodes, n)
	}
	for _, e := range edges {
		g.Edges = append(g.Edges, flowEdge{Source: fmt.Sprintf("n%d", e.a), Target: fmt.Sprintf("n%d", e.b)})
	}
	return g
}

// deps is which changed directories use which: a row's changed code calls or names a column's. Ordered so that a
// use is right of the diagonal; one left of it is a cycle's.
type deps struct {
	Dirs   []dep
	Cycles int      // groups of directories that use each other
	Top    []string // the heaviest uses between two: "a → b (n)"
}

type dep struct {
	Label, Href string // Href: its first changed file
	Cells       []cell
}

type cell struct {
	N     int    // the row's changed declarations' uses of the column's
	Self  bool   // the diagonal: uses within the directory
	Back  bool   // left of the diagonal: the column uses the row too, through others maybe
	Names string // the first few "a → b"
}

// matrix is the PR's changed directories and their uses of each other; nil without codegraph's index, with no use
// between two of them or past maxDirs: 40 columns is what a page holds.
func matrix(steps []step, syms map[string][]Symbol) *deps {
	all, byDecl := decls(steps)
	edges, _ := arrows(all, byDecl, syms, -1)
	dirOf := func(f *file) string { return path.Dir(f.Short) }
	var dirs []string
	first := map[string]*file{}
	for _, d := range all {
		if k := dirOf(d.f); first[k] == nil {
			first[k], dirs = d.f, append(dirs, k)
		}
	}
	slices.Sort(dirs)
	if len(dirs) < 2 || len(dirs) > maxDirs {
		return nil
	}
	at := map[string]int{}
	for k, d := range dirs {
		at[d] = k
	}
	n := len(dirs)
	w, pairs := make([][]int, n), make([][][]string, n)
	for r := range n {
		w[r], pairs[r] = make([]int, n), make([][]string, n)
	}
	cross := false
	for _, e := range edges {
		a, b := at[dirOf(all[e.a].f)], at[dirOf(all[e.b].f)]
		w[a][b]++
		pairs[a][b] = append(pairs[a][b], all[e.a].c.Name+" → "+all[e.b].c.Name)
		cross = cross || a != b
	}
	if !cross {
		return nil
	}
	ord, reach := order(w)
	m := &deps{}
	counted := make([]bool, n) // a cycle is counted by its first directory
	for i := range n {
		cycle := false
		for j := i + 1; j < n && !counted[i]; j++ {
			if reach[i][j] && reach[j][i] {
				counted[j], cycle = true, true
			}
		}
		if cycle {
			m.Cycles++
		}
	}
	type use struct{ a, b, n int }
	var top []use
	for a := range n {
		for b := range n {
			if a != b && w[a][b] > 0 {
				top = append(top, use{a, b, w[a][b]})
			}
		}
	}
	slices.SortStableFunc(top, func(x, y use) int { return cmp.Compare(y.n, x.n) })
	for _, u := range top[:min(len(top), 3)] {
		m.Top = append(m.Top, fmt.Sprintf("%s → %s (%d)", dirs[u.a], dirs[u.b], u.n))
	}
	for ri, r := range ord {
		row := dep{Label: dirs[r], Href: "#" + first[dirs[r]].Path}
		for ci, c := range ord {
			ps := pairs[r][c]
			names := strings.Join(ps[:min(len(ps), maxPairs)], ", ")
			if len(ps) > maxPairs {
				names += fmt.Sprintf(" +%d", len(ps)-maxPairs)
			}
			row.Cells = append(row.Cells, cell{N: w[r][c], Self: r == c, Back: w[r][c] > 0 && ci < ri, Names: names})
		}
		m.Dirs = append(m.Dirs, row)
	}
	return m
}

// order is the nodes of the weighted graph w so that its edges point forward where they can: next always one that
// no node left reaches without it reaching that one back, so an edge points back only within a cycle; of those
// the one that uses the others most and is used least. reach is which node reaches which.
func order(w [][]int) (ord []int, reach [][]bool) {
	n := len(w)
	reach = make([][]bool, n)
	for i := range n {
		reach[i] = make([]bool, n)
		for j := range n {
			reach[i][j] = w[i][j] > 0
		}
	}
	for k := range n { // Floyd–Warshall: n is maxDirs at most
		for i := range n {
			for j := range n {
				reach[i][j] = reach[i][j] || reach[i][k] && reach[k][j]
			}
		}
	}
	left := make([]bool, n)
	for i := range left {
		left[i] = true
	}
	for range n {
		best, score := -1, 0
		for v := range n {
			if !left[v] {
				continue
			}
			s, upstream := 0, false
			for u := range n {
				if left[u] && u != v {
					s += w[v][u] - w[u][v]
					upstream = upstream || reach[u][v] && !reach[v][u]
				}
			}
			if !upstream && (best < 0 || s > score) {
				best, score = v, s
			}
		}
		left[best] = false
		ord = append(ord, best)
	}
	return ord, reach
}

// affected is a changed declaration that code outside the PR uses: what sees the change without being in the diff.
type affected struct {
	Name, Kind, Path, ID string // ID: its change
	Why                  string // "signature changed", "" its body
	Refs                 []Ref
	N                    int
}

// reach is the changed declarations code outside the PR uses, changed signatures first, then the most used.
// codegraph's uses only: by name, a common name would list half the repo. A removed one is in no index of the
// head: the build tells who still calls it.
func reach(steps []step, syms map[string][]Symbol) []affected {
	var out []affected
	for _, st := range steps {
		for _, f := range st.Files {
			for _, c := range f.Changes {
				if c.State != "" {
					continue
				}
				why := map[bool]string{true: "signature changed"}[c.sig]
				for _, s := range syms[c.Name] {
					if s.Path == f.Path && s.Exact && s.NOutside > 0 {
						out = append(out, affected{c.Name, s.Kind, f.Path, c.ID, why, s.Outside, s.NOutside})
					}
				}
			}
		}
	}
	slices.SortStableFunc(out, func(a, b affected) int {
		return cmp.Or(cmp.Compare(b.Why, a.Why), cmp.Compare(b.N, a.N)) // "signature changed" before ""
	})
	return out[:min(len(out), maxAffected)]
}

// maxTests is the tests a step names; past them, how many more.
const maxTests = 8

// tested is the tests that reach what the step changes through codegraph's uses, its callers' and theirs, nearest
// first; more past maxTests, partial when a walk stopped short: at least that many. untested only when every file
// is code, every changed line in a declaration codegraph knows, and not even a guess by name reaches a test from
// one: see cgTests. Removed ones reach nothing.
func tested(st step, syms map[string][]Symbol) (tests []string, more int, partial, untested bool) {
	untested = true
	n := 0
	for _, f := range st.Files {
		untested = untested && f.Kind == "code" && !f.Stray && f.Cut == 0
		for _, c := range f.Changes {
			if c.State == "gone" {
				continue
			}
			n++
			known := false
			for _, s := range syms[c.Name] {
				if s.Path != f.Path || !s.Exact {
					continue
				}
				known, untested, partial = true, untested && s.Untested, partial || s.Partial
				for _, t := range s.Tests {
					if !slices.Contains(tests, t) {
						tests = append(tests, t)
					}
				}
			}
			untested = untested && known
		}
	}
	untested = untested && n > 0 && len(tests) == 0
	if len(tests) > maxTests {
		more, tests = len(tests)-maxTests, tests[:maxTests]
	}
	return tests, more, partial, untested
}
