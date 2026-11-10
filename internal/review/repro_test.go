package review

import (
	"diffharbor/internal/proc"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// A repro that fails keeps its finding with the run, one that passes lowers it, the rest stay as they were; the budget,
// tests off and the PR's own files hold, and no test file is left behind.
func TestReproduce(t *testing.T) {
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi = false
	sb := &proc.Sandbox{Dir: t.TempDir()}
	repo := sb.Repo()
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, s := range map[string]string{"go.mod": "module m\n\ngo 1.21\n", "f.go": "package m\n\nfunc F() int { return 1 }\n", "mine_test.go": "package m\n"} {
		if err := os.WriteFile(filepath.Join(repo, f), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	test := func(file, name, body string) *reproTest {
		return &reproTest{File: file, Name: name, Source: "package m\n\nimport \"testing\"\n\nfunc " + name + "(t *testing.T) { " + body + " }\n"}
	}
	major := func(r *reproTest) Comment {
		return Comment{Path: "f.go", Line: 3, Severity: "major", Body: "b", repro: r}
	}
	cs := []Comment{
		major(test("a_test.go", "TestFails", "if F() == 1 { t.Fatal(\"F is 1\") }")),
		major(test("b_test.go", "TestPasses", "if F() != 1 { t.Fatal() }")),
		major(test("c_test.go", "TestBroken", "undefined()")),
		major(test("d_test.go", "TestOver", "t.Fatal()")), // the budget of 3 is spent
	}
	in := Input{Sandbox: sb, repros: new(atomic.Int32)}
	reproduce(t.Context(), in, cs)
	if c := cs[0]; c.Severity != "major" || !strings.HasPrefix(c.Evidence, Repro+"`go test") || !strings.Contains(c.Body, "F is 1") {
		t.Errorf("reproduced: %+v", c)
	}
	if c := cs[1]; c.Severity != "minor" || !strings.HasPrefix(c.Evidence, Repro) || !strings.Contains(c.Evidence, "passes") {
		t.Errorf("refuted: %+v", c)
	}
	skipped := []Comment{ // a build error, the budget, the PR's own file, not a test file, a minor finding
		cs[2], cs[3], major(test("mine_test.go", "TestMine", "t.Fatal()")), major(test("e.go", "TestE", "t.Fatal()")),
		{Path: "f.go", Severity: "minor", repro: test("f_test.go", "TestMinor", "t.Fatal()")},
	}
	reproduce(t.Context(), Input{Sandbox: sb, repros: new(atomic.Int32)}, skipped[2:])
	reproduce(t.Context(), in, skipped[:2])
	for i, c := range skipped {
		if c.Evidence != "" || c.Body != "b" && i != 4 || c.Severity != "major" && i != 4 {
			t.Errorf("not attempted %d: %+v", i, c)
		}
	}
	if es, _ := os.ReadDir(repo); len(es) != 3 {
		t.Errorf("left behind: %v", es)
	}
	cfg, err := LoadSettings(`{"tests":false}`)
	if err != nil {
		t.Fatal(err)
	}
	off := []Comment{major(test("a_test.go", "TestFails", "t.Fatal()"))}
	reproduce(t.Context(), Input{Sandbox: sb, repros: new(atomic.Int32), Settings: cfg}, off)
	if off[0].Evidence != "" {
		t.Errorf("tests off: %+v", off[0])
	}
}

// The verdict's repro goes with the kept finding.
func TestVerdictRepro(t *testing.T) {
	kept, _, _, ok := verdicts([]Comment{{Path: "a.go"}}, nil, `{"verdicts":[{"i":0,"keep":true,"repro":{"file":"a_test.go","name":"TestA","source":"s"}}]}`, nil)
	if !ok || kept[0].repro == nil || kept[0].repro.Name != "TestA" {
		t.Fatalf("%+v", kept)
	}
}
