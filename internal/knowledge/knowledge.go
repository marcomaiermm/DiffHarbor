// Package knowledge finds the doc sections that mention what a PR changes and hands them to the review as reference
// material. The server asks the adapters the repo's review.json names, before the model runs: the model never
// searches docs and holds no token for them.
package knowledge

import (
	"cmp"
	"context"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
	"diffharbor/internal/review"
	"diffharbor/internal/text"
	"fmt"
	"log"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Entity is a name the PR touches that a doc may mention. Hop 0 is the diff itself.
type Entity struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // "symbol", "path" or "config"
	Hop  int    `json:"hop"`
}

// Request is what an adapter gets (the remote contract's body, version 1): fields are only ever added.
type Request struct {
	Version  int      `json:"version"`
	Repo     string   `json:"repo"`     // "owner/name"
	Refs     []string `json:"refs"`     // URLs the PR names, and review.json's refs for this adapter: data, never fetched by the core
	Entities []Entity `json:"entities"` // at most maxEntities, in a fixed order
	MaxChars int      `json:"max_chars"`
}

// Section is a part of a doc.
type Section struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Updated string `json:"updated"` // RFC 3339 or ""
	Text    string `json:"text"`    // markdown
}

// Adapter finds sections for a request. It may return false positives: Lookup filters.
type Adapter interface {
	Lookup(ctx context.Context, r Request) ([]Section, error)
}

type lookupFunc func(ctx context.Context, r Request) ([]Section, error)

func (f lookupFunc) Lookup(ctx context.Context, r Request) ([]Section, error) { return f(ctx, r) }

const (
	maxEntities = 40
	secChars    = 2000 // a section's text
	budget      = 8000 // all sections' text: the ticket's
	timeout     = 10 * time.Second
)

// Env is what a review gives Lookup.
type Env struct {
	Key, Owner, Repo string
	Diff             string                 // the PR's diff
	Texts            []string               // PR body, commits, ticket: where refs come from
	Sb               *proc.Sandbox          // docs reads its pinned checkout
	Blob             string                 // the forge's URL of a repo file: Blob + path
	GitLab           bool                   // the forge is GitLab: its heading anchors
	Wiki             func() ([]File, error) // the repo's wiki; nil without
	Redact           func(string) string    // forge.Redact: on all that goes into the prompt
}

// Lookup asks the adapters picks name (review.json's "name" or "name:ref") and returns the sections to read, in
// prompt order. An adapter's error or timeout costs what that adapter didn't return yet, no more.
func Lookup(ctx context.Context, env Env, picks []string) []review.Doc {
	remotes, err := Remotes(env.Owner) // main checked the env
	if err != nil {
		return nil
	}
	refs := map[string][]string{}
	for _, p := range picks {
		name, ref, _ := strings.Cut(p, ":")
		if _, ok := refs[name]; !ok {
			refs[name] = nil // the name alone: no ref, still asked
		}
		if ref != "" {
			refs[name] = append(refs[name], ref)
		}
	}
	ents, urls := Entities(env.Diff), URLs(env.Texts...)
	if ents == nil { // the contract says arrays: [], never null
		ents = []Entity{}
	}
	for i, u := range urls {
		urls[i] = env.Redact(u)
	}
	changed := diff.Paths(env.Diff)
	var names []string
	var ads []Adapter
	for _, n := range slices.Sorted(maps.Keys(refs)) {
		var a Adapter
		switch r, ok := remotes[n]; {
		case ok && r.URL != "":
			a = r
		case n == "docs":
			a = lookupFunc(func(ctx context.Context, q Request) ([]Section, error) { return env.docs(ctx, q, changed) })
		case n == "wiki":
			a = lookupFunc(env.wiki)
		case n == "linear" && ok: // a url-less entry: this owner may use the server's key
			a = lookupFunc(linear)
		default:
			log.Println(env.Key, "knowledge: no adapter", n)
			continue
		}
		names, ads = append(names, n), append(ads, a)
	}
	type result struct {
		secs []Section
		err  error
	}
	res := make([]result, len(names))
	var wg sync.WaitGroup
	for i := range names {
		wg.Go(func() {
			defer proc.Recovered(func(err error) { res[i].err = err })
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			res[i].secs, res[i].err = ads[i].Lookup(ctx, Request{
				Version: 1, Repo: env.Repo, Refs: append(append([]string{}, urls...), refs[names[i]]...), Entities: ents, MaxChars: secChars,
			})
		})
	}
	wg.Wait()
	var cs []cand
	returned := 0
	for i, r := range res {
		if r.err != nil { // what it returned before the error stays
			log.Println(env.Key, "knowledge:", names[i]+":", r.err)
		}
		returned += len(r.secs)
		for _, s := range r.secs {
			if s.ID == "" { // no identity: it would dedupe with every other one
				continue
			}
			if t, err := time.Parse(time.RFC3339, s.Updated); err == nil {
				s.Updated = t.Format(time.RFC3339)
			} else {
				s.Updated = "" // not a time: it goes into the prompt outside the fence
			}
			s.ID, s.Title, s.URL = env.Redact(s.ID), env.Redact(s.Title), env.Redact(s.URL)
			s.Text = text.Truncate(env.Redact(s.Text), secChars)
			cs = append(cs, cand{adapter: names[i], Section: s, refs: refs[names[i]], urls: urls})
		}
	}
	docs, chars := pick(cs, ents)
	log.Printf("%s knowledge: %s: %d sections returned, %d kept, %d chars", env.Key, strings.Join(names, ", "), returned, len(docs), chars)
	return docs
}

