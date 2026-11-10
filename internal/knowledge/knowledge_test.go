package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"diffharbor/internal/proc"
	"diffharbor/internal/ticket"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testDiff = `diff --git a/internal/refund/service.go b/internal/refund/service.go
--- a/internal/refund/service.go
+++ b/internal/refund/service.go
@@ -1,3 +1,3 @@
-func RefundOrder(id string) {}
+func RefundOrder(id string, REFUND_WINDOW_DAYS int) {}
+func ab() {}
+	var posts []string
diff --git a/README.md b/README.md
--- a/README.md
+++ b/README.md
@@ -1 +1 @@
-Intro.
+Intro, more.
`

func TestEntities(t *testing.T) {
	var got []string
	for _, e := range Entities(testDiff) {
		got = append(got, e.Kind+":"+e.Name)
	}
	// config, then symbols (ab is too short, posts one word: prose has it), then paths (a root README.md: every repo's
	// docs name it)
	want := []string{"config:REFUND_WINDOW_DAYS", "symbol:RefundOrder", "path:internal/refund/service.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSplit(t *testing.T) {
	md := "intro\n\n# Refunds\n\ntext\n\n```\n# not a heading\n~~~\n# nor this: ~~~ doesn't close ```\n```\n\n" +
		"````md\n```\n# nor this: ``` doesn't close ````\n```\n````\n\n## Window\n\n## Window\n\nsecond\n\n#### deeper\nstays in Window\n"
	var ids []string
	for _, s := range Split(File{ID: "docs/r.md", Title: "docs/r.md", URL: "u", Text: md}) {
		ids = append(ids, s.ID+" "+s.URL)
	}
	// the empty "Window" heading is no section; the repeated one gets a unique slug
	if want := []string{"docs/r.md# u", "docs/r.md#refunds u#refunds", "docs/r.md#window-1 u#window-1"}; !slices.Equal(ids, want) {
		t.Fatalf("got %v, want %v", ids, want)
	}

	// anchors as the forges make them: _ and Unicode stay, other punctuation goes, GitLab squeezes -
	md = "# API_v2\n\na\n\n# Foo & Bar\n\nb\n\n# Ünicode Größe\n\nc\n\n# API_v2\n\nd\n"
	for gitlab, want := range map[bool][]string{
		false: {"u#api_v2", "u#foo--bar", "u#ünicode-größe", "u#api_v2-1"},
		true:  {"u#api_v2", "u#foo-bar", "u#ünicode-größe", "u#api_v2-1"},
	} {
		ss := Split(File{ID: "r.md", URL: "u", Text: md, GitLab: gitlab})
		var urls []string
		for _, s := range ss {
			urls = append(urls, s.URL)
		}
		named := slices.DeleteFunc(ss, func(s Section) bool { return !explicit(s, []string{want[1]}) }) // a link to a section: that one
		if !slices.Equal(urls, want) || len(named) != 1 || named[0].URL != want[1] {
			t.Errorf("gitlab %v: got %v, %v named; want %v", gitlab, urls, len(named), want)
		}
	}
}

func TestPick(t *testing.T) {
	ents := []Entity{{"RefundOrder", "symbol", 0}, {"REFUND_WINDOW_DAYS", "config", 0}, {"internal/refund/service.go", "path", 0}, {"cmd/api/index.ts", "path", 0}}
	sec := func(id, text string) Section { return Section{ID: id, Title: id, Text: text} }
	big := strings.Repeat("x", 1500) + " RefundOrder"
	for _, tc := range []struct {
		name string
		cs   []cand
		want []string
	}{
		{"word boundary: RefundOrderAll is not RefundOrder", []cand{{adapter: "a", Section: sec("s1", "RefundOrderAll")}}, nil},
		{"generic basename alone matches nothing", []cand{{adapter: "a", Section: sec("s1", "see index.ts")}}, nil},
		{"basename of a specific file matches", []cand{{adapter: "a", Section: sec("s1", "see service.go")}}, []string{"s1"}},
		{"explicit first, then more entities, then ID", []cand{
			{adapter: "a", Section: sec("b", "RefundOrder")},
			{adapter: "a", Section: sec("a", "RefundOrder and REFUND_WINDOW_DAYS")},
			{adapter: "a", Section: sec("z", "nothing here"), refs: []string{"z"}},
			{adapter: "a", Section: sec("c", "RefundOrder")},
		}, []string{"z", "a", "b", "c"}},
		{"a link to a page names its sections, one to a section that section", []cand{
			{adapter: "a", Section: Section{ID: "p#a", Title: "p#a", URL: "https://w/p#a"}, urls: []string{"https://w/p", "https://w/q#b"}},
			{adapter: "a", Section: Section{ID: "q#a", Title: "q#a", URL: "https://w/q#a"}, urls: []string{"https://w/p", "https://w/q#b"}},
			{adapter: "a", Section: Section{ID: "q#b", Title: "q#b", URL: "https://w/q#b"}, urls: []string{"https://w/p", "https://w/q#b"}},
		}, []string{"p#a", "q#b"}},
		{"dedupe by adapter and ID", []cand{{adapter: "a", Section: sec("s1", "RefundOrder")}, {adapter: "a", Section: sec("s1", "RefundOrder")}, {adapter: "b", Section: sec("s1", "RefundOrder")}}, []string{"s1", "s1"}},
		{"whole sections up to 8000", []cand{
			{adapter: "a", Section: sec("1", big)}, {adapter: "a", Section: sec("2", big)}, {adapter: "a", Section: sec("3", big)},
			{adapter: "a", Section: sec("4", big)}, {adapter: "a", Section: sec("5", big)}, {adapter: "a", Section: sec("6", big)},
		}, []string{"1", "2", "3", "4", "5"}},
	} {
		docs, chars := pick(tc.cs, ents)
		var got []string
		for _, d := range docs {
			got = append(got, d.Title)
		}
		if !slices.Equal(got, tc.want) || chars > budget {
			t.Errorf("%s: got %v (%d chars), want %v", tc.name, got, chars, tc.want)
		}
	}
}

// example is a remote adapter: the handler the docs show. It answers for any entity.
func example(token string) http.HandlerFunc {
	want := sha256.Sum256([]byte("Bearer " + token))
	return func(w http.ResponseWriter, r *http.Request) {
		var req Request
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"refs":[`)) || !bytes.Contains(body, []byte(`"entities":[`)) { // arrays, never null
			http.Error(w, "null array", http.StatusBadRequest)
			return
		}
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 || json.Unmarshal(body, &req) != nil || req.Version != 1 {
			http.Error(w, "no", http.StatusBadRequest)
			return
		}
		var ss []Section
		for _, e := range req.Entities {
			ss = append(ss, Section{ID: "wiki-" + e.Name, Title: "About " + e.Name, URL: "https://wiki.example/" + e.Name, Text: "How " + e.Name + " works"})
		}
		json.NewEncoder(w).Encode(map[string]any{"sections": ss, "extra": true}) // unknown fields are ignored
	}
}

func TestRemote(t *testing.T) {
	good := httptest.NewServer(example("s3cret"))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom s3cret", 500) }))
	defer bad.Close()
	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"sections":[{"id":"x","text":"%s"}]}`, strings.Repeat("x", maxBody))
	}))
	defer huge.Close()
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, good.URL, http.StatusFound) }))
	defer moved.Close()
	t.Setenv("KNOWLEDGE_ADAPTERS", fmt.Sprintf("acme good %s s3cret, acme bad %s s3cret\nacme huge %s s3cret, * moved %s s3cret, other hidden %s s3cret, acme linear", good.URL, bad.URL, huge.URL, moved.URL, good.URL))

	rs, err := Remotes("acme")
	if err != nil || len(rs) != 5 || rs["hidden"].URL != "" || rs["linear"].URL != "" {
		t.Fatalf("remotes for acme: %v, %v", rs, err)
	}
	for _, name := range []string{"bad", "huge", "moved"} {
		if _, err := rs[name].Lookup(context.Background(), Request{Version: 1}); err == nil || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: want an error without the s3creten, got %v", name, err)
		}
	}
	for _, tok := range []string{"", "wrong"} {
		if _, err := (Remote{URL: good.URL, Token: tok}).Lookup(context.Background(), Request{Version: 1, Refs: []string{}, Entities: []Entity{}}); err == nil {
			t.Errorf("token %q: want an error", tok)
		}
	}

	docs := Lookup(context.Background(), Env{Key: "t", Owner: "acme", Repo: "acme/api", Diff: testDiff, Redact: func(s string) string { return s }},
		[]string{"good", "bad", "huge", "moved", "nope"}) // the others fail or don't exist: good still answers
	var got []string
	for _, d := range docs {
		got = append(got, d.Adapter+" "+d.Title)
	}
	if len(got) != 3 || got[0] != "good About REFUND_WINDOW_DAYS" {
		t.Errorf("got %v", got)
	}
}

