// Package ticket reads the itsaplan or Linear issues a PR names: whole for the report, as a prompt section for the
// review.
package ticket

import (
	"bytes"
	"cmp"
	"context"
	"diffharbor/internal/text"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A ticket the PR names ("WID-60" in title, branch or description) goes into the review as what the PR must do.
// itsaplan: ITSAPLAN_URL (the API) and ITSAPLAN_API_KEY, an agent key with read access to the projects (x-api-key).
// Linear: LINEAR_API_KEY, a personal API key (read). With both configured TICKET_PROVIDER (itsaplan or linear) must
// say which one; with one configured it is that one. Unset = no ticket.
var (
	client    = &http.Client{Timeout: time.Minute}
	URL       string
	APIKey    string
	LinearKey string
	Provider  string // TICKET_PROVIDER: itsaplan or linear
	ticketKey = regexp.MustCompile(`(?i)\b([a-z][a-z0-9]{1,9})-([0-9]{1,6})\b`)
)

var linearURL = "https://api.linear.app/graphql"

// maxReads is the tracker reads of one review, the PR's tickets first, then their follow-ups: "UTF-8" matches the
// pattern too, and a description can name dozens.
const maxReads = 20

// lookupTimeout bounds all reads of one Lookup: a hung tracker costs one wait, not one per key.
var lookupTimeout = time.Minute

// Active returns the provider tickets come from ("" = none), or an error if the settings leave it open or name one
// without its credentials.
func Active() (string, error) {
	var have []string
	if APIKey != "" && URL != "" {
		have = append(have, "itsaplan")
	}
	if LinearKey != "" {
		have = append(have, "linear")
	}
	switch {
	case Provider != "" && !slices.Contains(have, Provider):
		return "", fmt.Errorf("TICKET_PROVIDER %q: want itsaplan or linear, with its credentials set", Provider)
	case Provider != "":
		return Provider, nil
	case len(have) > 1:
		return "", fmt.Errorf("itsaplan and linear are both configured: set TICKET_PROVIDER to one of them")
	case len(have) == 1:
		return have[0], nil
	}
	return "", nil
}

// Named is the keys of projects (nil = any) the texts name, upper case, in order, each once.
func Named(projects []string, texts ...string) []string {
	var keys []string
	for _, t := range texts {
		for _, m := range ticketKey.FindAllStringSubmatch(strings.ReplaceAll(t, "_", " "), -1) { // "_" is a word character: \b misses "feat_WID-60_roles"
			key := strings.ToUpper(m[1]) + "-" + m[2]
			if !slices.Contains(keys, key) && (projects == nil || slices.ContainsFunc(projects, func(p string) bool { return strings.EqualFold(p, m[1]) })) {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

// Found is what Lookup read.
type Found struct {
	Keys      []string          // the PR's ticket keys as the texts name them, less those the tracker doesn't know
	Tickets   []Ticket          // those read: open first, then done, by key
	FollowUps []Ticket          // the description's follow-ups that aren't the PR's tickets, by key
	Failed    map[string]string // key -> why the tracker didn't answer for it
	Capped    bool              // keys past maxReads went unread
	ID        string            // the first ticket's key; "" if none
	Section   string            // the review prompt's ticket section
}

// Lookup reads every ticket of projects (nil = any) that title, branch, the description and the commit subjects
// name, and the follow-ups the description lists. A ticket's error is kept in Failed and the others are read on.
// The prompt section is the first ticket title, branch or description names, cut at 8,000 characters, and up to 3
// more they name ("closes", "see", links) at 2,000 as context; commits only add to the report.
func Lookup(ctx context.Context, projects []string, title, branch, body, subjects string) Found {
	follow, rest := followUps(body)
	keys := Named(projects, title, branch, rest)
	prompted := len(keys)
	for _, k := range Named(projects, subjects) {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	f := Found{Keys: keys}
	provider, err := Active()
	if provider == "" || err != nil {
		return f
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	r := reader{provider: provider, columns: map[string]map[int]column{}}
	f.Keys = nil
	reads := 0
	read := func(key string) (*Ticket, string, bool) { // known: false when the tracker has no such ticket
		if reads == maxReads {
			f.Capped = true
			return nil, "", true
		}
		reads++
		t, prompt, err := r.get(ctx, key)
		if err != nil {
			log.Println("ticket:", key, err)
			if f.Failed == nil {
				f.Failed = map[string]string{}
			}
			f.Failed[key] = err.Error()
			return nil, "", true
		}
		return t, prompt, t != nil
	}
	seen := map[string]bool{} // the tickets' own keys: two names of one ticket read it once
	var refs strings.Builder
	nrefs := 0
	lost := false // a prompted key went unread before the linked ticket was set: the next is no stand-in for it
	for i, k := range keys {
		t, prompt, known := read(k)
		if !known {
			continue
		}
		if t == nil && i < prompted && f.ID == "" {
			lost = true
		}
		f.Keys = append(f.Keys, k)
		if t == nil || seen[t.Key] {
			continue
		}
		seen[t.Key] = true
		f.Tickets = append(f.Tickets, *t)
		switch {
		case i >= prompted:
		case f.ID == "" && !lost:
			f.ID, f.Section = k, text.Truncate(fmt.Sprintf("## Linked ticket %s: %s", k, prompt), 8000)+"\n\n"
		case nrefs < 3:
			nrefs++
			refs.WriteString(text.Truncate(fmt.Sprintf("## Referenced ticket %s (context, not what this PR must do): %s", k, prompt), 2000) + "\n\n")
		}
	}
	f.Section += refs.String()
	for _, k := range Named(projects, follow) {
		if slices.Contains(keys, k) {
			continue // a PR's ticket is no follow-up
		}
		if t, _, _ := read(k); t != nil && !seen[t.Key] {
			seen[t.Key] = true
			f.FollowUps = append(f.FollowUps, *t)
		}
	}
	slices.SortFunc(f.Tickets, func(a, b Ticket) int {
		return cmp.Or(cmpBool(a.Done, b.Done), byKey(a.Key, b.Key))
	})
	slices.SortFunc(f.FollowUps, func(a, b Ticket) int { return byKey(a.Key, b.Key) })
	return f
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case b:
		return -1
	}
	return 1
}

// byKey orders keys by project, then by number: BIS-99 before BIS-100.
func byKey(a, b string) int {
	pa, na, _ := strings.Cut(a, "-")
	pb, nb, _ := strings.Cut(b, "-")
	ia, _ := strconv.Atoi(na)
	ib, _ := strconv.Atoi(nb)
	return cmp.Or(strings.Compare(pa, pb), cmp.Compare(ia, ib))
}

// reader reads tickets from one provider; columns caches each itsaplan project's states by column id.
type reader struct {
	provider string
	columns  map[string]map[int]column
}

type column struct{ Name, StateType string }

// get returns the ticket and its prompt text ("title\n\ndescription..."); nil with a nil error if the tracker has
// no such ticket, or not one of ours.
func (r reader) get(ctx context.Context, key string) (*Ticket, string, error) {
	var (
		t      *Ticket
		prompt string
		err    error
	)
	if r.provider == "linear" {
		t, prompt, err = linear(ctx, key)
	} else {
		t, prompt, err = r.itsaplan(ctx, key)
	}
	if t != nil {
		if t.Key == "" {
			t.Key = key
		}
		if t.Key != key {
			t.Was = key // the tracker answered an older key with the ticket it moved to
		}
		var done []string
		t.Outcome, done, t.Notes = contract(t.Body)
		t.DoneWhen = append(done, t.DoneWhen...) // itsaplan's "Done when" checklist follows the section
		t.Read = time.Now()
	}
	return t, prompt, err
}

// fetch decodes the JSON answer of req into v; found is false on 404 and 403: no such ticket, or not ours.
func fetch(req *http.Request, v any) (found bool, err error) {
	res, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case 200:
	case 403, 404:
		return false, nil
	default:
		return false, fmt.Errorf("%s", res.Status)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(v); err != nil {
		return false, err
	}
	return true, nil
}

func (r reader) itsaplan(ctx context.Context, key string) (*Ticket, string, error) {
	project, n, _ := strings.Cut(key, "-")
	var is struct {
		Identifier, Title, Description, DueDate string
		ColumnID                                int
		Links                                   []struct {
			Kind, Direction string
			Issue           struct{ Identifier string }
		}
		Checklists []struct {
			Title string
			Items []struct {
				Content string
				Done    bool
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/projects/%s/issues/%s", URL, project, n), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("x-api-key", APIKey)
	if found, err := fetch(req, &is); !found || err != nil {
		return nil, "", wrap("itsaplan", err)
	}
	cols, err := r.states(ctx, project)
	if err != nil {
		return nil, "", wrap("itsaplan: project "+project, err)
	}
	col := cols[is.ColumnID]
	t := &Ticket{
		Key: is.Identifier, Title: is.Title, State: col.Name, Body: is.Description,
		Done: col.StateType == "completed" || col.StateType == "canceled",
	}
	t.Due, _ = time.Parse(time.DateOnly, is.DueDate)
	for _, l := range is.Links {
		switch {
		case l.Kind != "blocks":
		case l.Direction == "inward":
			t.BlockedBy = append(t.BlockedBy, l.Issue.Identifier)
		default:
			t.Blocks = append(t.Blocks, l.Issue.Identifier)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s\n", is.Title, is.Description)
	for _, c := range is.Checklists {
		fmt.Fprintf(&b, "\n### %s\n\n", c.Title)
		for _, i := range c.Items {
			fmt.Fprintf(&b, "- [%s] %s\n", map[bool]string{true: "x", false: " "}[i.Done], i.Content)
			if title(c.Title) == "done when" {
				t.DoneWhen = append(t.DoneWhen, i.Content) // after the description's: see get
			}
		}
	}
	return t, b.String(), nil
}

// states reads a project's columns once per review: an issue names only its column.
func (r reader) states(ctx context.Context, project string) (map[int]column, error) {
	if cols, ok := r.columns[project]; ok {
		if cols == nil {
			return nil, fmt.Errorf("failed earlier in this review")
		}
		return cols, nil
	}
	var p struct {
		Columns []struct {
			ID int
			column
		}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/projects/%s", URL, project), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", APIKey)
	found, err := fetch(req, &p)
	if !found && err == nil {
		err = fmt.Errorf("not found")
	}
	if err != nil {
		r.columns[project] = nil // read once: an outage of the project costs one call, not one per ticket
		return nil, err
	}
	cols := map[int]column{}
	for _, c := range p.Columns {
		cols[c.ID] = c.column
	}
	r.columns[project] = cols
	return cols, nil
}

func wrap(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", what, err)
}

// linear reads the issue (Linear takes the "ENG-123" identifier as id, also a key it had before a move). Its
// checklists are markdown in the description.
func linear(ctx context.Context, key string) (*Ticket, string, error) {
	q, _ := json.Marshal(map[string]any{
		"query": `query($id: String!) { issue(id: $id) { identifier title description url dueDate previousIdentifiers
state { name type } relations(first: 50) { nodes { type relatedIssue { identifier } } }
inverseRelations(first: 50) { nodes { type issue { identifier } } } } }`,
		"variables": map[string]string{"id": key},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", linearURL, bytes.NewReader(q))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", LinearKey) // a personal API key goes without "Bearer"
	req.Header.Set("Content-Type", "application/json")
	type rel struct {
		Type         string
		RelatedIssue struct{ Identifier string }
		Issue        struct{ Identifier string }
	}
	var out struct {
		Errors []struct{ Message string }
		Data   struct {
			Issue *struct {
				Identifier, Title, Description, URL, DueDate string
				PreviousIdentifiers                          []string
				State                                        struct{ Name, Type string }
				Relations, InverseRelations                  struct{ Nodes []rel }
			}
		}
	}
	if found, err := fetch(req, &out); !found || err != nil || out.Data.Issue == nil {
		// GraphQL answers 200 with errors: one that isn't "not found" is a failure, not an unknown ticket
		for _, e := range out.Errors {
			if err == nil && !strings.Contains(strings.ToLower(e.Message), "not found") {
				err = errors.New(e.Message)
			}
		}
		return nil, "", wrap("linear", err)
	}
	if len(out.Errors) > 0 { // an issue with fields Linear failed to read: not the whole ticket
		return nil, "", fmt.Errorf("linear: partial: %s", out.Errors[0].Message)
	}
	is := out.Data.Issue
	t := &Ticket{
		Key: is.Identifier, Title: is.Title, State: is.State.Name, URL: is.URL, Body: is.Description,
		Done: is.State.Type == "completed" || is.State.Type == "canceled",
	}
	t.Due, _ = time.Parse(time.DateOnly, is.DueDate)
	if n := len(is.PreviousIdentifiers); n > 0 && is.Identifier == key {
		t.Was = is.PreviousIdentifiers[n-1]
	}
	for _, r := range is.Relations.Nodes {
		if r.Type == "blocks" {
			t.Blocks = append(t.Blocks, r.RelatedIssue.Identifier)
		}
	}
	for _, r := range is.InverseRelations.Nodes {
		if r.Type == "blocks" {
			t.BlockedBy = append(t.BlockedBy, r.Issue.Identifier)
		}
	}
	return t, is.Title + "\n\n" + is.Description + "\n", nil
}

var (
	heading = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.+?)[ \t#]*$`)
	item    = regexp.MustCompile(`^(?:[-*+]|\d{1,9}[.)])[ \t]+(?:\[[ xX]\][ \t]+)?(.*)$`)
	followT = regexp.MustCompile(`^follow[- ]?ups?$`)
)

// section is a markdown heading, lines[from], and the lines under it up to the next heading of its level or above.
type section struct {
	title    string
	from, to int
}

// sections finds the headings of lines outside code fences.
func sections(lines []string) []section {
	var ss []section
	var levels []int
	fence := false
	for i, l := range lines {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
		} else if m := heading.FindStringSubmatch(l); m != nil && !fence {
			ss, levels = append(ss, section{title(m[2]), i, len(lines)}), append(levels, len(m[1]))
		}
	}
	for i := range ss {
		for j := i + 1; j < len(ss); j++ {
			if levels[j] <= levels[i] {
				ss[i].to = ss[j].from
				break
			}
		}
	}
	return ss
}

// title is a heading's or checklist's name to compare: "**Done when:**" is "done when".
func title(s string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.Trim(strings.TrimSpace(s), "*_"), ":")))
}

func lines(md string) []string { return strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") }

// contract reads a description's sections "Outcome", "Done when" (its list items) and "Notes", the first of each.
func contract(md string) (outcome string, done []string, notes string) {
	ls := lines(md)
	var hasDone bool
	for _, s := range sections(ls) {
		body := ls[s.from+1 : s.to]
		switch {
		case s.title == "outcome" && outcome == "":
			outcome = strings.TrimSpace(strings.Join(body, "\n"))
		case s.title == "done when" && !hasDone:
			hasDone, done = true, items(body)
		case s.title == "notes" && notes == "":
			notes = strings.TrimSpace(strings.Join(body, "\n"))
		}
	}
	return outcome, done, notes
}

// items is the top-level list items of lines, a checkbox cut off; indented lines under an item, a nested list
// among them, join it on new lines.
func items(ls []string) []string {
	var out []string
	base := -1
	for _, l := range ls {
		t := strings.TrimSpace(l)
		indent := len(l) - len(strings.TrimLeft(l, " \t"))
		m := item.FindStringSubmatch(t)
		switch {
		case m != nil && (base < 0 || indent <= base):
			base = indent
			out = append(out, strings.TrimSpace(m[1]))
		case t != "" && len(out) > 0 && indent > base:
			out[len(out)-1] += "\n" + t
		}
	}
	return out
}

// followUps cuts the description's follow-up section ("## Follow-ups") out of it: its keys are follow-ups, the
// rest's are the PR's tickets.
func followUps(body string) (list, rest string) {
	ls := lines(body)
	for _, s := range sections(ls) {
		if followT.MatchString(s.title) {
			list = strings.Join(ls[s.from+1:s.to], "\n")
			return list, strings.Join(slices.Delete(ls, s.from, s.to), "\n")
		}
	}
	return "", body
}

// Ticket is a tracker issue the PR names, read whole: the page shows every ticket uncut, the prompt keeps its cuts.
// Outcome, DoneWhen and Notes are the ticket's contract, from its sections or checklists; Body is the description
// as the tracker has it, for the contract pass.
type Ticket struct {
	Key, Was          string // Was: its key before the tracker moved it ("PYR-228" for "BIS-228"); "" if none
	Title, State, URL string
	Done              bool      // State is one of the tracker's done states
	Due               time.Time // zero: no due date
	Outcome           string
	DoneWhen          []string
	Notes             string
	Body              string
	Blocks, BlockedBy []string  // keys
	Read              time.Time // when the tracker answered
}
