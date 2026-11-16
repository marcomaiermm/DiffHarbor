package ticket

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

const project = `{"project":{"key":"WID"},"columns":[{"id":1,"name":"Todo","stateType":"unstarted"},{"id":4,"name":"Done","stateType":"completed"}]}`

func TestTicket(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch {
		case r.Header.Get("x-api-key") != "k":
		case r.URL.Path == "/projects/WID":
			fmt.Fprint(w, project)
			return
		case r.URL.Path == "/projects/WID/issues/61":
			fmt.Fprint(w, `{"identifier":"WID-61","title":"Login","description":"x","columnId":1}`)
			return
		case r.URL.Path == "/projects/WID/issues/60":
			fmt.Fprint(w, `{"identifier":"WID-60","title":"Roles","description":"## Done when\n- tests","columnId":4,"dueDate":"2026-11-05",`+
				`"links":[{"kind":"blocks","direction":"outward","issue":{"identifier":"WID-61"}},{"kind":"relates","direction":"outward","issue":{"identifier":"WID-9"}}],`+
				`"checklists":[{"title":"AC","items":[{"content":"a","done":true},{"content":"b","done":false}]},{"title":"Done when:","items":[{"content":"c"}]}]}`)
			return
		}
		http.Error(w, `{"error":"Issue not found"}`, 404)
	}))
	defer srv.Close()
	URL, APIKey = srv.URL, ""
	if f := Lookup(t.Context(), nil, "x", "me/wid-60-roles", "", ""); f.ID+f.Section != "" || len(asked) > 0 || !slices.Equal(f.Keys, []string{"WID-60"}) {
		t.Fatalf("no key: %+v %v", f, asked)
	}
	APIKey = "k"
	defer func() { APIKey = "" }()
	f := Lookup(t.Context(), nil, "fix UTF-8 names", "me/wid-60-roles", "WID-60", "")
	want := "## Linked ticket WID-60: Roles\n\n## Done when\n- tests\n\n### AC\n\n- [x] a\n- [ ] b\n\n### Done when:\n\n- [ ] c\n\n\n"
	if f.ID != "WID-60" || f.Section != want || !slices.Equal(asked, []string{"/projects/UTF/issues/8", "/projects/WID/issues/60", "/projects/WID"}) {
		t.Fatalf("got %q, asked %v", f.Section, asked)
	}
	if !slices.Equal(f.Keys, []string{"WID-60"}) || len(f.Tickets) != 1 || f.Failed != nil {
		t.Fatalf("UTF-8 is no ticket: %+v", f)
	}
	tk := f.Tickets[0]
	if tk.State != "Done" || !tk.Done || tk.Due.Format(time.DateOnly) != "2026-11-05" || !slices.Equal(tk.Blocks, []string{"WID-61"}) ||
		tk.BlockedBy != nil || !slices.Equal(tk.DoneWhen, []string{"tests", "c"}) || tk.Read.IsZero() || tk.Was != "" {
		t.Fatalf("ticket: %+v", tk)
	}
	if f := Lookup(t.Context(), nil, "no key here", "main", "", ""); f.ID+f.Section != "" {
		t.Fatalf("none: %q", f.Section)
	}
	if f := Lookup(t.Context(), nil, "Roles", "main", "Some text.\n\nCloses WID-60", ""); f.ID != "WID-60" {
		t.Fatalf("description: %q", f.ID)
	}
	f = Lookup(t.Context(), nil, "Login", "feat/wid-61-login", "Needs [WID-60](https://x/WID-60) first, see WID-9.", "")
	if f.ID != "WID-61" || !strings.HasPrefix(f.Section, "## Linked ticket WID-61: Login\n\nx\n\n\n## Referenced ticket WID-60 (context, not what this PR must do): Roles\n") {
		t.Fatalf("branch first, description as reference: %+v", f)
	}
	if len(f.Tickets) != 2 || f.Tickets[0].Key != "WID-61" || f.Tickets[1].Key != "WID-60" {
		t.Fatalf("open first: %+v", f.Tickets)
	}
	asked = nil
	if f := Lookup(t.Context(), []string{"wid"}, "fix UTF-8 names", "WID-60", "", ""); f.ID != "WID-60" || len(asked) != 2 {
		t.Fatalf("only WID asked, its project once: %q %v", f.ID, asked)
	}
	if f := Lookup(t.Context(), []string{}, "WID-60", "", "", ""); f.ID != "" || len(asked) != 2 {
		t.Fatalf("none allowed: %q %v", f.ID, asked)
	}
	// a commit's ticket is read for the report, not the prompt
	f = Lookup(t.Context(), nil, "Roles", "main", "", "feat: WID-60 roles\nWID-61 login")
	if f.ID != "" || f.Section != "" || len(f.Tickets) != 2 {
		t.Fatalf("commits: %+v", f)
	}
}

