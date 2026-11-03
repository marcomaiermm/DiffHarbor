// Package ticket finds the itsaplan issue a PR names and turns it into a prompt section.
package ticket

import (
	"context"
	"diffharbor/internal/text"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// An itsaplan issue the PR names ("WID-60" in title, branch or description) goes into the review as what the PR
// must do. ITSAPLAN_URL (the API) and ITSAPLAN_API_KEY, an agent key with read access to the projects (x-api-key); unset = no ticket.
var (
	client    = &http.Client{Timeout: time.Minute}
	URL       = os.Getenv("ITSAPLAN_URL")
	APIKey    = os.Getenv("ITSAPLAN_API_KEY")
	ticketKey = regexp.MustCompile(`(?i)\b([a-z][a-z0-9]{1,9})-([0-9]{1,6})\b`)
)

// Lookup returns the first issue of projects (nil = any) texts name, its id and a prompt section; "" if none resolves.
func Lookup(ctx context.Context, projects []string, texts ...string) (id, section string) {
	if APIKey == "" || URL == "" {
		return "", ""
	}
	seen := map[string]bool{}
	for _, t := range texts {
		for _, m := range ticketKey.FindAllStringSubmatch(t, -1) {
			id = strings.ToUpper(m[1]) + "-" + m[2]
			if seen[id] || len(seen) == 3 || projects != nil && !slices.ContainsFunc(projects, func(p string) bool { return strings.EqualFold(p, m[1]) }) {
				continue // at most 3 lookups: "UTF-8" matches the pattern too
			}
			seen[id] = true
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
			req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/projects/%s/issues/%s", URL, strings.ToUpper(m[1]), m[2]), nil)
			if err != nil {
				return "", ""
			}
			req.Header.Set("x-api-key", APIKey)
			res, err := client.Do(req)
			if err != nil {
				log.Println("itsaplan:", err)
				return "", ""
			}
			err = json.NewDecoder(res.Body).Decode(&is)
			res.Body.Close()
			if res.StatusCode != 200 || err != nil { // 404/403: no such ticket, or not ours
				continue
			}
			var b strings.Builder
			fmt.Fprintf(&b, "## Linked ticket %s: %s\n\n%s\n", id, is.Title, is.Description)
			for _, c := range is.Checklists {
				fmt.Fprintf(&b, "\n### %s\n\n", c.Title)
				for _, i := range c.Items {
					fmt.Fprintf(&b, "- [%s] %s\n", map[bool]string{true: "x", false: " "}[i.Done], i.Content)
				}
			}
			return id, text.Truncate(b.String(), 8000) + "\n\n"
		}
	}
	return "", ""
}
