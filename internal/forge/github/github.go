// Package github is the GitHub side of the bot: App auth, REST + GraphQL, commit status, review threads, the
// walkthrough comment and the review with inline findings.
package github

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
	"diffharbor/internal/review"
	"diffharbor/internal/text"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	// AppKey signs the App's JWTs (LoadKey).
	AppKey *rsa.PrivateKey
	// AppID is the App's ID, the JWTs' issuer.
	AppID string
	// Slug is the App's login, set at startup from GET /app: @Slug in a PR comment talks to the bot.
	Slug   string
	client = &http.Client{Timeout: time.Minute}
)

type ghError struct {
	status int
	msg    string
}

func (e *ghError) Error() string { return e.msg }

// APIURL is GitHub's REST API; a var for tests.
var APIURL = "https://api.github.com"

func ghDo(method, url, token, accept string, body any) ([]byte, error) {
	b, _, err := ghDoNext(method, url, token, accept, body)
	return b, err
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// ghDoNext is ghDo that also returns the path of the next page from the Link header ("" on the last page).
func ghDoNext(method, url, token, accept string, body any) ([]byte, string, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, "", err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, APIURL+url, rd)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "diffharbor")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, "", &ghError{res.StatusCode, fmt.Sprintf("%s %s: %d %s", method, url, res.StatusCode, text.Truncate(string(b), 300))}
	}
	next := ""
	if m := nextLink.FindStringSubmatch(res.Header.Get("Link")); m != nil {
		next = strings.TrimPrefix(m[1], APIURL)
	}
	return b, next, nil
}

