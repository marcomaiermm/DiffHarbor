package report

import (
	"context"
	"diffharbor/internal/proc"
	"encoding/json"
	"errors"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// cgEdge is one use the index resolved: who names what, on which line.
type cgEdge struct {
	Line int // 0: unknown, the source's first line that names it
	Kind string
	By   string // "vta": from VTA below, "ts": from tsEdges; "": codegraph's
	// Kind: codegraph's (calls, references, instantiates, extends, implements), and tsEdges' renders, handles (a
	// route, SKind "route", to its handler), reads and writes (to a store, VTA's too: SQL; TPath "" for one only named),
	// requests and url (see below)
	SName, SKind, SPath string
	SLine, SEnd         int
	TName, TPath        string
	TLine               int
	// requests: a call that sends a network request, its TName the callee (TPath ""); url: a call that passes a URL
	// on. The URL's text or the environment variable it is read from, and where that stands (path:line); see outside.go.
	URL, Env, URLAt string
}

// vtaTimeout bounds VTA on one module: loading and type-checking a large repo takes long and much memory.
const vtaTimeout = 2 * time.Minute

// vtaEdges adds the calls VTA resolves in the Go modules of the indexes: all through an interface or a function
// value, which the index leaves unresolved and a walk goes on from, and the static ones of the wanted names. Run as
// `diffharbor vta` in sb, the PR's go list as the repo's user. A module that doesn't type-check, or past vtaTimeout,
// adds none: the index's alone, and a log line why.
func (idx *cgIndex) vtaEdges(sb *proc.Sandbox, repo string, wanted []string) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	in, _ := json.Marshal(wanted)
	for _, d := range idx.dbs {
		dir := filepath.Join(repo, filepath.FromSlash(d.Prefix))
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), vtaTimeout)
		cmd := sb.Command(ctx, dir, append(sb.ToolEnv(), "GOMEMLIMIT=2GiB"), self, "vta")
		cmd.Stdin = strings.NewReader(string(in))
		var stderr strings.Builder
		cmd.Stderr = &stderr
		start := time.Now()
		out, err := cmd.Output()
		cancel()
		var got []cgEdge
		if err == nil {
			err = json.Unmarshal(out, &got)
		}
		if err != nil {
			log.Printf("vta %s: codegraph alone: %v %s", d.Prefix, err, cut(stderr.String(), 400))
			continue
		}
		for _, e := range got {
			e.SPath = d.Prefix + e.SPath
			if e.TPath != "" { // a request or a url has no target in the module
				e.TPath = d.Prefix + e.TPath
			}
			idx.Edges = append(idx.Edges, e)
		}
		log.Printf("vta %s: %d edges in %s", d.Prefix, len(got), time.Since(start).Round(time.Millisecond))
	}
}

