package report

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"diffharbor/internal/checks"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"fmt"
	"io"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Map is the map of the change: its parts, the lines between them, where they sit, what lies on them. All of it is
// computed here, nothing is left to the page but drawing it; the routes of the lines, the ports on a box's edge and
// the tracks in a channel are the page's, from the Col and Row of what a line joins. nil when it has fewer than two
// parts and no outside system: there is no map to draw.
type Map struct {
	Parts     []MapPart // folder order, "Other" last
	Order     []string  // part IDs in map order: by column, then row; parts with no line after them, in folder order
	Removed   *MapBox   // the folders whose changed code files are all deleted
	Generated *MapBox
	Lines     []MapLine
	Systems   []MapSystem // outside systems, column 0
	Deploy    []MapDeploy // what a merge alone doesn't finish
	Off       []MapItem   // items with no box: a test, a data file
	Reach     MapReach
	Tables    []MapTable // the tables a migration drops
	Trace     *MapTrace  // one path from an entry to an outside call; nil without one
	Cols      int        // the highest column; the parts start at 1 when there are systems at 0
	NoGraph   bool       // no code graph: the parts sit in one row, without lines
	NoKinds   bool       // the index told no kind of any use: every use is a call
}

// MapPart is a folder of the PR's code. Col and Row place it; Alone: no line reaches it, it sits in the row
// "No line to other parts".
type MapPart struct {
	ID, Label, Path string // Path is its folder, "." the top level; ID is Path, "other" for the rest
	Files           []string
	Deleted         int  // its code files the PR deletes
	New             bool // every file is new
	Folders         int  // "Other": the folders it joins
	Col, Row        int
	Alone           bool
	Items           []MapItem
	Unread          int  // files the review left out or failed on
	Reach, Tests    int  // places outside the PR that name its changed code: in code, in tests
	DB              bool // its files used a table this PR drops
}

// MapBox is the Removed and the Generated box.
type MapBox struct {
	Files   []string    // Generated: its files
	N       int         // files
	Folders []MapFolder // Removed
	Items   []MapItem
}

// MapFolder is a folder of the Removed box and its deleted files.
type MapFolder struct {
	Path string
	N    int
}

// MapItem is something that needs a person, sitting on a box: Kind "finding", "risk" or "pr".
type MapItem struct {
	Kind, Title, Path string
	Line              int
	Sev               string
}

// MapLine is the use of one part's changed code by another: it runs from the part that declares to the part that
// uses, the way results go. Names are what From declares and To uses, Back the other way when TwoWay.
type MapLine struct {
	From, To string
	Names    []MapName
	Back     []MapName
	TwoWay   bool
	// Calls, Through (calls through an interface), Named (uses without a call), Reads and Writes (a store's, kept for
	// the page, drawn as nothing yet) count both ways.
	Calls, Through, Named, Reads, Writes int
	Weight                               int      // Calls + Through
	Width                                int      // the line's px: 1 for under 3 calls, 2 for under 10, else 3
	Style                                string   // "solid": a direct call; "dashed": through an interface or named; "": only reads and writes
	Chevron                              string   // where the arrow heads sit: "to" (at To), "from", "both", "" for names alone
	Via                                  []string // the interfaces its calls go through
}

// MapName is a declaration a line carries and how it is used, N times, first at At (path:line). Kind: "calls",
// "through" (Via the interface), "named", "reads", "writes".
type MapName struct {
	Name, Kind, Via, At string
	N                   int
}

// OutsideCall is a call out of the repo the review names: the system, the line that sends the request and the
// URL's name or literal with its place.
type OutsideCall struct {
	System, Path string
	Line         int
	URL, URLAt   string
}

// MapSystem is an outside system and the parts that hold its calls, a dotted line to each.
type MapSystem struct {
	Name     string
	Calls    []OutsideCall
	Parts    []string
	Col, Row int
}

// MapDeploy is a box in the row "Before merging": Kind "migration", "environment", "dependencies", "ci",
// "configuration", "prs", "docs"; N the items on it.
type MapDeploy struct {
	Kind, Label, Path string
	N                 int
	Items             []MapItem
	Drops             []string // a migration: the tables it drops
}

// MapReach is what code outside the PR names of the changed declarations: Places in code, Tests the places in tests
// left out, Decls the declarations with a place in code. List keeps at most maxAffected of them, More the rest.
type MapReach struct {
	Places, Tests, Decls, More int
	List                       []MapReachDecl
}

// MapReachDecl is a changed declaration that code outside the PR names.
type MapReachDecl struct {
	Part, Name, Path string
	Line, N          int   // N places in code
	Places           []Ref // the first of them
}

// MapTable is a table a migration drops, at Path:Line. Parts are those whose code files imported its declaration
// at the base, Tests the test files that did, Elsewhere the files in no part. Imports and Named are what the head
// still has: code that imports it, code that names it in SQL (path:line).
type MapTable struct {
	Name, Path, Decl string
	Line             int
	Parts            []MapTablePart
	Tests, Elsewhere int
	Imports, Named   []string
}

// MapTablePart is the files of a part that imported a dropped table.
type MapTablePart struct {
	Part  string
	Files []string
}

// MapTrace is the first way, in the report's order, that reaches a declaration holding an outside call: Rows from
// the entry down to that declaration, then the call.
type MapTrace struct {
	Method, Route string // the way's entry
	At            ref
	Rows          []wrow
	Call          OutsideCall
	Part          string // the part that holds the call
}

const (
	maxParts, maxDepth = 16, 3
	mapCols            = "_($["
)

