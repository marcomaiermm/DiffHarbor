package knowledge

import (
	"bytes"
	"context"
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
`

func TestEntities(t *testing.T) {
	var got []string
	for _, e := range Entities(testDiff) {
		got = append(got, e.Kind+":"+e.Name)
	}
	// config, then symbols (ab is too short, posts one word: prose has it), then paths
	want := []string{"config:REFUND_WINDOW_DAYS", "symbol:RefundOrder", "path:internal/refund/service.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSplit(t *testing.T) {
	md := "intro\n\n# Refunds\n\ntext\n\n```\n# not a heading\n```\n\n## Window\n\n## Window\n\nsecond\n\n#### deeper\nstays in Window\n"
	var ids []string
	for _, s := range Split(File{ID: "docs/r.md", Title: "docs/r.md", URL: "u", Text: md}) {
		ids = append(ids, s.ID)
	}
	// the empty "Window" heading is no section; the repeated one gets a unique slug
	if want := []string{"docs/r.md#", "docs/r.md#refunds", "docs/r.md#window-2"}; !slices.Equal(ids, want) {
		t.Fatalf("got %v, want %v", ids, want)
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

// example is a remote adapter: the handler the README shows. It answers for the refs it knows and any entity.
func example(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req Request
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"refs":[`)) || !bytes.Contains(body, []byte(`"entities":[`)) { // arrays, never null
			http.Error(w, "null array", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token || json.Unmarshal(body, &req) != nil || req.Version != 1 {
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
	want := []string{"docs/refund-by-ref/ref.md#ref https://forge/blob/docs/refund-by-ref/ref.md", "docs/refund.md#refunds https://forge/blob/docs/refund.md"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("got %v, %v; want %v", got, err, want)
	}
}

func TestLinear(t *testing.T) {
	searches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Query     string
			Variables map[string]string
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		doc := func(id, text string) string {
			return fmt.Sprintf(`{"id":%q,"title":"T %s","url":"https://linear.app/acme/document/t-%s","content":%q,"updatedAt":""}`, id, id, id, text)
		}
		switch {
		case strings.Contains(q.Query, "document(id"):
			if q.Variables["id"] != "3f2a1b9c8d7e" { // the link's slug id, not the link
				fmt.Fprint(w, `{"data":{"document":null}}`)
				return
			}
			fmt.Fprintf(w, `{"data":{"document":%s}}`, doc("uuid-9", "# Policy\n\nREFUND_WINDOW_DAYS"))
		case searches == 0:
			searches++
			fmt.Fprintf(w, `{"data":{"searchDocuments":{"nodes":[%s,%s]}}}`, doc("uuid-9", "# Policy\n\nREFUND_WINDOW_DAYS"), doc("uuid-2", "# Window\n\nREFUND_WINDOW_DAYS is 14"))
		default:
			http.Error(w, "down", 500)
		}
	}))
	defer srv.Close()
	oldURL, oldKey := linearURL, ticket.LinearKey
	linearURL, ticket.LinearKey = srv.URL, "k"
	t.Cleanup(func() { linearURL, ticket.LinearKey = oldURL, oldKey })
	t.Setenv("KNOWLEDGE_ADAPTERS", "acme linear")

	link := "https://linear.app/acme/document/refund-policy-3f2a1b9c8d7e"
	docs := Lookup(context.Background(), Env{Key: "t", Owner: "acme", Repo: "acme/api", Diff: testDiff, Texts: []string{"See " + link + "."},
		Redact: func(s string) string { return s }}, []string{"linear"})
	var got []string
	for _, d := range docs {
		got = append(got, d.Title)
	}
	// the linked document first (by id it sorts last) and once, though the search found it too; the first search's
	// other one stays though the next search failed
	if want := []string{"T uuid-9 › Policy", "T uuid-2 › Window"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
