package report

import (
	"cmp"
	"diffharbor/internal/diff"
	"maps"
	"math"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The map as the page draws it (spec 6.3–6.13, components 23–26): where each box sits, how each line runs, what a
// pointer, a hover and a selection show. Map holds the facts; this places and words them.

// mapView is the map frame's content.
type mapView struct {
	src           *Map
	W, H          int
	Fig           []*mbox // the parts with a line and the systems, placed
	Lines         []*mroute
	Alone         []*mbox // the row "No line to other parts"
	Removed, Gen  *mbox
	Writes, Reads []*mbox // the table rows (6.13)
	Deploy        []*mbox
	NoGraph       bool
	Sentence      string
	Keys          []mkey // the glyphs the key row shows, in its order
	OnMap         int    // open items on a box
	Off           []offLink
	Reach         string // the reach line
	ReachList     []reachPart
	ReachMore     int
	Tests         int
	Pairs         []string
	Trace         *traceView
	List          []*mbox // the phone's list, in map order
	byKey         map[string]*mbox
	partBox       map[string]*mbox // a part's ID -> its box
}

// mkey is a glyph of the legend's key row. The git-commit mark's is the script's: it knows the Since base.
type mkey struct{ K string }

// mbox is a box of the map (component 24). Kind: "part", "system", "deploy", "table", "removed", "generated".
type mbox struct {
	El, Key, Kind, Label, Path string
	X, Y, W                    int
	Lines                      []string // a system's name on up to three lines
	Open, Files, Reach         int
	New, DB, Eye, Hide         bool
	Pos                        bool   // placed in the figure
	Small                      bool   // the label in 12
	Line2                      string // a deploy, table, removed or generated box's text after its name
	Aria                       string
	Nb, Ln                     []string // the boxes and lines its hover lights
	Sel                        []string // the boxes a selection keeps: its parts for a table
	Says                       string   // the phone's line 2: its lines in words
	Panel                      *panel
	part                       *MapPart
}

// mroute is a line of the figure (component 25): its path, chevrons and pointer label.
type mroute struct {
	El, D, Chev, Style, Tip string
	// Callee is the box whose code the line's calls reach, Caller the box they come from (their element ids); Rel is
	// "calls", "named" (no call) or "both" (each calls the other), N the calls. A selection says on each box it lights
	// how that box touches it.
	Callee, Caller, Rel string
	N                   int
	Width, ChevW        float64
	a, b                *mbox
	pts                 [][2]float64
}

// offLink is an item off the map, as the legend's items line names it.
type offLink struct{ Text, Href string }

// reachPart is the reach list's declarations of one part.
type reachPart struct {
	Label string
	Decls []MapReachDecl
}

// traceView is the traced path under the map (6.12, component 30).
type traceView struct {
	Label        string
	Path         *pathView
	Foot         string
	Boxes, Lines []string
}

// panel is the selection panel of a box (component 26).
type panel struct {
	Head, Path, Count, Read, ReadHref string
	Kind, Key                         string // its box's
	Code                              bool   // the head is a table's name
	Rows                              []prow2
	Conn                              []connRow // a part's How it connects: who calls it, what it calls, its tables
	More                              int
	Subs                              []psub
}

// prow2 is a row of a selection panel: a link to an item card or a part block.
type prow2 struct{ Kind, Title, Place, Href string }

// psub is a sub-head of a panel with its chip and lines.
type psub struct {
	Head  string
	Chip  chip
	Lines []string
}

// figure geometry (6.3)
const (
	colStep, boxW, sysW, boxH, rowStep, rowTop = 176, 136, 96, 52, 62, 16
	maxTableBoxes                              = 12
)

// mapView builds the map frame from Data.Map; nil when there is none (6.10).
//
//nolint:gocognit // the map step by step: boxes, rows, routes, words
func (v *view) mapView() {
	m := v.Map
	if m == nil || len(m.Parts) < 1 {
		return
	}
	mv := &mapView{src: m, NoGraph: m.NoGraph, byKey: map[string]*mbox{}, partBox: map[string]*mbox{}}
	v.Page.Map = mv
	n := 0
	el := func() string { n++; return "b" + strconv.Itoa(n) }
	unread := map[string]bool{}
	for _, p := range slices.Concat(v.Omitted, v.Failed) {
		unread[p] = true
	}
	for i := range m.Parts {
		p := &m.Parts[i]
		b := &mbox{El: el(), Key: "part:" + p.ID, Kind: "part", Label: p.Label, Path: p.Path, Files: len(p.Files), Reach: p.Reach, New: p.New, DB: p.DB, Eye: p.Unread > 0, part: p}
		b.Small = len(p.Label) > 15
		for _, f := range p.Files {
			b.Eye = b.Eye || unread[f]
		}
		mv.add(b)
		mv.partBox[p.ID] = b
	}
	for _, s := range m.Systems {
		b := &mbox{El: el(), Key: "system:" + s.Name, Kind: "system", Label: s.Name, Lines: sysLines(s.Name, 3, 13)}
		mv.add(b)
		mv.Fig = append(mv.Fig, b)
		b.X, b.Y, b.W, b.Pos = 0, rowTop+rowStep*s.Row, sysW, true
	}
	sys := len(m.Systems) > 0
	for _, id := range m.Order {
		b := mv.partBox[id]
		if b.part.Alone || m.NoGraph {
			mv.Alone = append(mv.Alone, b)
			continue
		}
		b.X, b.Y, b.W, b.Pos = colX(b.part.Col, sys), rowTop+rowStep*b.part.Row, boxW, true
		mv.Fig = append(mv.Fig, b)
	}
	if r := m.Removed; r != nil {
		mv.Removed = &mbox{El: el(), Key: "removed", Kind: "removed", Label: "Removed", Line2: plural(r.N, "file", "files") + " in " + plural(len(r.Folders), "folder", "folders")}
		mv.add(mv.Removed)
	}
	if g := m.Generated; g != nil {
		mv.Gen = &mbox{El: el(), Key: "generated", Kind: "generated", Label: "Generated", Line2: plural(g.N, "file", "files")}
		mv.add(mv.Gen)
	}
	// the deploy row: its open items are the before-merge cards and major findings on it, counted with the cards
	for _, d := range m.Deploy {
		key := "deploy:" + d.Kind
		if d.Kind == "migration" {
			key = "deploy:" + d.Path
		}
		b := &mbox{El: el(), Key: key, Kind: "deploy", Label: d.Label, Path: d.Path}
		mv.add(b)
		mv.Deploy = append(mv.Deploy, b)
	}
	// the tables (6.13): writers first, then by parts, then by name
	for _, s := range m.Stores {
		b := &mbox{El: el(), Key: "table:" + s.Name, Kind: "table", Label: s.Name}
		var w, r []string
		for _, sp := range s.Parts {
			pb := mv.partBox[sp.Part]
			if pb == nil {
				continue
			}
			b.Sel = append(b.Sel, pb.El)
			for _, u := range sp.Uses {
				if u.Kind == "writes" && !slices.Contains(w, pb.Label) {
					w = append(w, pb.Label)
				}
				if u.Kind == "reads" && !slices.Contains(r, pb.Label) {
					r = append(r, pb.Label)
				}
			}
		}
		var t []string
		if len(w) > 0 {
			t = append(t, "written by "+upTo(w, 4))
		}
		if len(r) > 0 {
			t = append(t, "read by "+upTo(r, 4))
		}
		for _, mg := range s.Migrations {
			t = append(t, mg.Kind+" by "+strings.ToLower(mg.Label))
			if d := mv.byKey["deploy:"+mg.Path]; d != nil && !slices.Contains(b.Sel, d.El) {
				b.Sel = append(b.Sel, d.El) // S57: a table keeps the items on its migrations' boxes
			}
		}
		b.Line2 = strings.Join(t, " · ")
		b.Aria = s.Name
		if s.Subquery {
			b.Aria += ", a subquery"
		}
		if len(t) > 0 {
			b.Aria += ", " + strings.Join(t, ", ")
		}
		b.Says = sentenceCase(strings.Join(t, ". ")) + "."
		if s.Subquery {
			b.Says = "A subquery. " + b.Says
		}
		mv.add(b)
		if len(w) > 0 {
			mv.Writes = append(mv.Writes, b)
		} else {
			mv.Reads = append(mv.Reads, b)
		}
	}
	for _, row := range [][]*mbox{mv.Writes, mv.Reads} {
		for i, b := range row {
			b.Hide = i >= maxTableBoxes
		}
	}
	mv.route(m, sys) // words come once items has counted each box's open items
}

func (mv *mapView) add(b *mbox) { mv.byKey[b.Key] = b }

// colX is a column's left edge: systems at 0, the parts' columns 176 apart from 136, or from 0 without systems.
func colX(c int, sys bool) int {
	if !sys {
		return (c - 1) * colStep
	}
	if c == 0 {
		return 0
	}
	return boxW + (c-1)*colStep
}

func upTo(names []string, n int) string {
	if len(names) > n {
		return strconv.Itoa(len(names)) + " parts"
	}
	return join(names)
}

func sentenceCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

var sysBreak = regexp.MustCompile(`[._]`)

// sysLines is a system's name on up to keep lines of at most width characters, broken before "." or "_": the fewest
// lines, then the most even; a line still longer is cut with "…" (6.4).
func sysLines(name string, keep, width int) []string {
	var toks []string
	last := 0
	for _, ix := range sysBreak.FindAllStringIndex(name, -1) {
		if ix[0] > last {
			toks, last = append(toks, name[last:ix[0]]), ix[0]
		}
	}
	toks = append(toks, name[last:])
	var best []string
	bestKey := [2]int{math.MaxInt, math.MaxInt}
	var try func(at int, lines []string)
	try = func(at int, lines []string) {
		if len(lines) > keep {
			return
		}
		if at == len(toks) {
			lo, hi := math.MaxInt, 0
			for _, l := range lines {
				lo, hi = min(lo, len(l)), max(hi, len(l))
			}
			if k := [2]int{len(lines), hi - lo}; k[0] < bestKey[0] || k[0] == bestKey[0] && k[1] < bestKey[1] {
				best, bestKey = slices.Clone(lines), k
			}
			return
		}
		for j := at + 1; j <= len(toks); j++ {
			l := strings.Join(toks[at:j], "")
			if len(l) > width { // a longer line only grows: a long name stays quick
				break
			}
			try(j, append(lines, l))
		}
	}
	try(0, nil)
	if best != nil {
		return best
	}
	if len(toks) > keep {
		toks = append(toks[:keep-1:keep-1], strings.Join(toks[keep-1:], ""))
	}
	for i, t := range toks {
		if len(t) > width {
			toks[i] = t[:width-1] + "…"
		}
	}
	return toks
}

// wire is a line to route: a is its left end, b its right one.
type wire struct {
	r              *mroute
	a, b           *mbox
	kind           string // "same", "adj", "row", "top"
	lane           int    // "row": the row it runs along
	ya, yb, ly, ty float64
	tx1, tx2       float64
}

// route places the lines (6.3): ports 8 apart centred on an edge, tracks in the channels between columns, a row
// with no box in between or the top channel for a line across columns.
//
//nolint:gocognit,gocyclo // each line's route by its kind, then the channels, each a few lines
func (mv *mapView) route(m *Map, sys bool) {
	if m.NoGraph {
		return
	}
	at := map[[2]int]bool{} // a box at column, row
	col := func(b *mbox) int {
		if b.Kind == "system" {
			return 0
		}
		return b.part.Col
	}
	row := func(b *mbox) int { return (b.Y - rowTop) / rowStep }
	maxY, maxRow := 0, 0
	for _, b := range mv.Fig {
		at[[2]int{col(b), row(b)}] = true
		maxRow = max(maxRow, row(b))
		maxY = max(maxY, b.Y+boxH)
		mv.W = max(mv.W, b.X+b.W)
	}
	mv.H = maxY + 8 // the figure is as wide as its boxes: from 1,600 the selection panel sits beside it
	var es []*wire
	n := 0
	for i := range m.Lines {
		l := &m.Lines[i]
		from, to := mv.partBox[l.From], mv.partBox[l.To]
		if l.Style == "" || from == nil || to == nil || from.part.Alone || to.part.Alone {
			continue
		}
		n++
		r := &mroute{El: "l" + strconv.Itoa(n), Style: l.Style, Width: float64(max(l.Width, 1)), Tip: tipOf(l, m.NoKinds)}
		if l.Style == "dashed" {
			r.Width = 1
		}
		r.Callee, r.Caller, r.Rel, r.N = from.El, to.El, "calls", l.Calls+l.Through
		switch {
		case l.TwoWay:
			r.Rel = "both"
		case r.N == 0:
			r.Rel = "named"
		}
		a, b := from, to
		if col(to) < col(from) {
			a, b = to, from
		}
		switch l.Chevron { // where data goes: at To, at From, or both
		case "to":
			r.Chev = map[bool]string{true: "b", false: "a"}[b == to]
		case "from":
			r.Chev = map[bool]string{true: "b", false: "a"}[b == from]
		case "both":
			r.Chev = "both"
		}
		r.a, r.b = a, b
		es = append(es, &wire{r: r, a: a, b: b})
	}
	for _, s := range m.Systems {
		sb := mv.byKey["system:"+s.Name]
		for _, p := range s.Parts {
			pb := mv.partBox[p]
			if pb == nil || pb.part.Alone {
				continue
			}
			n++
			tip := s.Name
			for _, c := range s.Calls {
				if slices.Contains(pb.part.Files, c.Path) {
					tip += " · sent at " + path.Base(c.Path) + ":" + strconv.Itoa(c.Line)
					break
				}
			}
			r := &mroute{El: "l" + strconv.Itoa(n), Style: "dotted", Width: 1.5, Chev: "b", Tip: tip, a: sb, b: pb, Callee: sb.El, Caller: pb.El, Rel: "calls"}
			es = append(es, &wire{r: r, a: sb, b: pb})
		}
	}
	// the kind of route each takes
	for _, e := range es {
		ca, cb := col(e.a), col(e.b)
		switch {
		case ca == cb:
			e.kind = "same"
		case cb-ca == 1:
			e.kind = "adj"
		default:
			e.kind = "top"
			ra, rb := row(e.a), row(e.b)
			var cands []int
			cands = append(cands, ra, rb)
			for d := 1; d < 12; d++ {
				cands = append(cands, ra-d, ra+d)
			}
			for _, r := range cands {
				if r < 0 || r > maxRow {
					continue
				}
				// a lane on another row passes no box at either end: it would run into that box's ports
				free := r == ra || r == rb || !at[[2]int{ca, r}] && !at[[2]int{cb, r}]
				for c := ca + 1; c < cb; c++ {
					free = free && !at[[2]int{c, r}]
				}
				if free {
					e.kind, e.lane = "row", r
					break
				}
			}
		}
	}
	// ports: on an edge, the line whose other end is higher takes the higher port
	type port struct {
		e     *wire
		left  bool // the edge's left end sits here
		other float64
	}
	right, left := map[*mbox][]port{}, map[*mbox][]port{}
	for _, e := range es {
		oy := float64(e.b.Y)
		if e.kind == "top" {
			oy = -1
		}
		right[e.a] = append(right[e.a], port{e, true, oy})
		if e.kind == "same" {
			right[e.b] = append(right[e.b], port{e, false, float64(e.a.Y)})
		} else {
			left[e.b] = append(left[e.b], port{e, false, float64(e.a.Y)})
		}
	}
	place := func(ps map[*mbox][]port) {
		for b, list := range ps {
			slices.SortStableFunc(list, func(x, y port) int { return cmp.Compare(x.other, y.other) })
			mid := float64(b.Y) + boxH/2
			for i, p := range list {
				y := mid + (float64(i)-float64(len(list)-1)/2)*8
				if p.left {
					p.e.ya = y
				} else {
					p.e.yb = y
				}
			}
		}
	}
	place(right)
	place(left)
	// a straight line between neighbours on one row: the edge with fewer lines matches the other's port
	for _, e := range es {
		if (e.kind == "adj" || e.kind == "row" && e.lane == row(e.a)) && row(e.a) == row(e.b) && e.ya != e.yb {
			if len(right[e.a]) >= len(left[e.b]) {
				e.yb = e.ya
			} else {
				e.ya = e.yb
			}
		}
	}
	// the lanes along a row: 8 apart in the cells they cross
	used := map[[2]int][]float64{}
	free := func(r, c0, c1 int, y float64) bool {
		for c := c0; c <= c1; c++ {
			if slices.Contains(used[[2]int{c, r}], y) {
				return false
			}
		}
		return true
	}
	topN := 0
	for _, e := range es {
		ca, cb := col(e.a), col(e.b)
		switch e.kind {
		case "row":
			ra, rb := row(e.a), row(e.b)
			switch {
			case e.lane == ra && free(e.lane, ca+1, cb-1, e.ya):
				e.ly = e.ya
			case e.lane == rb && free(e.lane, ca+1, cb-1, e.yb):
				e.ly = e.yb
			default:
				mid := float64(rowTop+rowStep*e.lane) + boxH/2
				for _, off := range []float64{0, 8, -8, 16, -16, 24, -24} {
					if free(e.lane, ca+1, cb-1, mid+off) {
						e.ly = mid + off
						break
					}
				}
			}
			for c := ca + 1; c < cb; c++ {
				used[[2]int{c, e.lane}] = append(used[[2]int{c, e.lane}], e.ly)
			}
		case "top":
			e.ty = []float64{4, 12, 8, 0}[topN%4]
			topN++
		}
	}
	// the tracks: a channel is the gap right of a column; its vertical runs nest so that they don't cross
	type seg struct {
		e          *wire
		second     bool    // the run near the right end
		yl, yr     float64 // the y it comes in at from the left, leaves at to the right
		loop       bool
		chX        float64
		channelCol int
	}
	chans := map[int][]*seg{}
	chX := func(c int) float64 {
		w := boxW
		if c == 0 && sys {
			w = sysW
		}
		return float64(colX(c, sys) + w)
	}
	addSeg := func(c int, s *seg) {
		if s.yl == s.yr && !s.loop {
			return
		}
		s.chX, s.channelCol = chX(c), c
		chans[c] = append(chans[c], s)
	}
	for _, e := range es {
		ca, cb := col(e.a), col(e.b)
		switch e.kind {
		case "same":
			addSeg(ca, &seg{e: e, yl: e.ya, yr: e.yb, loop: true})
		case "adj":
			addSeg(ca, &seg{e: e, yl: e.ya, yr: e.yb})
		case "row":
			if e.ly != e.ya {
				addSeg(ca, &seg{e: e, yl: e.ya, yr: e.ly})
			}
			if e.ly != e.yb {
				addSeg(cb-1, &seg{e: e, second: true, yl: e.ly, yr: e.yb})
			}
		case "top":
			addSeg(ca, &seg{e: e, yl: e.ya, yr: e.ty})
			addSeg(cb-1, &seg{e: e, second: true, yl: e.ty, yr: e.yb})
		}
	}
	for _, ss := range chans {
		// rightmost first: loops by span, then runs down by where they start, then runs up the other way
		key := func(s *seg) (int, float64) {
			switch {
			case s.loop:
				return 0, -math.Abs(s.yr - s.yl)
			case s.yr > s.yl:
				return 1, s.yl
			}
			return 2, -s.yl
		}
		slices.SortStableFunc(ss, func(x, y *seg) int {
			gx, kx := key(x)
			gy, ky := key(y)
			return cmp.Or(cmp.Compare(gx, gy), cmp.Compare(kx, ky))
		})
		step := 8.0
		if len(ss) > 4 {
			step = 36.0 / float64(len(ss)+1)
		}
		for i, s := range ss {
			x := s.chX + 32 - float64(i)*step
			if len(ss) > 4 {
				x = s.chX + 38 - float64(i+1)*step
			}
			if s.second {
				s.e.tx2 = x
			} else {
				s.e.tx1 = x
			}
		}
	}
	for _, e := range es {
		ar, bl, br := float64(e.a.X+e.a.W), float64(e.b.X), float64(e.b.X+e.b.W)
		var pts [][2]float64
		switch e.kind {
		case "same":
			pts = [][2]float64{{ar, e.ya}, {e.tx1, e.ya}, {e.tx1, e.yb}, {br, e.yb}}
		case "adj":
			pts = [][2]float64{{ar, e.ya}, {e.tx1, e.ya}, {e.tx1, e.yb}, {bl, e.yb}}
			if e.ya == e.yb {
				pts = [][2]float64{{ar, e.ya}, {bl, e.yb}}
			}
		case "row":
			pts = [][2]float64{{ar, e.ya}}
			if e.ly != e.ya {
				pts = append(pts, [2]float64{e.tx1, e.ya}, [2]float64{e.tx1, e.ly})
			}
			if e.ly != e.yb {
				pts = append(pts, [2]float64{e.tx2, e.ly}, [2]float64{e.tx2, e.yb})
			}
			pts = append(pts, [2]float64{bl, e.yb})
		case "top":
			pts = [][2]float64{{ar, e.ya}, {e.tx1, e.ya}, {e.tx1, e.ty}, {e.tx2, e.ty}, {e.tx2, e.yb}, {bl, e.yb}}
		}
		e.r.pts = pts
		e.r.D = pathD(pts)
		e.r.ChevW = math.Min(math.Max(e.r.Width, 1.5), 2)
		var ch []string
		if e.r.Chev == "b" || e.r.Chev == "both" {
			ch = append(ch, chevD(pts[len(pts)-2], pts[len(pts)-1]))
		}
		if e.r.Chev == "a" || e.r.Chev == "both" {
			ch = append(ch, chevD(pts[1], pts[0]))
		}
		e.r.Chev = strings.Join(ch, " ")
		mv.Lines = append(mv.Lines, e.r)
	}
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// pathD is an SVG path through pts with right-angle turns of a 4 px radius.
func pathD(pts [][2]float64) string {
	var b strings.Builder
	b.WriteString("M" + num(pts[0][0]) + " " + num(pts[0][1]))
	for i := 1; i < len(pts)-1; i++ {
		p0, p1, p2 := pts[i-1], pts[i], pts[i+1]
		l1 := math.Abs(p1[0]-p0[0]) + math.Abs(p1[1]-p0[1])
		l2 := math.Abs(p2[0]-p1[0]) + math.Abs(p2[1]-p1[1])
		if l1 == 0 || l2 == 0 {
			continue
		}
		r := math.Min(4, math.Min(l1/2, l2/2))
		sx, sy := (p1[0]-p0[0])/l1, (p1[1]-p0[1])/l1
		tx, ty := (p2[0]-p1[0])/l2, (p2[1]-p1[1])/l2
		b.WriteString(" L" + num(p1[0]-sx*r) + " " + num(p1[1]-sy*r) + " Q" + num(p1[0]) + " " + num(p1[1]) + " " + num(p1[0]+tx*r) + " " + num(p1[1]+ty*r))
	}
	last := pts[len(pts)-1]
	b.WriteString(" L" + num(last[0]) + " " + num(last[1]))
	return b.String()
}

// chevD is an open chevron 6 px long, its tip just outside the box at to, pointing along from → to.
func chevD(from, to [2]float64) string {
	sign := func(f float64) float64 { return float64(map[bool]int{true: 1}[f > 0] - map[bool]int{true: 1}[f < 0]) }
	dx, dy := sign(to[0]-from[0]), sign(to[1]-from[1])
	tx, ty := to[0]-dx, to[1]-dy
	bx, by := tx-dx*5, ty-dy*5
	px, py := -dy*4, dx*4
	return "M" + num(bx+px) + " " + num(by+py) + " L" + num(tx) + " " + num(ty) + " L" + num(bx-px) + " " + num(by-py)
}

// tipOf is a line's pointer label (6.2).
func tipOf(l *MapLine, noKinds bool) string {
	first := func(ns []MapName) (MapName, bool) {
		if len(ns) == 0 {
			return MapName{}, false
		}
		return ns[0], true
	}
	n, ok := first(l.Names)
	if !ok {
		n, ok = first(l.Back)
	}
	if !ok {
		return ""
	}
	if noKinds {
		return n.Name + " ×" + strconv.Itoa(n.N) + " · " + plural(l.Calls+l.Through+l.Named, "use", "uses")
	}
	if l.TwoWay {
		if b, ok := first(l.Back); ok && len(l.Names) > 0 {
			return n.Name + " ×" + strconv.Itoa(n.N) + " ⇄ " + b.Name + " ×" + strconv.Itoa(b.N)
		}
	}
	calls := l.Calls + l.Through
	switch {
	case calls == 0:
		return n.Name + " ×" + strconv.Itoa(n.N) + " · no call"
	case l.Calls == 0:
		return n.Name + " ×" + strconv.Itoa(n.N) + " · " + plural(calls, "call", "calls") + " through " + viaWords(l.Via, false)
	}
	t := n.Name + " ×" + strconv.Itoa(n.N) + " · " + plural(calls, "call", "calls")
	for _, nm := range slices.Concat(l.Names, l.Back) {
		switch len(nm.Writes) {
		case 0:
		case 1:
			t += " · " + nm.Name + " writes " + nm.Writes[0]
		default:
			t += " · " + nm.Name + " writes " + strconv.Itoa(len(nm.Writes)) + " tables"
		}
	}
	return t
}

// viaWords is "the interface `T`", "the interfaces `T` and `U`", past two "{n} interfaces"; code without backticks.
func viaWords(via []string, ticks bool) string {
	q := func(s string) string {
		if ticks {
			return "`" + s + "`"
		}
		return s
	}
	switch len(via) {
	case 0:
		return "an interface"
	case 1:
		return "the interface " + q(via[0])
	case 2:
		return "the interfaces " + q(via[0]) + " and " + q(via[1])
	}
	return strconv.Itoa(len(via)) + " interfaces"
}

// words is what the map says in words: each box's hover set, accessible name and phone line, the legend, the reach,
// the trace and the selection panels.
//
//nolint:gocognit,gocyclo // each box's sentences from its lines, one case each
func (mv *mapView) words(v *view) {
	m := mv.src
	labelOf := func(id string) string {
		if b := mv.partBox[id]; b != nil {
			return b.Label
		}
		return id
	}
	// what each part calls, is called from, names; what each system is called from
	type rel struct{ calls, from, iface, names, namedBy, sys []string }
	rels := map[string]*rel{}
	get := func(id string) *rel {
		if rels[id] == nil {
			rels[id] = &rel{}
		}
		return rels[id]
	}
	for _, l := range m.Lines {
		if l.Style == "" {
			continue
		}
		f, t := get(l.From), get(l.To)
		if l.Calls+l.Through == 0 { // named without a call: To names From
			t.names, f.namedBy = append(t.names, labelOf(l.From)), append(f.namedBy, labelOf(l.To))
			continue
		}
		t.calls, f.from = append(t.calls, labelOf(l.From)), append(f.from, labelOf(l.To))
		if l.Calls == 0 {
			f.iface = append(f.iface, labelOf(l.To))
		}
		if l.TwoWay {
			f.calls, t.from = append(f.calls, labelOf(l.To)), append(t.from, labelOf(l.From))
		}
	}
	for _, s := range m.Systems {
		for _, p := range s.Parts {
			get(p).sys = append(get(p).sys, s.Name)
		}
	}
	for _, r := range mv.Lines {
		r.a.Nb, r.b.Nb = appendNew(r.a.Nb, r.b.El), appendNew(r.b.Nb, r.a.El)
		r.a.Ln, r.b.Ln = append(r.a.Ln, r.El), append(r.b.Ln, r.El)
	}
	tablesOf := map[string][2][]string{}
	for _, tb := range slices.Concat(mv.Writes, mv.Reads) {
		for _, s := range m.Stores {
			if s.Name != tb.Label {
				continue
			}
			for _, sp := range s.Parts {
				pb := mv.partBox[sp.Part]
				if pb == nil {
					continue
				}
				tb.Nb = appendNew(tb.Nb, pb.El)
				pb.Nb = appendNew(pb.Nb, tb.El)
				ws := tablesOf[sp.Part]
				for _, u := range sp.Uses {
					if u.Kind == "writes" && !slices.Contains(ws[0], s.Name) {
						ws[0] = append(ws[0], s.Name)
					}
					if u.Kind == "reads" && !slices.Contains(ws[1], s.Name) {
						ws[1] = append(ws[1], s.Name)
					}
				}
				tablesOf[sp.Part] = ws
			}
			for _, mg := range s.Migrations {
				if d := mv.byKey["deploy:"+mg.Path]; d != nil {
					d.Nb = appendNew(d.Nb, tb.El)
				}
			}
		}
	}
	for _, b := range mv.Fig {
		if b.Kind == "system" {
			var ps []string
			for _, s := range m.Systems {
				if s.Name == b.Label {
					for _, p := range s.Parts {
						ps = append(ps, labelOf(p))
					}
				}
			}
			b.Aria = b.Label + ", outside the repo"
			if len(ps) > 0 {
				b.Aria += ", called from " + join(ps)
			}
		}
	}
	for _, b := range mv.partBox {
		r := get(b.part.ID)
		var a, says []string
		a = append(a, b.Label)
		if b.New {
			a = append(a, "new")
		}
		if b.Open > 0 {
			a = append(a, plural(b.Open, "open item", "open items"))
		}
		a = append(a, plural(b.Files, "file", "files"))
		if b.Eye {
			a = append(a, "the review didn't read all its files")
		}
		if len(r.calls) > 0 {
			a, says = append(a, "calls "+upToParts(r.calls)), append(says, "Calls "+join(r.calls)+".")
		}
		for _, s := range r.sys {
			a = append(a, "calls "+s+" outside the repo")
			says = append(says, "Calls `"+s+"`.")
		}
		if len(r.from) > 0 {
			t := "called from " + upToParts(r.from)
			if len(r.iface) == len(r.from) {
				t += " through interfaces"
			}
			a, says = append(a, t), append(says, sentenceCase(t)+".")
		}
		if len(r.names) > 0 {
			a, says = append(a, "names "+upToParts(r.names)), append(says, "Names "+join(r.names)+" without a call.")
		}
		if len(r.namedBy) > 0 {
			a, says = append(a, "named by "+upToParts(r.namedBy)), append(says, "Named without a call by "+join(r.namedBy)+".")
		}
		ts := tablesOf[b.part.ID]
		for i, verb := range []string{"writes", "reads"} {
			if len(ts[i]) > 0 {
				a = append(a, verb+" "+upToTables(ts[i], false))
				says = append(says, sentenceCase(verb)+" "+upToTables(ts[i], true)+".")
			}
		}
		if b.DB {
			a, says = append(a, "used a table this PR drops"), append(says, "Used a table this PR drops.")
		}
		if b.Reach > 0 {
			a = append(a, plural(b.Reach, "place", "places")+" outside this PR")
		}
		b.Aria, b.Says = strings.Join(a, ", "), strings.Join(says, " ")
	}
	// a migration's hover: the parts that used a table it drops, the tables it changes
	for _, d := range mv.Deploy {
		for _, src := range m.Deploy {
			if src.Path != d.Path || src.Label != d.Label || len(src.Drops) == 0 {
				continue
			}
			for _, t := range m.Tables {
				if t.Path != src.Path {
					continue
				}
				for _, tp := range t.Parts {
					if pb := mv.partBox[tp.Part]; pb != nil {
						d.Nb = appendNew(d.Nb, pb.El)
					}
				}
			}
		}
	}
	for _, s := range m.Systems {
		if b := mv.byKey["system:"+s.Name]; b != nil {
			for _, p := range s.Parts {
				if pb := mv.partBox[p]; pb != nil {
					b.Nb = appendNew(b.Nb, pb.El)
				}
			}
		}
	}
	// the legend
	switch {
	case m.NoGraph:
		mv.Sentence = "No code graph for this review, so the map has no lines."
	case m.NoKinds:
		mv.Sentence = "Lines are uses of changed code: calls, types and other names."
	default:
		mv.Sentence = "Solid lines are calls into changed code. Arrows point where data goes: to the caller, or to the callee when the call writes a table."
	}
	keys := map[string]bool{}
	for _, l := range m.Lines {
		switch {
		case l.Style == "solid" && l.Width >= 3:
			keys["heavy"] = true
		case l.Style == "dashed" && l.Chevron != "":
			keys["iface"] = true
		case l.Style == "dashed":
			keys["dash"] = true
		}
	}
	keys["dot"] = len(mv.Lines) > 0 && len(m.Systems) > 0
	for _, b := range mv.partBox {
		keys["reach"] = keys["reach"] || b.Reach > 0
		keys["db"] = keys["db"] || b.DB
		keys["eye"] = keys["eye"] || b.Eye
	}
	if m.NoGraph {
		keys["heavy"], keys["iface"], keys["dash"], keys["dot"] = false, false, false, false
	}
	for _, k := range []string{"heavy", "iface", "dash", "dot", "reach", "db", "eye"} {
		if keys[k] {
			mv.Keys = append(mv.Keys, mkey{k})
		}
	}
	// the reach (6.7)
	rc := m.Reach
	mv.Tests, mv.ReachMore = rc.Tests, rc.More
	var pairs []string
	for _, l := range m.Lines {
		if l.TwoWay {
			pairs = append(pairs, labelOf(l.From)+" and "+labelOf(l.To))
			s := labelOf(l.From) + " and " + labelOf(l.To) + ":"
			if n, ok := firstName(l.Names); ok {
				s += " " + labelOf(l.To) + " calls `" + n + "`"
			}
			if n, ok := firstName(l.Back); ok {
				s += "; " + labelOf(l.From) + " calls `" + n + "`"
			}
			mv.Pairs = append(mv.Pairs, s+".")
		}
	}
	switch {
	case m.NoGraph:
	case rc.Places == 0:
		mv.Reach = "No code outside this PR names the code it changes."
	default:
		mv.Reach = "Outside this PR, " + plural(rc.Places, "place", "places") + " in code name " + strconv.Itoa(rc.Decls) + " of the declarations it changes."
	}
	if mv.Reach != "" && len(pairs) > 0 {
		mv.Reach += " " + map[bool]string{true: "One pair uses", false: strconv.Itoa(len(pairs)) + " pairs use"}[len(pairs) == 1] + " each other: " + join(pairs) + "."
	}
	for _, d := range rc.List {
		lb := labelOf(d.Part)
		if len(mv.ReachList) == 0 || mv.ReachList[len(mv.ReachList)-1].Label != lb {
			mv.ReachList = append(mv.ReachList, reachPart{Label: lb})
		}
		rp := &mv.ReachList[len(mv.ReachList)-1]
		rp.Decls = append(rp.Decls, d)
	}
	mv.trace(v)
}

func firstName(ns []MapName) (string, bool) {
	if len(ns) == 0 {
		return "", false
	}
	return ns[0].Name, true
}

func appendNew(s []string, x string) []string {
	if slices.Contains(s, x) {
		return s
	}
	return append(s, x)
}

func upToParts(ps []string) string {
	if len(ps) > 3 {
		return strconv.Itoa(len(ps)) + " parts"
	}
	return join(ps)
}

// upToTables is up to 3 table names, then "and {n} more tables"; code spans when ticks.
func upToTables(ts []string, ticks bool) string {
	q := slices.Clone(ts)
	if ticks {
		q = codeAll(ts)
	}
	if len(q) > 3 {
		return strings.Join(q[:3], ", ") + " and " + plural(len(q)-3, "more table", "more tables")
	}
	return join(q)
}

// trace is the traced path (6.12): the way's rows down to the outside call, and the boxes and lines it lights.
func (mv *mapView) trace(v *view) {
	t := mv.src.Trace
	if t == nil || len(t.Rows) == 0 {
		return
	}
	tv := &traceView{}
	entryPart := ""
	if pb := v.partOf(t.At.Path); pb != nil {
		entryPart = pb.Label
	} else if pb := v.partOf(t.Rows[0].Path); pb != nil {
		entryPart = pb.Label
	}
	if t.Method == "CALL" || t.Method == "" {
		tv.Foot = "The path starts where the code graph finds no caller."
	}
	tv.Label = "Follow one path from " + cmp.Or(entryPart, path.Base(t.At.Path)) + " to `" + t.Call.System + "`"
	tv.Path = v.tracePath(t, entryPart, v.hops())
	var boxes []string
	for _, r := range t.Rows {
		if pb := v.partOf(r.Path); pb != nil {
			if b := mv.partBox[pb.ID]; b != nil && !slices.Contains(boxes, b.El) {
				boxes = append(boxes, b.El)
			}
		}
	}
	if sb := mv.byKey["system:"+t.Call.System]; sb != nil {
		boxes = append(boxes, sb.El)
	}
	tv.Boxes = boxes
	for i := 0; i+1 < len(boxes); i++ {
		for _, r := range mv.Lines {
			if (r.a.El == boxes[i] && r.b.El == boxes[i+1]) || (r.b.El == boxes[i] && r.a.El == boxes[i+1]) {
				tv.Lines = appendNew(tv.Lines, r.El)
			}
		}
	}
	mv.Trace = tv
}

// partOf is the map's part that holds p: by its files, else by its folder; nil without a map or a part.
func (v *view) partOf(p string) *MapPart {
	if v.Map == nil {
		return nil
	}
	var best *MapPart
	for i := range v.Map.Parts {
		pt := &v.Map.Parts[i]
		if slices.Contains(pt.Files, p) {
			return pt
		}
		if pt.Path != "." && strings.HasPrefix(p, pt.Path+"/") && (best == nil || len(pt.Path) > len(best.Path)) && mapCode(p) {
			best = pt
		}
	}
	return best
}

// boxOf is the key of the box p sits on (6.5): its part, Generated, a deploy box by the file's kind; "" off
// the map.
func (v *view) boxOf(p string) string {
	mv := v.Page.Map
	if mv == nil {
		return ""
	}
	if pt := v.partOf(p); pt != nil {
		return "part:" + pt.ID
	}
	if g := mv.src.Generated; g != nil && slices.Contains(g.Files, p) {
		return "generated"
	}
	var key string
	switch {
	case diff.Kind(p) == "doc":
		key = "deploy:docs"
	case migration.MatchString(p):
		key = "deploy:" + p
	case deploy.MatchString(p) || toolConfig(p):
		key = "deploy:ci"
	case config.MatchString(p) && diff.Kind(p) != "test":
		key = "deploy:configuration"
	}
	if mv.byKey[key] != nil {
		return key
	}
	return ""
}

// boxLabel is the label of a box by its key.
func (v *view) boxLabel(key string) string {
	if mv := v.Page.Map; mv != nil && mv.byKey[key] != nil {
		return mv.byKey[key].Label
	}
	return key
}

// inMapOrder is box keys in the order the map lists them: the figure's parts, the parts without a line, the
// rest.
func (v *view) inMapOrder(keys []string) []string {
	mv := v.Page.Map
	if mv == nil {
		return keys
	}
	var order []string
	for _, b := range slices.Concat(mv.Fig, mv.Alone, mv.Deploy) {
		order = append(order, b.Key)
	}
	out := slices.Clone(keys)
	slices.SortStableFunc(out, func(a, b string) int {
		ia, ib := slices.Index(order, a), slices.Index(order, b)
		if ia < 0 {
			ia = len(order)
		}
		if ib < 0 {
			ib = len(order)
		}
		return cmp.Compare(ia, ib)
	})
	return out
}

// deployOf is the Map's deploy box of a box key, nil for none.
func (v *view) deployOf(key string) *MapDeploy {
	if v.Map == nil {
		return nil
	}
	for i, d := range v.Map.Deploy {
		if key == "deploy:"+d.Path && d.Kind == "migration" || key == "deploy:"+d.Kind && d.Kind != "migration" {
			return &v.Map.Deploy[i]
		}
	}
	return nil
}

// All is every box, in element order: the selection panels follow it.
func (mv *mapView) All() []*mbox {
	bs := slices.Collect(maps.Values(mv.byKey))
	slices.SortFunc(bs, func(a, b *mbox) int {
		x, _ := strconv.Atoi(a.El[1:])
		y, _ := strconv.Atoi(b.El[1:])
		return cmp.Compare(x, y)
	})
	return bs
}

// MoreTables is the ghost button past 12 boxes in a table row: "{n} more tables"; "" with none hidden.
func (mv *mapView) MoreTables() string {
	n := 0
	for _, b := range slices.Concat(mv.Writes, mv.Reads) {
		if b.Hide {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return plural(n, "more table", "more tables")
}
