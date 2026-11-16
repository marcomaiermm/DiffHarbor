package report

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// waysOf is the nodes, one line each: name, kind, its way on (the station up, or the end) and the joint.
func waysOf(nodes []Node) string {
	var b strings.Builder
	for _, n := range nodes {
		fmt.Fprintf(&b, "%s %s", n.Name, n.Kind)
		if n.Up > 0 {
			fmt.Fprintf(&b, " up %s", nodes[n.Up-1].Name)
		}
		if j := n.JUp; j != nil {
			fmt.Fprintf(&b, " %s@%s:%d", j.Rel, j.Path, j.Line)
		}
		if n.End != nil {
			fmt.Fprintf(&b, " end %s %s", n.End.Kind, n.End.Text)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// The ways up from Rec's uses: A's to main through B, though C calls it too; D, only a test calls; X and Y, which
// call each other; z.go, not in the code graph; a name found by name; L0, whose callers run past the bound; N,
// which nothing calls; the interface recorder, which lists it; two methods String in one file; a test's use, which
// gets no structure and no way. TestW's second line is a use of its own, as is the second String's.
func TestUsesWalk(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	sb, dir := sandbox(t)
	var l strings.Builder // L9 calls L8 … L0 calls Rec: past the walk's depth
	l.WriteString("package a\n")
	cg := `insert into files values ('rec.go'), ('a.go'), ('b.go'), ('main.go'), ('x.go'), ('rec_test.go'), ('l.go'), ('n.go');` +
		`insert into nodes values ('n', 'function', 'N', 'n.go', 3, 5);insert into edges values ('n', 'r', 'calls', 4);`
	for i := range 10 {
		callee := "Rec"
		if i > 0 {
			callee = fmt.Sprintf("L%d", i-1)
		}
		fmt.Fprintf(&l, "\nfunc L%d() {\n\t%s()\n}\n", i, callee)
		cg += fmt.Sprintf("insert into nodes values ('l%d', 'function', 'L%d', 'l.go', %d, %d);", i, i, 3+4*i, 5+4*i)
		if i > 0 {
			cg += fmt.Sprintf("insert into edges values ('l%d', 'l%d', 'calls', %d);", i, i-1, 4+4*i)
		} else {
			cg += "insert into edges values ('l0', 'r', 'calls', 4);"
		}
	}
	for name, src := range map[string]string{
		"rec.go":      "package a\n\nfunc Rec() {}\n",
		"a.go":        "package a\n\nfunc A() {\n\tRec()\n}\n\nfunc D() {\n\tRec()\n\tRec()\n}\n",
		"b.go":        "package a\n\nfunc B() {\n\tA()\n}\n\nfunc C() {\n\tA()\n}\n",
		"main.go":     "package a\n\nfunc main() {\n\tB()\n\tC()\n}\n",
		"x.go":        "package a\n\nfunc X() {\n\tRec()\n\tY()\n}\n\nfunc Y() {\n\tX()\n}\n",
		"rec_test.go": "package a\n\nfunc TestD(t *testing.T) {\n\tD()\n}\n",
		"l.go":        l.String(),
		"n.go":        "package a\n\nfunc N() {\n\tRec()\n}\n",
		"i.go":        "package a\n\ntype recorder interface {\n\tRec()\n}\n",
		"m.go":        "package a\n\nfunc (P) String() string {\n\tRec()\n\treturn \"\"\n}\n\nfunc (Q) String() string {\n\tRec()\n\tRec()\n\treturn \"\"\n}\n",
		"w_test.go":   "package a\n\nfunc TestW(t *testing.T) {\n\tRec()\n\tRec()\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db := `const { DatabaseSync } = require("node:sqlite"); const db = new DatabaseSync(process.argv[1]);
db.exec("create table nodes (id text, kind text, name text, file_path text, start_line int, end_line int);" +
  "create table edges (source text, target text, kind text, line int); create table files (path text);" +
  "insert into nodes values ('r', 'function', 'Rec', 'rec.go', 3, 3), ('a', 'function', 'A', 'a.go', 3, 5)," +
  " ('d', 'function', 'D', 'a.go', 7, 10), ('b', 'function', 'B', 'b.go', 3, 5), ('c', 'function', 'C', 'b.go', 7, 9)," +
  " ('m', 'function', 'main', 'main.go', 3, 6), ('x', 'function', 'X', 'x.go', 3, 6), ('y', 'function', 'Y', 'x.go', 8, 10)," +
  " ('t', 'function', 'TestD', 'rec_test.go', 3, 5);" +
  "insert into edges values ('a', 'r', 'calls', 4), ('d', 'r', 'calls', 8), ('x', 'r', 'calls', 4), ('b', 'a', 'calls', 4)," +
  " ('c', 'a', 'calls', 8), ('m', 'b', 'calls', 4), ('m', 'c', 'calls', 5), ('x', 'y', 'calls', 5), ('y', 'x', 'calls', 9), ('t', 'd', 'calls', null);" +
  process.argv[2])`
	if err := os.MkdirAll(filepath.Join(dir, ".codegraph"), 0o750); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", "-e", db, filepath.Join(dir, ".codegraph", "codegraph.db"), cg).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	root, err := sb.Pinned()
	if err != nil {
		t.Fatal(err)
	}
	idx := &cgIndex{dbs: []cgDB{{DB: filepath.Join(dir, ".codegraph", "codegraph.db")}}}
	run := func() (map[string][]Symbol, []Shape, []Node, bool) {
		syms := map[string][]Symbol{
			"Rec": {{Path: "rec.go", Line: 3, From: 3, To: 3, Exact: true, Refs: []Ref{
				{Path: "a.go", Line: 4, In: "A"},
				{Path: "a.go", Line: 8, In: "D"},
				{Path: "a.go", Line: 9, In: "D"}, // two kept lines of one declaration: one structure
				{Path: "x.go", Line: 4, In: "X"},
				{Path: "z.go", Line: 2, In: "Z"},
				{Path: "l.go", Line: 4, In: "L0"},
				{Path: "n.go", Line: 4, In: "N"},
				{Path: "i.go", Line: 4, In: "Rec"}, // the index puts a listing in the method's own node
				{Path: "w_test.go", Line: 4, In: "TestW"},
				{Path: "m.go", Line: 4, In: "String"},
				{Path: "m.go", Line: 9, In: "String", Rel: "call"}, // the index's word on line 9 only
			}}},
			"Other": {{Path: "rec.go", Line: 9, Refs: []Ref{{Path: "b.go", Line: 8, In: "C"}}}},
		}
		shapes, nodes, over := idx.uses(sb, root, map[string]bool{"rec.go": true}, map[string]bool{"rec.go:3": true}, syms)
		return syms, shapes, nodes, over
	}
	syms, shapes, nodes, over := run()
	want := `A decl up B call@b.go:4
D decl end tests D
B decl up main call@main.go:4
Rec decl end nograph i.go
L0 decl up L1 call@l.go:8
L1 decl up L2 call@l.go:12
L2 decl up L3 call@l.go:16
L3 decl up L4 call@l.go:20
L4 decl up L5 call@l.go:24
L5 decl up L6 call@l.go:28
L6 decl up L7 call@l.go:32
L7 decl end bound 
String decl end nograph m.go
String decl end nograph m.go
main call-in end main 
N call-in end nocaller 
X decl up Y call@x.go:9
Y decl end cycle X
Z decl end nograph z.go
C decl end byname 
`
	if got := waysOf(nodes); over || got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	var refs []string
	for _, r := range syms["Rec"][0].Refs {
		ref := fmt.Sprintf("%s:%d %s sh%d", r.Path, r.Line, r.Rel, r.Shape)
		if r.Via != "" {
			ref += " via " + r.Via
		}
		if r.Up > 0 {
			ref += " up " + nodes[r.Up-1].Name
		}
		if r.Test {
			ref += " test"
		}
		refs = append(refs, ref)
	}
	sh := func(i int) int { return syms["Rec"][0].Refs[i].Shape }
	wantRefs := fmt.Sprintf(`a.go:4  sh%d up A
a.go:8  sh%d up D
a.go:9  sh%[2]d up D
x.go:4  sh%d up X
z.go:2  sh0 up Z
l.go:4  sh%d up L0
n.go:4  sh%d up N
i.go:4 tie sh%d via recorder up Rec
w_test.go:4  sh0 test
w_test.go:5  sh0 test
m.go:4  sh%d up String
m.go:9 call sh%d up String
m.go:10  sh%[8]d up String`, sh(0), sh(2), sh(3), sh(5), sh(6), sh(7), sh(10), sh(11))
	if got := strings.Join(refs, "\n"); got != wantRefs || syms["Rec"][0].Uses != 2 {
		t.Errorf("refs, %d more uses:\n%s\nwant\n%s", syms["Rec"][0].Uses, got, wantRefs)
	}
	for i, r := range syms["Rec"][0].Refs {
		if (r.Shape == 0) != (r.Test || r.Path == "z.go") {
			t.Errorf("ref %d %s:%d: a structure for every use but a test's and one in a file the checkout lacks", i, r.Path, r.Line)
		}
	}
	if sh(10) == sh(11) {
		t.Errorf("the two String methods share structure %d", sh(10))
	}
	if d := shapes[sh(1)-1]; d.Path != "a.go" || d.Span != [2]int{7, 10} || len(d.Rows) != 3 || !d.Rows[1].Use || !d.Rows[2].Use {
		t.Errorf("D's shape, both lines marked: %+v", d)
	}
	if q := shapes[sh(11)-1]; q.Span != [2]int{8, 12} {
		t.Errorf("Q.String's shape %+v", q)
	}
	for _, s := range shapes {
		if s.Path == "w_test.go" {
			t.Errorf("a structure for a test: %+v", s)
		}
	}

	// the same input, the same output: no map's order in the shapes or the indexes into them
	syms2, shapes2, nodes2, _ := run()
	if !reflect.DeepEqual(shapes, shapes2) || !reflect.DeepEqual(nodes, nodes2) || !reflect.DeepEqual(syms, syms2) {
		t.Errorf("two runs differ")
	}
}

// Past maxShapes the budget keeps the uses in the PR's files first and says it cut.
func TestUsesBudget(t *testing.T) {
	sb, dir := sandbox(t)
	var src strings.Builder
	src.WriteString("package a\n")
	syms := map[string][]Symbol{}
	for i := range maxShapes + 10 {
		name := fmt.Sprintf("R%03d", i)
		fmt.Fprintf(&src, "\nfunc F%d() {\n\t%s()\n}\n", i, name)
		syms[name] = []Symbol{{Path: "rec.go", Line: 3, Exact: true, Refs: []Ref{{Path: "f.go", Line: 3 + 4*i, In: fmt.Sprintf("F%d", i)}}}}
	}
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(src.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pr.go"), []byte("package a\n\nfunc P() {\n\tZ()\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	syms["Z"] = []Symbol{{Path: "rec.go", Line: 5, Exact: true, Refs: []Ref{{Path: "pr.go", Line: 4, In: "P"}}}}
	root, err := sb.Pinned()
	if err != nil {
		t.Fatal(err)
	}
	shapes, _, over := (&cgIndex{}).uses(sb, root, map[string]bool{"pr.go": true}, nil, syms)
	if !over || len(shapes) != maxShapes || syms["Z"][0].Refs[0].Shape == 0 || syms["R000"][0].Refs[0].Shape == 0 {
		t.Errorf("over %v, %d shapes, pr.go's use %+v", over, len(shapes), syms["Z"][0].Refs[0])
	}
}

// Past maxRefs every line still counts: the index's places past the refs kept are read for their declarations'
// other lines. F21 names Rec twice, the index has its first line only.
func TestUsesPast(t *testing.T) {
	sb, dir := sandbox(t)
	var src strings.Builder
	src.WriteString("package a\n")
	idx := &cgIndex{Nodes: []cgNode{{Name: "Rec", Path: "rec.go", Kind: "function", Line: 3, End: 3}}}
	for i := range maxRefs + 2 {
		fmt.Fprintf(&src, "\nfunc F%d() {\n\tRec()\n", i)
		if i == maxRefs+1 {
			src.WriteString("\tRec()\n")
		}
		src.WriteString("}\n")
		idx.Edges = append(idx.Edges, cgEdge{
			Line: 4 + 4*i, Kind: "calls", SName: fmt.Sprintf("F%d", i), SKind: "function", SPath: "f.go",
			SLine: 3 + 4*i, SEnd: 5 + 4*i, TName: "Rec", TPath: "rec.go", TLine: 3,
		})
	}
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(src.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := sb.Pinned()
	if err != nil {
		t.Fatal(err)
	}
	syms := map[string][]Symbol{"Rec": {{Path: "rec.go", Line: 3}}}
	idx.exact(root, map[string]bool{"rec.go": true}, nil, syms)
	if s := syms["Rec"][0]; s.Uses != maxRefs+2 || len(s.past) != 2 {
		t.Fatalf("%d uses, past %+v", s.Uses, s.past)
	}
	idx.uses(sb, root, map[string]bool{"rec.go": true}, nil, syms)
	if s := syms["Rec"][0]; s.Uses != maxRefs+3 || len(s.Refs) != maxRefs {
		t.Errorf("%d uses, %d refs: F21's second line counts", s.Uses, len(s.Refs))
	}
}

// A declaration the PR adds reaches the table only its SQL names: the Reaches station, its joint written at the
// line the string names it, the string marked in the joint's structure. The same in a test file: neither.
func TestUsesReach(t *testing.T) {
	sb, dir := sandbox(t)
	src := "package a\n\nfunc (s *Store) Record(ctx context.Context, its []Item) error {\n\tfor _, it := range its {\n" +
		"\t\tif _, err := s.db.ExecContext(ctx, \"INSERT INTO items(name) VALUES ($1)\", it.Name); err != nil {\n" +
		"\t\t\treturn err\n\t\t}\n\t}\n\treturn nil\n}\n"
	for _, name := range []string{"store.go", "store_test.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := sb.Pinned()
	if err != nil {
		t.Fatal(err)
	}
	stores := []StorePath{{Kind: "writes", Table: "items", At: "store.go:5"}}
	syms := map[string][]Symbol{"Store.Record": {
		{Path: "store.go", Line: 3, From: 3, To: 10, Exact: true, Stores: stores},
		{Path: "store_test.go", Line: 3, From: 3, To: 10, Exact: true, Stores: []StorePath{{Kind: "writes", Table: "items", At: "store_test.go:5"}}},
	}}
	changed := map[string]bool{"store.go": true, "store_test.go": true}
	shapes, nodes, _ := (&cgIndex{}).uses(sb, root, changed, map[string]bool{"store.go:5": true, "store_test.go:5": true}, syms)
	if r := syms["Store.Record"][1].Reach; len(r) != 0 || len(shapes) != 1 {
		t.Errorf("a test's helper: reach %v, %d shapes; no way and no structure for tests", r, len(shapes))
	}
	if g := (&useBook{groups: map[string]*useGroup{}}).at("store_test.go", "TestRecord", "Record", 5, 2); g.rank != 5 {
		t.Errorf("a joint in a test file asks for a structure: rank %d", g.rank)
	}
	s := syms["Store.Record"][0]
	if len(s.Reach) != 1 {
		t.Fatalf("reach %v, nodes %+v", s.Reach, nodes)
	}
	n := nodes[s.Reach[0]-1]
	if n.Name != "items" || n.Kind != "table" || n.End == nil || n.End.Kind != "table" || n.Up != 0 || n.JUp == nil ||
		*n.JUp != (Joint{Rel: "write", Path: "store.go", Line: 5, Shape: n.JUp.Shape}) || n.JUp.Shape == 0 {
		t.Fatalf("station %+v, joint %+v", n, n.JUp)
	}
	if rows := shapes[n.JUp.Shape-1].Rows; !slices.ContainsFunc(rows, func(r ShapeRow) bool { return r.Use && r.Line == 5 }) {
		t.Errorf("the SQL's line unmarked:\n%s", rowsOf(shapes[n.JUp.Shape-1]))
	}
}

// From a route the way goes down the handler's call that reads its input to the table that call reads: the route
// station QUERY shop.list goes on (no end), called at items.ts:27, listItems read at the line that names orders.
// GET /items' named handler getItems is a station of its own, then its call that passes the input on, not the one
// before it that doesn't (renameUser, which writes users).
func TestUsesTS(t *testing.T) {
	if _, err := os.Stat(filepath.Join(tsAPI, "dist", "api", "sync", "api.js")); err != nil {
		t.Skip("no TypeScript 7.1 at DIFFHARBOR_TS")
	}
	sb, dir := sandbox(t)
	if err := os.CopyFS(dir, os.DirFS("testdata/ts")); err != nil {
		t.Fatal(err)
	}
	root, err := sb.Pinned()
	if err != nil {
		t.Fatal(err)
	}
	idx := &cgIndex{dbs: []cgDB{{Prefix: ""}}}
	idx.tsEdges(sb, dir)
	syms := map[string][]Symbol{
		"listItems": {{Path: "src/data.ts", Line: 18, Exact: true, Refs: []Ref{{Path: "src/items.ts", Line: 27, In: "query shop.list"}}}},
		"getItems":  {{Path: "src/api.ts", Line: 14, Exact: true, Refs: []Ref{{Path: "src/api.ts", Line: 18, In: "get /items"}}}},
	}
	_, nodes, _ := idx.uses(sb, root, nil, nil, syms)
	var got strings.Builder
	for _, n := range nodes {
		fmt.Fprintf(&got, "%s %s %s", n.Method, n.Name, n.Kind)
		if n.Down > 0 {
			fmt.Fprintf(&got, " down %s %s@%s:%d", nodes[n.Down-1].Name, n.JDown.Rel, n.JDown.Path, n.JDown.Line)
		}
		if n.End != nil {
			fmt.Fprintf(&got, " end %s", n.End.Kind)
		}
		got.WriteString("\n")
	}
	want := `GET /items route down getItems call@src/api.ts:18
QUERY shop.list route down listItems call@src/items.ts:27
 getItems decl down listItems call@src/api.ts:16
 listItems decl down orders read@src/data.ts:18
 orders table end table
`
	if got.String() != want {
		t.Errorf("got\n%s\nwant\n%s", got.String(), want)
	}
	if r := syms["listItems"][0].Refs[0]; r.Up == 0 || nodes[r.Up-1].Kind != "route" || r.Shape == 0 {
		t.Errorf("the use's station and structure: %+v", r)
	}
}

// Past the node budget a way ends "nograph" at the last station placed.
func TestPlaceNodes(t *testing.T) {
	sts := map[string]*station{"z": {node: Node{Name: "far", Kind: "decl"}, rank: 4}}
	for i := range maxNodes {
		sts[fmt.Sprintf("a%03d", i)] = &station{node: Node{Name: "n", Kind: "decl", Path: "a.go", JUp: &Joint{Rel: "call", Path: "a.go", Line: 1}}, up: "z", rank: 1}
	}
	nodes, place := placeNodes(sts)
	if n := nodes[0]; len(nodes) != maxNodes || place["z"] != 0 || n.Up != 0 || n.JUp != nil || n.End == nil || n.End.Kind != "nograph" {
		t.Errorf("%d nodes, the first %+v", len(nodes), nodes[0])
	}
}

// The ends of a way down: a subquery, a system, each after the call that leads to it.
func TestChainDown(t *testing.T) {
	sb, _ := sandbox(t)
	root, err := sb.Pinned()
	if err != nil {
		t.Fatal(err)
	}
	g := map[declKey][]cgEdge{{"a.go", "Run"}: {{Kind: "calls", Line: 5, SPath: "a.go", SName: "Run", TPath: "b.go", TName: "list", TLine: 3}}}
	via := []StoreCall{{At: "a.go:5", Callee: "list"}}
	for _, c := range []struct {
		end  any
		want string
	}{
		{
			&StorePath{Kind: "reads", Table: "recent", TPath: "q.go", TLine: 2, Sub: true, At: "b.go:4", Calls: via},
			"list decl call@a.go:5 in Run\nrecent subquery read@b.go:4 in list end subquery\n",
		},
		{
			&OutsideCall{System: "api.example.com", URLAt: "c.go:1", Calls: append(via, StoreCall{At: "b.go:6", Callee: "Do"})},
			"list decl call@a.go:5 in Run\napi.example.com system request@b.go:6 in list end system\n",
		},
	} {
		var got strings.Builder
		for _, s := range chainDown(g, declKey{"a.go", "Run"}, c.end, sources(root)) {
			fmt.Fprintf(&got, "%s %s %s@%s:%d in %s", s.node.Name, s.node.Kind, s.joint.Rel, s.joint.Path, s.joint.Line, s.in)
			if s.node.End != nil {
				fmt.Fprintf(&got, " end %s", s.node.End.Kind)
			}
			got.WriteString("\n")
		}
		if got.String() != c.want {
			t.Errorf("got\n%s\nwant\n%s", got.String(), c.want)
		}
	}
}
