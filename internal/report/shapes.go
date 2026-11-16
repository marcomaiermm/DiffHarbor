package report

import (
	"cmp"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// The code structure at a use (component 31, uses spec 5 and 9.2): a front-end reads the syntax tree, Go's here
// (goShape), TypeScript's in tsedges.cjs --shapes, and gives the way from the declaration down to each use as synEls;
// shape turns up to maxMarked of them into one Shape's rows, by the same rules for every language.
const (
	maxShapeDepth = 8    // rows from the declaration to a use; past it the outer 2, "{n} levels", the inner 5
	maxKept       = 3    // entries a fold keeps, nearest the way
	maxRowText    = 60   // characters a row's or an entry's text reads
	maxMarked     = 3    // uses one shape marks
	maxShapeBytes = 1500 // past it the folds drop their entries, outermost first, then texts cut to shortText
	shortText     = 32
)

// synEl is a syntax node on the way from a declaration down to a use: the kind of row it makes (section 5's; ""
// one that makes none of its own: a property's key, the use's name), its line, its text from its start to that
// line's end, the entries it holds as a container and the one of them the way goes on in (-1 none: a chain's head,
// an if's init), the note on where an if's condition comes from.
type synEl struct {
	K    string     `json:"k,omitempty"`
	L    int        `json:"l"`
	T    string     `json:"t"`
	Kids []synKid   `json:"e,omitempty"`
	W    int        `json:"w"`
	C    *ShapeCond `json:"c,omitempty"`
}

// synKid is a container's entry: its kind, first and last line, text, and whether it names the subject.
type synKid struct {
	K string `json:"k,omitempty"`
	L int    `json:"l"`
	E int    `json:"e"`
	T string `json:"t"`
	U bool   `json:"u,omitempty"`
}

// synUse is what a front-end reads of one declaration that names a subject: its lines (Span), every line in it that
// names the subject, the ways down to the first maxMarked of them, and the interface type's name when it is one
// that lists it.
type synUse struct {
	Span  [2]int    `json:"s"`
	Lines []int     `json:"n"`
	Ways  [][]synEl `json:"w,omitempty"`
	Iface string    `json:"i,omitempty"`
}

// synNode is the ways of one declaration merged: shared nodes once, w the entry of the parent the node sits in.
type synNode struct {
	el   synEl
	w    int
	use  bool
	next []*synNode
}

// shape is the structure of ways, the uses of name in one declaration of path, by section 5's rules: one row per
// line, a node that starts on its row's line merges into it, one that starts later is a row one level deeper; at
// each container the entries off the way fold. ok false for one row: nothing to open.
func shape(p, name string, span [2]int, ways [][]synEl) (Shape, bool) {
	if len(ways) == 0 || len(ways[0]) == 0 {
		return Shape{}, false
	}
	root := &synNode{el: ways[0][0]}
	for _, w := range ways {
		if w[0].L != root.el.L || w[0].T != root.el.T {
			continue // another declaration: not this shape's
		}
		n := root
		for i := 1; i < len(w); i++ {
			el, at := w[i], w[i-1].W
			j := slices.IndexFunc(n.next, func(c *synNode) bool { return c.el.L == el.L && c.el.T == el.T && c.el.K == el.K && c.w == at })
			if j < 0 {
				j, n.next = len(n.next), append(n.next, &synNode{el: el, w: at})
			}
			n = n.next[j]
		}
		n.use = true
	}
	b := shaper{rows: []ShapeRow{{Kind: "decl", Text: root.el.T, Line: root.el.L}}}
	b.emit(root, 0)
	if len(b.rows) == 1 {
		return Shape{}, false
	}
	rows := levels(b.rows)
	for i := range rows {
		r := &rows[i]
		r.Kind = cmp.Or(r.Kind, "stmt")
		r.Text = cutRow(r.Text, name, r.Use, maxRowText)
		if r.Cond != nil {
			r.Cond.Text = cutRow(r.Cond.Text, "", false, maxRowText)
		}
		for j := range r.Kept {
			r.Kept[j].Text = cutRow(r.Kept[j].Text, name, r.Kept[j].Use, maxRowText)
		}
	}
	s := Shape{Path: p, Span: span, Rows: rows}
	slim(&s, name)
	return s, true
}

type shaper struct{ rows []ShapeRow }

// emit adds the rows below n, which sits on row at: n's way on, and the entries of n off it as folds, in the
// order the code has them.
func (b *shaper) emit(n *synNode, at int) {
	r := &b.rows[at]
	line, d := r.Line, r.Depth
	if n.use {
		r.Use = true
	}
	if n.el.C != nil {
		r.Cond = n.el.C
	}
	if r.Kind == "" {
		r.Kind = n.el.K
	}
	child := func(c *synNode) {
		if c.el.L <= line { // on this row: merges
			b.emit(c, at)
			return
		}
		if n.el.K != "" {
			b.rows[at].Kind = n.el.K // the row opens n: n's entries hang below it
		}
		b.rows = append(b.rows, ShapeRow{Kind: c.el.K, Depth: d + 1, Text: c.el.T, Line: c.el.L})
		b.emit(c, len(b.rows)-1)
	}
	ways := slices.Clone(n.next)
	slices.SortStableFunc(ways, func(x, y *synNode) int { return cmp.Or(cmp.Compare(x.w, y.w), cmp.Compare(x.el.L, y.el.L)) })
	for _, c := range ways {
		if c.w < 0 {
			child(c)
		}
	}
	var off []synKid // entries off the way since the last on it
	seen := false    // an entry on the way came before them
	for i, k := range n.el.Kids {
		on := false
		for _, c := range ways {
			if c.w == i {
				if !on {
					b.fold(off, d+1, seen)
					off, on = nil, true
				}
				child(c)
			}
		}
		seen = seen || on
		switch {
		case on || k.L <= line: // on the way, or on n's own row
		case len(off) > 0 && off[len(off)-1].L == k.L: // on the line of the one before: one entry, one row (rule 2)
			last := &off[len(off)-1]
			last.E, last.U = max(last.E, k.E), last.U || k.U
		default:
			off = append(off, k)
		}
	}
	b.fold(off, d+1, seen)
}

// fold adds a row for entries off the way: the maxKept nearest it as they read, the others' lines as a range. after:
// an entry on the way is above them, so the first are nearest; else the last.
func (b *shaper) fold(ks []synKid, d int, after bool) {
	if len(ks) == 0 {
		return
	}
	r := ShapeRow{Kind: "fold", Depth: d, N: len(ks)}
	kept, rest := ks, []synKid(nil)
	if len(ks) > maxKept {
		if after {
			kept, rest = ks[:maxKept], ks[maxKept:]
		} else {
			kept, rest = ks[len(ks)-maxKept:], ks[:len(ks)-maxKept]
		}
		r.More = &[2]int{rest[0].L, max(rest[len(rest)-1].E, rest[len(rest)-1].L)}
	}
	for _, k := range kept {
		r.Kept = append(r.Kept, ShapeLeaf{Kind: cmp.Or(k.K, "stmt"), Text: k.T, Line: k.L, Use: k.U})
	}
	b.rows = append(b.rows, r)
}

// levels keeps each way deeper than maxShapeDepth rows readable, the deepest first: the rows of depth 0 and 1, one
// "levels" row for those between, the inner 5. Only that way's rows and what hangs off them fold; a use, a row on
// another way and a levels row stay, so the cut moves below them (and may then keep the way deeper). The rows below
// the cut move up to follow it.
func levels(rows []ShapeRow) []ShapeRow {
	for {
		out, ok := cutLevels(rows)
		if !ok {
			return rows
		}
		rows = out
	}
}

// cutLevels cuts the deepest use's way past maxShapeDepth once; ok false when there is none or it can't be cut.
func cutLevels(rows []ShapeRow) ([]ShapeRow, bool) {
	up := make([]int, len(rows)) // each row's parent, -1 none
	var stack []int
	for i, r := range rows {
		for len(stack) > 0 && rows[stack[len(stack)-1]].Depth >= r.Depth {
			stack = stack[:len(stack)-1]
		}
		up[i] = -1
		if len(stack) > 0 {
			up[i] = stack[len(stack)-1]
		}
		stack = append(stack, i)
	}
	way := func(i int) []int { // the rows from the top down to i
		var w []int
		for ; i >= 0; i = up[i] {
			w = append(w, i)
		}
		slices.Reverse(w)
		return w
	}
	deep := -1
	for i, r := range rows {
		if r.Use && r.Depth > maxShapeDepth && (deep < 0 || r.Depth > rows[deep].Depth) {
			deep = i
		}
	}
	if deep < 0 {
		return rows, false
	}
	a := way(deep)
	d := len(a) - 1
	from, to := 2, 0
	for moved := true; moved; { // below every row the cut mustn't take
		moved, to = false, min(from+d-7, d-1)
		for i, r := range rows {
			if i == deep || !r.Use && r.Kind != "levels" {
				continue
			}
			b, c := way(i), 0 // c: the rows the two ways share, the last of them stays
			for c < len(b) && c < len(a) && a[c] == b[c] {
				c++
			}
			if from <= c-1 && c-1 <= to {
				from, moved = c, true
			}
		}
	}
	if from >= to { // a cut that moves nothing up
		return rows, false
	}
	end := func(i int) int { // past i's rows and those below it
		j := i + 1
		for j < len(rows) && rows[j].Depth > rows[i].Depth {
			j++
		}
		return j
	}
	s0, s1 := a[from], a[to+1]
	e0, e1 := end(s0), end(s1)
	lv := ShapeRow{Kind: "levels", Depth: rows[s0].Depth}
	for _, r := range slices.Concat(rows[s0:s1], rows[e1:e0]) {
		if r.Kind == "fold" {
			continue
		}
		if lv.More == nil {
			lv.More = &[2]int{r.Line, r.Line}
		}
		lv.N, lv.More[0], lv.More[1] = lv.N+1, min(lv.More[0], r.Line), max(lv.More[1], r.Line)
	}
	out := append(slices.Clone(rows[:s0]), lv)
	for _, r := range rows[s1:e1] {
		r.Depth -= to - from
		out = append(out, r)
	}
	return append(out, rows[e0:]...), true
}

// slim keeps a shape within maxShapeBytes: the folds drop their kept entries, outermost first (their counts and
// lines stay), then every text cuts to shortText.
func slim(s *Shape, name string) {
	size := func() int { b, _ := json.Marshal(s); return len(b) }
	if size() <= maxShapeBytes {
		return
	}
	order := make([]int, 0, len(s.Rows))
	for i, r := range s.Rows {
		if len(r.Kept) > 0 {
			order = append(order, i)
		}
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(s.Rows[a].Depth, s.Rows[b].Depth) })
	for _, i := range order {
		r := &s.Rows[i]
		if r.More == nil {
			r.More = &[2]int{r.Kept[0].Line, r.Kept[len(r.Kept)-1].Line}
		}
		r.More[0], r.More[1] = min(r.More[0], r.Kept[0].Line), max(r.More[1], r.Kept[len(r.Kept)-1].Line)
		r.Kept = nil
		if size() <= maxShapeBytes {
			return
		}
	}
	for i := range s.Rows {
		s.Rows[i].Text = cutRow(s.Rows[i].Text, name, s.Rows[i].Use, shortText)
		if c := s.Rows[i].Cond; c != nil {
			c.Text = cutRow(c.Text, "", false, shortText)
		}
	}
}

