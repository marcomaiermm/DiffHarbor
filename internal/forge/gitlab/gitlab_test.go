package gitlab

import (
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"diffharbor/internal/review"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func serve(t *testing.T, h http.HandlerFunc) {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	APIURL, Bot = srv.URL, "review-bot"
}

func TestParse(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects/5/members/all/7":
			fmt.Fprint(w, `{"access_level":30}`)
		case "/projects/5/merge_requests/3/discussions/d1":
			fmt.Fprint(w, `{"notes":[{"id":1,"body":"finding"},{"id":2,"body":"why?"}]}`)
		default:
			http.NotFound(w, r)
		}
	})
	h := &Hub{"s"}
	hd := http.Header{"X-Gitlab-Token": {"s"}}
	if _, err := h.Parse(http.Header{"X-Gitlab-Token": {"x"}}, []byte(`{}`)); !errors.Is(err, forge.ErrSignature) {
		t.Fatal("token not checked")
	}
	push := `{"object_kind":"merge_request","project":{"id":5,"path_with_namespace":"g/s/p","git_http_url":"https://gl/g/s/p.git","default_branch":"main"},
"object_attributes":{"iid":3,"state":"opened","action":"update","oldrev":"a","last_commit":{"id":"abc"},"source_branch":"f","target_branch":"main","source_project_id":5}}`
	e, err := h.Parse(hd, []byte(push))
	if err != nil || e.Kind != "pull_request" || e.Action != "synchronize" || e.Key() != "gitlab/g/s/p#3" || e.PullRequest.Head.SHA != "abc" ||
		e.PullRequest.State != "open" || e.PullRequest.Base.Ref != "main" || e.Repository.DefaultBranch != "main" || e.Owner() != "gitlab/g" {
		t.Fatalf("push: %+v %v", e, err)
	}
	if e, _ := h.Parse(hd, []byte(strings.Replace(push, `"oldrev":"a"`, `"title":"x"`, 1))); e != nil {
		t.Fatal("a title edit is no push")
	}
	// with a push too: a full review against the new base, not an incremental one
	moved := strings.Replace(push, `"object_attributes"`, `"changes":{"target_branch":{"previous":"f0","current":"main"}},"object_attributes"`, 1)
	for _, retarget := range []string{strings.Replace(moved, `"oldrev":"a"`, `"title":"x"`, 1), moved} {
		if e, err := h.Parse(hd, []byte(retarget)); err != nil || e.Action != "edited" || e.Changes.Base.Ref.From != "f0" {
			t.Fatalf("retarget: %+v %v", e, err)
		}
	}
	reply := `{"object_kind":"note","user":{"id":7,"username":"dev"},"project":{"id":5,"path_with_namespace":"g/s/p"},"merge_request":{"iid":3},
"object_attributes":{"id":2,"note":"why?","noteable_type":"MergeRequest","type":"DiffNote","discussion_id":"d1","position":{"new_path":"a.go"}}}`
	e, err = h.Parse(hd, []byte(reply))
	c := e.Comment
	if err != nil || e.Kind != "pull_request_review_comment" || c.InReplyTo != 1 || c.AuthorAssociation != "COLLABORATOR" || c.Path != "a.go" || c.Ours {
		t.Fatalf("reply: %+v %v", e, err)
	}
	top := strings.NewReplacer(`"type":"DiffNote"`, `"type":null`, `"why?"`, `"@Review-Bot review"`).Replace(reply)
	if e, err = h.Parse(hd, []byte(top)); err != nil || e.Kind != "issue_comment" || e.Issue.Number != 3 || e.Issue.PullRequest == nil {
		t.Fatalf("mention: %+v %v", e, err)
	}
}

func TestOlds(t *testing.T) {
	m := olds("diff --git a/x.go b/y.go\n--- a/x.go\n+++ b/y.go\n@@ -4,3 +4,3 @@\n ctx\n-old\n+new\n ctx2\n" +
		"diff --git a/n.go b/n.go\n--- /dev/null\n+++ b/n.go\n@@ -0,0 +1 @@\n+n\n")
	if fmt.Sprint(m) != "map[n.go:1:{n.go 0} y.go:4:{x.go 4} y.go:5:{x.go 0} y.go:6:{x.go 6}]" {
		t.Fatal(m)
	}
}

