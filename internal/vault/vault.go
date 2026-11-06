// Package vault keeps the model keys, the forge's git tokens and the npm registries' tokens in the root server. node
// (git, checks, Pi, the PR's code) gets random tokens instead, which work only through the vault's proxy on 127.0.0.1,
// only for one provider's API, one repo's fetches or one registry's reads, and only until the run or job that got
// them ends.
package vault

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"slices"
	"strconv"
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

// Git lends token, which reads the repo at cloneURL, until ctx is done and returns the remote git fetches it through
// and the token that stands in for it, for git to send as "Authorization: Bearer <tok>" (http.extraHeader). Not in
// the remote: a URL lands in argv (git remote add, git-remote-http), and /proc/<pid>/cmdline is world-readable, also
// to another repo's job running at the same time.
func Git(ctx context.Context, cloneURL, token string) (remote, tok string, err error) {
	tok, err = lend(ctx, "git", cloneURL, token, gitPaths)
	return base + "/git", tok, err
}

// Npm lends token, which reads the npm registry at registry, until ctx is done and returns the registry the installs
// use instead: the vault sends the token upstream for GETs and points the registry's URLs in its answers (tarballs,
// redirects) back at itself. A token with a colon is user:password, Basic auth.
func Npm(ctx context.Context, registry, token string) (string, error) {
	tok, err := lend(ctx, "npm", strings.TrimSuffix(registry, "/")+"/", token, nil)
	return base + "/npm/" + tok + "/", err
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

// authHeaders are where Pi's providers put their key (pi-ai: OpenAI SDK Bearer, Anthropic SDK x-api-key, Azure api-key)
// and git its token.
var authHeaders = []string{"Authorization", "X-Api-Key", "Api-Key"}

func serve(w http.ResponseWriter, r *http.Request) {
	route, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	header, tok := "", ""
	if route == "npm" {
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
	case route == "npm" && strings.Contains(l.key, ":"):
		r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(l.key)))
	case route == "npm":
		r.Header.Set("Authorization", "Bearer "+l.key)
	case header == "Authorization":
		r.Header.Set(header, "Bearer "+l.key)
	default:
		r.Header.Set(header, l.key)
	}
	p, _ := url.PathUnescape(rest) // allowed checked it
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Path, pr.Out.URL.RawPath = "/"+p, "/"+rest
			pr.SetURL(l.up) // the loan's upstream, whatever Host the client sent
		},
		FlushInterval: -1, // SSE streams through
	}
	if route == "npm" {
		r.Header.Del("Accept-Encoding") // the transport asks for gzip itself and unpacks it: pointBack reads plain JSON
		for _, h := range []string{"X-Http-Method-Override", "X-Http-Method", "X-Method-Override"} {
			r.Header.Del(h) // GETs only: no upstream framework turns one into a DELETE
		}
		rp.ModifyResponse = func(res *http.Response) error { return pointBack(res, l.up.String(), base+"/npm/"+l.tok+"/") }
	}
	rp.ServeHTTP(w, r)
}

// maxJSON bounds a registry answer pointBack rewrites: PR code picks what it fetches, the root server holds it.
const maxJSON = 64 << 20

// pointBack points up's URLs in a registry's answer at via: a packument's tarballs, a redirect (also a relative one).
// Fetched from up directly they'd go without the token.
func pointBack(res *http.Response, up, via string) error {
	if h := res.Header.Get("Location"); h != "" {
		if loc, err := res.Request.URL.Parse(h); err == nil && strings.HasPrefix(loc.String(), up) {
			res.Header.Set("Location", via+strings.TrimPrefix(loc.String(), up))
		}
	}
	if res.Request.Method == http.MethodHead || !strings.Contains(res.Header.Get("Content-Type"), "json") {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, maxJSON+1))
	res.Body.Close()
	if err != nil {
		return err
	}
	if len(b) > maxJSON {
		return errors.New("vault: registry answer over 64 MiB")
	}
	b = bytes.ReplaceAll(b, []byte(up), []byte(via))
	res.Body, res.ContentLength = io.NopCloser(bytes.NewReader(b)), int64(len(b))
	res.Header.Set("Content-Length", strconv.Itoa(len(b)))
	res.Header.Del("ETag") // not the upstream's bytes any more
	return nil
}

// allowed reports whether a request may reach l's upstream: a model API's POSTs to its paths, a git fetch's
// info/refs and git-upload-pack, a registry's GETs (no publish). Never a path that walks out of the upstream's.
func allowed(l *loan, method, rest, service string) bool {
	p, err := url.PathUnescape(rest)
	if err != nil || path.Clean("/"+p) != "/"+p {
		return false
	}
	if l.route == "npm" {
		return method == http.MethodGet || method == http.MethodHead
	}
	if !slices.ContainsFunc(l.paths, func(pat string) bool {
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