var spaces = regexp.MustCompile(`\s+`)

// cutRow is a row's text, whitespace collapsed, at most n characters with an ellipsis. With the use in it (use: the
// name, as a word) past the cut, the cut moves to the start: "…" and the text that ends with the name.
func cutRow(t, name string, use bool, n int) string {
	t = strings.TrimSpace(spaces.ReplaceAllString(t, " "))
	rs := []rune(t)
	if len(rs) <= n {
		return t
	}
	if end := wordEnd(t, name); use && end > 0 {
		if end = utf8.RuneCountInString(t[:end]); end > n-1 {
			return "…" + string(rs[end-(n-1):end])
		}
	}
	return string(rs[:n-1]) + "…"
}

// wordEnd is the byte offset just past the first name in t that stands as a word; 0 for none.
func wordEnd(t, name string) int {
	word := func(i int) bool { return i >= 0 && i < len(t) && (t[i] == '_' || t[i] == '$' || isAlnum(t[i])) }
	for at := 0; name != ""; {
		i := strings.Index(t[at:], name)
		if i < 0 {
			return 0
		}
		i += at
		if !word(i-1) && !word(i+len(name)) {
			return i + len(name)
		}
		at = i + 1
	}
	return 0
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// goFile is a parsed Go file and its lines.
type goFile struct {
	fset *token.FileSet
	f    *ast.File
	ls   []string
}

// parseGo is src parsed; nil when it doesn't parse.
func parseGo(src []byte) *goFile {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	return &goFile{fset, f, strings.Split(string(src), "\n")}
}

// lineOf is p's line.
func (gf *goFile) lineOf(p token.Pos) int { return gf.fset.Position(p).Line }

// text is n's source from its start to its line's end.
func (gf *goFile) text(n ast.Node) string {
	pos := gf.fset.Position(n.Pos())
	if pos.Line < 1 || pos.Line > len(gf.ls) || pos.Column-1 > len(gf.ls[pos.Line-1]) {
		return ""
	}
	return gf.ls[pos.Line-1][pos.Column-1:]
}

// declAt is the top-level declaration that holds line, in a block of specs the spec; nil none.
func (gf *goFile) declAt(line int) ast.Node {
	var decl ast.Node
	for _, d := range gf.f.Decls {
		if gf.lineOf(d.Pos()) > line || gf.lineOf(d.End()) < line {
			continue
		}
		decl = d
		if g, ok := d.(*ast.GenDecl); ok && g.Lparen.IsValid() {
			for _, s := range g.Specs {
				if gf.lineOf(s.Pos()) <= line && gf.lineOf(s.End()) >= line {
					decl = s
				}
			}
		}
	}
	return decl
}

// goShape reads the Go declaration of gf that holds line for every line in it that names name and, with ways, the
// ways down to the first maxMarked of them: section 5's kinds from go/ast. By name: a local of the same name counts.
// When no identifier at line names it, a string there that holds it as a word does: a table SQL names. ok false
// when no declaration holds line.
func goShape(gf *goFile, line int, name string, ways bool) (synUse, bool) {
	decl := gf.declAt(line)
	if decl == nil {
		return synUse{}, false
	}
	u := synUse{Span: [2]int{gf.lineOf(decl.Pos()), gf.lineOf(decl.End())}}
	spec := decl
	if g, ok := decl.(*ast.GenDecl); ok && len(g.Specs) == 1 {
		spec = g.Specs[0]
	}
	own := map[*ast.Ident]bool{} // the name decl declares: no use of a subject of that name, a wrapper's own
	switch d := spec.(type) {
	case *ast.FuncDecl:
		own[d.Name] = true
	case *ast.TypeSpec:
		own[d.Name] = true
	case *ast.ValueSpec:
		for _, id := range d.Names {
			own[id] = true
		}
	}
	var ids, strs []ast.Node
	ast.Inspect(decl, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			if l := gf.lineOf(n.Pos()); n.Name == name && !own[n] && (len(u.Lines) == 0 || u.Lines[len(u.Lines)-1] != l) {
				u.Lines, ids = append(u.Lines, l), append(ids, n)
			}
		case *ast.BasicLit:
			if n.Kind == token.STRING && gf.lineOf(n.Pos()) <= line && line <= gf.lineOf(n.End()) && wordEnd(n.Value, name) > 0 {
				strs = append(strs, n)
			}
		}
		return true
	})
	if !slices.Contains(u.Lines, line) && len(strs) > 0 {
		ids = append(ids, strs[0])
		u.Lines = append(u.Lines, line)
		slices.SortFunc(ids, func(a, b ast.Node) int { return cmp.Compare(a.Pos(), b.Pos()) })
		slices.Sort(u.Lines)
	}
	if t, ok := spec.(*ast.TypeSpec); ok {
		if _, ok := t.Type.(*ast.InterfaceType); ok {
			u.Iface = t.Name.Name
		}
	}
	if !ways {
		return u, true
	}
	for _, id := range ids[:min(len(ids), maxMarked)] {
		u.Ways = append(u.Ways, gf.way(pathTo(decl, id), ids))
	}
	return u, true
}