// fakeMR is merge request 3's notes API: what a review sends, and what GitLab makes of it.
type fakeMR struct {
	t           *testing.T
	stale       string         // a draft there before, its note
	paged       bool           // the drafts listed with a next page
	drop        func(int) bool // the drafts GitLab doesn't place, by index
	reject      bool           // a finding's draft refused (4xx)
	fail        bool           // a finding's draft fails (5xx)
	failSummary bool           // the summary's draft fails
	failEdit    bool           // editing a draft fails
	lostEdit    bool           // editing a draft goes through, its answer fails (a timeout)
	failThreads bool           // the discussions don't read after publishing
	failAnswer  bool           // an answer's draft fails
	failPublish bool           // bulk_publish fails
	failNewest  bool           // the newest note doesn't read
	drafts      []map[string]any
	discussions []map[string]any
	notes       []string // posted on their own
	published   int
	states      []any // bulk_publish's reviewer_state
	edited      string
	deleted     bool
	answered    []string // threads answered and resolved on their own
}

// summary is the published summary draft, "" if none.
func (f *fakeMR) summary() string {
	if f.published == 0 {
		return ""
	}
	for _, d := range f.drafts {
		if d["position"] == nil && d["in_reply_to_discussion_id"] == nil {
			return d["note"].(string)
		}
	}
	return ""
}