// VTA is `diffharbor vta`: the calls of the Go module in the working directory, by VTA on its SSA, whose caller or
// callee is one of the names on stdin (a JSON list) or that go through an interface or a function value, as JSON
// cgEdges with paths relative to the module. Both ends in the module, test files aside; a closure counts as the
// declaration it is in. It fails when a package doesn't type-check: a partial program resolves calls wrongly.
func VTA() error {
	var names []string
	if err := json.NewDecoder(os.Stdin).Decode(&names); err != nil {
		return err
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	out, err := vtaCalls(dir, names)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

func vtaCalls(dir string, names []string) ([]cgEdge, error) {
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[n] = true
	}
	cfg := &packages.Config{Mode: packages.LoadAllSyntax | packages.NeedModule, Dir: dir}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	if packages.PrintErrors(pkgs) > 0 {
		return nil, errors.New("packages don't type-check")
	}
	if len(pkgs) == 0 || pkgs[0].Module == nil {
		return nil, errors.New("no module")
	}
	mod := pkgs[0].Module.Path
	prog, _ := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()
	cg := vta.CallGraph(ssautil.AllFunctions(prog), cha.CallGraph(prog))
	own := func(f *ssa.Function) *ssa.Function { // the declaration f is in, nil outside the module
		if o := f.Origin(); o != nil { // a generic function's instance
			f = o
		}
		for f.Parent() != nil {
			f = f.Parent()
		}
		if f.Synthetic != "" || f.Pkg == nil || f.Syntax() == nil {
			return nil
		}
		if p := f.Pkg.Pkg.Path(); p != mod && !strings.HasPrefix(p, mod+"/") {
			return nil
		}
		return f
	}
	rel := func(p token.Position) string {
		r, err := filepath.Rel(dir, p.Filename)
		if err != nil || strings.HasPrefix(r, "..") || strings.HasSuffix(r, "_test.go") {
			return ""
		}
		return filepath.ToSlash(r)
	}
	static := func(e *callgraph.Edge) bool { return e.Site != nil && e.Site.Common().StaticCallee() != nil } // the index has those
	type key struct {
		s, t *ssa.Function
		line int
	}
	seen := map[key]bool{}
	out := []cgEdge{}
	decl := func(s *ssa.Function, at token.Position) (cgEdge, bool) { // the declaration s, an edge's source
		sp := prog.Fset.Position(s.Syntax().Pos())
		kind := "function"
		if s.Signature.Recv() != nil {
			kind = "method"
		}
		return cgEdge{
			Line: at.Line, By: "vta", SName: s.Name(), SKind: kind, SPath: rel(sp), SLine: sp.Line,
			SEnd: prog.Fset.Position(s.Syntax().End()).Line,
		}, rel(sp) != "" && rel(at) == rel(sp)
	}
	for f, n := range cg.Nodes {
		s := own(f)
		if s == nil {
			continue
		}
		for _, e := range n.Out {
			if senders[e.Callee.Func.String()] { // net/http's, also through an interface VTA resolves to it
				at := prog.Fset.Position(e.Pos())
				k := key{s, e.Callee.Func, at.Line}
				if c, ok := decl(s, at); ok && !seen[k] {
					seen[k] = true
					c.Kind, c.TName = "requests", e.Callee.Func.Name()
					out = append(out, c)
				}
				continue
			}
			t := own(e.Callee.Func)
			if e.Callee.Func.Parent() != nil || t == nil || (!wanted[s.Name()] && !wanted[t.Name()] && static(e)) {
				continue
			}
			at := prog.Fset.Position(e.Pos())
			sp, tp := prog.Fset.Position(s.Syntax().Pos()), prog.Fset.Position(t.Syntax().Pos())
			k := key{s, t, at.Line}
			if seen[k] || rel(sp) == "" || rel(tp) == "" || rel(at) != rel(sp) {
				continue
			}
			seen[k] = true
			c, _ := decl(s, at)
			c.Kind, c.TName, c.TPath, c.TLine = "calls", t.Name(), rel(tp), tp.Line
			out = append(out, c)
		}
	}
	return append(out, argEdges(pkgs, mod, rel)...), nil
}

// senders are the functions that send a request over the network: net/http's client and its package functions.
var senders = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range []string{"Get", "Post", "PostForm", "Head"} {
		m["net/http."+n], m["(*net/http.Client)."+n] = true, true
	}
	m["(*net/http.Client).Do"] = true
	return m
}()

// argEdges are the calls in the module that pass a URL or a SQL statement on. A URL: an argument that is a string
// constant starting with http:// or https:// (written there, or through a const), or read from the environment
// (os.Getenv, also through a variable). One per call, Kind "url"; a call that sends it is a "requests" edge, the walk
// in outside.go joins them. A store, a "reads" or "writes" edge, one per chain of calls: a SQL statement, a string
// constant (sqlc's generated ones too) to a call that takes one, to its table, TPath ""; or an ORM's chain (gorm's,
// bun's), see store.
func argEdges(pkgs []*packages.Package, mod string, rel func(token.Position) string) []cgEdge {
	var out []cgEdge
	for _, pkg := range pkgs {
		u := argFinder{pkg: pkg, mod: mod, rel: rel, src: map[types.Object]ast.Expr{}}
		for _, f := range pkg.Syntax {
			ast.Inspect(f, u.record)
		}
		for _, f := range pkg.Syntax {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
					out = append(out, u.calls(fd)...)
				}
			}
		}
	}
	return out
}

// argFinder reads one package's arguments. src is a variable's one initializer: `u := os.Getenv("X")`.
type argFinder struct {
	pkg *packages.Package
	mod string
	rel func(token.Position) string
	src map[types.Object]ast.Expr
}

func (u argFinder) record(n ast.Node) bool {
	var lhs, rhs []ast.Expr
	switch n := n.(type) {
	case *ast.AssignStmt:
		lhs, rhs = n.Lhs, n.Rhs
	case *ast.ValueSpec:
		for _, id := range n.Names {
			lhs = append(lhs, id)
		}
		rhs = n.Values
	}
	if len(lhs) == len(rhs) {
		for i, l := range lhs {
			if id, ok := l.(*ast.Ident); ok && u.pkg.TypesInfo.ObjectOf(id) != nil {
				u.src[u.pkg.TypesInfo.ObjectOf(id)] = rhs[i]
			}
		}
	}
	return true
}