// pathTo is the nodes from root down to n, root first; nil when root doesn't hold n.
func pathTo(root, n ast.Node) []ast.Node {
	var stack, out []ast.Node
	ast.Inspect(root, func(x ast.Node) bool {
		switch {
		case out != nil:
			return false
		case x == nil:
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, x)
		if x == n {
			out = slices.Clone(stack)
		}
		return true
	})
	return out
}

// linkSpan is a chain's link as a node: from its name to its end.
type linkSpan struct{ pos, end token.Pos }

func (s linkSpan) Pos() token.Pos { return s.pos }
func (s linkSpan) End() token.Pos { return s.end }

// way is the rows path makes, from its declaration down to its last node, the use; each container with its entries
// (ids marks those that hold a use) and the one the way goes on in. A chain of calls and selectors over lines is one
// chain row, its links its entries, and the link the use is in a row of its own, its arguments its entries (section
// 5 rule 1).
func (gf *goFile) way(path []ast.Node, ids []ast.Node) []synEl {
	in := func(k, n ast.Node) bool { return k.Pos() <= n.Pos() && n.End() <= k.End() }
	holds := func(k ast.Node) bool { return slices.ContainsFunc(ids, func(id ast.Node) bool { return in(k, id) }) }
	var rows []wayRow
	skip := map[ast.Node]bool{} // a chain's own nodes, its row and its links' stand for them
	for j, n := range path {
		if skip[n] {
			continue
		}
		k, kids, ok := goEntries(n)
		if j < len(path)-1 && chainTop(path, j) {
			if ch := gf.chainRows(n.(ast.Expr), path[len(path)-1], skip); ch != nil {
				rows = append(rows, ch...)
				continue
			}
		}
		switch {
		case j == len(path)-1: // the use
		case !ok:
			continue
		case isElseOf(path, j):
			k = "if"
		}
		if _, b := n.(*ast.BlockStmt); b && !isElseOf(path, j) {
			continue
		}
		r := wayRow{n: n, k: k, kids: kids}
		if _, ok := n.(*ast.IfStmt); ok {
			r.c = goCond(path, j, gf.lineOf, gf.text)
		}
		rows = append(rows, r)
	}
	way := make([]synEl, len(rows))
	for j, r := range rows {
		var next ast.Node // the row the way goes on to
		if j+1 < len(rows) {
			next = rows[j+1].n
		}
		el := synEl{K: r.k, L: gf.lineOf(r.n.Pos()), T: gf.text(r.n), W: -1, C: r.c}
		var flat []ast.Node
		for _, kid := range r.kids {
			_, sub, _ := goEntries(kid)
			// an entry on this line that holds no more of the way: its own entries hang below this row
			if gf.lineOf(kid.Pos()) == el.L && len(sub) > 0 && (next == nil || !in(kid, next)) {
				flat = append(flat, sub...)
			} else {
				flat = append(flat, kid)
			}
		}
		for i, kid := range flat {
			el.Kids = append(el.Kids, synKid{K: goKind(kid), L: gf.lineOf(kid.Pos()), E: gf.lineOf(kid.End()), T: gf.text(kid), U: holds(kid)})
			if next != nil && el.W < 0 && in(kid, next) {
				el.W = i
			}
		}
		way[j] = el
	}
	return way
}

