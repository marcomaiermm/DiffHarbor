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

// Checks reads sha's check runs and the commit statuses other CI posts. The runs need "Checks: read" in a token of its
// own: the review's lacks it, and an App without it still reviews. Each run's workflow ("Actions: read") and the
// statuses ("Commit statuses: read") come when the App has them, each in a token of its own, so one missing loses
// only its part.
func (p *pr) Checks(sha string) ([]report.Check, error) {
	token, err := Mint(p.e, map[string]string{"checks": "read"})
	if err != nil {
		return nil, fmt.Errorf("token with Checks: read (app permission): %w", err)
	}
	actions, _ := Mint(p.e, map[string]string{"actions": "read"})
	cs, err := checkRuns(p.api, url.PathEscape(sha), token, actions)
	if err != nil {
		return nil, err
	}
	if st, err := Mint(p.e, map[string]string{"statuses": "read"}); err == nil {
		ss, err := commitStatuses(p.api, url.PathEscape(sha), st)
		if err != nil {
			return nil, err // a partial list would call a failing status nothing
		}
		cs = append(cs, ss...)
	}
	return cs, nil
}

// commitStatuses is each context's latest status on sha, ours left out: CI that posts statuses, not check runs.
func commitStatuses(api, sha, token string) ([]report.Check, error) {
	var combined struct {
		Statuses []struct {
			Context   string `json:"context"`
			State     string `json:"state"`
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	if err := Call("GET", api+"/commits/"+sha+"/status?per_page=100", token, nil, &combined); err != nil {
		return nil, err
	}
	var cs []report.Check
	for _, s := range combined.Statuses {
		if s.Context == "diffharbor" {
			continue
		}
		c := report.Check{Name: s.Context, Status: "pending", URL: s.TargetURL}
		switch s.State {
		case "success":
			c.Status, c.Conclusion = "completed", "success"
		case "failure", "error":
			c.Status, c.Conclusion = "completed", "failure"
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// checkRuns is sha's check runs, ours left out; with actions, a token that reads workflow runs, each with its workflow.
func checkRuns(api, sha, token, actions string) ([]report.Check, error) {
	var runs struct {
		WorkflowRuns []struct {
			Name       string `json:"name"`
			Path       string `json:"path"`
			CheckSuite int64  `json:"check_suite_id"`
		} `json:"workflow_runs"`
	}
	if actions != "" {
		// best effort: without them the checks go without their workflows
		_ = Call("GET", api+"/actions/runs?per_page=100&head_sha="+sha, actions, nil, &runs)
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
