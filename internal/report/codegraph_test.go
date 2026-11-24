package report

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// cgFile writes a codegraph index under dir with the tables the queries read, then the rows of insert.
func cgFile(t *testing.T, dir, insert string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	f := filepath.Join(dir, ".codegraph", "codegraph.db")
	if err := os.MkdirAll(filepath.Dir(f), 0o750); err != nil {
		t.Fatal(err)
	}
	db := `const { DatabaseSync } = require("node:sqlite"); const db = new DatabaseSync(process.argv[1]);
db.exec("create table nodes (id text, kind text, name text, file_path text, start_line int, end_line int);" +
  "create table edges (source text, target text, kind text, line int); create table files (path text);" + process.argv[2])`
	if out, err := exec.Command("node", "-e", db, f, insert).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	return f
}

// A function of the method's name elsewhere in the file is no scan of the method: it is fresh still.
func TestFreshMethodSameName(t *testing.T) {
	idx := &cgIndex{Nodes: []cgNode{
		{Name: "run", Path: "a.py", Kind: "function", Line: 1, End: 2},
		{Name: "run", Path: "a.py", Kind: "method", Owner: "A", Line: 5, End: 7},
	}}
	syms := map[string][]Symbol{"run": {{Kind: "function", Path: "a.py", Line: 1}}}
	idx.exact(nil, map[string]bool{"a.py": true}, map[string]bool{"a.py:6": true}, syms)
	if len(syms["A.run"]) != 1 {
		t.Errorf("A.run %+v: its body changed and the scan has only the function run", syms["A.run"])
	}
}

// A changed file's method is in the edges it starts too, not only those into it: what it calls.
func TestQueryMethodCalls(t *testing.T) {
	sb, dir := sandbox(t)
	f := cgFile(t, dir, "insert into nodes values ('c', 'class', 'A', 'a.py', 1, 4), ('m', 'method', 'm', 'a.py', 2, 3), ('h', 'function', 'helper', 'b.py', 1, 1);"+
		"insert into edges values ('m', 'h', 'calls', 3);")
	var idx cgIndex
	if !runNode(sb, cgQuery, map[string]any{"names": []string{}, "dbs": []cgDB{{DB: f}}, "paths": []string{"a.py"}}, &idx) {
		t.Fatal("query failed")
	}
	if !slices.ContainsFunc(idx.Edges, func(e cgEdge) bool { return e.SName == "m" && e.TName == "helper" }) {
		t.Errorf("edges %+v: m calls helper", idx.Edges)
	}
}

// A fresh method, A.run, is the index's run: the tests that reach that one reach it.
func TestFreshMethodTests(t *testing.T) {
	sb, dir := sandbox(t)
	f := cgFile(t, dir, "insert into files values ('a.py'), ('test_a.py');"+
		"insert into nodes values ('c', 'class', 'A', 'a.py', 1, 4), ('m', 'method', 'run', 'a.py', 3, 4), ('t', 'function', 'test_run', 'test_a.py', 1, 2);"+
		"insert into edges values ('t', 'm', 'calls', 2);")
	idx := &cgIndex{dbs: []cgDB{{DB: f}}}
	syms := map[string][]Symbol{"A.run": {{Kind: "method", Path: "a.py", Line: 3, Exact: true}}}
	idx.tests(sb, nil, map[string]bool{"a.py": true}, syms)
	if s := syms["A.run"][0]; !slices.Contains(s.Tests, "test_a.py test_run") {
		t.Errorf("tests %q: test_run calls A.run", s.Tests)
	}
}

// A monorepo whose root has no tsconfig.json: the projects are those above the changed TS files; with one at the
// root, that one alone.
func TestTSRoots(t *testing.T) {
	repo := t.TempDir()
	for _, f := range []string{"apps/backend/tsconfig.json", "apps/frontend/tsconfig.json", "apps/backend/src/a.ts"} {
		if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	idx := &cgIndex{dbs: []cgDB{{Prefix: ""}}, paths: []string{"apps/backend/src/a.ts", "apps/backend/src/b.ts", "README.md", "apps/frontend/x.css"}}
	if got := idx.tsRoots(repo); !slices.Equal(got, []string{"apps/backend/"}) {
		t.Fatal(got)
	}
	if err := os.WriteFile(filepath.Join(repo, "tsconfig.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := idx.tsRoots(repo); !slices.Equal(got, []string{""}) {
		t.Fatal(got)
	}
}
