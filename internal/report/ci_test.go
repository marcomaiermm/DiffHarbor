package report

import "testing"

// The workflow that runs the tests on the base decides; else a check's workflow file; else any CI file.
func TestEdits(t *testing.T) {
	tested := []Check{{Name: "test", Path: ".github/workflows/test.yml"}}
	for _, c := range []struct {
		files  []string
		checks []Check
		tests  string
		want   bool
	}{
		{[]string{".github/workflows/test.yml"}, nil, "test.yml", true},
		{[]string{".github/workflows/lint.yml"}, tested, "test.yml", false},
		{[]string{".gitlab-ci.yml"}, nil, ".gitlab-ci.yml", true},
		{[]string{".github/workflows/test.yml"}, tested, "", true},
		{[]string{".github/workflows/lint.yml"}, tested, "", false},
		{[]string{".github/workflows/lint.yml"}, []Check{{Name: "test"}}, "", true}, // no path from the forge
		{[]string{".gitlab-ci.yml"}, nil, "", true},
		{[]string{"src/a.go"}, nil, "", false},
		{[]string{".github/workflows/lint.yml"}, []Check{{Name: "lint", Path: ".github/workflows/lint.yml"}}, "test.yml", false},
	} {
		if got := Edits(c.files, c.checks, c.tests); got != c.want {
			t.Errorf("Edits(%v, %v, %q) = %v", c.files, c.checks, c.tests, got)
		}
	}
	if !(Check{Conclusion: "timed_out"}).Failed() || (Check{Conclusion: "neutral"}).Failed() || (Check{Status: "pending"}).Failed() {
		t.Error("Failed")
	}
}

// A JS runner's "suite > test" resolves as given; a Go name by its key or as the one subtest of that name.
func TestRanOf(t *testing.T) {
	ran := map[string]string{"cart > adds tax": "fail", "TestX/a_b": "pass"}
	for name, want := range map[string]string{"cart > adds tax": "fail", "TestX/a b": "pass", "a b": "pass", "cart": ""} {
		if got := ranOf(ran, name); got != want {
			t.Errorf("ranOf(%q) = %q, want %q", name, got, want)
		}
	}
}
