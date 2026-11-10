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
	ts    []forge.Thread
	asked map[string]int // CanWrite calls per login
}

func (p *votePR) Voted() ([]forge.Thread, error) { return p.ts, nil }
func (p *votePR) CanWrite(l string) (bool, error) {
	p.asked[l]++
	return l == "ann", nil
}

type voteForge struct {
	skipForge
	pr *votePR
}

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
	pr := &votePR{asked: map[string]int{}, ts: []forge.Thread{
		{Body: body("major", "Leaks"), Path: "a.go", Addressed: true},
		{Body: body("major", "Races"), Path: "b.go", Down: []string{"ann", "mallory"}},
		{Body: body("minor", "Naming"), Path: "c.go", Up: []string{"ann"}},
		{Body: body("minor", "Typo"), Path: "d.go"},
		{Body: body("minor", "Spam"), Path: "f.go", Resolved: true, Resolver: "mallory"}, // the PR's author may resolve
		{Body: body("minor", "Stale"), Path: "g.go", Resolved: true, Resolver: "ann"},
		{Body: body("minor", "Fixed"), Path: "h.go", Resolved: true}, // by the chat pass
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
	if len(tl.PRs) != 1 || tl.Total[addressed] != 1 || tl.Total[dismissed] != 4 || tl.Total[accepted] != 1 || tl.Total[ignored] != 1 ||
		tl.Severity["major"][dismissed] != 1 || tl.Profile["quiet"][accepted] != 1 {
		t.Fatalf("%+v", tl)
	}
	ls := learningsOf("o/r")
	if len(ls) != 2 || !strings.HasPrefix(ls[0], "- Maintainers declined a major finding: Races (b.go, ") ||
		!strings.HasPrefix(ls[1], "- Maintainers declined a minor finding: Stale (g.go, ") {
		t.Fatalf("%q", ls)
	}
	if pr.asked["ann"] != 1 || pr.asked["mallory"] != 1 {
		t.Errorf("CanWrite asked %v, want once per login", pr.asked)
	}
	dismiss := func(n int, title, path string) {
		e.PullRequest.Number, pr.ts = n, []forge.Thread{{Body: body("major", title), Path: path, Down: []string{"ann"}}}
		if err := Outcomes(voteForge{pr: pr}, e); err != nil {
			t.Fatal(err)
		}
	}
	pr.cfg = `{"learnings":{"outcomes":false}}`
	dismiss(8, "Panics", "e.go")
	pr.cfg = `{}`
	dismiss(9, "Races", "b.go") // declined again: one line still
	if ls := learningsOf("o/r"); len(ls) != 2 || readTally("o/r").Total[dismissed] != 6 {
		t.Fatalf("%q %+v", ls, readTally("o/r"))
	}
	var b bytes.Buffer
	if err := Stats(&b, "o/r"); err != nil || !strings.Contains(b.String(), "all") || !strings.Contains(b.String(), "severity major") ||
		!strings.Contains(b.String(), "profile quiet") || !strings.Contains(b.String(), "67%") {
		t.Fatalf("%v\n%s", err, b.String())
	}
}