// A ticket the tracker fails on is kept by key; the next is read.
func TestTicketError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects/WID":
			fmt.Fprint(w, project)
		case "/projects/WID/issues/1":
			http.Error(w, "down", http.StatusBadGateway)
		case "/projects/WID/issues/2":
			fmt.Fprint(w, `{"identifier":"WID-2","title":"Two","columnId":1}`)
		default:
			http.Error(w, "", 404)
		}
	}))
	defer srv.Close()
	URL, APIKey = srv.URL, "k"
	defer func() { APIKey = "" }()
	f := Lookup(t.Context(), nil, "WID-1 and WID-2", "", "", "")
	if f.ID != "WID-2" || len(f.Tickets) != 1 || !strings.Contains(f.Failed["WID-1"], "502") || !slices.Equal(f.Keys, []string{"WID-1", "WID-2"}) {
		t.Fatalf("%+v", f)
	}
}

// Past maxReads the keys stay unread and Capped says so; follow-ups come last.
func TestTicketCap(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/projects/WID" {
			fmt.Fprint(w, project)
			return
		}
		n++
		k := "WID-" + r.URL.Path[len("/projects/WID/issues/"):]
		fmt.Fprintf(w, `{"identifier":%q,"title":"t","columnId":1}`, k)
	}))
	defer srv.Close()
	URL, APIKey = srv.URL, "k"
	defer func() { APIKey = "" }()
	var title []string
	for i := range maxReads + 2 {
		title = append(title, fmt.Sprintf("WID-%d", i+1))
	}
	f := Lookup(t.Context(), nil, strings.Join(title, " "), "", "## Follow-ups\n- WID-99", "")
	if !f.Capped || n != maxReads || len(f.Tickets) != maxReads || len(f.Keys) != maxReads+2 || f.FollowUps != nil {
		t.Fatalf("capped: %v, %d reads, %d tickets, %d keys", f.Capped, n, len(f.Tickets), len(f.Keys))
	}
}

// A key in the description's follow-up list is a follow-up unless the PR names it elsewhere; one named anywhere
// else is a ticket.
func TestFollowUps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/projects/WID" {
			fmt.Fprint(w, project)
			return
		}
		fmt.Fprintf(w, `{"identifier":"WID-%s","title":"t","columnId":1}`, r.URL.Path[len("/projects/WID/issues/"):])
	}))
	defer srv.Close()
	URL, APIKey = srv.URL, "k"
	defer func() { APIKey = "" }()
	body := "Needs WID-5 (merged).\n\n## Follow-ups\n\n- WID-9: names.\n- WID-3: also a commit's.\n- WID-8 zones\n\n## Verification\n\nSee WID-7."
	f := Lookup(t.Context(), nil, "WID-1", "", body, "WID-3 fix")
	var keys, follow []string
	for _, t := range f.Tickets {
		keys = append(keys, t.Key)
	}
	for _, t := range f.FollowUps {
		follow = append(follow, t.Key)
	}
	if !slices.Equal(keys, []string{"WID-1", "WID-3", "WID-5", "WID-7"}) || !slices.Equal(follow, []string{"WID-8", "WID-9"}) || f.FollowUps[0].State != "Todo" {
		t.Fatalf("tickets %v, follow-ups %v", keys, follow)
	}
}

func TestContract(t *testing.T) {
	md := "## Problem\nx\n\n## Outcome\nIt works.\nTwice.\n\n```md\n## Notes\nnot a heading\n```\n\n" +
		"## **Done when:**\n1. One,\n   wrapped.\n2. [x] Two\n   - nested\n* [ ] Three\n\nAfter.\n\n## Notes\n- n1\n\n### Sub\nn2\n\n# Next\nz"
	outcome, done, notes := contract(md)
	if outcome != "It works.\nTwice.\n\n```md\n## Notes\nnot a heading\n```" {
		t.Fatalf("outcome %q", outcome)
	}
	if !slices.Equal(done, []string{"One,\nwrapped.", "Two\n- nested", "Three"}) {
		t.Fatalf("done when %q", done)
	}
	if notes != "- n1\n\n### Sub\nn2" {
		t.Fatalf("notes %q", notes)
	}
	if o, d, n := contract("just text"); o+n != "" || d != nil {
		t.Fatal("no sections")
	}
}

