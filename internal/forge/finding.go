package forge

import (
	"cmp"
	"diffharbor/internal/review"
	"fmt"
	"html"
	"slices"
	"strings"
)

var badge = map[string]string{"critical": "🔴 Critical", "major": "🟠 Major", "minor": "🟡 Minor", "nit": "🔵 Nit"}

// Finding is a Comment as posted: Start..End are the new-file lines its fix replaces as a suggestion, 0 = none.
// OnDiff: it is on a diff line, so one in the summary is there because the forge didn't take it inline.
type Finding struct {
	review.Comment
	Start, End int
	OnDiff     bool
}

// Format renders one finding: severity, title, explanation, a folded prompt for an AI agent. suggestion opens
// the forge's suggestion block for Start..End: "suggestion" on GitHub, "suggestion:-1+0" on GitLab; "" where none
// can go (the summary), placed or not.
func Format(c Finding, suggestion string) string {
	s := "_" + cmp.Or(badge[c.Severity], c.Severity) + "_"
	if c.Title != "" {
		s += " · **" + c.Title + "**"
	}
	s += "\n\n" + c.Body
	// f is "" when the fix is a blank line: c.Fix says whether there is one.
	e, f := trim(c.Existing), trim(c.Fix)
	if c.Fix != "" && c.Start > 0 && suggestion != "" { // the forge shows a button that commits the fix
		fence := "```"
		for strings.Contains(f, fence) {
			fence += "`"
		}
		s += "\n\n" + fence + suggestion + "\n" + f + "\n" + fence
	} else if c.Fix != "" && c.Existing != "" && e != f { // no place for a suggestion: what it replaces too
		d := prefixed("-", e) + "\n" + prefixed("+", f)
		s += "\n\n**Suggested fix:**\n\n" + strings.Replace(review.Fence(d), "\n", "diff\n", 1)
	} else if c.Fix != "" {
		s += "\n\n**Suggested fix:**\n\n" + review.Fence(f)
	}
	if c.Prompt != "" {
		s += "\n\n<details>\n<summary>🤖 Prompt for AI Agents</summary>\n\n" + review.Fence(c.Prompt) + "\n\n</details>"
	}
	return s
}

// trim drops the newline a model ends Existing and Fix with. Only Format and Place's match trim, once each.
func trim(s string) string { return strings.TrimSuffix(s, "\n") }

// prefixed puts p before each of s's lines.
func prefixed(p, s string) string {
	return p + strings.ReplaceAll(s, "\n", "\n"+p)
}

// Parse reads severity, title and the verifier's evidence back from a finding Format posted. A body Format
// did not write comes back as the body alone.
func Parse(body string) review.Comment {
	c := review.Comment{Body: body}
	for sev, b := range badge {
		if rest, ok := strings.CutPrefix(body, "_"+b+"_"); ok {
			c.Severity, c.Body = sev, rest
			if t, ok := strings.CutPrefix(rest, " · **"); ok {
				if title, after, ok := strings.Cut(t, "**"); ok {
					c.Title, c.Body = title, after
				}
			}
			break
		}
	}
	c.Body = strings.TrimSpace(c.Body)
	if _, e, ok := strings.Cut(c.Body, review.Verified); ok {
		c.Evidence, _, _ = strings.Cut(e, "\n")
	}
	return c
}

// Place finds the lines c.Existing quotes in the diff, nearest to c.Line and at most 10 lines from it, and sets
// c.Start and c.End to them. No match leaves 0: the fix then shows as a plain code block.
func Place(c *Finding, lines map[string]string) {
	if c.Existing == "" || c.Fix == "" || trim(c.Existing) == trim(c.Fix) {
		return
	}
	want := strings.Split(trim(c.Existing), "\n")
	best := 11
	for s := c.Line - len(want) - 10; s <= c.Line+10; s++ {
		matches := true
		for i, w := range want {
			l, ok := lines[fmt.Sprintf("%s:%d", c.Path, s+i)]
			if !ok || strings.TrimRight(l, " \t\r") != strings.TrimRight(w, " \t\r") {
				matches = false
				break
			}
		}
		e := s + len(want) - 1
		if d := max(s-c.Line, c.Line-e, 0); matches && d < best {
			best, c.Start, c.End = d, s, e
		}
	}
}

