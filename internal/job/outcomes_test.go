package job

import (
	"bytes"
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"strings"
	"testing"
)

// votePR serves voted threads; only ann may push.
type votePR struct {
	skipPR
	ts []forge.Thread
}

func (p *votePR) Voted() ([]forge.Thread, error) { return p.ts, nil }
func (*votePR) CanWrite(l string) (bool, error)  { return l == "ann", nil }

type voteForge struct {
	skipForge
	pr *votePR
}

func (voteForge) Instance() string                      { return "test" }
func (f voteForge) Open(*forge.Event) (forge.PR, error) { return f.pr, nil }

func TestClassify(t *testing.T) {
	can := func(l string) bool { return l == "ann" }
	for _, c := range []struct {
		t    forge.Thread
		want string
	}{
		{forge.Thread{Addressed: true, Resolved: true, Down: []string{"ann"}}, addressed},
		{forge.Thread{Down: []string{"ann"}, Up: []string{"ann"}}, dismissed},
		{forge.Thread{Up: []string{"ann"}, Resolved: true}, accepted},
		{forge.Thread{Up: []string{"mallory"}}, ignored}, // anyone can react on a public repo
		{forge.Thread{Down: []string{"mallory"}}, ignored},
		{forge.Thread{Resolved: true}, dismissed},
		{forge.Thread{}, ignored},
	} {
		if got := classify(c.t, can); got != c.want {
			t.Errorf("%+v: %s, want %s", c.t, got, c.want)
		}
	}
}

// A close counts once and saves declined findings as learnings; learnings.outcomes: false only counts.
func TestOutcomes(t *testing.T) {
	proc.DataDir = t.TempDir()
	body := func(sev, title string) string {
		return "_" + map[string]string{"major": "🟠 Major", "minor": "🟡 Minor"}[sev] + "_ · **" + title + "**\n\nbody"
	}
	pr := &votePR{ts: []forge.Thread{
		{Body: body("major", "Leaks"), Path: "a.go", Addressed: true},
		{Body: body("major", "Races"), Path: "b.go", Down: []string{"ann", "mallory"}},
		{Body: body("minor", "Naming"), Path: "c.go", Up: []string{"ann"}},
		{Body: body("minor", "Typo"), Path: "d.go"},
	}}
	e := &forge.Event{Forge: "github"}
	e.Repository.FullName = "o/r"
	e.PullRequest.Number, e.PullRequest.Body = 7, "review: quiet"
	pr.cfg = `{}`
	for range 2 { // closed again after a reopen
		if err := Outcomes(voteForge{pr: pr}, e); err != nil {
			t.Fatal(err)
		}
	}
	tl := readTally("o/r")
	if len(tl.PRs) != 1 || tl.Total[addressed] != 1 || tl.Total[dismissed] != 1 || tl.Total[accepted] != 1 || tl.Total[ignored] != 1 ||
		tl.Severity["major"][dismissed] != 1 || tl.Profile["quiet"][accepted] != 1 {
		t.Fatalf("%+v", tl)
	}
	ls := learningsOf("o/r")
	if len(ls) != 1 || !strings.HasPrefix(ls[0], "- Maintainers declined a major finding: Races (b.go, ") {
		t.Fatalf("%q", ls)
	}
	e.PullRequest.Number, pr.cfg = 8, `{"learnings":{"outcomes":false}}`
	if err := Outcomes(voteForge{pr: pr}, e); err != nil || len(learningsOf("o/r")) != 1 || readTally("o/r").Total[dismissed] != 2 {
		t.Fatalf("%v %q", err, learningsOf("o/r"))
	}
	var b bytes.Buffer
	if err := Stats(&b, "o/r"); err != nil || !strings.Contains(b.String(), "all") || !strings.Contains(b.String(), "severity major") ||
		!strings.Contains(b.String(), "profile quiet") || !strings.Contains(b.String(), "25%") {
		t.Fatalf("%v\n%s", err, b.String())
	}
}
