package job

import (
	"bytes"
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// votePR serves voted threads; only ann may push.
type votePR struct {
	skipPR
	ts          []forge.Thread
	asked       map[string]int // CanWrite calls per login
	canFail     bool
	refreshFail bool
}

func (p *votePR) Refresh() error {
	if p.refreshFail {
		return errors.New("503")
	}
	return p.skipPR.Refresh()
}

func (p *votePR) Voted() ([]forge.Thread, error) { return p.ts, nil }
func (p *votePR) CanWrite(l string) (bool, error) {
	p.asked[l]++
	if p.canFail {
		return false, errors.New("503")
	}
	return l == "ann", nil
}

func tallyOf(t *testing.T, key string) tally {
	t.Helper()
	tl, err := readTally(key)
	if err != nil {
		t.Fatal(err)
	}
	return tl
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
		{forge.Thread{Resolved: true}, resolved},
		{forge.Thread{Resolved: true, Down: []string{"mallory"}}, resolved},
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
		{Body: body("minor", "Naming"), Path: "c.go", Up: []string{"ann"}, Resolved: true}, // accepted, then resolved
		{Body: body("minor", "Typo"), Path: "d.go"},
		{Body: body("minor", "Spam"), Path: "f.go", Resolved: true, Down: []string{"mallory"}}, // not a writer's 👎
		{Body: body("minor", "Stale"), Path: "g.go", Resolved: true},                           // fixed or declined: no learning
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
	tl := tallyOf(t, "o/r")
	if len(tl.PRs) != 1 || tl.Total[addressed] != 1 || tl.Total[dismissed] != 1 || tl.Total[resolved] != 2 || tl.Total[accepted] != 1 || tl.Total[ignored] != 1 ||
		tl.Severity["major"][dismissed] != 1 || tl.Profile["quiet"][accepted] != 1 {
		t.Fatalf("%+v", tl)
	}
	ls := learningsOf("o/r")
	if len(ls) != 1 || !strings.HasPrefix(ls[0], "- Maintainers declined a major finding: Races (b.go, ") {
		t.Fatalf("%q", ls)
	}
	if pr.asked["ann"] != 1 || pr.asked["mallory"] != 1 || pr.asked[""] != 0 {
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
	if ls := learningsOf("o/r"); len(ls) != 1 || tallyOf(t, "o/r").Total[dismissed] != 3 {
		t.Fatalf("%q %+v", ls, tallyOf(t, "o/r"))
	}
	var b bytes.Buffer
	if err := Stats(&b, "o/r"); err != nil || !strings.Contains(b.String(), "all") || !strings.Contains(b.String(), "severity major") ||
		!strings.Contains(b.String(), "profile quiet") || !strings.Contains(strings.Join(strings.Fields(b.String()), " "), "all 8 12% 12% 38% 25% 12%") {
		t.Fatalf("%v\n%s", err, b.String())
	}
}

// What fails counts nothing and is tried again on the next close: a tally that doesn't read (it stays as it is), a
// CanWrite or Refresh error, a tally that doesn't write (its learnings are saved already, once). A review.json that doesn't
// parse counts, but learns nothing.
func TestOutcomesFail(t *testing.T) {
	proc.DataDir = t.TempDir()
	pr := &votePR{asked: map[string]int{}, ts: []forge.Thread{{Body: "_🟠 Major_ · **Races**\n\nbody", Path: "b.go", Down: []string{"ann"}}}}
	pr.cfg = `{}`
	e := &forge.Event{Forge: "github"}
	e.Repository.FullName = "o/r"
	e.PullRequest.Number = 7
	f := tallyFile("o/r")
	if err := os.MkdirAll(f+".tmp", 0o700); err != nil { // the tally's write fails
		t.Fatal(err)
	}
	if err := Outcomes(voteForge{pr: pr}, e); err == nil || len(learningsOf("o/r")) != 0 || len(tallyOf(t, "o/r").PRs) != 0 {
		t.Fatalf("%v %q", err, learningsOf("o/r"))
	}
	_ = os.Remove(f + ".tmp")
	pr.canFail = true
	if err := Outcomes(voteForge{pr: pr}, e); err == nil || len(tallyOf(t, "o/r").PRs) != 0 {
		t.Fatalf("CanWrite failed: %v %+v", err, tallyOf(t, "o/r"))
	}
	pr.canFail, pr.refreshFail = false, true // the description may be gone, and its profile with it
	if err := Outcomes(voteForge{pr: pr}, e); err == nil || len(tallyOf(t, "o/r").PRs) != 0 {
		t.Fatalf("Refresh failed: %v %+v", err, tallyOf(t, "o/r"))
	}
	pr.refreshFail = false
	if err := Outcomes(voteForge{pr: pr}, e); err != nil || len(learningsOf("o/r")) != 1 || tallyOf(t, "o/r").Total[dismissed] != 1 {
		t.Fatalf("%v %q %+v", err, learningsOf("o/r"), tallyOf(t, "o/r"))
	}
	pr.cfg, pr.ts[0].Path, e.PullRequest.Number = `{"learnings":`, "e.go", 8
	if err := Outcomes(voteForge{pr: pr}, e); err != nil || len(learningsOf("o/r")) != 1 || len(tallyOf(t, "o/r").PRs) != 2 {
		t.Fatalf("broken review.json: %v %q", err, learningsOf("o/r"))
	}
	if err := os.WriteFile(f, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.PullRequest.Number = 9
	var b bytes.Buffer
	if err := Outcomes(voteForge{pr: pr}, e); err == nil || Stats(&b, "o/r") == nil || Stats(&b, "") == nil {
		t.Fatal("a broken tally reads")
	}
	if got, _ := os.ReadFile(f); string(got) != "{" {
		t.Fatalf("broken tally overwritten: %q", got)
	}
	if tallyFile("a__b/c") == tallyFile("a/b__c") {
		t.Error("tally files collide")
	}
	// A GitLab project deep in groups: its escaped name is longer than a file name may be.
	deep := "gitlab/" + strings.Repeat("group/", 40)
	if a, b := tallyFile(deep+"p"), tallyFile(deep+"q"); a == b || len(filepath.Base(a)) > 255 {
		t.Errorf("long tally files: %s %s", a, b)
	}
	if ok, err := record(deep+"p", 1, "chill", []string{"major"}, []string{ignored}); !ok || err != nil || tallyOf(t, deep+"p").Repo != deep+"p" {
		t.Errorf("long repo: %v %v", ok, err)
	}
	b.Reset()
	if err := Stats(&b, deep+"p"); err != nil || !strings.Contains(b.String(), deep+"p") {
		t.Errorf("stats of a long repo: %v", err)
	}
}

// One close saves at most maxLearnedPerClose new learnings, and counts every decline. One the repo has, or a
// second of the same, takes no place. A retry after a failed count adds no more.
func TestOutcomesLearnCap(t *testing.T) {
	proc.DataDir = t.TempDir()
	decline := func(n int, paths ...string) error {
		pr := &votePR{asked: map[string]int{}}
		pr.cfg = `{}`
		for _, p := range paths {
			pr.ts = append(pr.ts, forge.Thread{Body: "_🟠 Major_ · **Races**\n\nbody", Path: p, Down: []string{"ann"}})
		}
		e := &forge.Event{Forge: "github"}
		e.Repository.FullName, e.PullRequest.Number = "o/r", n
		return Outcomes(voteForge{pr: pr}, e)
	}
	if err := decline(6, "0.go"); err != nil {
		t.Fatal(err)
	}
	f := tallyFile("o/r")
	if err := os.MkdirAll(f+".tmp", 0o700); err != nil { // the tally's write fails
		t.Fatal(err)
	}
	paths := []string{"0.go", "1.go", "1.go", "2.go", "3.go", "4.go", "5.go", "6.go"}
	if err := decline(7, paths...); err == nil || len(learningsOf("o/r")) != 1 {
		t.Fatalf("%v %q", err, learningsOf("o/r"))
	}
	_ = os.Remove(f + ".tmp")
	err := decline(7, paths...)
	ls := learningsOf("o/r")
	if err != nil || len(ls) != 1+maxLearnedPerClose || !strings.Contains(ls[3], "(3.go") || tallyOf(t, "o/r").Total[dismissed] != 1+len(paths) {
		t.Fatalf("%v %q %+v", err, ls, tallyOf(t, "o/r"))
	}
}

// learnOnce knows an entry whatever its date, and only that entry: not one whose path starts like it.
func TestLearnOnce(t *testing.T) {
	proc.DataDir = t.TempDir()
	for _, l := range []string{"- X (a, b)", "- X (a)", "- X (a)"} {
		if _, err := learnOnce("o/r", l); err != nil {
			t.Fatal(err)
		}
	}
	d := time.Now().Format(time.DateOnly)
	if ls := learningsOf("o/r"); strings.Join(ls, "|") != "- X (a, b, "+d+")|- X (a, "+d+")" {
		t.Fatalf("%q", ls)
	}
}
