package forge

import (
	"cmp"
	"diffharbor/internal/review"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strings"
)

var badge = map[string]string{"critical": "🔴 Critical", "major": "🟠 Major", "minor": "🟡 Minor", "nit": "⚪ Trivial"}

// marks are how a finding of severity sev starts, with what comes before its title: as Format writes it, then as it
// did before, on threads still open.
func marks(sev string) [][2]string {
	old := cmp.Or(map[string]string{"nit": "🔵 Nit"}[sev], badge[sev])
	return [][2]string{{"**" + badge[sev] + "**", "\n\n**"}, {"_" + old + "_", " · **"}}
}

// Finding is a Comment as posted: Start..End are the new-file lines its fix replaces as a suggestion, 0 = none.
// OnDiff: it is on a diff line, so one in the summary is there because the forge didn't take it inline.
type Finding struct {
	review.Comment
	Start, End int
	OnDiff     bool
	Run        string // forge.Stamp of the review that found it
}

// Format renders one finding: severity, title, explanation, the fix and a prompt for an AI agent, both folded.
// suggestion opens the forge's suggestion block for Start..End: "suggestion" on GitHub, "suggestion:-1+0" on GitLab;
// "" where none can go (the summary), placed or not. The suggestion stays unfolded: its button commits the fix.
func Format(c Finding, suggestion string) string {
	s := c.Body
	if c.Title != "" {
		s = "**" + c.Title + "**\n\n" + s
	}
	if c.Severity != "" {
		// A title is the paragraph after the severity; without one the body starts on the next line, so Parse
		// never takes a body that opens with a bold paragraph for a title.
		sep := "\n\n"
		if c.Title == "" {
			sep = "\n"
		}
		s = "**" + cmp.Or(badge[c.Severity], c.Severity) + "**" + sep + s
	}
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
		s += "\n\n" + fold("Suggested fix", strings.Replace(review.Fence(d), "\n", "diff\n", 1), false, false)
	} else if c.Fix != "" {
		s += "\n\n" + fold("Suggested fix", review.Fence(f), false, false)
	}
	if p := c.Prompt(); p != "" {
		s += "\n\n" + fold("🤖 Prompt for AI Agents", review.Fence(p), false, false)
	}
	return s
}

// trim drops the newline a model ends Existing and Fix with. Only Format and Place's match trim, once each.
func trim(s string) string { return strings.TrimSuffix(s, "\n") }

// prefixed puts p before each of s's lines.
func prefixed(p, s string) string {
	return p + strings.ReplaceAll(s, "\n", "\n"+p)
}

// fold is a block that opens on a click, its body indented under its summary. open shows it unfolded, quote bars
// the body's left edge, for a list of findings.
func fold(summary, body string, open, quote bool) string {
	attr, bar, unbar := "", "", ""
	if open {
		attr = " open"
	}
	if quote {
		bar, unbar = "<blockquote>", "</blockquote>"
	}
	return fmt.Sprintf("<details%s>\n<summary>%s</summary>%s\n<dl>\n<dd>\n<br>\n\n%s\n\n</dd>\n</dl>\n%s</details>", attr, summary, bar, body, unbar)
}

// Run is what ran the review that posted a finding, read back from its stamp.
type Run struct{ Version, Review, Verify, SHA string }

// runTag is a finding's stamp, the last thing before Mark: a forged one a model quoted from the PR sits earlier in
// the body. Fixed charset and lengths, so a stamp stays small; v= fits edge builds ("edge-" and a SHA).
var runTag = regexp.MustCompile(`<!-- diffharbor:run v=([\w.+-]{1,64}) r=([\w./:@+-]{0,80}) x=([\w./:@+-]{0,80}) s=([0-9a-f]{7,40}) -->\r?\n` + Mark + `\s*$`)

// Stamp is the run's hidden marker for Seal, "" when a value doesn't fit what ParseRun reads: a finding with none
// counts with the fields empty.
func Stamp(v Run) string {
	s := fmt.Sprintf("<!-- diffharbor:run v=%s r=%s x=%s s=%s -->\n", v.Version, v.Review, v.Verify, v.SHA)
	if r := ParseRun(s + Mark); r != v {
		return ""
	}
	return s
}

// Seal is what ends a finding's comment: its run's stamp and Mark.
func Seal(c Finding) string { return "\n\n" + c.Run + Mark }

// Unseal is s without its stamp and Mark, for text that goes into a prompt or a page.
func Unseal(s string) string { return strings.ReplaceAll(runTag.ReplaceAllString(s, ""), Mark, "") }

// ParseRun reads the stamp off a comment of ours, the zero Run for none or a malformed one.
func ParseRun(body string) Run {
	if m := runTag.FindStringSubmatch(body); m != nil {
		return Run{m[1], m[2], m[3], m[4]}
	}
	return Run{}
}

