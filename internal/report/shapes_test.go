package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rowsOf is a shape's rows, one line each: depth, kind, line, text, "*" for a use, the fold's count and kept lines.
func rowsOf(s Shape) string {
	var b strings.Builder
	for _, r := range s.Rows {
		fmt.Fprintf(&b, "%d %s %d %s", r.Depth, r.Kind, r.Line, r.Text)
		if r.Use {
			b.WriteString(" *")
		}
		if r.Kind == "fold" || r.Kind == "levels" {
			fmt.Fprintf(&b, " n%d", r.N)
			for _, k := range r.Kept {
				fmt.Fprintf(&b, " %d", k.Line)
			}
			if r.More != nil {
				fmt.Fprintf(&b, " more %d-%d", r.More[0], r.More[1])
			}
		}
		if r.Cond != nil {
			fmt.Fprintf(&b, " if %s@%d %s", r.Cond.Name, r.Cond.Line, r.Cond.Text)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// A Go function that names backfill twice: under an if, whose condition comes from the line above, and in a map
// literal, whose entries off the way fold to the three nearest it.
func TestGoShape(t *testing.T) {
	src := `package a

func Run(ctx context.Context, flags Flags) error {
	cfg := load()
	ok := flags.Bool("backfill")
	if ok {
		return backfill(ctx, cfg)
	}
	jobs := map[string]Job{
		"one":  {Name: "one"},
		"two":  {Name: "two"},
		"tre":  {Name: "tre"},
		"four": {Name: "four"},
		"fill": {Name: "fill", Do: backfill},
		"five": {Name: "five"},
	}
	for _, j := range jobs {
		j.Do(ctx, cfg)
	}
	return nil
}
`
	u, ok := goShape(parseGo([]byte(src)), 7, "backfill", true)
	if !ok || u.Span != [2]int{3, 21} || fmt.Sprint(u.Lines) != "[7 14]" {
		t.Fatalf("got %v %+v", ok, u)
	}
	s, ok := shape("a.go", "backfill", u.Span, u.Ways)
	want := `0 func 3 func Run(ctx context.Context, flags Flags) error {
1 fold 0  n2 4 5
1 if 6 if ok { if ok@5 flags.Bool("backfill")
2 stmt 7 return backfill(ctx, cfg) *
1 object 9 jobs := map[string]Job{
2 fold 0  n4 11 12 13 more 10-10
2 object 14 "fill": {Name: "fill", Do: backfill}, *
2 fold 0  n1 15
1 fold 0  n2 17 20
`
	if got := rowsOf(s); !ok || got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}

	for _, c := range []struct {
		why, name, src, want string
		line                 int
	}{
		{"a chain over lines: one chain row, the use's link a row of its own, the others its entries", "items", `package a

func List(db *DB) ([]Row, error) {
	return db.Select("id").
		From(items).
		Where("x = ?", 1).
		All()
}
`, `0 func 3 func List(db *DB) ([]Row, error) {
1 chain 4 return db.Select("id").
2 link 5 From(items). *
2 fold 0  n2 6 7
`, 5},
		{"the if's own init declares err; ok's declaration is in a block the if isn't in: no notes", "backfill", `package a

func Run(ctx context.Context) error {
	cfg, err := load()
	if err := validate(ctx); err != nil {
		return backfill(ctx, cfg)
	}
	{
		ok := true
		_ = ok
	}
	if ok {
		backfill(ctx, cfg)
	}
	return err
}
`, `0 func 3 func Run(ctx context.Context) error {
1 fold 0  n1 4
1 if 5 if err := validate(ctx); err != nil {
2 stmt 6 return backfill(ctx, cfg) *
1 fold 0  n1 8
1 if 12 if ok {
2 stmt 13 backfill(ctx, cfg) *
1 fold 0  n1 15
`, 6},
		{"a table only SQL names: the string at the line is the use", "items", `package a

func (s *Store) Record(ctx context.Context, its []Item) error {
	for _, it := range its {
		if _, err := s.db.ExecContext(ctx, "INSERT INTO items(name) VALUES ($1)", it.Name); err != nil {
			return err
		}
	}
	return nil
}
`, `0 func 3 func (s *Store) Record(ctx context.Context, its []Item) err…
1 loop 4 for _, it := range its {
2 if 5 if _, err := s.db.ExecContext(ctx, "INSERT INTO items(name)… *
3 fold 0  n1 6
1 fold 0  n1 9
`, 5},
		{"arguments that share a later line: one entry, one row", "items", `package a

func Save(ctx context.Context, db *DB, a, b, c int) error {
	_, err := db.Exec(ctx, "INSERT INTO items(x)",
		a, b, c)
	return err
}
`, `0 func 3 func Save(ctx context.Context, db *DB, a, b, c int) error {
1 stmt 4 _, err := db.Exec(ctx, "INSERT INTO items(x)", *
2 fold 0  n1 5
1 fold 0  n1 6
`, 4},
	} {
		u, ok := goShape(parseGo([]byte(c.src)), c.line, c.name, true)
		if !ok {
			t.Fatalf("%s: no declaration", c.why)
		}
		s, ok := shape("a.go", c.name, u.Span, u.Ways)
		if got := rowsOf(s); !ok || got != c.want {
			t.Errorf("%s: got\n%s\nwant\n%s", c.why, got, c.want)
		}
	}

	// a wrapper of the same name: its own name is no use, only the line that calls the subject
	u, ok = goShape(parseGo([]byte("package a\n\nfunc (b B) Close() error {\n\treturn b.a.Close()\n}\n")), 4, "Close", true)
	if s, _ := shape("a.go", "Close", u.Span, u.Ways); !ok || fmt.Sprint(u.Lines) != "[4]" || len(s.Rows) == 0 || s.Rows[0].Use {
		t.Errorf("wrapper: lines %v, rows\n%s", u.Lines, rowsOf(s))
	}
}

// A way 12 levels deep keeps the outer two, one levels row and the inner five; a long row keeps the use in view.
func TestShapeBounds(t *testing.T) {
	var way []synEl
	for i := 1; i <= 12; i++ {
		way = append(way, synEl{K: "object", L: i, T: fmt.Sprintf("k%d: {", i), Kids: []synKid{{K: "object", L: i + 1, E: 13, T: "k"}}, W: 0})
	}
	long := strings.Repeat("x", 70) + " + backfill(a, b) + y"
	way = append(way, synEl{K: "stmt", L: 13, T: long, W: -1})
	s, ok := shape("a.ts", "backfill", [2]int{1, 14}, [][]synEl{way})
	if !ok {
		t.Fatal("no shape")
	}
	depth, lv := 0, 0
	for _, r := range s.Rows {
		depth = max(depth, r.Depth)
		if r.Kind == "levels" {
			lv++
			if r.Depth != 2 || r.N != 6 || *r.More != [2]int{3, 8} {
				t.Errorf("levels row %+v %v", r, *r.More)
			}
		}
	}
	if depth > maxShapeDepth || lv != 1 {
		t.Errorf("depth %d, %d levels rows:\n%s", depth, lv, rowsOf(s))
	}
	last := s.Rows[len(s.Rows)-1]
	if !last.Use || len([]rune(last.Text)) != maxRowText || !strings.HasPrefix(last.Text, "…") || !strings.HasSuffix(last.Text, "backfill") {
		t.Errorf("the use's row %q: 60 characters ending with the name", last.Text)
	}

	// the same depth with a fold before the way at depth 2 and one inside the cut, and a second, shallow use under the
	// row of depth 2: that row stays, so the cut starts below it, and both uses stay marked
	var deep, shallow []synEl
	for i := 1; i <= 12; i++ {
		kids := []synKid{{K: "object", L: 10 * (i + 1), E: 200, T: "k"}}
		w := 0
		if i == 2 || i == 5 {
			kids, w = append([]synKid{{K: "stmt", L: 10*i + 1, E: 10*i + 1, T: "x"}}, kids...), 1
		}
		if i == 3 {
			kids = append(kids, synKid{K: "stmt", L: 199, E: 199, T: "backfill()", U: true})
		}
		el := synEl{K: "object", L: 10 * i, T: fmt.Sprintf("k%d: {", i), Kids: kids, W: w}
		deep = append(deep, el)
		if i <= 3 {
			if i == 3 {
				el.W = 1
			}
			shallow = append(shallow, el)
		}
	}
	deep = append(deep, synEl{K: "stmt", L: 130, T: "backfill()", W: -1})
	shallow = append(shallow, synEl{K: "stmt", L: 199, T: "backfill()", W: -1})
	s, ok = shape("a.ts", "backfill", [2]int{10, 200}, [][]synEl{deep, shallow})
	uses, lv := 0, 0
	for _, r := range s.Rows {
		if r.Depth > maxShapeDepth-1 || r.More != nil && r.More[0] == 0 {
			t.Errorf("row %+v", r)
		}
		if r.Use {
			uses++
		}
		if r.Kind == "levels" {
			lv++
			if r.Depth != 3 || r.N != 6 || *r.More != [2]int{40, 90} {
				t.Errorf("levels row %+v %v: the rows of depth 3 to 8, lines 40 to 90", r, *r.More)
			}
		}
	}
	if !ok || uses != 2 || lv != 1 {
		t.Errorf("%d uses, %d levels rows:\n%s", uses, lv, rowsOf(s))
	}

	// three wide containers with long entries: past maxShapeBytes the folds drop their entries, then texts cut
	// and the function's own entries after them, the outermost fold: it drops first, the innermost keeps its entries
	var ways [][]synEl
	head := synEl{K: "func", L: 1, T: "func f() {"}
	for i := range 3 {
		head.Kids = append(head.Kids, synKid{K: "if", L: 2 + i*10, E: 10 + i*10, T: "if c {"})
	}
	for j := range 3 {
		head.Kids = append(head.Kids, synKid{K: "stmt", L: 32 + j, E: 32 + j, T: strings.Repeat("z", 58)})
	}
	for i := range 3 {
		var kids []synKid
		for j := range 8 {
			kids = append(kids, synKid{K: "stmt", L: 2 + i*10 + j, E: 2 + i*10 + j, T: strings.Repeat("y", 58)})
		}
		kids[4].U = true
		h := head
		h.W = i
		ways = append(ways, []synEl{h, {K: "if", L: 2 + i*10, T: fmt.Sprintf("if c%d {", i), Kids: kids, W: 4}, {K: "stmt", L: 6 + i*10, T: "backfill()", W: -1}})
	}
	s, ok = shape("a.go", "backfill", [2]int{1, 40}, ways)
	if b, _ := json.Marshal(s); !ok || len(b) > maxShapeBytes {
		t.Errorf("%d bytes:\n%s", len(b), rowsOf(s))
	}
	var outer, inner *ShapeRow
	for i, r := range s.Rows {
		if r.Kind == "fold" && r.Depth == 1 {
			outer = &s.Rows[i]
		}
		if r.Kind == "fold" && r.Depth == 2 {
			inner = &s.Rows[i]
		}
	}
	if outer == nil || inner == nil || outer.Kept != nil || outer.More == nil || *outer.More != [2]int{32, 34} || inner.Kept == nil {
		t.Errorf("the outermost fold drops first, the last inner one keeps its entries:\n%s", rowsOf(s))
	}

	// rows too long even without the folds' entries: every text cuts to shortText, the use in view
	ways = nil
	head = synEl{K: "func", L: 1, T: "func f() {"}
	for i := range 20 {
		head.Kids = append(head.Kids, synKid{K: "stmt", L: 2 + i, E: 2 + i, T: "x"})
	}
	for i := range 20 {
		h := head
		h.W = i
		ways = append(ways, []synEl{h, {K: "stmt", L: 2 + i, T: strings.Repeat("w", 50) + " + backfill(a)", W: -1}})
	}
	s, ok = shape("a.go", "backfill", [2]int{1, 22}, ways)
	for _, r := range s.Rows {
		if n := len([]rune(r.Text)); !ok || n > shortText || r.Use && !strings.HasSuffix(r.Text, "backfill") {
			t.Errorf("row %q: at most %d characters, a use's ending with the name", r.Text, shortText)
		}
	}
}

func TestCutRow(t *testing.T) {
	for _, c := range []struct {
		t, name string
		use     bool
		n       int
		want    string
	}{
		{"a   b\n\tc", "", false, 60, "a b c"},
		{"abcdefghij", "", false, 5, "abcd…"},
		{"abcdef backfill", "backfill", true, 10, "… backfill"},
		{"abcdef backfill", "backfill", false, 10, "abcdef ba…"},
		{"backfillX abcdef backfill", "backfill", true, 12, "…ef backfill"}, // backfillX isn't the name
		{"backfill abcdefghij", "backfill", true, 10, "backfill …"},         // in view already
	} {
		if got := cutRow(c.t, c.name, c.use, c.n); got != c.want {
			t.Errorf("cutRow(%q, %d) = %q, want %q", c.t, c.n, got, c.want)
		}
	}
}

// The checker's shapes in testdata/ts/src/items.ts: KINDS in a schema's chain, ok in a route's inline handler.
func TestTSShape(t *testing.T) {
	if _, err := os.Stat(filepath.Join(tsAPI, "dist", "api", "sync", "api.js")); err != nil {
		t.Skip("no TypeScript 7.1 at DIFFHARBOR_TS")
	}
	repo, err := filepath.Abs("testdata/ts")
	if err != nil {
		t.Fatal(err)
	}
	sb, _ := sandbox(t)
	items := []map[string]any{
		{"file": "src/items.ts", "line": 9, "name": "KINDS", "in": "filtersSchema", "ways": true},
		{"file": "src/items.ts", "line": 27, "name": "listItems", "in": "query shop.list", "ways": true},
		{"file": "src/data.ts", "line": 11, "name": "audit_log", "in": "rawCount", "ways": true}, // only SQL names it
		{"file": "src/notify.ts", "line": 5, "name": "send", "in": "send", "ways": false},        // an interface lists it
	}
	var got []*synUse
	if !runNode(sb, tsEdgesJS, map[string]any{"dir": repo, "ts": tsAPI, "shapes": items}, &got) || len(got) != 4 || got[0] == nil || got[1] == nil || got[2] == nil || got[3] == nil {
		t.Fatalf("got %+v", got)
	}
	if w := got[2].Ways; fmt.Sprint(got[2].Lines) != "[11]" || len(w) != 1 || !strings.HasPrefix(w[0][len(w[0])-1].T, `"SELECT count(*) FROM audit_log"`) {
		t.Errorf("audit_log: the string is the use: %+v", got[2])
	}
	if got[3].Iface != "Sender" {
		t.Errorf("send listed in Sender: %+v", got[3])
	}
	s, ok := shape("src/items.ts", "KINDS", got[0].Span, got[0].Ways)
	if !ok || fmt.Sprint(got[0].Lines) != "[9 10]" {
		t.Errorf("KINDS: %v %+v", ok, got[0])
	}
	want := `0 object 6 export const filtersSchema = s.object({
1 fold 0  n1 7
1 chain 8 kinds: s
2 link 9 .array(s.enum(KINDS)) *
2 link 10 .max(KINDS.length) *
2 fold 0  n1 11
1 fold 0  n2 12 13
`
	if got := rowsOf(s); got != want {
		t.Errorf("KINDS:\n%s\nwant\n%s", got, want)
	}
	s, ok = shape("src/items.ts", "listItems", got[1].Span, got[1].Ways)
	want = `0 object 20 export const itemsRouter = router({
1 object 21 shop: {
2 chain 22 list: proc
3 fold 0  n1 23
3 func 24 .query(async ({ input, ctx }) => {
4 fold 0  n1 25
4 if 26 if (ok) { if ok@25 input.team !== "" && !!ctx;
5 stmt 27 return listItems(input.team); *
4 fold 0  n1 29
`
	if got := rowsOf(s); !ok || got != want {
		t.Errorf("listItems:\n%s\nwant\n%s", got, want)
	}
}
