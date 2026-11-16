package report

import "testing"

// The workflow that runs the tests on the base decides; else a check's workflow file; else any CI file.
func TestEdits(t *testing.T) {
	quality := []Check{{Name: "test", Path: ".github/workflows/quality.yml"}}
	for _, c := range []struct {
		files  []string
		checks []Check
		tests  string
		want   bool
	}{
		{[]string{".github/workflows/quality.yml"}, nil, "quality.yml", true},
		{[]string{".github/workflows/lint.yml"}, quality, "quality.yml", false},
		{[]string{".gitlab-ci.yml"}, nil, ".gitlab-ci.yml", true},
		{[]string{".github/workflows/quality.yml"}, quality, "", true},
		{[]string{".github/workflows/lint.yml"}, quality, "", false},
		{[]string{".github/workflows/lint.yml"}, []Check{{Name: "test"}}, "", true}, // no path from the forge
		{[]string{".gitlab-ci.yml"}, nil, "", true},
		{[]string{"src/a.go"}, nil, "", false},
	} {
		if got := Edits(c.files, c.checks, c.tests); got != c.want {
			t.Errorf("Edits(%v, %v, %q) = %v", c.files, c.checks, c.tests, got)
		}
	}
	if !(Check{Conclusion: "timed_out"}).Failed() || (Check{Conclusion: "neutral"}).Failed() || (Check{Status: "pending"}).Failed() {
		t.Error("Failed")
	}
}