// cand is a returned section with what ranks it.
type cand struct {
	Section
	adapter    string
	refs, urls []string
	explicit   bool
	hop, n     int
}

// pick keeps the sections that came from an explicit ref or name an entity, once each, in the order the prompt
// reads them (an explicit ref, then hop, then entities named, then ID), whole ones up to the budget.
func pick(cs []cand, ents []Entity) (docs []review.Doc, chars int) {
	var keep []cand
	seen := map[string]bool{}
	for _, c := range cs {
		c.explicit = explicit(c.Section, append(slices.Clone(c.refs), c.urls...))
		c.n, c.hop = matches(c.Section, ents)
		if k := c.adapter + "\x00" + c.ID; (c.explicit || c.n > 0) && !seen[k] {
			seen[k] = true
			keep = append(keep, c)
		}
	}
	slices.SortFunc(keep, func(a, b cand) int {
		return cmp.Or(cmp.Compare(b2i(b.explicit), b2i(a.explicit)), cmp.Compare(a.hop, b.hop), cmp.Compare(b.n, a.n), cmp.Compare(a.ID, b.ID), cmp.Compare(a.adapter, b.adapter))
	})
	for _, c := range keep {
		if n := utf8.RuneCountInString(c.Text); chars+n <= budget {
			chars += n
			docs = append(docs, review.Doc{Adapter: c.adapter, Title: c.Title, URL: c.URL, Updated: c.Updated, Text: c.Text})
		}
	}
	return docs, chars
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// explicit reports whether s is what a ref names: its ID or URL, or a section of the doc that ID or URL is.
func explicit(s Section, refs []string) bool {
	return slices.ContainsFunc(refs, func(r string) bool {
		return r != "" && (s.ID == r || strings.HasPrefix(s.ID, r+"#") || s.URL == r || strings.HasPrefix(s.URL, r+"#"))
	})
}

// generic are file names every repo has: a doc naming "index.ts" says nothing about this one.
var generic = regexp.MustCompile(`(?i)^(index|main|mod|__init__|readme)\.\w+$`)

// matches counts the entities s's text names and returns the lowest hop of them.
func matches(s Section, ents []Entity) (n, hop int) {
	hop = 1 << 30
	for _, e := range ents {
		hit := word(s.Text, e.Name)
		if b := path.Base(e.Name); e.Kind == "path" && !hit && !generic.MatchString(b) {
			hit = word(s.Text, b)
		}
		if hit {
			n, hop = n+1, min(hop, e.Hop)
		}
	}
	return n, hop
}

// word reports whether s has n between non-word characters: "Save" is not in "SaveAll" or "unsaved".
func word(s, n string) bool {
	isWord := func(c byte) bool {
		return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
	}
	for i := 0; n != "" && i < len(s); {
		j := strings.Index(s[i:], n)
		if j < 0 {
			return false
		}
		j += i
		if e := j + len(n); (j == 0 || !isWord(s[j-1])) && (e == len(s) || !isWord(s[e])) {
			return true
		}
		i = j + 1
	}
	return false
}

var (
	// configKey is a SCREAMING_CASE name with an underscore: an env or config key.
	configKey = regexp.MustCompile(`\b[A-Z][A-Z0-9_]{2,}\b`)
	// compound is a name of more than one word, camelCase or snake_case: prose has "posts" and "Review", not these.
	compound = regexp.MustCompile(`[a-z0-9][A-Z]|[A-Za-z0-9]_[A-Za-z0-9]`)
)

// Entities are the names the diff touches, from the diff alone: config keys, then declared symbols (report.Declared,
// of more than one word: a one-word name is in every doc's prose), then changed paths; each by name, at most
// maxEntities.
func Entities(d string) []Entity {
	set := map[Entity]bool{}
	for _, p := range diff.Paths(d) {
		if !generic.MatchString(p) { // a root README.md: every repo's docs name it
			set[Entity{p, "path", 0}] = true
		}
	}
	for _, n := range report.Declared(d) {
		if len(n) >= 4 && compound.MatchString(n) {
			set[Entity{n, "symbol", 0}] = true
		}
	}
	for l := range strings.SplitSeq(d, "\n") {
		if l != "" && (l[0] == '+' || l[0] == '-') && !strings.HasPrefix(l, "+++") && !strings.HasPrefix(l, "---") {
			for _, k := range configKey.FindAllString(l[1:], -1) {
				if strings.Contains(k, "_") && !strings.HasSuffix(k, "_") {
					set[Entity{k, "config", 0}] = true
				}
			}
		}
	}
	rank := map[string]int{"config": 0, "symbol": 1, "path": 2}
	es := slices.SortedFunc(maps.Keys(set), func(a, b Entity) int {
		return cmp.Or(cmp.Compare(rank[a.Kind], rank[b.Kind]), cmp.Compare(a.Name, b.Name))
	})
	return es[:min(len(es), maxEntities)]
}

var urlRe = regexp.MustCompile("https?://[^\\s<>\"'`)\\]]+")

// URLs are the links in texts, sorted, at most 20. They go to the adapters as data: the core never fetches one,
// so a PR's text can't make the server request a URL.
func URLs(texts ...string) []string {
	set := map[string]bool{}
	for _, t := range texts {
		for _, u := range urlRe.FindAllString(t, -1) {
			if u = strings.TrimRight(u, ".,;:!?"); len(u) <= 300 {
				set[u] = true
			}
		}
	}
	us := slices.Sorted(maps.Keys(set))
	return us[:min(len(us), 20)]
}

// File is a markdown page of a source: a file of the repo, a wiki page, a document.
type File struct {
	ID, Title, URL, Updated, Text string
	GitLab                        bool // its heading anchors are GitLab's, else GitHub's
}

var (
	heading = regexp.MustCompile(`^#{1,3}\s+(.+?)[\s#]*$`)
	fenceRe = regexp.MustCompile("^\\s*(`{3,}|~{3,})")
	dashes  = regexp.MustCompile(`-{2,}`)
)

// anchor is a heading's anchor as the forge makes it: lower case, no punctuation but - and _, a space a -; GitLab
// then squeezes runs of -. Of the heading's markdown, not its rendered text: a link in a heading keeps its URL in it.
func anchor(h string, gitlab bool) string {
	a := strings.Map(func(r rune) rune {
		switch {
		case r == ' ':
			return '-'
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r):
			return unicode.ToLower(r)
		}
		return -1
	}, h)
	if gitlab {
		a = dashes.ReplaceAllString(a, "-")
	}
	return a
}

