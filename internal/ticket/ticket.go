// Package ticket finds the itsaplan or Linear issue a PR names and turns it into a prompt section.
package ticket

import (
	"bytes"
	"context"
	"diffharbor/internal/text"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"slices"
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

// Lookup returns the first issue of projects (nil = any) texts name, its id and a prompt section; "" if none
// resolves. Up to 3 more issues the texts name ("closes", "see", links in the description) follow it as context.
func Lookup(ctx context.Context, projects []string, texts ...string) (id, section string) {
	provider, err := Active()
	if provider == "" || err != nil {
		return "", ""
	}
	seen := map[string]bool{}
	var refs strings.Builder
	nrefs := 0
	for _, t := range texts {
		for _, m := range ticketKey.FindAllStringSubmatch(strings.ReplaceAll(t, "_", " "), -1) { // "_" is a word character: \b misses "feat_WID-60_roles"
			key := strings.ToUpper(m[1]) + "-" + m[2]
			if seen[key] || len(seen) == 6 || nrefs == 3 || projects != nil && !slices.ContainsFunc(projects, func(p string) bool { return strings.EqualFold(p, m[1]) }) {
				continue // at most 6 lookups: "UTF-8" matches the pattern too
			}
			seen[key] = true
			var body string
			if provider == "linear" {
				body, err = linear(ctx, key)
			} else {
				body, err = itsaplan(ctx, strings.ToUpper(m[1]), m[2])
			}
			switch {
			case err != nil:
				log.Println("ticket:", err)
				return id, section + refs.String()
			case body == "":
			case id == "":
				id, section = key, text.Truncate(fmt.Sprintf("## Linked ticket %s: %s", key, body), 8000)+"\n\n"
			default:
				nrefs++
				refs.WriteString(text.Truncate(fmt.Sprintf("## Referenced ticket %s (context, not what this PR must do): %s", key, body), 2000) + "\n\n")
			}
		}
	}
	return id, section + refs.String()
}

// itsaplan returns the issue as "title\n\ndescription..."; "" with a nil error if there is no such ticket, or not ours.
func itsaplan(ctx context.Context, project, n string) (string, error) {
	var is struct {
		Title, Description string
		Checklists         []struct {
			Title string
			Items []struct {
				Content string
				Done    bool
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/projects/%s/issues/%s", URL, project, n), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", APIKey)
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("itsaplan: %w", err)
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(&is); res.StatusCode != 200 || err != nil { // 404/403: no such ticket, or not ours
		return "", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s\n", is.Title, is.Description)
	for _, c := range is.Checklists {
		fmt.Fprintf(&b, "\n### %s\n\n", c.Title)
		for _, i := range c.Items {
			fmt.Fprintf(&b, "- [%s] %s\n", map[bool]string{true: "x", false: " "}[i.Done], i.Content)
		}
	}
	return b.String(), nil
}

// linear returns the issue (Linear takes the "ENG-123" identifier as id) as "title\n\ndescription"; "" with a nil
// error if there is no such issue, or not ours. Checklists are markdown in the description.
func linear(ctx context.Context, id string) (string, error) {
	q, _ := json.Marshal(map[string]any{
		"query":     "query($id: String!) { issue(id: $id) { title description } }",
		"variables": map[string]string{"id": id},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", linearURL, bytes.NewReader(q))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", LinearKey) // a personal API key goes without "Bearer"
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("linear: %w", err)
	}
	defer res.Body.Close()
	var out struct {
		Data struct {
			Issue *struct{ Title, Description string }
		}
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); res.StatusCode != 200 || err != nil || out.Data.Issue == nil {
		return "", nil
	}
	return out.Data.Issue.Title + "\n\n" + out.Data.Issue.Description + "\n", nil
}
