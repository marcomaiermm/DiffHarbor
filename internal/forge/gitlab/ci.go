package gitlab

import (
	"cmp"
	"diffharbor/internal/report"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
)

// conclusions maps a finished job's status onto a check run's conclusion; any other status is pending.
var conclusions = map[string]string{"success": "success", "failed": "failure", "canceled": "cancelled", "skipped": "skipped", "manual": "skipped"}

// Checks are the jobs of sha's pipelines, a job in two (a branch and a merge request pipeline) by its newest run.
// ponytail: the newest 10 pipelines; a job only in an older one is lost, raise per_page if reruns bury it.
func (p *pr) Checks(sha string) ([]report.Check, error) {
	var ps []struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		WebURL string `json:"web_url"`
	}
	if err := call("GET", p.api+"/pipelines?order_by=id&sort=desc&per_page=10&sha="+url.QueryEscape(sha), nil, &ps); err != nil || len(ps) == 0 {
		return nil, err
	}
	var cs []report.Check
	seen := map[string]bool{}
	for _, pl := range ps {
		js, cut, err := paged[struct {
			Name         string `json:"name"`
			Status       string `json:"status"`
			AllowFailure bool   `json:"allow_failure"`
			WebURL       string `json:"web_url"`
		}](fmt.Sprintf("%s/pipelines/%d/jobs?per_page=100", p.api, pl.ID))
		if err == nil && cut {
			err = errors.New("pipeline jobs: more than 30 pages")
		}
		if err != nil {
			return nil, err // a partial list would call a failing job nothing
		}
		for _, j := range js {
			if j.Name == "diffharbor" || seen[j.Name] {
				continue
			}
			seen[j.Name] = true
			c := report.Check{Name: j.Name, Status: "pending", Workflow: cmp.Or(pl.Name, "#"+strconv.FormatInt(pl.ID, 10)), URL: cmp.Or(j.WebURL, pl.WebURL)}
			if con, ok := conclusions[j.Status]; ok {
				c.Status, c.Conclusion = "completed", con
				if con == "failure" && j.AllowFailure { // GitLab passes the pipeline with it
					c.Conclusion = "neutral"
				}
			}
			cs = append(cs, c)
		}
	}
	return cs, nil
}

// Members are the project's active members, inherited ones too, by username; bot users and ours left out.
func (p *pr) Members() ([]string, error) {
	ms, cut, err := paged[struct {
		Username string `json:"username"`
		State    string `json:"state"`
	}](p.api + "/members/all?per_page=100")
	if err == nil && cut { // a partial list would call a member gone
		err = errors.New("members: more than 30 pages")
	}
	if err != nil {
		return nil, err
	}
	var ls []string
	for _, m := range ms {
		if m.State == "active" && m.Username != Bot && !otherBot.MatchString(m.Username) {
			ls = append(ls, m.Username)
		}
	}
	slices.Sort(ls)
	return slices.Compact(ls), nil
}
