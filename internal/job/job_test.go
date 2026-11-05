package job

import (
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

type skipForge struct {
	forge.Forge
	pr *skipPR
}

func (skipForge) Bot() string                           { return "bot" }
func (f skipForge) Open(*forge.Event) (forge.PR, error) { return f.pr, nil }

// The PR methods the base-branch skip reaches; anything past it panics on the nil forge.PR.
type skipPR struct {
	forge.PR
	notes  []string
	status string
}

func (*skipPR) Refresh() error                          { return nil }
func (*skipPR) File(string, ...string) string           { return `{"base_branches":["release/*"]}` }
func (*skipPR) Notes() ([]forge.Note, error)            { return nil, nil }
func (p *skipPR) Status(_, state, desc string)          { p.status = state + " " + desc }
func (p *skipPR) Note(_ int64, b string) (int64, error) { p.notes = append(p.notes, b); return 1, nil }

// A PR into a branch neither the default nor in base_branches gets no review, but a green status.
func TestReviewSkipsBaseBranch(t *testing.T) {
	pr := &skipPR{}
	f := skipForge{pr: pr}
	e := &forge.Event{Action: "opened"}
	e.Repository.DefaultBranch = "main"
	e.PullRequest.State, e.PullRequest.Head.SHA, e.PullRequest.Base.Ref = "open", strings.Repeat("a", 40), "prod"
	if err := Review(t.Context(), f, e); err != nil || pr.status != "success Skipped: base branch" || len(pr.notes) != 1 ||
		!strings.Contains(pr.notes[0], "Review skipped:** base branch `prod`") {
		t.Fatalf("%v %q %q", err, pr.status, pr.notes)
	}
}

// A self-hosted Renovate on a user token is no Bot type, its branch or description skips the review.
func TestReviewSkipsBotBranch(t *testing.T) {
	e := &forge.Event{Action: "opened"}
	e.Repository.DefaultBranch = "main"
	e.PullRequest.State, e.PullRequest.Head.SHA, e.PullRequest.Base.Ref = "open", strings.Repeat("a", 40), "main"
	for _, c := range []struct{ ref, body string }{
		{"renovate/vite-7.x", ""},
		{"deps/vite-7.x", "Update vite\n\n<!--renovate-debug:eyJjcmVhdGVk-->"},
	} {
		pr := &skipPR{}
		e.PullRequest.Head.Ref, e.PullRequest.Body = c.ref, c.body
		if err := Review(t.Context(), skipForge{pr: pr}, e); err != nil || pr.status != "success Skipped: bot author" {
			t.Fatalf("%s: %v %q %q", c.ref, err, pr.status, pr.notes)
		}
	}
}

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
	key := reportSecret("")
	dir := reportPath(e) + "/" + key
	for i := range maxVersions + 2 {
		if u := writeReport(e, key, report.Data{Repo: "o/r", Number: 7, SHA: sha(i)}); u != ReportURL+"/r/github.com/o/r/7/"+key {
			t.Fatal(u)
		}
	}
	writeReport(e, key, report.Data{Repo: "o/r", Number: 7, SHA: sha(5)}) // reviewed again: the newest
	vs, err := Versions(dir)
	if err != nil || len(vs) != maxVersions || vs[0].SHA != sha(5) || vs[1].SHA != sha(maxVersions+1) || vs[0].At.IsZero() {
		t.Fatalf("%+v", vs)
	}
	if _, err := Reports().Get(dir + "/" + sha(0) + ".html.gz"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("kept a version past maxVersions:", err)
	}
	if _, err := Reports().Get(dir + "/" + sha(5) + ".html.gz"); err != nil {
		t.Fatal("the newest page:", err)
	}
	Reports().Put(reportPath(e)+"/versions.json", []byte("[]")) // from before the secret
	if err := DeleteReports(e); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{dir + "/versions.json", reportPath(e) + "/versions.json"} {
		if _, err := Reports().Get(k); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("closed PR kept its reports:", k, err)
		}
	}
	Reports().Put(dir+"/versions.json", []byte("{")) // unreadable: a report would drop the list
	if u := writeReport(e, key, report.Data{Repo: "o/r", Number: 7, SHA: sha(1)}); u != "" {
		t.Fatal("wrote over an unreadable list:", u)
	}
	gl := &forge.Event{Forge: "gitlab"} // a project in a subgroup
	gl.Repository.FullName, gl.PullRequest.Number, gl.PullRequest.HTMLURL = "g/s/p", 3, "https://gitlab.example.com/g/s/p/-/merge_requests/3"
	if u := writeReport(gl, key, report.Data{Repo: "g/s/p", Number: 3, SHA: sha(1)}); u != ReportURL+"/r/gitlab.example.com/g/s/p/3/"+key {
		t.Fatal(u)
	}
}

// A PR keeps the secret its walkthrough holds; a new one is random, and one edited out of shape is replaced.
func TestReportSecret(t *testing.T) {
	a, b := reportSecret(""), reportSecret("")
	if !Secret.MatchString(a) || a == b || reportSecret(a) != a {
		t.Fatal(a, b)
	}
	for _, s := range []string{"../../x/y", "7", strings.ToLower(a), a + "A"} {
		if got := reportSecret(s); got == s || !Secret.MatchString(got) {
			t.Fatal(s, got)
		}
	}
}
