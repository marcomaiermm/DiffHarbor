package forge

import (
	"diffharbor/internal/diff"
	"diffharbor/internal/review"
	"net/http"
	"strings"
	"testing"
)

func TestPlace(t *testing.T) {
	lines := diff.NewLines("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,3 +10,4 @@\n ctx\n-old\n+\tif x {\n+\t\treturn\n ctx2\n")
	c := Finding{Comment: review.Comment{Path: "a.go", Line: 11, Severity: "major", Body: "b", Existing: "\tif x {\n\t\treturn  \n", Fix: "\tif x != nil {\n\t\treturn\n"}}
	Place(&c, lines)
	if c.Start != 11 || c.End != 12 || !strings.Contains(Format(c, "suggestion:-1+0"), "```suggestion:-1+0\n\tif x != nil {\n\t\treturn\n```") {
		t.Fatalf("placed %d-%d: %q", c.Start, c.End, Format(c, "suggestion"))
	}
	far := Finding{Comment: review.Comment{Path: "a.go", Line: 40, Existing: "ctx\nz\n", Fix: "y"}}
	if Place(&far, lines); far.Start != 0 || !strings.Contains(Format(far, "suggestion"), "**Suggested fix:**\n\n````diff\n-ctx\n-z\n+y\n````") {
		t.Fatalf("far: %+v", far)
	}
	blank := Finding{Comment: review.Comment{Path: "a.go", Line: 13, Existing: "ctx2\n\n", Fix: "y\n\n"}}
	if Place(&blank, diff.NewLines("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +13,2 @@\n ctx2\n \n")); blank.End != 14 ||
		!strings.Contains(Format(blank, "suggestion"), "```suggestion\ny\n\n```") {
		t.Fatalf("blank: %+v %q", blank, Format(blank, "suggestion"))
	}
	if got := Format(Finding{Comment: review.Comment{Existing: "a ````", Fix: "b"}}, ""); !strings.HasSuffix(got, "`````diff\n-a ````\n+b\n`````") {
		t.Fatalf("diff fence: %q", got)
	}
	// Trimmed once, placed or not, inline or outside the diff: a fix's last blank line stays.
	// A fix that is a blank line stays one, placed or not.
	cs := []review.Comment{{Path: "a.go", Line: 11, Existing: "nope\n\n", Fix: "y\n\n"}, {Path: "a.go", Line: 40, Existing: "nope\n\n", Fix: "y\n\n"}, {Path: "a.go", Line: 41, Existing: "x\n", Fix: "x"},
		{Path: "a.go", Line: 42, Existing: "x", Fix: "\n"}, {Path: "a.go", Line: 11, Existing: "\tif x {\n", Fix: "\n"}}
	inline, rest, _ := Split(review.Result{Comments: cs}, nil, lines)
	if len(inline) != 2 || len(rest) != 3 {
		t.Fatalf("split: %d inline, %d rest", len(inline), len(rest))
	}
	want := "````diff\n-nope\n-\n+y\n+\n````"
	if !strings.Contains(Format(inline[0], "suggestion"), want) || !strings.Contains(Format(rest[0], ""), want) || !strings.HasSuffix(Format(rest[1], ""), "**Suggested fix:**\n\n````\nx\n````") ||
		!strings.Contains(Format(rest[2], ""), "````diff\n-x\n+\n````") || !strings.HasSuffix(Format(inline[1], "suggestion"), "```suggestion\n\n```") {
		t.Fatalf("split: %q %q %q %q %q", Format(inline[0], "suggestion"), Format(rest[0], ""), Format(rest[1], ""), Format(rest[2], ""), Format(inline[1], "suggestion"))
	}
	// A placed finding moved into the summary (GitLab's diff not ready yet) shows what it replaces.
	if got := Summary(nil, inline[1:], nil, ""); !strings.Contains(got, "````diff\n-\tif x {\n+\n````") {
		t.Fatalf("moved: %q", got)
	}
}

func TestFormat(t *testing.T) {
	c := Finding{Comment: review.Comment{Severity: "major", Title: "t", Body: "b", Prompt: "In a.go around line 3, use ````x````"}}
	got := Format(c, "suggestion")
	if !strings.HasPrefix(got, "_🟠 Major_ · **t**\n\nb") || !strings.Contains(got, "`````\nIn a.go around line 3, use ````x````\n`````") {
		t.Fatalf("format: %q", got)
	}
	if allPrompts([]Finding{{Comment: review.Comment{Body: "x"}}}) != "" || !strings.Contains(allPrompts([]Finding{c, c}), "use ````x````\nIn a.go") {
		t.Fatalf("allPrompts: %q", allPrompts([]Finding{c, c}))
	}
	c.Body += "\n\n" + review.Verified + "the map is nil on line 3\n"
	if p := Parse(Format(c, "suggestion")); p.Severity != "major" || p.Title != "t" || !strings.HasPrefix(p.Body, "b\n") || p.Evidence != "the map is nil on line 3" {
		t.Fatalf("parse: %+v", p)
	}
	if p := Parse("by hand"); p.Severity != "" || p.Body != "by hand" || p.Evidence != "" {
		t.Fatalf("parse plain: %+v", p)
	}
}

