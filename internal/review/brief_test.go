package review

import (
	"context"
	"diffharbor/internal/proc"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The first finder's brief is checked at the head: what fails is dropped. A large PR's brief comes from one more
// pass that sees what the parts wrote, no diff.
func TestBrief(t *testing.T) {
	const code = "package a\n\nconst api = \"https://api.example.com/v1\"\n\nfunc get() { http.Get(api) }\n"
	diffOf := func(f string) string {
		return "diff --git a/" + f + " b/" + f + "\n--- a/" + f + "\n+++ b/" + f + "\n@@ -1 +1,5 @@\n x\n+y\n+y\n+y\n+y\n"
	}
	run := func(replies map[string][]string, parts ...string) (Result, *fakeAgent) {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "repo"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"a.go", "b.go", "old.go"} {
			if err := os.WriteFile(filepath.Join(dir, "repo", f), []byte(code), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		f := &fakeAgent{replies: replies}
		r, _, err := Analyze(context.Background(), Input{Agent: f, Sandbox: &proc.Sandbox{Dir: dir}, Title: "t", Guide: true, Parts: parts})
		if err != nil {
			t.Fatal(err)
		}
		return r, f
	}

	reply := `{"summary":"s","comments":[],
"brief":[{"text":"get calls the example API.","at":"a.go:5"},{"text":"A line that is gone.","at":"a.go:99"},{"text":"A file that is gone.","at":"gone.go:1"}]}`
	r, f := run(map[string][]string{"review": {reply}}, diffOf("a.go"))
	if len(r.Brief) != 1 || r.Brief[0].At != "a.go:5" {
		t.Errorf("brief: a sentence with a bad place is dropped: %+v", r.Brief)
	}
	if !strings.Contains(f.tasks["review"], `"brief"`) || strings.Contains(f.tasks["review"], `"outside"`) {
		t.Error("a single part's finder is asked for the brief, and for no outside systems")
	}

	// two parts: the brief pass reads summaries and files
	p1 := `{"summary":"a.go gets the example API (a.go:5).","walkthrough":{"a.go":"adds the call"},"comments":[]}`
	p2 := `{"summary":"b.go reads it.","walkthrough":{"b.go":"reads"},"comments":[]}`
	brief := `{"brief":[{"text":"a.go calls the example API.","at":"a.go:5"},{"text":"Nowhere.","at":"a.go:50"}]}`
	r, f = run(map[string][]string{"review1": {p1}, "review2": {p2}, "brief": {brief}}, diffOf("a.go"), diffOf("b.go"))
	if len(r.Brief) != 1 || r.Brief[0].At != "a.go:5" {
		t.Errorf("multi-part: %+v", r.Brief)
	}
	task := f.contTask["brief"]
	for _, want := range []string{"a.go gets the example API", "b.go reads it", "a.go: adds the call"} {
		if !strings.Contains(task, want) {
			t.Errorf("brief prompt lacks %q:\n%s", want, task)
		}
	}
	if strings.Contains(task, "```diff") || strings.Contains(task, "@@") {
		t.Errorf("brief prompt holds the diff:\n%s", task)
	}
	if strings.Contains(f.tasks["review1"], `"brief"`) {
		t.Error("a part's finder is not asked for a brief")
	}
}
