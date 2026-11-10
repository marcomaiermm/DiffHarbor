package knowledge

import (
	"bytes"
	"cmp"
	"context"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"diffharbor/internal/ticket"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
)

// Remote is an adapter the operator set in KNOWLEDGE_ADAPTERS: an HTTP service (contract v1, see the README).
// Without a URL the entry only lets its owner use a built-in that has a server-wide key (linear).
type Remote struct{ URL, Token string }

// maxBody is the most a remote adapter answers.
const maxBody = 1 << 20

// plain talks to a remote adapter: no redirects, which could carry the token to another host.
var plain = &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// Lookup posts the request to the adapter. Errors never carry the token.
func (a Remote) Lookup(ctx context.Context, r Request) ([]Section, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.URL, bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("bad request")
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := plain.Do(req)
	if err != nil {
		return nil, fmt.Errorf("remote: %w", err) // a url.Error: the URL, not the header
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, fmt.Errorf("remote: status %d", res.StatusCode)
	}
	b, err = io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil || len(b) > maxBody {
		return nil, fmt.Errorf("remote: no answer within %d bytes", maxBody)
	}
	var out struct{ Sections []Section }
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, errors.New("remote: answer is not the contract's JSON")
	}
	return out.Sections, nil
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// Remotes are KNOWLEDGE_ADAPTERS' entries for owner (forge.Event.Owner), by name: "<owner|*> <name> <url> <token>",
// split by commas or newlines; an owner's own entry beats * for its name. "<owner|*> linear" without url and token
// lets the owner use LINEAR_API_KEY. Every entry is checked, "" checks them all.
func Remotes(owner string) (map[string]Remote, error) {
	out := map[string]Remote{}
	for i, e := range strings.FieldsFunc(os.Getenv("KNOWLEDGE_ADAPTERS"), func(r rune) bool { return r == ',' || r == '\n' }) {
		f := strings.Fields(e)
		var u *url.URL
		if len(f) == 4 {
			u, _ = url.Parse(f[2])
		}
		switch {
		case len(f) == 2 && f[1] == "linear":
		case len(f) == 4 && nameRe.MatchString(f[1]) && !slices.Contains([]string{"docs", "wiki", "linear", "itsaplan"}, f[1]) &&
			u != nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "":
		default:
			return nil, fmt.Errorf(`KNOWLEDGE_ADAPTERS entry %d: want "<owner|*> <name> <url> <token>" or "<owner|*> linear"`, i+1) // never the entry
		}
		if _, ok := out[f[1]]; f[0] == "*" && ok || f[0] != "*" && !strings.EqualFold(f[0], owner) {
			continue
		}
		r := Remote{}
		if len(f) == 4 {
			r = Remote{URL: f[2], Token: f[3]}
		}
		out[f[1]] = r
	}
	return out, nil
}

// maxMD and maxDocs bound what docs reads of the checkout.
const (
	maxMD   = 256 << 10
	maxDocs = 300
)

// notDocs are dirs whose markdown isn't the project's own.
var notDocs = map[string]bool{"node_modules": true, "vendor": true, "third_party": true, "testdata": true}

// history is a changelog: what changed when, never out of date, and often written by a release tool.
var history = regexp.MustCompile(`(?i)^(changelog|changes|history|news)\.md$`)

// docs reads the repo's markdown from the pinned checkout (Sandbox.Pin, before the job's code ran), as the files at
// the merge base: those the PR changes are skipped, the diff shows them. Symlinks too. Keep this before Analyze: its
// checks and Pi run the PR's code in the checkout.
func (e Env) docs(ctx context.Context, r Request, changed []string) ([]Section, error) {
	root, err := e.Sb.Pinned()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var out []Section
	n := 0
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			return nil //nolint:nilerr // an unreadable directory: read the rest
		case d.IsDir():
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || notDocs[d.Name()]) {
				return fs.SkipDir
			}
			return nil
		case n >= maxDocs:
			return fs.SkipAll
		case d.Type()&fs.ModeSymlink != 0 || !strings.EqualFold(rel[max(0, len(rel)-3):], ".md") || history.MatchString(d.Name()) ||
			slices.Contains(changed, rel):
			return nil
		}
		src, err := diff.ReadIn(root, rel, maxMD)
		if err != nil {
			return nil //nolint:nilerr // left out, like a large file
		}
		n++
		out = append(out, onTopic(Split(File{ID: rel, Title: rel, URL: e.Blob + rel, Text: string(src), GitLab: e.GitLab}), r)...)
		return nil
	})
	return out, err
}

// onTopic is the sections a ref names or an entity: what the core would keep, so less of a big repo goes round.
func onTopic(ss []Section, r Request) []Section {
	return slices.DeleteFunc(ss, func(s Section) bool {
		n, _ := matches(s, r.Entities)
		return n == 0 && !explicit(s, r.Refs)
	})
}

