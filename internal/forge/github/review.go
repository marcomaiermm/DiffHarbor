package github

import (
	"diffharbor/internal/diff"
	"diffharbor/internal/forge"
	"diffharbor/internal/report"
	"diffharbor/internal/review"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
)

// Configure: GitHub posts the same for every repo.
func (*pr) Configure(review.Settings) {}

// Publish has nothing to send: Resolve answers at once.
func (*pr) Publish() {}

// readBack keeps the comment id of each finding of cs the review posted, matched by file and line. The ids only
// link the report: a failed read costs the links, not the review.
func (p *pr) readBack(review int64, cs []forge.Finding) {
	if review == 0 {
		return
	}
	got, err := All[struct {
		ID           int64
		Path         string
		OriginalLine int `json:"original_line"` // the line at the commit posted to: "line" is null once a push outdates it
	}](fmt.Sprintf("%s/pulls/%d/reviews/%d/comments?per_page=100", p.api, p.e.PullRequest.Number, review), p.token)
	if err != nil {
		log.Println(p.e.Key(), "review comments:", err)
		return
	}
	p.posted = map[string]int64{}
	for _, c := range cs {
		line := c.Line
		if c.Start > 0 { // a range comment sits on its last line
			line = c.End
		}
		for i, g := range got {
			if g.Path == c.Path && g.OriginalLine == line {
				p.posted[report.ThreadKey(c.Comment)] = g.ID
				got = slices.Delete(got, i, i+1)
				break
			}
		}
	}
}

// Posted is the comment ids Post read back.
func (p *pr) Posted() map[string]int64 { return p.posted }

// Post sends one review: findings on diff lines inline, the rest in the body. Returns how many findings it posted, the folded ones not.
func (p *pr) Post(sha string, r review.Result, threads []forge.Thread, raw, footer string) (int, error) {
	inline, rest, folded := forge.Split(r, threads, diff.NewLines(raw))
	p.posted = nil
	send := func(cs []forge.Finding) error {
		body := make([]map[string]any, 0, len(cs))
		for _, c := range cs {
			at := map[string]any{"path": c.Path, "line": c.Line, "side": "RIGHT", "body": forge.Format(c, "suggestion") + forge.Seal(c)}
			if c.Start > 0 {
				at["line"] = c.End
				if c.Start < c.End {
					at["start_line"], at["start_side"] = c.Start, "RIGHT"
				}
			}
			body = append(body, at)
		}
		b, err := ghDo("POST", fmt.Sprintf("%s/pulls/%d/reviews", p.api, p.e.PullRequest.Number), p.token, "application/vnd.github+json", map[string]any{
			"commit_id": sha, "event": "COMMENT", "body": forge.Summary(cs, rest, folded, footer), "comments": body,
		})
		if err == nil && len(cs) > 0 {
			var rev struct{ ID int64 }
			_ = json.Unmarshal(b, &rev) // best effort: the review is posted, only the links need its id
			p.readBack(rev.ID, cs)
		}
		return err
	}
	rejected := func(err error) bool {
		var ge *ghError
		return errors.As(err, &ge) && ge.status == 422
	}
	err := send(inline)
	if rejected(err) && slices.ContainsFunc(inline, func(c forge.Finding) bool { return c.Start > 0 }) {
		for i := range inline { // a suggestion range GitHub rejects: the findings on their own line, fixes as plain code
			inline[i].Start = 0
		}
		err = send(inline)
	}
	if rejected(err) {
		rest, inline = append(rest, inline...), nil // a line GitHub rejects: everything into the body
		err = send(nil)
	}
	if err != nil {
		return 0, err
	}
	return len(inline) + len(rest), nil
}