func (f *fakeMR) serve() *pr {
	f.t.Helper()
	serve(f.t, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bot := map[string]string{"username": "review-bot"}
		out := func(v any) { json.NewEncoder(w).Encode(v) }
		mr := "/projects/5/merge_requests/3"
		if id, ok := strings.CutPrefix(r.URL.Path, mr+"/draft_notes/"); ok && r.Method == "PUT" { // a draft's id: its index + 1
			if !f.failEdit {
				n, _ := strconv.Atoi(id)
				f.drafts[n-1]["note"] = b["note"]
			}
			if f.failEdit || f.lostEdit {
				w.WriteHeader(500)
			}
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET " + mr + "/draft_notes":
			if f.paged {
				w.Header().Set("X-Next-Page", "2")
			}
			if f.stale != "" {
				out([]map[string]any{{"id": 4, "note": f.stale}})
			} else {
				out([]any{})
			}
		case "DELETE " + mr + "/draft_notes/4":
			f.deleted = true
		case "POST " + mr + "/draft_notes":
			switch {
			case b["position"] != nil && f.reject:
				w.WriteHeader(400)
			case b["position"] != nil && f.fail, b["in_reply_to_discussion_id"] != nil && f.failAnswer,
				b["position"] == nil && b["in_reply_to_discussion_id"] == nil && f.failSummary:
				w.WriteHeader(500)
			default:
				f.drafts = append(f.drafts, b)
				out(map[string]any{"id": len(f.drafts)})
			}
		case "POST " + mr + "/draft_notes/bulk_publish":
			if f.failPublish {
				w.WriteHeader(500)
				return
			}
			f.published++
			f.states = append(f.states, b["reviewer_state"])
		case "POST " + mr + "/discussions":
			f.discussions = append(f.discussions, b)
		case "GET " + mr + "/discussions":
			if f.failThreads {
				w.WriteHeader(500)
				return
			}
			var ds []map[string]any
			for i, d := range f.drafts {
				if d["position"] == nil {
					continue
				}
				if ds == nil { // an older thread of ours with the body of the first finding, on its line: not this review's
					ds = append(ds, map[string]any{"id": "d0", "notes": []map[string]any{{"id": 2, "body": d["note"], "author": bot, "position": d["position"]}}})
				}
				if f.drop == nil || !f.drop(i) {
					ds = append(ds, map[string]any{"id": fmt.Sprint("n", i), "notes": []map[string]any{{"id": 20 + i, "body": d["note"], "author": bot, "position": d["position"]}}})
				}
			}
			out(ds)
		case "GET " + mr + "/notes":
			if f.failNewest && r.URL.Query().Get("per_page") == "1" {
				w.WriteHeader(500)
				return
			}
			ns := []map[string]any{{"id": 1, "body": "before", "author": bot}}
			if s := f.summary(); s != "" && r.URL.Query().Get("per_page") != "1" {
				ns = append([]map[string]any{{"id": 9, "body": s, "author": bot}}, ns...)
			}
			out(ns)
		case "POST " + mr + "/notes":
			f.notes = append(f.notes, b["body"].(string))
			out(map[string]any{"id": 10})
		case "POST " + mr + "/discussions/d7/notes":
			f.answered = append(f.answered, "d7")
		case "PUT " + mr + "/discussions/d7":
		case "PUT " + mr + "/notes/9":
			f.edited = b["body"].(string)
		default:
			f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	e := &forge.Event{Forge: "gitlab"}
	e.Repository.ID, e.PullRequest.Number = 5, 3
	o, _ := (&Hub{}).Open(e)
	p := o.(*pr)
	p.refs = [3]string{"b", "s", "abcdef1"}
	return p
}

const postDiff = "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,3 +10,4 @@\n ctx\n-old\n+\tif x {\n+\t\treturn\n ctx2\n"

var (
	postFix = review.Comment{Path: "a.go", Line: 11, Severity: "major", Body: "fix body", Existing: "\tif x {\n\t\treturn\n", Fix: "\tif x != nil {\n\t\treturn\n"}
	postCtx = review.Comment{Path: "a.go", Line: 13, Body: "context line"}
)

// One review: the stale draft dropped; the answer, the summary and the findings drafted, the summary before the
// findings so it heads them, published once. GitLab doesn't place the first finding: an older thread with its body
// on its line doesn't count, the summary gets it as plain code.
func TestPost(t *testing.T) {
	f := &fakeMR{t: t, stale: "old " + forge.Mark, drop: func(i int) bool { return i == 2 }}
	p := f.serve()
	p.Resolve(forge.Thread{ID: "d7"}, "abcdef1")
	n, err := p.Post("abcdef1", review.Result{Comments: []review.Comment{postFix, postCtx}}, []forge.Thread{{ID: "d0"}}, postDiff, "f")
	pos := func(i int) map[string]any { return f.drafts[i]["position"].(map[string]any) }
	if err != nil || n != 2 || !f.deleted || len(f.drafts) != 4 || f.published != 1 || f.states[0] != "reviewed" || f.notes != nil ||
		f.drafts[0]["in_reply_to_discussion_id"] != "d7" || f.drafts[0]["resolve_discussion"] != true ||
		f.drafts[1]["note"] != f.summary() || !strings.Contains(f.drafts[2]["note"].(string), "```suggestion:-1+0\n") ||
		pos(2)["new_line"] != 12.0 || pos(3)["old_line"] != 3.0 || pos(3)["head_sha"] != "abcdef1" ||
		!strings.Contains(f.summary(), "Actionable comments posted: 2") ||
		!strings.Contains(f.edited, "Comments not posted inline (1)") || strings.Contains(f.edited, "Outside diff") || !strings.Contains(f.edited, "fix body") ||
		!strings.Contains(f.edited, "Suggested fix") || strings.Contains(f.edited, "context line") {
		t.Fatalf("post: %d %v %v %v\n%s", n, err, f.drafts, f.notes, f.edited)
	}
}

// No summary draft: the summary comes as a note of its own. Unread threads: it repeats the findings.
func TestPostNoSummaryDraft(t *testing.T) {
	f := &fakeMR{t: t, failSummary: true, failThreads: true}
	p := f.serve()
	n, err := p.Post("abcdef1", review.Result{Comments: []review.Comment{postCtx}}, nil, postDiff, "f")
	if err != nil || n != 1 || f.published != 1 || len(f.notes) != 1 || f.edited != "" ||
		!strings.Contains(f.notes[0], "Comments not posted inline (1)") || !strings.Contains(f.notes[0], "context line") {
		t.Fatalf("post: %d %v %v", n, err, f.notes)
	}
}

// Every finding's draft refused (4xx): the summary keeps them, still one review.
func TestPostRefused(t *testing.T) {
	f := &fakeMR{t: t, reject: true}
	p := f.serve()
	n, err := p.Post("abcdef1", review.Result{Comments: []review.Comment{postCtx, postFix}}, nil, postDiff, "f")
	if err != nil || n != 2 || f.published != 1 || f.notes != nil || f.edited != "" ||
		!strings.Contains(f.summary(), "Comments not posted inline (2)") || strings.Contains(f.summary(), "Outside diff") || !strings.Contains(f.summary(), "context line") {
		t.Fatalf("post: %d %v %v\n%s", n, err, f.notes, f.summary())
	}
}

// Refused findings and the summary's draft not updated: it goes out as drafted, then the note gets them.
func TestPostSummaryNotUpdated(t *testing.T) {
	f := &fakeMR{t: t, reject: true, failEdit: true}
	p := f.serve()
	n, err := p.Post("abcdef1", review.Result{Comments: []review.Comment{postCtx, postFix}}, nil, postDiff, "f")
	if err != nil || n != 2 || f.published != 1 || f.notes != nil || strings.Contains(f.summary(), "not posted inline") ||
		!strings.Contains(f.edited, "Comments not posted inline (2)") {
		t.Fatalf("post: %d %v %v\n%s", n, err, f.notes, f.edited)
	}
}

// The summary's draft updated though GitLab's answer failed, and a finding dropped: the published summary, as
// updated, is edited, not a second one posted.
func TestPostSummaryUpdateLost(t *testing.T) {
	f := &fakeMR{t: t, fail: true, lostEdit: true}
	p := f.serve()
	n, err := p.Post("abcdef1", review.Result{Comments: []review.Comment{postCtx, postFix}}, nil, postDiff, "f")
	if err != nil || n != 2 || f.published != 1 || f.notes != nil || !strings.Contains(f.edited, "Comments not posted inline (2)") {
		t.Fatalf("post: %d %v %v\n%s", n, err, f.notes, f.edited)
	}
}

// A finding's draft failing (5xx) may have drafted it: the rest goes into the summary, which takes it too once
// no discussion of it shows.
func TestPostDraftFails(t *testing.T) {
	f := &fakeMR{t: t, fail: true}
	p := f.serve()
	n, err := p.Post("abcdef1", review.Result{Comments: []review.Comment{postCtx, postFix}}, nil, postDiff, "f")
	if err != nil || n != 2 || f.published != 1 || f.notes != nil ||
		!strings.Contains(f.edited, "Comments not posted inline (2)") || strings.Contains(f.edited, "Outside diff") || !strings.Contains(f.edited, "context line") {
		t.Fatalf("post: %d %v %v\n%s", n, err, f.notes, f.edited)
	}
}

// The newest note unread before publishing: a summary GitLab didn't place a finding of goes as a new note, not
// over an older one with the same text.
func TestPostNewestUnknown(t *testing.T) {
	f := &fakeMR{t: t, failNewest: true, drop: func(int) bool { return true }}
	p := f.serve()
	n, err := p.Post("abcdef1", review.Result{Comments: []review.Comment{postCtx}}, []forge.Thread{{ID: "d0"}}, postDiff, "f")
	if err != nil || n != 1 || f.edited != "" || len(f.notes) != 1 || !strings.Contains(f.notes[0], "context line") {
		t.Fatalf("post: %d %v %v %q", n, err, f.notes, f.edited)
	}
}

// An answer's draft or the publishing failing: the thread is answered on its own.
func TestPublishFails(t *testing.T) {
	for _, f := range []*fakeMR{{t: t, failAnswer: true}, {t: t, failPublish: true}} {
		p := f.serve()
		p.Resolve(forge.Thread{ID: "d7"}, "abcdef1")
		p.Publish()
		if len(f.answered) != 1 || f.published != 0 {
			t.Fatalf("answered %v, published %d", f.answered, f.published)
		}
	}
}

// A person's draft (a personal token), one quoting ours too, stays and would be published: discussions instead,
// answers too. So do drafts on more than one page: one past the first could be a person's.
func TestPostPersonsDraft(t *testing.T) {
	for _, f := range []*fakeMR{{t: t, stale: "> " + forge.Mark + "\n\nmine"}, {t: t, stale: "old " + forge.Mark, paged: true}} {
		p := f.serve()
		p.Resolve(forge.Thread{ID: "d7"}, "abcdef1")
		p.Post("abcdef1", review.Result{Comments: []review.Comment{postCtx}}, nil, postDiff, "f")
		if f.deleted || f.drafts != nil || f.published != 0 || len(f.discussions) != 1 || len(f.answered) != 1 {
			t.Fatalf("drafts %v, published %d, discussions %v", f.drafts, f.published, f.discussions)
		}
	}
}

// Answers alone: Publish sends them, Post with no inline finding publishes them with the summary.
func TestPublishAnswers(t *testing.T) {
	f := &fakeMR{t: t}
	p := f.serve()
	p.Resolve(forge.Thread{ID: "d7"}, "abcdef1")
	p.Publish()
	p.Publish()
	if len(f.drafts) != 1 || f.published != 1 || f.states[0] != "reviewed" {
		t.Fatalf("drafts %v, published %d", f.drafts, f.published)
	}
	p.Resolve(forge.Thread{ID: "d8"}, "abcdef1")
	n, err := p.Post("abcdef1", review.Result{}, nil, postDiff, "f")
	if err != nil || n != 0 || len(f.drafts) != 3 || f.published != 2 || f.notes != nil ||
		!strings.Contains(f.drafts[2]["note"].(string), "No actionable comments") {
		t.Fatalf("post: %d %v %v %v", n, err, f.drafts, f.notes)
	}
}

// A review with nothing on diff lines, clean or all outside the diff: its summary is the review, the bot reviewed.
func TestPostSummaryOnly(t *testing.T) {
	for _, cs := range [][]review.Comment{nil, {{Path: "b.go", Line: 1, Severity: "major", Body: "elsewhere"}}} {
		f := &fakeMR{t: t}
		p := f.serve()
		n, err := p.Post("abcdef1", review.Result{Comments: cs}, nil, postDiff, "f")
		if err != nil || n != len(cs) || len(f.drafts) != 1 || f.published != 1 || f.states[0] != "reviewed" || f.notes != nil ||
			cs != nil && (!strings.Contains(f.summary(), "Outside diff range comments (1)") || strings.Contains(f.summary(), "not posted inline")) {
			t.Fatalf("post: %d %v %v %v", n, err, f.drafts, f.notes)
		}
	}
}

// gitlab_findings discussions: the note comes first. A suggestion GitLab refuses goes without it, a line it refuses
// into the note under its own heading, apart from the findings outside the diff.
func TestPostDiscussions(t *testing.T) {
	var discussions []map[string]any
	var summary string
	first := false
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		switch r.URL.Path {
		case "/projects/5/merge_requests/3/discussions":
			discussions = append(discussions, b)
			if len(discussions) != 2 {
				w.WriteHeader(400)
			}
		case "/projects/5/merge_requests/3/notes":
			summary, first = b["body"].(string), discussions == nil
		case "/projects/5/merge_requests/3/notes/9":
			summary = b["body"].(string)
		}
		fmt.Fprint(w, `{"id":9}`)
	})
	e := &forge.Event{Forge: "gitlab"}
	e.Repository.ID, e.PullRequest.Number = 5, 3
	f, _ := (&Hub{}).Open(e)
	p := f.(*pr)
	p.Configure(review.Settings{GitLabFindings: "discussions"})
	p.refs = [3]string{"b", "s", "abcdef1"}
	src := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,3 +10,4 @@\n ctx\n-old\n+\tif x {\n+\t\treturn\n ctx2\n"
	fix := review.Comment{Path: "a.go", Line: 11, Severity: "major", Body: "b", Existing: "\tif x {\n\t\treturn\n", Fix: "\tif x != nil {\n\t\treturn\n"}
	ctx := review.Comment{Path: "a.go", Line: 13, Body: "context line"}
	out := review.Comment{Path: "b.go", Line: 1, Body: "elsewhere"}
	n, err := p.Post("abcdef1", review.Result{Comments: []review.Comment{fix, ctx, out}}, nil, src, "f")
	pos := func(i int) map[string]any { return discussions[i]["position"].(map[string]any) }
	if err != nil || n != 3 || len(discussions) != 3 || !first ||
		!strings.Contains(discussions[0]["body"].(string), "```suggestion:-1+0\n") || pos(0)["new_line"] != 12.0 ||
		strings.Contains(discussions[1]["body"].(string), "suggestion") || pos(1)["new_line"] != 11.0 || pos(1)["old_line"] != nil ||
		pos(2)["old_line"] != 3.0 || pos(2)["head_sha"] != "abcdef1" ||
		!strings.Contains(summary, "Actionable comments posted: 3") || !strings.Contains(summary, "<summary><b>⚠️ Outside diff range comments (1)</b></summary>") ||
		!strings.Contains(summary, "<summary><b>💬 Comments not posted inline (1)</b></summary>") ||
		strings.Index(summary, "elsewhere") > strings.Index(summary, "Comments not posted inline") || !strings.Contains(summary, "context line") {
		t.Fatalf("post: %d %v %v\n%s", n, err, discussions, summary)
	}
}

// gitlab_findings discussions, a discussion GitLab fails: the note keeps the findings not posted.
func TestPostDiscussionsFails(t *testing.T) {
	var summary string
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		switch r.Method + " " + r.URL.Path {
		case "POST /projects/5/merge_requests/3/discussions":
			w.WriteHeader(500)
		case "POST /projects/5/merge_requests/3/notes", "PUT /projects/5/merge_requests/3/notes/9":
			summary = b["body"].(string)
			fmt.Fprint(w, `{"id":9}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	e := &forge.Event{Forge: "gitlab"}
	e.Repository.ID, e.PullRequest.Number = 5, 3
	f, _ := (&Hub{}).Open(e)
	p := f.(*pr)
	p.Configure(review.Settings{GitLabFindings: "discussions"})
	p.refs = [3]string{"b", "s", "abcdef1"}
	src := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n ctx\n+new\n"
	c := review.Comment{Path: "a.go", Line: 2, Body: "lost?"}
	if _, err := p.Post("abcdef1", review.Result{Comments: []review.Comment{c}}, nil, src, "f"); err == nil ||
		!strings.Contains(summary, "Comments not posted inline (1)") || strings.Contains(summary, "Outside diff") || !strings.Contains(summary, "lost?") {
		t.Fatalf("post: %v\n%s", err, summary)
	}
}

func TestCopyDir(t *testing.T) {
	files := map[string]string{
		".gitlab/review/sql.md":            "sql rules",
		".gitlab/review/go-style":          "../../.agents/skills/go-style",
		".gitlab/review/evil":              "../../../etc",
		".agents/skills/go-style/SKILL.md": "go style",
	}
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "base" {
			http.Error(w, "ref", 400)
			return
		}
		switch p := r.URL.Query().Get("path"); {
		case strings.HasSuffix(r.URL.Path, "/repository/tree") && p == ".gitlab/review":
			fmt.Fprint(w, `[{"path":".gitlab/review/sql.md","type":"blob","mode":"100644"},{"path":".gitlab/review/go-style","type":"blob","mode":"120000"},
{"path":".gitlab/review/evil","type":"blob","mode":"120000"}]`)
		case strings.HasSuffix(r.URL.Path, "/repository/tree") && p == ".agents/skills/go-style":
			fmt.Fprint(w, `[{"path":".agents/skills/go-style/SKILL.md","type":"blob","mode":"100644"}]`)
		case strings.HasSuffix(r.URL.Path, "/repository/tree"):
			fmt.Fprint(w, `[]`)
		default:
			f, ok := files[strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/projects/5/repository/files/"), "/raw")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, f)
		}
	})
	dst := t.TempDir()
	calls := 60
	copyDir("/projects/5", "base", ".gitlab/review", dst, &calls)
	for f, want := range map[string]string{"sql.md": "sql rules", "go-style/SKILL.md": "go style"} {
		if b, _ := os.ReadFile(filepath.Join(dst, f)); string(b) != want {
			t.Errorf("%s: %q", f, b)
		}
	}
	if proc.Exists(filepath.Join(dst, "evil")) {
		t.Fatal("escaped the repo")
	}
}

// Others skips this MR and takes the files from each MR's diffs, which Patches then reuses.
func TestOthers(t *testing.T) {
	calls := map[string]int{}
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		switch r.URL.Path {
		case "/projects/5/merge_requests":
			fmt.Fprint(w, `[{"iid":3},{"iid":4,"title":"t","web_url":"u","draft":true,"author":{"username":"ann"}}]`)
		case "/projects/5/merge_requests/4/diffs":
			fmt.Fprint(w, `[{"new_path":"b.go","diff":"@@ -1 +1 @@\n-x\n+y\n"},{"new_path":"a.go","diff":""}]`)
		}
	})
	e := &forge.Event{Forge: "gitlab"}
	e.Repository.ID, e.PullRequest.Number = 5, 3
	p, _ := (&Hub{}).Open(e)
	got, err := p.Others()
	if err != nil || len(got) != 1 || fmt.Sprint(got[0]) != "{4 t u ann true [a.go b.go] []}" {
		t.Fatalf("%v %+v", err, got)
	}
	ps, err := p.Patches(4)
	if err != nil || ps["b.go"] != "@@ -1 +1 @@\n-x\n+y\n" || calls["/projects/5/merge_requests/4/diffs"] != 1 || calls["/projects/5/merge_requests/3/diffs"] != 0 {
		t.Fatal(err, ps, calls)
	}
}

// No line of a body runs as a quick action, whatever markdown the model wraps it in. A fix with one goes as plain
// code, a suggestion is committed as written.
func TestInert(t *testing.T) {
	for _, in := range []string{
		"/merge", "see ```x := 1```\n```x := 1```\n/merge", "``` a`b\n/merge", "text\n    ```\n/merge", "```\n    ```\n```\n/merge",
		"```\n``` ```\n```\n/merge", "```\r\n/a\r\n```\r\n/merge\r\n", "\r/merge", "- a\n  ```\n/merge", "<br>\n```\n\n/merge",
	} {
		for _, l := range strings.Split(inert(in), "\n") {
			if command(l) {
				t.Errorf("%q: %q runs", in, l)
			}
		}
	}
	if got := inert("// go\n/* c */\nsee /close"); got != "// go\n/* c */\nsee /close" {
		t.Fatalf("%q", got)
	}
	var bodies []string
	serve(t, func(_ http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b["body"].(string))
	})
	e := &forge.Event{Forge: "gitlab"}
	e.Repository.ID, e.PullRequest.Number = 5, 3
	f, _ := (&Hub{}).Open(e)
	p := f.(*pr)
	for _, fix := range []string{"// keep\nx := 1", "x := 1\n/approve"} {
		c := forge.Finding{Comment: review.Comment{Path: "a.go", Line: 2, Body: "```x```\n/merge", Fix: fix}, Start: 1, End: 2}
		if err := p.discuss(c, nil); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(bodies[0], "```suggestion:-1+0\n// keep\nx := 1\n```") || strings.Contains(bodies[1], "suggestion") ||
		!strings.Contains(bodies[1], "\\/approve") || !strings.Contains(bodies[0], "\\/merge") {
		t.Fatalf("%q", bodies)
	}
}

// A folded finding on a diff line gets no draft of its own: the summary has it, folded, and it doesn't count.
func TestPostFolded(t *testing.T) {
	f := &fakeMR{t: t}
	p := f.serve()
	nit := review.Comment{Path: "a.go", Line: 13, Severity: "nit", Title: "name", Body: "b", Fold: true}
	n, err := p.Post("abcdef1", review.Result{Comments: []review.Comment{postFix, nit}}, nil, postDiff, "f")
	if err != nil || n != 1 || len(f.drafts) != 2 || !strings.Contains(f.summary(), "🧹 Nitpicks (1)") ||
		!strings.Contains(f.summary(), "Actionable comments posted: 1") {
		t.Fatalf("post: %d %v %v\n%s", n, err, f.drafts, f.summary())
	}
}

// A resolved thread keeps the maintainer's answer, so a declined point isn't reported again; system notes stay out.
func TestThreadsReplies(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":"d1","notes":[{"id":1,"body":"finding","resolved":true,"author":{"username":"review-bot"},"position":{"new_path":"a.ts","new_line":3}},`+
			`{"id":2,"body":"changed this line","system":true},{"id":3,"body":"won't split","author":{"username":"kev"}}]}]`)
	})
	e := &forge.Event{Forge: "gitlab"}
	e.Repository.ID, e.PullRequest.Number = 5, 3
	p, _ := (&Hub{}).Open(e)
	ts, err := p.Threads()
	if err != nil || len(ts) != 1 || !ts[0].Resolved || ts[0].Replies != "\n\n**@kev**: won't split" {
		t.Fatalf("%+v %v", ts, err)
	}
}