// wiki is the repo's wiki pages, when the forge has them.
func (e Env) wiki(ctx context.Context, r Request) ([]Section, error) {
	if e.Wiki == nil {
		return nil, nil
	}
	// the forge's call takes no ctx: stop waiting for it at the deadline (the goroutine ends with its own timeout)
	type res struct {
		files []File
		err   error
	}
	ch := make(chan res, 1)
	go func() {
		defer proc.Recovered(func(err error) { ch <- res{err: err} })
		f, err := e.Wiki()
		ch <- res{f, err}
	}()
	var w res
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case w = <-ch:
	}
	files, err := w.files, w.err
	var out []Section
	for _, f := range files {
		out = append(out, onTopic(Split(f), r)...)
	}
	return out, err
}

// linearURL is Linear's GraphQL API.
var linearURL = "https://api.linear.app/graphql"

var (
	linearID = regexp.MustCompile(`^[\w-]{1,64}$`)
	// linearLink is a document's link, its slug id at the end: https://linear.app/<workspace>/document/<title>-<slug id>.
	linearLink = regexp.MustCompile(`^https://linear\.app/[\w-]+/document/(?:[\w-]*-)?([0-9a-f]{12})(?:[/?#].*)?$`)
)

// maxSearches is the Linear searches a review makes: its entities are in a fixed order, the first count.
const maxSearches = 5

// linear is Linear's documents: the ones refs name by id or link (document), and a search (searchDocuments) for the
// first entities. Both fields are in Linear's GraphQL schema: github.com/linear/linear packages/sdk/src/schema.graphql.
func linear(ctx context.Context, r Request) ([]Section, error) {
	if ticket.LinearKey == "" {
		return nil, nil
	}
	const fields = "id slugId title url content updatedAt"
	type doc struct{ ID, SlugID, Title, URL, Content, UpdatedAt string }
	var out []Section
	add := func(d doc) {
		out = append(out, Split(File{ID: d.ID, Title: d.Title, URL: d.URL, Updated: d.UpdatedAt, Text: d.Content})...)
	}
	// by Linear's id and slug id, the two a ref can name: a document refs, links and a search find is read once
	seen := map[string]bool{}
	// a ref to the whole document first: all its sections are then explicit, a ref to one of them still finds it
	refs := slices.Clone(r.Refs)
	slices.SortStableFunc(refs, func(a, b string) int {
		return cmp.Compare(b2i(strings.Contains(a, "#")), b2i(strings.Contains(b, "#")))
	})
	for _, ref := range refs {
		page, _, _ := strings.Cut(ref, "#") // the document; the anchor names a section of it
		id := page
		if m := linearLink.FindStringSubmatch(ref); m != nil {
			id = m[1] // a link the PR names: its id goes to Linear's API, the link is never opened
		} else if !linearID.MatchString(page) { // another URL, or another adapter's ref: not ours
			continue
		}
		if seen[id] { // read for another ref already
			continue
		}
		var res struct{ Document *doc }
		if err := gql(ctx, "query($id: String!) { document(id: $id) { "+fields+" } }", map[string]any{"id": id}, &res); err == nil && res.Document != nil &&
			!seen[res.Document.ID] {
			d := *res.Document
			seen[d.ID], seen[d.SlugID] = true, true
			d.ID = page // explicit() compares with the ref: a slug id or a link, and a section's ref with its anchor
			add(d)
		} // a ref that isn't a document of Linear's: skipped
	}
	for _, e := range r.Entities[:min(len(r.Entities), maxSearches)] {
		var res struct{ SearchDocuments struct{ Nodes []doc } }
		if err := gql(ctx, "query($t: String!) { searchDocuments(term: $t, first: 3) { nodes { "+fields+" } } }", map[string]any{"t": e.Name}, &res); err != nil {
			return out, err // what was found so far stays: Lookup keeps it
		}
		for _, d := range res.SearchDocuments.Nodes {
			if !seen[d.ID] {
				seen[d.ID], seen[d.SlugID] = true, true
				add(d)
			}
		}
	}
	return out, nil
}

// gql runs a query as ticket.go does (a personal API key goes without "Bearer"); GraphQL errors are errors.
func gql(ctx context.Context, query string, vars map[string]any, data any) error {
	b, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, "POST", linearURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", ticket.LinearKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	defer res.Body.Close()
	var out struct {
		Data   json.RawMessage
		Errors []struct{ Message string }
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&out); res.StatusCode != 200 || err != nil || len(out.Errors) > 0 {
		return fmt.Errorf("linear: status %d", res.StatusCode)
	}
	return json.Unmarshal(out.Data, data)
}