// Parse reads severity, title and the verifier's evidence back from a finding Format posted. A body Format
// did not write comes back as the body alone.
func Parse(body string) review.Comment {
	c := review.Comment{Body: body}
found:
	for sev := range badge {
		for _, m := range marks(sev) {
			rest, ok := strings.CutPrefix(body, m[0])
			if !ok {
				continue
			}
			c.Severity, c.Body = sev, rest
			if t, ok := strings.CutPrefix(rest, m[1]); ok {
				// a title is its whole paragraph: a body that opens in bold ("**Cause:** …") keeps it
				if title, after, ok := strings.Cut(t, "**"); ok && (after == "" || strings.HasPrefix(after, "\n\n")) {
					c.Title, c.Body = title, after
				}
			}
			break found
		}
	}
	c.Body = strings.TrimSpace(c.Body)
	if _, e, ok := strings.Cut(c.Body, review.Verified); ok {
		c.Evidence, _, _ = strings.Cut(e, "\n")
	}
	return c
}

// Place finds the lines c.Existing quotes in the diff, nearest to c.Line and at most 10 lines from it, and sets
// c.Start and c.End to them. No match, or two equally near, leaves 0: the fix then shows as a plain code block.
// Lines match exactly but for trailing space; only when none does, ignoring indentation too, as models drift on it.
// Then Fix and Existing move to the file's indentation, line by line through the pairs the quote shows.
// ponytail: a Fix at a depth the quote does not show, or a quote that indents differently than the file, stays a
// diff block; extend the map from the file's neighbouring lines if that bites. A quote that drifts alike over two
// depths, with a Fix in the file's real indentation, moves the Fix one level too deep; no cheap guard keeps both.
func Place(c *Finding, lines map[string]string) {
	if c.Existing == "" || c.Fix == "" || trim(c.Existing) == trim(c.Fix) {
		return
	}
	want := strings.Split(trim(c.Existing), "\n")
	// find returns the start of the nearest run of lines equal under same, within 10 lines, and whether it is the only
	// one that near; 0 if there is none.
	find := func(same func(l, w string) bool) (int, bool) {
		best, tie, s0 := 11, false, 0
		for s := c.Line - len(want) - 10; s <= c.Line+10; s++ {
			matches := true
			for i, w := range want {
				if l, ok := lines[fmt.Sprintf("%s:%d", c.Path, s+i)]; !ok || !same(l, w) {
					matches = false
					break
				}
			}
			if d := max(s-c.Line, c.Line-(s+len(want)-1), 0); matches && d <= min(best, 10) {
				tie = d == best
				best, s0 = d, s
			}
		}
		return s0, best <= 10 && !tie
	}
	s0, ok := find(func(l, w string) bool { return strings.TrimRight(l, " \t\r") == strings.TrimRight(w, " \t\r") })
	if ok {
		c.Start, c.End = s0, s0+len(want)-1
		return
	}
	if s0 != 0 { // two exact copies at the same distance: no looser guess
		return
	}
	s0, ok = find(func(l, w string) bool { return strings.Trim(l, " \t\r") == strings.Trim(w, " \t\r") })
	if !ok {
		return
	}
	indent := func(s string) string { return s[:len(s)-len(strings.TrimLeft(s, " \t"))] }
	to := map[string]string{} // the file's indentation, by the quote's
	file := make([]string, len(want))
	for i, w := range want {
		file[i] = lines[fmt.Sprintf("%s:%d", c.Path, s0+i)]
		if strings.TrimSpace(w) == "" {
			continue
		}
		q, f := indent(w), indent(file[i])
		if t, ok := to[q]; ok && t != f {
			return
		}
		to[q] = f
	}
	// A deeper quoted line is deeper in the file too, each depth beginning with the shallower one: else the Fix would
	// lose or swap a level, and a tab next to a space has no order.
	for q, f := range to {
		for q2, f2 := range to {
			deeper := strings.HasPrefix(q2, q)
			if q != q2 && (deeper != (f != f2 && strings.HasPrefix(f2, f)) || !deeper && !strings.HasPrefix(q, q2)) {
				return
			}
		}
	}
	fix := strings.Split(c.Fix, "\n")
	for j, f := range fix {
		if strings.TrimSpace(f) == "" {
			continue
		}
		t, ok := to[indent(f)]
		if !ok {
			return
		}
		fix[j] = t + strings.TrimLeft(f, " \t")
	}
	c.Start, c.End, c.Fix, c.Existing = s0, s0+len(want)-1, strings.Join(fix, "\n"), strings.Join(file, "\n")
}

