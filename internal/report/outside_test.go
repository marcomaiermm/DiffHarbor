package report

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A changed function that sends a request, or calls one that does within three calls, has an outside call at its own
// line: the system is the URL's host, the environment variable, or the sending declaration. Found by types, no model.
func TestOutsideGo(t *testing.T) {
	dir := t.TempDir()
	for name, src := range map[string]string{
		"go.mod": "module fix\n\ngo 1.26\n",
		"changed.go": `package fix

import (
	"net/http"
	"os"
)

const itemsURL = "https://items.example.com/v1/items"
const rosterURL = "https://roster.example.com:8443/guild"

func Items() { _, _ = http.Get(itemsURL) }

func Roster() { loadRoster(rosterURL) }

func Token() { u := os.Getenv("TOKEN_URL"); send(u + "/token") }

func Quiet() { local() }

func Far() { a() }
`,
		"remote.go": `package fix

import "net/http"

func loadRoster(u string) { fetch(u) }

func fetch(u string) { _, _ = http.DefaultClient.Get(u) }

func send(u string) { _, _ = http.Post(u, "", nil) }

func local() {}

func a() { b() }
func b() { c() }
func c() { d() }
func d() { http.Head("x") }
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	edges, err := vtaCalls(dir, []string{"Items", "Roster", "Token", "Quiet", "Far", "loadRoster", "fetch", "send", "local", "a", "b", "c", "d"})
	if err != nil {
		t.Fatal(err)
	}
	g := map[declKey][]cgEdge{}
	for _, e := range edges {
		g[declKey{e.SPath, e.SName}] = append(g[declKey{e.SPath, e.SName}], e)
	}
	for name, want := range map[string][]OutsideCall{
		"Items":  {{System: "items.example.com", Path: "changed.go", Line: 11, URL: "https://items.example.com/v1/items", URLAt: "changed.go:8"}},
		"Roster": {{System: "roster.example.com", Path: "changed.go", Line: 13, URL: "https://roster.example.com:8443/guild", URLAt: "changed.go:9"}}, // two calls down, in a file the PR leaves
		"Token":  {{System: "TOKEN_URL", Path: "changed.go", Line: 15, URL: "TOKEN_URL", URLAt: "changed.go:15"}},
		"Quiet":  nil,
		"Far":    nil, // the request is four calls down
	} {
		if got := sendersFrom(g, declKey{"changed.go", name}); !slices.EqualFunc(got, want, OutsideCall.same) {
			t.Errorf("%s: got %+v, want %+v", name, got, want)
		}
	}
}

// The walk is bounded in hops and finds a request on any path of calls; the system's name falls back step by step.
func TestOutsideWalk(t *testing.T) {
	call := func(s, t string, line int) cgEdge {
		return cgEdge{Kind: "calls", SPath: "x.go", SName: s, TPath: "x.go", TName: t, Line: line}
	}
	g := map[declKey][]cgEdge{}
	for _, e := range []cgEdge{
		call("start", "dead", 3), call("start", "one", 4), call("one", "two", 20), call("two", "three", 30),
		{Kind: "requests", SPath: "x.go", SName: "three", TName: "Do", Line: 40},
		call("start", "deep", 5), call("deep", "d2", 1), call("d2", "d3", 1), call("d3", "d4", 1),
		{Kind: "requests", SPath: "x.go", SName: "d4", TName: "Do", Line: 2},
	} {
		g[declKey{e.SPath, e.SName}] = append(g[declKey{e.SPath, e.SName}], e)
	}
	got := sendersFrom(g, declKey{"x.go", "start"})
	if len(got) != 1 || !got[0].same(OutsideCall{System: "three", Path: "x.go", Line: 4}) {
		t.Errorf("a request three calls down, none four: %+v", got)
	}
	for _, c := range []struct{ url, env, sender, want string }{
		{"https://Wago.Tools:443/api", "X", "get", "wago.tools"},
		{"", "ITEMDB_API_URL", "get", "ITEMDB_API_URL"},
		{"not a url", "", "get", "get"},
	} {
		if got := systemName(c.url, c.env, c.sender); got != c.want {
			t.Errorf("systemName(%q, %q, %q) = %q, want %q", c.url, c.env, c.sender, got, c.want)
		}
	}
}

// The checker's requests and urls in testdata/ts: the global fetch, node:http, a package given a URL constant; a URL
// written, in a const, a template's prefix, new URL(…), or an environment variable, also through a module-level object.
func TestOutsideTS(t *testing.T) {
	if _, err := os.Stat(filepath.Join(tsAPI, "dist", "api", "sync", "api.js")); err != nil {
		t.Skip("no TypeScript 7.1 at DIFFHARBOR_TS")
	}
	repo, err := filepath.Abs("testdata/ts")
	if err != nil {
		t.Fatal(err)
	}
	sb, _ := sandbox(t)
	idx := &cgIndex{dbs: []cgDB{{Prefix: ""}}}
	idx.tsEdges(sb, repo)
	var got []string
	for _, e := range idx.Edges {
		if e.SPath == "src/outbound.ts" && (e.Kind == "requests" || e.Kind == "url") {
			got = append(got, fmt.Sprintf("%s %s:%d %s %s %s", e.Kind, e.SName, e.Line, e.URL, e.Env, e.URLAt))
		}
	}
	for _, w := range []string{
		"requests loadItems:5   ",
		"url loadItems:5 https://items.example.com/v1  src/outbound.ts:3", // `${ITEMS}/items`: the const's line
		"url loadEnv:6  API_URL src/outbound.ts:6",
		"url token:7  TOKEN_URL src/outbound.ts:7",
		"requests viaNode:8   ", // node:http's get
		"url viaNode:8 http://node.example.com/x  src/outbound.ts:8",
		"requests viaPackage:9   ", // a package's call given a URL
		"url viaPackage:9 https://pkg.example.com/y  src/outbound.ts:9",
		"requests local:10   ", // a request, no URL to name
	} {
		if !slices.Contains(got, w) {
			t.Errorf("no %q in\n%s", w, strings.Join(got, "\n"))
		}
	}
	g := map[declKey][]cgEdge{}
	for _, e := range idx.Edges {
		g[declKey{e.SPath, e.SName}] = append(g[declKey{e.SPath, e.SName}], e)
	}
	want := []OutsideCall{{System: "items.example.com", Path: "src/outbound.ts", Line: 11, URL: "https://items.example.com/v1", URLAt: "src/outbound.ts:3"}}
	if w := sendersFrom(g, declKey{"src/outbound.ts", "pass"}); !slices.EqualFunc(w, want, OutsideCall.same) { // one call down
		t.Errorf("pass: got %+v, want %+v", w, want)
	}
	for _, g := range got {
		if strings.HasPrefix(g, "url local") || strings.HasPrefix(g, "requests pass") {
			t.Errorf("not a URL, not a sender: %q", g)
		}
	}
}

// The walk reads the index for calls out of unchanged code: Sync, which the PR changes, reaches the request three
// calls down, in a file it leaves, and the outside call is Sync's own line.
func TestOutsideIndexWalk(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	sb, dir := sandbox(t)
	db := `const { DatabaseSync } = require("node:sqlite"); const db = new DatabaseSync(process.argv[1]);
db.exec("create table nodes (id text, kind text, name text, file_path text, start_line int, end_line int);" +
  "create table edges (source text, target text, kind text, line int);" +
  "insert into nodes values ('s', 'function', 'Sync', 'sync.go', 3, 7), ('f', 'function', 'fetchRoster', 'roster.go', 3, 5)," +
  " ('g', 'function', 'getRoster', 'roster.go', 7, 9), ('a', 'function', 'fetchAPI', 'api.go', 3, 9);" +
  "insert into edges values ('s', 'f', 'calls', 5), ('f', 'g', 'calls', 4), ('g', 'a', 'calls', 8);")`
	if err := os.MkdirAll(filepath.Join(dir, ".codegraph"), 0o750); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", "-e", db, filepath.Join(dir, ".codegraph", "codegraph.db")).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	idx := &cgIndex{dbs: []cgDB{{DB: filepath.Join(dir, ".codegraph", "codegraph.db")}}, Edges: []cgEdge{
		{Line: 9, Kind: "requests", By: "ts", SName: "fetchAPI", SPath: "api.go", TName: "fetch"},
		{Line: 9, Kind: "requests", By: "ts", SName: "other", SPath: "sync.go", TName: "fetch"}, // unchanged: not a start
		{Line: 5, Kind: "url", By: "ts", SName: "Sync", SPath: "sync.go", URL: "https://blizzard.example.com/roster", URLAt: "sync.go:2"},
	}}
	syms := map[string][]Symbol{"Sync": {{Path: "sync.go", Line: 3, From: 3, To: 7, Exact: true}}, "other": {{Path: "sync.go", Line: 9, From: 9, To: 9, Exact: true}}}
	idx.outside(sb, nil, map[string]bool{"sync.go": true}, map[string]bool{"sync.go:6": true}, syms)
	want := []OutsideCall{{System: "blizzard.example.com", Path: "sync.go", Line: 5, URL: "https://blizzard.example.com/roster", URLAt: "sync.go:2"}}
	if got := Outside(syms); !slices.EqualFunc(got, want, OutsideCall.same) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A token read from the environment before the request names no system; a URL in the sender does, without its user.
func TestOutsideCallPrefersURL(t *testing.T) {
	k := declKey{"a.go", "send"}
	g := map[declKey][]cgEdge{k: {
		{Line: 3, Kind: "url", Env: "API_TOKEN"},
		{Line: 4, Kind: "url", URL: "https://u:secret@api.example.com/x", URLAt: "a.go:4"},
		{Line: 5, Kind: "requests"},
	}}
	got := outsideCall(g, declKey{"b.go", "run"}, 9, k, g[k][2])
	want := OutsideCall{System: "api.example.com", Path: "b.go", Line: 9, URL: "https://api.example.com/x", URLAt: "a.go:4"}
	if !got.same(want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// The store walk keeps a path per call of the start, at most three calls long, and the start's own query.
func TestStoresWalk(t *testing.T) {
	call := func(s, t string, line int) cgEdge {
		return cgEdge{Kind: "calls", SPath: "x.go", SName: s, TPath: "x.go", TName: t, Line: line}
	}
	g := map[declKey][]cgEdge{}
	for _, e := range []cgEdge{
		{Kind: "writes", SPath: "x.go", SName: "start", TName: "users", Line: 2},
		call("start", "one", 3), call("start", "one", 3), // the index's and VTA's: one call
		call("start", "one", 6), call("one", "two", 20), call("two", "three", 30),
		{Kind: "reads", SPath: "x.go", SName: "three", TName: "orders", TPath: "s.go", TLine: 4, Line: 40},
		call("start", "deep", 5), call("deep", "d2", 1), call("d2", "d3", 1), call("d3", "d4", 1),
		{Kind: "reads", SPath: "x.go", SName: "d4", TName: "far", Line: 2},
	} {
		g[declKey{e.SPath, e.SName}] = append(g[declKey{e.SPath, e.SName}], e)
	}
	var got []string
	for _, p := range storesFrom(g, declKey{"x.go", "start"}) {
		got = append(got, fmt.Sprintf("%s %s %s:%d at %s %v", p.Kind, p.Table, p.TPath, p.TLine, p.At, p.Calls))
	}
	want := []string{
		"writes users :0 at x.go:2 []",
		"reads orders s.go:4 at x.go:40 [{x.go:3 one} {x.go:20 two} {x.go:30 three}]",
		"reads orders s.go:4 at x.go:40 [{x.go:6 one} {x.go:20 two} {x.go:30 three}]", // the other call into one
	} // far is four calls down
	if !slices.Equal(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