// wayRow is a node on a way that makes a row, its kind, its entries and its condition note.
type wayRow struct {
	n    ast.Node
	k    string
	kids []ast.Node
	c    *ShapeCond
}

// chainRows is the rows of the chain n when it runs over lines: one chain row, its links its entries, and the link
// use is in, its arguments its entries (none when use is the head); the chain's own nodes join skip. nil for a
// chain on one line or none.
func (gf *goFile) chainRows(n ast.Expr, use ast.Node, skip map[ast.Node]bool) []wayRow {
	links := goLinks(n)
	if len(links) == 0 || gf.lineOf(links[len(links)-1].span.Pos()) <= gf.lineOf(n.Pos()) {
		return nil
	}
	rows := []wayRow{{n: n, k: "chain"}}
	for _, l := range links {
		skip[l.x], skip[l.sel] = true, true
		rows[0].kids = append(rows[0].kids, l.span)
		if l.span.Pos() <= use.Pos() && use.End() <= l.span.End() {
			r := wayRow{n: l.span, k: "link"}
			if l.call != nil {
				r.kids = exprs(l.call.Args)
			}
			rows = append(rows, r)
		}
	}
	return rows
}

// goLink is a link of a chain: the selector, the call it is the function of (nil a field's read), the outermost of
// the two, and its span from the selector's name.
type goLink struct {
	sel  *ast.SelectorExpr
	call *ast.CallExpr
	x    ast.Node
	span linkSpan
}

