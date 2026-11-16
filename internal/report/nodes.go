package report

import (
	"cmp"
	"diffharbor/internal/checks"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"diffharbor/internal/review"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The page's budget for how a name connects (uses spec 9.3), and the stations a way below a route shows.
const (
	maxShapes, maxShapeTotal = 400, 250 << 10
	maxNodes, maxNodeTotal   = 600, 90 << 10
	maxDown                  = 3 // stations below a route, the table or system included
	maxReach                 = 3 // ends a declaration's "Reaches" draws
)

// useGroup is a line of a declaration that names a declaration: where it is (Path; In, the name the declaration
// has, "" a file's top level), the name, the line; what a front-end read of its declaration, and its shape (1-based,
// 0 none), which the groups of one declaration share.
type useGroup struct {
	Path, In, Name string
	Line           int
	// uses spec 9.3's order: 1 the PR's own, 2 a drawn way's joint, 3 in the PR's files, 4 the rest; 5 its lines
	// alone, no structure: a use in a test file (spec 7), a place past the refs kept
	rank, seq int
	got       *synUse
	shape     int
}

type useBook struct {
	groups map[string]*useGroup
	seq    int
}

// at is the group of line in p, in and name, ranked the best it is asked with; in a test file for its lines alone,
// however it is asked (spec 7: no structure for tests).
func (b *useBook) at(p, in, name string, line, rank int) *useGroup {
	if checks.IsTest(p) {
		rank = 5
	}
	k := p + "\x00" + in + "\x00" + name + "\x00" + strconv.Itoa(line)
	g := b.groups[k]
	if g == nil {
		b.seq++
		g = &useGroup{Path: p, In: in, Name: name, Line: line, rank: rank, seq: b.seq}
		b.groups[k] = g
	}
	g.rank = min(g.rank, rank)
	return g
}

// station is a station of the ways before the budget places it in Data.Nodes: up and down by key, the groups of the
// joints to them for their shapes.
type station struct {
	node       Node
	up, down   string
	jUp, jDown *useGroup
	rank       int
}

// upStation is a station of cgTests' walk up (upAll).
type upStation struct {
	N, Kind, M, P string
	L             int
	U             string
	JU            *struct {
		R, Via, P string
		L         int
	}
	E *End
}

// useStart is a declaration a use sits in, as cgTests' walk up starts from it.
type useStart struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Line int    `json:"line"`
}

