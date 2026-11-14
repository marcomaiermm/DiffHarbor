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

// A SQL statement's table, sqlc's generated form too: a write unless it selects; not a message that reads like one.
func TestVTAStores(t *testing.T) {
	dir := t.TempDir()
	for name, src := range map[string]string{
		"go.mod": "module fix\n\ngo 1.26\n",
		"db.go": `package fix

import (
	"context"
	"database/sql"
	"fmt"
)

const createUser = "-- name: CreateUser :one\nINSERT INTO users (name) VALUES ($1) RETURNING id"

func CreateUser(ctx context.Context, db *sql.DB) { db.QueryRowContext(ctx, createUser, "a") }

func listOrders(db *sql.DB) { db.Query("SELECT id FROM \"orders\" WHERE n > 1") }

func bump(db *sql.Tx) error {
	q := "UPDATE accounts SET n = n + 1"
	db.Exec(q)
	db.Exec("update failed")
	db.Query("SELECT x FROM unnest($1::int[]) x")
	return fmt.Errorf("select a file from disk")
}
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := vtaCalls(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var stores []string
	for _, e := range got {
		if e.Kind == "reads" || e.Kind == "writes" {
			stores = append(stores, fmt.Sprintf("%s %s %d -> %s %q", e.Kind, e.SName, e.Line, e.TName, e.TPath))
		}
	}
	want := []string{`writes CreateUser 11 -> users ""`, `reads listOrders 13 -> orders ""`, `writes bump 17 -> accounts ""`}
	if !slices.Equal(stores, want) {
		t.Errorf("got %q, want %q", stores, want)
	}
}

// An ORM's chain, gorm's and bun's shapes: its verbs say read or write, its arguments the store. A package's only:
// the module's own Create (a dotted path like a package's) names none, nor a chain without a verb.
func TestVTAORM(t *testing.T) {
	dir := t.TempDir()
	for name, src := range map[string]string{
		"go.mod":     "module example.com/app\n\ngo 1.26\n\nrequire example.com/orm v0.0.0\n\nreplace example.com/orm => ./orm\n",
		"orm/go.mod": "module example.com/orm\n\ngo 1.26\n",
		"orm/orm.go": `package orm

type DB struct{}

func (d *DB) Model(v any) *DB              { return d }
func (d *DB) Table(name string) *DB        { return d }
func (d *DB) Where(q string, a ...any) *DB { return d }
func (d *DB) Create(v any) *DB             { return d }
func (d *DB) Find(v any) *DB               { return d }
func (d *DB) Update(c string, v any) *DB   { return d }
func (d *DB) NewInsert() *DB               { return d }
func (d *DB) Exec(ctx any) error           { return nil }
func (d *DB) AutoMigrate(v ...any) error   { return nil }
`,
		"db.go": `package fix

import "example.com/orm"

type User struct{ Name string }

type Order struct{ ID int }

func add(db *orm.DB, u *User) { db.Create(u) }

func list(db *orm.DB) {
	var os []Order
	db.Where("id > ?", 1).
		Find(&os)
}

func rename(db *orm.DB) { db.Table("accounts").Where("id = ?", 1).Update("name", "x") }

func insert(db *orm.DB) { db.NewInsert().Model(&User{}).Exec(nil) }

func migrate(db *orm.DB) { db.AutoMigrate(&User{}) }

func Create(u *User) {}

func local(u *User) { Create(u) }

func archive(db *orm.DB) { db.Table("archive").Create(&User{}) }
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
	got, err := vtaCalls(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var stores []string
	for _, e := range got {
		if e.Kind == "reads" || e.Kind == "writes" {
			stores = append(stores, fmt.Sprintf("%s %s %d -> %s %s:%d", e.Kind, e.SName, e.Line, e.TName, e.TPath, e.TLine))
		}
	}
	want := []string{
		"writes add 9 -> User db.go:5",
		"reads list 13 -> Order db.go:7", // the chain's first line
		"writes rename 17 -> accounts :0",
		"writes insert 19 -> User db.go:5",
		"writes archive 27 -> archive :0", // the store nearest the client
	}
	if !slices.Equal(stores, want) {
		t.Errorf("got %q, want %q", stores, want)
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
		"writes nsWrite src/data.ts:15 -> users src/schema.ts",     // insert(schema.users)
		"reads findPosts src/data.ts:10 -> post ",                  // client.post.findMany()
		"reads rawCount src/data.ts:11 -> audit_log ",              // SQL
		"reads allOrders src/data.ts:12 -> Order src/schema.ts",    // Repository<Order>
		"handles get /orders src/api.ts:6 -> getOrders src/api.ts", // app.get("/orders", getOrders)
		"calls post /users src/api.ts:7 -> addUser src/data.ts",    // an inline handler
		"calls query posts src/api.ts:9 -> findPosts src/data.ts",  // a procedure under its key
		"calls shout src/api.ts:12 -> send src/notify.ts",          // n.make().send()
		"calls notifyAll src/notify.ts:4 -> toLabel src/notify.ts", // xs.map(toLabel)
		"renders List src/App.tsx:2 -> Row src/App.tsx",            // <Row />
		"reads listRecent src/data.ts:17 -> recentSq src/data.ts",  // a subquery is a table of its own
	} {
		if !slices.Contains(got, w) {
			t.Errorf("no %q in\n%s", w, strings.Join(got, "\n"))
		}
	}
	for _, e := range idx.Edges {
		if (e.Kind == "reads" || e.Kind == "writes") && e.Sub != (e.TName == "recentSq") {
			t.Errorf("only recentSq, made by .as(…), is a subquery: %+v", e)
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
