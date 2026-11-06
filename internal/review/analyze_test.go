package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeAgent answers each pass from a queue of replies per pass name and records the passes it ran.
type fakeAgent struct {
	mu      sync.Mutex // parts run at once
	replies map[string][]string
	calls   []string
	tasks   map[string]string // the last task per pass name
}

func (f *fakeAgent) reply(name string) (string, error) {
	q := f.replies[name]
	if len(q) == 0 {
		return "", nil
	}
	f.replies[name] = q[1:]
	return q[0], nil
}

func (f *fakeAgent) Run(_ context.Context, _, name, _, task, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	if f.tasks == nil {
		f.tasks = map[string]string{}
	}
	f.tasks[name] = task
	return f.reply(name)
}

// Continue answers a second look from name+" again", with nothing more found when none is queued.
func (f *fakeAgent) Continue(_ context.Context, _, name, _, task, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(task, repeatsTask[:30]) {
		f.calls = append(f.calls, name+" repeats")
		return f.reply(name + " repeats")
	}
	if task != againTask {
		f.calls = append(f.calls, name+" repair")
		return f.reply(name)
	}
	f.calls = append(f.calls, name+" again")
	if len(f.replies[name+" again"]) == 0 {
		return `{"summary":"","comments":[]}`, nil
	}
	return f.reply(name + " again")
}