var mapExt = map[string]bool{".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true}

// mapCode is a code file the map groups: tests, tool config (vite.config.ts goes to CI) and the rest aren't.
func mapCode(p string) bool {
	return mapExt[path.Ext(p)] && diff.Kind(p) == "code" && !strings.Contains(path.Base(p), ".config.")
}

// folder is p's first n directories, "." at the top.
func folder(p string, n int) string {
	dir := path.Dir(p)
	if dir == "." {
		return dir
	}
	segs := strings.Split(dir, "/")
	return strings.Join(segs[:min(n, len(segs))], "/")
}

type mapper struct {
	d     Data
	v     view
	m     *Map
	part  map[string]int // a kept code file -> its part
	depth int
	other map[string]bool // the folders "Other" joins
	box   map[string]*MapDeploy
	gen   map[string]bool
	added map[string]bool
}

// BuildMap is the map of d's change, with the outside calls the review named (none yet). sb is the checkout: its
// generated headers, and git for the tables a migration drops; nil reads neither. Symbol.State is set as Render's
// page does, by prepare.
func BuildMap(sb *proc.Sandbox, d Data, outside []OutsideCall) *Map {
	b := &mapper{
		d: d, v: prepare(d), m: &Map{}, part: map[string]int{}, box: map[string]*MapDeploy{}, gen: map[string]bool{},
		added: diff.RightLines(d.Diff, false), other: map[string]bool{},
	}
	root := ""
	if sb != nil {
		root = sb.Repo()
	}
	b.parts(root)
	b.systems(outside)
	if len(b.m.Parts) < 2 && len(b.m.Systems) == 0 {
		return nil
	}
	b.lines()
	b.layout()
	b.reach()
	b.tables(sb)
	b.deploy()
	b.trace()
	return b.m
}

// parts groups the PR's code files by folder (6.1): the first n levels, n the largest up to maxDepth that gives
// at most maxParts; past that at one level, the biggest stay and the rest are "Other".
func (b *mapper) parts(root string) {
	var kept, gone []string
	fresh := map[string]bool{}
	diff.EachFile(b.d.Diff, func(p, f string) {
		switch {
		case !mapCode(p):
		case diff.Skip.MatchString(p) || diff.Generated(root, p, f):
			b.gen[p] = true
		default:
			kept = append(kept, p)
			fresh[p] = strings.Contains(f, "\n--- /dev/null\n")
		}
	})
	diff.EachDeleted(b.d.Diff, func(p, _ string) {
		if mapCode(p) && !diff.Skip.MatchString(p) {
			gone = append(gone, p)
		}
	})
	b.depth = 1
	for n := maxDepth; n > 1; n-- {
		if len(foldersOf(kept, n)) <= maxParts {
			b.depth = n
			break
		}
	}
	groups := foldersOf(kept, b.depth)
	keys := slices.Sorted(maps.Keys(groups))
	if len(keys) > maxParts {
		slices.SortStableFunc(keys, func(x, y string) int { return cmp.Compare(len(groups[y]), len(groups[x])) }) // the biggest first
		for _, k := range keys[maxParts-1:] {
			b.other[k] = true
		}
		keys = slices.Sorted(slices.Values(keys[:maxParts-1]))
	}
	at := map[string]int{}
	for _, k := range keys {
		at[k] = len(b.m.Parts)
		b.m.Parts = append(b.m.Parts, MapPart{ID: k, Path: k, Files: groups[k]})
	}
	if len(b.other) > 0 {
		at["other"] = len(b.m.Parts)
		o := MapPart{ID: "other", Path: "other", Label: fmt.Sprintf("Other · %d folders", len(b.other)), Folders: len(b.other)}
		for k := range b.other {
			o.Files = append(o.Files, groups[k]...)
			at[k] = len(b.m.Parts)
		}
		slices.Sort(o.Files)
		b.m.Parts = append(b.m.Parts, o)
	}
	labels := map[string]int{}
	for i := range b.m.Parts {
		p := &b.m.Parts[i]
		if p.Label == "" {
			p.Label = partLabel(p.Path, false)
		}
		labels[p.Label]++
		p.New = len(p.Files) > 0
		for _, f := range p.Files {
			b.part[f], p.New = i, p.New && fresh[f]
		}
	}
	for i := range b.m.Parts {
		if p := &b.m.Parts[i]; labels[p.Label] > 1 && p.ID != "other" {
			p.Label = partLabel(p.Path, true)
		}
	}
	removed := map[string]int{}
	for _, f := range gone {
		k := folder(f, b.depth)
		if i, ok := at[k]; ok {
			b.m.Parts[i].Deleted++
		} else {
			removed[k]++
		}
	}
	if len(removed) > 0 {
		r := &MapBox{}
		for _, k := range slices.Sorted(maps.Keys(removed)) {
			r.Folders, r.N = append(r.Folders, MapFolder{k, removed[k]}), r.N+removed[k]
		}
		slices.SortStableFunc(r.Folders, func(x, y MapFolder) int { return cmp.Compare(y.N, x.N) })
		b.m.Removed = r
	}
	if len(b.gen) > 0 {
		b.m.Generated = &MapBox{Files: slices.Sorted(maps.Keys(b.gen)), N: len(b.gen)}
	}
	for i := range b.m.Parts {
		for _, f := range b.m.Parts[i].Files {
			for _, u := range slices.Concat(b.d.Failed, b.d.Omitted) {
				if u == f {
					b.m.Parts[i].Unread++
					break
				}
			}
		}
	}
}

func foldersOf(files []string, n int) map[string][]string {
	g := map[string][]string{}
	for _, f := range files {
		k := folder(f, n)
		g[k] = append(g[k], f)
	}
	return g
}

// partLabel is a folder's last directory; a name that starts with _, (, $ or [ keeps its parent, and so does one
// that shares its label with another part.
func partLabel(key string, parent bool) string {
	if key == "." {
		return "top level"
	}
	segs := strings.Split(key, "/")
	last := segs[len(segs)-1]
	if len(segs) > 1 && (parent || strings.ContainsRune(mapCols, rune(last[0]))) {
		return segs[len(segs)-2] + "/" + last
	}
	return last
}

// stated is whether the PR adds or changes s: the page's State, or a method whose lines the diff adds to (the
// class methods the code graph knows have no State).
func (b *mapper) stated(s *Symbol) bool {
	if s.State != "" {
		return true
	}
	if s.Kind != "method" || s.To == 0 {
		return false
	}
	for l := s.From; l <= s.To; l++ {
		if b.added[s.Path+":"+strconv.Itoa(l)] {
			return true
		}
	}
	return false
}

// useKind is what a use of the index's kind is: a call (a JSX element too), a store's read or write, or a name.
func useKind(k string) string {
	switch k {
	case "", "calls", "renders":
		return "calls"
	case "reads", "writes":
		return k
	}
	return "named"
}

func kindAt(s *Symbol, i int) string {
	if i < len(s.HereKind) {
		return s.HereKind[i]
	}
	return ""
}

func placeOf(site string) string { return site[:max(strings.LastIndexByte(site, ':'), 0)] }

type use struct{ decl, user int }

type nameKey struct{ name, kind, via string }

// lines reads the uses of changed declarations between parts (6.2): a direct call, a call through an interface, a
// name used without one, a store read or write.
//
// Through an interface: the index ties an interface's method to the methods behind it with a use on the line where
// the interface declares it, and the callers of the interface's method are uses of the interface's. VTA, which
// resolves a call through an interface to the method it reaches, adds an edge into that method: a call. So a
// call site VTA resolved is a direct call, solid; the index's alone, tied to every method of the name, is a call
// through the interface, dashed.
func (b *mapper) lines() {
	syms := b.d.Symbols
	if len(syms) == 0 {
		b.m.NoGraph = true
		for i := range b.m.Parts {
			b.m.Parts[i].Col = i + 1
		}
		b.m.Cols = len(b.m.Parts)
		b.m.Order = b.ids(nil)
		return
	}
	names := slices.Sorted(maps.Keys(syms))
	iface := map[string]string{} // where an interface declares a method -> the interface
	for _, n := range names {
		for _, s := range syms[n] {
			if t, ok := strings.CutPrefix(s.Kind, "method of "); ok {
				iface[s.Path+":"+strconv.Itoa(s.Line)] = t
			}
		}
	}
	used := usage{}
	behind, kinds := b.direct(used, names, iface)
	b.through(used, names, iface, behind)
	b.m.NoKinds = !kinds && len(used) > 0
	done := map[use]bool{}
	for _, u := range slices.SortedFunc(maps.Keys(used), func(x, y use) int { return cmp.Or(cmp.Compare(x.decl, y.decl), cmp.Compare(x.user, y.user)) }) {
		if done[u] {
			continue
		}
		done[u], done[use{u.user, u.decl}] = true, true
		back := used[use{u.user, u.decl}]
		l := MapLine{From: b.m.Parts[u.decl].ID, To: b.m.Parts[u.user].ID, Names: sortNames(used[u]), Back: sortNames(back), TwoWay: len(back) > 0 && len(used[u]) > 0}
		if len(used[u]) == 0 { // only the other way: it runs from there
			l.From, l.To, l.Names, l.Back = l.To, l.From, l.Back, nil
		}
		l.count()
		b.m.Lines = append(b.m.Lines, l)
	}
}

// usage is the uses between two parts, by declaration and kind.
type usage map[use]map[nameKey]*MapName

func (us usage) add(decl, user int, name, kind, via, site string) {
	u := use{decl, user}
	if us[u] == nil {
		us[u] = map[nameKey]*MapName{}
	}
	k := nameKey{name, kind, via}
	if us[u][k] == nil {
		us[u][k] = &MapName{Name: name, Kind: kind, Via: via, At: site}
	}
	us[u][k].N++
}

// direct adds the uses of the declarations the PR changes in another part's code file; behind is the methods the index
// ties to each interface method's declaring line, kinds whether any use told its kind.
func (b *mapper) direct(used usage, names []string, iface map[string]string) (behind map[string][]*Symbol, kinds bool) {
	behind = map[string][]*Symbol{}
	for _, n := range names {
		for i := range b.d.Symbols[n] {
			s := &b.d.Symbols[n][i]
			dp, ok := b.part[s.Path]
			if !ok || !s.Exact || !b.stated(s) || strings.HasPrefix(s.Kind, "method of ") {
				continue
			}
			kinds = kinds || slices.ContainsFunc(s.HereKind, func(k string) bool { return k != "" })
			for j, h := range s.Here {
				k := useKind(kindAt(s, j))
				if iface[h] != "" { // the index's tie to an interface: no call
					behind[h], k = append(behind[h], s), "named"
				}
				if up, ok := b.part[placeOf(h)]; ok && up != dp {
					used.add(dp, up, n, k, "", h)
				}
			}
		}
	}
	return behind, kinds
}

// through adds the calls of an interface's method, each to the part of every method behind it, unless VTA resolved the
// call site to one of them: that is a direct call.
func (b *mapper) through(used usage, names []string, iface map[string]string, behind map[string][]*Symbol) {
	for _, n := range names {
		for i := range b.d.Symbols[n] {
			s := &b.d.Symbols[n][i]
			t, ok := strings.CutPrefix(s.Kind, "method of ")
			tied := behind[s.Path+":"+strconv.Itoa(s.Line)]
			if !ok || !s.Exact || len(tied) == 0 {
				continue
			}
			for j, h := range s.Here {
				up, ok := b.part[placeOf(h)]
				if !ok || useKind(kindAt(s, j)) != "calls" || iface[h] != "" || slices.ContainsFunc(tied, func(c *Symbol) bool { return calledAt(c, h) }) {
					continue
				}
				seen := map[int]bool{}
				for _, c := range tied {
					if dp, ok := b.part[c.Path]; ok && dp != up && !seen[dp] {
						seen[dp] = true
						used.add(dp, up, n, "through", t, h)
					}
				}
			}
		}
	}
}

// count sets what a line's names add up to: its counts, weight, width, style and chevrons.
func (l *MapLine) count() {
	vias := map[string]bool{}
	for _, ns := range [][]MapName{l.Names, l.Back} {
		for _, n := range ns {
			switch n.Kind {
			case "calls":
				l.Calls += n.N
			case "through":
				l.Through += n.N
				vias[n.Via] = true
			case "named":
				l.Named += n.N
			case "reads":
				l.Reads += n.N
			case "writes":
				l.Writes += n.N
			}
		}
	}
	l.Via = slices.Sorted(maps.Keys(vias))
	l.Weight = l.Calls + l.Through
	l.Width = 1 + map[bool]int{true: 1}[l.Weight >= 3] + map[bool]int{true: 1}[l.Weight >= 10]
	switch {
	case l.Calls > 0:
		l.Style = "solid"
	case l.Through+l.Named > 0:
		l.Style = "dashed"
	}
	l.Chevron = map[[2]bool]string{{true, true}: "both", {true, false}: "to", {false, true}: "from"}[[2]bool{arrow(l.Names), arrow(l.Back)}]
}

// calledAt is whether s has a call at site h.
func calledAt(s *Symbol, h string) bool {
	i := slices.Index(s.Here, h)
	return i >= 0 && useKind(kindAt(s, i)) == "calls"
}

// arrow is whether a direction has calls: the chevron sits at the caller's edge.
func arrow(ns []MapName) bool {
	return slices.ContainsFunc(ns, func(n MapName) bool { return n.Kind == "calls" || n.Kind == "through" })
}

// sortNames puts calls first, then those through an interface, names, reads, writes; in each the most uses first,
// ties in name order ignoring case.
func sortNames(m map[nameKey]*MapName) []MapName {
	rank := map[string]int{"calls": 0, "through": 1, "named": 2, "reads": 3, "writes": 4}
	var out []MapName
	for _, n := range m {
		out = append(out, *n)
	}
	slices.SortFunc(out, func(x, y MapName) int {
		return cmp.Or(cmp.Compare(rank[x.Kind], rank[y.Kind]), cmp.Compare(y.N, x.N),
			cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)), cmp.Compare(x.Name, y.Name), cmp.Compare(x.Via, y.Via))
	})
	return out
}

