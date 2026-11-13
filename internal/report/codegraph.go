package report

import (
	"bytes"
	"context"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"encoding/json"
	"maps"
	"os"
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
  for (const n of db.prepare("select name, file_path path, start_line line, end_line end from nodes where kind not in " + skip +
    " and name in (select value from json_each(?))").all(list)) {
    n.path = prefix + n.path;
    if (!other(n.path, prefix)) out.nodes.push(n);
  }
  for (const e of db.prepare("select e.line, e.kind, s.name sname, s.kind skind, s.file_path spath, s.start_line sline," +
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
	dbs   []cgDB
	Nodes []struct {
		Name, Path string
		Line, End  int
	}
	Edges []cgEdge
}

// codegraph is the indexes of the checkout at repo, the root's and those of the directories above paths, for the
// wanted names; nil without one.
func codegraph(sb *proc.Sandbox, repo string, wanted map[string]bool, paths []string) *cgIndex {
	var dbs []cgDB
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
				dbs = append(dbs, cgDB{f, strings.TrimPrefix(d+"/", "./")})
			}
		}
	}
	if len(dbs) == 0 {
		return nil
	}
	idx := cgIndex{dbs: dbs}
	names := slices.Collect(maps.Keys(wanted))
	if !runNode(sb, cgQuery, map[string]any{"names": names, "dbs": dbs}, &idx) {
		return nil
	}
	idx.vtaEdges(sb, repo, names)
	idx.tsEdges(sb, repo)
	return &idx
}

type cgDB struct {
	DB     string `json:"db"`
	Prefix string `json:"prefix"`
}

// runNode runs script with in as its stdin, its stdout into out; false when it fails or takes past a minute. In sb, as
// the repo's user: it opens the PR's indexes and files, and their symlinks (another repo's index) reach nothing of
// root's.
func runNode(sb *proc.Sandbox, script string, in, out any) bool {
	b, _ := json.Marshal(in)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := sb.Command(ctx, "/", nil, "node", "-e", script)
	cmd.Stdin = bytes.NewReader(b)
	got, err := cmd.Output()
	return err == nil && json.Unmarshal(got, out) == nil
}

