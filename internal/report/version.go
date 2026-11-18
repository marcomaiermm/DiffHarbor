package report

import (
	"cmp"
	"diffharbor/internal/review"
	"slices"
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
	Items    []Item            `json:"items,omitempty"`    // the open items by kind and place; none in a version from before them
	// The merge status's short form and whether it blocks, without the forge's CI, for the commit status (decision 6).
	Short   string `json:"-"`
	Blocked bool   `json:"-"`
}

// Item is an open item as a Version keeps it, for a later page to mark what is new since (spec 5.13): the page's
// script compares a version's Items with this one's, and its Files. A base's findings come from its Findings and
// Weights, which every version has.
type Item struct {
	Kind  string `json:"kind"`  // "finding" (major or worse), "before" (Before you merge), "difference", "gate" (a gate that waits), "test-with", "test-kept", "test-stay" (Test changes' groups 1 to 3), "gap"
	Place string `json:"place"` // a finding's "path: title" as in Findings, a difference's "path:line", a before-merge item's name or path, a gate's ticket or quote, a test file's path, a file the review didn't read
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
			now.Items = append(now.Items, Item{"before", it.What})
		}
	}
	for _, c := range v.Findings {
		if review.Blocks(c.Severity) {
			now.Items = append(now.Items, Item{"finding", c.Path + ": " + c.Title})
		}
	}
	for kind, fs := range map[string][]TestFile{"test-with": v.TestChanges.With, "test-kept": v.TestChanges.Kept} {
		for _, f := range fs {
			now.Items = append(now.Items, Item{kind, f.Path})
		}
	}
	for _, f := range v.TestChanges.Stay {
		now.Items = append(now.Items, Item{"test-stay", f.Path})
	}
	for _, d := range v.Terms.Differences {
		now.Items = append(now.Items, Item{"difference", d.At})
	}
	for _, g := range v.waiting() {
		now.Items = append(now.Items, Item{"gate", gateKey(g)})
	}
	for _, p := range slices.Concat(v.Failed, v.Omitted) {
		now.Items = append(now.Items, Item{"gap", p})
	}
	slices.SortFunc(now.Items, func(a, b Item) int { return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Place, b.Place)) })
	now.Items = slices.Compact(now.Items)
	return now
}

// sinceData is what the page's script compares with a version of /versions (spec 5.13): this version's files and
// items, the map box or kind of each file, and the boxes in map order with what the Since line calls them.
type sinceData struct {
	Files map[string]string `json:"files"`
	Items []Item            `json:"items"`
	Where map[string]string `json:"where"` // path -> its box's key, else "code", "test", "doc" or "other"
	Boxes [][]string        `json:"boxes"` // [key, name, "1" for a box named alone, as "migration 0059"]
	Major []string          `json:"major"` // the severities that make a finding an open item
}

// SinceData is the page's since data.
func (v view) SinceData() sinceData {
	s := sinceData{Files: v.now.Files, Items: v.now.Items, Where: map[string]string{}, Boxes: [][]string{}}
	s.Major = slices.DeleteFunc(slices.Clone(review.Severities), func(sev string) bool { return !review.Blocks(sev) })
	if s.Items == nil {
		s.Items = []Item{}
	}
	for p, f := range v.files {
		w := v.boxOf(p)
		if w == "" {
			w = f.Kind
			if w != "code" && w != "test" && w != "doc" {
				w = "other"
			}
		}
		s.Where[p] = w
	}
	if mv := v.Page.Map; mv != nil {
		for _, b := range slices.Concat(mv.Fig, mv.Alone, mv.Deploy) {
			switch b.Kind {
			case "deploy":
				s.Boxes = append(s.Boxes, []string{b.Key, strings.ToLower(b.Label), "1"})
			case "part":
				s.Boxes = append(s.Boxes, []string{b.Key, b.Label, ""})
			}
		}
		if mv.Gen != nil {
			s.Boxes = append(s.Boxes, []string{mv.Gen.Key, "generated files", "1"})
		}
	}
	return s
}