// ids is the parts' IDs in folder order, minus skip.
func (b *mapper) ids(skip map[string]bool) []string {
	var out []string
	for _, p := range b.m.Parts {
		if !skip[p.ID] {
			out = append(out, p.ID)
		}
	}
	return out
}

// systems are the review's outside calls grouped by system, kept when the call sits in a part's code file (6.4).
func (b *mapper) systems(calls []OutsideCall) {
	at := map[string]int{}
	for _, c := range calls {
		pi, ok := b.part[c.Path]
		if !ok {
			continue
		}
		i, ok := at[c.System]
		if !ok {
			i = len(b.m.Systems)
			at[c.System] = i
			b.m.Systems = append(b.m.Systems, MapSystem{Name: c.System})
		}
		s := &b.m.Systems[i]
		s.Calls = append(s.Calls, c)
		if id := b.m.Parts[pi].ID; !slices.Contains(s.Parts, id) {
			s.Parts = append(s.Parts, id)
		}
	}
	slices.SortFunc(b.m.Systems, func(x, y MapSystem) int { return strings.Compare(x.Name, y.Name) })
}

// mapEdge is a weighted line between two nodes of the layout.
type mapEdge struct{ to, w int }

// rows gives each node its row in its column, sweeping the columns by the weighted mean row of their neighbours;
// it also returns the last column.
func rows(node []string, cols []int, adj [][]mapEdge) (top int, row []int) {
	for _, c := range cols {
		top = max(top, c)
	}
	row = make([]int, len(node))
	rank := func(c int, near func(o int) bool) { // the column c by the weighted mean row of its near neighbours
		var m []int
		for i := range node {
			if cols[i] == c {
				m = append(m, i)
			}
		}
		mean := map[int]float64{}
		for _, i := range m {
			sum, tot := 0, 0
			for _, e := range adj[i] {
				if near(e.to) {
					sum, tot = sum+e.w*row[e.to], tot+e.w
				}
			}
			mean[i] = float64(row[i])
			if tot > 0 {
				mean[i] = float64(sum) / float64(tot)
			}
		}
		slices.SortStableFunc(m, func(x, y int) int {
			return cmp.Or(cmp.Compare(mean[x], mean[y]), cmp.Compare(len(adj[y]), len(adj[x])), strings.Compare(node[x], node[y]))
		})
		for r, i := range m {
			row[i] = r
		}
	}
	for c := 0; c <= top; c++ { // folder order to start with
		r := 0
		for i := range node {
			if cols[i] == c {
				row[i] = r
				r++
			}
		}
	}
	for range 4 {
		for c := 1; c <= top; c++ {
			rank(c, func(o int) bool { return cols[o] < c })
		}
		for c := top - 1; c >= 0; c-- {
			rank(c, func(o int) bool { return cols[o] > c })
		}
	}
	return top, row
}

