package report

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Version is a review's report as a later one compares itself with: its head, when, and what it showed.
type Version struct {
	SHA      string            `json:"sha"`
	At       time.Time         `json:"at"`
	Files    map[string]string `json:"files"`              // path -> diff.PatchIDs': the file's change
	Findings []string          `json:"findings,omitempty"` // "path: title" of the open ones
	Risks    []string          `json:"risks,omitempty"`    // the names and paths of Before you merge
	Weights  map[string]string `json:"weights,omitempty"`  // "path: title" of Findings -> its severity
	Tests    map[string]string `json:"tests,omitempty"`    // the tests the page names -> pass, fail, skip or none (not run); unset when none ran
	Broken   bool              `json:"broken,omitempty"`   // a changed package did not build
}

// since is what changed from the version before to this one.
type since struct {
	Prev         Version
	Files        []*file  // their change changed, or new in the PR
	Back         []string // files the PR no longer changes
	New, Fixed   []string // findings
	Risen, Eased []string // merge risks
	now          map[string]string
}

// stamp is the version v shows, its time unset.
func stamp(v view, ids map[string]string) Version {
	now := Version{SHA: v.SHA, Files: ids, Weights: map[string]string{}, Broken: len(v.Data.Tests.Broken) > 0}
	for _, c := range v.Findings {
		now.Findings = append(now.Findings, c.Path+": "+c.Title)
		now.Weights[c.Path+": "+c.Title] = c.Severity
	}
	for _, r := range v.Risks {
		for _, it := range r.Items {
			now.Risks = append(now.Risks, it.What)
		}
	}
	return now
}

// Sev is the severity of a finding of New or Fixed, "path: title": the one it has now or had at Prev.
func (s *since) Sev(k string) string { return cmp.Or(s.now[k], s.Prev.Weights[k]) }

// Say is how many findings it fixed and added: "1 finding fixed, 1 new".
func (s *since) Say() string {
	var p []string
	if n := len(s.Fixed); n > 0 {
		p = append(p, plural(n, "finding", "findings")+" fixed")
	}
	if n := len(s.New); n > 0 && len(p) > 0 {
		p = append(p, strconv.Itoa(n)+" new")
	} else if n > 0 {
		p = append(p, plural(n, "new finding", "new findings"))
	}
	return strings.Join(p, ", ")
}

// compare marks the files whose change differs from prev's and lists what else changed; nil without prev.
func compare(prev *Version, now Version, files map[string]*file) *since {
	if prev == nil {
		return nil
	}
	s := &since{Prev: *prev, now: now.Weights}
	for _, p := range slices.Sorted(maps.Keys(now.Files)) {
		if now.Files[p] != prev.Files[p] && files[p] != nil {
			files[p].Changed = short(prev.SHA)
			s.Files = append(s.Files, files[p])
		}
	}
	for _, p := range slices.Sorted(maps.Keys(prev.Files)) {
		if _, ok := now.Files[p]; !ok {
			s.Back = append(s.Back, p)
		}
	}
	s.New, s.Fixed = minus(now.Findings, prev.Findings), minus(prev.Findings, now.Findings)
	s.Risen, s.Eased = minus(now.Risks, prev.Risks), minus(prev.Risks, now.Risks)
	return s
}

// minus is what of a b lacks.
func minus(a, b []string) []string {
	return slices.DeleteFunc(slices.Clone(a), func(x string) bool { return slices.Contains(b, x) })
}
