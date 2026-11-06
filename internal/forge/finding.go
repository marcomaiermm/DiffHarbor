package forge

import (
	"cmp"
	"diffharbor/internal/review"
	"fmt"
	"html"
	"slices"
	"strings"
)

var badge = map[string]string{"critical": "🔴 Critical", "major": "🟠 Major", "minor": "🟡 Minor"}

// Finding is a Comment as posted: Start..End are the new-file lines its fix replaces as a suggestion, 0 = none.
type Finding struct {
	review.Comment
	Start, End int
}

// Format renders one finding: severity, title, explanation, a folded prompt for an AI agent. suggestion opens
// the forge's suggestion block for Start..End: "suggestion" on GitHub, "suggestion:-1+0" on GitLab.
func Format(c Finding, suggestion string) string {
	s := "_" + cmp.Or(badge[c.Severity], c.Severity) + "_"
	if c.Title != "" {
		s += " · **" + c.Title + "**"
	}
	s += "\n\n" + c.Body
	if c.Fix != "" && c.Start > 0 { // the forge shows a button that commits the fix
		f := "```"
		for strings.Contains(c.Fix, f) {
			f += "`"
		}
		s += "\n\n" + f + suggestion + "\n" + c.Fix + "\n" + f
	} else if c.Fix != "" && c.Existing != "" && c.Existing != c.Fix { // no place for a suggestion: what it replaces too
		d := prefixed("-", c.Existing) + "\n" + prefixed("+", c.Fix)
		s += "\n\n**Suggested fix:**\n\n" + strings.Replace(review.Fence(d), "\n", "diff\n", 1)
	} else if c.Fix != "" {
		s += "\n\n**Suggested fix:**\n\n" + review.Fence(c.Fix)
	}
	if c.Prompt != "" {
		s += "\n\n<details>\n<summary>🤖 Prompt for AI Agents</summary>\n\n" + review.Fence(c.Prompt) + "\n\n</details>"
	}
	return s
}

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
	c.Existing, c.Fix = strings.TrimSuffix(c.Existing, "\n"), strings.TrimSuffix(c.Fix, "\n")
	if c.Existing == "" || c.Fix == "" || c.Existing == c.Fix {
		return
	}
	want := strings.Split(c.Existing, "\n")
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

// Split sorts a review's findings: the ones on diff lines (lines) to post inline, placed, and the rest. Lines
// that already have one of threads get nothing new.
func Split(r review.Result, threads []Thread, lines map[string]string) (inline, rest []Finding) {
	seen := map[string]bool{}
	for _, t := range threads {
		if t.Line > 0 {
			seen[fmt.Sprintf("%s:%d", t.Path, t.Line)] = true
		}
	}
	for _, rc := range r.Comments { // review.Analyze caps them: 10 per part
		c := Finding{Comment: rc}
		at := fmt.Sprintf("%s:%d", c.Path, c.Line)
		if seen[at] {
			continue
		}
		if _, ok := lines[at]; ok {
			Place(&c, lines)
			inline = append(inline, c)
		} else {
			c.Existing, c.Fix = strings.TrimSuffix(c.Existing, "\n"), strings.TrimSuffix(c.Fix, "\n") // as Place does
			rest = append(rest, c)
		}
	}
	return inline, rest
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

// Summary is the review's top comment: how many findings, the ones outside the diff in full, all prompts.
func Summary(inline, rest []Finding, footer string) string {
	parts := []string{fmt.Sprintf("**Actionable comments posted: %d**", len(inline)+len(rest))}
	if len(inline)+len(rest) == 0 {
		parts[0] = "**No actionable comments were generated in this review.**"
	}
	if len(rest) > 0 { // inline comments only go on diff lines
		out := fmt.Sprintf("> [!CAUTION]\n> Some comments are outside the diff and can't be posted inline.\n\n<details open>\n<summary><b>⚠️ Outside diff range comments (%d)</b></summary>", len(rest))
		for _, c := range rest {
			out += fmt.Sprintf("\n\n<details open>\n<summary>%s · <code>%s:%d</code></summary>\n\n%s\n\n</details>",
				html.EscapeString(cmp.Or(c.Title, c.Severity)), html.EscapeString(c.Path), c.Line, Format(c, ""))
		}
		parts = append(parts, out+"\n\n</details>")
	}
	if p := allPrompts(append(slices.Clone(inline), rest...)); p != "" {
		parts = append(parts, p)
	}
	parts = append(parts, "<details>\n<summary>ℹ️ Review info</summary>\n\n<sub>"+footer+"</sub>\n\n</details>\n\n"+Mark)
	return strings.Join(parts, "\n\n")
}
