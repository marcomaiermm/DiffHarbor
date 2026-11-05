package job

import (
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
	"diffharbor/internal/review"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"time"
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
	now := time.Now()
	for i := range review.MaxLearnings + 2 {
		addLearning("o/r", fmt.Sprintf("- r%d (@u)", i), now)
	}
	addLearning("o", "- shared (@u)", now)
	addLearning("x/y", "- other (@u)", now)
	got := readLearnings("o", "o/r", review.Learnings{})
	if !strings.HasPrefix(got, "- shared (@u, "+now.Format(time.DateOnly)+")\n- r72 ") || strings.Count(got, "\n") != 31 || strings.Contains(got, "other") {
		t.Fatalf("%q", got)
	}
	if got := learningsOf("o/r"); len(got) != review.MaxLearnings || !strings.HasPrefix(got[0], "- r2 ") {
		t.Fatalf("kept %d, first %q", len(got), got[0])
	}
	none, ten := 0, 10
	if got := readLearnings("o", "o/r", review.Learnings{Max: &none}); got != "" {
		t.Fatalf("max 0: %q", got)
	}
	if got := readLearnings("o", "o/r", review.Learnings{Max: &ten}); strings.Count(got, "\n") != 11 {
		t.Fatalf("max 10: %q", got)
	}
	addLearning("a", "- old (@u)", now.AddDate(0, 0, -40))
	addLearning("a", "- new (@u)", now.AddDate(0, 0, -10))
	writeLearnings("a", append(learningsOf("a"), "- undated (@u)"))
	if got := readLearnings("a", "a/b", review.Learnings{MaxAgeDays: 30}); strings.Contains(got, "old") || !strings.Contains(got, "new") || !strings.Contains(got, "undated") {
		t.Fatalf("max_age_days 30: %q", got)
	}
	if gone, err := forgetLearnings("o/r", "R1"); err != nil || len(gone) != 12 || strings.Contains(strings.Join(learningsOf("o/r"), ""), "r1") {
		t.Fatalf("forget: %v %q", err, gone)
	}
}

// `learnings` lists the repo's and the owner's; `forget` drops the owner's only for an owner or member.
func TestLearningsCommand(t *testing.T) {
	proc.DataDir = t.TempDir()
	e := &forge.Event{}
	e.Repository.FullName = "o/r"
	if got := learningsCommand("learnings", "bot", e); got != "No learnings yet." {
		t.Fatalf("empty: %q", got)
	}
	now := time.Now()
	addLearning("o/r", "- repo rule (@u)", now)
	addLearning("o", "- shared rule (@u)", now)
	if got := learningsCommand("learnings", "bot", e); !strings.Contains(got, "**This repo**\n\n- repo rule") || !strings.Contains(got, "**All `o` repos**\n\n- shared rule") {
		t.Fatalf("list: %q", got)
	}
	if got := learningsCommand("forget  ru ", "bot", e); !strings.HasPrefix(got, "Forget what?") {
		t.Fatalf("short: %q", got)
	}
	e.Comment.AuthorAssociation = "COLLABORATOR"
	if got := learningsCommand("forget SHARED", "bot", e); !strings.HasPrefix(got, "No learning contains") || len(learningsOf("o")) != 1 {
		t.Fatalf("collaborator forgot the owner's: %q", got)
	}
	e.Comment.AuthorAssociation = "MEMBER"
	if got := learningsCommand("forget rule", "bot", e); !strings.Contains(got, "- repo rule") || !strings.Contains(got, "- shared rule") ||
		len(learningsOf("o/r"))+len(learningsOf("o")) != 0 {
		t.Fatalf("member: %q", got)
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

// A reply in our thread without @bot gets 👀 from Chat, off the worker: a slow forge doesn't hold up the queue.
type reactPR struct {
	skipPR
	reacted, release chan struct{}
}

func (*reactPR) Discussion() (forge.Discussion, error) { return forge.Discussion{Ours: true}, nil }
func (p *reactPR) React()                              { close(p.reacted); <-p.release }

type reactForge struct {
	skipForge
	pr *reactPR
}

func (f reactForge) Open(*forge.Event) (forge.PR, error) { return f.pr, nil }

func TestChatReactsOffTheWorker(t *testing.T) {
	pr := &reactPR{reacted: make(chan struct{}), release: make(chan struct{})}
	defer close(pr.release)
	e := &forge.Event{Kind: "pull_request_review_comment"} // no repository id: Chat stops right after the thread check
	e.Comment.Body = "why?"
	done := make(chan error, 1)
	go func() { done <- Chat(t.Context(), reactForge{pr: pr}, regexp.MustCompile(`@bot`), e) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no repository id") {
			t.Fatal("Chat stopped elsewhere:", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Chat waited on the reaction")
	}
	select {
	case <-pr.reacted:
	case <-time.After(5 * time.Second):
		t.Fatal("Chat did not react")
	}
}
