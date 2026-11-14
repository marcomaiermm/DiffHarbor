package report

import (
	"cmp"
	"diffharbor/internal/proc"
	"maps"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
)

// The outside calls (6.4): where the PR's changed code sends a request to a system outside the repo, by the code
// graph and the types, not the review's say. A "requests" edge is a call that sends one (vta.go: net/http; tsedges.cjs:
// fetch, node:http, a package given a URL); a walk from each declaration the PR adds or changes follows call edges
// at most maxHops down, through unchanged code too, to one. The outside call is the line in the changed declaration
// where the walk starts.
const (
	maxHops    = 3
	maxStarts  = 300 // changed declarations walked
	maxWalk    = 400 // declarations whose calls the index is asked for
	maxPer     = 5   // outside calls from one declaration
	maxOutside = 40  // in all
	maxStores  = 100 // queries one declaration's walk keeps
)

// declKey is a declaration as the edges name it. Two of one name in a file are one: the index's name is no more.
type declKey struct{ Path, Name string }

// cgOut is the index's calls out of the declarations in "from" ({Path, Name}): the one hop a walk takes.
const cgOut = `
const { DatabaseSync } = require("node:sqlite");
const { dbs, from } = JSON.parse(require("fs").readFileSync(0, "utf8"));
const out = [];
const other = (p, own) => dbs.some(d => d.prefix.length > own.length && p.startsWith(d.prefix));
for (const { db: file, prefix } of dbs) {
  const db = new DatabaseSync(file, { readOnly: true });
  const mine = JSON.stringify(from.filter(f => f.Path.startsWith(prefix) && !other(f.Path, prefix)).map(f => ({ p: f.Path.slice(prefix.length), n: f.Name })));
  for (const e of db.prepare("select e.line, s.name sname, s.kind skind, s.file_path spath, s.start_line sline, s.end_line send," +
    " t.name tname, t.file_path tpath, t.start_line tline from edges e join nodes s on s.id = e.source join nodes t on t.id = e.target" +
    " where e.kind = 'calls' and t.kind not in ('file', 'import') and (s.file_path, s.name) in" +
    " (select json_extract(value, '$.p'), json_extract(value, '$.n') from json_each(?1))").all(mine)) {
    e.spath = prefix + e.spath;
    e.tpath = prefix + e.tpath;
    e.kind = "calls";
    if (!other(e.tpath, prefix)) out.push(e);
  }
}
process.stdout.write(JSON.stringify(out));
`

// callGraph is the calls out of the starts and of what they reach, up to maxHops down: the index's, asked for one hop
// at a time, and the added ones (VTA's, the checker's). Past maxWalk declarations the walk stops growing.
func (idx *cgIndex) callGraph(sb *proc.Sandbox, starts []declKey) map[declKey][]cgEdge {
	g := map[declKey][]cgEdge{}
	for _, e := range idx.Edges {
		k := declKey{e.SPath, e.SName}
		g[k] = append(g[k], e)
	}
	asked := map[declKey]bool{}
	front := starts
	for range maxHops {
		var ask []map[string]string
		var fresh []declKey
		for _, k := range front {
			if !asked[k] && len(asked) < maxWalk {
				asked[k] = true
				ask, fresh = append(ask, map[string]string{"Path": k.Path, "Name": k.Name}), append(fresh, k)
			}
		}
		if len(ask) == 0 {
			break
		}
		var got []cgEdge
		if runNode(sb, cgOut, map[string]any{"dbs": idx.dbs, "from": ask}, &got) {
			for _, e := range got {
				k := declKey{e.SPath, e.SName}
				g[k] = append(g[k], e)
			}
		}
		front = nil
		for _, k := range fresh {
			for _, e := range g[k] {
				if e.Kind == "calls" && e.TPath != "" {
					front = append(front, declKey{e.TPath, e.TName})
				}
			}
		}
	}
	for _, es := range g { // the caps below cut the same ones whatever order the edges came in
		slices.SortStableFunc(es, func(a, b cgEdge) int { return cmp.Compare(a.Line, b.Line) })
	}
	return g
}

// sendersFrom are the requests start's code sends, itself or through the calls it makes, at most maxHops down: each an
// OutsideCall at the line of start where that path begins.
func sendersFrom(g map[declKey][]cgEdge, start declKey) []OutsideCall {
	type step struct {
		k  declKey
		at int // the line of start that began the path
	}
	var out []OutsideCall
	seen := map[declKey]bool{start: true}
	level := []step{{start, 0}}
	for hop := 0; hop <= maxHops && len(level) > 0 && len(out) < maxPer; hop++ {
		var next []step
		for _, s := range level {
			for _, e := range g[s.k] {
				at := s.at
				if hop == 0 {
					at = e.Line
				}
				switch {
				case at == 0:
				case e.Kind == "requests":
					c := outsideCall(g, start, at, s.k, e)
					if !slices.Contains(out, c) {
						out = append(out, c)
					}
				case e.Kind == "calls" && hop < maxHops && e.TPath != "" && !seen[declKey{e.TPath, e.TName}]:
					seen[declKey{e.TPath, e.TName}] = true
					next = append(next, step{declKey{e.TPath, e.TName}, at})
				}
			}
		}
		level = next
	}
	return out[:min(len(out), maxPer)]
}