// uses fills what uses spec 9.2 adds to the symbols: the stations of the ways up from each use to an entry and down
// from a route to its table or system (Data.Nodes, Ref.Up, Symbol.Reach), each use's code structure (Data.Shapes,
// Ref.Shape, Hop.Shape, Joint.Shape), a listing in an interface ("tie"), every line of a declaration that names one as
// a use, uses in test files marked: those get no structure and no way (spec 7). Within the page's budget in 9.3's
// order; over: the budget left a structure out. A pass that fails leaves its part empty: the index's alone.
func (idx *cgIndex) uses(sb *proc.Sandbox, root *os.Root, changed, added map[string]bool, syms map[string][]Symbol) (shapes []Shape, nodes []Node, over bool) {
	names := slices.Sorted(maps.Keys(syms))
	pr := func(s *Symbol) bool { return s.Exact && changed[s.Path] && addedIn(added, s.Path, s.From, s.To) }
	type use struct {
		s    *Symbol
		name string
		r    Ref
	}
	var all []use
	for _, name := range names {
		for i := range syms[name] {
			s := &syms[name][i]
			for j := range s.Refs {
				s.Refs[j].Test = checks.IsTest(s.Refs[j].Path)
				all = append(all, use{s, name, s.Refs[j]})
			}
		}
	}
	rank := func(s *Symbol, r Ref) int {
		switch {
		case r.Test:
			return 5
		case pr(s):
			return 1
		case changed[r.Path]:
			return 3
		}
		return 4
	}
	slices.SortStableFunc(all, func(a, b use) int { return cmp.Compare(rank(a.s, a.r), rank(b.s, b.r)) })

	// the ways up, from the declaration each use sits in
	w := &upWalk{at: map[useStart]int{}, sts: map[string]*station{}}
	var starts []useStart
	for _, u := range all {
		s := useStart{u.r.Path, u.r.In, u.r.Line}
		if _, ok := w.at[s]; !ok && u.s.Exact && !u.r.Test && !strings.Contains(u.r.In, " ") {
			w.at[s], starts = len(starts), append(starts, s)
		}
	}
	if len(starts) > 0 && (!runNode(sb, cgTests, map[string]any{"repo": sb.Repo(), "dbs": idx.dbs, "extra": idx.added(), "ups": starts}, w) || len(w.Starts) != len(starts)) {
		w.Stations, w.Starts = nil, nil
	}
	book := &useBook{groups: map[string]*useGroup{}}
	for _, u := range all { // the uses' own lines first, then the joints of their ways (uses spec 9.3)
		if !u.r.Test {
			w.add(u.s, u.r, rank(u.s, u.r))
		}
		if u.s.Exact {
			book.at(u.r.Path, u.r.In, u.name, u.r.Line, rank(u.s, u.r))
		}
	}
	sts := w.sts
	for _, k := range slices.Sorted(maps.Keys(sts)) { // the joints up, ranked as the uses that reach them
		if st := sts[k]; st.node.JUp != nil && st.up != "" && st.node.JUp.Line > 0 {
			st.jUp = book.at(st.node.JUp.Path, sts[st.up].node.Name, st.node.Name, st.node.JUp.Line, st.rank)
		}
	}
	lines := sources(root)
	idx.down(sb, sts, book, lines)
	reach := map[*Symbol][]string{}
	for _, name := range names {
		for i := range syms[name] {
			if s := &syms[name][i]; pr(s) && !checks.IsTest(s.Path) { // no way for a test's helper (spec 7)
				reach[s] = idx.reach(s, name, sts, book, lines)
			}
		}
	}

	// the front-ends: each use's declaration, its lines and ways
	eachHop(syms, names, func(h *Hop, in string) { book.at(h.In, in, h.Name, h.At, 2) })
	book.past(syms, names)
	order := slices.SortedFunc(maps.Values(book.groups), func(a, b *useGroup) int {
		return cmp.Or(cmp.Compare(a.rank, b.rank), cmp.Compare(a.seq, b.seq))
	})
	idx.read(sb, root, order)

	shapes, over = placeShapes(order)
	eachHop(syms, names, func(h *Hop, in string) { h.Shape = book.at(h.In, in, h.Name, h.At, 2).shape })
	ups := map[*Symbol]map[useStart]string{} // each use's station, by its declaration and line
	for _, name := range names {
		for i := range syms[name] {
			s := &syms[name][i]
			ups[s] = book.count(s, name, lines, w.key)
		}
	}
	nodes, place := placeNodes(sts)
	for s, ks := range reach {
		for _, k := range ks {
			if place[k] > 0 {
				s.Reach = append(s.Reach, place[k])
			}
		}
	}
	for _, name := range names {
		for i := range syms[name] {
			s := &syms[name][i]
			for j := range s.Refs {
				r := &s.Refs[j]
				r.Up = place[ups[s][useStart{r.Path, r.In, r.Line}]]
			}
		}
	}
	return shapes, nodes, over
}

// upWalk is the walk up (cgTests with "ups"): the stations by key, the key of each start, by its place in at; and
// the stations the uses reach, before the budget.
type upWalk struct {
	Stations map[string]upStation
	Starts   []string
	at       map[useStart]int
	sts      map[string]*station
}

// get is the station k and the way up from it, from the walk, ranked as the best use that reaches it; nil none.
func (w *upWalk) get(k string, rank int) *station {
	if st := w.sts[k]; st != nil {
		st.rank = min(st.rank, rank)
		if up := w.sts[st.up]; up != nil && up.rank > rank {
			w.get(st.up, rank)
		}
		return st
	}
	u, ok := w.Stations[k]
	if !ok {
		return nil
	}
	st := &station{node: Node{Name: u.N, Kind: u.Kind, Method: u.M, Path: u.P, Line: u.L, End: u.E}, rank: rank}
	w.sts[k] = st
	if up := w.get(u.U, rank); up != nil && u.JU != nil {
		st.up = u.U
		st.node.JUp = &Joint{Rel: u.JU.R, Via: u.JU.Via, Path: u.JU.P, Line: u.JU.L}
	}
	return st
}