// weights is the matrix of the drawn lines: w[i][j] is how much part i uses part j, at least 1 when it does.
func (b *mapper) weights(at map[string]int) [][]int {
	n := len(b.m.Parts)
	drawn := func(ns []MapName) bool {
		return slices.ContainsFunc(ns, func(x MapName) bool { return x.Kind == "calls" || x.Kind == "through" || x.Kind == "named" })
	}
	w := make([][]int, n) // w[i][j]: i uses j, by the weight of the line, at least 1
	for i := range w {
		w[i] = make([]int, n)
	}
	for _, l := range b.m.Lines {
		from, to := at[l.From], at[l.To]
		if drawn(l.Names) { // To uses what From declares
			w[to][from] += max(l.Weight, 1)
		}
		if drawn(l.Back) {
			w[from][to] += max(l.Weight, 1)
		}
	}
	return w
}

// layout sets the columns and rows (6.3): a part is one column right of the parts it uses, a cycle shares one; each
// column is ordered by four sweeps of the weighted mean row of the parts a part touches.
func (b *mapper) layout() {
	if b.m.NoGraph {
		return
	}
	n, at := len(b.m.Parts), map[string]int{}
	for i, p := range b.m.Parts {
		at[p.ID] = i
	}
	w := b.weights(at)
	_, reach := order(w)
	same := func(i, j int) bool { return i == j || reach[i][j] && reach[j][i] }
	memo := make([]int, n)
	var col func(i int) int
	col = func(i int) int {
		if memo[i] > 0 {
			return memo[i]
		}
		top := 0
		for j := range n {
			if !same(i, j) {
				continue
			}
			for k := range n {
				if w[j][k] > 0 && !same(i, k) {
					top = max(top, col(k))
				}
			}
		}
		for j := range n {
			if same(i, j) {
				memo[j] = top + 1
			}
		}
		return top + 1
	}
	// nodes: the parts with a line, then the systems
	var node []string // IDs, parts first
	idx := map[string]int{}
	touch := make([]bool, n)
	for _, l := range b.m.Lines {
		if l.Style != "" {
			touch[at[l.From]], touch[at[l.To]] = true, true
		}
	}
	for _, s := range b.m.Systems {
		for _, p := range s.Parts {
			touch[at[p]] = true
		}
	}
	var cols []int
	for i, p := range b.m.Parts {
		if touch[i] {
			idx[p.ID] = len(node)
			node, cols = append(node, p.ID), append(cols, col(i))
		} else {
			b.m.Parts[i].Alone = true
		}
	}
	nparts := len(node)
	for _, s := range b.m.Systems {
		idx["system "+s.Name] = len(node)
		node, cols = append(node, "system "+s.Name), append(cols, 0)
	}
	adj := make([][]mapEdge, len(node))
	link := func(a, c, wt int) {
		adj[a], adj[c] = append(adj[a], mapEdge{c, wt}), append(adj[c], mapEdge{a, wt})
	}
	for _, l := range b.m.Lines {
		if l.Style != "" {
			link(idx[l.From], idx[l.To], max(l.Weight, 1))
		}
	}
	for _, s := range b.m.Systems {
		for _, p := range s.Parts {
			link(idx["system "+s.Name], idx[p], 1)
		}
	}
	top, row := rows(node, cols, adj)
	b.m.Cols = top
	for i, id := range node[:nparts] {
		p := &b.m.Parts[at[id]]
		p.Col, p.Row = cols[i], row[i]
	}
	for k := range b.m.Systems {
		i := nparts + k
		b.m.Systems[k].Col, b.m.Systems[k].Row = cols[i], row[i]
	}
	ord := slices.Clone(b.m.Parts)
	slices.SortStableFunc(ord, func(x, y MapPart) int {
		return cmp.Or(cmp.Compare(map[bool]int{true: 1}[x.Alone], map[bool]int{true: 1}[y.Alone]), cmp.Compare(x.Col, y.Col), cmp.Compare(x.Row, y.Row))
	})
	for _, p := range ord {
		b.m.Order = append(b.m.Order, p.ID)
	}
}

