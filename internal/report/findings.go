package report

import (
	"diffharbor/internal/review"
	"diffharbor/internal/text"
	"strconv"
	"strings"
)

// finding is a review.Comment as the diff shows it, at its line or file.
type finding struct {
	review.Comment
	ID    string // its anchor at its line or file
	Proof string // why to believe it: a source shown without a model, or the second model's
	Shown bool   // Proof needs no model: a test run
}

// judge words the findings for the diff's notes, and records the weakened tests in the version, so the next
// review sees them come and go. They weigh "test", no severity: Test changes counts them, not the major findings.
func judge(v *view, found []*finding) {
	gone := map[string]bool{}
	for _, r := range retire(v.Outline.Removed, v.files) {
		for _, n := range r.Names {
			gone[n] = true
		}
	}
	for _, w := range v.Outline.Weakened {
		p, rest, _ := strings.Cut(w.At, ":")
		if w.What == "test deleted" && gone[strings.TrimSpace(rest)] {
			continue // with the code it tested
		}
		v.now.Findings, v.now.Weights[p+": "+w.What] = append(v.now.Findings, p+": "+w.What), "test"
	}
	for _, fd := range found {
		fd.Body = strings.TrimSpace(fd.Body) // a verified finding without a body is "\n\n_Verified:_ …"
		if fd.Title == "" {
			fd.Title = text.OneLine(fd.Body, 120)
		}
		switch {
		case strings.HasPrefix(fd.Evidence, review.Ran): // a run, no model
			fd.Proof, fd.Shown = "Test run on the base", true
		case strings.HasPrefix(fd.Evidence, review.Repro):
			fd.Proof, fd.Shown = "Test run", true
		case fd.Evidence != "":
			fd.Proof = "AI cross-check"
		default:
			fd.Proof = "not verified"
		}
	}
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

// weighed is what findings weigh: the heaviest of them that matters to the merge and how many, "1 blocker", "2 major".
type weighed struct{ Sev, Text string }

// weigh is nil for no finding, or minor ones only.
func weigh(fds []*finding) *weighed {
	crit, major := 0, 0
	for _, fd := range fds {
		switch fd.Severity {
		case "critical":
			crit++
		case "major":
			major++
		}
	}
	switch {
	case crit > 0:
		return &weighed{"critical", plural(crit, "blocker", "blockers")}
	case major > 0:
		return &weighed{"major", strconv.Itoa(major) + " major"}
	}
	return nil
}

// found is the findings on f: on a line of it or on the file.
func (f *file) found() []*finding {
	fds := f.Findings
	for _, l := range f.Lines {
		fds = append(fds, l.Findings...)
	}
	return fds
}

func (f *file) Weight() *weighed { return weigh(f.found()) }

// heavier is whether severity a weighs more than b; "" weighs nothing.
func heavier(a, b string) bool { return review.Rank(a) > review.Rank(b) }
