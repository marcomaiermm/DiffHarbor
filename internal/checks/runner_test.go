package checks

import (
	"diffharbor/internal/proc"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestCmd picks the package's runner as Run does and names the one test.
func TestTestCmd(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		file  string
		proof string
		dir   string
		want  []string // the runner's bin is repo-relative here
	}{
		{"go", nil, "a/b_test.go", "TestX/sub one", "a", []string{"go", "test", "-v", "-short", "-count=1", "-timeout=1m30s", "-run=^TestX$/^sub one$", "."}},
		{
			"vitest",
			map[string]string{"package.json": "{}", "node_modules/.bin/vitest": "", "node_modules/.bin/jest": ""},
			"src/a.test.ts", "suite > it works",
			".",
			[]string{"node_modules/.bin/vitest", "run", "--reporter=verbose", "--testNamePattern=suite it works", "./src/a.test.ts"},
		},
		{
			"jest in a package, bin hoisted",
			map[string]string{"web/package.json": "{}", "node_modules/.bin/jest": ""},
			"web/a.spec.js", "works",
			"web",
			[]string{"node_modules/.bin/jest", "--ci", "--verbose", "--testNamePattern=works", "--runTestsByPath", "./a.spec.js"},
		},
		{"bun", map[string]string{"package.json": "{}", "bun.lock": ""}, "a.test.ts", "it (x)", ".", []string{"bun", "test", `--test-name-pattern=it \(x\)`, "./a.test.ts"}},
		{"node", map[string]string{"package.json": `{"scripts":{"test":"node --test"}}`}, "a.test.js", "it", ".", []string{"node", "--test", "--test-name-pattern=it", "./a.test.js"}},
		{"no runner", map[string]string{"package.json": "{}"}, "a.test.js", "it", "", nil},
		{"no package", nil, "a.test.js", "it", "", nil},
		{"not code", map[string]string{"package.json": "{}"}, "a.md", "it", "", nil},
	} {
		repo := t.TempDir()
		for f, s := range tc.files {
			_ = os.MkdirAll(filepath.Dir(filepath.Join(repo, f)), 0o755)
			if err := os.WriteFile(filepath.Join(repo, f), []byte(s), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		dir, got := TestCmd(t.Context(), &proc.Sandbox{}, repo, tc.file, tc.proof, 90*time.Second)
		if len(got) > 0 && filepath.IsAbs(got[0]) {
			got[0], _ = filepath.Rel(repo, got[0])
		}
		if dir != tc.dir || !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q %q", tc.name, dir, got)
		}
	}
}

// Outcome reads each runner's verbose line for the test, a failure first; no line is no run.
func TestOutcome(t *testing.T) {
	for _, tc := range []struct{ out, proof, want string }{
		{"=== RUN   TestX\n--- PASS: TestX (0.00s)\nok", "TestX", "pass"},
		{"    --- FAIL: TestX/sub (0.00s)\n--- FAIL: TestX (0.00s)", "TestX/sub", "fail"},
		{"ok  \tm\t0.1s [no tests to run]", "TestX", ""},
		{" ✓ src/a.test.ts > suite > it works 2ms", "suite > it works", "pass"},
		{" × src/a.test.ts > suite > it works 2ms", "suite > it works", "fail"},
		{" ↓ src/a.test.ts > it works", "it works", ""},
		{"  ✓ it works (3 ms)\n  ✕ other (1 ms)", "it works", "pass"},
		{"(fail) suite > it works [0.1ms]", "suite > it works", "fail"},
		{"✔ it works (0.5ms)", "it works", "pass"},
		{"✖ it works (0.5ms)", "it works", "fail"},
		{"suite\n    ✓ it works (3 ms)", "suite > it works", "pass"},
		{"✕ it works\n✓ it works too", "it works", "fail"},
	} {
		if got := Outcome(tc.out, tc.proof); got != tc.want {
			t.Errorf("%q %q: %q", tc.out, tc.proof, got)
		}
	}
}
