package job

import (
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLearnings(t *testing.T) {
	proc.DataDir = t.TempDir()
	for i := range maxLearnings + 2 {
		addLearning("o/r", fmt.Sprintf("- r%d", i))
	}
	addLearning("o", "- shared")
	addLearning("x/y", "- other")
	got := readLearnings("o", "o/r")
	if !strings.HasPrefix(got, "- shared\n- r2\n") || strings.Contains(got, "- r1\n") || strings.Contains(got, "other") {
		t.Fatalf("%q", got)
	}
}

// A file left out keeps its last reviewed patch ID, one never reviewed gets none: the next push reviews both if changed.
func TestReviewedPatches(t *testing.T) {
	got := reviewedPatches(map[string]string{"a": "1", "b": "1", "gone": "1"}, map[string]string{"a": "2", "b": "2", "c": "2", "d": "2"}, []string{"a", "c"})
	if fmt.Sprint(got) != "map[a:2 b:1 c:2]" {
		t.Fatal(got)
	}
}

// A PR keeps its newest reports, one per head, newest first.
func TestReportVersions(t *testing.T) {
	proc.DataDir = t.TempDir()
	e := &forge.Event{}
	e.Repository.FullName, e.PullRequest.Number, e.PullRequest.HTMLURL = "o/r", 7, "https://github.com/o/r/pull/7"
	sha := func(i int) string { return fmt.Sprintf("%040x", i) }
	for i := range maxVersions + 2 {
		if u := writeReport(e, report.Data{Repo: "o/r", Number: 7, SHA: sha(i)}); u != ReportURL+"/r/github.com/o/r/7" {
			t.Fatal(u)
		}
	}
	writeReport(e, report.Data{Repo: "o/r", Number: 7, SHA: sha(5)}) // reviewed again: the newest
	vs := Versions(reportPath(e))
	if len(vs) != maxVersions || vs[0].SHA != sha(5) || vs[1].SHA != sha(maxVersions+1) || vs[0].At.IsZero() {
		t.Fatalf("%+v", vs)
	}
	if _, err := os.Stat(filepath.Join(ReportDir(e), sha(0)+".html")); !os.IsNotExist(err) {
		t.Fatal("kept a version past maxVersions:", err)
	}
	gl := &forge.Event{Forge: "gitlab"} // a project in a subgroup
	gl.Repository.FullName, gl.PullRequest.Number, gl.PullRequest.HTMLURL = "g/s/p", 3, "https://gitlab.example.com/g/s/p/-/merge_requests/3"
	if u := writeReport(gl, report.Data{Repo: "g/s/p", Number: 3, SHA: sha(1)}); u != ReportURL+"/r/gitlab.example.com/g/s/p/3" {
		t.Fatal(u)
	}
}
