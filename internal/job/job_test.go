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
	"sync"
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
	cfg     string // review.json; unset: base_branches release/*
	notes   []string
	replies []string
	status  string
	// Configure's: the profile the review got
	settings review.Settings
}

func (*skipPR) Refresh() error                  { return nil }
func (p *skipPR) Configure(cfg review.Settings) { p.settings = cfg }
func (p *skipPR) Reply(b string) error          { p.replies = append(p.replies, b); return nil }

func (p *skipPR) File(string, ...string) string {
	if p.cfg != "" {
		return p.cfg
	}
	return `{"base_branches":["release/*"]}`
}

// Notes is the walkthrough as last written: its state carries over to the next review.
func (p *skipPR) Notes() ([]forge.Note, error) {
	if len(p.notes) == 0 {
		return nil, nil
	}
	return []forge.Note{{ID: 1, Body: p.notes[len(p.notes)-1], Ours: true}}, nil
}
func (p *skipPR) Status(_, state, desc string)          { p.status = state + " " + desc }
func (p *skipPR) Note(_ int64, b string) (int64, error) { p.notes = append(p.notes, b); return 1, nil }

// A PR into a branch neither the default nor in base_branches gets no review, but a green status.
func TestReviewSkipsBaseBranch(t *testing.T) {
	pr := &skipPR{}
	f := skipForge{pr: pr}
	e := &forge.Event{Action: "opened"}
	e.Repository.DefaultBranch = "main"
	e.PullRequest.State, e.PullRequest.Head.SHA, e.PullRequest.Base.Ref = "open", strings.Repeat("a", 40), "prod"
	if err := Review(t.Context(), f, e, ""); err != nil || pr.status != "success Skipped: base branch" || len(pr.notes) != 1 ||
		!strings.Contains(pr.notes[0], "Review skipped:** base branch `prod`") {
		t.Fatalf("%v %q %q", err, pr.status, pr.notes)
	}
}

// A draft gets no review on open by default: no status, no comment, ready_for_review brings it.
func TestReviewSkipsDraft(t *testing.T) {
	pr := &skipPR{}
	e := &forge.Event{Action: "opened"}
	e.Repository.DefaultBranch = "main"
	e.PullRequest.State, e.PullRequest.Draft, e.PullRequest.Head.SHA, e.PullRequest.Base.Ref = "open", true, strings.Repeat("a", 40), "main"
	if err := Review(t.Context(), skipForge{pr: pr}, e, ""); err != nil || pr.status != "" || len(pr.notes) != 0 {
		t.Fatalf("%v %q %q", err, pr.status, pr.notes)
	}
}

// drafts off: asked anyway, the bot says why instead of reviewing.
func TestReviewDraftOff(t *testing.T) {
	pr := &skipPR{cfg: `{"drafts":"off"}`}
	e := &forge.Event{Action: "manual"}
	e.PullRequest.State, e.PullRequest.Draft, e.PullRequest.Head.SHA = "open", true, strings.Repeat("a", 40)
	if err := Review(t.Context(), skipForge{pr: pr}, e, ""); err != nil || pr.status != "" || len(pr.notes) != 0 ||
		len(pr.replies) != 1 || !strings.Contains(pr.replies[0], "Drafts get no review") {
		t.Fatalf("%v %q %q %q", err, pr.status, pr.notes, pr.replies)
	}
}

// drafts opt_in: a draft's pushes get no review until someone asks, then they do. Into prod, so a push that gets
// past the draft gate stops at the base-branch skip.
func TestReviewDraftOptIn(t *testing.T) {
	pr := &skipPR{cfg: `{"drafts":"opt_in"}`}
	f := skipForge{pr: pr}
	e := &forge.Event{Action: "synchronize"}
	e.Repository.DefaultBranch = "main"
	e.PullRequest.State, e.PullRequest.Draft, e.PullRequest.Head.SHA, e.PullRequest.Base.Ref = "open", true, strings.Repeat("a", 40), "prod"
	if err := Review(t.Context(), f, e, ""); err != nil || pr.status != "" {
		t.Fatalf("push before asking: %v %q", err, pr.status)
	}
	e.Action = "manual" // fails past the gate: the event has no repository id
	if err := Review(t.Context(), f, e, ""); err == nil || pr.status != "error Review failed" {
		t.Fatalf("asked: %v %q", err, pr.status)
	}
	e.Action, e.PullRequest.Head.SHA = "synchronize", strings.Repeat("b", 40)
	if err := Review(t.Context(), f, e, ""); err != nil || pr.status != "success Skipped: base branch" {
		t.Fatalf("push after asking: %v %q", err, pr.status)
	}
}

