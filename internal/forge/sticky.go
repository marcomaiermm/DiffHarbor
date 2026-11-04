package forge

import (
	"diffharbor/internal/diff"
	"diffharbor/internal/review"
	"diffharbor/internal/text"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// State is what the walkthrough comment remembers between reviews, stored in the comment itself.
type State struct {
	SHA     string            `json:"sha"` // last reviewed head
	Summary string            `json:"summary"`
	Files   map[string]string `json:"files"`
	Diagram string            `json:"diagram"`
	Effort  int               `json:"effort"`
	Patches map[string]string `json:"patches"`         // diff.PatchIDs at SHA: what a push has to change to get reviewed
	Large   bool              `json:"large,omitempty"` // someone asked for all parts: later pushes get them too
	// The report's reading order and the questions for a person, from the last full review that asked for them.
	Flow      []review.Step `json:"flow,omitempty"`
	Decisions []string      `json:"decisions,omitempty"`
	Story     review.Story  `json:"story,omitzero"`
	Report    string        `json:"report,omitempty"` // the report's URL, "" = none
	// The report folder's secret in its URL. Here, as only who can read the PR sees this comment; kept across pushes,
	// restarts and a close, so a reopened PR's report comes back at its old link.
	Secret string `json:"secret,omitempty"`
}

const stickyMark = "<!-- diffharbor:walkthrough -->"

var stateTag = regexp.MustCompile(`<!-- diffharbor:state ([A-Za-z0-9+/=]+) -->`)

// Sticky is our walkthrough comment on the PR; id 0 means not posted yet.
type Sticky struct {
	pr      PR
	id      int64
	State   State
	Ask     string        // AskLarge's box under the banner, "" = none
	Outline *diff.Outline // of this review's whole diff: tests and mechanical edits folded away; nil = not known yet
}

const largeMark = "<!-- diffharbor:large -->"

var largeBox = regexp.MustCompile(`(?m)^- \[([ xX])\] .*` + largeMark)

// AskLarge offers a review of the changed files in parts. On GitHub (box) it is a box: ticking it edits our
// comment, which GitHub allows only users with write access, and sends an issue_comment "edited" webhook.
// GitLab lets only the author tick boxes in a comment, so there it asks for a `@bot review all` comment.
func AskLarge(files, all, parts int, box bool, bot string) string {
	what := fmt.Sprintf("all %d files", all)
	if files < all {
		what = fmt.Sprintf("%d of %d files", files, all)
	}
	if !box {
		return fmt.Sprintf("Comment `@%s review all` to **review %s** in %d parts (about %d× the time and tokens)", bot, what, parts, parts)
	}
	return fmt.Sprintf("- [ ] **Review %s** in %d parts (about %d× the time and tokens) %s", what, parts, parts, largeMark)
}

// LargeTicked reports whether an edit of our walkthrough from → to ticked the AskLarge box.
func LargeTicked(from, to string) bool {
	f, t := largeBox.FindStringSubmatch(from), largeBox.FindStringSubmatch(to)
	return f != nil && t != nil && f[1] == " " && t[1] != " "
}

// LoadSticky finds the walkthrough comment. Only our bot's comment counts: its state decides what gets re-reviewed.
func LoadSticky(pr PR) (*Sticky, error) {
	ns, err := pr.Notes()
	if err != nil {
		return nil, err
	}
	s := &Sticky{pr: pr}
	for _, n := range ns {
		if n.Ours && strings.Contains(n.Body, stickyMark) {
			s.id, s.State = n.ID, parseState(n.Body)
			break
		}
	}
	return s, nil
}

func parseState(body string) State {
	var st State
	if m := stateTag.FindStringSubmatch(body); m != nil {
		if b, err := base64.StdEncoding.DecodeString(m[1]); err == nil {
			_ = json.Unmarshal(b, &st)
		}
	}
	return st
}

var effort = []string{"", "1 (Trivial)", "2 (Simple)", "3 (Moderate)", "4 (Complex)", "5 (Very complex)"}

// shown is the list items a walkthrough section shows; the report has all.
const shown = 15

// render: banner on top, what a person has to look at, then the walkthrough of the last finished review with
// tests, data, docs and mechanical edits folded, the state hidden at the end.
func (st State) render(banner, ask, footer string, o *diff.Outline) string {
	var b strings.Builder
	b.WriteString(stickyMark + "\n")
	if banner != "" {
		b.WriteString("> " + banner + "\n\n")
	}
	if ask != "" {
		b.WriteString(ask + "\n\n")
	}
	if o == nil {
		o = &diff.Outline{}
	}
	if st.SHA != "" {
		if st.Report != "" {
			fmt.Fprintf(&b, "📖 **[Read this PR with the report](%s)**: reading order with the diff, commits, tests as cases\n\n", st.Report)
		}
		if s := st.Story; s.Before != "" && s.After != "" {
			fmt.Fprintf(&b, "**Before:** %s\n**After:** %s\n\n", text.OneLine(s.Before, 300), text.OneLine(s.After, 300))
		}
		if len(st.Decisions) > 0 {
			b.WriteString("**For you to decide**\n")
			for _, d := range st.Decisions {
				b.WriteString("- " + text.OneLine(d, 300) + "\n")
			}
			b.WriteString("\n")
		}
		if len(o.Weakened) > 0 {
			b.WriteString("**⚠️ Tests to check**\n")
			list(&b, o.Weakened, func(f diff.Flag) string { return "`" + f.At + "` " + f.What })
		}
		b.WriteString("<details>\n<summary><b>Walkthrough</b></summary>\n\n" + st.Summary + "\n\n")
		skip := o.Skippable()
		kinds := map[string][]string{}
		for _, f := range slices.Sorted(maps.Keys(st.Files)) {
			if !skip[f] {
				kinds[diff.Kind(f)] = append(kinds[diff.Kind(f)], f)
			}
		}
		table := func(fs []string) {
			b.WriteString("| File | Change |\n|---|---|\n")
			for _, f := range fs {
				fmt.Fprintf(&b, "| `%s` | %s |\n", f, strings.ReplaceAll(st.Files[f], "|", `\|`))
			}
			b.WriteString("\n")
		}
		if code := kinds["code"]; len(code) > 0 {
			table(code)
		}
		if tests := kinds["test"]; len(tests) > 0 || len(o.Added) > 0 {
			fmt.Fprintf(&b, "<details>\n<summary>🧪 Tests: %d files, %d new cases, %d deleted</summary>\n\n", len(tests), len(o.Added), len(o.Removed))
			if len(o.Added) > 0 {
				list(&b, o.Added, func(c string) string { return c })
			}
			if len(tests) > 0 {
				table(tests)
			}
			b.WriteString("</details>\n\n")
		}
		if other := slices.Concat(kinds["data"], kinds["doc"]); len(other) > 0 {
			fmt.Fprintf(&b, "<details>\n<summary>📄 Data and docs: %d files</summary>\n\n", len(other))
			table(other)
			b.WriteString("</details>\n\n")
		}
		if len(o.Mechanical) > 0 {
			fmt.Fprintf(&b, "<details>\n<summary>🔁 Mechanical: %d files to skim</summary>\n\n", len(skip))
			list(&b, o.Mechanical, func(g diff.Group) string {
				return fmt.Sprintf("%s (%d): `%s`", g.What, len(g.Files), strings.Join(g.Files[:min(len(g.Files), 5)], "`, `"))
			})
			b.WriteString("</details>\n\n")
		}
		if st.Diagram != "" {
			b.WriteString("<details>\n<summary>Sequence diagram</summary>\n\n```mermaid\n" + st.Diagram + "\n```\n\n</details>\n\n")
		}
		if st.Effort > 0 && st.Effort < len(effort) {
			b.WriteString("**Estimated review effort:** 🎯 " + effort[st.Effort] + "\n\n")
		}
		b.WriteString("</details>\n\n")
	}
	if footer != "" {
		b.WriteString("<sub>" + footer + "</sub>\n")
	}
	j, _ := json.Marshal(st) //nolint:gosec // the report's secret is for who reads the PR, as this comment
	b.WriteString("<!-- diffharbor:state " + base64.StdEncoding.EncodeToString(j) + " -->")
	return b.String()
}

// list writes up to shown items as a markdown list, then how many more.
func list[T any](b *strings.Builder, items []T, line func(T) string) {
	for _, it := range items[:min(len(items), shown)] {
		b.WriteString("- " + line(it) + "\n")
	}
	if len(items) > shown {
		fmt.Fprintf(b, "- … %d more\n", len(items)-shown)
	}
	b.WriteString("\n")
}

// Update folds a review into the state. Incremental reviews only see some files: the rest of the walkthrough stays.
func (st State) Update(r review.Result, sha string, prFiles []string, full bool) State {
	files := map[string]string{}
	for _, f := range prFiles {
		if c, ok := st.Files[f]; ok {
			files[f] = c
		}
		if c, ok := r.Walkthrough[f]; ok {
			files[f] = text.OneLine(c, 200)
		}
	}
	d := strings.TrimSpace(r.Diagram)
	d = strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(d, "```mermaid"), "```"), "```", "")
	if full || st.Summary == "" {
		st.Summary = r.Summary
	}
	if full || st.Diagram == "" {
		st.Diagram = text.Truncate(strings.TrimSpace(d), 4000)
	}
	if full || st.Effort == 0 {
		st.Effort = min(max(r.Effort, 0), 5)
	}
	if full || st.Flow == nil {
		st.Flow, st.Decisions, st.Story = r.Flow, r.Decisions, r.Story
	}
	st.SHA, st.Files = sha, files
	return st
}

// Write posts or edits the walkthrough comment. Failures only log: the review itself still counts.
func (s *Sticky) Write(banner, footer string) {
	id, err := s.pr.Note(s.id, s.State.render(banner, s.Ask, footer, s.Outline))
	if err != nil {
		log.Println("walkthrough:", err)
		return
	}
	s.id = id
}
