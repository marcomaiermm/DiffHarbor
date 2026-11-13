package report

import (
	"context"
	"diffharbor/internal/proc"
	"encoding/json"
	"errors"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	// route, SKind "route", to its handler), reads and writes (to a store; TPath "" for one only named: a string, SQL)
	SName, SKind, SPath string
	SLine, SEnd         int
	TName, TPath        string
	TLine               int
}

// vtaTimeout bounds VTA on one module: loading and type-checking a large repo takes long and much memory.
const vtaTimeout = 2 * time.Minute

// vtaEdges adds the calls VTA resolves in the Go modules of the indexes, calls through an interface or a function
// value the index leaves unresolved, for the wanted names. Run as `diffharbor vta` in sb, the PR's go list as the
// repo's user. A module that doesn't type-check, or past vtaTimeout, adds none: the index's alone, and a log line why.
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
// callee is one of the names on stdin (a JSON list), as JSON cgEdges with paths relative to the module. Both ends in
// the module, test files aside; a closure counts as the declaration it is in. It fails when a package doesn't
// type-check: a partial program resolves calls wrongly.
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
	type key struct {
		s, t *ssa.Function
		line int
	}
	seen := map[key]bool{}
	out := []cgEdge{}
	for f, n := range cg.Nodes {
		s := own(f)
		if s == nil {
			continue
		}
		for _, e := range n.Out {
			t := own(e.Callee.Func)
			if e.Callee.Func.Parent() != nil || t == nil || (!wanted[s.Name()] && !wanted[t.Name()]) {
				continue
			}
			at := prog.Fset.Position(e.Pos())
			sp, tp := prog.Fset.Position(s.Syntax().Pos()), prog.Fset.Position(t.Syntax().Pos())
			k := key{s, t, at.Line}
			if seen[k] || rel(sp) == "" || rel(tp) == "" || rel(at) != rel(sp) {
				continue
			}
			seen[k] = true
			kind := "function"
			if s.Signature.Recv() != nil {
				kind = "method"
			}
			out = append(out, cgEdge{Line: at.Line, Kind: "calls", By: "vta", SName: s.Name(), SKind: kind,
				SPath: rel(sp), SLine: sp.Line, SEnd: prog.Fset.Position(s.Syntax().End()).Line,
				TName: t.Name(), TPath: rel(tp), TLine: tp.Line})
		}
	}
	return out, nil
}
