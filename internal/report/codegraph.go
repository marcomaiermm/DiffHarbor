package report

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// cgQuery reads the review's codegraph indexes (.codegraph/codegraph.db, built by `codegraph init` before the
// review): the declarations called what the diff shows, and every edge into or out of one. Node's built-in SQLite,
// as Go's stdlib has none. A Go module below the root has an index of its own, as codegraph reads only the root's
// go.mod: its files are that one's, their paths prefixed with its directory.
const cgQuery = `
const { DatabaseSync } = require("node:sqlite");
const { names, dbs } = JSON.parse(require("fs").readFileSync(0, "utf8"));
const out = { nodes: [], edges: [] }, skip = "('file', 'import')", list = JSON.stringify(names);
const other = (p, own) => dbs.some(d => d.prefix.length > own.length && p.startsWith(d.prefix)); // a deeper index's
for (const { db: file, prefix } of dbs) {
  const db = new DatabaseSync(file, { readOnly: true });
  for (const n of db.prepare("select name, file_path path, start_line line from nodes where kind not in " + skip +
    " and name in (select value from json_each(?))").all(list)) {
    n.path = prefix + n.path;
    if (!other(n.path, prefix)) out.nodes.push(n);
  }
  for (const e of db.prepare("select e.line, s.name sname, s.kind skind, s.file_path spath, s.start_line sline," +
    " s.end_line send, t.name tname, t.file_path tpath, t.start_line tline from edges e join nodes s on s.id = e.source" +
    " join nodes t on t.id = e.target where e.kind in ('calls', 'references', 'instantiates', 'extends', 'implements')" +
    " and t.kind not in " + skip + " and (t.name in (select value from json_each(?1)) or s.name in (select value from json_each(?1)))").all(list)) {
    e.spath = prefix + e.spath;
    e.tpath = prefix + e.tpath;
    if (!other(e.spath, prefix) && !other(e.tpath, prefix)) out.edges.push(e);
  }
}
process.stdout.write(JSON.stringify(out));
`

type cgIndex struct {
	Nodes []struct {
		Name, Path string
		Line       int
	}
	Edges []struct {
		Line                int // 0: unknown, the source's first line that names it
		SName, SKind, SPath string
		SLine, SEnd         int
		TName, TPath        string
		TLine               int
	}
}

// codegraph is the indexes of the checkout at repo, the root's and those of the directories above paths, for the
// wanted names; nil without one.
func codegraph(repo string, wanted map[string]bool, paths []string) *cgIndex {
	type db struct {
		DB     string `json:"db"`
		Prefix string `json:"prefix"`
	}
	var dbs []db
	seen := map[string]bool{}
	for _, p := range paths {
		for d := p; d != "."; {
			d = path.Dir(d)
			f := filepath.Join(repo, filepath.FromSlash(d), ".codegraph", "codegraph.db")
			if seen[d] {
				break
			}
			seen[d] = true
			if _, err := os.Stat(f); err == nil {
				dbs = append(dbs, db{f, strings.TrimPrefix(d+"/", "./")})
			}
		}
	}
	if len(dbs) == 0 {
		return nil
	}
	in, _ := json.Marshal(map[string]any{"names": slices.Collect(maps.Keys(wanted)), "dbs": dbs})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "-e", cgQuery)
	cmd.Stdin = bytes.NewReader(in)
	out, err := cmd.Output()
	var idx cgIndex
	if err != nil || json.Unmarshal(out, &idx) != nil {
		return nil
	}
	return &idx
}

// exact replaces what the name scan guessed with the index's resolved edges, for the declarations the index knows:
// who names one (Refs, Uses, Files; Here, the lines of the diff's files; Outside, those of the others), what it
// names (Calls). Those it doesn't know, a Go struct's fields, keep the scan's.
func (idx *cgIndex) exact(repo string, changed map[string]bool, syms map[string][]Symbol) {
	type key struct{ path, name string }
	at := map[key][]*Symbol{}
	for name, ss := range syms {
		for i := range ss {
			at[key{ss[i].Path, name}] = append(at[key{ss[i].Path, name}], &ss[i])
		}
	}
	// the declaration at path called name, the one nearest line: the index counts from a decorator, a doc, …
	find := func(path, name string, line int) *Symbol {
		var best *Symbol
		for _, s := range at[key{path, name}] {
			if best == nil || abs(s.Line-line) < abs(best.Line-line) {
				best = s
			}
		}
		return best
	}
	type uses struct {
		refs, out []Ref
		seen      map[Ref]bool
		files     map[string]bool
		n, nOut   int
	}
	known := map[*Symbol]*uses{}
	for _, n := range idx.Nodes {
		if s := find(n.Path, n.Name, n.Line); s != nil && known[s] == nil {
			known[s] = &uses{seen: map[Ref]bool{}, files: map[string]bool{}}
		}
	}
	lines := sources(repo)
	for _, e := range idx.Edges {
		in := e.SName
		if e.SKind == "file" {
			in = ""
		}
		if from := find(e.SPath, e.SName, e.SLine); from != nil && known[from] != nil {
			if c := e.TPath + "\x00" + e.TName; !slices.Contains(from.Calls, c) && len(from.Calls) < maxCalls {
				from.Calls = append(from.Calls, c)
			}
		}
		to := find(e.TPath, e.TName, e.TLine)
		if to == nil || known[to] == nil {
			continue
		}
		line := e.Line
		if line == 0 {
			if line = lines.naming(e.SPath, e.TName, e.SLine, e.SEnd); line == 0 {
				continue
			}
		}
		u, r := known[to], Ref{Path: e.SPath, Line: line, In: in, Text: cut(strings.TrimSpace(lines.text(e.SPath, line)), maxText)}
		if u.seen[Ref{Path: r.Path, Line: r.Line}] {
			continue
		}
		u.seen[Ref{Path: r.Path, Line: r.Line}] = true
		u.n++
		u.files[r.Path] = true
		if len(u.refs) < maxRefs {
			u.refs = append(u.refs, r)
		}
		switch {
		case changed[r.Path]:
			to.Here = append(to.Here, r.Path+":"+strconv.Itoa(r.Line))
		case len(u.out) < maxRefs:
			u.out = append(u.out, r)
			fallthrough
		default:
			u.nOut++
		}
	}
	for s, u := range known {
		s.Refs, s.Uses, s.Files, s.Exact = u.refs, u.n, len(u.files), true
		s.Outside, s.NOutside = u.out, u.nOut
	}
}

func abs(n int) int { return max(n, -n) }

// files is the checkout's files by line, read when first asked.
type files struct {
	repo string
	got  map[string][]string
}

func sources(repo string) files { return files{repo, map[string][]string{}} }

func (fs files) lines(p string) []string {
	ls, ok := fs.got[p]
	if !ok {
		b, _ := os.ReadFile(filepath.Join(fs.repo, filepath.FromSlash(p)))
		ls = strings.Split(string(b), "\n")
		fs.got[p] = ls
	}
	return ls
}

func (fs files) text(p string, line int) string {
	if ls := fs.lines(p); line >= 1 && line <= len(ls) {
		return ls[line-1]
	}
	return ""
}

// naming is the first line from..to of p that names name; 0 for none.
func (fs files) naming(p, name string, from, to int) int {
	ls := fs.lines(p)
	for i := max(from, 1); i <= min(to, len(ls)); i++ {
		for _, w := range ident.FindAllString(ls[i-1], -1) {
			if w == name {
				return i
			}
		}
	}
	return 0
}