// panicForge's bug is past the review's gates: RepoID asks for its Instance.
type panicForge struct{ skipForge }

func (panicForge) Instance() string { panic("boom") }

// A bug in a review fails it, a red status rather than a green one, and the server's other jobs run on.
func TestReviewPanicFails(t *testing.T) {
	pr := &skipPR{cfg: `{}`}
	e := &forge.Event{Action: "opened"}
	e.Repository.ID, e.Repository.DefaultBranch = 1, "main"
	e.PullRequest.State, e.PullRequest.Head.SHA, e.PullRequest.Base.Ref = "open", strings.Repeat("a", 40), "main"
	err := Review(t.Context(), panicForge{skipForge{pr: pr}}, e, "")
	if err == nil || !strings.HasPrefix(err.Error(), "panic: boom") || pr.status != "error Review failed" {
		t.Fatalf("%v %q", err, pr.status)
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
		if err := Review(t.Context(), skipForge{pr: pr}, e, ""); err != nil || pr.status != "success Skipped: bot author" {
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

// An owner's file is shared by its repos' jobs, which run at once: no append is lost.
func TestLearningsConcurrent(t *testing.T) {
	proc.DataDir = t.TempDir()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			if err := addLearning("o", fmt.Sprintf("- r%d (@u)", i), time.Now()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := learningsOf("o"); len(got) != 50 {
		t.Fatalf("kept %d of 50", len(got))
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
	e.Repository.ID, e.Repository.FullName, e.PullRequest.Number, e.PullRequest.HTMLURL = 5, "o/r", 7, "https://github.com/o/r/pull/7"
	sha := func(i int) string { return fmt.Sprintf("%040x", i) }
	key := reportSecret("")
	dir := reportPath(e) + "/" + key
	for i := range maxVersions + 2 {
		if u := writeReport(e, key, "", report.Data{Repo: "o/r", Number: 7, SHA: sha(i)}); u != ReportURL+"/r/github.com/id:5/7/"+key {
			t.Fatal(u)
		}
	}
	writeReport(e, key, "", report.Data{Repo: "o/r", Number: 7, SHA: sha(5)}) // reviewed again: the newest
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
	Reports().Put(namePath(e)+"/"+key+"/versions.json", []byte("[]")) // from before the id
	Reports().Put(namePath(e)+"/versions.json", []byte("[]"))         // from before the secret
	if err := DeleteReports(e); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{dir + "/versions.json", namePath(e) + "/" + key + "/versions.json", namePath(e) + "/versions.json"} {
		if _, err := Reports().Get(k); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("closed PR kept its reports:", k, err)
		}
	}
	Reports().Put(dir+"/versions.json", []byte("{")) // unreadable: a report would drop the list
	if u := writeReport(e, key, "", report.Data{Repo: "o/r", Number: 7, SHA: sha(1)}); u != "" {
		t.Fatal("wrote over an unreadable list:", u)
	}
	gl := &forge.Event{Forge: "gitlab"} // a project in a subgroup
	gl.Repository.FullName, gl.PullRequest.Number, gl.PullRequest.HTMLURL = "g/s/p", 3, "https://gitlab.example.com/g/s/p/-/merge_requests/3"
	if u := writeReport(gl, key, "", report.Data{Repo: "g/s/p", Number: 3, SHA: sha(1)}); u != ReportURL+"/r/gitlab.example.com/g/s/p/3/"+key {
		t.Fatal("without the repo's id, by its name:", u)
	}
	gl.Repository.ID = 9
	if u := writeReport(gl, key, "", report.Data{Repo: "g/s/p", Number: 3, SHA: sha(1)}); u != ReportURL+"/r/gitlab.example.com/id:9/3/"+key {
		t.Fatal(u)
	}
}

// A PR's reports from before the repo's ID move to its ID's folder on its next report, history and all, from the folder
// its walkthrough links under a name the repo had then; the old link redirects. A link edited to another PR moves none.
func TestAdoptNamedReports(t *testing.T) {
	proc.DataDir = t.TempDir()
	e := &forge.Event{}
	e.Repository.ID, e.Repository.FullName, e.PullRequest.Number, e.PullRequest.HTMLURL = 5, "o/new", 7, "https://github.com/o/new/pull/7"
	sha := func(i int) string { return fmt.Sprintf("%040x", i) }
	key := reportSecret("")
	old, other := "github.com/o/old/7/"+key, "github.com/o/old/8/"+key // renamed since; another PR's number
	for _, dir := range []string{old, other} {
		Reports().Put(dir+"/"+sha(1)+".html.gz", []byte("one"))
		Reports().Put(dir+"/"+sha(2)+".html", []byte("two")) // from before .html.gz
		Reports().Put(dir+"/versions.json", fmt.Appendf(nil, `[{"sha":"%s","at":"2026-01-02T00:00:00Z"},{"sha":"%s","at":"2026-01-01T00:00:00Z"}]`, sha(2), sha(1)))
	}
	dir := reportPath(e) + "/" + key
	if u := writeReport(e, key, ReportURL+"/r/"+old, report.Data{SHA: sha(3)}); u != ReportURL+"/r/"+dir {
		t.Fatal(u)
	}
	vs, err := Versions(dir)
	if err != nil || len(vs) != 3 || vs[0].SHA != sha(3) || vs[1].SHA != sha(2) || vs[2].SHA != sha(1) {
		t.Fatalf("%+v %v", vs, err)
	}
	if b, err := Reports().Get(dir + "/" + sha(2) + ".html"); string(b) != "two" {
		t.Fatal("page not moved:", err)
	}
	if vs, _ := Versions(old); len(vs) != 0 || Moved(old) != dir {
		t.Fatal("old folder:", vs, Moved(old))
	}
	if _, err := Reports().Get(old + "/" + sha(1) + ".html.gz"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("old page stays:", err)
	}
	writeReport(e, key, ReportURL+"/r/"+other, report.Data{SHA: sha(4)})
	if vs, _ := Versions(other); len(vs) != 2 || Moved(other) != "" {
		t.Fatal("moved another PR's reports")
	}
	if err := DeleteReports(e); err != nil {
		t.Fatal(err)
	}
	if vs, _ := Versions(dir); len(vs) != 0 {
		t.Fatal("the close kept the moved reports")
	}
}

// A move cut short, old's list still there, finishes on the next report without listing a version twice or one without
// its page; a walkthrough link from another REPORT_URL still finds old; an .html page from before goes past maxVersions.
func TestAdoptRetries(t *testing.T) {
	proc.DataDir = t.TempDir()
	e := &forge.Event{}
	e.Repository.ID, e.Repository.FullName, e.PullRequest.Number, e.PullRequest.HTMLURL = 5, "o/new", 7, "https://github.com/o/new/pull/7"
	sha := func(i int) string { return fmt.Sprintf("%040x", i) }
	key := reportSecret("")
	old, dir := "github.com/o/old/7/"+key, reportPath(e)+"/"+key
	list := fmt.Appendf(nil, `[{"sha":"%s","at":"2026-01-02T00:00:00Z"},{"sha":"%s","at":"2026-01-01T00:00:00Z"},`+
		`{"sha":"%s","at":"2025-12-01T00:00:00Z"}]`, sha(2), sha(1), sha(0)) // sha(0): its page gone
	Reports().Put(old+"/"+sha(1)+".html.gz", []byte("one"))
	Reports().Put(old+"/"+sha(2)+".html", []byte("two"))
	Reports().Put(old+"/versions.json", list)
	link := "https://before.example/r/" + old
	writeReport(e, key, link, report.Data{SHA: sha(3)})
	Reports().Put(old+"/versions.json", list) // cut short: one page and the list left
	Reports().Put(old+"/"+sha(1)+".html.gz", []byte("one"))
	writeReport(e, key, link, report.Data{SHA: sha(4)})
	vs, err := Versions(dir)
	if err != nil || len(vs) != 4 || vs[2].SHA != sha(2) || vs[3].SHA != sha(1) {
		t.Fatalf("%+v %v", vs, err)
	}
	if v, _ := Versions(old); len(v) != 0 || Moved(old) != dir {
		t.Fatal("not finished:", v)
	}
	for i := range maxVersions {
		writeReport(e, key, "", report.Data{SHA: sha(10 + i)})
	}
	if _, err := Reports().Get(dir + "/" + sha(2) + ".html"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("kept an .html page past maxVersions:", err)
	}
}

// A move whose list or moved file can't be written deletes none of old's pages: its links still serve, the walkthrough
// keeps its link, and the next report moves them all.
func TestAdoptPublishFails(t *testing.T) {
	proc.DataDir = t.TempDir()
	e := &forge.Event{}
	e.Repository.ID, e.Repository.FullName, e.PullRequest.Number, e.PullRequest.HTMLURL = 5, "o/r", 7, "https://github.com/o/r/pull/7"
	sha := func(i int) string { return fmt.Sprintf("%040x", i) }
	key := reportSecret("")
	old, dir := "github.com/o/r/7/"+key, reportPath(e)+"/"+key
	Reports().Put(old+"/"+sha(1)+".html.gz", []byte("one"))
	Reports().Put(old+"/versions.json", fmt.Appendf(nil, `[{"sha":"%s","at":"2026-01-01T00:00:00Z"}]`, sha(1)))
	link := ReportURL + "/r/" + old
	for _, block := range []string{dir + "/versions.json", old + "/moved"} { // a folder in the way
		Reports().Put(block+"/x", nil)
		if u := writeReport(e, key, link, report.Data{SHA: sha(2)}); u != link {
			t.Fatal(block, u)
		}
		if b, err := Reports().Get(old + "/" + sha(1) + ".html.gz"); string(b) != "one" {
			t.Fatal(block, "deleted old's page:", err)
		}
		Reports().DeleteAll(block)
	}
	writeReport(e, key, link, report.Data{SHA: sha(2)})
	if vs, err := Versions(dir); err != nil || len(vs) != 2 || vs[1].SHA != sha(1) || Moved(old) != dir {
		t.Fatalf("%+v %v", vs, err)
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
func (*reactPR) CanWrite(string) (bool, error)         { return false, nil }

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

func TestOtherMention(t *testing.T) {
	for body, want := range map[string]bool{
		"That is true. @BrudyR, what do you think?": true,
		"@john.doe-2 ok?":                     true,
		"What do you think (@alice)?":         true,
		"Fixed in abc123.":                    false,
		"mail me at a@b.com":                  false,
		"use `@Override` here":                false,
		"Why does `@Override` fail here?":     false,
		"```java\n@Override\nvoid run()\n```": false,
	} {
		if got := mentionsOther(body); got != want {
			t.Errorf("%q: got %v, want %v", body, got, want)
		}
	}
}

func TestNewer(t *testing.T) {
	bot := regexp.MustCompile(`@bot\b`)
	ours := []forge.Note{{ID: 1, Login: "bot", Ours: true}, {ID: 2, Login: "ann", Body: "why?"}, {ID: 3, Login: "bob", Body: "hm"},
		{ID: 4, Login: "ann", Body: "and line 9?"}, {ID: 5, Login: "ann", Body: "@bob what do you think?"}, {ID: 6, Login: "ann", Body: "thanks"}}
	humans := []forge.Note{{ID: 1, Login: "bob"}, {ID: 2, Login: "ann", Body: "@bot why?"}, {ID: 3, Login: "ann", Body: "also line 9?"}}
	capped := []forge.Note{{ID: 1, Login: "bot", Ours: true}, {ID: 2, Login: "ann", Body: "why?"},
		{ID: 3, Ours: true}, {ID: 4, Ours: true}, {ID: 5, Ours: true}, {ID: 6, Login: "ann", Body: "and now?"}}
	edited := []forge.Note{{ID: 1, Login: "bot", Ours: true}, {ID: 2, Login: "ann", Body: "why?"}, {ID: 3, Login: "ann", Body: "also why?", Edited: true}}
	for _, c := range []struct {
		d     forge.Discussion
		id    int64
		login string
		maint bool
		want  bool
	}{
		{forge.Discussion{Ours: true, Notes: ours}, 2, "ann", false, true},  // asked again past bob's comment: that one answers both
		{forge.Discussion{Ours: true, Notes: ours}, 4, "ann", false, false}, // later only a question for bob and thanks: chat answers neither, so this one
		{forge.Discussion{Ours: true, Notes: ours}, 3, "bob", false, false},
		{forge.Discussion{Ours: true, Notes: ours}, 9, "ann", false, false},
		{forge.Discussion{Notes: humans}, 2, "ann", false, false},             // no @bot after: a human thread goes on between humans
		{forge.Discussion{Ours: true, Notes: capped}, 2, "ann", false, false}, // we said our piece: "and now?" goes unanswered
		{forge.Discussion{Ours: true, Notes: capped}, 2, "ann", true, true},   // unless a maintainer asks: the decision is theirs
		{forge.Discussion{Ours: true, Notes: edited}, 2, "ann", false, false}, // "thanks" edited into a question: its edit was never queued
	} {
		if got := newer(bot, c.d, c.id, c.login, c.maint); got != c.want {
			t.Errorf("%d %s: got %v", c.id, c.login, got)
		}
	}
}

func TestCommand(t *testing.T) {
	bot := regexp.MustCompile(`(?i)(^|[^\w.+-])@(bot)\.*([^\w.-]|$)`)
	for body, want := range map[string]string{"@bot review": "review", "(@bot) review": "review", "(@bot review)": "review)",
		"@bot, Review all!": "review all!", "@bot thanks": "thanks", "@bot forget foo.": "forget foo.", "@bot agreed?": "agreed?"} {
		if got := command(bot, body); got != want {
			t.Errorf("%q: got %q, want %q", body, got, want)
		}
	}
}

func TestParseAnswer(t *testing.T) {
	if _, ok := parseAnswer(`{"reply":"old","resolved":"invalid"}`); ok {
		t.Fatal("a type error accepted")
	}
	if a, ok := parseAnswer(`{}`); ok || a.Reply != nil {
		t.Fatalf("no reply accepted: %+v", a)
	}
	if a, ok := parseAnswer(`{"reply":""}`); !ok || *a.Reply != "" {
		t.Fatal("an empty reply is an answer")
	}
}

func TestChatLeavesQuestionsForOthers(t *testing.T) {
	pr := &reactPR{reacted: make(chan struct{}), release: make(chan struct{})}
	defer close(pr.release)
	e := &forge.Event{Kind: "pull_request_review_comment"}
	e.Comment.Body = "What do you think (@alice)?"
	if err := Chat(t.Context(), reactForge{pr: pr}, regexp.MustCompile(`@bot`), e); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pr.reacted:
		t.Fatal("Chat reacted to a question for @alice")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAck(t *testing.T) {
	for body, want := range map[string]bool{
		"thanks!": true, "agreed!": true, "Danke schön": true, "LGTM": true, "👍": true, "ok.": true,
		"ok, but why line 3?": false, "agreed?": false, "done": false, "fixed in abc123": false, "thanks, but this is wrong": false,
	} {
		if got := ack.MatchString(body); got != want {
			t.Errorf("%q: got %v, want %v", body, got, want)
		}
	}
}

func TestThanks(t *testing.T) {
	bot := regexp.MustCompile(`(?i)(^|[^\w.+-])@(bot)\.*([^\w.-]|$)`)
	for body, want := range map[string]bool{"@bot thanks!": true, "agreed @bot": true, "@bot agreed?": false, "agreed @bot?": false} {
		if got := thanks(bot, body); got != want {
			t.Errorf("%q: got %v, want %v", body, got, want)
		}
	}
}

func TestReviewCommand(t *testing.T) {
	type got struct {
		large   bool
		profile string
		ok      bool
	}
	for cmd, want := range map[string]got{
		"review": {ok: true}, "full review": {ok: true}, "review all": {true, "", true},
		"review quiet": {false, "quiet", true}, "review all assertive": {true, "assertive", true}, "review chill all": {true, "chill", true},
		"review loud": {}, "reviewer?": {}, "what is a review": {}, "reviews": {},
		"review: quiet": {false, "quiet", true}, "review, all, chill": {true, "chill", true}, "review\tquiet": {false, "quiet", true},
		"review quiet chill": {}, "review quiet quiet": {}, // a second profile: no command, a question
	} {
		large, profile, ok := reviewCommand(cmd)
		if g := (got{large, profile, ok}); g != want {
			t.Errorf("%q: %+v, want %+v", cmd, g, want)
		}
	}
}

// Only folded findings: an incremental review posts nothing, they'd come back on every push. A full one always posts.
// A finding on a thread's line counts as none: Split drops it.
func TestPosts(t *testing.T) {
	nits := []review.Comment{{Path: "a.go", Line: 1, Severity: "nit", Fold: true}}
	minor := review.Comment{Path: "a.go", Line: 2, Severity: "minor"}
	on := []forge.Thread{{Path: "a.go", Line: 2}}
	r := func(cs ...review.Comment) review.Result { return review.Result{Comments: cs} }
	if posts(false, r(nits...), nil) || posts(false, r(), nil) || !posts(true, r(), nil) || !posts(false, r(nits[0], minor), nil) ||
		posts(false, r(nits[0], minor), on) {
		t.Fatal("posts")
	}
}

// The profile a review gets: the comment's over the description's over review.json's. A draft with drafts off
// stops the review right after, before any checkout.
func TestReviewProfile(t *testing.T) {
	inline := func(cfg review.Settings) string { // what the profile puts inline
		switch {
		case !cfg.Fold(review.Comment{Severity: "nit"}):
			return "assertive"
		case cfg.Fold(review.Comment{Severity: "minor"}):
			return "quiet"
		}
		return "chill"
	}
	for _, c := range []struct{ cfg, cmd, body, want string }{
		{`{"drafts":"off"}`, "", "", "chill"},
		{`{"drafts":"off","profile":"quiet"}`, "", "", "quiet"},
		{`{"drafts":"off","profile":"quiet"}`, "", "- [x] review: assertive", "assertive"},
		{`{"drafts":"off","profile":"quiet"}`, "chill", "review: assertive", "chill"},
	} {
		pr := &skipPR{cfg: c.cfg}
		e := &forge.Event{Action: "manual"}
		e.PullRequest.State, e.PullRequest.Draft, e.PullRequest.Body, e.PullRequest.Head.SHA = "open", true, c.body, strings.Repeat("a", 40)
		if err := Review(t.Context(), skipForge{pr: pr}, e, c.cmd); err != nil || inline(pr.settings) != c.want {
			t.Errorf("%+v: %v %s", c, err, inline(pr.settings))
		}
	}
}

// `@bot review all quiet` reviews in parts with that profile.
func TestChatReviewProfile(t *testing.T) {
	pr := &skipPR{cfg: `{"drafts":"off"}`}
	e := &forge.Event{Kind: "issue_comment", Action: "created"}
	e.Comment.Body = "@bot review all quiet"
	e.PullRequest.State, e.PullRequest.Draft, e.PullRequest.Head.SHA = "open", true, strings.Repeat("a", 40)
	mention := regexp.MustCompile(`(?i)(^|[^\w.+-])@(bot)\.*([^\w.-]|$)`)
	if err := Chat(t.Context(), skipForge{pr: pr}, mention, e); err != nil || e.Action != "large" || pr.settings.Override != "quiet" ||
		!pr.settings.Fold(review.Comment{Severity: "minor"}) {
		t.Fatalf("%v %q %q", err, e.Action, pr.settings.Override)
	}
}

func TestBodyProfile(t *testing.T) {
	for body, want := range map[string]string{
		"Fixes x.\n\nreview: quiet":                    "quiet",
		"Review: Assertive\n":                          "assertive",
		"- [ ] review: quiet\n- [x] review: chill\n":   "chill",
		"* [X] review: assertive":                      "assertive",
		"- review: quiet":                              "quiet",
		"- [ ] review: quiet\n- [ ] review: chill":     "",
		"<!-- review: quiet | chill | assertive -->\n": "",
		"code review: quiet please":                    "", // not a line of its own
		"review: loud":                                 "",
		"review: quiet-ish":                            "",
		"1. [x] review: quiet\r\n2. [ ] review: chill": "quiet", // GitLab's numbered task list, CRLF
		"review: **assertive**":                        "assertive",
		"review: `quiet`.":                             "quiet",
		"```markdown\n- [x] review: chill\n```\n":      "", // the README's example, pasted
		"~~~\nreview: quiet\n~~~\nreview: chill":       "chill",
		"```\nreview: quiet":                           "", // unclosed: code up to the end
		"<!-- hint\nreview: quiet":                     "",
		"text\n\n    review: quiet":                    "", // indented: code
		"\treview: quiet":                              "",
		"    - [x] review: quiet":                      "",      // indented: code, list-shaped or not
		"- [ ] a\n    - [x] review: quiet":             "quiet", // a nested list item
		"- item\n\n      - [x] review: quiet":          "",      // code in a list item
		"- item\n\n  - [x] review: quiet":              "quiet",
		">     review: quiet":                          "", // code in a blockquote
		">\n> review: quiet":                           "quiet",
		"- ```\n  review: quiet\n  ```\nreview: chill": "chill", // a fence in a list item
		"```\n<!--\n```\nreview: quiet":                "quiet", // a comment marker in code is code
		"text\n    review: quiet":                      "quiet", // indented, but no blank line before: the paragraph goes on
		"````\n```\n````\nreview: chill":               "chill",
		"````\n```\nreview: quiet\n```\n````":          "", // a fence inside a longer one
		"review: quiet please, but assertive here":     "",
		"review: quiet  ":                              "quiet",
	} {
		if got := bodyProfile(body); got != want {
			t.Errorf("%q: %q, want %q", body, got, want)
		}
	}
}

// A failed write-access check drops no reply, also past the cap: nothing retries a chat job.
type deniedPR struct{ skipPR }

func (*deniedPR) CanWrite(string) (bool, error) { return false, errors.New("503") }
func (*deniedPR) React()                        {}
func (*deniedPR) Discussion() (forge.Discussion, error) {
	return forge.Discussion{Ours: true, Notes: []forge.Note{{ID: 1, Ours: true}, {ID: 2, Ours: true}, {ID: 3, Ours: true}, {ID: 4, Ours: true}}}, nil
}

type deniedForge struct {
	skipForge
	pr *deniedPR
}

func (f deniedForge) Open(*forge.Event) (forge.PR, error) { return f.pr, nil }

func TestChatAnswersOnFailedAccessCheck(t *testing.T) {
	e := &forge.Event{Kind: "pull_request_review_comment"} // no repository id: Chat stops right after the thread check
	e.Comment.Body = "why?"
	if err := Chat(t.Context(), deniedForge{pr: &deniedPR{}}, regexp.MustCompile(`@bot`), e); err == nil || !strings.Contains(err.Error(), "no repository id") {
		t.Fatal("Chat stopped elsewhere:", err)
	}
}