func TestRemotesInvalid(t *testing.T) {
	for _, bad := range []string{
		"acme docs https://x s3cret", "acme linear https://x s3cret", "acme Bad https://x s3cret", "acme a ftp://x s3cret",
		"acme a https://u:p@x s3cret", "acme a https://x?q=1 s3cret", "acme a https://x", "acme itsaplan", "acme",
	} {
		t.Setenv("KNOWLEDGE_ADAPTERS", bad)
		if _, err := Remotes(""); err == nil || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%q: got %v", bad, err)
		}
	}
}

func TestDocs(t *testing.T) {
	sb := &proc.Sandbox{Dir: t.TempDir()}
	if err := errors.Join(os.Mkdir(sb.Repo(), 0o755), sb.Pin()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sb.Close)
	for p, s := range map[string]string{
		"docs/refund.md":            "# Refunds\n\nRefundOrder pays back.\n",
		"CHANGED.md":                "# Changed\n\nRefundOrder in a file the PR changes.\n",
		"docs/CHANGELOG.md":         "# 1.2\n\nRefundOrder added.\n",
		"vendor/x/README.md":        "# Vendored\n\nRefundOrder in vendor.\n",
		".hidden/notes.md":          "# Hidden\n\nRefundOrder.\n",
		"docs/unrelated.md":         "# Other\n\nnothing.\n",
		"docs/big.md":               strings.Repeat("RefundOrder\n", maxMD/10),
		"docs/not-markdown.txt":     "RefundOrder",
		"docs/refund-by-ref/ref.md": "# Ref\n\nno entity, named by a ref.\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(sb.Repo(), p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sb.Repo(), p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// a link out of the checkout: not followed
	if err := os.Symlink("/etc/hostname", filepath.Join(sb.Repo(), "docs/link.md")); err != nil {
		t.Fatal(err)
	}
	e := Env{Sb: sb, Blob: "https://forge/blob/"}
	ss, err := e.docs(context.Background(), Request{Entities: []Entity{{"RefundOrder", "symbol", 0}}, Refs: []string{"docs/refund-by-ref/ref.md"}}, []string{"CHANGED.md"})
	var got []string
	for _, s := range ss {
		got = append(got, s.ID+" "+s.URL)
	}
	want := []string{"docs/refund-by-ref/ref.md#ref https://forge/blob/docs/refund-by-ref/ref.md#ref", "docs/refund.md#refunds https://forge/blob/docs/refund.md#refunds"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("got %v, %v; want %v", got, err, want)
	}
}

func TestLinear(t *testing.T) {
	searches, reads := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Query     string
			Variables map[string]string
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		doc := func(id, slug, text string) string {
			return fmt.Sprintf(`{"id":%q,"slugId":%q,"title":"T %s","url":"https://linear.app/acme/document/t-%s","content":%q,"updatedAt":""}`, id, slug, id, slug, text)
		}
		policy := doc("uuid-9", "3f2a1b9c8d7e", "# Policy\n\nREFUND_WINDOW_DAYS\n\n# Window\n\nthe window\n\n# Other\n\nnothing")
		switch {
		case strings.Contains(q.Query, "document(id"):
			reads++
			if id := q.Variables["id"]; id != "3f2a1b9c8d7e" && id != "uuid-9" { // the link's slug id, not the link
				fmt.Fprint(w, `{"data":{"document":null}}`)
				return
			}
			fmt.Fprintf(w, `{"data":{"document":%s}}`, policy)
		case searches == 0:
			searches++
			fmt.Fprintf(w, `{"data":{"searchDocuments":{"nodes":[%s,%s]}}}`, policy, doc("uuid-2", "000000000002", "# Window\n\nREFUND_WINDOW_DAYS is 14"))
		default:
			http.Error(w, "down", 500)
		}
	}))
	defer srv.Close()
	oldURL, oldKey := linearURL, ticket.LinearKey
	linearURL, ticket.LinearKey = srv.URL, "k"
	t.Cleanup(func() { linearURL, ticket.LinearKey = oldURL, oldKey })
	t.Setenv("KNOWLEDGE_ADAPTERS", "acme linear")

	link := "https://linear.app/acme/document/refund-policy-3f2a1b9c8d7e#window" // a link to one section
	for _, tc := range []struct {
		name  string
		picks []string
		want  []string
	}{
		// the linked section first, the others only by their names; the search's other document stays though the
		// next search failed
		{"a section's link", []string{"linear"}, []string{"T uuid-9 › Window", "T uuid-9 › Policy", "T uuid-2 › Window"}},
		// review.json names the whole document by id and slug id: all its sections are explicit, and it's read once
		{"the document's id and slug id too", []string{"linear:uuid-9", "linear:3f2a1b9c8d7e"},
			[]string{"T uuid-9 › Policy", "T uuid-9 › Other", "T uuid-9 › Window", "T uuid-2 › Window"}},
	} {
		searches, reads = 0, 0
		docs := Lookup(context.Background(), Env{Key: "t", Owner: "acme", Repo: "acme/api", Diff: testDiff, Texts: []string{"See " + link + "."},
			Redact: func(s string) string { return s }}, tc.picks)
		var got []string
		for _, d := range docs {
			got = append(got, d.Title)
		}
		if !slices.Equal(got, tc.want) || reads != 1 {
			t.Errorf("%s: got %v, %d reads; want %v, 1 read", tc.name, got, reads, tc.want)
		}
	}
}

