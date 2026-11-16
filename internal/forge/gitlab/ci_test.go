package gitlab

import (
	"diffharbor/internal/report"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// The newest pipeline's jobs over two pages: an allowed failure doesn't fail, ours is left out.
func TestChecks(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/projects/5/pipelines":
			if q := r.URL.Query(); q.Get("sha") != "abc" || q.Get("sort") != "desc" {
				t.Errorf("pipelines %q", r.URL.RawQuery)
			}
			fmt.Fprint(w, `[{"id":812,"web_url":"https://gl/p/812"}]`)
		case r.URL.Path != "/projects/5/pipelines/812/jobs":
			http.NotFound(w, r)
		case r.URL.Query().Get("page") == "1":
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprint(w, `[{"name":"test","status":"failed","web_url":"https://gl/j/1"},{"name":"lint","status":"failed","allow_failure":true,"web_url":"https://gl/j/2"},{"name":"diffharbor","status":"success"}]`)
		default:
			fmt.Fprint(w, `[{"name":"deploy","status":"running"}]`)
		}
	})
	p := &pr{api: "/projects/5"}
	got, err := p.Checks("abc")
	want := []report.Check{
		{Name: "test", Status: "completed", Conclusion: "failure", Workflow: "#812", URL: "https://gl/j/1"},
		{Name: "lint", Status: "completed", Conclusion: "neutral", Workflow: "#812", URL: "https://gl/j/2"},
		{Name: "deploy", Status: "pending", Workflow: "#812", URL: "https://gl/p/812"},
	}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("%v %v", got, err)
	}
}

// A Blocked PR's "failure" posts GitLab's "failed", the state name GitLab accepts.
func TestStatusFailure(t *testing.T) {
	var got map[string]string
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/projects/5/statuses/abc" {
			_ = json.NewDecoder(r.Body).Decode(&got)
		}
		fmt.Fprint(w, `{}`)
	})
	(&pr{api: "/projects/5"}).Status("abc", "failure", "Blocked")
	if got["state"] != "failed" || got["description"] != "Blocked" {
		t.Fatalf("%v", got)
	}
}

// No pipeline for the sha: no checks, no error.
func TestChecksNoPipeline(t *testing.T) {
	serve(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `[]`) })
	if got, err := (&pr{api: "/projects/5"}).Checks("abc"); err != nil || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}

// Active members over two pages, bots and ours left out, sorted, once each; a 403 is an error, no list.
func TestMembers(t *testing.T) {
	status := 0
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprint(w, `[{"username":"zoe","state":"active"},{"username":"review-bot","state":"active"},{"username":"project_5_bot_x","state":"active"},{"username":"gone","state":"blocked"}]`)
			return
		}
		fmt.Fprint(w, `[{"username":"ann","state":"active"},{"username":"zoe","state":"active"}]`)
	})
	p := &pr{api: "/projects/5"}
	if got, err := p.Members(); err != nil || !slices.Equal(got, []string{"ann", "zoe"}) {
		t.Fatalf("%v %v", got, err)
	}
	status = http.StatusForbidden
	if got, err := p.Members(); err == nil || got != nil {
		t.Fatalf("403: %v %v", got, err)
	}
}

// A GitLab that doesn't answer in time: the read fails (after Send's retries), it doesn't hang the report.
func TestMembersTimeout(t *testing.T) {
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
	if _, err := (&pr{api: "/projects/5"}).Members(); err == nil {
		t.Fatal("no error")
	}
}
