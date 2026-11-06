package vault

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		{"POST", "/git/git-upload-pack", "Authorization", "Bearer " + ant, 401},                         // a model key's token on the git route
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
	remote, tok, err := Git(ctx, u.URL+"/o/r.git", "ghs_read")
	if err != nil || remote != base+"/git" || strings.Contains(remote, tok) {
		t.Fatal(remote, err) // the token never in the URL: argv is world-readable
	}
	auth := "Bearer " + tok
	if c := send(t, "GET", remote+"/info/refs?service=git-upload-pack", "Authorization", auth); c != 200 {
		t.Fatalf("info/refs: %d", c)
	}
	r := <-got
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_read")); r.URL.Path != "/o/r.git/info/refs" || r.Header.Get("Authorization") != want {
		t.Fatalf("upstream got %s %q", r.URL, r.Header.Get("Authorization"))
	}
	if c := send(t, "POST", remote+"/git-upload-pack", "Authorization", auth); c != 200 {
		t.Fatalf("upload-pack: %d", c)
	}
	<-got
	for _, p := range []string{"GET /info/refs?service=git-receive-pack", "POST /git-receive-pack", "GET /info/refs", "GET /HEAD", "GET /../../x/y.git/info/refs?service=git-upload-pack"} {
		m, path, _ := strings.Cut(p, " ")
		if c := send(t, m, remote+path, "Authorization", auth); c != 403 {
			t.Errorf("%s: %d, want 403", p, c)
		}
	}
	for _, c := range []struct{ path, header, val string }{
		{"/git/info/refs?service=git-upload-pack", "", ""},                      // no header
		{"/git/info/refs?service=git-upload-pack", "Authorization", "Bearer x"}, // not a loan
		{"/git/" + tok + "/info/refs?service=git-upload-pack", "", ""},          // the token in the path is no way in
		{"/openai/responses", "Authorization", auth},                            // another route
	} {
		if got := send(t, "GET", base+c.path, c.header, c.val); got != 401 {
			t.Errorf("%s with %q: %d, want 401", c.path, c.val, got)
		}
	}
	revoke()
	if c := send(t, "GET", remote+"/info/refs?service=git-upload-pack", "Authorization", auth); c != 401 {
		t.Fatalf("revoked: %d", c)
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

// Revoking a token cuts its open streams: one held open would carry the key past its job.
func TestRevokeStream(t *testing.T) {
	start(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data: 1\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	tok, _ := Key(ctx, "deepseek", s.URL, "k", "chat/completions")
	req, _ := http.NewRequest("POST", base+"/deepseek/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	r := bufio.NewReader(res.Body)
	r.ReadString('\n')
	cancel()
	at := time.Now()
	if _, err := io.ReadAll(r); err == nil || time.Since(at) > 5*time.Second { // not the client's timeout
		t.Fatal("the stream outlived its token")
	}
}

func TestNpm(t *testing.T) {
	start(t)
	got := make(chan *http.Request, 10)
	var reg string
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r
		switch {
		case strings.HasSuffix(r.URL.Path, ".tgz"):
			http.Redirect(w, r, reg+"blobs/a.tgz", http.StatusFound)
			return
		case strings.HasSuffix(r.URL.Path, "/relative"):
			w.Header().Set("Location", "/api/npm/virtual/blobs/b.tgz")
			w.WriteHeader(http.StatusFound)
			return
		}
		body := `{"versions":{"1.0.0":{"dist":{"tarball":"` + reg + `@corp/a/-/a-1.0.0.tgz"}}}}`
		w.Header().Set("Content-Type", "application/vnd.npm.install-v1+json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("ETag", `"up"`)
		if r.Method != http.MethodHead {
			w.Write([]byte(body))
		}
	}))
	t.Cleanup(u.Close)
	reg = u.URL + "/api/npm/virtual/"
	ctx, revoke := context.WithCancel(context.Background())
	via, err := Npm(ctx, reg, "art-token")
	if err != nil || !strings.HasPrefix(via, base+"/npm/") {
		t.Fatal(via, err)
	}
	basic, _ := Npm(ctx, reg, "user:pw")

	res, err := client.Get(via + "@corp%2fa")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	r := <-got
	if r.URL.EscapedPath() != "/api/npm/virtual/@corp%2fa" || r.Header.Get("Authorization") != "Bearer art-token" {
		t.Fatalf("upstream got %s %q", r.URL.EscapedPath(), r.Header.Get("Authorization"))
	}
	if want := via + "@corp/a/-/a-1.0.0.tgz"; !strings.Contains(string(b), want) || strings.Contains(string(b), reg) || res.ContentLength != int64(len(b)) || res.Header.Get("ETag") != "" {
		t.Fatalf("packument %q (length %d, ETag %q), want the tarball at %s", b, res.ContentLength, res.Header.Get("ETag"), want)
	}
	res, err = client.Head(via + "a")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if <-got; res.Header.Get("Content-Length") == "0" || res.Header.Get("ETag") != `"up"` { // nothing to rewrite
		t.Fatalf("HEAD: length %q, ETag %q", res.Header.Get("Content-Length"), res.Header.Get("ETag"))
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err = noRedirect.Get(basic + "@corp/a/-/a-1.0.0.tgz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if r := <-got; r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:pw")) || res.Header.Get("Location") != basic+"blobs/a.tgz" {
		t.Fatalf("tarball: %q, redirect to %q", r.Header.Get("Authorization"), res.Header.Get("Location"))
	}
	req, _ := http.NewRequest("GET", via+"relative", nil)
	req.Header.Set("X-HTTP-Method-Override", "DELETE")
	res, err = noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if r := <-got; r.Header.Get("X-Http-Method-Override") != "" || res.Header.Get("Location") != via+"blobs/b.tgz" {
		t.Fatalf("override %q reached upstream, relative redirect to %q", r.Header.Get("X-Http-Method-Override"), res.Header.Get("Location"))
	}

	for _, p := range []string{"PUT /@corp%2fa", "POST /-/npm/v1/security/advisories/bulk", "GET /../../x", "GET /%2e%2e/x"} {
		m, path, _ := strings.Cut(p, " ")
		if c := send(t, m, via+strings.TrimPrefix(path, "/")); c != 403 {
			t.Errorf("%s: %d, want 403", p, c)
		}
	}
	revoke()
	if c := send(t, "GET", via+"a"); c != 401 {
		t.Fatalf("revoked: %d", c)
	}
	if len(got) != 0 {
		t.Fatalf("a refused request reached upstream: %s", (<-got).URL)
	}
}