// reach is what code outside the PR names of the changed declarations, in code only: a place in a test names the code
// to test it. The sums count all of it; the list keeps the first maxAffected declarations, signature changes first.
func (b *mapper) reach() {
	type cand struct {
		d   MapReachDecl
		sig bool
	}
	var cs []cand
	for _, n := range slices.Sorted(maps.Keys(b.d.Symbols)) {
		for i := range b.d.Symbols[n] {
			s := &b.d.Symbols[n][i]
			pi, ok := b.part[s.Path]
			if !ok || !s.Exact || !b.stated(s) || s.NOutside == 0 {
				continue
			}
			p := &b.m.Parts[pi]
			code := s.NOutside - s.NOutsideTests
			p.Tests, b.m.Reach.Tests = p.Tests+s.NOutsideTests, b.m.Reach.Tests+s.NOutsideTests
			if code <= 0 {
				continue
			}
			p.Reach, b.m.Reach.Places = p.Reach+code, b.m.Reach.Places+code
			d := MapReachDecl{Part: p.ID, Name: n, Path: s.Path, Line: s.Line, N: code}
			for _, r := range s.Outside {
				if !checks.IsTest(r.Path) {
					d.Places = append(d.Places, r)
				}
			}
			cs = append(cs, cand{d, s.State == "signature"})
		}
	}
	slices.SortStableFunc(cs, func(x, y cand) int {
		return cmp.Or(cmp.Compare(map[bool]int{true: 0, false: 1}[x.sig], map[bool]int{true: 0, false: 1}[y.sig]), cmp.Compare(y.d.N, x.d.N))
	})
	b.m.Reach.Decls = len(cs)
	if len(cs) > maxAffected {
		b.m.Reach.More, cs = len(cs)-maxAffected, cs[:maxAffected]
	}
	pos := map[string]int{}
	for i, id := range b.m.Order {
		pos[id] = i
	}
	slices.SortStableFunc(cs, func(x, y cand) int { return cmp.Compare(pos[x.d.Part], pos[y.d.Part]) })
	for _, c := range cs {
		b.m.Reach.List = append(b.m.Reach.List, c.d)
	}
}