func TestLinear(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct{ Variables struct{ ID string } }
		_ = json.NewDecoder(r.Body).Decode(&q)
		switch {
		case r.Header.Get("Authorization") != "lin_k":
			http.Error(w, "", http.StatusUnauthorized)
		case q.Variables.ID == "ENG-7", q.Variables.ID == "OLD-7":
			fmt.Fprint(w, `{"data":{"issue":{"identifier":"ENG-7","title":"Rate limit","url":"https://linear.app/x/issue/ENG-7",`+
				`"description":"## Outcome\nBursts get 429.\n\n## Done when\n- [ ] 429 on burst\n","dueDate":"2026-12-01","previousIdentifiers":[],`+
				`"state":{"name":"In Progress","type":"started"},"relations":{"nodes":[{"type":"related","relatedIssue":{"identifier":"ENG-2"}}]},`+
				`"inverseRelations":{"nodes":[{"type":"blocks","issue":{"identifier":"ENG-3"}}]}}}}`)
		case q.Variables.ID == "ENG-8":
			fmt.Fprint(w, `{"data":{"issue":{"identifier":"ENG-8","title":"Moved","previousIdentifiers":["PYR-1","PYR-8"],"state":{"name":"Done","type":"completed"}}}}`)
		default:
			fmt.Fprint(w, `{"data":{"issue":null}}`)
		}
	}))
	defer srv.Close()
	linearURL, LinearKey = srv.URL, "lin_k"
	defer func() { LinearKey = "" }()
	f := Lookup(t.Context(), nil, "fix UTF-8 names", "me/feat_eng-7_limit", "", "")
	want := "## Linked ticket ENG-7: Rate limit\n\n## Outcome\nBursts get 429.\n\n## Done when\n- [ ] 429 on burst\n\n\n\n"
	if f.ID != "ENG-7" || f.Section != want {
		t.Fatalf("got %q %q", f.ID, f.Section)
	}
	tk := f.Tickets[0]
	if tk.State != "In Progress" || tk.Done || tk.Outcome != "Bursts get 429." || !slices.Equal(tk.DoneWhen, []string{"429 on burst"}) ||
		!slices.Equal(tk.BlockedBy, []string{"ENG-3"}) || tk.Blocks != nil || tk.URL == "" || tk.Due.IsZero() {
		t.Fatalf("ticket %+v", tk)
	}
	f = Lookup(t.Context(), nil, "OLD-7 ENG-8", "", "", "")
	if len(f.Tickets) != 2 || f.Tickets[0].Key != "ENG-7" || f.Tickets[0].Was != "OLD-7" || f.Tickets[1].Was != "PYR-8" || !f.Tickets[1].Done {
		t.Fatalf("moved: %+v", f.Tickets)
	}
	LinearKey = "bad"
	if f := Lookup(t.Context(), nil, "ENG-7", "", "", ""); !strings.Contains(f.Failed["ENG-7"], "401") {
		t.Fatalf("auth: %+v", f.Failed)
	}
}

func TestActive(t *testing.T) {
	defer func() { URL, APIKey, LinearKey, Provider = "", "", "", "" }()
	for _, c := range []struct {
		url, key, lin, prov, want string
		fail                      bool
	}{
		{},
		{key: "k", url: "u", want: "itsaplan"},
		{lin: "l", want: "linear"},
		{key: "k", url: "u", lin: "l", fail: true},
		{key: "k", url: "u", lin: "l", prov: "linear", want: "linear"},
		{key: "k", url: "u", prov: "linear", fail: true},
		{lin: "l", prov: "jira", fail: true},
	} {
		URL, APIKey, LinearKey, Provider = c.url, c.key, c.lin, c.prov
		if got, err := Active(); got != c.want || (err != nil) != c.fail {
			t.Errorf("%+v: %q %v", c, got, err)
		}
	}
}