func TestSticky(t *testing.T) {
	st := State{}.Update(review.Result{
		Summary: "s", Walkthrough: map[string]string{"a.go": "x | y", "gone.go": "z"},
		Diagram: "```mermaid\nsequenceDiagram\n```", Effort: 9,
	}, "abcdef1234", []string{"a.go", "b.go"}, true)
	if st.SHA != "abcdef1234" || len(st.Files) != 1 || st.Diagram != "sequenceDiagram" || st.Effort != 5 {
		t.Fatalf("update: %+v", st)
	}
	// Incremental: b.go gets a row, a.go keeps its row, summary stays.
	st = st.Update(review.Result{Summary: "delta", Walkthrough: map[string]string{"b.go": "new"}}, "bbbbbbb", []string{"a.go", "b.go"}, false)
	if st.Summary != "s" || st.Files["a.go"] != "x | y" || st.Files["b.go"] != "new" || st.Diagram != "sequenceDiagram" {
		t.Fatalf("incremental: %+v", st)
	}
	ask := AskLarge(300, 412, 4, true, "bot")
	body := st.render("⏳ busy", ask, "foot", nil)
	// Only a user ticking the box counts: not the bot rewriting the comment, not a box ticked already.
	ticked := strings.Replace(body, "- [ ]", "- [x]", 1)
	if !strings.Contains(body, "\n- [ ] **Review 300 of 412 files** in 4 parts") || !LargeTicked(body, ticked) ||
		LargeTicked(ticked, ticked) || LargeTicked(ticked, st.render("", "", "foot", nil)) || LargeTicked(body, body) {
		t.Fatalf("ask: %s", body)
	}
	if !strings.HasPrefix(body, stickyMark+"\n> [!NOTE]\n> ⏳ busy") || !strings.Contains(body, "| `a.go` | x \\| y |") ||
		!strings.Contains(body, "```mermaid\nsequenceDiagram\n```") || !strings.Contains(body, "🎯 5 (Very complex)") {
		t.Fatalf("render: %s", body)
	}
	if got := parseState(body); got.SHA != "bbbbbbb" || got.Files["b.go"] != "new" {
		t.Fatalf("parseState: %+v", got)
	}
	st.Secret = "S"
	if got := parseState(st.Update(review.Result{}, "c", nil, true).render("", "", "", nil)); got.Secret != "S" {
		t.Fatal("a review lost the report's secret:", got.Secret)
	}
	if Clean(body) == body || strings.Contains(Clean(body), "diffharbor:state") {
		t.Fatal("clean")
	}
	if got := Clean("[report](https://x/r/github.com/o/r/1/ABCDEFGHIJKLMNOPQRSTUVWXYZ/versions)"); strings.Contains(got, "ABCDEF") {
		t.Fatal("a report's secret reaches the prompt:", got)
	}
	if got := Clean("https://x/r/github.com/o/r/1/%41%42CDEFGHIJKLMNOPQRSTUVWXYZ/versions"); strings.Contains(got, "CDEF") {
		t.Fatal("an encoded report secret reaches the prompt:", got)
	}
	if got := Clean("https://x/r/github.com/o/r/1%2FABCDEFGHIJKLMNOPQRSTUVWXYZ/versions"); strings.Contains(got, "ABCDEF") {
		t.Fatal("a secret after an encoded slash reaches the prompt:", got)
	}
}

func TestStickyOutline(t *testing.T) {
	st := State{}.Update(review.Result{
		Summary:     "s",
		Walkthrough: map[string]string{"api/a.go": "code", "api/a_test.go": "test", "README.md": "doc", "p1/x.go": "import", "p2/x.go": "import", "p3/x.go": "import"},
		Flow:        []review.Step{{Title: "Start", Files: []string{"api/a.go"}}}, Decisions: []review.Decision{{Text: "Retry on failure?"}},
		Story: review.Story{Problem: "p", Before: "a 429 failed the review", After: "the fallback reviews"},
	}, "abcdef1234", []string{"api/a.go", "api/a_test.go", "README.md", "p1/x.go", "p2/x.go", "p3/x.go"}, true)
	st.Report = "https://review.example/r/o/r/1"
	o := &diff.Outline{
		Added:      []string{"api/a_test.go: TestA"},
		Weakened:   []diff.Flag{{At: "api/a_test.go:9", What: "skip/only: `t.Skip()`"}},
		Mechanical: []diff.Group{{What: "Same edit in 3 files", Files: []string{"p1/x.go", "p2/x.go", "p3/x.go"}}},
	}
	body := st.render("", "", "foot", o)
	top, walk, ok := strings.Cut(body, "<summary><b>Walkthrough")
	if !ok {
		t.Fatalf("no walkthrough:\n%s", body)
	}
	for _, w := range []string{"(https://review.example/r/o/r/1)", "- Retry on failure?", "**Before:** a 429 failed the review\n**After:** the fallback reviews", "**⚠️ Tests to check**\n- `api/a_test.go:9` skip/only"} {
		if !strings.Contains(top, w) {
			t.Errorf("above the walkthrough, want %q:\n%s", w, body)
		}
	}
	code, _, _ := strings.Cut(walk, "<details>") // the table before the folded sections
	if !strings.Contains(code, "`api/a.go`") || strings.Contains(code, "a_test.go") || strings.Contains(code, "p1/x.go") || strings.Contains(code, "README") {
		t.Errorf("code table: %s", code)
	}
	for _, w := range []string{"🧪 Tests: 1 file, 1 new case</summary>", "</summary>\n\n- TestA\n", "📄 Data and docs: 1 file</summary>\n\n- `README.md`", "🔁 Mechanical: 3 files to skim", "- Same edit in 3 files (3)"} {
		if !strings.Contains(walk, w) {
			t.Errorf("folded sections lack %q:\n%s", w, walk)
		}
	}
	if got := parseState(body); len(got.Flow) != 1 || got.Decisions[0].Text != "Retry on failure?" || got.Story.After != "the fallback reviews" || got.Report == "" {
		t.Errorf("state: %+v", got)
	}
}