// key is the station of the declaration r sits in: by the walk; a route the checker names it in; one of a name found
// by name, which goes no further. "" none.
func (w *upWalk) key(s *Symbol, r Ref) string {
	switch {
	case !s.Exact && r.In != "":
		return "byname\x00" + r.Path + "\x00" + r.In
	case !s.Exact:
		return ""
	case strings.Contains(r.In, " "):
		m, n, _ := strings.Cut(r.In, " ")
		return "route\x00" + r.Path + "\x00" + strings.ToUpper(m) + " " + n
	case w.Starts != nil:
		if i, ok := w.at[useStart{r.Path, r.In, r.Line}]; ok {
			return w.Starts[i]
		}
	}
	return ""
}

// add is the station of r's declaration and the way up from it: the walk's; a route or a name found by name, made
// here.
func (w *upWalk) add(s *Symbol, r Ref, rank int) {
	k := w.key(s, r)
	switch {
	case k == "":
	case !s.Exact:
		if w.sts[k] == nil {
			w.sts[k] = &station{node: Node{Name: r.In, Kind: "decl", Path: r.Path, End: &End{Kind: "byname"}}, rank: rank}
		}
	case strings.HasPrefix(k, "route\x00") && w.Stations[k].N == "":
		if w.sts[k] == nil {
			m, n, _ := strings.Cut(r.In, " ")
			w.sts[k] = &station{node: Node{Name: n, Kind: "route", Method: strings.ToUpper(m), Path: r.Path, Line: r.Line, End: &End{Kind: "route"}}, rank: rank}
		}
	default:
		w.get(k, rank)
	}
}

// eachHop calls f with the joints of the drawn ways (the traced path, call paths to a finding) and the name of the
// station each joint is in.
func eachHop(syms map[string][]Symbol, names []string, f func(h *Hop, in string)) {
	for _, name := range names {
		for _, s := range syms[name] {
			for _, w := range s.Ways {
				for i := range w.Chain {
					if h := &w.Chain[i]; h.At > 0 && h.In != "" {
						in := ""
						if i > 0 {
							in = w.Chain[i-1].Name
						}
						f(h, in)
					}
				}
			}
		}
	}
}

// past adds a group, for its lines alone, of each declaration past the refs the symbols keep.
func (b *useBook) past(syms map[string][]Symbol, names []string) {
	for _, name := range names {
		for i := range syms[name] {
			for _, r := range pastDecls(&syms[name][i]) {
				b.at(r.Path, r.In, name, r.Line, 5)
			}
		}
	}
}

// pastDecls is a place of each declaration past the refs s keeps (a file's top level: each place), whose other lines
// count as uses.
func pastDecls(s *Symbol) []Ref {
	var out []Ref
	seen := map[string]bool{}
	for _, r := range s.past {
		if k := r.Path + "\x00" + r.In; r.In == "" || !seen[k] {
			seen[k], out = true, append(out, r)
		}
	}
	return out
}

// placeShapes is the groups' shapes, in order within the budget (uses spec 9.3), each group's set; the groups of one
// declaration share one. over: one didn't fit.
func placeShapes(order []*useGroup) (shapes []Shape, over bool) {
	total, done := 0, map[string]int{} // a declaration's shape, by path, span and name
	for _, g := range order {
		if g.got == nil || len(g.got.Ways) == 0 {
			continue
		}
		k := fmt.Sprint(g.Path, g.got.Span, g.Name)
		if i, ok := done[k]; ok {
			g.shape = i
			continue
		}
		done[k] = 0
		sh, ok := shape(g.Path, g.Name, g.got.Span, g.got.Ways)
		if !ok {
			continue
		}
		b, _ := json.Marshal(sh)
		if len(shapes) >= maxShapes || total+len(b) > maxShapeTotal {
			over = true
			continue
		}
		total += len(b)
		shapes = append(shapes, sh)
		g.shape, done[k] = len(shapes), len(shapes)
	}
	return shapes, over
}