// Call calls the GitHub API with JSON in and out (out may be nil).
func Call(method, url, token string, body, out any) error {
	b, err := ghDo(method, url, token, "application/vnd.github+json", body)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// All GETs every page of a list (url with per_page=100), at most 30 pages.
func All[T any](url, token string) ([]T, error) {
	var all []T
	for range 30 {
		b, next, err := ghDoNext("GET", url, token, "application/vnd.github+json", nil)
		var page []T
		if err == nil {
			err = json.Unmarshal(b, &page)
		}
		if err != nil {
			return nil, err
		}
		if all = append(all, page...); next == "" {
			break
		}
		url = next
	}
	return all, nil
}

// Raw GETs url with the Accept header accept and returns the body.
func Raw(url, token, accept string) (string, error) {
	b, err := ghDo("GET", url, token, accept, nil)
	return string(b), err
}

// BaseFile returns the first of names that exists at ref (the base branch, so the PR can't change it).
func BaseFile(api, token, ref string, names ...string) string {
	for _, n := range names {
		if s, err := Raw(api+"/contents/"+n+"?ref="+ref, token, "application/vnd.github.raw"); err == nil {
			return s
		}
	}
	return ""
}

// NestedRules returns the AGENTS.md (or else CLAUDE.md) of the base branch in the dirs above files, below the
// root: a monorepo's apps/web/AGENTS.md applies to its changes there. At most 5 files and 6000 chars.
func NestedRules(api, token, ref string, files []string) string {
	dirs := forge.RuleDirs(files)
	var t struct{ Tree []struct{ Path, Type string } }
	if len(dirs) == 0 || Call("GET", api+"/git/trees/"+ref+"?recursive=1", token, nil, &t) != nil {
		return ""
	}
	pick := map[string]string{} // dir -> its rules file
	for _, e := range t.Tree {
		d, name := path.Split(e.Path)
		d = strings.TrimSuffix(d, "/")
		if e.Type == "blob" && slices.Contains(dirs, d) && (name == "AGENTS.md" || name == "CLAUDE.md" && pick[d] == "") {
			pick[d] = e.Path
		}
	}
	return forge.Rules(pick, func(f string) string { return BaseFile(api, token, ref, f) })
}

// Skills copies the review skills of the base branch, from the first of dirs that exists, to dst/repo, and
// those of the owner's .github repo (default branch, shared by all the owner's repos, default dirs) to dst/owner.
// Pi lists them; the repo wins on a name clash. The skills are the rules a repo owner opts into: one
// <name>/SKILL.md or <name>.md each, or a symlink to a dev skill elsewhere in the repo
// (e.g. go-style -> ../../.agents/skills/go-style).
func Skills(e *forge.Event, token, ref string, dirs []string, dst string) {
	calls := 60 // API calls per source: bounds symlink loops and big skill folders
	copyFirst("/repos/"+e.Repository.FullName, token, "?ref="+ref, dirs, filepath.Join(dst, "repo"), &calls)
	owner, name, _ := strings.Cut(e.Repository.FullName, "/")
	if name == ".github" {
		return
	}
	jwt, err := AppJWT()
	if err != nil {
		return
	}
	var tok struct {
		Token string `json:"token"`
	}
	// Fails when the App isn't installed on <owner>/.github: then there are no shared skills.
	if Call("POST", fmt.Sprintf("/app/installations/%d/access_tokens", e.Installation.ID), jwt, map[string]any{
		"repositories": []string{".github"}, "permissions": map[string]string{"contents": "read"},
	}, &tok) != nil {
		return
	}
	calls = 60
	copyFirst("/repos/"+owner+"/.github", tok.Token, "", review.Settings{}.SkillPaths(), filepath.Join(dst, "owner"), &calls)
}

// copyFirst copies the first of ps that exists to dst.
func copyFirst(api, token, ref string, ps []string, dst string, calls *int) {
	for _, p := range ps {
		if copyBase(api, token, ref, p, dst, calls); proc.Exists(dst) {
			return
		}
	}
}

// copyBase copies the file or directory p of a repo to dst and follows symlinks that stay inside the repo.
func copyBase(api, token, ref, p, dst string, calls *int) {
	if *calls--; *calls < 0 {
		return
	}
	var raw json.RawMessage
	if Call("GET", api+"/contents/"+p+ref, token, nil, &raw) != nil || len(raw) == 0 {
		return
	}
	if raw[0] == '[' {
		var es []struct{ Name, Path, Type string }
		if json.Unmarshal(raw, &es) != nil || os.MkdirAll(dst, 0o755) != nil {
			return
		}
		for _, x := range es {
			if x.Type != "submodule" && x.Name != "" && !strings.ContainsAny(x.Name, "/\\") && x.Name != ".." {
				copyBase(api, token, ref, x.Path, filepath.Join(dst, x.Name), calls)
			}
		}
		return
	}
	var f struct{ Type, Target, Content, Encoding string }
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	switch {
	case f.Type == "symlink": // GitHub resolves links to files itself; this one points at a directory
		// Inside the repo and not an ancestor (a loop); other loops end with the call budget.
		t := path.Join(path.Dir(p), f.Target)
		if !path.IsAbs(f.Target) && t != ".." && !strings.HasPrefix(t, "../") && t != "." && !strings.HasPrefix(p+"/", t+"/") {
			copyBase(api, token, ref, t, dst, calls)
		}
	case f.Type == "file" && f.Encoding == "base64":
		if b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(f.Content, "\n", "")); err == nil {
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				log.Println("skill", p+":", err)
			} else if err := os.WriteFile(dst, b, 0o644); err != nil {
				log.Println("skill", p+":", err)
			}
		}
	}
}

// LoadKey parses the App's private key, base64 PEM (PKCS#1 or PKCS#8).
func LoadKey(b64 string) (*rsa.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(raw)
	if blk == nil {
		return nil, errors.New("no PEM block")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an RSA key")
	}
	return rk, nil
}