// Split sorts a review's findings: the ones on diff lines (lines) to post inline, placed, the ones outside the
// diff, and the folded ones (Comment.Fold), anywhere. Lines that already have one of threads get nothing new.
func Split(r review.Result, threads []Thread, lines map[string]string) (inline, outside, folded []Finding) {
	seen := map[string]bool{}
	for _, t := range threads {
		if t.Line > 0 {
			seen[fmt.Sprintf("%s:%d", t.Path, t.Line)] = true
		}
	}
	for _, rc := range r.Comments { // review.Analyze caps them: 10 and 5 nits per pass
		c := Finding{Comment: rc}
		at := fmt.Sprintf("%s:%d", c.Path, c.Line)
		if seen[at] {
			continue
		}
		_, ok := lines[at]
		switch {
		case c.Fold:
			folded = append(folded, c)
		case ok:
			c.OnDiff = true
			Place(&c, lines)
			inline = append(inline, c)
		default:
			outside = append(outside, c)
		}
	}
	return inline, outside, folded
}

// section lists findings in full under a heading, each in its own block; open shows them unfolded.
func section(title string, cs []Finding, open bool) string {
	attr := ""
	if open {
		attr = " open"
	}
	out := fmt.Sprintf("<details%s>\n<summary><b>%s</b></summary>", attr, title)
	for _, c := range cs {
		out += fmt.Sprintf("\n\n<details%s>\n<summary>%s · <code>%s:%d</code></summary>\n\n%s\n\n</details>",
			attr, html.EscapeString(cmp.Or(c.Title, c.Severity)), html.EscapeString(c.Path), c.Line, Format(c, ""))
	}
	return out + "\n\n</details>"
}

// foldedTitle names the folded findings by what they are: nits, under quiet minor ones too.
func foldedTitle(cs []Finding) string {
	nits := 0
	for _, c := range cs {
		if c.Severity == "nit" {
			nits++
		}
	}
	switch nits {
	case 0:
		return "Minor comments"
	case len(cs):
		return "Nitpicks"
	}
	return "Minor comments and nitpicks"
}

// allPrompts folds every finding's prompt into one block to paste into an agent.
func allPrompts(cs []Finding) string {
	var ps []string
	for _, c := range cs {
		if c.Prompt != "" {
			ps = append(ps, c.Prompt)
		}
	}
	if len(ps) == 0 {
		return ""
	}
	sep := "\n" // one finding a line; a blank line once a prompt has lines of its own
	if slices.ContainsFunc(ps, func(p string) bool { return strings.Contains(p, "\n") }) {
		sep = "\n\n"
	}
	return "<details>\n<summary>🤖 Prompt for all review comments with AI agents</summary>\n\n" +
		review.Fence("Verify each finding against the current code and only fix it if needed.\n\n"+strings.Join(ps, sep)) + "\n\n</details>"
}

// Summary is the review's top comment: how many findings, the ones not posted inline in full, the folded ones
// folded, all prompts. Those outside the diff and those the forge didn't take inline (refused, dropped, not tried)
// go under their own heading.
func Summary(inline, rest, folded []Finding, footer string) string {
	parts := []string{fmt.Sprintf("**Actionable comments posted: %d**", len(inline)+len(rest))}
	if len(inline)+len(rest) == 0 {
		parts[0] = "**No actionable comments were generated in this review.**"
	}
	var outside, refused []Finding
	for _, c := range rest {
		if c.OnDiff {
			refused = append(refused, c)
		} else {
			outside = append(outside, c)
		}
	}
	if len(outside) > 0 { // inline comments only go on diff lines
		parts = append(parts, "> [!CAUTION]\n> Some comments are outside the diff and can't be posted inline.\n\n"+
			section(fmt.Sprintf("⚠️ Outside diff range comments (%d)", len(outside)), outside, true))
	}
	if len(refused) > 0 {
		parts = append(parts, "> [!WARNING]\n> Some comments are on the diff but couldn't be posted inline.\n\n"+
			section(fmt.Sprintf("💬 Comments not posted inline (%d)", len(refused)), refused, true))
	}
	if len(folded) > 0 {
		parts = append(parts, section(fmt.Sprintf("🧹 %s (%d)", foldedTitle(folded), len(folded)), folded, false))
	}
	if p := allPrompts(slices.Concat(inline, rest, folded)); p != "" {
		parts = append(parts, p)
	}
	parts = append(parts, "<details>\n<summary>ℹ️ Review info</summary>\n\n<sub>"+footer+"</sub>\n\n</details>\n\n"+Mark)
	return strings.Join(parts, "\n\n")
}
