package report

import (
	"diffharbor/internal/checks"
	"diffharbor/internal/diff"
	"diffharbor/internal/review"
	"fmt"
	"html/template"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const prDiff = `diff --git a/api/handler.go b/api/handler.go
--- a/api/handler.go
+++ b/api/handler.go
@@ -1,2 +1,3 @@
 package api
+func Handle() { store.Save("</script><script>alert(1)</script>") }
 // end
diff --git a/store/store.go b/store/store.go
--- a/store/store.go
+++ b/store/store.go
@@ -5 +5 @@
-func Save(s string) {}
+func Save(s string) error { return nil }
diff --git a/api/handler_test.go b/api/handler_test.go
--- a/api/handler_test.go
+++ b/api/handler_test.go
@@ -1 +1,2 @@
 package api
+func TestHandle(t *testing.T) {}
diff --git a/README.md b/README.md
--- a/README.md
+++ b/README.md
@@ -1 +1 @@
-old
+new
diff --git a/gone.go b/gone.go
deleted file mode 100644
--- a/gone.go
+++ /dev/null
@@ -1 +0,0 @@
-package gone
`

func TestRender(t *testing.T) {
	b, _, err := Render(Data{
		Repo: "acme/shop", Number: 7, Title: "Save <orders>", URL: "https://gitlab.com/acme/shop/-/merge_requests/7", SHA: "0123456789abcdef",
		Forge: Forge{
			Name: "GitLab", Ref: "!", Blob: "https://gitlab.com/acme/shop/-/blob/0123456789abcdef/",
			PRCommit: "https://gitlab.com/acme/shop/-/merge_requests/7/diffs?commit_id=",
		}, // nothing in the page is GitHub's
		Summary: "Handle saves orders.", Diagram: "sequenceDiagram", Decisions: []review.Decision{{Text: "Should a failed save retry?"}},
		Story: review.Story{Problem: "Orders were lost.", Before: "POST /orders: 200, nothing stored.", After: "POST /orders: the order is in the store."},
		Flow: []review.Step{
			{Title: "Storage first", Why: "Save is what Handle calls.", Files: []string{"store/store.go", "missing.go"}},
			{Title: "Then the handler", Files: []string{"api/handler.go"}},
		},
		Commits:  []review.Commit{{SHA: "aaaaaaaaaa", Subject: "Add Save", Files: 1, Add: 1, Del: 1}, {SHA: "bbbbbbbbbb", Subject: "wip", Files: 1}},
		Outline:  diff.Analyze(prDiff),
		Findings: []review.Comment{{Path: "api/handler.go", Line: 2, Severity: "major", Title: "Error dropped", Body: " \n"}, {Path: "other.go", Body: "elsewhere"}},
		Diff:     prDiff,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	order := []string{
		"Orders were lost.", "Handle saves orders.", "Before", "POST /orders: 200, nothing stored.",
		"After", "POST /orders: the order is in the store.", `class="vd"`, "Not ready to merge", "Fix first: Error dropped", "handler.go:2: Error dropped",
		"Findings", "Decisions", "Fix before merging", `href="#finding-1"`, "Error dropped", "handler.go:2 · step 2", "not verified",
		"Minor", `href="#finding-2"`, "elsewhere", "other.go · not in the diff", "Outside the diff", `id="finding-2"`,
		`href="https://gitlab.com/acme/shop/-/blob/0123456789abcdef/other.go"`, "other.go at the head",
		"What only a person can settle", "Should a failed save retry?",
		"Storage first", `id="store/store.go"`, "Then the handler", `id="api/handler.go"`, `id="finding-1"`, `<span class="sev" data-sev="major">Major</span>`, `<span class="fnote-t">Error dropped</span>`,
		`id="tests"`, `title="api/handler_test.go">TestHandle<`, `id="api/handler_test.go"`, `id="other"`, `id="README.md"`,
		`id="deleted"`, `id="gone.go"`, `id="explore"`, "Look closer", "How the parts talk", `id="commits"`, "Work in progress", "/merge_requests/7/diffs?commit_id=aaaaaaaaaa", "Only this commit",
		`id="finish"`, "Request changes", "Review for !7</span>",
		"Error dropped (handler.go:2).", "Questions:\n1. Should a failed save retry?", "Minor:\n- other.go  elsewhere", "Open the draft on GitLab",
	}
	for _, w := range []string{"acme/shop!7 · Save", ">GitLab <svg", // the forge's own names
		`<span class="chip" data-sev="major">1 major</span>`, `data-progress="step-2"`, `id="progress-text" data-steps="2"`, `<span class="chip" data-del>deleted</span>`, // a step's head, a file's
		`<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;base64,PHN2Zy`, `sizes="32x32" href="data:image/png;base64,iVBORw0KGgo`, `<img src="data:image/png;base64,iVBORw0KGgo`} { // the favicons, unescaped
		if !strings.Contains(s, w) {
			t.Errorf("%q missing", w)
		}
	}
	at := 0
	for _, w := range order {
		i := strings.Index(s[at:], w)
		if i < 0 {
			t.Fatalf("%q missing or out of order", w)
		}
		at += i + len(w)
	}
	if strings.Contains(s, "<script>alert(1)") || strings.Contains(s, "Save <orders>") {
		t.Error("PR text reaches the page unescaped")
	}
	if n := strings.Count(s, `class="fnote-b"`); n != 1 { // only "elsewhere" has a body
		t.Errorf("%d finding bodies, want 1", n)
	}
	if strings.Contains(s, `id="missing.go"`) {
		t.Error("a flow file outside the diff got a block")
	}
	// the head opens the file, the Viewed box beside it is not inside the button
	_, head, _ := strings.Cut(s, `aria-controls="api/handler.go~body"`)
	button, rest, _ := strings.Cut(head, "</button>")
	if strings.Contains(button, "Viewed") || !strings.Contains(rest[:strings.Index(rest, `id="api/handler.go~body"`)], "Viewed") {
		t.Error("the Viewed box is not beside the file's button")
	}
	if strings.Count(s, `id="api/handler.go"`) != 1 {
		t.Error("a file shows twice")
	}
}

func TestStory(t *testing.T) {
	cs := []review.Commit{{Subject: "feat: add Save"}, {Subject: "fix(api): handle a nil store"}, {Subject: "test: cover Save"}, {Subject: "fixup! feat: add Save"}}
	b, _, _ := Render(Data{Repo: "a/b", Commits: cs})
	if !strings.Contains(string(b), "Each one a step") { // 1 of 4 is wip: still a story
		t.Error("conventional commits read as a story")
	}
}

func TestFold(t *testing.T) {
	d := `diff --git a/old.go b/old.go
--- a/old.go
+++ b/old.go
@@ -10,6 +10,1 @@ func keep() {
 	keep()
-func Save(s string) error {
-	if s == "" {
-		return errEmpty
-	}
-	return store.Put(s)
diff --git a/new.go b/new.go
--- a/new.go
+++ b/new.go
@@ -1,3 +1,6 @@
 import (
+	"errors"
+	log "example.com/log"
 )
+// Save stores s.
+// It refuses empty strings.
@@ -20,1 +23,7 @@
 }
+func Save(s string) error {
+	if s == "" {
+		return errEmpty
+	}
+	return store.Put(strings.TrimSpace(s))
+	return "done"
`
	files := map[string]*file{}
	var order []string
	diff.EachFile(d, func(p, f string) { files[p], order = parse(p, f, ""), append(order, p) })
	runs := fold(order, files, nil)
	got := func(p string) (s []string) {
		for _, x := range parts(files[p].Lines, runs) {
			if x.ID != 0 {
				s = append(s, fmt.Sprintf("%d %s %s", len(x.Lines), x.What, x.Where))
			}
		}
		return s
	}
	want := map[string][]string{
		"old.go": {"4 moved to new.go:24"},
		"new.go": {"6 imports and comments only ", "4 moved from old.go:11"}, // the changed Put line and return "done" stay out
	}
	for p, w := range want {
		if g := got(p); !slices.Equal(g, w) {
			t.Errorf("%s folds %q, want %q", p, g, w)
		}
	}
	if files["new.go"].Moved != 4 {
		t.Errorf("moved %d", files["new.go"].Moved)
	}
	// Save moved and changed: a change, not a move; Load only moved: a move, its link the fold
	if cs, mv := changes(files["new.go"], runs); !slices.ContainsFunc(cs, func(c change) bool { return c.Name == "Save" }) || len(mv) != 0 {
		t.Errorf("changes %+v, moves %+v", cs, mv)
	}
	body := "\tload(a)\n\tload(b)\n\tload(c)\n}\n"
	d = "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,5 +1,0 @@\n" + pre("-", "func Load() {\n"+body) +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,0 +1,5 @@\n" + pre("+", "func Load() {\n"+body)
	files, order = map[string]*file{}, nil
	diff.EachFile(d, func(p, f string) { files[p], order = parse(p, f, ""), append(order, p) })
	runs = fold(order, files, nil)
	if cs, mv := changes(files["b.go"], runs); len(cs) != 0 || len(mv) != 1 || mv[0].Name != "Load" || !strings.HasPrefix(mv[0].ID, "fold-") {
		t.Errorf("changes %+v, moves %+v", cs, mv)
	}
}

// pre puts p before each line of s.
func pre(p, s string) string {
	return p + strings.ReplaceAll(strings.TrimSuffix(s, "\n"), "\n", "\n"+p) + "\n"
}

func TestIcons(t *testing.T) {
	var page []byte
	files, _ := fs.Glob(templates, "templates/*.html")
	for _, f := range files {
		b, _ := fs.ReadFile(templates, f)
		page = append(page, b...)
	}
	for _, m := range regexp.MustCompile(`\{\{icon "([^"]+)"`).FindAllStringSubmatch(string(page), -1) {
		if icons[m[1]] == "" {
			t.Errorf("icon %q is not in icons", m[1])
		}
	}
}

func TestLoad(t *testing.T) {
	defer func(t0 *template.Template, l, f template.HTML, c string) { tmpl, logo, favicons, css = t0, l, f, c }(tmpl, logo, favicons, css)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "brand.html"), []byte(`{{define "theme"}}<style>:root{--primary:#c00}</style>{{end}}{{define "footer"}}<footer class="py-6">Acme</footer>{{end}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "logo.png"), []byte("acme"), 0o644)
	if err := Load(dir); err != nil {
		t.Fatal(err)
	}
	b, _, err := Render(Data{Repo: "a/b"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if _, err := exec.LookPath("tailwindcss"); err == nil && !strings.Contains(s, ".py-6{") { // the image has it
		t.Error("the footer's class isn't compiled")
	}
	for _, w := range []string{"--primary:#c00", `<footer class="py-6">Acme</footer>`, "base64,YWNtZQ==", `id="overview"`} {
		if !strings.Contains(s, w) {
			t.Errorf("%q missing", w)
		}
	}
	if strings.Contains(s, "Geist's grays") {
		t.Error("our theme is still there")
	}
	os.WriteFile(filepath.Join(dir, "zz.html"), []byte(`{{define "footer"}}{{.Nope}}{{end}}`), 0o644)
	if Load(dir) == nil {
		t.Error("a template naming a missing field loads")
	}
}

func TestOutline(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/n.go b/n.go\nnew file mode 100644\n--- /dev/null\n+++ b/n.go\n@@ -0,0 +1,40 @@\n+package n\n+\n")
	b.WriteString("+type T struct {\n" + strings.Repeat("+\tA int\n", 8) + "+}\n")
	b.WriteString("+// Save stores.\n+func Save() {\n" + strings.Repeat("+\tx()\n", 60) + "+}\n")
	b.WriteString("+func Small() {\n+\tx()\n+}\n")
	files := map[string]*file{}
	var order []string
	diff.EachFile(b.String(), func(p, f string) { files[p], order = parse(p, f, ""), append(order, p) })
	runs := fold(order, files, nil)
	var got []string
	for _, x := range parts(files["n.go"].Lines, runs) {
		if x.ID != 0 {
			got = append(got, fmt.Sprintf("%d %s", len(x.Lines), x.What))
		}
	}
	if !slices.Equal(got, []string{"60 in Save"}) { // the type's fields stay, Small is too short
		t.Errorf("folds %q", got)
	}
	if cs, _ := changes(files["n.go"], runs); len(cs) != 3 || cs[1].Name != "Save" || cs[1].State != "new" || cs[1].ID == "" {
		t.Errorf("changes %+v", cs)
	}
}

func TestSaid(t *testing.T) {
	d := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@ func f() {\n-\told()\n+\tnew()\n x()\n"
	files := map[string]*file{}
	diff.EachFile(d, func(p, f string) { files[p] = parse(p, f, "") })
	runs := fold([]string{"a.go"}, files, map[string]map[int]string{"a.go": {2: "a rename"}})
	if r := runs[files["a.go"].Lines[1].Run]; r.What != "routine" || r.Note != "a rename" {
		t.Errorf("run %+v", r)
	}
}

// A deleted file keeps its first maxGone lines and what moved out of it: the other side links there.
func TestTrim(t *testing.T) {
	f := &file{Lines: make([]line, maxGone+10)}
	f.Lines[maxGone+5].Run = 1
	trim(f, map[int]run{1: {Where: "a.go:1"}})
	if len(f.Lines) != maxGone+1 || f.Cut != 9 || f.Lines[maxGone].Run != 1 {
		t.Errorf("%d lines, %d cut", len(f.Lines), f.Cut)
	}
}

// a generated file's one huge line cuts the file there: what follows it is cut too, not shown out of order
func TestCut(t *testing.T) {
	f := parse("a.json", "--- a/a.json\n+++ b/a.json\n@@ -0,0 +1,3 @@\n+{\n+"+strings.Repeat("x", maxBytes)+"\n+}\n", "")
	if len(f.Lines) != 2 || f.Cut != 2 {
		t.Errorf("%d lines, %d cut", len(f.Lines), f.Cut)
	}
}

func TestSymbols(t *testing.T) {
	dir := t.TempDir()
	write := func(p, s string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n\n// Limit caps it.\nconst Limit = 3\n\ntype T struct {\n\tCount int // how many\n}\n\nfunc Use() int { return Limit }\n\nfunc Show(t T) int {\n\treturn t.Count + f().Limit\n}\n")
	write("c.go", "package a\n\ntype Rec struct {\n\tID      string `json:\"id\"`\n\tCreated int    `json:\"c\"`\n}\n")
	write("b.ts", "/** Greets. */\nexport function greet(n: string) {\n  return n\n}\ngreet(\"x\")\n")
	s := Symbols(dir, "+x := a.Limit + t.Count + Show(t)\n+greet(y)\n+Rec{}\n")
	l := s["Limit"]
	if len(l) != 1 || l[0].Sig != "const Limit = 3" || l[0].Doc != "Limit caps it." || l[0].Uses != 2 || l[0].Refs[0].In != "Use" || l[0].Kind != "constant" || l[0].Unit != "package" {
		t.Errorf("Limit %+v", l)
	}
	if n := s["Count"]; len(n) != 1 || n[0].Kind != "field of T" || n[0].Doc != "how many" {
		t.Errorf("N %+v", n)
	}
	if g := s["greet"]; len(g) != 1 || g[0].Sig != "export function greet(n: string)" || g[0].Doc != "Greets." || g[0].Uses != 1 || g[0].Kind != "function" || g[0].Unit != "" {
		t.Errorf("greet %+v", g)
	}
	// gofmt's way: a tab only to indent, columns kept by spaces, so they line up at any tab width
	if r := s["Rec"]; len(r) != 1 || r[0].Sig != "type Rec struct {\n\tID      string `json:\"id\"`\n\tCreated int    `json:\"c\"`\n}" {
		t.Errorf("Rec %+v", r)
	}
	if _, ok := s["Use"]; ok {
		t.Error("a name the diff doesn't show is indexed")
	}
}

func TestCycles(t *testing.T) {
	inv := &file{Path: "x/billing/invoice/invoice.go", Short: "billing/invoice/invoice.go", Changes: []change{{Name: "Validate"}, {Name: "Discount"}}}
	set := &file{Path: "x/billing/settings/settings.go", Short: "billing/settings/settings.go", Changes: []change{{Name: "MaxDiscount"}}}
	other := &file{Path: "x/api/api.go", Short: "api/api.go", Changes: []change{{Name: "Get"}}}
	steps := []step{{Files: []*file{set}}, {Files: []*file{inv}}, {Files: []*file{other}}}
	syms := map[string][]Symbol{
		"Validate":    {{Path: inv.Path, Calls: []string{set.Path + "\x00MaxDiscount"}}},
		"MaxDiscount": {{Path: set.Path, Calls: []string{inv.Path + "\x00Discount"}}},
		"Get":         {{Path: other.Path, Calls: []string{inv.Path + "\x00Validate"}}}, // one way only: no cycle
	}
	want := []cycle{{"billing/invoice", "billing/settings", "Validate → MaxDiscount", "MaxDiscount → Discount"}}
	for i := range 2 { // a use each way, one end in the step
		if got := cycles(steps, i, syms); !reflect.DeepEqual(got, want) {
			t.Errorf("step %d: %+v", i, got)
		}
	}
	if got := cycles(steps, 2, syms); got != nil {
		t.Errorf("a step the cycle doesn't touch: %+v", got)
	}
}

func TestWeigh(t *testing.T) {
	f := &file{Findings: []*finding{{Comment: review.Comment{Severity: "major"}}}, Lines: []line{{Findings: []*finding{{Comment: review.Comment{Severity: "major"}}, {Comment: review.Comment{Severity: "minor"}}}}}}
	if w := f.Weight(); w == nil || *w != (weighed{"major", "2 major"}) {
		t.Errorf("file: %+v", w)
	}
	g := &file{Lines: []line{{Findings: []*finding{{Comment: review.Comment{Severity: "critical"}}}}}}
	if w := (step{Files: []*file{f, g}}).Weight(); w == nil || *w != (weighed{"critical", "1 blocker"}) {
		t.Errorf("step: %+v", w)
	}
	if w := (&file{Findings: []*finding{{Comment: review.Comment{Severity: "minor"}}}}).Weight(); w != nil {
		t.Errorf("minor only: %+v", w)
	}
}

func TestGraph(t *testing.T) {
	store := &file{Path: "store.go", Short: "store.go", Changes: []change{{Name: "Save", State: "new", ID: "s"}}}
	api := &file{Path: "api.go", Short: "api.go", Changes: []change{{Name: "Handle", ID: "h"}, {Name: "Other", ID: "o"}}}
	steps := []step{{Files: []*file{store}}, {Files: []*file{api}}}
	syms := map[string][]Symbol{"Handle": {{Path: "api.go", Calls: []string{"store.go\x00Save", "api.go\x00Missing"}}}, "Save": {{Path: "elsewhere.go", Calls: []string{"api.go\x00Handle"}}}}
	g := graph(steps, 1, syms)
	want := &flow{
		Nodes: []flowNode{
			{ID: "f0", Label: "store.go", Href: "#store.go", Path: "store.go"},
			{ID: "n0", Label: "Save · step 1", Parent: "f0", Href: "#s", State: "ext", Name: "Save", Path: "store.go"},
			{ID: "f1", Label: "api.go", Href: "#api.go", Path: "api.go"},
			{ID: "n1", Label: "Handle", Parent: "f1", Href: "#h", Name: "Handle", Path: "api.go"},
		},
		Edges: []flowEdge{{Source: "n1", Target: "n0"}}, // Other names nothing: left out; Save's Calls are another file's
	}
	if !reflect.DeepEqual(g, want) {
		t.Errorf("graph %+v", g)
	}
	if g := graph(steps, 0, map[string][]Symbol{}); g != nil {
		t.Errorf("no edges, still a graph: %+v", g)
	}
	store.Short = "store/store.go"
	m := &deps{New: []newDep{{From: ".", To: "store", Why: "Handle uses Save"}}} // Save is new: so is the use
	if got := matrix(steps, syms); !reflect.DeepEqual(got, m) {
		t.Errorf("matrix %+v", got)
	}
	store.Changes[0].State = "" // both there before; and Save uses Handle back: a cycle
	syms["Save"] = []Symbol{{Path: "store.go", Calls: []string{"api.go\x00Handle"}}}
	if got := matrix(steps, syms); !reflect.DeepEqual(got, &deps{Old: []string{". → store", "store → ."}, Cycles: 1}) {
		t.Errorf("matrix, old uses %+v", got)
	}
	if got := matrix(steps, map[string][]Symbol{}); got != nil {
		t.Errorf("no uses, still a matrix: %+v", got)
	}
}

// a way in runs the PR's declarations as a tree below it: routes first, the finding on it, the step it shows in
func TestWays(t *testing.T) {
	store := &file{Path: "store.go", Changes: []change{{Name: "Save", State: "new", Sev: "major"}},
		Findings: []*finding{{Comment: review.Comment{Severity: "major"}, Href: "#store.go:9"}}}
	api := &file{Path: "api.go", Changes: []change{{Name: "Handle", sig: true}}}
	v := view{Steps: []step{{Files: []*file{store}}, {Files: []*file{api}}}, files: map[string]*file{"store.go": store, "api.go": api}}
	syms := map[string][]Symbol{
		"Save": {{Path: "store.go", Exact: true, Ways: []Way{
			{Method: "CALL", Route: "main", Path: "main.go", Line: 1, Chain: []Hop{{Name: "Save", Path: "store.go", Line: 5, In: "main.go", At: 2}}},
			{Method: "GET", Route: "/x", Path: "server.go", Line: 3, Chain: []Hop{{Name: "Handle", Path: "api.go", Line: 10, In: "server.go", At: 3}, {Name: "Save", Path: "store.go", Line: 5, In: "api.go", At: 12}}},
		}}},
		"Handle": {{Path: "api.go", Exact: true, Ways: []Way{{Method: "GET", Route: "/x", Path: "server.go", Line: 3, Chain: []Hop{{Name: "Handle", Path: "api.go", Line: 10, In: "server.go", At: 3}}}}}},
	}
	ws := ways(&v, syms)
	if len(ws) != 2 || ws[0].Method != "GET" || ws[1].Route != "main" || ws[1].Sub != "outside the PR" {
		t.Fatalf("ways %+v", ws)
	}
	g := ws[0]
	var rows []string
	for _, r := range g.Rows {
		rows = append(rows, fmt.Sprintf("%d %s %v %v %s %s %v %s:%d %s", r.D, r.Name, r.Cols, r.Kids, r.Tag, r.St, r.Sym, r.At.Path, r.At.Line, r.At.Href))
	}
	// Handle is named where the entry is: it shows where it is declared
	if want := []string{"1 Handle [end] true signature chg true api.go:10 #api.go:10", "2 Save [blank end] false new major true api.go:12 #api.go:12"}; !slices.Equal(rows, want) {
		t.Errorf("rows\n%q\nwant\n%q", rows, want)
	}
	if g.Reach != 2 || g.New != 1 || g.Sev != "major" || g.Finding != "#store.go:9" || g.Step != 1 || v.Steps[0].Way != &ws[0] {
		t.Errorf("way %+v", g)
	}
	if got := g.Says(); got != "Runs 2 declarations the PR changes, 1 of them new. A major finding is on the way." {
		t.Errorf("says %q", got)
	}
	if g.Name() != "GET /x" || ws[1].Name() != "main" { // CALL is no method to read
		t.Errorf("names %q %q", g.Name(), ws[1].Name())
	}
	v.Ways = ws
	if got := v.WaysSay(); got != "2 ways in · the GET /x path is in step 01" {
		t.Errorf("ways say %q", got)
	}
}

func TestOrder(t *testing.T) {
	// 0 and 1 use each other, 2 uses 1: 2 first, as nothing reaches it; then the cycle
	if got, _ := order([][]int{{0, 1, 0}, {1, 0, 0}, {0, 1, 0}}); !slices.Equal(got, []int{2, 0, 1}) {
		t.Errorf("order %v", got)
	}
	// a chain listed backwards comes out in its order
	if got, _ := order([][]int{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}); !slices.Equal(got, []int{2, 1, 0}) {
		t.Errorf("chain %v", got)
	}
}

func TestSignature(t *testing.T) {
	for _, c := range []struct {
		src, want string
		n         int
	}{
		{"export async function resolveItemByName(input: {\n\tname: string;\n\texpansionId?: number;\n}) {\n\tconst n = 1;", "export async function resolveItemByName(input: { name: string; expansionId?: number }) {", 4},
		{"export async function getItemsByInstance({\n\tinstanceId,\n\tminQuality,\n}: {\n\tinstanceId?: number;\n}): Promise<\n\tArray<Item & { id: number | null }>\n> {\n\treturn [];",
			"export async function getItemsByInstance({ instanceId, minQuality }: { instanceId?: number }): Promise<Array<Item & { id: number | null }>> {", 8},
		{"export const protectedProcedure = t.procedure\n\t.use(a)\n\t.use(b);", "export const protectedProcedure", 1},
		{"export const getItemById = db\n\t.select()", "export const getItemById", 1},
		{"const env: Env = createEnv({", "const env: Env", 1},
		{"export const f = async (a: number, b = c >= d) => a + b;", "export const f = async (a: number, b = c >= d) =>", 1},
		{"const g = function (a) { return a }", "const g = function (a) {", 1},
		{"var h = func(a int) error {", "var h = func(a int) error {", 1},
		{"func Old() { println(\"old\") }", "func Old() {", 1},
		{"func set() map[string]struct{} {", "func set() map[string]struct{} {", 1},
		{"function shape(): { a: number } {", "function shape(): { a: number } {", 1},
		{"fn get<'a>(x: &'a str) -> Option<&'a str> {", "fn get<'a>(x: &'a str) -> Option<&'a str> {", 1},
		{"def load(path,\n         mode=\"r\"):  # (todo\n    pass", "def load(path, mode=\"r\"):", 2},
		{"function f(a, // the (first) one\n\tb) {", "function f(a, b) {", 2},
		{"export type Row = Pick<\n\tItem,\n\t\"id\" | \"name\"\n>;", "export type Row = Pick<Item, \"id\" | \"name\">;", 4},
		{"export interface Props {\n\ta: number;", "export interface Props {", 1},
	} {
		if got, n := signature(strings.Split(c.src, "\n")); got != c.want || n != c.n {
			t.Errorf("%q:\n got %q, %d\nwant %q, %d", c.src, got, n, c.want, c.n)
		}
	}
	if !same("f(a: { b: string })", "f(a: {b: string})") || same("f(a: number)", "f(a: string)") {
		t.Error("same")
	}
}

// what a signature is over several lines, of a declaration whose declaring line is context or a value
func TestSignatures(t *testing.T) {
	d := Data{Title: "t", Diff: `diff --git a/a.ts b/a.ts
--- a/a.ts
+++ b/a.ts
@@ -1,20 +1,17 @@
-export async function resolveItemByName(input: {
-	name: string;
-	expansionId?: number;
-}) {
+export async function resolveItemByName(input: { name: string }) {
 	const name = input.name.trim();
 	return name;
 }

 export function search(opts: {
-	offset: number;
+	cursor?: string;
 	limit: number;
 }) {
 	return opts;
 }

-export const protectedProcedure = t.procedure.use(auth);
+export const protectedProcedure = t.procedure
+	.use(mapper)
+	.use(auth);

 export function reflowed(
-	a: number, b: number) {
+	a: number,
+	b: number,
+) {
 	return a + b;
 }
`}
	var got []string
	for _, g := range prepare(d).Inventory {
		for _, it := range g.Items {
			got = append(got, fmt.Sprintf("%s %s: %s -> %s +%d -%d", g.Key, it.Name, it.OldHead, it.Head, it.NPlus, it.NMinus))
		}
	}
	want := []string{
		"signature resolveItemByName: export async function resolveItemByName(input: { name: string; expansionId?: number }) { -> export async function resolveItemByName(input: { name: string }) { +0 -0",
		"signature search: export function search(opts: { offset: number; limit: number }) { -> export function search(opts: { cursor?: string; limit: number }) { +0 -0",
		"body protectedProcedure:  ->  +3 -1",
		"body reflowed:  ->  +3 -1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("inventory\n%s", strings.Join(got, "\n"))
	}
	f := parse("a.ts", d.Diff, "")
	sig := map[string]bool{}
	cs, _ := changes(f, map[int]run{})
	for _, c := range cs {
		sig[c.Name] = c.sig
	}
	if want := map[string]bool{"resolveItemByName": true, "search": true, "protectedProcedure": false, "reflowed": false}; !maps.Equal(sig, want) {
		t.Errorf("changes' signatures %v", sig)
	}
}

func TestReach(t *testing.T) {
	ref := Ref{Path: "cmd/main.go", Line: 3, Text: "x()"}
	api := &file{Path: "api.go", Changes: []change{
		{Name: "Body", ID: "b"}, {Name: "Sig", ID: "s", added: true, removed: true, sig: true}, {Name: "Fresh", State: "new"}, {Name: "Guess"},
	}}
	syms := map[string][]Symbol{
		"Body":  {{Path: "api.go", Exact: true, Outside: []Ref{ref}, NOutside: 9}},
		"Sig":   {{Path: "api.go", Exact: true, Outside: []Ref{ref}, NOutside: 1}, {Path: "other.go", Exact: true, NOutside: 4}},
		"Fresh": {{Path: "api.go", Exact: true, NOutside: 2}},
		"Guess": {{Path: "api.go", NOutside: 2}},
	}
	got := reach([]step{{Files: []*file{api}}}, syms)
	want := []affected{
		{Name: "Sig", Path: "api.go", ID: "s", Why: "signature changed", Refs: []Ref{ref}, N: 1},
		{Name: "Body", Path: "api.go", ID: "b", Refs: []Ref{ref}, N: 9},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reach %+v", got)
	}
}

func TestCodegraph(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	src := "package a\n\nconst Max = 2\n\nfunc Save() {}\n\nfunc Handle() {\n\tSave()\n\t_ = Max\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\n\nfunc Use() { Save() }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// codegraph's tables, the columns the query reads
	db := `const { DatabaseSync } = require("node:sqlite"); const db = new DatabaseSync(process.argv[1]);
db.exec("create table nodes (id text, kind text, name text, file_path text, start_line int, end_line int);" +
  "create table edges (source text, target text, kind text, line int);" +
  "insert into nodes values ('f', 'file', 'a.go', 'a.go', 1, 10), ('m', 'constant', 'Max', 'a.go', 3, 3)," +
  " ('s', 'function', 'Save', 'a.go', 5, 5), ('h', 'function', 'Handle', 'a.go', 7, 10), ('u', 'function', 'Use', 'b.go', 3, 3);" +
  "insert into edges values ('h', 's', 'calls', 8), ('h', 'm', 'references', null), ('f', 'h', 'contains', null), ('u', 's', 'calls', 3);")`
	if err := os.MkdirAll(filepath.Join(dir, ".codegraph"), 0o750); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", "-e", db, filepath.Join(dir, ".codegraph", "codegraph.db")).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	use := Ref{Path: "b.go", Line: 3, In: "Use", Text: "func Use() { Save() }"}
	s := Symbols(dir, "+++ b/a.go\n+\tSave()\n+\t_ = Max\n+func Handle() {\n")
	want := Symbol{
		Kind: "function", Pkg: "a", Unit: "package", Sig: "func Save()", Path: "a.go", Line: 5, From: 5, To: 5, Uses: 2, Files: 2, Exact: true,
		Refs: []Ref{{Path: "a.go", Line: 8, In: "Handle", Text: "Save()"}, use}, Here: []string{"a.go:8"},
		Outside: []Ref{use}, NOutside: 1, // b.go isn't in the diff
	}
	if !reflect.DeepEqual(s["Save"], []Symbol{want}) {
		t.Errorf("Save %+v", s["Save"])
	}
	if m := s["Max"][0]; m.Uses != 1 || m.Refs[0].Line != 9 { // no line in the index: the first in Handle that names it
		t.Errorf("Max %+v", m)
	}
	if c := s["Handle"][0].Calls; !slices.Equal(c, []string{"a.go\x00Save", "a.go\x00Max"}) {
		t.Errorf("Handle calls %q", c)
	}
}

func TestInventory(t *testing.T) {
	d := Data{
		Title: "t", Diff: `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,20 +1,10 @@
 package a
 
-func matrix() []string {
-	out := []string{"alpha"}
-	out = append(out, "beta")
-	return out
-}
-
-func shift(n int) int {
-	total := n * 2
-	total += 10
-	return total
-}
-
-func Sig(a int) int {
+func Sig(a, b int) int {
 	return a
 }
 
 func Body() {
-	println("one")
+	println("two")
 }
-
-func Old() { println("old") }
diff --git a/b.go b/b.go
new file mode 100644
--- /dev/null
+++ b/b.go
@@ -0,0 +1,14 @@
+package a
+
+func Matrix() []string {
+	out := []string{"alpha"}
+	out = append(out, "beta")
+	return out
+}
+
+func Shift(n int) int {
+	total := n * 2
+	total += 20
+	return total
+}
+
+func Fresh() {}
`,
		Commits: []review.Commit{{SHA: "f00dfacecafe", Subject: "feat: add Fresh", Lines: []string{"+func Fresh() {}"}}, {Subject: "wip", Lines: []string{"+func Sig(a, b int) int {"}}},
		Whys:    map[string]string{"a.go\x00Sig": "takes b too", "a.go\x00Body": "unasked"},
	}
	var got []string
	for _, g := range prepare(d).Inventory {
		for _, it := range g.Items {
			got = append(got, fmt.Sprintf("%s %s was=%s from=%s:%d why=%s commit=%v +%v -%v", g.Key, it.Name, it.Was, it.From, it.OldLine, it.Why, it.Commit, it.Plus, it.Minus))
		}
	}
	want := []string{
		"new Fresh was= from=:0 why=feat: add Fresh commit=true +[] -[]",
		"signature Sig was= from=:0 why=takes b too commit=false +[] -[]",
		"body Body was= from=:0 why=unasked commit=false +[] -[]",
		"reworked Shift was=shift from=a.go:9 why= commit=false +[total += 20] -[total += 10]",
		"moved Matrix was=matrix from=a.go:3 why= commit=false +[] -[]",
		"removed Old was= from=:0 why= commit=false +[] -[]",
	}
	if !slices.Equal(got, want) {
		t.Errorf("inventory\n%s", strings.Join(got, "\n"))
	}
	v := prepare(d)
	got = nil
	for _, p := range v.API() {
		for _, r := range p.Items {
			got = append(got, fmt.Sprintf("%s %s %s · %s:%d %s %q", p.Pkg, r.Mark, r.Sig, r.File, r.Line, r.SHA, r.Info))
		}
	}
	want = []string{
		`. + func Fresh() · b.go:15 f00dfacecafe []`,
		`. ~ func Sig(a, b int) int · a.go:3  ["signature"]`,
		`. ~ Body · a.go:8  ["behaviour"]`,
		`. → func Shift(n int) int · b.go:9  ["from a.go:9 as shift, changed"]`,
		`. → func Matrix() []string · b.go:3  ["from a.go:3 as matrix, unchanged"]`,
		`. − func Old() · a.go:23  []`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("API\n%s", strings.Join(got, "\n"))
	}
	b, _, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		`<p class="apkg">.</p>`,
		`aria-label="new">&#43;</span><code class="asig" data-name="Fresh">func Fresh()</code>`,
		`<span>feat: add Fresh</span><a class="chip font-mono"`,
		`>commit f00dfac</a>`,
		`<a class="aloc" href="#b.go~`,
		`>b.go:15</a>`,
		`<span>takes b too</span><span class="chip" data-dash title="A small model's reading of its code">AI · read from the code</span>`,
		`<span>from a.go:3 as matrix, unchanged</span>`,
		`<summary class="as as-del">`,
	} {
		if !strings.Contains(string(b), w) {
			t.Errorf("page without %s", w)
		}
	}
	if keys, texts := Asks(d); !slices.Equal(keys, []string{"a.go\x00Sig"}) || !strings.Contains(texts[0], "func Sig(a, b int) int {") {
		t.Errorf("asks %q %q", keys, texts)
	}
}

// What a merge alone doesn't finish: env vars by their reads, manifests by name and version, files by path; a name
// both read and no longer read only moved.
func TestRisks(t *testing.T) {
	file := func(p, hunk string) string {
		return "diff --git a/" + p + " b/" + p + "\n--- a/" + p + "\n+++ b/" + p + "\n" + hunk
	}
	d := file("cmd/main.go", "@@ -1,3 +1,4 @@\n-\tkey := os.Getenv(\"OLD_KEY\")\n-\turl := os.Getenv(\"DB_URL\")\n+\tkey := os.Getenv(\"API_TOKEN\")\n+\turl, _ := os.LookupEnv(\"DB_URL\")\n+\tport := process.env.PORT\n") +
		file("go.mod", "@@ -3,3 +3,4 @@\n-\tgithub.com/a/b v1.4.0\n+\tgithub.com/a/b v2.0.1\n+\tgolang.org/x/net v0.30.0\n+\tgithub.com/c/d v1.0.0 // indirect\n") +
		file("db/migrations/0042_users.sql", "@@ -0,0 +1 @@\n+ALTER TABLE users ADD COLUMN plan text;\n") +
		file("compose.yaml", "@@ -1 +1,2 @@\n services:\n+    environment: [\"SMTP_HOST=${SMTP_HOST:?}\", \"LOG_LEVEL=${LOG_LEVEL:-info}\"]\n") +
		file(".golangci.yml", "@@ -1 +1 @@\n-x: 1\n+x: 2\n") +
		file("internal/settings.go", "@@ -1 +1 @@\n-const x = 1\n+const x = 2\n") +
		file("web/vite.config.ts", "@@ -1 +1 @@\n-a\n+b\n") +
		file("cmd/main_test.go", "@@ -1 +1 @@\n+\tt.Setenv(\"TEST_ONLY\", os.Getenv(\"TEST_ONLY\"))\n")
	got := map[string][]string{}
	for _, r := range prepare(Data{Diff: d}).Risks {
		for _, it := range r.Items {
			got[r.Title] = append(got[r.Title], fmt.Sprintf("%s %s %v", it.What, it.Note, it.Warn))
		}
	}
	want := map[string][]string{
		"Environment":          {"API_TOKEN newly read true", "LOG_LEVEL newly read, has a default false", "OLD_KEY no longer read false", "PORT newly read false", "SMTP_HOST newly read false"},
		"Dependencies":         {"github.com/a/b v1.4.0 → v2.0.1 true", "golang.org/x/net new v0.30.0 false"},
		"Migrations":           {"db/migrations/0042_users.sql +1 −0 false"},
		"Build, deploy and CI": {"compose.yaml +1 −0 false"},
		"Configuration":        {".golangci.yml +1 −1 false", "web/vite.config.ts +1 −1 false"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("risks:\n got %q\nwant %q", got, want)
	}
}

// A report against the version before marks the files whose change changed and lists what else did.
func TestSince(t *testing.T) {
	file := func(p, line string) string {
		return fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-x\n+%s\n", p, p, p, p, line)
	}
	d := Data{
		Repo: "a/b", SHA: "1111111aaa", Diff: file("a.go", "one") + file("b.go", "two"),
		Findings: []review.Comment{{Path: "a.go", Line: 1, Title: "nil map"}},
	}
	_, prev, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	d.SHA, d.Prev = "2222222bbb", &prev
	d.Diff = file("a.go", "one") + file("c.go", "three") // b.go dropped, c.go new, a.go the same
	d.Findings = []review.Comment{{Path: "c.go", Line: 1, Title: "leak"}}
	d.Outline.Weakened = []diff.Flag{{At: "c_test.go:4", What: "test deleted"}}
	if _, v, _ := Render(d); !slices.Contains(v.Findings, "c_test.go: test deleted") || v.Weights["c_test.go: test deleted"] != "major" {
		t.Errorf("a weakened test is no finding of the version: %+v", v)
	}
	d.Outline.Weakened = nil
	b, now, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	s := prepare(d).Since
	if len(s.Files) != 1 || s.Files[0].Path != "c.go" || !slices.Equal(s.Back, []string{"b.go"}) ||
		!slices.Equal(s.New, []string{"c.go: leak"}) || !slices.Equal(s.Fixed, []string{"a.go: nil map"}) || now.SHA != d.SHA {
		t.Fatalf("%+v", s)
	}
	page := string(b)
	for _, want := range []string{`id="since"`, `id="c.go" open data-changed`, `No longer in the PR: <s title="b.go">b.go</s>`, `1 finding fixed, 1 new: <span data-sev="" title="c.go: leak">c.go: leak</span>`, "1 fixed</", "a.go: nil map</s> · fixed in 2222222", `data-filter="changed"`, "Changed since 1111111", "changed since 1111111</span>"} {
		if !strings.Contains(page, want) {
			t.Error("missing", want)
		}
	}
	if regexp.MustCompile(`id="a.go"[^>]*data-changed`).MatchString(page) {
		t.Error("a.go marked though its change is the same")
	}
}

func TestJudge(t *testing.T) {
	const d = "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n x\n+y\n"
	v := prepare(Data{SHA: "0123456789", Diff: d, Decisions: []review.Decision{{Text: "Retry?"}}, Flow: []review.Step{{Title: "s", Files: []string{"a.go"}}},
		Outline: diff.Outline{Weakened: []diff.Flag{{At: "a_test.go:4", What: "skip/only: `t.Skip()`"}}},
		Findings: []review.Comment{
			{Path: "a.go", Line: 9, Severity: "minor", Title: "naming"},
			{Path: "a.go", Line: 5, Severity: "nit", Title: "name it n"},
			{Path: "a.go", Line: 2, Severity: "critical", Title: "nil map", Body: "m is nil. It panics.\n\n_Verified:_ seen", Evidence: "seen"},
			{Path: "a.go", Line: 2, Severity: "major", Title: "leak", Body: "The file stays open.\n\nClose it."},
		}})
	var got []string
	for _, g := range v.Groups {
		for _, f := range g.Items {
			got = append(got, g.Title+": "+f.Title+" @ "+f.Where+" · "+f.Proof)
		}
	}
	want := []string{
		"Blocks the merge: nil map @ a.go:2 · step 1 · AI cross-check",
		"Fix before merging: leak @ a.go:2 · step 1 · not verified",
		"Fix before merging: skip/only: `t.Skip()` @ a_test.go:4 · not in the diff · In the diff",
		"Minor: naming @ a.go:9 · step 1 · not verified",
		"Nits: name it n @ a.go:5 · step 1 · not verified",
	}
	if !slices.Equal(got, want) {
		t.Errorf("groups:\n%s", strings.Join(got, "\n"))
	}
	if v.Verdict.OK || v.Verdict.Title != "Fix first: nil map" ||
		v.Verdict.Body != "a.go:2: m is nil. After that, 2 more findings to fix, 1 minor finding, 1 nit and 1 decision." {
		t.Errorf("verdict: %+v", v.Verdict)
	}
	if v.Draft.Event != "Request changes" || v.Draft.Say != "1 blocker, 2 major, 1 minor and 1 nit, listed in the draft" ||
		v.Draft.MD != "nil map (a.go:2). m is nil.\n\nFix before merging:\n- a.go:2  leak. The file stays open.\n- a_test.go:4  skip/only: `t.Skip()`\n\nQuestions:\n1. Retry?\n\nMinor:\n- a.go:9  naming\n\nNits:\n- a.go:5  name it n" {
		t.Errorf("draft: %+v\n%s", v.Draft, v.Draft.MD)
	}

	v = prepare(Data{SHA: "0123456789", Diff: d, Decisions: []review.Decision{{Text: "Retry?"}}})
	if !v.Verdict.OK || v.Verdict.Short != "No findings" || v.Verdict.Body != "No finding on 0123456. 1 decision still needs you before you approve." ||
		v.Draft.Event != "Comment" || v.Draft.MD != "No findings on 0123456.\n\nQuestions:\n1. Retry?" || v.Draft.Say != "none" {
		t.Errorf("none: %+v %+v", v.Verdict, v.Draft)
	}
	v = prepare(Data{SHA: "0123456789", Diff: d, Findings: []review.Comment{{Path: "a.go", Line: 2, Severity: "minor", Title: "naming"}}})
	if !v.Verdict.OK || v.Verdict.Short != "No blockers" || v.Draft.Event != "Comment" {
		t.Errorf("minor only: %+v %+v", v.Verdict, v.Draft)
	}
	// in its step, the file with the blocker comes first
	two := d + strings.ReplaceAll(d, "a.go", "b.go")
	v = prepare(Data{Diff: two, Flow: []review.Step{{Title: "s", Files: []string{"a.go", "b.go"}}}, Findings: []review.Comment{{Path: "b.go", Line: 2, Severity: "critical", Title: "x"}}})
	if fs := v.Steps[0].Files; fs[0].Path != "b.go" || fs[1].Path != "a.go" {
		t.Errorf("blocker first: %s, %s", fs[0].Path, fs[1].Path)
	}
}

// A step names the tests that call what it changes; it says none does only when codegraph knows every use.
func TestTested(t *testing.T) {
	// a.go: Save on lines 3-5, Load on 7-9; the diff adds line 4 and, after line 8, removes one
	diff := []line{{Class: "hunk", Text: "@@ -1,9 +1,9 @@"}, {Class: "ctx", No: "3"}, {Class: "add", No: "4", Text: "\tx++"},
		{Class: "ctx", No: "8"}, {Class: "del", Text: "\ty--"}, {Class: "ctx", No: "9"}}
	code := func(ls ...line) *file {
		return &file{Path: "a.go", Kind: "code", Lines: append(slices.Clone(diff), ls...), Changes: []change{{Name: "Save"}}}
	}
	save := func(s Symbol) map[string][]Symbol {
		s.Path, s.Exact, s.From, s.To = "a.go", true, 3, 5
		return map[string][]Symbol{"Save": {s}, "Load": {{Path: "a.go", Exact: true, From: 7, To: 9, Untested: true}}}
	}
	many := make([]string, 10)
	for i := range many {
		many[i] = fmt.Sprintf("a_test.go Test%d", i)
	}
	for _, c := range []struct {
		why      string
		f        *file
		syms     map[string][]Symbol
		tests    []string
		more     int
		untested bool
	}{
		{"a test reaches it", code(), save(Symbol{Tests: []string{"a_test.go TestSave"}}), []string{"a_test.go TestSave"}, 0, false},
		{"none is found", code(), save(Symbol{Untested: true}), nil, 0, true},
		{"a guess finds one", code(), save(Symbol{}), nil, 0, false},
		{"the line removed in Load: Load's tests", code(), map[string][]Symbol{"Save": {{Path: "a.go", Exact: true, From: 3, To: 5, Untested: true}},
			"Load": {{Path: "a.go", Exact: true, From: 7, To: 9, Tests: []string{"a_test.go TestLoad"}}}}, []string{"a_test.go TestLoad"}, 0, false},
		{"a line in no declaration", code(line{Class: "add", No: "11", Text: "register(Save)"}), save(Symbol{Untested: true}), nil, 0, false},
		{"a comment in none", code(line{Class: "add", No: "11", Text: "// Save saves."}), save(Symbol{Untested: true}), nil, 0, true},
		{"one the index doesn't know", code(), map[string][]Symbol{"Save": {{Path: "a.go", Untested: true}}}, nil, 0, false},
		{"a removed one", &file{Path: "a.go", Kind: "code", Lines: diff, Changes: []change{{Name: "Save"}, {Name: "Old", State: "gone"}}},
			save(Symbol{Untested: true}), nil, 0, false},
		{"cut short", &file{Path: "a.go", Kind: "code", Lines: diff, Cut: 9}, save(Symbol{Untested: true}), nil, 0, false},
		{"not code", &file{Path: "a.go", Kind: "config", Lines: diff}, save(Symbol{Untested: true}), nil, 0, false},
		{"nothing changed", &file{Path: "a.go", Kind: "code"}, save(Symbol{Untested: true}), nil, 0, false},
		{"counted past maxTests", code(), save(Symbol{Tests: many}), many[:maxTests], 2, false},
	} {
		tests, more, _, untested, _ := tested(step{Files: []*file{c.f}}, c.syms)
		if !slices.Equal(tests, c.tests) || more != c.more || untested != c.untested {
			t.Errorf("%s: %v +%d %v", c.why, tests, more, untested)
		}
	}
}

// TestWalk walks a checkout by codegraph's tables and git grep: a test through a caller, a call codegraph can't see
// in a Rust macro, a use in a template, and a function only a doc and a comment name.
func TestWalk(t *testing.T) {
	for _, tool := range []string{"node", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	dir := t.TempDir()
	for p, src := range map[string]string{
		"a.go":      "package a\n\nfunc Save() { helper() }\n\nfunc helper() {}\n\nfunc Lonely() {}\n\nfunc Hidden() {}\n",
		"a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestSave(t *testing.T) { Save() }\n",
		"b.go":      "package a\n\n// Lonely is alone.\n",
		"lib.rs":    "pub fn wrap() -> i32 { 1 }\n\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn wraps() { assert_eq!(super::wrap(), 1); }\n}\n",
		"page.html": "{{Hidden}}\n",
		"README.md": "Call Lonely.\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db := `const { DatabaseSync } = require("node:sqlite"); const db = new DatabaseSync(process.argv[1]);
db.exec("create table nodes (id text, kind text, name text, file_path text, start_line int, end_line int);" +
  "create table edges (source text, target text, kind text, line int); create table files (path text);" +
  "insert into files values ('a.go'), ('a_test.go'), ('b.go'), ('lib.rs');" +
  "insert into nodes values ('s', 'function', 'Save', 'a.go', 3, 3), ('h', 'function', 'helper', 'a.go', 5, 5)," +
  " ('l', 'function', 'Lonely', 'a.go', 7, 7), ('x', 'function', 'Hidden', 'a.go', 9, 9), ('i', 'import', 'testing', 'a_test.go', 3, 3)," +
  " ('t', 'function', 'TestSave', 'a_test.go', 5, 5), ('w', 'function', 'wrap', 'lib.rs', 1, 1), ('ws', 'function', 'wraps', 'lib.rs', 6, 6);" +
  "insert into edges values ('s', 'h', 'calls', 3), ('t', 's', 'calls', 5);")`
	if err := os.MkdirAll(filepath.Join(dir, ".codegraph"), 0o750); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", "-e", db, filepath.Join(dir, ".codegraph", "codegraph.db")).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	s := Symbols(dir, "+++ b/a.go\n+func Save() { helper() }\n+func helper() {}\n+func Lonely() {}\n+func Hidden() {}\n+++ b/lib.rs\n+pub fn wrap() -> i32 { 1 }\n")
	for name, want := range map[string]struct {
		tests    []string
		untested bool
	}{
		"Save":   {[]string{"a_test.go TestSave"}, false},
		"helper": {[]string{"a_test.go TestSave"}, false}, // through Save
		"Lonely": {nil, true},                             // a doc and a comment name it
		"Hidden": {nil, false},                            // a template may call it
		"wrap":   {nil, false},                            // a test below #[cfg(test)], in a macro the index can't see
	} {
		if len(s[name]) != 1 {
			t.Fatalf("%s: %+v", name, s[name])
		}
		if got := s[name][0]; !slices.Equal(got.Tests, want.tests) || got.Untested != want.untested {
			t.Errorf("%s: tests %v, untested %v", name, got.Tests, got.Untested)
		}
	}
}

// an imports hunk says what it brings in and drops; a line that only moved is neither
func TestImported(t *testing.T) {
	ls := []line{{Class: "ctx", Text: "import ("}, {Class: "del", Text: `	"fmt"`}, {Class: "del", Text: `	"os"`},
		{Class: "add", Text: `	x "a/b"`}, {Class: "add", Text: `	"os"`}, {Class: "ctx", Text: ")"}}
	if got := imported(ls); got != "a/b in · fmt out" {
		t.Errorf("imported %q", got)
	}
	if got := imported([]line{{Class: "add", Text: "use std::io;"}}); got != "use std::io in" {
		t.Errorf("imported %q", got)
	}
}

// The places a model's text names become links into the diff, the forge's for a file outside it; a test the
// page names says how it ran, at the head and at the last review.
func TestPlaces(t *testing.T) {
	d := Data{
		Repo: "a/b", SHA: "2222222bbb", Forge: Forge{Blob: "https://f/blob/"},
		Diff:      "diff --git a/pkg/a.go b/pkg/a.go\n--- a/pkg/a.go\n+++ b/pkg/a.go\n@@ -1 +1 @@\n-x\n+y\n",
		Summary:   "Total takes tax after it (a.go:42), and other.go:7 caps it.",
		Decisions: []review.Decision{{Text: "Reject?", At: "pkg/a.go:3"}},
		Story:     review.Story{Before: "old", After: "new", Proof: "TestTotal/with discount"},
		Commits:   []review.Commit{{SHA: "2222222bbb", Author: "Kim", Subject: "s"}},
		Tests:     checks.Tests{Ran: map[string]string{"TestTotal/with_discount": "pass"}, Broken: []string{"pkg/b"}},
		Prev:      &Version{SHA: "1111111aaa", Tests: map[string]string{"TestTotal/with_discount": "none"}},
	}
	v := prepare(d)
	rs := v.Refs(d.Summary)
	if len(rs) != 3 || rs[0].Text != "Total takes tax after it " || rs[0].Href != "#pkg/a.go:42" ||
		rs[1].Text != ", and " || rs[1].Href != "https://f/blob/other.go#L7" || !rs[1].Out || rs[2].Text != " caps it." {
		t.Fatalf("%+v", rs)
	}
	if r := v.Run("TestTotal/with discount"); r.Mark != "pass" || r.Text != "not run on 1111111, passed on 2222222" {
		t.Errorf("%+v", r)
	}
	if r := v.Run("TestOther"); r.Mark != "none" || r.Text != "not run on 2222222" {
		t.Errorf("%+v", r)
	}
	if v.now.Tests["TestTotal/with_discount"] != "pass" || !v.now.Broken {
		t.Errorf("%+v", v.now)
	}
	b, _, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`<p class="meta"><span>Kim</span>`, `href="#pkg/a.go:42"`, `title="other.go:7, not in the diff"`,
		`TestTotal/with discount · not run on 1111111, passed on 2222222`, `href="#pkg/a.go:3"`, ">does not build<", "1 by Kim"} {
		if !strings.Contains(string(b), w) {
			t.Errorf("%q missing", w)
		}
	}
	d.Tests = checks.Tests{}
	d.Prev = nil
	if r := prepare(d).Run("TestTotal"); r != (testRun{}) {
		t.Errorf("no test ran, yet %+v", r)
	}
}

// A test deleted with the declaration it is named after leaves the findings for the line under them.
func TestRetire(t *testing.T) {
	files := map[string]*file{
		"a.go": {Kind: "code", Changes: []change{{Name: "totalNet", State: "gone"}, {Name: "Total", State: "gone"}, {Name: "Sum", State: "gone"}, {Name: "Net", State: "changed"}}},
		"b.go": {Kind: "code", Changes: []change{{Name: "Total", State: "new"}}},
		"x.go": {Kind: "test", Changes: []change{{Name: "helper", State: "gone"}}},
	}
	got := retire([]string{"a_test.go: TestTotalNet", "a_test.go: TestTotalNetRounding", "a_test.go: TestTotal", "a_test.go: TestTotalnetwork", "a_test.go: TestSumNet", "x_test.go: TestHelper"}, files)
	if !reflect.DeepEqual(got, []retired{{Names: []string{"TestTotalNet", "TestTotalNetRounding"}, With: "totalNet"}}) {
		t.Fatalf("%+v", got)
	}
}

// A step names what to check in it and what no test reaches; the tests list their cases with how they ran.
func TestStepTests(t *testing.T) {
	for _, c := range []struct {
		s    step
		want string
	}{
		{step{Untested: true}, "none found that reaches what this step changes"},
		{step{Untested: true, bare: []string{"scanLine"}}, "none: scanLine is not called by any test"},
		{step{Untested: true, bare: []string{"a", "b", "c", "d", "e"}}, "none: a, b, c and 2 more are not called by any test"},
		{step{Tests: []string{"x_test.go TestX"}, bare: []string{"scanLine"}}, "none reaches scanLine"},
		{step{Tests: []string{"x_test.go TestX"}, bare: []string{"scanLine"}, Partial: true}, ""},
	} {
		if got := c.s.Gap(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.s, got, c.want)
		}
	}
	file := func(p, body string) string {
		return fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1,0 +1,2 @@\n%s", p, p, p, p, body)
	}
	d := Data{
		Repo: "a/b", SHA: "2222222bbb",
		Diff:  file("a.go", "+func A() {}\n+\n") + file("a_test.go", "+func TestA(t *testing.T) {}\n+func TestB(t *testing.T) { t.Skip(\"later\") }\n"),
		Flow:  []review.Step{{Title: "A", Check: "A returns", Files: []string{"a.go"}, Placed: map[string]string{"a.go": "here because B calls it"}}},
		Tests: checks.Tests{Ran: map[string]string{"TestA": "pass", "TestB": "skip"}, Broken: []string{"c"}},
	}
	d.Outline = diff.Analyze(d.Diff)
	v := prepare(d)
	if v.Skipped != 1 || v.files["a_test.go"].Skipped != 1 {
		t.Fatalf("skipped %d", v.Skipped)
	}
	if got := v.TestsSay(); got != "1 of 2 new cases passed on 2222222. c does not build on 2222222, so nothing ran there. 1 test is now skipped or narrowed to one." {
		t.Error(got)
	}
	b, _, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`<span>Check</span> · A returns`, `here because B calls it</span>`, `>1 skipped</span>`, `title="a_test.go">TestA</span><span class="case-s">passed on 2222222</span>`, `data-mark="pass"`} {
		if !strings.Contains(string(b), w) {
			t.Errorf("%q missing", w)
		}
	}
}

// The text of a changed file goes with it for the lines between hunks, once and quoted; a new file has none to show.
func TestSources(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\n// </script> stays text\nfunc A() {}\n"), 0o644)
	d := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -4 +4 @@\n-func A() { }\n+func A() {}\n" +
		"diff --git a/b.go b/b.go\nnew file mode 100644\n--- /dev/null\n+++ b/b.go\n@@ -0,0 +1 @@\n+package a\n"
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\n"), 0o644)
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "secret"), []byte("no"), 0o644)
	os.Symlink(filepath.Join(out, "secret"), filepath.Join(dir, "c.go"))
	os.Symlink(out, filepath.Join(dir, "d"))
	d += "diff --git a/c.go b/c.go\nnew file mode 120000\n--- /dev/null\n+++ b/c.go\n@@ -0,0 +1 @@\n+x\n" +
		"diff --git a/d/secret b/d/secret\n--- a/d/secret\n+++ b/d/secret\n@@ -1 +1 @@\n-n\n+no\n"
	src := Sources(dir, d)
	if len(src) != 2 || src["c.go"] != "" || src["d/secret"] != "" {
		t.Fatalf("sources %v", src)
	}
	b, _, err := Render(Data{Repo: "o/r", Number: 1, SHA: "abc", Diff: d, Sources: src})
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	if n := strings.Count(page, `class="file-src"`); n != 1 {
		t.Errorf("%d sources on the page, want a.go's only", n)
	}
	if !strings.Contains(page, `data-lines="4">"package a\n\n// \u003c/script\u003e stays text\nfunc A() {}\n"</script>`) {
		i := strings.Index(page, `class="file-src"`)
		t.Errorf("a.go's source: %q", page[i:min(len(page), i+120)])
	}
}

func TestCallAt(t *testing.T) {
	for _, c := range []struct{ line, name, want string }{
		{"\tif err := inv.Validate(); err != nil {", "Validate", "inv.Validate()"},
		{"\tmax := settings.MaxDiscount(ctx, id)", "MaxDiscount", "settings.MaxDiscount(…)"},
		{"\t\tinv.Lines[i].Discount = max", "Discount", "inv.Lines[i].Discount"},
		{"\tr.Get(\"/x\", GetInvoice)", "GetInvoice", "GetInvoice"},
		{"\tf().Save()", "Save", "Save()"},
		{"\tSaver(x)", "Save", ""},
		{"\taVeryLongReceiverName.anotherLongFieldName.yetAnotherOne.Save()", "Save", ""},
	} {
		if got := callAt(c.line, c.name); got != c.want {
			t.Errorf("callAt(%q, %q) = %q, want %q", c.line, c.name, got, c.want)
		}
	}
}

// A part whose review failed leaves its files unread: the verdict says so instead of clean, and an empty list
// credits the verifier only when it dropped something.
func TestFailedNotClean(t *testing.T) {
	d := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-package a\n+package b\n"
	b, _, err := Render(Data{Repo: "o/r", Number: 1, SHA: "abc", Diff: d, Failed: []string{"a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	for _, w := range []string{"Not fully reviewed", "1 file not reviewed", "The review of a.go failed", "The review found nothing"} {
		if !strings.Contains(page, w) {
			t.Errorf("%q missing", w)
		}
	}
	if strings.Contains(page, "Nothing blocks the merge") || strings.Contains(page, "A second model checked") {
		t.Error("a clean verdict or a verifier that did not run")
	}
	// a file the review left out, all of it or a piece, isn't clean either
	b, _, err = Render(Data{Repo: "o/r", Number: 1, SHA: "abc", Diff: d, Omitted: []string{"a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if page = string(b); !strings.Contains(page, "1 file not fully reviewed") || !strings.Contains(page, "in the files reviewed") || strings.Contains(page, "Nothing blocks the merge") {
		t.Error("omitted: a clean verdict")
	}
}

// A nit or a minor finding on a declaration marks no way: only what blocks the merge is "on the way".
func TestWaysNit(t *testing.T) {
	for _, sev := range []string{"nit", "minor"} {
		store := &file{Path: "store.go", Changes: []change{{Name: "Save", State: "new", Sev: sev}}}
		v := view{Steps: []step{{Files: []*file{store}}}, files: map[string]*file{"store.go": store}}
		syms := map[string][]Symbol{"Save": {{Path: "store.go", Exact: true, Ways: []Way{
			{Method: "GET", Route: "/x", Path: "server.go", Line: 3, Chain: []Hop{{Name: "Save", Path: "store.go", Line: 5, In: "server.go", At: 3}}},
		}}}}
		if ws := ways(&v, syms); len(ws) != 1 || ws[0].Sev != "" || strings.Contains(ws[0].Says(), "on the way") {
			t.Errorf("%s: %+v", sev, ws)
		}
	}
}