// Split sorts a review's findings: the ones on diff lines (lines) to post inline, placed, the ones outside the
// diff, and the folded ones (Comment.Fold), anywhere. A thread's line gets nothing new, and an inline finding
// none within 5 lines of one either: a repeat lands a line or two off the first.
// Of the inline ones, review.CapInline folds the least severe past its cap.
// ponytail: by position only, a different finding next to a thread goes too; compare titles if that bites.
func Split(r review.Result, threads []Thread, lines map[string]string) (inline, outside, folded []Finding) {
	for _, rc := range r.Comments { // review.Analyze caps them: 10 and 5 nits per pass
		c := Finding{Comment: rc, Run: r.Run}
		near := 6 // distance to the closest thread on c's file
		for _, t := range threads {
			if t.Line > 0 && t.Path == c.Path {
				near = min(near, abs(t.Line-c.Line))
			}
		}
		_, ok := lines[fmt.Sprintf("%s:%d", c.Path, c.Line)]
		switch {
		case near == 0, near <= 5 && ok && !c.Fold:
			continue
		case c.Fold:
			folded = append(folded, c)
		case ok:
			inline = append(inline, c)
		default:
			outside = append(outside, c)
		}
	}
	// The cap counts only what would open a thread: one near a thread or outside the diff takes no slot.
	cs := make([]review.Comment, len(inline))
	for i, c := range inline {
		cs[i] = c.Comment
	}
	review.CapInline(cs)
	all := inline
	inline = nil
	for i, c := range all {
		if c.Fold = cs[i].Fold; c.Fold {
			folded = append(folded, c)
			continue
		}
		c.OnDiff = true
		Place(&c, lines)
		inline = append(inline, c)
	}
	return inline, outside, folded
}

// quoted lists findings in a block quote under an alert: each folded, its severity, title and place on its summary.
func quoted(alert, heading string, cs []Finding) string {
	out := []string{"[!" + alert + "]\n" + heading}
	for _, c := range cs {
		at := fmt.Sprintf("%s:%d", c.Path, c.Line)
		var head []string
		if c.Severity != "" {
			head = append(head, "<strong>"+html.EscapeString(cmp.Or(badge[c.Severity], c.Severity))+"</strong>")
		}
		if c.Title != "" {
			head = append(head, html.EscapeString(c.Title))
		}
		head = append(head, "<code>"+html.EscapeString(at)+"</code>")
		out = append(out, fold(strings.Join(head, " · "), "`"+at+"`\n"+Format(c, ""), false, true))
	}
	return "> " + strings.ReplaceAll(strings.Join(out, "\n\n"), "\n", "\n> ")
}

// byFile lists findings folded by file, the files in the order they first come, each finding after its line.
func byFile(cs []Finding) string {
	var paths []string
	at := map[string][]string{}
	for _, c := range cs {
		if at[c.Path] == nil {
			paths = append(paths, c.Path)
		}
		at[c.Path] = append(at[c.Path], fmt.Sprintf("`%d`: %s", c.Line, Format(c, "")))
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = fold(fmt.Sprintf("%s: %d", html.EscapeString(p), len(at[p])), strings.Join(at[p], "\n\n---\n\n"), false, true)
	}
	return strings.Join(out, "\n\n")
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
		return "Nitpick comments"
	}
	return "Minor comments and nitpicks"
}

// allPrompts folds every finding's prompt into one block to paste into an agent, under where the finding is: on
// the diff (inline or not taken inline), outside it, folded (named as in the summary).
func allPrompts(onDiff, outside, folded []Finding) string {
	var parts []string
	for _, g := range []struct {
		name string
		cs   []Finding
	}{{"Inline comments", onDiff}, {"Outside diff comments", outside}, {foldedTitle(folded), folded}} {
		ps := []string{g.name + ":"}
		for _, c := range g.cs {
			if p := c.Prompt(); p != "" {
				ps = append(ps, p)
			}
		}
		if len(ps) > 1 {
			parts = append(parts, strings.Join(ps, "\n\n"))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return fold("🤖 Prompt to fix all issues", review.Fence("Verify each finding against the current code and only fix it if needed.\n\n"+
		strings.Join(parts, "\n\n---\n\n")), false, false)
}

// Summary is the review's top comment: how many findings, the ones not posted inline in full, the folded ones by
// file, all prompts, the review info in small print. Those outside the diff and those the forge didn't take inline
// (refused, dropped, not tried) each go under their own alert.
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
		parts = append(parts, quoted("CAUTION", "Some comments are outside the diff and can't be posted inline.\n\n"+
			fmt.Sprintf("**⚠️ Outside diff range comments (%d)**", len(outside)), outside))
	}
	if len(refused) > 0 {
		parts = append(parts, quoted("WARNING", "Some comments are on the diff but couldn't be posted inline.\n\n"+
			fmt.Sprintf("**💬 Comments not posted inline (%d)**", len(refused)), refused))
	}
	if len(folded) > 0 {
		parts = append(parts, fold(fmt.Sprintf("🧹 %s: %d", foldedTitle(folded), len(folded)), byFile(folded), false, true))
	}
	if p := allPrompts(slices.Concat(inline, refused), outside, folded); p != "" {
		parts = append(parts, p)
	}
	parts = append(parts, "<h6>\n"+fold("ℹ️ Review info", footer, false, false)+"\n</h6>\n\n"+Mark)
	return strings.Join(parts, "\n\n")
}

func abs(n int) int { return max(n, -n) }
