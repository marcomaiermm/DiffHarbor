package github

import (
	"cmp"
	"diffharbor/internal/report"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Checks reads sha's check runs and the workflow runs they belong to. It needs "Checks: read" and "Actions: read",
// in a token of its own: the review's has neither, and an App without them still reviews.
func (p *pr) Checks(sha string) ([]report.Check, error) {
	token, err := Mint(p.e, map[string]string{"checks": "read", "actions": "read"})
	if err != nil {
		return nil, fmt.Errorf("token with Checks and Actions: read (app permission): %w", err)
	}
	return checkRuns(p.api, url.PathEscape(sha), token)
}

func checkRuns(api, sha, token string) ([]report.Check, error) {
	var runs struct {
		WorkflowRuns []struct {
			Name       string `json:"name"`
			Path       string `json:"path"`
			CheckSuite int64  `json:"check_suite_id"`
		} `json:"workflow_runs"`
	}
	if err := Call("GET", api+"/actions/runs?per_page=100&head_sha="+sha, token, nil, &runs); err != nil {
		return nil, err
	}
	// the first 100 runs: a check of a run past them goes without its workflow
	wf := map[int64][2]string{} // check suite -> the workflow's name and file
	for _, w := range runs.WorkflowRuns {
		// a dynamic workflow's path carries its ref: ".github/workflows/q.yml@refs/heads/main"
		wf[w.CheckSuite] = [2]string{w.Name, strings.Split(w.Path, "@")[0]}
	}
	var cs []report.Check
	next := api + "/commits/" + sha + "/check-runs?per_page=100"
	for range 10 {
		b, n, err := ghDoNext("GET", next, token, "application/vnd.github+json", nil)
		var page struct {
			CheckRuns []struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				HTMLURL    string `json:"html_url"`
				DetailsURL string `json:"details_url"`
				CheckSuite struct {
					ID int64 `json:"id"`
				} `json:"check_suite"`
			} `json:"check_runs"`
		}
		if err == nil {
			err = json.Unmarshal(b, &page)
		}
		if err != nil {
			return nil, err
		}
		for _, r := range page.CheckRuns {
			if r.Name == "diffharbor" {
				continue
			}
			c := report.Check{Name: r.Name, Status: "pending", URL: cmp.Or(r.HTMLURL, r.DetailsURL)}
			if r.Status == "completed" {
				c.Status, c.Conclusion = "completed", r.Conclusion
			}
			w := wf[r.CheckSuite.ID]
			c.Workflow, c.Path = w[0], w[1]
			cs = append(cs, c)
		}
		if next = n; next == "" {
			return cs, nil
		}
	}
	return nil, fmt.Errorf("check runs of %s: more than 10 pages", sha)
}

// Members are the repo's collaborators, by login: outside collaborators and those with access through the
// organization or a team too. "Metadata: read", which every App has.
func (p *pr) Members() ([]string, error) {
	us, cut, err := paged[struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	}](p.api+"/collaborators?per_page=100&affiliation=all", p.token)
	if err == nil && cut { // a partial list would call a member gone
		err = errors.New("collaborators: more than 30 pages")
	}
	if err != nil {
		return nil, err
	}
	var ls []string
	for _, u := range us {
		if u.Type != "Bot" {
			ls = append(ls, u.Login)
		}
	}
	slices.Sort(ls)
	return ls, nil
}