// count gives s's uses their shapes and a listing in an interface "tie", and adds every other line of an exact use's
// declaration that names name as a use of its own (decision 28), to maxRefs; those of the declarations past the refs
// kept count. A use in a test file gets no shape and no station. Out: each use's station, by upKey.
func (b *useBook) count(s *Symbol, name string, lines files, upKey func(*Symbol, Ref) string) map[useStart]string {
	ups := map[useStart]string{}
	have := map[string]bool{s.Path + ":" + strconv.Itoa(s.Line): true}
	for _, r := range slices.Concat(s.Refs, s.Outside, s.past) {
		have[r.Path+":"+strconv.Itoa(r.Line)] = true
	}
	for _, h := range s.Here {
		have[h] = true
	}
	var refs []Ref
	for _, r := range s.Refs {
		if !r.Test {
			ups[useStart{r.Path, r.In, r.Line}] = upKey(s, r)
		}
		if !s.Exact {
			refs = append(refs, r)
			continue
		}
		g := b.at(r.Path, r.In, name, r.Line, 4)
		if g.got != nil && g.got.Iface != "" {
			r.Rel, r.Via = "tie", g.got.Iface
		}
		r.Shape = g.shape // none in a test file: see at
		refs = append(refs, r)
		for _, l := range linesOf(g) {
			if at := r.Path + ":" + strconv.Itoa(l); !have[at] {
				have[at] = true
				x := r
				x.Line, x.Text, x.Rel, x.Via = l, cut(strings.TrimSpace(lines.text(r.Path, l)), maxText), "", ""
				refs, s.Uses = append(refs, x), s.Uses+1
				if !r.Test {
					ups[useStart{x.Path, x.In, x.Line}] = upKey(s, r)
				}
			}
		}
	}
	for _, r := range pastDecls(s) {
		for _, l := range linesOf(b.at(r.Path, r.In, name, r.Line, 5)) {
			if at := r.Path + ":" + strconv.Itoa(l); !have[at] {
				have[at], s.Uses = true, s.Uses+1
			}
		}
	}
	s.Refs = refs[:min(len(refs), maxRefs)]
	return ups
}

// placeNodes is the stations within the budget, in the order their uses rank, linked up and down by place (1-based)
// with their joints' shapes; a link past the budget ends its way "nograph". place: each station's.
func placeNodes(sts map[string]*station) (nodes []Node, place map[string]int) {
	keys := slices.SortedFunc(maps.Keys(sts), func(a, b string) int { return cmp.Or(cmp.Compare(sts[a].rank, sts[b].rank), cmp.Compare(a, b)) })
	place, size := map[string]int{}, 0
	for _, k := range keys {
		b, _ := json.Marshal(sts[k].node)
		if len(nodes) >= maxNodes || size+len(b)+16 > maxNodeTotal { // 16: the indexes and shapes set below
			break
		}
		size += len(b) + 16
		nodes = append(nodes, sts[k].node)
		place[k] = len(nodes)
	}
	for k, i := range place {
		st, n := sts[k], &nodes[i-1]
		link := func(to string, g *useGroup, j *Joint) (int, *Joint) {
			if j != nil && g != nil {
				j.Shape = g.shape
			}
			switch {
			case to == "":
				return 0, j // a Reach's first station: its joint from the declaration
			case place[to] == 0: // past the budget: the way ends here
				n.End = &End{Kind: "nograph", Text: n.Path}
				return 0, nil
			}
			return place[to], j
		}
		n.Up, n.JUp = link(st.up, st.jUp, n.JUp)
		n.Down, n.JDown = link(st.down, st.jDown, n.JDown)
	}
	return nodes, place
}

// linesOf are the lines of g's declaration that name its name, none when no front-end read it.
func linesOf(g *useGroup) []int {
	if g.got == nil {
		return nil
	}
	return g.got.Lines
}

