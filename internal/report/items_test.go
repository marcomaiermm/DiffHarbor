package report

import (
	"diffharbor/internal/review"
	"encoding/json"
	"strings"
	"testing"
)

// A version without open items says so, [], where one from before them has none: the script tells them apart.
func TestStampEmptyItems(t *testing.T) {
	b, _ := json.Marshal(stamp(view{}, nil))
	if !strings.Contains(string(b), `"items":[]`) {
		t.Errorf("stamp without items = %s", b)
	}
}

func TestSpanPathWithColon(t *testing.T) {
	for at, want := range map[string]struct {
		p        string
		from, to int
	}{
		"data:archive/file.go:12":    {"data:archive/file.go", 12, 12},
		"data:archive/file.go:12-14": {"data:archive/file.go", 12, 14},
		"data:archive/file.go":       {"data:archive/file.go", 0, 0},
		"go/a.go:3":                  {"go/a.go", 3, 3},
		"go/a.go":                    {"go/a.go", 0, 0},
	} {
		if p, from, to := span(at); p != want.p || from != want.from || to != want.to {
			t.Errorf("span(%q) = %q, %d, %d; want %+v", at, p, from, to, want)
		}
	}
}

func TestWaitingKeepsQuotesWithOneName(t *testing.T) {
	v := &view{}
	v.Terms.Gates = []review.Gate{
		{Name: "the cutover", Waits: true, Quote: review.Quote{Text: "Merge after CAT-41.", In: "description"}},
		{Name: "the cutover", Waits: true, Quote: review.Quote{Text: "Merge after CAT-42.", In: "description"}},
		{Name: "the cutover", Waits: true, Quote: review.Quote{Text: "Merge after CAT-41.", In: "description"}},
	}
	if w := v.waiting(); len(w) != 2 || w[1].Text != "Merge after CAT-42." {
		t.Errorf("waiting = %+v, want the two quotes", w)
	}
}

func TestGateCardIDsUnique(t *testing.T) {
	v := &view{}
	v.Terms.Gates = []review.Gate{
		{Name: "API rollout", Waits: true, Quote: review.Quote{Text: "Wait for the API rollout."}},
		{Name: "API-rollout", Waits: true, Quote: review.Quote{Text: "Hold until the API-rollout."}},
		{Name: "API rollout", Waits: true, Quote: review.Quote{Text: "Not before the API rollout."}},
	}
	want := []string{"api-rollout-gate", "api-rollout-gate-2", "api-rollout-gate-3"}
	for i, g := range v.waiting() {
		if id := v.gateCard(g).ID; id != want[i] {
			t.Errorf("gate %d: ID %q, want %q", i, id, want[i])
		}
	}
}

func TestDiffCardBaseExcerptChip(t *testing.T) {
	v := &view{files: map[string]*file{"go/a.go": {}}}
	v.SHA = "722b2510"
	v.Excerpts = []Excerpt{{Path: "go/a.go", Base: true, From: 1, Lines: []string{"package a", "", "func A() {}"}}}
	k := v.diffCard(0, review.Difference{Title: "A", At: "go/a.go:3", Excerpt: "go/a.go:3"})
	b := k.Blocks[len(k.Blocks)-1]
	if b.Excerpt == nil || b.Excerpt.At != "on the base" || b.Chip != evBase {
		t.Errorf("code block: chip %+v, excerpt %+v; want the base's", b.Chip, b.Excerpt)
	}
}
