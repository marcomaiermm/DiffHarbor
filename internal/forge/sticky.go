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
	Risk    *Risk             `json:"risk,omitempty"`   // nil in a sticky from before it existed
	Patches map[string]string `json:"patches"`          // diff.PatchIDs at SHA: what a push has to change to get reviewed
	Large   bool              `json:"large,omitempty"`  // someone asked for all parts: later pushes get them too
	Drafts  bool              `json:"drafts,omitempty"` // someone asked for a review of the draft: drafts opt_in reviews its pushes
	// The report's reading order and the questions for a person, from the last full review that asked for them.
	Flow      []review.Step     `json:"flow,omitempty"`
	Decisions []review.Decision `json:"decisions,omitempty"`
	Story     review.Story      `json:"story,omitzero"`
	Report    string            `json:"report,omitempty"` // the report's URL, "" = none
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

// Risk is the merge risk after a review: 0 (nothing open) to 5 (a critical finding open), and what it counts.
type Risk struct {
	Score int    `json:"score"`
	Why   string `json:"why"`
}

// Risked scores what is still open: cs, the open findings (a folded one counts as a nit, one with no severity as
// a minor), and failing, the tests and packages that fail. 5 a critical one; 4 two majors or anything failing;
// 3 one major; 2 a minor; 1 a nit; 0 nothing.
func Risked(cs []review.Comment, failing int) Risk {
	var n [4]int // nit, minor, major, critical
	for _, c := range cs {
		sev := review.Rank(c.Severity)
		if sev == 0 {
			sev = review.Rank("minor")
		}
		if c.Fold {
			sev = 1
		}
		n[sev-1]++
	}
	var why []string
	if failing > 0 {
		why = append(why, text.Count(failing, "failing test"))
	}
	for i, w := range slices.Backward(review.Severities) {
		if n[i] > 0 {
			why = append(why, text.Count(n[i], w))
		}
	}
	r := Risk{Why: strings.Join(why, ", ")}
	switch {
	case n[3] > 0:
		r.Score = 5
	case failing > 0 || n[2] > 1:
		r.Score = 4
	case n[2] == 1:
		r.Score = 3
	case n[1] > 0:
		r.Score = 2
	case n[0] > 0:
		r.Score = 1
	}
	return r
}

// String is the score with its band's colour, 🟢 0-1, 🟡 2-3, 🔴 4-5, and what is open.
func (r Risk) String() string {
	s := fmt.Sprintf("%s %d/5", []string{"🟢", "🟡", "🔴"}[min(max(r.Score, 0), 5)/2], r.Score)
	if r.Why != "" {
		s += " · " + r.Why + " open"
	}
	return s
}

// shown is the list items a walkthrough section shows; the report has all.
const shown = 15

// render: banner on top, what a person has to look at, then the walkthrough of the last finished review with
// tests, data, docs and mechanical edits folded, the state hidden at the end.
func (st State) render(banner, ask, footer string, o *diff.Outline) string {
	var b strings.Builder
	b.WriteString(stickyMark + "\n")
	if banner != "" {
		b.WriteString("> [!NOTE]\n> " + banner + "\n\n")
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
				b.WriteString("- " + text.OneLine(d.Text, 300) + "\n")
			}
			b.WriteString("\n")
		}
		if len(o.Weakened) > 0 {
			b.WriteString("**⚠️ Tests to check**\n" + list(o.Weakened, func(f diff.Flag) string { return "`" + f.At + "` " + f.What }) + "\n\n")
		}
		var w strings.Builder // the walkthrough
		w.WriteString(st.Summary + "\n\n")
		skip := o.Skippable()
		kinds := map[string][]string{}
		for _, f := range slices.Sorted(maps.Keys(st.Files)) {
			if !skip[f] {
				kinds[diff.Kind(f)] = append(kinds[diff.Kind(f)], f)
			}
		}
		if code := kinds["code"]; len(code) > 0 { // folded like the sections below: as many rows as a list shows
			t := "| File | Change |\n|---|---|"
			for _, f := range code[:min(len(code), shown)] {
				t += fmt.Sprintf("\n| `%s` | %s |", f, strings.ReplaceAll(st.Files[f], "|", `\|`))
			}
			if len(code) > shown {
				t += fmt.Sprintf("\n\n… %d more files", len(code)-shown)
			}
			w.WriteString(fold("📂 <strong>Changes: "+text.Count(len(code), "file")+"</strong>", t, false, false) + "\n\n")
		}
		if tests := kinds["test"]; len(tests) > 0 || len(o.Added) > 0 {
			var sum []string
			if len(tests) > 0 {
				sum = append(sum, text.Count(len(tests), "file"))
			}
			sum = append(sum, text.Count(len(o.Added), "new case"))
			if len(o.Removed) > 0 {
				sum = append(sum, fmt.Sprintf("%d deleted", len(o.Removed)))
			}
			// the case, not its file: diff.Outline writes "path: case"
			cases := list(o.Added, func(c string) string { _, n, _ := strings.Cut(c, ": "); return n })
			w.WriteString(fold("🧪 <strong>Tests: "+strings.Join(sum, ", ")+"</strong>", cases, false, false) + "\n\n")
		}
		if other := slices.Concat(kinds["data"], kinds["doc"]); len(other) > 0 {
			w.WriteString(fold("📄 <strong>Data and docs: "+text.Count(len(other), "file")+"</strong>",
				list(other, func(f string) string { return "`" + f + "`" }), false, false) + "\n\n")
		}
		if len(o.Mechanical) > 0 {
			w.WriteString(fold("🔁 <strong>Mechanical: "+text.Count(len(skip), "file")+" to skim</strong>", list(o.Mechanical, func(g diff.Group) string {
				s := fmt.Sprintf("%s (%d): `%s`", g.What, len(g.Files), strings.Join(g.Files[:min(len(g.Files), 5)], "`, `"))
				if len(g.Files) > 5 {
					s += ", …"
				}
				return s
			}), false, false) + "\n\n")
		}
		if st.Diagram != "" {
			w.WriteString(fold("🔀 <strong>Sequence diagram</strong>", "```mermaid\n"+st.Diagram+"\n```", false, false) + "\n\n")
		}
		if st.Effort > 0 && st.Effort < len(effort) {
			w.WriteString("**Estimated code review effort:** " + effort[st.Effort] + "\n\n")
		}
		b.WriteString(fold("<strong>Walkthrough</strong>", strings.TrimSpace(w.String()), true, false) + "\n\n")
		if st.Risk != nil {
			fmt.Fprintf(&b, "**Merge Risk:** %s · up to `%s`\n\n", st.Risk, st.SHA[:min(len(st.SHA), 7)])
		}
	}
	if footer != "" {
		b.WriteString("<sub>" + footer + "</sub>\n")
	}
	j, _ := json.Marshal(st) //nolint:gosec // the report's secret is for who reads the PR, as this comment
	b.WriteString("<!-- diffharbor:state " + base64.StdEncoding.EncodeToString(j) + " -->")
	return b.String()
}

// list is up to shown items as a markdown list, then how many more.
func list[T any](items []T, line func(T) string) string {
	var out []string
	for _, it := range items[:min(len(items), shown)] {
		out = append(out, "- "+line(it))
	}
	if len(items) > shown {
		out = append(out, fmt.Sprintf("- … %d more", len(items)-shown))
	}
	return strings.Join(out, "\n")
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
