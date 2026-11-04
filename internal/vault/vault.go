// Package vault keeps the model keys and the forge's git tokens in the root server. node (git, checks, Pi, the PR's
// code) gets random tokens instead, which work only through the vault's proxy on 127.0.0.1, only for one provider's
// API or one repo's fetches, and only until the run or job that got them ends.
package vault

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	base  string // http://127.0.0.1:<port> once Start ran
	mu    sync.Mutex
	loans []*loan
)

type loan struct {
	ctx        context.Context // done = revoked
	route, tok string          // route: a provider, or "git"
	up         *url.URL
	key        string
	paths      []string // path.Match patterns under up it may reach
}

// gitPaths are a fetch's smart-HTTP requests: no git-receive-pack, no dumb HTTP.
var gitPaths = []string{"info/refs", "git-upload-pack"}

// Start serves the proxy on a random port of 127.0.0.1. PR code shares the network and reaches it, but only with its
// own job's tokens.
func Start() error {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	base = "http://" + ln.Addr().String()
	srv := &http.Server{Handler: http.HandlerFunc(serve), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute, IdleTimeout: time.Minute}
	go func() { log.Fatal(srv.Serve(ln)) }()
	return nil
}

// URL is the proxy's, "" = not started: credentials go to node as they are.
func URL() string { return base }

// Key lends key, provider's credential for upstream, until ctx is done and returns the token that stands in for it:
// Pi sends it where it would send the key (Authorization, x-api-key or api-key) to URL()/provider/<path>, the vault
// sends the key to upstream/<path> for a POST to one of paths.
func Key(ctx context.Context, provider, upstream, key string, paths ...string) (string, error) {
	return lend(ctx, provider, upstream, key, paths)
}

// Git lends token, which reads the repo at cloneURL, until ctx is done and returns the remote git fetches it through.
func Git(ctx context.Context, cloneURL, token string) (string, error) {
	tok, err := lend(ctx, "git", cloneURL, token, gitPaths)
	return base + "/git/" + tok, err
}

func lend(ctx context.Context, route, upstream, key string, paths []string) (string, error) {
	up, err := url.Parse(upstream)
	if err != nil || up.Scheme != "https" && up.Scheme != "http" || up.Host == "" {
		return "", errors.New("vault: bad upstream for " + route)
	}
	l := &loan{ctx: ctx, route: route, tok: rand.Text(), up: up, key: key, paths: paths}
	mu.Lock()
	loans = append(loans, l)
	mu.Unlock()
	context.AfterFunc(ctx, func() {
		mu.Lock()
		loans = slices.DeleteFunc(loans, func(x *loan) bool { return x == l })
		mu.Unlock()
	})
	return l.tok, nil
}

// find is route's live loan for tok, compared in constant time.
func find(route, tok string) *loan {
	mu.Lock()
	defer mu.Unlock()
	var found *loan
	for _, l := range loans {
		if subtle.ConstantTimeCompare([]byte(l.tok), []byte(tok)) == 1 && l.route == route && l.ctx.Err() == nil {
			found = l
		}
	}
	return found
}

// authHeaders are where Pi's providers put their key (pi-ai: OpenAI SDK Bearer, Anthropic SDK x-api-key, Azure api-key).
var authHeaders = []string{"Authorization", "X-Api-Key", "Api-Key"}

func serve(w http.ResponseWriter, r *http.Request) {
	route, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	header, tok := "", ""
	if route == "git" {
		tok, rest, _ = strings.Cut(rest, "/")
	} else {
		for _, h := range authHeaders {
			if v := r.Header.Get(h); v != "" {
				header, tok = h, strings.TrimPrefix(v, "Bearer ")
				break
			}
		}
	}
	l := find(route, tok)
	if l == nil {
		http.Error(w, "unknown or revoked token", http.StatusUnauthorized)
		return
	}
	if !allowed(l, r.Method, rest, r.URL.Query().Get("service")) {
		http.Error(w, "not through the vault", http.StatusForbidden)
		return
	}
	// A revoked token also ends its streams: one held open would carry the key past its job.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(l.ctx, cancel)()
	r = r.WithContext(ctx)
	r.Header.Del("Upgrade") // no upstream path upgrades: never a raw tunnel through the vault
	for _, h := range authHeaders {
		r.Header.Del(h)
	}
	switch {
	case route == "git":
		r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+l.key)))
	case header == "Authorization":
		r.Header.Set(header, "Bearer "+l.key)
	default:
		r.Header.Set(header, l.key)
	}
	p, _ := url.PathUnescape(rest) // allowed checked it
	(&httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Path, pr.Out.URL.RawPath = "/"+p, "/"+rest
			pr.SetURL(l.up) // the loan's upstream, whatever Host the client sent
		},
		FlushInterval: -1, // SSE streams through
	}).ServeHTTP(w, r)
}

// allowed reports whether a request may reach l's upstream: a model API's POSTs to its paths, a git fetch's
// info/refs and git-upload-pack. Never a path that walks out of the upstream's.
func allowed(l *loan, method, rest, service string) bool {
	p, err := url.PathUnescape(rest)
	if err != nil || path.Clean("/"+p) != "/"+p || !slices.ContainsFunc(l.paths, func(pat string) bool {
		ok, _ := path.Match(pat, rest)
		return ok
	}) {
		return false
	}
	if l.route != "git" {
		return method == http.MethodPost
	}
	return rest == "info/refs" && method == http.MethodGet && service == "git-upload-pack" ||
		rest == "git-upload-pack" && method == http.MethodPost
}