// goLinks is the links of the chain n, `db.Select(…).From(t).All()`, from the head's out; none when n is no chain.
func goLinks(n ast.Expr) []goLink {
	var ls []goLink
	for x := n; ; {
		call, _ := x.(*ast.CallExpr)
		fun := x
		if call != nil {
			fun = call.Fun
		}
		sel, ok := fun.(*ast.SelectorExpr)
		if !ok {
			break
		}
		ls = append(ls, goLink{sel, call, x, linkSpan{sel.Sel.Pos(), x.End()}})
		x = sel.X
	}
	slices.Reverse(ls)
	return ls
}

// chainTop is whether path[j] is the outermost call or selector of a chain: not the function of a call or the
// operand of a selector above it.
func chainTop(path []ast.Node, j int) bool {
	switch path[j].(type) {
	case *ast.CallExpr, *ast.SelectorExpr:
	default:
		return false
	}
	if j == 0 {
		return true
	}
	switch p := path[j-1].(type) {
	case *ast.CallExpr:
		return p.Fun != path[j]
	case *ast.SelectorExpr:
		return p.X != path[j]
	}
	return true
}

// goEntries is n's row kind and the entries it holds as a container; ok false for a node that makes no row: an
// expression inside one. A declaration, a spec and a key: value make a row without a kind of their own.
func goEntries(n ast.Node) (kind string, kids []ast.Node, ok bool) {
	switch n := n.(type) {
	case *ast.FuncDecl:
		return "func", stmts(n.Body), true
	case *ast.FuncLit:
		return "func", stmts(n.Body), true
	case *ast.CompositeLit:
		return compositeKind(n), exprs(n.Elts), true
	case *ast.CallExpr:
		return "call", exprs(n.Args), true
	case *ast.IfStmt:
		kids = stmts(n.Body)
		if n.Else != nil {
			kids = append(kids, n.Else)
		}
		return "if", kids, true
	case *ast.SwitchStmt:
		return "if", stmts(n.Body), true
	case *ast.TypeSwitchStmt:
		return "if", stmts(n.Body), true
	case *ast.SelectStmt:
		return "if", stmts(n.Body), true
	case *ast.CaseClause:
		return "if", stmtNodes(n.Body), true
	case *ast.CommClause:
		return "if", stmtNodes(n.Body), true
	case *ast.ForStmt:
		return "loop", stmts(n.Body), true
	case *ast.RangeStmt:
		return "loop", stmts(n.Body), true
	case *ast.InterfaceType:
		return "object", fields(n.Methods), true
	case *ast.StructType:
		return "object", fields(n.Fields), true
	case *ast.BlockStmt:
		return "", stmts(n), true
	case *ast.GenDecl, *ast.ValueSpec, *ast.TypeSpec, *ast.KeyValueExpr:
		return "", nil, true
	case ast.Stmt:
		return "stmt", nil, true
	}
	return "", nil, false
}

