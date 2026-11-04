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
}

func (f *fakeAgent) reply(name string) (string, error) {
	q := f.replies[name]
	if len(q) == 0 {
		return "", nil
	}
	f.replies[name] = q[1:]
	return q[0], nil
}

func (f *fakeAgent) Run(_ context.Context, _, name, _, _, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	return f.reply(name)
}

func (f *fakeAgent) Continue(_ context.Context, _, name, _, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name+" repair")
	return f.reply(name)
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
		strings.Join(calls, ",") != "review,verify" {
		t.Fatalf("kept: %+v %q %v", r.Comments, stats, calls)
	}

	r, stats, calls = run(map[string][]string{"review": {"still exploring", finding}, "verify": {drop}}, Settings{})
	if len(r.Comments) != 0 || !strings.HasSuffix(stats, "0/1 verified") || strings.Join(calls, ",") != "review,review repair,verify" {
		t.Fatalf("repaired, dropped: %+v %q %v", r.Comments, stats, calls)
	}

	r, stats, calls = run(map[string][]string{"review": {finding}, "verify": {"no", "still no"}}, Settings{})
	if len(r.Comments) != 1 || r.Comments[0].Evidence != "" || !strings.HasSuffix(stats, "unverified") || strings.Join(calls, ",") != "review,verify,verify repair" {
		t.Fatalf("unverified: %+v %q %v", r.Comments, stats, calls)
	}

	r, _, calls = run(map[string][]string{"review": {finding}}, Settings{pkgSettings: pkgSettings{MinSeverity: "critical"}})
	if len(r.Comments) != 0 || strings.Join(calls, ",") != "review" {
		t.Fatalf("below min_severity skips verify: %+v %v", r.Comments, calls)
	}

	// In parts: one review and verify pass each, findings merged most severe first, one per line, a failed part named.
	other := `{"summary":"o","comments":[{"path":"a.md","line":1,"severity":"critical","title":"c","body":"b"},{"path":"b.md","line":5,"severity":"minor","title":"m","body":"b"}]}`
	r, stats, calls = run(map[string][]string{
		"review1": {finding}, "verify1": {keep},
		"review2": {other}, "verify2": {`{"verdicts":[{"i":0,"keep":true},{"i":1,"keep":true}]}`}, "review3": {"x", "y"},
	}, Settings{}, md, md, md)
	slices.Sort(calls)
	var got []string
	for _, c := range r.Comments {
		got = append(got, fmt.Sprintf("%s:%d %s", c.Path, c.Line, c.Severity))
	}
	if strings.Join(got, ", ") != "a.md:1 critical, a.md:2 major, b.md:5 minor" || r.Summary != "s o" ||
		!strings.HasSuffix(stats, " · 3 parts · 3/3 verified · part 3 failed") || !slices.Equal(r.Failed, []string{"a.md"}) ||
		strings.Join(calls, ",") != "review1,review2,review3,review3 repair,verify1,verify2" {
		t.Fatalf("parts: %v %q %q %v %v", got, r.Summary, stats, calls, r.Failed)
	}

	// A finding on another part's file is that part's: dropped before verify. b.md above is in no part, it stays.
	bmd := strings.ReplaceAll(md, "a.md", "b.md")
	r, _, _ = run(map[string][]string{"review1": {other}, "verify1": {keep}}, Settings{}, md, bmd)
	if len(r.Comments) != 1 || r.Comments[0].Path != "a.md" {
		t.Fatalf("another part's file: %+v", r.Comments)
	}

	if _, tag, _ := merge([]part{{}}, "m", "m", "ci.yml"); tag != "m · tests: CI (ci.yml)" {
		t.Fatalf("tests left to CI: %q", tag)
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