// outsideCall names the system a request edge e, in the declaration sender, sends to from the call at line at of
// start: its URL is what the call at start passes on, else what the sender's own call, else any URL the sender
// names.
func outsideCall(g map[declKey][]cgEdge, start declKey, at int, sender declKey, e cgEdge) OutsideCall {
	var u cgEdge
	pick := func(k declKey, line int, urlOnly bool) bool { // line 0: any
		i := slices.IndexFunc(g[k], func(x cgEdge) bool {
			return x.Kind == "url" && (line == 0 || x.Line == line) && (!urlOnly || x.URL != "")
		})
		if i >= 0 {
			u = g[k][i]
		}
		return i >= 0
	}
	// a header's token read from the environment is no system: a URL anywhere in the sender goes before an env
	if !pick(start, at, false) && !pick(sender, e.Line, false) && !pick(sender, 0, true) {
		pick(sender, 0, false)
	}
	return OutsideCall{System: systemName(u.URL, u.Env, sender.Name), Path: start.Path, Line: at, URL: cmp.Or(noUser(u.URL), u.Env), URLAt: u.URLAt}
}

// noUser is rawURL without a user:password@, which is rendered.
func noUser(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	return rawURL
}

// systemName is what the code names the system: the URL's host, else the environment variable the URL is read from,
// else the declaration that sends the request.
func systemName(rawURL, env, sender string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		return strings.ToLower(u.Hostname())
	}
	return cmp.Or(env, sender)
}

// StorePath is a query a declaration's code runs (spec 6.13): Kind "reads" or "writes", the table as the store edge
// names it (Table; TPath and TLine its declaration, "" when the code only names it; Text that line and the next; Sub,
// the declaration is a subquery), At the query's place. Calls lead to it from the declaration, the first in its own
// code; none for its own query.
type StorePath struct {
	Kind, Table, TPath, Text, At string
	TLine                        int
	Sub                          bool
	Calls                        []StoreCall
}

// StoreCall is a call on a store path: its place (path:line) and its callee.
type StoreCall struct{ At, Callee string }

// storesFrom are the queries start's code runs, its own and those within maxHops calls: a walk per call it makes, so
// each of its calls keeps the paths it begins. Each declaration once per walk, the first path to it; the first
// maxStores in all.
func storesFrom(g map[declKey][]cgEdge, start declKey) []StorePath {
	var out []StorePath
	add := func(e cgEdge, calls []StoreCall) {
		if len(out) < maxStores {
			p := StorePath{Kind: e.Kind, Table: e.TName, TPath: e.TPath, TLine: e.TLine, Sub: e.Sub, Calls: calls}
			p.At = e.SPath + ":" + strconv.Itoa(e.Line)
			out = append(out, p)
		}
	}
	store := func(e cgEdge) bool { return e.Kind == "reads" || e.Kind == "writes" }
	call := func(e cgEdge) StoreCall {
		return StoreCall{e.SPath + ":" + strconv.Itoa(cmp.Or(e.Line, e.SLine)), e.TName}
	}
	type step struct {
		k     declKey
		calls []StoreCall
	}
	firsts := map[StoreCall]bool{}
	for _, f := range g[start] {
		if store(f) {
			add(f, nil)
		}
		if f.Kind != "calls" || f.TPath == "" || f.Line == 0 || firsts[call(f)] {
			continue
		}
		firsts[call(f)] = true
		k := declKey{f.TPath, f.TName}
		seen := map[declKey]bool{start: true, k: true}
		level := []step{{k, []StoreCall{call(f)}}}
		for hop := 1; hop <= maxHops && len(level) > 0; hop++ {
			var next []step
			for _, s := range level {
				for _, e := range g[s.k] {
					t := declKey{e.TPath, e.TName}
					switch {
					case store(e):
						add(e, s.calls)
					case e.Kind == "calls" && e.TPath != "" && !seen[t]:
						seen[t] = true
						next = append(next, step{t, append(slices.Clip(s.calls), call(e))})
					}
				}
			}
			level = next
		}
	}
	return out
}

// outside sets Requests and Stores on the declarations the PR adds or changes (an added line between their lines):
// the requests they send and the queries they run, themselves or within maxHops calls. root: the checkout, for the
// lines that declare a table.
func (idx *cgIndex) outside(sb *proc.Sandbox, root *os.Root, changed, added map[string]bool, syms map[string][]Symbol) {
	at := map[declKey][]*Symbol{}
	for _, name := range slices.Sorted(maps.Keys(syms)) { // the cap cuts the same ones every run
		ss := syms[name]
		for i := range ss {
			s := &ss[i]
			k := declKey{s.Path, name[strings.LastIndex(name, ".")+1:]}
			if s.Exact && changed[s.Path] && addedIn(added, s.Path, s.From, s.To) && (len(at) < maxStarts || at[k] != nil) {
				at[k] = append(at[k], s)
			}
		}
	}
	if len(at) == 0 {
		return
	}
	starts := slices.SortedFunc(maps.Keys(at), func(a, b declKey) int { return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Name, b.Name)) })
	g := idx.callGraph(sb, starts)
	lines := sources(root)
	for k, ss := range at {
		reqs, stores := sendersFrom(g, k), storesFrom(g, k)
		for i, p := range stores {
			if p.TPath != "" {
				stores[i].Text = lines.text(p.TPath, p.TLine) + "\n" + lines.text(p.TPath, p.TLine+1)
			}
		}
		for _, s := range ss {
			s.Requests, s.Stores = reqs, stores
		}
	}
}

// Outside is the outside calls of the changed declarations: one per place, system and URL, by place, the first maxOutside.
func Outside(syms map[string][]Symbol) []OutsideCall {
	var out []OutsideCall
	for _, ss := range syms {
		for _, s := range ss {
			for _, c := range s.Requests {
				if !slices.Contains(out, c) {
					out = append(out, c)
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b OutsideCall) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line), cmp.Compare(a.System, b.System),
			cmp.Compare(a.URL, b.URL), cmp.Compare(a.URLAt, b.URLAt))
	})
	return out[:min(len(out), maxOutside)]
}