// isElseOf is whether path[j] is the else block of the if above it.
func isElseOf(path []ast.Node, j int) bool {
	if j == 0 {
		return false
	}
	i, ok := path[j-1].(*ast.IfStmt)
	return ok && i.Else == path[j]
}

func stmts(b *ast.BlockStmt) []ast.Node {
	if b == nil {
		return nil
	}
	return stmtNodes(b.List)
}

func stmtNodes(ss []ast.Stmt) []ast.Node {
	out := make([]ast.Node, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func exprs(es []ast.Expr) []ast.Node {
	out := make([]ast.Node, len(es))
	for i, e := range es {
		out[i] = e
	}
	return out
}

func fields(fl *ast.FieldList) []ast.Node {
	if fl == nil {
		return nil
	}
	out := make([]ast.Node, len(fl.List))
	for i, f := range fl.List {
		out[i] = f
	}
	return out
}

// compositeKind is a composite literal's kind: a slice's or an array's elements are an array, a map's and a struct's
// fields an object; without a type, keyed elements make an object.
func compositeKind(c *ast.CompositeLit) string {
	switch c.Type.(type) {
	case *ast.ArrayType:
		return "array"
	case nil:
		if len(c.Elts) > 0 {
			if _, ok := c.Elts[0].(*ast.KeyValueExpr); !ok {
				return "array"
			}
		}
	}
	return "object"
}

// goKind is the kind of an entry: the value it holds; a statement's own kind; a chain's link.
func goKind(n ast.Node) string {
	switch n := n.(type) {
	case linkSpan:
		return "link"
	case *ast.KeyValueExpr:
		return goKind(n.Value)
	case *ast.ExprStmt:
		return goKind(n.X)
	case *ast.FuncLit:
		return "func"
	case *ast.CompositeLit:
		return compositeKind(n)
	case *ast.CallExpr:
		return "call"
	case *ast.IfStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.CaseClause, *ast.CommClause, *ast.BlockStmt:
		return "if"
	case *ast.ForStmt, *ast.RangeStmt:
		return "loop"
	}
	return "stmt"
}

// goCond is where the condition of the if at path[j] gets its name, when its function declares it before the if in
// a block around it (the nearest, in scope): its line and its value. None when the if's own init declares it: the
// row shows that already.
func goCond(path []ast.Node, j int, lineOf func(token.Pos) int, text func(ast.Node) string) *ShapeCond {
	s := path[j].(*ast.IfStmt)
	name := condName(s.Cond)
	if name == "" {
		return nil
	}
	if id, _ := declares(s.Init, name); id != nil {
		return nil
	}
	for i := j - 1; i >= 0; i-- {
		var list []ast.Stmt // the statements of path[i] that run before the way on
		switch n := path[i].(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			return nil
		case *ast.BlockStmt:
			list = n.List
		case *ast.CaseClause:
			list = n.Body
		case *ast.CommClause:
			list = n.Body
		case *ast.IfStmt:
			list = []ast.Stmt{n.Init}
		case *ast.ForStmt:
			list = []ast.Stmt{n.Init}
		case *ast.SwitchStmt:
			list = []ast.Stmt{n.Init}
		case *ast.TypeSwitchStmt:
			list = []ast.Stmt{n.Init}
		}
		for k := len(list) - 1; k >= 0; k-- {
			if st := list[k]; st != nil && st.End() <= path[i+1].Pos() {
				if id, v := declares(st, name); id != nil {
					return &ShapeCond{Name: name, Line: lineOf(id.Pos()), Text: cutRow(text(v), "", false, maxRowText)}
				}
			}
		}
	}
	return nil
}

// declares is the identifier st declares as name (`name := v`, `var name = v`) and its value; nil none.
func declares(st ast.Stmt, name string) (*ast.Ident, ast.Expr) {
	var lhs, rhs []ast.Expr // nil st: neither
	switch st := st.(type) {
	case *ast.AssignStmt:
		if st.Tok == token.DEFINE {
			lhs, rhs = st.Lhs, st.Rhs
		}
	case *ast.DeclStmt:
		if g, ok := st.Decl.(*ast.GenDecl); ok {
			for _, sp := range g.Specs {
				if vs, ok := sp.(*ast.ValueSpec); ok && len(vs.Values) > 0 {
					for i, id := range vs.Names {
						if id.Name == name {
							return id, vs.Values[min(i, len(vs.Values)-1)]
						}
					}
				}
			}
		}
	}
	for i, l := range lhs {
		if id, ok := l.(*ast.Ident); ok && id.Name == name && len(rhs) > 0 {
			return id, rhs[min(i, len(rhs)-1)]
		}
	}
	return nil, nil
}

// condName is the variable a condition reads first: x of *x, !x, x.y, x != nil.
func condName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		if e.Name != "nil" && e.Name != "true" && e.Name != "false" {
			return e.Name
		}
	case *ast.StarExpr:
		return condName(e.X)
	case *ast.UnaryExpr:
		return condName(e.X)
	case *ast.ParenExpr:
		return condName(e.X)
	case *ast.SelectorExpr:
		return condName(e.X)
	case *ast.BinaryExpr:
		return cmp.Or(condName(e.X), condName(e.Y))
	}
	return ""
}