// read asks the front-ends for each group: Go's here, TypeScript's through tsedges.cjs --shapes in the project of its
// index; the ways for the first 2·maxShapes in order (a structure of one row is none), the lines alone for the rest
// and for those ranked for their lines alone.
func (idx *cgIndex) read(sb *proc.Sandbox, root *os.Root, order []*useGroup) {
	type item struct {
		File string `json:"file"`
		Line int    `json:"line"`
		Name string `json:"name"`
		In   string `json:"in"`
		Ways bool   `json:"ways"`
	}
	gos := map[string]*goFile{}
	ts := map[string][]int{} // an index's prefix: its groups, by place in order
	tsOK := false
	if _, err := os.Stat(filepath.Join(tsAPI, "dist", "api", "sync", "api.js")); err == nil {
		tsOK = true
	}
	ways := func(i int) bool { return i < 2*maxShapes && order[i].rank < 5 }
	for i, g := range order {
		switch ext := path.Ext(g.Path); {
		case ext == ".go":
			gf, ok := gos[g.Path]
			if !ok {
				if src, err := diff.ReadIn(root, filepath.FromSlash(g.Path), maxSize); err == nil {
					gf = parseGo(src)
				}
				gos[g.Path] = gf
			}
			if gf == nil {
				continue
			}
			if u, ok := goShape(gf, g.Line, g.Name, ways(i)); ok {
				g.got = &u
			}
		case jsExt[ext] && tsOK:
			best := -1
			for j, d := range idx.dbs {
				if strings.HasPrefix(g.Path, d.Prefix) && (best < 0 || len(d.Prefix) > len(idx.dbs[best].Prefix)) {
					best = j
				}
			}
			if best >= 0 {
				ts[idx.dbs[best].Prefix] = append(ts[idx.dbs[best].Prefix], i)
			}
		}
	}
	for prefix, is := range ts {
		dir := filepath.Join(sb.Repo(), filepath.FromSlash(prefix))
		if _, err := os.Stat(filepath.Join(dir, "tsconfig.json")); err != nil {
			continue
		}
		items := make([]item, len(is))
		for j, i := range is {
			g := order[i]
			items[j] = item{strings.TrimPrefix(g.Path, prefix), g.Line, g.Name, g.In, ways(i)}
		}
		var got []*synUse
		if !runNode(sb, tsEdgesJS, map[string]any{"dir": dir, "ts": tsAPI, "shapes": items}, &got) || len(got) != len(is) {
			continue
		}
		for j, i := range is {
			order[i].got = got[j]
		}
	}
}

// down is the way down from each route the ways reach (uses spec 9.2): its handler's first call that passes its
// input on (a named handler is a station of its own, then its call) and, within maxHops calls of that one, reaches a
// table or a system, to the first it reaches; at most maxDown stations, the table or system included, then "bound".
func (idx *cgIndex) down(sb *proc.Sandbox, sts map[string]*station, book *useBook, lines files) {
	byLine := func(a, b cgEdge) int { return cmp.Compare(a.Line, b.Line) }
	inner := map[declKey][]cgEdge{} // a named handler's calls that pass its input on
	for _, e := range idx.Edges {
		if e.Kind == "passes" && e.SKind != "route" {
			inner[declKey{e.SPath, e.SName}] = append(inner[declKey{e.SPath, e.SName}], e)
		}
	}
	for _, es := range inner {
		slices.SortFunc(es, byLine)
	}
	passes := map[string][]cgEdge{} // a route's key: its calls that pass its input on
	var targets []declKey
	for _, k := range slices.Sorted(maps.Keys(sts)) {
		st := sts[k]
		if st.node.Kind != "route" {
			continue
		}
		for _, e := range idx.Edges {
			if e.Kind == "passes" && e.SKind == "route" && e.SPath == st.node.Path && strings.EqualFold(e.SName, st.node.Method+" "+st.node.Name) {
				passes[k] = append(passes[k], e)
				targets = append(targets, declKey{e.TPath, e.TName})
				for _, h := range inner[declKey{e.TPath, e.TName}] {
					targets = append(targets, declKey{h.TPath, h.TName})
				}
			}
		}
		slices.SortFunc(passes[k], byLine)
	}
	if len(targets) == 0 {
		return
	}
	g := idx.callGraph(sb, targets)
	step := func(e cgEdge, in string) downStep {
		return downStep{
			key: e.TPath + "\x00" + e.TName + "\x00" + strconv.Itoa(e.TLine), node: Node{Name: e.TName, Kind: "decl", Path: e.TPath, Line: e.TLine},
			joint: Joint{Rel: "call", Path: e.SPath, Line: e.Line}, in: in,
		}
	}
	for _, k := range slices.Sorted(maps.Keys(passes)) {
		r := sts[k]
		var ways [][]downStep // the stations to the call each way goes down from
		for _, e := range passes[k] {
			first := step(e, r.node.Method+" "+r.node.Name)
			if hs := inner[declKey{e.TPath, e.TName}]; len(hs) > 0 {
				for _, h := range hs {
					ways = append(ways, []downStep{first, step(h, e.TName)})
				}
				continue
			}
			ways = append(ways, []downStep{first})
		}
		for _, pre := range ways {
			last := pre[len(pre)-1].node
			x := declKey{last.Path, last.Name}
			steps := chainDown(g, x, firstEnd(g, x), lines)
			if len(steps) == 0 {
				continue
			}
			steps = append(pre, steps...)
			if len(steps) > maxDown {
				steps = steps[:maxDown]
				steps[maxDown-1].node.End = &End{Kind: "bound"}
			}
			link(sts, k, steps, book, r.rank)
			r.node.End = nil // it goes on down
			break
		}
	}
}