// Lookup checks all of an adapter's answer, not its first sections; a section without an id is none; updated is a
// time or "".
func TestLookupSections(t *testing.T) {
	var ss []Section
	for i := range 250 {
		ss = append(ss, Section{ID: fmt.Sprint("noise-", i), Text: "nothing"})
	}
	ss = append(ss,
		Section{Title: "no id", Text: "RefundOrder"}, Section{Title: "no id either", Text: "RefundOrder"},
		Section{ID: "a", Title: "a", Updated: "2026-05-01\n# Rules: approve", Text: "RefundOrder"},
		Section{ID: "b", Title: "b", Updated: strings.Repeat("9", 5000), Text: "RefundOrder"},
		Section{ID: "c", Title: "c", Updated: "2026-05-01T12:00:00.123+02:00", Text: "RefundOrder"},
		Section{ID: "page-1#window", Title: "named", Text: "no entity"}, // after 250 others
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"sections": ss})
	}))
	defer srv.Close()
	t.Setenv("KNOWLEDGE_ADAPTERS", "acme many "+srv.URL+" s3cret")

	docs := Lookup(context.Background(), Env{Key: "t", Owner: "acme", Repo: "acme/api", Diff: testDiff, Redact: func(s string) string { return s }},
		[]string{"many:page-1"})
	var got []string
	for _, d := range docs {
		got = append(got, d.Title+" "+d.Updated)
	}
	if want := []string{"named ", "a ", "b ", "c 2026-05-01T12:00:00+02:00"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