// AppJWT signs the App's JWT, which mints installation tokens.
func AppJWT() (string, error) {
	b64 := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	now := time.Now().Unix()
	data := b64(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." +
		b64(map[string]any{"iat": now - 60, "exp": now + 540, "iss": AppID})
	sum := sha256.Sum256([]byte(data))
	sig, err := rsa.SignPKCS1v15(nil, AppKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return data + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Redeliver asks GitHub to resend webhooks of the last 6 hours that never got a 2xx (e.g. 404 while the
// container was down): GitHub doesn't retry on its own. Stale ones are harmless, the review job reloads the PR.
func Redeliver() {
	jwt, err := AppJWT()
	if err != nil {
		log.Println("redeliver:", err)
		return
	}
	type delivery struct {
		ID          int64     `json:"id"`
		GUID        string    `json:"guid"`
		Status      int       `json:"status_code"`
		DeliveredAt time.Time `json:"delivered_at"`
	}
	var ds []delivery
	for url := "/app/hook/deliveries?per_page=100"; url != ""; { // newest first, pages until older than 6 h
		b, next, err := ghDoNext("GET", url, jwt, "application/vnd.github+json", nil)
		var page []delivery
		if err == nil {
			err = json.Unmarshal(b, &page)
		}
		if err != nil {
			log.Println("redeliver:", err)
			return
		}
		ds = append(ds, page...)
		if len(page) == 0 || time.Since(page[len(page)-1].DeliveredAt) > 6*time.Hour {
			break
		}
		url = next
	}
	done := map[string]bool{} // a redelivery keeps the guid
	for _, d := range ds {
		done[d.GUID] = done[d.GUID] || d.Status/100 == 2
	}
	for _, d := range ds {
		if done[d.GUID] || time.Since(d.DeliveredAt) > 6*time.Hour {
			continue
		}
		done[d.GUID] = true
		if err := Call("POST", fmt.Sprintf("/app/hook/deliveries/%d/attempts", d.ID), jwt, nil, nil); err != nil {
			log.Println("redeliver:", err)
			continue
		}
		log.Println("redelivered", d.GUID, "after", d.Status)
	}
}

// InstallToken is scoped to the event's repo only. statuses is false when the app lacks "Commit statuses: write".
func InstallToken(e *forge.Event) (token string, statuses bool, err error) {
	perms := map[string]string{"contents": "read", "pull_requests": "write", "statuses": "write"}
	if token, err = Mint(e, perms); err == nil {
		return token, true, nil
	}
	delete(perms, "statuses") // review without the badge
	token, err = Mint(e, perms)
	return token, false, err
}

// ReadToken only reads the event's repo: what git fetches with, through the vault.
func ReadToken(e *forge.Event) (string, error) {
	return Mint(e, map[string]string{"contents": "read"})
}

// Mint returns an installation token for the event's repo only.
func Mint(e *forge.Event, perms map[string]string) (string, error) {
	jwt, err := AppJWT()
	if err != nil {
		return "", err
	}
	var tok struct {
		Token string `json:"token"`
	}
	err = Call("POST", fmt.Sprintf("/app/installations/%d/access_tokens", e.Installation.ID), jwt, map[string]any{
		"repository_ids": []int64{e.Repository.ID}, "permissions": perms,
	}, &tok)
	return tok.Token, err
}

// SetStatus sets the diffharbor commit status, the badge in the PR's checks box.
func SetStatus(api, sha, token, state, desc string) {
	if err := Call("POST", api+"/statuses/"+sha, token, map[string]string{"state": state, "context": "diffharbor", "description": desc}, nil); err != nil {
		log.Println("status:", err)
	}
}

// graphql runs a GraphQL query; out gets the "data" object (out may be nil).
func graphql(token, query string, vars map[string]any, out any) error {
	var r struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := Call("POST", "/graphql", token, map[string]any{"query": query, "variables": vars}, &r); err != nil {
		return err
	}
	if len(r.Errors) > 0 {
		return errors.New("graphql: " + r.Errors[0].Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Data, out)
}

// OpenPRs lists the repository's open PRs but n, the 50 updated last, with their first 100 files.
func OpenPRs(repo string, n int, token string) ([]report.Other, error) {
	owner, name, _ := strings.Cut(repo, "/")
	var d struct {
		Repository struct {
			PullRequests struct {
				Nodes []struct {
					Number  int    `json:"number"`
					Title   string `json:"title"`
					URL     string `json:"url"`
					IsDraft bool   `json:"isDraft"`
					Author  struct {
						Login string `json:"login"`
					} `json:"author"`
					Files struct {
						Nodes []struct {
							Path string `json:"path"`
						} `json:"nodes"`
					} `json:"files"`
				} `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"repository"`
	}
	err := graphql(token, `query($o:String!,$r:String!){repository(owner:$o,name:$r){pullRequests(states:OPEN,first:50,orderBy:{field:UPDATED_AT,direction:DESC}){
nodes{number title url isDraft author{login} files(first:100){nodes{path}}}}}}`, map[string]any{"o": owner, "r": name}, &d)
	if err != nil {
		return nil, err
	}
	var prs []report.Other
	for _, p := range d.Repository.PullRequests.Nodes {
		if p.Number == n {
			continue
		}
		pr := report.Other{Number: p.Number, Title: p.Title, URL: p.URL, Author: p.Author.Login, Draft: p.IsDraft}
		for _, f := range p.Files.Nodes {
			pr.Files = append(pr.Files, f.Path)
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

// Patches is a PR's diff by file, as GitHub's files API has it: hunks only, none for binary or huge files.
func Patches(api string, n int, token string) (map[string]string, error) {
	fs, err := All[struct {
		Filename string `json:"filename"`
		Patch    string `json:"patch"`
	}](fmt.Sprintf("%s/pulls/%d/files?per_page=100", api, n), token)
	if err != nil {
		return nil, err
	}
	ps := map[string]string{}
	for _, f := range fs {
		ps[f.Filename] = f.Patch
	}
	return ps, nil
}

// Threads returns our threads on the PR.
func Threads(repo string, n int, token string) ([]forge.Thread, error) {
	owner, name, _ := strings.Cut(repo, "/")
	var ts []forge.Thread
	var after *string
	for range 30 {
		var d struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []struct {
							ID         string `json:"id"`
							IsResolved bool   `json:"isResolved"`
							Comments   struct {
								Nodes []struct {
									DatabaseID   int64  `json:"databaseId"`
									Path         string `json:"path"`
									Line         int    `json:"line"`
									OriginalLine int    `json:"originalLine"`
									Body         string `json:"body"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		err := graphql(token, `query($o:String!,$r:String!,$n:Int!,$a:String){repository(owner:$o,name:$r){pullRequest(number:$n){
reviewThreads(first:100,after:$a){pageInfo{hasNextPage endCursor} nodes{id isResolved comments(first:1){nodes{databaseId path line originalLine body}}}}}}}`,
			map[string]any{"o": owner, "r": name, "n": n, "a": after}, &d)
		if err != nil {
			return nil, err
		}
		rt := d.Repository.PullRequest.ReviewThreads
		for _, t := range rt.Nodes {
			if len(t.Comments.Nodes) == 0 || !strings.Contains(t.Comments.Nodes[0].Body, forge.Mark) {
				continue
			}
			c := t.Comments.Nodes[0]
			ts = append(ts, forge.Thread{ID: t.ID, Comment: c.DatabaseID, Resolved: t.IsResolved, Path: c.Path, Line: c.Line, Original: c.OriginalLine, Body: c.Body})
		}
		if !rt.PageInfo.HasNextPage {
			break
		}
		after = &rt.PageInfo.EndCursor
	}
	return ts, nil
}

// Resolve answers a thread whose finding a later commit fixed, then resolves it.
func Resolve(e *forge.Event, api string, n int, token string, t forge.Thread, sha string) {
	err := Call("POST", fmt.Sprintf("%s/pulls/%d/comments/%d/replies", api, n, t.Comment), token,
		map[string]string{"body": fmt.Sprintf("✅ Addressed in `%s`\n\n%s", sha[:7], forge.Mark)}, nil)
	if err == nil {
		err = ResolveThread(e, t.ID)
	}
	if err != nil {
		log.Println("resolve:", err)
	}
}

// ResolveThread needs Contents: write, GitHub refuses it to an app with Pull requests: write only. So it gets its
// own token, which stays in this process: the review's token reaches git, which runs as node.
func ResolveThread(e *forge.Event, id string) error {
	token, err := Mint(e, map[string]string{"contents": "write", "pull_requests": "write"})
	if err != nil {
		return fmt.Errorf("token with Contents: write (app permission): %w", err)
	}
	return graphql(token, `mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{id}}}`, map[string]any{"id": id}, nil)
}