// reach is the stations below s toward each table and system it reaches, at most maxReach (Symbol.Reach): its own,
// not shared, as the joint into the first is from s. Their keys, the first of each.
func (idx *cgIndex) reach(s *Symbol, name string, sts map[string]*station, book *useBook, lines files) []string {
	from := declKey{s.Path, name[strings.LastIndex(name, ".")+1:]} // the declaration's own name, as its index has it
	var out []string
	seen := map[string]bool{}
	ends := make([]any, 0, len(s.Stores)+len(s.Requests))
	for i := range s.Stores {
		ends = append(ends, &s.Stores[i])
	}
	for i := range s.Requests {
		ends = append(ends, &s.Requests[i])
	}
	for _, e := range ends {
		steps := chainDown(idx.calls, from, e, lines)
		if len(steps) == 0 || seen[steps[len(steps)-1].key] || len(out) >= maxReach {
			continue
		}
		seen[steps[len(steps)-1].key] = true
		pre := "reach\x00" + s.Path + "\x00" + name + "\x00" + strconv.Itoa(len(out)) + "\x00"
		for i := range steps {
			steps[i].key = pre + steps[i].key
		}
		sts[steps[0].key] = &station{node: steps[0].node, rank: 1}
		st := sts[steps[0].key]
		st.node.JUp = &steps[0].joint
		if steps[0].named() {
			st.jUp = book.at(steps[0].joint.Path, steps[0].in, steps[0].node.Name, steps[0].joint.Line, 1)
		}
		link(sts, steps[0].key, steps[1:], book, 1)
		out = append(out, steps[0].key)
	}
	return out
}

// downStep is a station on a way down and the joint into it from the one above (in, by name).
type downStep struct {
	key   string
	node  Node
	joint Joint
	in    string
}

// named is whether the joint names the station by a name a structure can mark: a declaration, a table (a string
// that names it, SQL's, is marked when no identifier does); not a system.
func (d downStep) named() bool {
	return d.node.Kind != "system"
}

// firstEnd is the first table, else the first system, x reaches: a *StorePath, a *OutsideCall, nil.
func firstEnd(g map[declKey][]cgEdge, x declKey) any {
	if sp := storesFrom(g, x); len(sp) > 0 {
		return &sp[0]
	}
	if rq := sendersFrom(g, x); len(rq) > 0 {
		return &rq[0]
	}
	return nil
}

// tableAt is the first line from from on that passes table as a call's argument, as .from(t) or .insert(t); 0 for none.
func tableAt(lines files, p, table string, from int) int {
	for i := from; i <= from+maxSigLines; i++ {
		if t := lines.text(p, i); strings.Contains(t, "("+table+")") || strings.Contains(t, "("+table+",") {
			return i
		}
	}
	return 0
}