// at is "path:line" of p, "" outside the module.
func (u argFinder) at(p token.Pos) string {
	pos := u.pkg.Fset.Position(p)
	if r := u.rel(pos); r != "" {
		return r + ":" + strconv.Itoa(pos.Line)
	}
	return ""
}

// find is the URL e passes: the constant's text and the line it is declared on, or the environment variable and the
// line it is read on; none of either when e is neither.
func (u argFinder) find(e ast.Expr, depth int) (url, env, where string) {
	info := u.pkg.TypesInfo
	if depth > 3 {
		return
	}
	if tv := info.Types[e]; tv.Value != nil && tv.Value.Kind() == constant.String {
		v := constant.StringVal(tv.Value)
		if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			return
		}
		pos := e.Pos()
		switch x := e.(type) { // a const's own line, not the place that names it
		case *ast.Ident:
			if c, ok := info.Uses[x].(*types.Const); ok {
				pos = c.Pos()
			}
		case *ast.SelectorExpr:
			if c, ok := info.Uses[x.Sel].(*types.Const); ok {
				pos = c.Pos()
			}
		}
		return v, "", u.at(pos)
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		return u.find(x.X, depth)
	case *ast.Ident:
		if i, ok := u.src[info.Uses[x]]; ok {
			return u.find(i, depth+1)
		}
	case *ast.BinaryExpr: // base + "/path": the left names the host
		if x.Op == token.ADD {
			if url, env, where = u.find(x.X, depth); url == "" && env == "" {
				return u.find(x.Y, depth)
			}
		}
	case *ast.CallExpr:
		if f, ok := calleeOf(info, x).(*types.Func); ok && f.FullName() == "os.Getenv" && len(x.Args) == 1 {
			if tv := info.Types[x.Args[0]]; tv.Value != nil && tv.Value.Kind() == constant.String {
				return "", constant.StringVal(tv.Value), u.at(x.Pos())
			}
		}
	}
	return
}

// stmt is a SQL statement's verb and its table, past leading comments (sqlc writes "-- name: …" first). An update
// counts with its set only: "update failed" is a message; a function is no table: from unnest($1). ponytail: no WITH, the first table of a join only.
var stmt = regexp.MustCompile(`(?is)^(?:\s*(?:--[^\n]*|/\*.*?\*/))*\s*(?:(select)\b.*?\bfrom|insert\s+into|delete\s+from|(update))\s+["\x60]?([A-Za-z_][\w.]*)["\x60]?(\s*\()?(\s+set\b)?`)

// sqlCall names the calls that take one: database/sql's, pgx's, sqlx's, an ORM's Raw; not fmt.Errorf("select … from").
var sqlCall = regexp.MustCompile(`^(Must)?(Exec|Query|Prepare|Select|Get|Raw|NewRaw|Named)`)

// ormWrite and ormRead are an ORM's verbs, gorm's and bun's (NewInsert…Exec); a chain that writes is a write.
var (
	ormWrite = regexp.MustCompile(`^(Create|Save|Update|Delete|Insert|Upsert|FirstOrCreate|NewInsert|NewUpdate|NewDelete|NewMerge)`)
	ormRead  = regexp.MustCompile(`^(Find|First|Last|Take|Scan|Count|Pluck|Exists|NewSelect)`)
)

// sql is the string constant e is, also through a variable's one initializer; "" when it is none.
func (u argFinder) sql(e ast.Expr, depth int) string {
	if tv := u.pkg.TypesInfo.Types[e]; tv.Value != nil && tv.Value.Kind() == constant.String {
		return constant.StringVal(tv.Value)
	}
	if id, ok := ast.Unparen(e).(*ast.Ident); ok && depth < 3 {
		if i, ok := u.src[u.pkg.TypesInfo.Uses[id]]; ok {
			return u.sql(i, depth+1)
		}
	}
	return ""
}

