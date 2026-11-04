package vault

import (
	"bufio"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// up is a fake provider or forge: it records what reached it.
func up(t *testing.T) (*httptest.Server, chan *http.Request) {
	got := make(chan *http.Request, 10)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r
		w.Write([]byte("ok"))
	}))
	t.Cleanup(s.Close)
	return s, got
}

func start(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(serve))
	t.Cleanup(s.Close)
	base = s.URL
	t.Cleanup(func() { base = "" })
}

func send(t *testing.T, method, url string, h ...string) int {
	req, _ := http.NewRequest(method, url, strings.NewReader("{}"))
	for i := 0; i < len(h); i += 2 {
		switch h[i] {
		case "":
		case "Host":
			req.Host = h[i+1]
		default:
			req.Header.Set(h[i], h[i+1])
		}
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

var client = &http.Client{Timeout: 10 * time.Second}

func TestKey(t *testing.T) {
	start(t)
	u, got := up(t)
	ctx, revoke := context.WithCancel(context.Background())
	ant, _ := Key(ctx, "anthropic", u.URL+"/gw", "sk-real", "v1/messages")
	oai, _ := Key(ctx, "openai", u.URL+"/v1", "sk-oai", "responses")
	brk, _ := Key(ctx, "amazon-bedrock", u.URL, "bedrock-key", "model/*/converse-stream")

	// The token where Pi puts the key, the key there upstream; a Host the client picks changes nothing.
	if c := send(t, "POST", base+"/anthropic/v1/messages?beta=true", "X-Api-Key", ant, "Host", "evil.example"); c != 200 {
		t.Fatalf("anthropic: %d", c)
	}
	r := <-got
	if r.URL.Path != "/gw/v1/messages" || r.URL.RawQuery != "beta=true" || r.Header.Get("X-Api-Key") != "sk-real" || r.Host != u.Listener.Addr().String() {
		t.Fatalf("anthropic upstream got %s %s %q %q", r.Host, r.URL, r.Header.Get("X-Api-Key"), r.Header)
	}
	if c := send(t, "POST", base+"/openai/responses", "Authorization", "Bearer "+oai); c != 200 {
		t.Fatalf("openai: %d", c)
	}
	if r := <-got; r.Header.Get("Authorization") != "Bearer sk-oai" || r.URL.Path != "/v1/responses" {
		t.Fatalf("openai upstream got %s %q", r.URL, r.Header.Get("Authorization"))
	}

	for _, c := range []struct {
		method, path, header, tok string
		want                      int
	}{
		{"POST", "/anthropic/v1/messages", "X-Api-Key", "nope", 401},
		{"POST", "/anthropic/v1/messages", "X-Api-Key", "", 401},
		{"POST", "/openai/responses", "Authorization", "Bearer " + ant, 401}, // another provider's token
		{"POST", "/anthropic/v1/files", "X-Api-Key", ant, 403},               // not an inference path
		{"GET", "/anthropic/v1/messages", "X-Api-Key", ant, 403},
		{"POST", "/anthropic/v1/messages/../files", "X-Api-Key", ant, 403},
		{"POST", "/anthropic/v1/%2e%2e/v1/messages", "X-Api-Key", ant, 403},
		{"POST", "/git/" + ant + "/git-upload-pack", "", "", 401},
		{"POST", "/amazon-bedrock/model/%2e%2e/converse-stream", "Authorization", "Bearer " + brk, 403}, // * is one segment, never ..
	} {
		if got := send(t, c.method, base+c.path, c.header, c.tok); got != c.want {
			t.Errorf("%s %s with %q: %d, want %d", c.method, c.path, c.tok, got, c.want)
		}
	}
	revoke()
	if c := send(t, "POST", base+"/anthropic/v1/messages", "X-Api-Key", ant); c != 401 {
		t.Fatalf("revoked: %d", c)
	}
	if len(got) != 0 {
		t.Fatalf("a refused request reached upstream: %s", (<-got).URL)
	}
}

func TestGit(t *testing.T) {
	start(t)
	u, got := up(t)
	ctx, revoke := context.WithCancel(context.Background())
	defer revoke()
	remote, err := Git(ctx, u.URL+"/o/r.git", "ghs_read")
	if err != nil || !strings.HasPrefix(remote, base+"/git/") {
		t.Fatal(remote, err)
	}
	if c := send(t, "GET", remote+"/info/refs?service=git-upload-pack"); c != 200 {
		t.Fatalf("info/refs: %d", c)
	}
	r := <-got
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_read")); r.URL.Path != "/o/r.git/info/refs" || r.Header.Get("Authorization") != want {
		t.Fatalf("upstream got %s %q", r.URL, r.Header.Get("Authorization"))
	}
	if c := send(t, "POST", remote+"/git-upload-pack"); c != 200 {
		t.Fatalf("upload-pack: %d", c)
	}
	<-got
	for _, p := range []string{"GET /info/refs?service=git-receive-pack", "POST /git-receive-pack", "GET /info/refs", "GET /HEAD", "GET /../../x/y.git/info/refs?service=git-upload-pack"} {
		m, path, _ := strings.Cut(p, " ")
		if c := send(t, m, remote+path); c != 403 {
			t.Errorf("%s: %d, want 403", p, c)
		}
	}
	if len(got) != 0 {
		t.Fatalf("a refused request reached upstream: %s", (<-got).URL)
	}
}

// A model's SSE stream reaches Pi event by event, not when it ends.
func TestStream(t *testing.T) {
	start(t)
	next := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: 1\n\n"))
		w.(http.Flusher).Flush()
		<-next
		w.Write([]byte("data: 2\n\n"))
	}))
	defer s.Close()
	defer close(next)
	tok, _ := Key(context.Background(), "deepseek", s.URL, "k", "chat/completions")
	req, _ := http.NewRequest("POST", base+"/deepseek/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := client.Do(req) // its timeout ends a buffered stream
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if l, err := bufio.NewReader(res.Body).ReadString('\n'); l != "data: 1\n" {
		t.Fatalf("the first event waits for the stream's end: %q %v", l, err)
	}
}
