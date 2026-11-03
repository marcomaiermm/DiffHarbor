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
	push := `{"object_kind":"merge_request","project":{"id":5,"path_with_namespace":"g/s/p","git_http_url":"https://gl/g/s/p.git"},
"object_attributes":{"iid":3,"state":"opened","action":"update","oldrev":"a","last_commit":{"id":"abc"},"source_branch":"f","target_branch":"main","source_project_id":5}}`
	e, err := h.Parse(hd, []byte(push))
	if err != nil || e.Kind != "pull_request" || e.Action != "synchronize" || e.Key() != "gitlab/g/s/p#3" || e.PullRequest.Head.SHA != "abc" ||
		e.PullRequest.State != "open" || e.PullRequest.Base.Ref != "main" || e.Owner() != "gitlab/g" {
		t.Fatalf("push: %+v %v", e, err)
	}
	if e, _ := h.Parse(hd, []byte(strings.Replace(push, `"oldrev":"a"`, `"title":"x"`, 1))); e != nil {
		t.Fatal("a title edit is no push")
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

// A suggestion GitLab refuses goes without it, a line it refuses into the note.
func TestPost(t *testing.T) {
	var discussions []map[string]any
	var summary string
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
			summary = b["body"].(string)
		}
		fmt.Fprint(w, `{"id":9}`)
	})
	e := &forge.Event{Forge: "gitlab"}
	e.Repository.ID, e.PullRequest.Number = 5, 3
	f, _ := (&Hub{}).Open(e)
	p := f.(*pr)
	p.refs = [3]string{"b", "s", "abcdef1"}
	src := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,3 +10,4 @@\n ctx\n-old\n+\tif x {\n+\t\treturn\n ctx2\n"
	fix := review.Comment{Path: "a.go", Line: 11, Severity: "major", Body: "b", Existing: "\tif x {\n\t\treturn\n", Fix: "\tif x != nil {\n\t\treturn\n"}
	ctx := review.Comment{Path: "a.go", Line: 13, Body: "context line"}
	n, err := p.Post("abcdef1", review.Result{Comments: []review.Comment{fix, ctx}}, nil, src, "f")
	pos := func(i int) map[string]any { return discussions[i]["position"].(map[string]any) }
	if err != nil || n != 2 || len(discussions) != 3 ||
		!strings.Contains(discussions[0]["body"].(string), "```suggestion:-1+0\n") || pos(0)["new_line"] != 12.0 ||
		strings.Contains(discussions[1]["body"].(string), "suggestion") || pos(1)["new_line"] != 11.0 || pos(1)["old_line"] != nil ||
		pos(2)["old_line"] != 3.0 || pos(2)["head_sha"] != "abcdef1" ||
		!strings.Contains(summary, "Actionable comments posted: 2") || !strings.Contains(summary, "context line") {
		t.Fatalf("post: %d %v %v\n%s", n, err, discussions, summary)
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