// calls are the url, reads and writes edges of the calls in fd, a closure's counted as fd's.
func (u argFinder) calls(fd *ast.FuncDecl) []cgEdge {
	fset := u.pkg.Fset
	sp, ep := fset.Position(fd.Pos()), fset.Position(fd.End())
	if u.rel(sp) == "" {
		return nil
	}
	kind := "function"
	if fd.Recv != nil {
		kind = "method"
	}
	var out []cgEdge
	done := map[*ast.CallExpr]bool{} // a chain's inner calls: the outer one, seen first, stands for them
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		edge := cgEdge{
			Line: fset.Position(c.Lparen).Line, By: "vta", SName: fd.Name.Name, SKind: kind, SPath: u.rel(sp),
			SLine: sp.Line, SEnd: ep.Line,
		}
		for _, a := range c.Args {
			if url, env, where := u.find(a, 0); url != "" || env != "" {
				e := edge
				e.Kind, e.URL, e.Env, e.URLAt = "url", url, env, where
				out = append(out, e)
				break
			}
		}
		if done[c] {
			return true
		}
		var chain []*ast.CallExpr
		for x := c; x != nil; {
			chain, done[x] = append(chain, x), true
			sel, _ := ast.Unparen(x.Fun).(*ast.SelectorExpr)
			x = nil
			if sel != nil {
				x, _ = ast.Unparen(sel.X).(*ast.CallExpr)
			}
		}
		slices.Reverse(chain) // innermost first: db.Model(&u) before .Update(…)
		edge.Line = fset.Position(c.Pos()).Line
		if e, ok := u.store(chain, edge); ok {
			out = append(out, e)
		}
		return true
	})
	return out
}

// store is the store a chain of calls reads or writes: Kind, TName and, for a type of the module, TPath and TLine.
// A SQL statement to a call that takes one; else an ORM's (a package's, not the platform's) verbs in the chain and
// the store its arguments name, nearest the client first: Table("users"), or a struct of the module, through pointers
// and slices (Create(&u), Find(&users), Model(&User{})). ponytail: one chain; a query built over statements
// (`tx := db.Model(&u)`, then `tx.Update(…)`) names none.
func (u argFinder) store(chain []*ast.CallExpr, e cgEdge) (cgEdge, bool) {
	info := u.pkg.TypesInfo
	for _, c := range chain {
		if f := calleeOf(info, c); f != nil && sqlCall.MatchString(f.Name()) {
			for _, a := range c.Args {
				if m := stmt.FindStringSubmatch(u.sql(a, 0)); m != nil && (m[1] == "" || m[4] == "") && (m[2] == "" || m[5] != "") {
					e.Kind, e.TName = "writes", m[3]
					if m[1] != "" {
						e.Kind = "reads"
					}
					return e, true
				}
			}
		}
	}
	var write, read bool
	for _, c := range chain {
		f, _ := calleeOf(info, c).(*types.Func)
		if f == nil || f.Pkg() == nil || !strings.Contains(strings.Split(f.Pkg().Path(), "/")[0], ".") || u.own(f.Pkg()) {
			continue
		}
		write, read = write || ormWrite.MatchString(f.Name()), read || ormRead.MatchString(f.Name())
		for _, a := range c.Args {
			if e.TName != "" {
				break
			}
			if tv := info.Types[a]; f.Name() == "Table" && tv.Value != nil && tv.Value.Kind() == constant.String {
				e.TName = constant.StringVal(tv.Value)
			} else if n := u.model(info.TypeOf(a)); n != nil {
				p := u.pkg.Fset.Position(n.Obj().Pos())
				e.TName, e.TPath, e.TLine = n.Obj().Name(), u.rel(p), p.Line
			}
		}
	}
	if e.TName == "" || !write && !read {
		return e, false
	}
	e.Kind = "reads"
	if write {
		e.Kind = "writes"
	}
	return e, true
}

// own is whether p is the module's.
func (u argFinder) own(p *types.Package) bool {
	return p.Path() == u.mod || strings.HasPrefix(p.Path(), u.mod+"/")
}

// model is the module's struct type t is, through pointers and slices; nil when it is none.
func (u argFinder) model(t types.Type) *types.Named {
	for t != nil {
		switch x := types.Unalias(t).(type) {
		case *types.Pointer:
			t = x.Elem()
		case *types.Slice:
			t = x.Elem()
		case *types.Named:
			if _, ok := x.Underlying().(*types.Struct); ok && x.Obj().Pkg() != nil && u.own(x.Obj().Pkg()) &&
				u.rel(u.pkg.Fset.Position(x.Obj().Pos())) != "" {
				return x
			}
			return nil
		default:
			return nil
		}
	}
	return nil
}

// calleeOf is the object a call names: a function or a method, nil for a value.
func calleeOf(info *types.Info, c *ast.CallExpr) types.Object {
	switch f := ast.Unparen(c.Fun).(type) {
	case *ast.Ident:
		return info.Uses[f]
	case *ast.SelectorExpr:
		return info.Uses[f.Sel]
	}
	return nil
}