// Voted reads the award emoji of each finding but an addressed one, every page.
func TestVoted(t *testing.T) {
	Bot = "review-bot"
	var asked []string
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/discussions") {
			ok, _ := json.Marshal(forge.AddressedIn("abcdef1"))
			fmt.Fprint(w, `[{"id":"d1","notes":[{"id":1,"body":"f `+forge.Mark+`","resolved":true,"author":{"username":"review-bot"},"position":{"new_path":"a.ts","new_line":3}}]},`+
				`{"id":"d2","notes":[{"id":2,"body":"f `+forge.Mark+`","resolved":true,"author":{"username":"review-bot"},"position":{"new_path":"b.ts","new_line":3}},`+
				`{"id":4,"body":`+string(ok)+`,"author":{"username":"review-bot"}}]}]`)
			return
		}
		asked = append(asked, r.URL.Path)
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprint(w, `[{"name":"thumbsup","user":{"username":"bo"}},{"name":"heart","user":{"username":"di"}}]`)
			return
		}
		fmt.Fprint(w, `[{"name":"thumbsdown","user":{"username":"cy"}}]`)
	})
	e := &forge.Event{Forge: "gitlab"}
	e.Repository.ID, e.PullRequest.Number = 5, 3
	p, _ := (&Hub{}).Open(e)
	ts, err := p.Voted()
	if err != nil || len(ts) != 2 || !slices.Equal(ts[0].Up, []string{"bo"}) || !slices.Equal(ts[0].Down, []string{"cy"}) ||
		!ts[1].Addressed || len(asked) != 2 || !strings.HasSuffix(asked[0], "/notes/1/award_emoji") {
		t.Fatalf("%+v %q %v", ts, asked, err)
	}
}

