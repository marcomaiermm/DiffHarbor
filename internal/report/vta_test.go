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

// An interface call resolves to the implementation the program passes, not to the other one.
func TestVTACalls(t *testing.T) {
	dir := t.TempDir()
	for name, src := range map[string]string{
		"go.mod": "module fix\n\ngo 1.26\n",
		"store/store.go": `package store

type Store interface{ Record(n int) }

type DB struct{}

func (DB) Record(n int) {}

type Mem struct{}

func (Mem) Record(n int) {}
`,
		"main.go": `package main

import "fix/store"

func backfill(s store.Store) {
	s.Record(1)
}

func each[S store.Store](s S) { s.Record(2) }

func main() { backfill(store.DB{}); each(store.DB{}) }
`,
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := vtaCalls(dir, []string{"Record"})
	if err != nil {
		t.Fatal(err)
	}
	want := cgEdge{Line: 6, Kind: "calls", By: "vta", SName: "backfill", SKind: "function", SPath: "main.go", SLine: 5, SEnd: 7,
		TName: "Record", TPath: "store/store.go", TLine: 7}
	generic := cgEdge{Line: 9, Kind: "calls", By: "vta", SName: "each", SKind: "function", SPath: "main.go", SLine: 9, SEnd: 9,
		TName: "Record", TPath: "store/store.go", TLine: 7} // through its instance
	if len(got) != 2 || !slices.Contains(got, want) || !slices.Contains(got, generic) {
		t.Fatalf("got %+v, want only %+v and %+v", got, want, generic)
	}
	got, err = vtaCalls(dir, nil) // none wanted: still the interface's, a walk goes on through it; not main's static ones
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, want) || slices.ContainsFunc(got, func(e cgEdge) bool { return e.SName == "main" }) {
		t.Fatalf("no names: got %+v, want %+v and none from main", got, want)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.go"), []byte("package main\nvar x int = \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := vtaCalls(dir, []string{"Record"}); err == nil {
		t.Fatal("a program that doesn't type-check: want an error")
	}
}

// The checker's edges in testdata/ts, two made-up packages: an ORM and a web framework. No rule names either.
func TestTSEdges(t *testing.T) {
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
		got = append(got, fmt.Sprintf("%s %s %s:%d -> %s %s", e.Kind, e.SName, e.SPath, e.Line, e.TName, e.TPath))
	}
	for _, e := range idx.Edges {
		if e.SName == "view" {
			t.Errorf("createElement(\"div\") names no store: %+v", e)
		}
		if strings.HasPrefix(e.SName, "transaction") {
			t.Errorf("a callback in a function is that function's, not a route: %+v", e)
		}
	}
	for _, w := range []string{ // the root tsconfig only references tsconfig.app.json
		"reads listOrders src/data.ts:5 -> orders src/schema.ts",   // the table in the chain
		"writes addUser src/data.ts:7 -> users src/schema.ts",      // in a transaction's callback
		"writes renameUser src/data.ts:9 -> users src/schema.ts",   // update(t).set(…)
		"writes txCount src/data.ts:13 -> users src/schema.ts",     // const n = db.transaction(…): no route "n"
		"reads findPosts src/data.ts:10 -> post ",                  // client.post.findMany()
		"reads rawCount src/data.ts:11 -> audit_log ",              // SQL
		"reads allOrders src/data.ts:12 -> Order src/schema.ts",    // Repository<Order>
		"handles get /orders src/api.ts:6 -> getOrders src/api.ts", // app.get("/orders", getOrders)
		"calls post /users src/api.ts:7 -> addUser src/data.ts",    // an inline handler
		"calls query posts src/api.ts:9 -> findPosts src/data.ts",  // a procedure under its key
		"calls shout src/api.ts:12 -> send src/notify.ts",          // n.make().send()
		"calls notifyAll src/notify.ts:4 -> toLabel src/notify.ts", // xs.map(toLabel)
		"renders List src/App.tsx:2 -> Row src/App.tsx",            // <Row />
	} {
		if !slices.Contains(got, w) {
			t.Errorf("no %q in\n%s", w, strings.Join(got, "\n"))
		}
	}
}

// Tests and ways walk the added edges too: Rec is reached only through an interface (VTA's edge from Run) and Run
// only from a route the checker found.
func TestTestsWalkAdded(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	sb, dir := sandbox(t)
	for name, src := range map[string]string{
		"store.go":    "package a\n\nfunc (DB) Rec() {}\n",
		"run.go":      "package a\n\nfunc Run(s S) {\n\ts.Rec()\n}\n",
		"run_test.go": "package a\n\nfunc TestRun(t *testing.T) {\n\tRun(DB{})\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db := `const { DatabaseSync } = require("node:sqlite"); const db = new DatabaseSync(process.argv[1]);
db.exec("create table nodes (id text, kind text, name text, file_path text, start_line int, end_line int);" +
  "create table edges (source text, target text, kind text, line int); create table files (path text);" +
  "insert into files values ('store.go'), ('run.go'), ('run_test.go');" +
  "insert into nodes values ('r', 'method', 'Rec', 'store.go', 3, 3), ('u', 'function', 'Run', 'run.go', 3, 5)," +
  " ('t', 'function', 'TestRun', 'run_test.go', 3, 5);" +
  "insert into edges values ('t', 'u', 'calls', 4);")`
	if err := os.MkdirAll(filepath.Join(dir, ".codegraph"), 0o750); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", "-e", db, filepath.Join(dir, ".codegraph", "codegraph.db")).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	root, err := sb.Pinned()
	if err != nil {
		t.Fatal(err)
	}
	idx := &cgIndex{dbs: []cgDB{{DB: filepath.Join(dir, ".codegraph", "codegraph.db")}}, Edges: []cgEdge{
		{Line: 4, Kind: "calls", By: "vta", SName: "Run", SKind: "function", SPath: "run.go", SLine: 3, SEnd: 5, TName: "Rec", TPath: "store.go", TLine: 3},
		{Line: 7, Kind: "handles", By: "ts", SName: "get /rec", SKind: "route", SPath: "api.ts", SLine: 7, SEnd: 7, TName: "Run", TPath: "run.go", TLine: 3},
	}}
	syms := map[string][]Symbol{"Rec": {{Path: "store.go", Line: 3, Exact: true}}}
	idx.tests(sb, root, map[string]bool{"store.go": true}, syms)
	s := syms["Rec"][0]
	if !slices.Contains(s.Tests, "run_test.go TestRun") {
		t.Errorf("tests %q: TestRun reaches Rec through Run", s.Tests)
	}
	if len(s.Ways) != 1 || s.Ways[0].Method != "GET" || s.Ways[0].Route != "/rec" || s.Ways[0].Path != "api.ts" {
		t.Errorf("ways %+v: GET /rec runs Run, which runs Rec", s.Ways)
	}
}