func TestAnalyze(t *testing.T) {
	const (
		finding = `{"summary":"s","comments":[{"path":"a.md","line":1,"severity":"major","title":"t","body":"b","prompt":"fix line 1"}]}`
		keep    = `{"verdicts":[{"i":0,"keep":true,"line":2,"evidence":"seen"}]}`
		drop    = `{"verdicts":[{"i":0,"keep":false,"evidence":"handled"}]}`
	)
	const md = "diff --git a/a.md b/a.md\n--- a/a.md\n+++ b/a.md\n@@ -1 +1,2 @@\n x\n+y\n"
	run := func(replies map[string][]string, s Settings, parts ...string) (Result, string, []string) {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "repo"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "repo", "a.md"), []byte("x\ny\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		f := &fakeAgent{replies: replies}
		if len(parts) == 0 {
			parts = []string{md}
		}
		r, stats, err := Analyze(context.Background(), Input{Agent: f, Dir: dir, Title: "t", Settings: s, Parts: parts})
		if err != nil {
			t.Fatal(err)
		}
		return r, stats, f.calls
	}

	r, stats, calls := run(map[string][]string{"review": {finding}, "verify": {keep}}, Settings{})
	if len(r.Comments) != 1 || r.Comments[0].Line != 2 || r.Comments[0].Prompt != "fix line 2" ||
		!strings.Contains(r.Comments[0].Body, "_Verified:_ seen") || r.Comments[0].Evidence != "seen" || !strings.HasSuffix(stats, "1/1 verified") ||
		strings.Join(calls, ",") != "review,review again,verify" {
		t.Fatalf("kept: %+v %q %v", r.Comments, stats, calls)
	}

	r, stats, calls = run(map[string][]string{"review": {"still exploring", finding}, "verify": {drop}}, Settings{})
	if len(r.Comments) != 0 || !strings.HasSuffix(stats, "0/1 verified") || strings.Join(calls, ",") != "review,review repair,review again,verify" {
		t.Fatalf("repaired, dropped: %+v %q %v", r.Comments, stats, calls)
	}

	r, stats, calls = run(map[string][]string{"review": {finding}, "verify": {"no", "still no"}}, Settings{})
	if len(r.Comments) != 1 || r.Comments[0].Evidence != "" || !strings.HasSuffix(stats, "unverified") || strings.Join(calls, ",") != "review,review again,verify,verify repair" {
		t.Fatalf("unverified: %+v %q %v", r.Comments, stats, calls)
	}

	r, _, calls = run(map[string][]string{"review": {finding}}, Settings{pkgSettings: pkgSettings{MinSeverity: "critical"}})
	if len(r.Comments) != 0 || strings.Join(calls, ",") != "review,review again" {
		t.Fatalf("below min_severity skips verify: %+v %v", r.Comments, calls)
	}

	// The profile folds by the file the verifier settled on; min_severity drops a nit before verify.
	quiet, _ := LoadSettings(`{"profile":"quiet"}`)
	nit := `{"summary":"s","comments":[{"path":"a.md","line":1,"severity":"minor","title":"m","body":"b"},{"path":"a.md","line":2,"severity":"nit","title":"n","body":"b"}]}`
	r, _, _ = run(map[string][]string{"review": {nit}, "verify": {`{"verdicts":[{"i":0,"keep":true},{"i":1,"keep":true}]}`}}, quiet)
	if len(r.Comments) != 2 || !r.Comments[0].Fold || !r.Comments[1].Fold {
		t.Fatalf("quiet folds: %+v", r.Comments)
	}
	r, _, _ = run(map[string][]string{"review": {nit}, "verify": {`{"verdicts":[{"i":0,"keep":true},{"i":1,"keep":true}]}`}}, Settings{})
	if len(r.Comments) != 2 || r.Comments[0].Fold || !r.Comments[1].Fold {
		t.Fatalf("chill folds nits: %+v", r.Comments)
	}
	r, _, calls = run(map[string][]string{"review": {strings.Replace(nit, `"minor"`, `"nit"`, 1)}}, Settings{pkgSettings: pkgSettings{MinSeverity: "minor"}})
	if len(r.Comments) != 0 || strings.Join(calls, ",") != "review,review again" {
		t.Fatalf("min_severity minor drops nits: %+v %v", r.Comments, calls)
	}

	// The second look's findings are verified with the first's, one per line, most severe first.
	r, _, calls = run(map[string][]string{
		"review":       {finding},
		"review again": {`{"summary":"","comments":[{"path":"a.md","line":2,"severity":"critical","title":"c","body":"b"},{"path":"a.md","line":1,"severity":"minor","title":"m","body":"b"}]}`},
		"verify":       {`{"verdicts":[{"i":0,"keep":true},{"i":1,"keep":true},{"i":2,"keep":true}]}`},
	}, Settings{})
	if len(r.Comments) != 2 || r.Comments[0].Severity != "critical" || r.Comments[1].Severity != "major" || strings.Join(calls, ",") != "review,review again,verify" {
		t.Fatalf("second look: %+v %v", r.Comments, calls)
	}

	// A further finder, without a second look: its findings verified with the first's, the first's summary. A failed
	// first one leaves the other standing.
	no := false
	r, _, calls = run(map[string][]string{
		"review": {finding}, "review.2": {`{"summary":"x","comments":[{"path":"a.md","line":2,"severity":"minor","title":"m","body":"b"}]}`},
		"verify": {`{"verdicts":[{"i":0,"keep":true},{"i":1,"keep":true}]}`},
	}, Settings{Passes: []Pass{{}, {Again: &no}}})
	slices.Sort(calls)
	if len(r.Comments) != 2 || r.Summary != "s" || strings.Join(calls, ",") != "review,review again,review.2,verify" {
		t.Fatalf("passes: %+v %q %v", r.Comments, r.Summary, calls)
	}
	r, _, calls = run(map[string][]string{"review": {"x", "y"}, "review.2": {finding}, "verify": {keep}}, Settings{Passes: []Pass{{}, {}}})
	slices.Sort(calls)
	if len(r.Comments) != 1 || strings.Join(calls, ",") != "review,review repair,review.2,review.2 again,verify" {
		t.Fatalf("first pass failed: %+v %v", r.Comments, calls)
	}

	// Open earlier findings reach the verifier, numbered past the rest, so it can drop a repeat in other words; a
	// claimed fixed one stays in its own list.
	f := &fakeAgent{replies: map[string][]string{"review": {`{"summary":"","comments":[{"path":"a.md","line":2,"severity":"minor","title":"t","body":"b"}],"fixed":[1]}`}}}
	prior := []Comment{{Path: "a.md", Line: 1, Body: "open race"}, {Path: "a.md", Line: 1, Body: "gone"}} // threads carry no title
	if _, _, err := Analyze(context.Background(), Input{Agent: f, Dir: t.TempDir(), Parts: []string{md}, Prior: prior}); err != nil {
		t.Fatal(err)
	}
	if v := f.tasks["verify"]; !strings.Contains(v, "## Earlier findings, still open (posted already: no verdicts for these)\n\n### 3 · a.md:1\n\nopen race") ||
		strings.Count(v, "gone") != 1 {
		t.Fatalf("verify task: %q", v)
	}

	// A claimed fix the verifier rejects leaves its thread open: a kept finding repeating it is dropped, asked in the
	// verify session. A confirmed fix asks nothing; a failed answer keeps the finding.
	fixedRun := func(replies map[string][]string) (Result, []string) {
		t.Helper()
		replies["review"] = []string{`{"summary":"","comments":[{"path":"a.md","line":2,"severity":"minor","title":"t","body":"b"}],"fixed":[0]}`}
		f := &fakeAgent{replies: replies}
		r, _, err := Analyze(context.Background(), Input{Agent: f, Dir: t.TempDir(), Parts: []string{md}, Prior: []Comment{{Path: "a.md", Line: 1, Body: "race"}}})
		if err != nil {
			t.Fatal(err)
		}
		return r, f.calls
	}
	r, calls = fixedRun(map[string][]string{"verify": {`{"verdicts":[{"i":0,"keep":true}],"fixed":[]}`}, "verify repeats": {`{"repeats":[0]}`}})
	if len(r.Comments) != 0 || len(r.Dropped) != 1 || len(r.Fixed) != 0 || strings.Join(calls, ",") != "review,review again,verify,verify repeats" {
		t.Fatalf("repeats a rejected fix: %+v %+v %v", r.Comments, r.Dropped, calls)
	}
	r, calls = fixedRun(map[string][]string{"verify": {`{"verdicts":[{"i":0,"keep":true}],"fixed":[0]}`}})
	if len(r.Comments) != 1 || !slices.Equal(r.Fixed, []int{0}) || strings.Join(calls, ",") != "review,review again,verify" {
		t.Fatalf("confirmed fix: %+v %v %v", r.Comments, r.Fixed, calls)
	}
	for _, bad := range []string{"no json", `{"repeats":[null]}`, `{"repeats":[0,99]}`, `{"repeats":[-1]}`} {
		r, calls = fixedRun(map[string][]string{"verify": {`{"verdicts":[{"i":0,"keep":true}],"fixed":[]}`}, "verify repeats": {bad}})
		if len(r.Comments) != 1 || len(r.Dropped) != 0 || strings.Join(calls, ",") != "review,review again,verify,verify repeats" {
			t.Fatalf("repeats %s: %+v %v", bad, r.Comments, calls)
		}
	}

	// In parts: a review pass, a second look and a verify pass each, findings merged most severe first, a failed part
	// named. A second look without JSON leaves its part's first pass standing.
	other := `{"summary":"o","comments":[{"path":"a.md","line":1,"severity":"critical","title":"c","body":"b"},{"path":"b.md","line":5,"severity":"minor","title":"m","body":"b"}]}`
	r, stats, calls = run(map[string][]string{
		"review1": {finding}, "verify1": {keep},
		"review2": {other}, "review2 again": {"x"}, "verify2": {`{"verdicts":[{"i":0,"keep":true},{"i":1,"keep":true}]}`}, "review3": {"x", "y"},
	}, Settings{}, md, md, md)
	slices.Sort(calls)
	var got []string
	for _, c := range r.Comments {
		got = append(got, fmt.Sprintf("%s:%d %s", c.Path, c.Line, c.Severity))
	}
	if strings.Join(got, ", ") != "a.md:1 critical, a.md:2 major, b.md:5 minor" || r.Summary != "s o" ||
		!strings.HasSuffix(stats, " · 3 parts · 3/3 verified · part 3 failed") || !slices.Equal(r.Failed, []string{"a.md"}) ||
		strings.Join(calls, ",") != "review1,review1 again,review2,review2 again,review2 repair,review3,review3 repair,verify1,verify2" {
		t.Fatalf("parts: %v %q %q %v %v", got, r.Summary, stats, calls, r.Failed)
	}

	// A finding on another part's file is that part's: dropped before verify. b.md above is in no part, it stays.
	bmd := strings.ReplaceAll(md, "a.md", "b.md")
	r, _, _ = run(map[string][]string{"review1": {other}, "verify1": {keep}}, Settings{}, md, bmd)
	if len(r.Comments) != 1 || r.Comments[0].Path != "a.md" {
		t.Fatalf("another part's file: %+v", r.Comments)
	}

	// A file in pieces: a finding on lines of another part's piece is that part's.
	at11 := `{"summary":"","comments":[{"path":"a.md","line":11,"severity":"major","title":"t","body":"b"}]}`
	piece2 := "diff --git a/a.md b/a.md\n--- a/a.md\n+++ b/a.md\n@@ -9 +10,2 @@\n x\n+z\n"
	r, _, calls = run(map[string][]string{"review1": {at11}, "review2": {at11}, "verify2": {`{"verdicts":[{"i":0,"keep":true}]}`}}, Settings{}, md, piece2)
	slices.Sort(calls)
	if len(r.Comments) != 1 || strings.Join(calls, ",") != "review1,review1 again,review2,review2 again,verify2" {
		t.Fatalf("a file in pieces: %+v %v", r.Comments, calls)
	}

	if _, tag, _ := merge([]part{{}}, "m", "m", "ci.yml"); tag != "m · tests: CI (ci.yml)" {
		t.Fatalf("tests left to CI: %q", tag)
	}

	// Changes that could merge on their own, across parts, become the first decision; one change asks nothing.
	q := []Decision{{Text: "q1"}, {Text: "q2"}, {Text: "q3"}}
	r, _, _ = merge([]part{{r: Result{Split: []string{"cache"}, Decisions: q}}, {r: Result{Split: []string{"retry"}}}}, "m", "m", "")
	if len(r.Decisions) != 3 || r.Decisions[0].Text != "Split into 2 PRs? The AI suggests: cache; retry. Each could be reviewed and merged on its own; check the cut first." || r.Decisions[2].Text != "q2" {
		t.Fatalf("split: %+v", r.Decisions)
	}
	if r, _, _ = merge([]part{{r: Result{Split: []string{"cache"}}}}, "m", "m", ""); len(r.Decisions) != 0 {
		t.Fatalf("one change: %+v", r.Decisions)
	}
}

func TestExplain(t *testing.T) {
	f := &fakeAgent{replies: map[string][]string{"explain": {`{"1":"parses the flag","3":"ignored"}`}}}
	got := Explain(context.Background(), f, t.TempDir(), "openai/m:low", "t", []string{"a.go\nfunc A()", "b.go\nfunc B()"})
	if !slices.Equal(got, []string{"parses the flag", ""}) || !slices.Equal(f.calls, []string{"explain repair"}) {
		t.Errorf("explain %q, calls %q", got, f.calls)
	}
	if got := Explain(context.Background(), f, "", "m", "t", nil); len(got) != 0 || len(f.calls) != 1 {
		t.Error("no declarations, still a call")
	}
}