func TestVisibility(t *testing.T) {
	h := &Hub{"s"}
	hd := http.Header{"X-Gitlab-Token": {"s"}}
	for vis, want := range map[string]bool{`"visibility_level":0,`: true, `"visibility":"private",`: true, `"visibility_level":10,`: false, `"visibility_level":20,`: false, ``: false} {
		body := `{"object_kind":"merge_request","project":{"id":5,` + vis + `"path_with_namespace":"g/p"},
"object_attributes":{"iid":3,"state":"opened","action":"open","source_branch":"f","target_branch":"main"}}`
		if e, err := h.Parse(hd, []byte(body)); err != nil || e.Private() != want {
			t.Errorf("%q: private %v, %v", vis, e != nil && e.Private(), err)
		}
	}
}

// Refresh reads the project as it is now: a webhook that said private doesn't keep it so.
func TestRefreshVisibility(t *testing.T) {
	for vis, want := range map[string]bool{`{"visibility":"private"}`: true, `{"visibility":"public"}`: false, `{"visibility":"internal"}`: false, ``: false} {
		serve(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/projects/5/merge_requests/3":
				fmt.Fprint(w, `{"iid":3,"state":"opened","author":{"id":7}}`)
			case "/users/7":
				fmt.Fprint(w, `{"bot":false}`)
			case "/projects/5":
				if vis == "" { // unavailable: not private
					http.Error(w, "boom", 500)
					return
				}
				fmt.Fprint(w, vis)
			default:
				http.NotFound(w, r)
			}
		})
		e := &forge.Event{Forge: "gitlab"}
		e.Repository.ID, e.PullRequest.Number, e.Repository.Visibility = 5, 3, "private" // as the webhook said
		p, _ := (&Hub{}).Open(e)
		if err := p.Refresh(); err != nil || e.Private() != want {
			t.Errorf("%q: private %v, %v", vis, e.Private(), err)
		}
	}
}