// exact replaces what the name scan guessed with the index's resolved edges, for the declarations the index knows:
// its lines (From, To), who names one (Refs, Uses, Files; Here, the lines of the diff's files; Outside, those of
// the others), what it names (Calls). Those it doesn't know, a Go struct's fields, keep the scan's.
func (idx *cgIndex) exact(root *os.Root, changed map[string]bool, syms map[string][]Symbol) {
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
			s.From, s.To = n.Line, n.End
		}
	}
	lines := sources(root)
	for _, e := range idx.Edges {
		in := e.SName
		if e.SKind == "file" {
			in = ""
		}
		if e.TPath == "" { // a store only named (a string, SQL's table): no declaration to point at
			continue
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

// added is the edges VTA and the checker add that point at a declaration: cgTests walks them with the index's.
func (idx *cgIndex) added() []cgEdge {
	var es []cgEdge
	for _, e := range idx.Edges {
		if e.By != "" && e.TPath != "" {
			es = append(es, e)
		}
	}
	return es
}

// cgTests is which tests reach each of the starts, the diff's declarations an index knows. tests: by the index's
// resolved uses and those VTA and the checker add (extra: their routes start ways), its callers' and theirs, nearest first, test code nothing calls: the runner does; partial past maxNodes. untested: not even a guess reaches a
// test. That guess is every line of the checkout that names it (git grep), what declaration that line is in, the
// lines that name that one and so on: a call in a macro, through an interface, by reflection is one the index
// misses. A line in a file no index read, or at a file's top level, may run anything: no claim, as past maxNames
// names or a name in more than maxMentions lines, as common as "Run". A test: the conventions of codegraph's
// isTestPath, a marker on or just above the declaration, or below a #[cfg(test)]. ways: how the code reaches it,
// see ways below.
const cgTests = `
const { DatabaseSync } = require("node:sqlite");
const { execFileSync } = require("child_process");
const fs = require("fs"), base = require("path").basename;
const { repo, dbs, starts, extra } = JSON.parse(fs.readFileSync(0, "utf8"));
const byTarget = new Map(); // "path NUL name": the edges VTA and the checker add into it, the index doesn't have
for (const e of extra || []) { const k = e.TPath + "\0" + e.TName; byTarget.set(k, [...(byTarget.get(k) || []), e]) }
const maxNodes = 5000, maxNames = 2000, maxMentions = 1000, maxLines = 50000;
const open = dbs.map(d => {
  const db = new DatabaseSync(d.db, { readOnly: true });
  return { prefix: d.prefix, files: new Set(db.prepare("select path from files").all().map(r => r.path)),
    named: db.prepare("select id, kind, start_line line from nodes where file_path = ? and name = ? and kind not in ('file', 'import')"),
    node: db.prepare("select file_path path, name, start_line line, end_line end from nodes where id = ?"),
    file: db.prepare("select id, start_line line from nodes where file_path = ? and kind = 'file'"),
    into: db.prepare("select s.id, s.kind, s.name, s.file_path path, s.start_line line, e.line at from edges e join nodes s on s.id = e.source" +
      " where e.target = ? and e.kind in ('calls', 'references', 'instantiates')"),
    around: db.prepare("select kind, name, start_line line from nodes where file_path = :f and start_line <= :l and end_line >= :l" +
      " and kind != 'file' order by start_line desc, end_line limit 1") };
}).sort((a, b) => b.prefix.length - a.prefix.length);
const owner = p => open.find(d => p.startsWith(d.prefix));
// into is who names id: the index's edges and the added ones, a route's (SKind "route") as a caller of its own
const into = (d, id) => {
  d.memo ??= new Map();
  if (d.memo.has(id)) return d.memo.get(id);
  const route = String(id).startsWith("route\0"), rows = route ? [] : d.into.all(id), n = !route && d.node.get(id);
  for (const e of (n && byTarget.get(d.prefix + n.path + "\0" + n.name)) || []) {
    if (e.TLine < n.line || e.TLine > n.end || !e.SPath.startsWith(d.prefix)) continue; // another of that name
    const sp = e.SPath.slice(d.prefix.length);
    if (e.SKind === "route") {
      rows.push({ id: "route\0" + e.SPath + "\0" + e.SName, kind: "route", name: e.SName, path: sp, line: e.SLine, at: e.Line, route: e.SName });
      continue;
    }
    const s = e.SKind === "file" ? d.file.get(sp) : d.named.all(sp, e.SName).sort((a, b) => Math.abs(a.line - e.SLine) - Math.abs(b.line - e.SLine))[0];
    if (s && !rows.some(r => r.id === s.id)) rows.push({ id: s.id, kind: s.kind || "file", name: e.SName, path: sp, line: s.line, at: e.Line });
  }
  d.memo.set(id, rows);
  return rows;
};
const src = new Map();
const lines = p => { if (!src.has(p)) { let t = ""; try { t = fs.readFileSync(repo + "/" + p, "utf8") } catch {} src.set(p, t.split("\n")) } return src.get(p) };
const testPath = p => { // codegraph's isTestPath
  const lower = p.toLowerCase(), name = base(p), ln = name.toLowerCase();
  return ln.startsWith("test_") || ln.startsWith("test.") || /[._-](test|tests|spec|specs)\.[a-z0-9]+$/.test(ln) ||
    /(?:Test|Tests|TestCase|Tester|Spec|Specs)\.[A-Za-z0-9]+$/.test(name) ||
    ["/tests/", "/test/", "/__tests__/", "/spec/", "/specs/", "/testlib/", "/testing/", "/e2e/"].some(d => lower.includes(d)) ||
    ["test/", "tests/", "spec/", "specs/", "e2e/"].some(d => lower.startsWith(d)) ||
    /(?:^|\/)[A-Za-z0-9]*(?:Test|Tests|Spec)\//.test(p) ||
    /(?:^|\/)(?:[\w.]+[-_]test(?:s|ing)?|testdata|testutils?|test[-_]utils?|fakes?|mocks?|__mocks__|stubs)\//.test(lower);
};
const mark = /#\[\s*(?:[\w:]+::)?(?:test|rstest|test_case|quickcheck)\b|\[\s*(?:Fact|Theory|Test|TestMethod|DataTestMethod|TestCase\w*)\b|@(?:Test|ParameterizedTest|RepeatedTest|TestFactory|TestTemplate|pytest\.mark\.\w+)\b/;
const cfgTest = /#\[\s*cfg\s*\(\s*test\s*\)\s*\]/;
const isTest = (p, line) => { const ls = lines(p); return testPath(p) || mark.test(ls.slice(Math.max(line - 4, 0), line).join("\n")) || ls.slice(0, line).some(l => cfgTest.test(l)) };
const comment = /^\s*(?:\/\/|\/\*|\*|--|;|%|<!--|"""|'''|#(?![\[!]|\s*(?:include|define|if|ifdef|ifndef|pragma|import)\b))/;
const doc = p => /\.(?:md|mdx|markdown|txt|rst|adoc|org)$/i.test(p) || /(?:^|\/)(?:docs?|LICENSE[^/]*|CHANGELOG[^/]*)(?:\/|$)/i.test(p);

function strict(d, id) {
  const seen = new Set([id]), q = [id], tests = [];
  let partial = false;
  for (let i = 0; i < q.length && !partial; i++) {
    for (const s of into(d, q[i])) {
      if (seen.has(s.id)) continue;
      if (seen.size >= maxNodes) { partial = true; break }
      seen.add(s.id);
      const p = d.prefix + s.path;
      if (!isTest(p, s.line) || into(d, s.id).length) { q.push(s.id); continue } // a test helper, an interface's: on to the test
      const t = s.kind === "file" ? base(p) : base(p) + " " + s.name;
      if (!tests.includes(t)) tests.push(t);
    }
  }
  return { tests, partial };
}

const mentions = new Map(); // name -> [path, line][], null: in too many
function grep(names) {
  const want = new Set(names.filter(n => !mentions.has(n)));
  if (!want.size) return;
  for (const n of want) mentions.set(n, []);
  let out = "";
  try {
    out = execFileSync("git", ["-C", repo, "grep", "-n", "-z", "-w", "-F", "-I", "-f", "-"], { input: [...want].join("\n"), maxBuffer: 1 << 28, encoding: "utf8" });
  } catch (e) { if (e.status !== 1) throw e } // 1: no line
  for (const row of out.split("\n")) {
    const [p, line, text] = row.split("\0");
    if (text === undefined) continue;
    for (const w of new Set(text.match(/[A-Za-z0-9_$]+/g))) {
      const ms = mentions.get(w);
      if (!want.has(w) || !ms) continue;
      if (ms.push([p, +line]) > maxMentions) mentions.set(w, null);
    }
  }
}

function untested(name) {
  const seen = new Set([name]);
  let level = [name], n = 0;
  while (level.length) {
    grep(level);
    const next = [];
    for (const w of level) {
      const ms = mentions.get(w);
      if (!ms || (n += ms.length) > maxLines) return false;
      for (const [p, line] of ms) {
        const d = owner(p), f = d && p.slice(d.prefix.length);
        if (!d || !d.files.has(f)) { if (doc(p)) continue; return false }
        const at = d.around.get({ f, l: line });
        if (!at) { if (comment.test(lines(p)[line - 1] || "")) continue; return false }
        if (at.kind === "import") continue;
        if (isTest(p, at.line)) return false;
        if (seen.has(at.name)) continue;
        if (seen.size >= maxNames) return false;
        seen.add(at.name);
        next.push(at.name);
      }
    }
    level = next;
  }
  return true;
}

// a call that registers a route: its name, the method maybe in the path ("GET /x", Go's), the path
const verbs = /^(?:get|post|put|patch|delete|head|options|all|handle\w*|route|\w*mapping)$/i;
const reg = /\b(\w+)\s*\(\s*["'\x60](?:(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+)?(\/[^"'\x60\s]*)["'\x60]/g;
// routeAt is the route registered on line at of p, or by a line around it, a closure's: from is where its declaration starts
function routeAt(p, from, at) {
  const ls = lines(p), ind = l => l.length - l.trimStart().length;
  let min = Infinity;
  for (let i = at - 1; i >= Math.max(from - 1, 0); i--) {
    const l = ls[i] || "";
    if (!l.trim() || ind(l) >= min) continue; // a statement beside it, not around it
    min = ind(l);
    for (const m of l.matchAll(reg)) {
      if (!verbs.test(m[1])) continue;
      const by = /^(get|post|put|patch|delete|head|options)/i.exec(m[1]);
      return { method: m[2] || (by ? by[1].toUpperCase() : "ANY"), route: m[3], line: i + 1 };
    }
  }
  return null;
}

// ways is how the code reaches start: up its callers, tests aside, to a route that runs one or to a declaration
// nothing calls, the nearest first. A way's chain runs from the first it runs down to start, each hop with the line
// of the one before that names it (in, at). Past maxWalk callers or maxDepth hops, no more.
const maxWays = 4, maxWalk = 400, maxDepth = 8;
function ways(d, start, name, path, line) {
  const info = new Map([[start, { name, path, line }]]), next = new Map([[start, null]]), depth = new Map([[start, 0]]);
  const q = [start], out = [], seen = new Set();
  const chain = (id, inn, at) => {
    const c = [];
    for (; id != null; ) {
      const { name, path, line } = info.get(id);
      c.push({ name, path, line, in: inn, at });
      const n = next.get(id);
      if (!n) break;
      [id, inn, at] = [n.to, info.get(id).path, n.at];
    }
    return c;
  };
  for (let i = 0; i < q.length && out.length < maxWays; i++) {
    const id = q[i], callers = into(d, id).filter(s => !isTest(d.prefix + s.path, s.line));
    if (!callers.length) {
      const top = info.get(id);
      // a function or a file's top level: a method nothing calls is an interface's, a variable runs nothing itself
      if (i && !seen.has(id) && (top.kind === "function" || top.kind === "file"))
        out.push({ method: "CALL", route: top.name, path: top.path, line: top.line, chain: chain(id, "", 0).slice(1) });
      seen.add(id);
      continue;
    }
    if (depth.get(id) >= maxDepth) continue;
    for (const s of callers) {
      const p = d.prefix + s.path, by = s.route && /^(\S+)\s+(.*)$/.exec(s.route); // "get /orders", "query posts"
      const r = by ? { method: by[1].toUpperCase(), route: by[2], line: s.line } : s.at && routeAt(p, s.line, s.at);
      if (r) {
        const k = r.method + " " + r.route + " " + p;
        if (!seen.has(k) && out.length < maxWays) seen.add(k), out.push({ ...r, path: p, chain: chain(id, p, s.at) });
        continue;
      }
      if (next.has(s.id) || next.size >= maxWalk) continue;
      info.set(s.id, { name: s.kind === "file" ? base(p) : s.name, path: p, line: s.line, kind: s.kind });
      next.set(s.id, { to: id, at: s.at || 0 });
      depth.set(s.id, depth.get(id) + 1);
      q.push(s.id);
    }
  }
  return out;
}

const out = [], known = new Map();
for (const { path, name } of starts) {
  const d = owner(path);
  if (!d) continue;
  for (const n of d.named.all(path.slice(d.prefix.length), name)) {
    if (!known.has(name)) { try { known.set(name, untested(name)) } catch { known.set(name, false) } }
    out.push({ path, name, line: n.line, ...strict(d, n.id), untested: known.get(name), ways: ways(d, n.id, name, path, n.line) });
  }
}
process.stdout.write(JSON.stringify(out));
`

// tests sets the Tests, Partial, Untested and Ways of the diff's declarations in the index: see cgTests. Without git or
// node, or past a minute, none: no test listed, and no claim that none reaches.
func (idx *cgIndex) tests(sb *proc.Sandbox, root *os.Root, changed map[string]bool, syms map[string][]Symbol) {
	type start struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	var starts []start
	for name, ss := range syms {
		for _, s := range ss {
			if s.Exact && changed[s.Path] && !slices.Contains(starts, start{s.Path, name}) {
				starts = append(starts, start{s.Path, name})
			}
		}
	}
	var got []struct {
		Path, Name        string
		Line              int
		Tests             []string
		Partial, Untested bool
		Ways              []Way
	}
	if len(starts) == 0 || !runNode(sb, cgTests, map[string]any{"repo": sb.Repo(), "dbs": idx.dbs, "starts": starts, "extra": idx.added()}, &got) {
		return
	}
	lines := sources(root)
	for _, g := range got {
		for _, w := range g.Ways {
			for i, h := range w.Chain {
				if h.At > 0 && h.In != "" {
					w.Chain[i].Call = callAt(lines.text(h.In, h.At), h.Name)
				}
			}
		}
	}
	set := map[*Symbol]bool{} // two of the index's, one of ours: what reaches either reaches it
	for _, g := range got {
		var best *Symbol
		for i, s := range syms[g.Name] {
			if s.Path == g.Path && (best == nil || abs(s.Line-g.Line) < abs(best.Line-g.Line)) {
				best = &syms[g.Name][i]
			}
		}
		switch {
		case best == nil:
		case !set[best]:
			set[best], best.Tests, best.Partial, best.Untested, best.Ways = true, g.Tests, g.Partial, g.Untested, g.Ways
		default:
			best.Ways = append(best.Ways, g.Ways...)
			for _, t := range g.Tests {
				if !slices.Contains(best.Tests, t) {
					best.Tests = append(best.Tests, t)
				}
			}
			best.Partial, best.Untested = best.Partial || g.Partial, best.Untested && g.Untested
		}
	}
}

// files is the checkout's files by line, read when first asked.
type files struct {
	root *os.Root // the PR's symlinks don't reach out of it
	got  map[string][]string
}

// callAt is how line names name: the receivers before it and, called, its brackets, "()" or "(…)" with arguments:
// "inv.Lines[i].Discount", "inv.Validate()". The first time it names it; "" when it doesn't or that runs past maxCall.
func callAt(line, name string) string {
	const maxCall = 48
	for _, m := range ident.FindAllStringIndex(line, -1) {
		if line[m[0]:m[1]] != name {
			continue
		}
		from, to := m[0], m[1]
		for from > 0 && line[from-1] == '.' { // a receiver: a name, maybe indexed
			at := from - 1
			for at > 0 && line[at-1] == ']' {
				if at = strings.LastIndexByte(line[:at-1], '['); at < 0 {
					return ""
				}
			}
			ns := ident.FindAllStringIndex(line[:at], -1)
			if len(ns) == 0 || ns[len(ns)-1][1] != at {
				break
			}
			from = ns[len(ns)-1][0]
		}
		s := line[from:to]
		if to < len(line) && line[to] == '(' {
			s += "(…)"
			if n := strings.IndexByte(line[to:], ')'); n > 0 && strings.TrimSpace(line[to+1:to+n]) == "" {
				s = s[:len(s)-len("(…)")] + "()"
			}
		}
		if len(s) > maxCall {
			return ""
		}
		return s
	}
	return ""
}

func sources(root *os.Root) files { return files{root, map[string][]string{}} }

func (fs files) lines(p string) []string {
	ls, ok := fs.got[p]
	if !ok {
		b, _ := diff.ReadIn(fs.root, filepath.FromSlash(p), maxSize)
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
