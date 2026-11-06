package github

import (
	"diffharbor/internal/diff"
	"diffharbor/internal/forge"
	"diffharbor/internal/review"
	"errors"
	"fmt"
	"slices"
)

// Configure: GitHub posts the same for every repo.
func (*pr) Configure(review.Settings) {}

// Publish has nothing to send: Resolve answers at once.
func (*pr) Publish() {}

// Post sends one review: findings on diff lines inline, the rest in the body. Returns how many findings it posted, the folded ones not.
func (p *pr) Post(sha string, r review.Result, threads []forge.Thread, raw, footer string) (int, error) {
	inline, rest, folded := forge.Split(r, threads, diff.NewLines(raw))
	send := func(cs []forge.Finding) error {
		body := make([]map[string]any, 0, len(cs))
		for _, c := range cs {
			at := map[string]any{"path": c.Path, "line": c.Line, "side": "RIGHT", "body": forge.Format(c, "suggestion") + "\n\n" + forge.Mark}
			if c.Start > 0 {
				at["line"] = c.End
				if c.Start < c.End {
					at["start_line"], at["start_side"] = c.Start, "RIGHT"
				}
			}
			body = append(body, at)
		}
		return Call("POST", fmt.Sprintf("%s/pulls/%d/reviews", p.api, p.e.PullRequest.Number), p.token, map[string]any{
			"commit_id": sha, "event": "COMMENT", "body": forge.Summary(cs, rest, folded, footer), "comments": body,
		}, nil)
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
