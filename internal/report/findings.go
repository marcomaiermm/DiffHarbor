package report

import (
	"cmp"
	"diffharbor/internal/review"
	"diffharbor/internal/text"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// finding is a review.Comment as the page shows it: at its line and in the findings list.
type finding struct {
	review.Comment
	ID         string // its anchor at its line or file, "" when the page doesn't show it there
	Href       string // where the list links to
	Loc, Where string // "a.go:22", and with where it sits: "a.go:22 · step 2"
	Lead       string // the first paragraph of Body, the list's line under the title
	Proof      string // why to believe it: a source shown without a model, or the second model's
	Shown      bool   // Proof needs no model: the diff, the imports, a search
	External   bool   // Href leaves the page: not in the diff
	inline     bool   // on a line of the diff: a line comment in the draft
}

// fgroup is findings that weigh the same: what blocks the merge, what to fix before it, the rest.
type fgroup struct {
	Title string
	Items []*finding
}

// verdict is what the page leads with and closes on: ready or not, and what to fix first.
type verdict struct {
	OK                 bool // nothing blocks the merge
	Short, Title, Body string
}

// draft is the review the person sends from the forge: the page posts nothing.
type draft struct {
	Event    string   // "Request changes" or "Comment"
	Comments int      // findings on a line of the diff
	Say      string   // what it holds of the findings: "1 blocker and 2 major"
	Risks    []string // the merge risks by title
	MD       string
}

// judge sorts the findings and the weakened tests into their groups and writes the verdict and the draft.
// Call it once the steps are built: a finding names the step its file is in.
func judge(v *view, found []*finding) {
	where := map[string]string{}
	for i, s := range v.Steps {
		for _, f := range s.Files {
			where[f.Path] = "step " + strconv.Itoa(i+1)
		}
	}
	for _, f := range v.Tests {
		where[f.Path] = "tests"
	}
	for _, f := range v.Other {
		where[f.Path] = "data and docs"
	}
	for _, g := range v.Mechanical {
		for _, f := range g.Files {
			where[f.Path] = "mechanical"
		}
	}
	for _, f := range v.Deleted {
		where[f.Path] = "deleted"
	}
	for _, w := range v.Outline.Weakened {
		p, rest, _ := strings.Cut(w.At, ":")
		fd := &finding{Comment: review.Comment{Path: p, Severity: "major", Title: w.What}, Proof: "In the diff", Shown: true}
		fd.Loc = path.Base(p)
		if rest != "" {
			fd.Loc += ":" + rest
		}
		if n, err := strconv.Atoi(rest); err == nil {
			fd.Line, fd.inline = n, true
		}
		if where[p] != "" {
			fd.Href = "#" + p
		}
		found = append(found, fd)
	}
	v.Groups = []fgroup{{Title: "Blocks the merge"}, {Title: "Fix before merging"}, {Title: "Minor"}}
	for _, fd := range found {
		if fd.Loc == "" {
			fd.Loc = path.Base(fd.Path)
			if fd.Line > 0 {
				fd.Loc += ":" + strconv.Itoa(fd.Line)
			}
		}
		fd.Where = fd.Loc + " · " + cmp.Or(where[fd.Path], "not in the diff")
		if fd.Title == "" {
			fd.Title = text.OneLine(fd.Body, 120)
		} else {
			fd.Lead = lead(fd.Body)
		}
		if fd.Proof == "" {
			fd.Proof = "AI cross-check"
			if fd.Evidence == "" {
				fd.Proof = "not verified"
			}
		}
		g := 2
		switch fd.Severity {
		case "critical":
			g = 0
		case "major":
			g = 1
		}
		v.Groups[g].Items = append(v.Groups[g].Items, fd)
	}
	v.NFound = len(found)
	v.Verdict, v.Draft = verdictOf(v), draftOf(v)
}

// verdictOf is the verdict: the most severe blocking finding first, or what still needs the person.
func verdictOf(v *view) verdict {
	block, minor := blocking(v), len(v.Groups[2].Items)
	var need []string
	if n := len(v.Decisions); n > 0 {
		need = append(need, plural(n, "decision", "decisions"))
	}
	if v.MergeN > 0 {
		need = append(need, "the merge risks")
	}
	if len(block) > 0 {
		var after []string
		if n := len(block) - 1; n > 0 {
			after = append(after, plural(n, "more finding", "more findings")+" to fix")
		}
		if minor > 0 {
			after = append(after, plural(minor, "minor finding", "minor findings"))
		}
		after = append(after, need...)
		top := block[0]
		body := top.Loc + ": " + cmp.Or(sentence(top.Lead), top.Title)
		if len(after) > 0 {
			body += " After that, " + join(after) + "."
		}
		return verdict{Short: "Not ready to merge", Title: "Fix first: " + top.Title, Body: body}
	}
	vd := verdict{OK: true, Short: "No findings", Title: "Nothing blocks the merge", Body: "No finding on " + short(v.SHA) + "."}
	if minor > 0 {
		vd.Short, vd.Body = "No blockers", plural(minor, "minor finding", "minor findings")+", none that blocks."
	}
	if len(need) > 0 {
		verb := "need"
		if len(need) == 1 && strings.HasPrefix(need[0], "1 ") {
			verb = "needs"
		}
		s := join(need)
		vd.Body += " " + strings.ToUpper(s[:1]) + s[1:] + " still " + verb + " you before you approve."
	}
	return vd
}

// blocking is the findings of the first two groups, most severe first.
func blocking(v *view) []*finding {
	return append(append([]*finding{}, v.Groups[0].Items...), v.Groups[1].Items...)
}

// draftOf is the draft review: the blocking findings, the decisions as questions, the minor ones in a line.
func draftOf(v *view) draft {
	d := draft{Event: "Comment"}
	block := blocking(v)
	var b strings.Builder
	if len(block) > 0 {
		d.Event = "Request changes"
		fmt.Fprintf(&b, "%s (%s).", block[0].Title, block[0].Loc)
		if s := sentence(block[0].Lead); s != "" {
			b.WriteString(" " + s)
		}
		if len(block) > 1 {
			b.WriteString("\n\nFix before merging:")
			for _, fd := range block[1:] {
				fmt.Fprintf(&b, "\n- %s  %s", fd.Loc, fd.Title)
			}
		}
	} else if len(v.Groups[2].Items) == 0 {
		b.WriteString("No findings on " + short(v.SHA) + ".")
	} else {
		b.WriteString("Nothing blocks the merge on " + short(v.SHA) + ".")
	}
	for _, fd := range block {
		if fd.inline {
			d.Comments++
		}
	}
	if len(v.Decisions) > 0 {
		b.WriteString("\n\nQuestions:")
		for i, q := range v.Decisions {
			fmt.Fprintf(&b, "\n%d. %s", i+1, q)
		}
	}
	if ms := v.Groups[2].Items; len(ms) > 0 {
		b.WriteString("\n\nMinor:")
		for _, fd := range ms {
			fmt.Fprintf(&b, "\n- %s  %s", fd.Loc, fd.Title)
		}
	}
	d.MD = b.String()
	var ns []string
	for i, n := range []string{"blocker", "major", "minor"} {
		if k := len(v.Groups[i].Items); k > 0 {
			ns = append(ns, strconv.Itoa(k)+" "+n)
		}
	}
	d.Say = "none"
	if len(ns) > 0 {
		d.Say = join(ns) + ", all in the draft"
	}
	if v.Conflicts {
		d.Risks = append(d.Risks, "conflicts")
	}
	if len(v.BaseMoved) > 0 {
		d.Risks = append(d.Risks, "the base moved on")
	}
	for _, r := range v.Risks {
		d.Risks = append(d.Risks, strings.ToLower(r.Title[:1])+r.Title[1:])
	}
	if len(v.Others) > 0 {
		d.Risks = append(d.Risks, plural(len(v.Others), "open PR", "open PRs")+" on the same files")
	}
	return d
}

// lead is a finding body's first paragraph on one line.
func lead(body string) string {
	p, _, _ := strings.Cut(strings.TrimSpace(body), "\n\n")
	if strings.HasPrefix(p, review.Verified) || strings.HasPrefix(p, "```") {
		return ""
	}
	return text.OneLine(p, 300)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// join is "a", "a and b", "a, b and c".
func join(s []string) string {
	if len(s) < 2 {
		return strings.Join(s, "")
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}

func short(sha string) string { return sha[:min(len(sha), 7)] }
