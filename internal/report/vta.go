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
	// route, SKind "route", to its handler), reads and writes (to a store; TPath "" for one only named: a string, SQL),
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
			e.SPath, e.TPath = d.Prefix+e.SPath, d.Prefix+e.TPath
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
	return append(out, urlEdges(pkgs, rel)...), nil
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

// urlEdges are the calls in the module that pass a URL on: an argument that is a string constant starting with
// http:// or https:// (written there, or through a const), or read from the environment (os.Getenv, also through a
// variable). One per call, Kind "url"; a call that sends it is a "requests" edge, the walk in outside.go joins them.
func urlEdges(pkgs []*packages.Package, rel func(token.Position) string) []cgEdge {
	var out []cgEdge
	for _, pkg := range pkgs {
		u := urlFinder{pkg: pkg, rel: rel, src: map[types.Object]ast.Expr{}}
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

// urlFinder reads one package's arguments. src is a variable's one initializer: `u := os.Getenv("X")`.
type urlFinder struct {
	pkg *packages.Package
	rel func(token.Position) string
	src map[types.Object]ast.Expr
}

func (u urlFinder) record(n ast.Node) bool {
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
func (u urlFinder) at(p token.Pos) string {
	pos := u.pkg.Fset.Position(p)
	if r := u.rel(pos); r != "" {
		return r + ":" + strconv.Itoa(pos.Line)
	}
	return ""
}

// find is the URL e passes: the constant's text and the line it is declared on, or the environment variable and the
// line it is read on; none of either when e is neither.
func (u urlFinder) find(e ast.Expr, depth int) (url, env, where string) {
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

// calls are the url edges of the calls in fd, a closure's counted as fd's.
func (u urlFinder) calls(fd *ast.FuncDecl) []cgEdge {
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
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		for i := 0; ok && i < len(c.Args); i++ {
			if url, env, where := u.find(c.Args[i], 0); url != "" || env != "" {
				out = append(out, cgEdge{
					Line: fset.Position(c.Lparen).Line, Kind: "url", By: "vta", SName: fd.Name.Name,
					SKind: kind, SPath: u.rel(sp), SLine: sp.Line, SEnd: ep.Line, URL: url, Env: env, URLAt: where,
				})
				break
			}
		}
		return true
	})
	return out
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
