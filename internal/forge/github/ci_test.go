package github

import (
	"crypto/rand"
	"crypto/rsa"
	"diffharbor/internal/forge"
	"diffharbor/internal/report"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// Two pages of check runs, each with its workflow from the runs of the same check suite; ours left out.
func TestChecks(t *testing.T) {
	AppKey, _ = rsa.GenerateKey(rand.Reader, 2048)
	var perms string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			b, _ := io.ReadAll(r.Body)
			perms = string(b)
			fmt.Fprint(w, `{"token":"ci"}`)
		case r.Header.Get("Authorization") != "Bearer ci":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/repos/o/r/actions/runs":
			if r.URL.Query().Get("head_sha") != "abc" {
				t.Errorf("runs of %q", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"total_count":1,"workflow_runs":[{"name":"quality","path":".github/workflows/quality.yml@refs/heads/main","check_suite_id":7}]}`)
		case r.URL.Query().Get("page") == "":
			w.Header().Set("Link", `<`+srv.URL+r.URL.Path+`?per_page=100&page=2>; rel="next"`)
			fmt.Fprint(w, `{"total_count":3,"check_runs":[{"name":"test","status":"completed","conclusion":"failure","html_url":"https://gh/runs/1","check_suite":{"id":7}},
{"name":"diffharbor","status":"completed","conclusion":"success","check_suite":{"id":8}}]}`)
		default:
			fmt.Fprint(w, `{"total_count":3,"check_runs":[{"name":"deploy","status":"queued","conclusion":null,"details_url":"https://ci/2","check_suite":{"id":9}}]}`)
		}
	}))
	defer srv.Close()
	APIURL = srv.URL
	p := &pr{e: &forge.Event{}, api: "/repos/o/r", token: "t"}
	got, err := p.Checks("abc")
	want := []report.Check{
		{Name: "test", Status: "completed", Conclusion: "failure", Workflow: "quality", Path: ".github/workflows/quality.yml", URL: "https://gh/runs/1"},
		{Name: "deploy", Status: "pending", URL: "https://ci/2"},
	}
	if err != nil || !slices.Equal(got, want) || !strings.Contains(perms, `"checks":"read"`) || !strings.Contains(perms, `"actions":"read"`) {
		t.Fatalf("%v %v, perms %s", got, err, perms)
	}
}

// An App without "Checks: read" gets no token for them: the error says which permission, nothing is read.
func TestChecksNoPermission(t *testing.T) {
	AppKey, _ = rsa.GenerateKey(rand.Reader, 2048)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/access_tokens") {
			t.Errorf("read %s without a token", r.URL.Path)
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"The permissions requested are not granted to this installation."}`)
	}))
	defer srv.Close()
	APIURL = srv.URL
	p := &pr{e: &forge.Event{}, api: "/repos/o/r", token: "t"}
	if cs, err := p.Checks("abc"); err == nil || cs != nil || !strings.Contains(err.Error(), "Checks and Actions: read") {
		t.Fatalf("%v %v", cs, err)
	}
}

// A forge that doesn't answer in time: the read fails (after Send's retries), it doesn't hang the report.
func TestChecksTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out Send's retries, 5 s")
	}
	defer func(d time.Duration) { client.Timeout = d }(client.Timeout)
	client.Timeout = 50 * time.Millisecond
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-done }))
	defer srv.Close()
	defer close(done)
	APIURL = srv.URL
	if _, err := checkRuns("/repos/o/r", "abc", "ci"); err == nil {
		t.Fatal("no error")
	}
}

// Collaborators over two pages, bots left out, sorted; a 403 is an error, no list.
func TestMembers(t *testing.T) {
	status := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		if r.URL.Query().Get("affiliation") != "all" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", `<`+srv.URL+r.URL.Path+`?per_page=100&affiliation=all&page=2>; rel="next"`)
			fmt.Fprint(w, `[{"login":"zoe","type":"User"},{"login":"ci-bot[bot]","type":"Bot"}]`)
			return
		}
		fmt.Fprint(w, `[{"login":"ann","type":"User"}]`)
	}))
	defer srv.Close()
	APIURL = srv.URL
	p := &pr{e: &forge.Event{}, api: "/repos/o/r", token: "t"}
	if got, err := p.Members(); err != nil || !slices.Equal(got, []string{"ann", "zoe"}) {
		t.Fatalf("%v %v", got, err)
	}
	status = http.StatusForbidden
	if got, err := p.Members(); err == nil || got != nil {
		t.Fatalf("403: %v %v", got, err)
	}
}