// deploy is the row "Before merging" and the items on the map (6.5): a box for each of risks()' kinds with items, a
// box for each migration, one for open PRs and one for docs; a finding sits on the box that holds its file: a part,
// Generated, a deploy box, else it is Off.
func (b *mapper) deploy() {
	boxes := map[string]*MapDeploy{}
	box := func(kind, label, p string) *MapDeploy {
		if boxes[label] == nil {
			boxes[label] = &MapDeploy{Kind: kind, Label: label, Path: p}
		}
		return boxes[label]
	}
	label := func(kind string) string {
		return map[string]string{"ci": "CI"}[kind] + map[bool]string{true: strings.ToUpper(kind[:1]) + kind[1:]}[kind != "ci"]
	}
	kinds := map[string]string{"Environment": "environment", "Dependencies": "dependencies", "Build, deploy and CI": "ci", "Configuration": "configuration"}
	for _, r := range b.v.Risks {
		var bx *MapDeploy
		for _, it := range r.Items {
			mi := MapItem{Kind: "risk", Title: strings.TrimSpace(it.What + " " + it.Note)}
			if r.Title == "Migrations" {
				mi.Path = it.What
				bx = box("migration", migrationLabel(it.What), it.What)
			} else {
				bx = box(kinds[r.Title], label(kinds[r.Title]), "")
				if r.Title != "Environment" && r.Title != "Dependencies" {
					mi.Path = it.What
				}
			}
			bx.Items, bx.N = append(bx.Items, mi), bx.N+1
		}
		if bx != nil && r.Title != "Migrations" {
			bx.N += r.More
		}
	}
	for _, o := range b.d.Others {
		bx := box("prs", "Open PRs", "")
		bx.Items, bx.N = append(bx.Items, MapItem{Kind: "pr", Title: o.Title}), bx.N+1
	}
	for _, t := range b.m.Tables {
		bx := box("migration", migrationLabel(t.Path), t.Path)
		bx.Drops = append(bx.Drops, t.Name)
	}
	for _, f := range b.d.Findings {
		mi := MapItem{Kind: "finding", Title: f.Title, Path: f.Path, Line: f.Line, Sev: f.Severity}
		on := func(bx *MapDeploy) { bx.Items, bx.N = append(bx.Items, mi), bx.N+1 }
		switch pi, ok := b.part[f.Path]; {
		case ok:
			b.m.Parts[pi].Items = append(b.m.Parts[pi].Items, mi)
		case b.gen[f.Path]:
			b.m.Generated.Items = append(b.m.Generated.Items, mi)
		case diff.Kind(f.Path) == "doc":
			on(box("docs", "Docs", ""))
		case migration.MatchString(f.Path):
			on(box("migration", migrationLabel(f.Path), f.Path))
		case deploy.MatchString(f.Path) || strings.Contains(path.Base(f.Path), ".config.") && mapExt[path.Ext(f.Path)]:
			on(box("ci", "CI", ""))
		case config.MatchString(f.Path) && diff.Kind(f.Path) != "test":
			on(box("configuration", "Configuration", ""))
		default:
			b.m.Off = append(b.m.Off, mi)
		}
	}
	rank := map[string]int{"migration": 0, "environment": 1, "dependencies": 2, "ci": 3, "configuration": 4, "prs": 5, "docs": 6}
	for _, bx := range slices.SortedFunc(maps.Values(boxes), func(x, y *MapDeploy) int {
		return cmp.Or(cmp.Compare(rank[x.Kind], rank[y.Kind]), strings.Compare(x.Label, y.Label))
	}) {
		b.m.Deploy = append(b.m.Deploy, *bx)
	}
}

// migrationLabel is "Migration 0059" for drizzle/0059_drop_x.sql, the file's name without its extension otherwise.
func migrationLabel(p string) string {
	base := path.Base(p)
	base = strings.TrimSuffix(base, path.Ext(base))
	if n := strings.IndexFunc(base, func(r rune) bool { return r < '0' || r > '9' }); n > 0 {
		base = base[:n]
	}
	return "Migration " + base
}

// trace is the first way into the change, in the report's order, to a declaration that holds an outside call
// (6.12); nil without a call or a way that reaches one.
func (b *mapper) trace() {
	for _, w := range b.v.Ways {
		for ri, r := range w.Rows {
			for _, s := range b.d.Symbols[r.Name] {
				if s.Path != r.Path || !s.Exact {
					continue
				}
				for _, sys := range b.m.Systems {
					for _, c := range sys.Calls {
						if c.Path != r.Path || c.Line < s.From || c.Line > s.To {
							continue
						}
						rows := []wrow{r}
						for k, d := ri-1, r.D; k >= 0 && d > 1; k-- { // the rows above that lead down to it
							if w.Rows[k].D < d {
								rows, d = append([]wrow{w.Rows[k]}, rows...), w.Rows[k].D
							}
						}
						b.m.Trace = &MapTrace{Method: w.Method, Route: w.Route, At: w.At, Rows: rows, Call: c, Part: b.m.Parts[b.part[c.Path]].ID}
						return
					}
				}
			}
		}
	}
}

var (
	dropTable = regexp.MustCompile(`(?i)\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?([^;]+)`)
	// downMark starts what runs on the way back: goose's and dbmate's
	downMark  = regexp.MustCompile(`(?i)^\s*--\s*(?:\+goose\s+down|migrate:down)\b`)
	tableName = regexp.MustCompile(`^\w+$`)
	// tableDecl is a table's declaration, its name in group 1: export const items = pgTable(
	tableDecl = regexp.MustCompile(`(?:const|let|var)\s+(\w+)\s*(?::[^=]+)?=\s*[\w.]*[Tt]able\w*\s*(?:<[^>]*>)?\(`)
	// reexport is an import or export of names from a module, the names in group 1 and the module in group 2
	reexport = regexp.MustCompile(`(?s)\b(import|export)\b([^;'"]*?)\bfrom\s*['"]([^'"]+)['"]`)
	jsSpecs  = []string{"*.ts", "*.tsx", "*.js", "*.jsx", "*.mjs"}
)