// A fixed finding back: answered in its thread with the finding, quick actions inert, then reopened. A failed
// answer posts nothing more.
func TestReopen(t *testing.T) {
	var sent []string
	fail := false
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		sent = append(sent, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+b["body"])
		if fail {
			w.WriteHeader(http.StatusBadGateway)
		}
		fmt.Fprint(w, `{}`)
	})
	e := &forge.Event{Forge: "gitlab"}
	e.Repository.ID, e.PullRequest.Number = 5, 3
	o, _ := (&Hub{}).Open(e)
	c := forge.Finding{Comment: review.Comment{Severity: "major", Title: "race", Body: "back\n/merge"}}
	if err := o.Reopen(forge.Thread{ID: "d7"}, "abcdef1", c); err != nil || len(sent) != 2 ||
		!strings.HasPrefix(sent[0], "POST /projects/5/merge_requests/3/discussions/d7/notes? 🔁 Back in `abcdef1`") || !strings.Contains(sent[0], "\n\\/merge") ||
		!strings.HasSuffix(sent[0], forge.Mark) || sent[1] != "PUT /projects/5/merge_requests/3/discussions/d7?resolved=false " {
		t.Fatalf("%q %v", sent, err)
	}
	sent, fail = nil, true
	if err := o.Reopen(forge.Thread{ID: "d7"}, "abcdef1", c); err == nil || len(sent) != 1 {
		t.Fatalf("%q %v", sent, err)
	}
}
