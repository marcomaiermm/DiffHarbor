package report

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// call each other; z.go, not in the code graph; a name found by name. D's second line is a use of its own.
func TestUsesWalk(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	sb, dir := sandbox(t)
	for name, src := range map[string]string{
		"rec.go":      "package a\n\nfunc Rec() {}\n",
		"a.go":        "package a\n\nfunc A() {\n\tRec()\n}\n\nfunc D() {\n\tRec()\n\tRec()\n}\n",
		"b.go":        "package a\n\nfunc B() {\n\tA()\n}\n\nfunc C() {\n\tA()\n}\n",
		"main.go":     "package a\n\nfunc main() {\n\tB()\n\tC()\n}\n",
		"x.go":        "package a\n\nfunc X() {\n\tRec()\n\tY()\n}\n\nfunc Y() {\n\tX()\n}\n",
		"rec_test.go": "package a\n\nfunc TestD(t *testing.T) {\n\tD()\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db := `const { DatabaseSync } = require("node:sqlite"); const db = new DatabaseSync(process.argv[1]);
db.exec("create table nodes (id text, kind text, name text, file_path text, start_line int, end_line int);" +
  "create table edges (source text, target text, kind text, line int); create table files (path text);" +
  "insert into files values ('rec.go'), ('a.go'), ('b.go'), ('main.go'), ('x.go'), ('rec_test.go');" +
  "insert into nodes values ('r', 'function', 'Rec', 'rec.go', 3, 3), ('a', 'function', 'A', 'a.go', 3, 5)," +
  " ('d', 'function', 'D', 'a.go', 7, 10), ('b', 'function', 'B', 'b.go', 3, 5), ('c', 'function', 'C', 'b.go', 7, 9)," +
  " ('m', 'function', 'main', 'main.go', 3, 6), ('x', 'function', 'X', 'x.go', 3, 6), ('y', 'function', 'Y', 'x.go', 8, 10)," +
  " ('t', 'function', 'TestD', 'rec_test.go', 3, 5);" +
  "insert into edges values ('a', 'r', 'calls', 4), ('d', 'r', 'calls', 8), ('x', 'r', 'calls', 4), ('b', 'a', 'calls', 4)," +
  " ('c', 'a', 'calls', 8), ('m', 'b', 'calls', 4), ('m', 'c', 'calls', 5), ('x', 'y', 'calls', 5), ('y', 'x', 'calls', 9), ('t', 'd', 'calls', null);")`
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
	idx := &cgIndex{dbs: []cgDB{{DB: filepath.Join(dir, ".codegraph", "codegraph.db")}}}
	syms := map[string][]Symbol{
		"Rec": {{Path: "rec.go", Line: 3, From: 3, To: 3, Exact: true, Refs: []Ref{
			{Path: "a.go", Line: 4, In: "A"},
			{Path: "a.go", Line: 8, In: "D"},
			{Path: "x.go", Line: 4, In: "X"},
			{Path: "z.go", Line: 2, In: "Z"},
		}}},
		"Other": {{Path: "rec.go", Line: 9, Refs: []Ref{{Path: "b.go", Line: 8, In: "C"}}}},
	}
	shapes, nodes, over := idx.uses(sb, root, map[string]bool{"rec.go": true}, map[string]bool{"rec.go:3": true}, syms)
	want := `A decl up B call@b.go:4
D decl end tests D
B decl up main call@main.go:4
main call-in end main 
X decl up Y call@x.go:9
Y decl end cycle X
Z decl end nograph z.go
C decl end byname 
`
	if got := waysOf(nodes); over || got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if s := syms["Rec"][0]; s.Uses != 1 || len(s.Refs) != 5 || s.Refs[2].Line != 9 || s.Refs[2].In != "D" || s.Refs[2].Shape != s.Refs[1].Shape {
		t.Errorf("D names Rec on line 9 too: %d more, refs %+v", s.Uses, s.Refs)
	}
	if d := shapes[syms["Rec"][0].Refs[1].Shape-1]; d.Path != "a.go" || d.Span != [2]int{7, 10} || len(d.Rows) != 3 || !d.Rows[1].Use || !d.Rows[2].Use {
		t.Errorf("D's shape, both lines marked: %+v", d)
	}
	var ups []string
	for _, r := range syms["Rec"][0].Refs {
		if r.Up > 0 {
			ups = append(ups, fmt.Sprintf("%s:%d %s", r.Path, r.Line, nodes[r.Up-1].Name))
		}
	}
	if got := strings.Join(ups, ", "); got != "a.go:4 A, a.go:8 D, a.go:9 D, x.go:4 X, z.go:2 Z" {
		t.Errorf("uses up: %s", got)
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