// maxTableFiles is the files a search of the base or the head reads; maxNamed the places it keeps per table.
const (
	maxTableFiles, maxNamed = 300, 20
	tableTime               = time.Minute
)

// drops is the tables the migrations of the diff drop: a DROP TABLE on a line the PR adds, what runs on migrate (a
// goose file's Down part doesn't count). A file's text is its head's, the diff's lines without it.
func (b *mapper) drops() []MapTable {
	var out []MapTable
	diff.EachFile(b.d.Diff, func(p, f string) {
		if path.Ext(p) != ".sql" {
			return
		}
		fl := parse(p, f, "")
		type ln struct {
			no   int
			text string
			add  bool
		}
		var lines []ln
		added := map[int]bool{}
		for _, l := range fl.Lines {
			if n, err := strconv.Atoi(l.No); err == nil && l.Class == "add" {
				added[n] = true
				if b.d.Sources[p] == "" {
					lines = append(lines, ln{n, l.Text, true})
				}
			} else if err == nil && b.d.Sources[p] == "" {
				lines = append(lines, ln{n, l.Text, false})
			}
		}
		for i, t := range strings.Split(b.d.Sources[p], "\n") {
			if b.d.Sources[p] != "" {
				lines = append(lines, ln{i + 1, t, added[i+1]})
			}
		}
		seen := map[string]bool{}
		for _, l := range lines {
			if downMark.MatchString(l.text) {
				break
			}
			m := dropTable.FindStringSubmatch(l.text)
			if m == nil || !l.add || strings.HasPrefix(strings.TrimSpace(l.text), "--") {
				continue
			}
			for _, t := range strings.Split(m[1], ",") {
				fs := strings.Fields(t)
				if len(fs) == 0 {
					continue
				}
				name := fs[0][strings.LastIndexByte(fs[0], '.')+1:]
				name = strings.Trim(name, "\"`'[]")
				if tableName.MatchString(name) && !seen[name] {
					seen[name] = true
					out = append(out, MapTable{Name: name, Path: p, Line: l.no})
				}
			}
		}
	})
	return out
}

// tableSite is the file and id a table's declaration names.
type tableSite struct{ file, id string }

// declarations finds, at the base, the file whose code quotes each table name with a table call on that line or the one above.
func declarations(git func(args ...string) []byte, names []string) map[string]tableSite {
	// the declaration: the file whose code quotes the name, a table call on that line or the one above
	quoted := regexp.MustCompile("[\"'`](" + strings.Join(names, "|") + ")[\"'`]")
	text := map[string]map[int]string{}
	for _, l := range strings.Split(string(git(append([]string{"grep", "-n", "-z", "-I", "-B1", "-E", quoted.String(), "refs/base", "--"}, jsSpecs...)...)), "\n") {
		if f := strings.Split(l, "\x00"); len(f) == 3 {
			p, n := strings.TrimPrefix(f[0], "refs/base:"), 0
			n, _ = strconv.Atoi(f[1])
			if text[p] == nil {
				text[p] = map[int]string{}
			}
			text[p][n] = f[2]
		}
	}
	decls := map[string]tableSite{}
	for _, p := range slices.Sorted(maps.Keys(text)) {
		if diff.Kind(p) == "test" {
			continue
		}
		for _, n := range slices.Sorted(maps.Keys(text[p])) {
			q := quoted.FindStringSubmatch(text[p][n])
			if q == nil || decls[q[1]].id != "" {
				continue
			}
			if m := tableDecl.FindStringSubmatch(text[p][n-1] + "\n" + text[p][n]); m != nil {
				decls[q[1]] = tableSite{p, m[1]}
			}
		}
	}
	return decls
}

// sqlNames notes where the head names a table in SQL.
func sqlNames(git func(args ...string) []byte, ts []MapTable, names []string) {
	// the head names a table in SQL: a keyword before it (git's patterns are POSIX: no (?:))
	pat := "\\b(from|join|into|update|table)\\s+[\"`]?(" + strings.Join(names, "|") + ")\\b"
	sql := regexp.MustCompile("(?i)" + pat)
	args := []string{"grep", "-n", "-z", "-i", "-I", "-E", pat, "--", "*.go"}
	for _, l := range strings.Split(string(git(append(args, jsSpecs...)...)), "\n") {
		f := strings.Split(l, "\x00")
		if len(f) != 3 || diff.Kind(f[0]) == "test" {
			continue
		}
		if m := sql.FindStringSubmatch(f[2]); m != nil {
			for i := range ts {
				if strings.EqualFold(ts[i].Name, m[2]) && len(ts[i].Named) < maxNamed {
					ts[i].Named = append(ts[i].Named, f[0]+":"+f[1])
				}
			}
		}
	}
}