// chainDown is the stations from start (not included) down end's calls to its table or system, each with its joint;
// none for a nil end, or a call the graph doesn't place. A read's or write's place is the line that names the table,
// not its statement's first.
func chainDown(g map[declKey][]cgEdge, start declKey, end any, lines files) []downStep {
	var calls []StoreCall
	var last downStep
	switch e := end.(type) {
	case *StorePath:
		calls = e.Calls
		kind, rel := "table", map[string]string{"reads": "read", "writes": "write"}[e.Kind]
		if e.Sub {
			kind = "subquery"
		}
		p, l := splitAt(e.At)
		l = cmp.Or(tableAt(lines, p, e.Table, l), lines.naming(p, e.Table, l, l+maxSigLines), l)
		last = downStep{
			key: kind + "\x00" + e.TPath + "\x00" + e.Table, node: Node{Name: e.Table, Kind: kind, Path: e.TPath, Line: e.TLine, End: &End{Kind: kind}},
			joint: Joint{Rel: rel, Path: p, Line: l},
		}
	case *OutsideCall:
		if len(e.Calls) == 0 {
			return nil
		}
		calls = e.Calls[:len(e.Calls)-1]
		p, l := splitAt(e.Calls[len(e.Calls)-1].At)
		up, ul := splitAt(e.URLAt)
		last = downStep{
			key: "system\x00" + e.System, node: Node{Name: e.System, Kind: "system", Path: up, Line: ul, End: &End{Kind: "system"}},
			joint: Joint{Rel: "request", Path: p, Line: l},
		}
	default:
		return nil
	}
	var out []downStep
	cur, in := start, start.Name
	for _, c := range calls {
		p, l := splitAt(c.At)
		i := slices.IndexFunc(g[cur], func(e cgEdge) bool { return e.Kind == "calls" && e.TName == c.Callee && cmp.Or(e.Line, e.SLine) == l })
		if i < 0 {
			return nil
		}
		e := g[cur][i]
		out = append(out, downStep{
			key: e.TPath + "\x00" + e.TName + "\x00" + strconv.Itoa(e.TLine), node: Node{Name: e.TName, Kind: "decl", Path: e.TPath, Line: e.TLine},
			joint: Joint{Rel: "call", Path: p, Line: l}, in: in,
		})
		cur, in = declKey{e.TPath, e.TName}, e.TName
	}
	last.in = in
	return append(out, last)
}

// link hangs steps below the station at k, each the next one down, as far as a station has no way down of its own.
func link(sts map[string]*station, k string, steps []downStep, book *useBook, rank int) {
	above := sts[k]
	for _, s := range steps {
		if above.down != "" {
			return
		}
		st := sts[s.key]
		if st == nil {
			st = &station{node: s.node, rank: rank}
			sts[s.key] = st
		}
		above.down, above.node.JDown = s.key, &s.joint
		if s.named() && s.joint.Line > 0 {
			above.jDown = book.at(s.joint.Path, s.in, s.node.Name, s.joint.Line, rank)
		}
		st.rank = min(st.rank, rank)
		above = st
	}
}

// splitAt is "path:line" apart.
func splitAt(at string) (string, int) {
	i := strings.LastIndexByte(at, ':')
	if i < 0 {
		return at, 0
	}
	n, _ := strconv.Atoi(at[i+1:])
	return at[:i], n
}

// stations says on each station of the ways what the PR did to it, as the popover says it of a declaration: "new",
// "changed", "signature", "outside" for one in a file the PR doesn't change; the heaviest blocking finding on it and
// its card; the map's part it sits in.
func stations(v *view, chs []changed) {
	parts := map[string]string{}
	if v.Map != nil {
		for _, p := range v.Map.Parts {
			for _, f := range p.Files {
				parts[f] = p.Label
			}
		}
	}
	for i := range v.Nodes {
		n := &v.Nodes[i]
		if n.Kind == "table" || n.Kind == "subquery" || n.Kind == "system" {
			continue
		}
		n.Part = parts[n.Path]
		if v.files[n.Path] == nil {
			n.State = "outside"
			continue
		}
		for _, c := range chs {
			if c.f.Path != n.Path || c.c.State == "gone" || c.c.Name[strings.LastIndex(c.c.Name, ".")+1:] != n.Name {
				continue
			}
			if st := c.c.said(); stateRank[st] > stateRank[n.State] {
				n.State = st
			}
			if heavier(c.c.Sev, n.Sev) && review.Blocks(c.c.Sev) {
				n.Sev, n.Find = c.c.Sev, findingOf(c.f, c.c.Sev)
			}
		}
	}
}