// Split cuts f at its headings (# to ###, none inside a code fence) into sections with the ID "<f.ID>#<anchor>" and
// the URL "<f.URL>#<anchor>"; the text before the first heading is "<f.ID>#" with f.URL. A heading with nothing
// under it is no section.
func Split(f File) []Section {
	var out []Section
	slugs := map[string]bool{}
	var head, slug string
	var body []string
	flush := func() {
		if t := strings.TrimSpace(strings.Join(body, "\n")); t != "" {
			title, u := f.Title, f.URL
			if head != "" {
				title += " › " + head
				t = "# " + head + "\n\n" + t
				u += "#" + slug
			}
			out = append(out, Section{ID: f.ID + "#" + slug, Title: title, URL: u, Updated: f.Updated, Text: text.Truncate(t, secChars)})
		}
		body = nil
	}
	fence := "" // the open code fence's run of ` or ~
	for l := range strings.SplitSeq(f.Text, "\n") {
		if m := fenceRe.FindStringSubmatch(l); m != nil {
			switch {
			case fence == "":
				fence = m[1]
			case m[1][0] == fence[0] && len(m[1]) >= len(fence) && strings.TrimSpace(l[len(m[0]):]) == "": // closes it
				fence = ""
			}
		}
		if m := heading.FindStringSubmatch(l); m != nil && fence == "" {
			flush()
			head, slug = m[1], anchor(m[1], f.GitLab)
			for base, i := cmp.Or(slug, "s"), 1; slug == "" || slugs[slug]; i++ { // unique as the forges number them: a repeated heading, or one of symbols only
				slug = fmt.Sprintf("%s-%d", base, i)
			}
			slugs[slug] = true
			continue
		}
		body = append(body, l)
	}
	flush()
	return out
}
