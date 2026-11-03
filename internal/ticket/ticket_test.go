package ticket

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestTicket(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if r.Header.Get("x-api-key") != "k" || r.URL.Path != "/projects/WID/issues/60" {
			http.Error(w, `{"error":"Issue not found"}`, 404)
			return
		}
		fmt.Fprint(w, `{"title":"Roles","description":"## Done when\n- tests","checklists":[{"title":"AC","items":[{"content":"a","done":true},{"content":"b","done":false}]}]}`)
	}))
	defer srv.Close()
	URL, APIKey = srv.URL, ""
	if id, got := Lookup(t.Context(), nil, "x", "me/wid-60-roles", ""); id+got != "" || len(asked) > 0 {
		t.Fatalf("no key: %q %v", got, asked)
	}
	APIKey = "k"
	defer func() { APIKey = "" }()
	id, got := Lookup(t.Context(), nil, "fix UTF-8 names", "me/wid-60-roles", "WID-60")
	want := "## Linked ticket WID-60: Roles\n\n## Done when\n- tests\n\n### AC\n\n- [x] a\n- [ ] b\n\n\n"
	if id != "WID-60" || got != want || !slices.Equal(asked, []string{"/projects/UTF/issues/8", "/projects/WID/issues/60"}) {
		t.Fatalf("got %q, asked %v", got, asked)
	}
	if id, got := Lookup(t.Context(), nil, "no key here", "main", ""); id+got != "" {
		t.Fatalf("none: %q", got)
	}
	asked = nil
	if id, _ := Lookup(t.Context(), []string{"wid"}, "fix UTF-8 names", "WID-60"); id != "WID-60" || len(asked) != 1 {
		t.Fatalf("only WID asked: %q %v", id, asked)
	}
	if id, _ := Lookup(t.Context(), []string{}, "WID-60"); id != "" || len(asked) != 1 {
		t.Fatalf("none allowed: %q %v", id, asked)
	}
}

func TestLinear(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct{ Variables struct{ ID string } }
		_ = json.NewDecoder(r.Body).Decode(&q)
		if r.Header.Get("Authorization") != "lin_k" || q.Variables.ID != "ENG-7" {
			fmt.Fprint(w, `{"data":{"issue":null}}`)
			return
		}
		fmt.Fprint(w, `{"data":{"issue":{"title":"Rate limit","description":"- [ ] 429 on burst"}}}`)
	}))
	defer srv.Close()
	linearURL, LinearKey = srv.URL, "lin_k"
	defer func() { LinearKey = "" }()
	id, got := Lookup(t.Context(), nil, "fix UTF-8 names", "me/eng-7-limit")
	want := "## Linked ticket ENG-7: Rate limit\n\n- [ ] 429 on burst\n\n\n"
	if id != "ENG-7" || got != want {
		t.Fatalf("got %q %q", id, got)
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