type instance string

func (instance) Bot() string                               { return "" }
func (instance) Parse(http.Header, []byte) (*Event, error) { return nil, nil }
func (instance) Open(*Event) (PR, error)                   { return nil, nil }
func (instance) Boxes() bool                               { return false }
func (i instance) Instance() string                        { return string(i) }

// A repo's id is its forge's instance and its number, never its name; no number is no id.
func TestRepoID(t *testing.T) {
	e := &Event{Forge: "gitlab"}
	e.Repository.FullName = "g/p"
	if _, err := RepoID(instance("gitlab.com"), e); err == nil {
		t.Fatal("no repository id: no error")
	}
	e.Repository.ID = 5
	a, _ := RepoID(instance("gitlab.com"), e)
	b, _ := RepoID(instance("git.example.com"), e)
	if a != "gitlab:gitlab.com:5" || a == b {
		t.Fatal(a, b)
	}
}

func TestMention(t *testing.T) {
	m := MentionOf("diffharbor")
	for s, want := range map[string]bool{
		"@diffharbor review": true, "hey @DiffHarbor, why?": true, "@diffharbor": true,
		"@diffharbor-bot hi": false, "mail@diffharbor": false,
	} {
		if m.MatchString(s) != want {
			t.Errorf("%q", s)
		}
	}
	if got := strings.TrimSpace(m.ReplaceAllString("@diffharbor review", "")); got != "review" {
		t.Fatalf("%q", got)
	}
	m = MentionOf("project_1_bot_ab", "mr-agent")
	for s, want := range map[string]bool{"@mr-agent why?": true, "@project_1_bot_ab why?": true, "(@mr-agent) why?": true, "@mr-agent-x": false, "@mr-agent.alice what?": false, "ask @mr-agent.": true, "@mr-agent... hm": true, "@mr why": false} {
		if m.MatchString(s) != want {
			t.Errorf("%q", s)
		}
	}
}

// Folded findings go into the summary, folded and apart from those outside the diff, and don't count as actionable.
func TestSummaryFolded(t *testing.T) {
	lines := diff.NewLines("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n x\n+y\n")
	r := review.Result{Comments: []review.Comment{
		{Path: "a.go", Line: 2, Severity: "major", Title: "inline", Body: "b"},
		{Path: "a.go", Line: 2, Severity: "nit", Title: "name", Body: "b", Fold: true, Prompt: "rename"},
		{Path: "b.go", Line: 9, Severity: "minor", Title: "far", Body: "b"},
	}}
	inline, outside, folded := Split(r, nil, lines)
	s := Summary(inline, outside, folded, "f")
	if len(inline) != 1 || len(outside) != 1 || len(folded) != 1 || !strings.Contains(s, "Actionable comments posted: 2") ||
		!strings.Contains(s, "Outside diff range comments (1)") || !strings.Contains(s, "<details>\n<summary><b>🧹 Nitpicks (1)</b></summary>") ||
		!strings.Contains(s, "_🔵 Nit_ · **name**") || !strings.Contains(s, "rename") {
		t.Fatalf("%d inline, %d outside, %d folded:\n%s", len(inline), len(outside), len(folded), s)
	}
	if p := Parse(Format(Finding{Comment: r.Comments[1]}, "")); p.Severity != "nit" || p.Title != "name" {
		t.Fatalf("parse nit: %+v", p)
	}
	minor := []Finding{{Comment: review.Comment{Severity: "minor", Fold: true}}}
	if s := Summary(nil, nil, minor, "f"); !strings.Contains(s, "No actionable comments") || !strings.Contains(s, "🧹 Minor comments (1)") {
		t.Fatal(s)
	}
}
