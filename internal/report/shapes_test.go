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
	deep, lv := 0, 0
	for _, r := range s.Rows {
		deep = max(deep, r.Depth)
		if r.Kind == "levels" {
			lv++
			if r.Depth != 2 || r.N != 6 || *r.More != [2]int{3, 8} {
				t.Errorf("levels row %+v %v", r, *r.More)
			}
		}
	}
	if deep > maxShapeDepth || lv != 1 {
		t.Errorf("depth %d, %d levels rows:\n%s", deep, lv, rowsOf(s))
	}
	last := s.Rows[len(s.Rows)-1]
	if !last.Use || len([]rune(last.Text)) != maxRowText || !strings.HasPrefix(last.Text, "…") || !strings.HasSuffix(last.Text, "backfill") {
		t.Errorf("the use's row %q: 60 characters ending with the name", last.Text)
	}

	// three wide containers with long entries: past maxShapeBytes the folds drop their entries, then texts cut
	var ways [][]synEl
	head := synEl{K: "func", L: 1, T: "func f() {"}
	for i := range 3 {
		var kids []synKid
		for j := range 8 {
			kids = append(kids, synKid{K: "stmt", L: 2 + i*10 + j, E: 2 + i*10 + j, T: strings.Repeat("y", 58)})
		}
		kids[4].U = true
		h := head
		h.Kids, h.W = []synKid{{K: "if", L: 2 + i*10, E: 10 + i*10, T: "if c {"}}, 0
		ways = append(ways, []synEl{h, {K: "if", L: 2 + i*10, T: fmt.Sprintf("if c%d {", i), Kids: kids, W: 4}, {K: "stmt", L: 6 + i*10, T: "backfill()", W: -1}})
	}
	s, ok = shape("a.go", "backfill", [2]int{1, 40}, ways)
	if b, _ := json.Marshal(s); !ok || len(b) > maxShapeBytes {
		t.Errorf("%d bytes:\n%s", len(b), rowsOf(s))
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
	}
	var got []*synUse
	if !runNode(sb, tsEdgesJS, map[string]any{"dir": repo, "ts": tsAPI, "shapes": items}, &got) || len(got) != 2 || got[0] == nil || got[1] == nil {
		t.Fatalf("got %+v", got)
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
