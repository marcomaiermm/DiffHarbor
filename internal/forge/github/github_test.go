package github

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"diffharbor/internal/review"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAppJWT(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	if AppKey, err = LoadKey(base64.StdEncoding.EncodeToString(p)); err != nil {
		t.Fatal(err)
	}
	AppID = "42"
	j, err := AppJWT()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(j, ".")
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if err := rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatal(err)
	}
	var claims struct{ Iss string }
	b, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if json.Unmarshal(b, &claims); claims.Iss != "42" {
		t.Fatalf("claims: %s", b)
	}
}

func TestCopyBase(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	files := map[string]string{
		".review/skills":                   `[{"name":"sql.md","path":".review/skills/sql.md","type":"file"},{"name":"go-style","path":".review/skills/go-style","type":"symlink"},{"name":"evil","path":".review/skills/evil","type":"symlink"},{"name":"loop","path":".review/skills/loop","type":"symlink"}]`,
		".review/skills/sql.md":            `{"type":"file","encoding":"base64","content":"` + b64("sql rules") + `"}`,
		".review/skills/go-style":          `{"type":"symlink","target":"../../.agents/skills/go-style"}`,
		".review/skills/evil":              `{"type":"symlink","target":"../../../etc"}`,
		".review/skills/loop":              `{"type":"symlink","target":"."}`,
		".agents/skills/go-style":          `[{"name":"SKILL.md","path":".agents/skills/go-style/SKILL.md","type":"file"}]`,
		".agents/skills/go-style/SKILL.md": `{"type":"file","encoding":"base64","content":"` + b64("go style") + `"}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "base" {
			http.Error(w, "ref", 400)
			return
		}
		f, ok := files[strings.TrimPrefix(r.URL.Path, "/repos/o/r/contents/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(f))
	}))
	defer srv.Close()
	APIURL = srv.URL
	dst := t.TempDir()
	calls := 60
	copyBase("/repos/o/r", "t", "?ref=base", ".review/skills", dst, &calls)
	for f, want := range map[string]string{"sql.md": "sql rules", "go-style/SKILL.md": "go style"} {
		if b, _ := os.ReadFile(filepath.Join(dst, f)); string(b) != want {
			t.Errorf("%s: %q", f, b)
		}
	}
	if proc.Exists(filepath.Join(dst, "evil")) || proc.Exists(filepath.Join(dst, "loop")) || calls != 53 {
		t.Fatalf("escaped the repo or looped: calls=%d", calls)
	}
}

func TestRedeliver(t *testing.T) {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	AppKey = k
	ago := func(h int) string { return time.Now().Add(-time.Duration(h) * time.Hour).Format(time.RFC3339) }
	var posted []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST":
			posted = append(posted, r.URL.Path)
		case r.URL.Query().Get("cursor") == "":
			w.Header().Set("Link", `<`+srv.URL+`/app/hook/deliveries?per_page=100&cursor=2>; rel="next"`)
			fmt.Fprintf(w, `[{"id":1,"guid":"a","status_code":202,"delivered_at":%q},{"id":2,"guid":"b","status_code":404,"delivered_at":%q}]`, ago(0), ago(1))
		default: // a: redelivered fine, c: failed but too old
			fmt.Fprintf(w, `[{"id":3,"guid":"a","status_code":404,"delivered_at":%q},{"id":4,"guid":"c","status_code":404,"delivered_at":%q}]`, ago(2), ago(7))
		}
	}))
	defer srv.Close()
	APIURL = srv.URL
	Redeliver()
	if !slices.Equal(posted, []string{"/app/hook/deliveries/2/attempts"}) {
		t.Fatalf("posted %v", posted)
	}
}

func TestAll(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", `<`+srv.URL+r.URL.Path+`?per_page=100&page=2>; rel="next"`)
			fmt.Fprint(w, `[1,2]`)
			return
		}
		fmt.Fprint(w, `[3]`)
	}))
	defer srv.Close()
	APIURL = srv.URL
	if got, err := All[int]("/repos/o/r/issues/1/comments?per_page=100", "t"); err != nil || !slices.Equal(got, []int{1, 2, 3}) {
		t.Fatal(got, err)
	}
}

func TestSuggestion(t *testing.T) {
	src := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,3 +10,4 @@\n ctx\n-old\n+\tif x {\n+\t\treturn\n ctx2\n"
	c := review.Comment{Path: "a.go", Line: 11, Severity: "major", Body: "b", Existing: "\tif x {\n\t\treturn  \n", Fix: "\tif x != nil {\n\t\treturn\n"}
	// GitHub rejects the suggestion range: the finding goes inline without it, before everything moves to the body.
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Comments []map[string]any }
		json.NewDecoder(r.Body).Decode(&b)
		sent = append(sent, b.Comments...)
		if len(sent) == 1 {
			w.WriteHeader(422)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	APIURL = srv.URL
	e := &forge.Event{}
	e.PullRequest.Number = 1
	p := &pr{e: e, api: "/repos/o/r", token: "t"}
	stamp := forge.Stamp(forge.Run{Version: "v1.0.0", Review: "lab/big", Verify: "lab/small", SHA: "abcdef1"})
	n, err := p.Post("abc", review.Result{Comments: []review.Comment{c}, Run: stamp}, nil, src, "f")
	if err != nil || len(sent) == 0 {
		t.Fatalf("post: %v %v", err, sent)
	}
	if body, _ := sent[len(sent)-1]["body"].(string); !strings.HasSuffix(body, stamp+forge.Mark) || forge.ParseRun(body).Review != "lab/big" {
		t.Fatalf("stamp: %q", body)
	}
	if err != nil || n != 1 || len(sent) != 2 || sent[0]["start_line"] != 11.0 || sent[0]["line"] != 12.0 ||
		sent[1]["start_line"] != nil || sent[1]["line"] != 11.0 || !strings.Contains(sent[1]["body"].(string), "Suggested fix") {
		t.Fatalf("post: %d %v %v", n, err, sent)
	}
}

// GitHub rejects a line: the finding on it goes into the body under its own heading, apart from the one outside
// the diff.
func TestRejectedLine(t *testing.T) {
	src := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n ctx\n+new\n"
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Body     string
			Comments []any
		}
		json.NewDecoder(r.Body).Decode(&b)
		if len(b.Comments) > 0 {
			w.WriteHeader(422)
		}
		body = b.Body
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	APIURL = srv.URL
	e := &forge.Event{}
	e.PullRequest.Number = 1
	p := &pr{e: e, api: "/repos/o/r", token: "t"}
	on, out := review.Comment{Path: "a.go", Line: 2, Body: "on the diff"}, review.Comment{Path: "b.go", Line: 1, Body: "elsewhere"}
	n, err := p.Post("abc", review.Result{Comments: []review.Comment{on, out}}, nil, src, "f")
	_, after, _ := strings.Cut(body, "Comments not posted inline (1)")
	if err != nil || n != 2 || !strings.Contains(body, "Actionable comments posted: 2") || !strings.Contains(body, "Outside diff range comments (1)") ||
		!strings.Contains(after, "on the diff") || strings.Contains(after, "elsewhere") {
		t.Fatalf("post: %d %v\n%s", n, err, body)
	}
}

func TestVerify(t *testing.T) {
	body := []byte(`{"a":1}`)
	m := hmac.New(sha256.New, []byte("s"))
	m.Write(body)
	sig := "sha256=" + hex.EncodeToString(m.Sum(nil))
	if !verify("s", body, sig) || verify("x", body, sig) || verify("s", body, "") || verify("s", body, "sha256=00") {
		t.Fatal("verify")
	}
}

func TestNestedRules(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/git/trees/base":
			fmt.Fprint(w, `{"tree":[{"path":"AGENTS.md","type":"blob"},{"path":"apps/web/CLAUDE.md","type":"blob"},{"path":"apps/web/AGENTS.md","type":"blob"},
{"path":"apps/api/AGENTS.md","type":"blob"},{"path":"apps/web/src/CLAUDE.md","type":"blob"}]}`)
		default:
			fmt.Fprint(w, "rules of "+strings.TrimPrefix(r.URL.Path, "/repos/o/r/contents/"))
		}
	}))
	defer srv.Close()
	APIURL = srv.URL
	got := NestedRules("/repos/o/r", "t", "base", []string{"apps/web/src/a.ts", "README.md"})
	if got != "\n\n### apps/web/AGENTS.md (for changes under apps/web/)\n\nrules of apps/web/AGENTS.md"+
		"\n\n### apps/web/src/CLAUDE.md (for changes under apps/web/src/)\n\nrules of apps/web/src/CLAUDE.md" {
		t.Fatalf("%q", got)
	}
}

// A thread is ours by its first comment's author: anyone can copy the mark into theirs.
func TestThreadsOurs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		node := func(id, typ, login string) string {
			return `{"id":"` + id + `","comments":{"nodes":[{"databaseId":1,"path":"a.go","line":1,"body":"x ` + forge.Mark +
				`","author":{"__typename":"` + typ + `","login":"` + login + `"}}]},"replies":{"nodes":[{"databaseId":1,"body":"x"},` +
				`{"databaseId":2,"body":"won't split","author":{"login":"kev"}}]}}`
		}
		fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[`+node("ours", "Bot", "app")+","+
			node("copy", "User", "app")+","+node("other", "Bot", "else")+`]}}}}}`)
	}))
	defer srv.Close()
	APIURL, Slug = srv.URL, "app"
	ts, err := Threads("o/r", 1, "t")
	if err != nil || len(ts) != 1 || ts[0].ID != "ours" || ts[0].Replies != "\n\n**@kev**: won't split" { // the first comment once
		t.Fatalf("threads %+v %v", ts, err)
	}
}

// A folded finding on a diff line goes into the review's body, not inline, and doesn't count as posted.
func TestPostFolded(t *testing.T) {
	src := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1,3 @@\n x\n+y\n+z\n"
	var sent struct {
		Body     string
		Comments []map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&sent)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	APIURL = srv.URL
	e := &forge.Event{}
	e.PullRequest.Number = 1
	p := &pr{e: e, api: "/repos/o/r", token: "t"}
	n, err := p.Post("abc", review.Result{Comments: []review.Comment{
		{Path: "a.go", Line: 2, Severity: "major", Title: "bug", Body: "b"},
		{Path: "a.go", Line: 3, Severity: "nit", Title: "name", Body: "b", Fold: true},
	}}, nil, src, "f")
	if err != nil || n != 1 || len(sent.Comments) != 1 || sent.Comments[0]["line"] != 2.0 || !strings.Contains(sent.Body, "🧹 Nitpick comments: 1") {
		t.Fatalf("post: %d %v %+v", n, err, sent)
	}
}

// A thread is addressed while our "✅ Addressed in" is its last reply: a person's reply after it, or the text in
// theirs (also a user's named like the app), makes it settled in the thread instead.
func TestThreadsAddressed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		node := func(id, replies string) string {
			return `{"id":"` + id + `","isResolved":true,"comments":{"nodes":[{"databaseId":1,"path":"a.go","line":1,"body":"x ` + forge.Mark +
				`","author":{"__typename":"Bot","login":"app"}}]},"replies":{"nodes":[` + replies + `]}}`
		}
		ok, _ := json.Marshal(forge.AddressedIn("abcdef1"))
		ours := `{"databaseId":2,"body":` + string(ok) + `,"author":{"__typename":"Bot","login":"app"}}`
		fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[`+node("fixed", ours)+","+
			node("declined", ours+`,{"databaseId":3,"body":"no, it's back","author":{"login":"kev"}}`)+","+
			node("copied", `{"databaseId":2,"body":`+string(ok)+`,"author":{"__typename":"User","login":"kev"}}`)+","+
			node("namesake", `{"databaseId":2,"body":`+string(ok)+`,"author":{"__typename":"User","login":"app"}}`)+`]}}}}}`)
	}))
	defer srv.Close()
	APIURL, Slug = srv.URL, "app"
	ts, err := Threads("o/r", 1, "t")
	if err != nil || len(ts) != 4 || !ts[0].Addressed || ts[1].Addressed || ts[2].Addressed || ts[3].Addressed {
		t.Fatalf("threads %+v %v", ts, err)
	}
}

// Voted asks for the reactions on the first comment, Threads doesn't. Past the first 100 of a kind it reads them
// all from REST, and fails when those are fewer than the count or cut off at 30 pages, however many.
func TestVoted(t *testing.T) {
	var q []string
	up, more := 2, false // more: reactions added since the query, a next page past the 30th
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/pulls/comments/1/reactions" && r.URL.Query().Get("content") == "+1" {
			if more {
				n, _ := strconv.Atoi(r.URL.Query().Get("page"))
				w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?content=%%2B1&per_page=100&page=%d>; rel="next"`, r.Host, r.URL.Path, n+1))
			}
			fmt.Fprint(w, `[{"user":{"login":"bo"}},{"user":{"login":"ed"}}]`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		q = append(q, string(b))
		fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[`+
			`{"id":"a","isResolved":true,"comments":{"nodes":[{"databaseId":1,"body":"x `+forge.Mark+
			`","author":{"__typename":"Bot","login":"app"},"up":{"totalCount":`+fmt.Sprint(up)+`,"nodes":[{"user":{"login":"bo"}}]},"down":{"totalCount":2,"nodes":[{"user":{"login":"cy"}},{"user":{"login":"di"}}]}}]}},`+
			`{"id":"b","isResolved":true,"comments":{"nodes":[{"databaseId":2,"body":"x `+forge.Mark+
			`","author":{"__typename":"Bot","login":"app"}}]}}]}}}}}`)
	}))
	defer srv.Close()
	APIURL, Slug = srv.URL, "app"
	ts, err := Voted("o/r", 1, "t")
	if err != nil || len(ts) != 2 || !slices.Equal(ts[0].Up, []string{"bo", "ed"}) || !slices.Equal(ts[0].Down, []string{"cy", "di"}) || len(q) != 1 {
		t.Fatalf("%+v %v", ts, err)
	}
	if _, err := Threads("o/r", 1, "t"); err != nil || !strings.Contains(q[0], "reactions") || strings.Contains(q[1], "reactions") {
		t.Fatalf("%v %q", err, q)
	}
	up = 3
	if _, err := Voted("o/r", 1, "t"); err == nil || !strings.Contains(err.Error(), "2 of 3") {
		t.Fatalf("a truncated list counts: %v", err)
	}
	up, more = 2, true // 60 read of 2
	if _, err := Voted("o/r", 1, "t"); err == nil || !strings.Contains(err.Error(), "cut off at 60") {
		t.Fatalf("a cut-off list counts: %v", err)
	}
}

// A fixed finding back: answered in its thread, then reopened with a token that may (Contents: write). A failed
// answer is an error, and nothing more is sent.
func TestReopen(t *testing.T) {
	AppKey, _ = rsa.GenerateKey(rand.Reader, 2048)
	var sent []string
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = append(sent, r.URL.Path+" "+string(b))
		if fail {
			w.WriteHeader(http.StatusBadGateway)
		}
		fmt.Fprint(w, `{"token":"w","data":{}}`)
	}))
	defer srv.Close()
	APIURL = srv.URL
	e := &forge.Event{}
	e.PullRequest.Number = 1
	p := &pr{e: e, api: "/repos/o/r", token: "t"}
	c := forge.Finding{Comment: review.Comment{Path: "a.go", Line: 42, Severity: "major", Title: "race", Body: "back"}}
	if err := p.Reopen(forge.Thread{ID: "T", Comment: 9}, "abcdef1", c); err != nil || len(sent) != 3 ||
		!strings.HasPrefix(sent[0], `/repos/o/r/pulls/1/comments/9/replies {"body":"🔁 Back in `+"`abcdef1`, at `a.go:42`"+`\n\n**🟠 Major**\n\n**race**`) ||
		!strings.Contains(sent[1], `"contents":"write"`) || !strings.Contains(sent[2], `unresolveReviewThread(input:{threadId:$id})`) {
		t.Fatalf("%q %v", sent, err)
	}
	sent, fail = nil, true
	if err := p.Reopen(forge.Thread{ID: "T", Comment: 9}, "abcdef1", c); err == nil || len(sent) != 1 {
		t.Fatalf("%q %v", sent, err)
	}
}