// tables is Map.Tables (6.11): the tables the diff's migrations drop, who imported their declaration at the base,
// and what the head still has. Six git calls at most, in the minute tableTime gives them; without sb the first only.
func (b *mapper) tables(sb *proc.Sandbox) {
	ts := b.drops()
	if sb == nil || len(ts) == 0 {
		b.m.Tables = ts
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), tableTime)
	defer cancel()
	git := func(args ...string) []byte {
		out, _ := proc.Run(sb.Command(ctx, sb.Repo(), nil, "git", args...)) // exit 1: nothing found
		return out
	}
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = t.Name
	}
	decls := declarations(git, names)
	ids := map[string]bool{}
	for i := range ts {
		if d, ok := decls[ts[i].Name]; ok {
			ts[i].Decl, ids[d.id] = d.file, true
		}
	}
	if len(ids) > 0 {
		in := func(ref string) map[string]string { // the files that name an id, by path
			args := []string{"grep", "-l", "-z", "-I", "-w", "-F"}
			for id := range ids {
				args = append(args, "-e", id)
			}
			if ref != "" {
				args = append(args, ref)
			}
			var specs []string
			for _, p := range strings.Split(string(git(append(args, append([]string{"--"}, jsSpecs...)...)...)), "\x00") {
				if p = strings.TrimPrefix(p, ref+":"); p != "" && len(specs) < maxTableFiles {
					specs = append(specs, cmp.Or(ref, "HEAD")+":"+p)
				}
			}
			return catFiles(ctx, sb, specs)
		}
		base, head := in("refs/base"), in("")
		for i := range ts {
			d, ok := decls[ts[i].Name]
			if !ok {
				continue
			}
			mods := importers(base, d.file, d.id, "refs/base:")
			at := map[int][]string{} // the part -> files
			for _, p := range slices.Sorted(maps.Keys(mods.files)) {
				pi, ok := b.part[p]
				switch {
				case diff.Kind(p) == "test":
					ts[i].Tests++
				case ok:
					at[pi] = append(at[pi], p)
				default: // a deleted file: its folder's part
					if pj, ok := b.byFolder(folder(p, b.depth)); ok {
						at[pj] = append(at[pj], p)
					} else {
						ts[i].Elsewhere++
					}
				}
			}
			for _, pi := range slices.Sorted(maps.Keys(at)) {
				ts[i].Parts = append(ts[i].Parts, MapTablePart{b.m.Parts[pi].ID, at[pi]})
				b.m.Parts[pi].DB = true
			}
			for _, p := range slices.Sorted(maps.Keys(importers(head, d.file, d.id, "HEAD:", mods.mods).files)) {
				if diff.Kind(p) != "test" && len(ts[i].Imports) < maxNamed {
					ts[i].Imports = append(ts[i].Imports, p)
				}
			}
		}
	}
	sqlNames(git, ts, names)
	b.m.Tables = ts
}

// byFolder is the part that holds folder k, "Other" when it joins it.
func (b *mapper) byFolder(k string) (int, bool) {
	for i, p := range b.m.Parts {
		if p.Path == k || p.ID == "other" && b.other[k] {
			return i, true
		}
	}
	return 0, false
}

type importing struct {
	files map[string]bool // who imports the declaration
	mods  map[string]bool // the modules that hold it: its file, the barrels that re-export it
}

// importers is the files of texts (by "ref:path") that import id, declared in file, directly or through a module
// that re-exports it, a name in a multi-line import too. mods seeds the modules, else the declaring file's.
func importers(texts map[string]string, file, id, ref string, seed ...map[string]bool) importing {
	im := importing{files: map[string]bool{}, mods: map[string]bool{}}
	noExt := func(p string) string { return strings.TrimSuffix(p, path.Ext(p)) }
	im.mods[noExt(file)] = true
	for _, s := range seed {
		maps.Copy(im.mods, s)
	}
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(id) + `\b`)
	// resolves is whether spec, written in from, names a module of the set: an alias (~/, @/, #/) by its tail
	resolves := func(from, spec string) bool {
		spec = strings.TrimSuffix(strings.TrimSuffix(spec, ".js"), ".ts")
		if strings.HasPrefix(spec, ".") {
			spec = path.Join(path.Dir(from), spec)
			return im.mods[spec] || im.mods[spec+"/index"]
		}
		for _, p := range []string{"~/", "@/", "#/"} {
			spec = strings.TrimPrefix(spec, p)
		}
		for m := range im.mods {
			if m == spec || strings.HasSuffix(m, "/"+spec) || m == spec+"/index" || strings.HasSuffix(m, "/"+spec+"/index") {
				return true
			}
		}
		return false
	}
	for range 3 { // barrels of barrels
		grew := false
		for key, t := range texts {
			p := strings.TrimPrefix(key, ref)
			if noExt(p) == noExt(file) {
				continue
			}
			for _, m := range reexport.FindAllStringSubmatch(t, -1) {
				if m[1] == "export" && (word.MatchString(m[2]) || strings.Contains(m[2], "*")) && resolves(p, m[3]) && !im.mods[noExt(p)] {
					im.mods[noExt(p)], grew = true, true
				}
			}
		}
		if !grew {
			break
		}
	}
	for key, t := range texts {
		p := strings.TrimPrefix(key, ref)
		if noExt(p) == noExt(file) {
			continue
		}
		for _, m := range reexport.FindAllStringSubmatch(t, -1) {
			if (word.MatchString(m[2]) || m[1] == "export" && strings.Contains(m[2], "*")) && resolves(p, m[3]) {
				im.files[p] = true
			}
		}
	}
	return im
}

// catFiles reads the blobs of specs ("ref:path") with one git process, up to 64 MiB of them.
func catFiles(ctx context.Context, sb *proc.Sandbox, specs []string) map[string]string {
	out := map[string]string{}
	if len(specs) == 0 {
		return out
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := sb.Command(ctx, sb.Repo(), nil, "git", "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(strings.Join(specs, "\n") + "\n")
	pipe, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return out
	}
	r := bufio.NewReader(io.LimitReader(pipe, 64<<20))
	for _, spec := range specs {
		head, err := r.ReadString('\n')
		if err != nil {
			break
		}
		f := strings.Fields(head)
		if len(f) != 3 { // "<spec> missing"
			continue
		}
		n, _ := strconv.Atoi(f[2])
		blob := make([]byte, n+1) // and the newline after it
		if _, err := io.ReadFull(r, blob); err != nil {
			break
		}
		if n <= maxSize && !bytes.Contains(blob, []byte{0}) {
			out[spec] = string(blob[:n])
		}
	}
	cancel()
	_ = cmd.Wait()
	return out
}
