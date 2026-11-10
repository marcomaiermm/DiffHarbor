package job

import (
	"bytes"
	"context"
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// A close counts once and saves declined findings as learnings; learnings.outcomes: false only counts. Each finding
// is kept with the decision a maintainer's reply filed for it, before the close or after.
func TestOutcomes(t *testing.T) {
	defer func(v string) { Version = v }(Version)
	proc.DataDir, Version = t.TempDir(), "v1.2.3"
	body := func(sev, title string) string {
		return "_" + map[string]string{"major": "🟠 Major", "minor": "🟡 Minor"}[sev] + "_ · **" + title + "**\n\nbody"
	}
	pr := &votePR{asked: map[string]int{}, ts: []forge.Thread{
		{Comment: 1, Body: body("major", "Leaks"), Path: "a.go", Addressed: true},
		{Comment: 2, Body: body("major", "Races"), Path: "b.go", Down: []string{"ann", "mallory"}},
		{Comment: 3, Body: body("minor", "Naming"), Path: "c.go", Up: []string{"ann"}, Resolved: true}, // accepted, then resolved
		{Comment: 4, Body: body("minor", "Typo"), Path: "d.go"},
		{Comment: 5, Body: body("minor", "Spam"), Path: "f.go", Resolved: true, Down: []string{"mallory"}}, // not a writer's 👎
		{Comment: 6, Body: body("minor", "Stale"), Path: "g.go", Resolved: true},                           // resolved by hand: no learning, whatever the chat heard
	}}
	e := &forge.Event{Forge: "github"}
	e.Repository.FullName = "o/r"
	e.PullRequest.Number, e.PullRequest.Body = 7, "review: quiet"
	pr.cfg = `{}`
	// Replies in the chat, before the PR closes: the last one on a finding stands. PR 70 stays open.
	for _, d := range []struct {
		pr      int
		comment int64
		d       string
	}{{7, 6, "wrong"}, {7, 6, "intended"}, {70, 1, "fixed"}} {
		if err := decide("o/r", d.pr, d.comment, d.d); err != nil {
			t.Fatal(err)
		}
	}
	var b bytes.Buffer
	if Stats(&b, "o/r") == nil || Stats(&b, "") == nil {
		t.Fatal("stats of a repo with decisions only, no closed PR")
	}
	for range 2 { // closed again after a reopen
		if err := Outcomes(t.Context(), voteForge{pr: pr}, e); err != nil {
			t.Fatal(err)
		}
	}
	tl := tallyOf(t, "o/r")
	if len(tl.PRs) != 1 || tl.Total[addressed] != 1 || tl.Total[dismissed] != 1 || tl.Total[resolved] != 2 || tl.Total[accepted] != 1 || tl.Total[ignored] != 1 ||
		tl.Severity["major"][dismissed] != 1 || tl.Profile["quiet"][accepted] != 1 {
		t.Fatalf("%+v", tl)
	}
	stale := finding{
		PR: 7, Comment: 6, Closed: time.Now().Format(time.DateOnly), Version: "v1.2.3", Path: "g.go", Title: "Stale",
		Severity: "minor", Profile: "quiet", Outcome: resolved, Decision: "intended",
	}
	if len(tl.Findings) != 6 || tl.Findings[5] != stale || tl.Findings[4].Decision != "" || len(tl.Decisions) != 1 || tl.Decisions[70][1] != "fixed" {
		t.Fatalf("findings %+v, decisions %v", tl.Findings, tl.Decisions)
	}
	// Replies after the close: on the finding itself. One on a thread that isn't a finding of ours files nothing.
	if err := errors.Join(decide("o/r", 7, 4, "fixed"), decide("o/r", 7, 404, "fixed")); err != nil {
		t.Fatal(err)
	}
	if tl := tallyOf(t, "o/r"); len(tl.Findings) != 6 || tl.Findings[3].Decision != "fixed" || len(tl.Decisions) != 1 {
		t.Fatalf("after the close: findings %+v, decisions %v", tl.Findings, tl.Decisions)
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
		if err := Outcomes(t.Context(), voteForge{pr: pr}, e); err != nil {
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
	b.Reset()
	if err := Stats(&b, "o/r"); err != nil || !strings.Contains(b.String(), "all") || !strings.Contains(b.String(), "severity major") ||
		!strings.Contains(b.String(), "profile quiet") || !strings.Contains(strings.Join(strings.Fields(b.String()), " "), "all 8 12% 12% 38% 25% 12%") {
		t.Fatalf("%v\n%s", err, b.String())
	}
	// The decisions per outcome: fixed wrong intended unlikely deferred declined none.
	for _, want := range []string{
		"decision, since " + stale.Closed + " findings fixed", "all 8 12% 0% 12% 0% 0% 0% 75%",
		"resolved 2 0% 0% 50% 0% 0% 0% 50%", "ignored 1 100% 0%", "dismissed 3 0% 0% 0% 0% 0% 0% 100%",
	} {
		if !strings.Contains(strings.Join(strings.Fields(b.String()), " "), want) {
			t.Errorf("stats lack %q:\n%s", want, b.String())
		}
	}
	// A tally from before findings were kept: its counts as ever, no decisions to show, a late one filed nowhere.
	if err := os.WriteFile(tallyFile("o/old"), []byte(`{"Repo":"o/old","PRs":[1],"Total":{"resolved":2}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	if err := errors.Join(decide("o/old", 1, 5, "fixed"), Stats(&b, "o/old")); err != nil || strings.Contains(b.String(), "decision") ||
		!strings.Contains(strings.Join(strings.Fields(b.String()), " "), "all 2 0% 0% 0% 100% 0%") || len(tallyOf(t, "o/old").Findings)+len(tallyOf(t, "o/old").Decisions) != 0 {
		t.Fatalf("old tally: %v\n%s", err, b.String())
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
	if err := Outcomes(t.Context(), voteForge{pr: pr}, e); err == nil || len(learningsOf("o/r")) != 1 || len(tallyOf(t, "o/r").PRs) != 0 {
		t.Fatalf("%v %q", err, learningsOf("o/r"))
	}
	_ = os.Remove(f + ".tmp")
	pr.canFail = true
	if err := Outcomes(t.Context(), voteForge{pr: pr}, e); err == nil || len(tallyOf(t, "o/r").PRs) != 0 {
		t.Fatalf("CanWrite failed: %v %+v", err, tallyOf(t, "o/r"))
	}
	pr.canFail, pr.refreshFail = false, true // the description may be gone, and its profile with it
	if err := Outcomes(t.Context(), voteForge{pr: pr}, e); err == nil || len(tallyOf(t, "o/r").PRs) != 0 {
		t.Fatalf("Refresh failed: %v %+v", err, tallyOf(t, "o/r"))
	}
	pr.refreshFail = false
	if err := Outcomes(t.Context(), voteForge{pr: pr}, e); err != nil || len(learningsOf("o/r")) != 1 || tallyOf(t, "o/r").Total[dismissed] != 1 {
		t.Fatalf("%v %q %+v", err, learningsOf("o/r"), tallyOf(t, "o/r"))
	}
	pr.cfg, pr.ts[0].Path, e.PullRequest.Number = `{"learnings":`, "e.go", 8
	if err := Outcomes(t.Context(), voteForge{pr: pr}, e); err != nil || len(learningsOf("o/r")) != 1 || len(tallyOf(t, "o/r").PRs) != 2 {
		t.Fatalf("broken review.json: %v %q", err, learningsOf("o/r"))
	}
	if err := os.WriteFile(f, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.PullRequest.Number = 9
	var b bytes.Buffer
	if err := Outcomes(t.Context(), voteForge{pr: pr}, e); err == nil || Stats(&b, "o/r") == nil || Stats(&b, "") == nil {
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
	if ok, err := record(deep+"p", 1, []finding{{Severity: "major", Profile: "chill", Outcome: ignored}}); !ok || err != nil || tallyOf(t, deep+"p").Repo != deep+"p" {
		t.Errorf("long repo: %v %v", ok, err)
	}
	b.Reset()
	if err := Stats(&b, deep+"p"); err != nil || !strings.Contains(b.String(), deep+"p") {
		t.Errorf("stats of a long repo: %v", err)
	}
}

// One close saves its first maxLearnedPerClose distinct declines, and counts every one. A second of the same
// takes no place, one the repo has does. A retry after a failed count saves the same ones, so no more.
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
		return Outcomes(t.Context(), voteForge{pr: pr}, e)
	}
	if err := decline(6, "0.go"); err != nil {
		t.Fatal(err)
	}
	f := tallyFile("o/r")
	if err := os.MkdirAll(f+".tmp", 0o700); err != nil { // the tally's write fails
		t.Fatal(err)
	}
	paths := []string{"0.go", "1.go", "1.go", "2.go", "3.go", "4.go", "5.go", "6.go"}
	if err := decline(7, paths...); err == nil || len(learningsOf("o/r")) != maxLearnedPerClose {
		t.Fatalf("%v %q", err, learningsOf("o/r"))
	}
	_ = os.Remove(f + ".tmp")
	err := decline(7, paths...)
	ls := learningsOf("o/r")
	if err != nil || len(ls) != maxLearnedPerClose || !strings.Contains(ls[2], "(2.go") || tallyOf(t, "o/r").Total[dismissed] != 1+len(paths) {
		t.Fatalf("%v %q %+v", err, ls, tallyOf(t, "o/r"))
	}
}

// learnOnce knows an entry whatever its date, and only that entry: not one whose path starts like it.
func TestLearnOnce(t *testing.T) {
	proc.DataDir = t.TempDir()
	for _, l := range []string{"- X (a, b)", "- X (a)", "- X (a)"} {
		if err := learnOnce("o/r", l); err != nil {
			t.Fatal(err)
		}
	}
	d := time.Now().Format(time.DateOnly)
	if ls := learningsOf("o/r"); strings.Join(ls, "|") != "- X (a, b, "+d+")|- X (a, "+d+")" {
		t.Fatalf("%q", ls)
	}
}

// A close asks once about the threads a maintainer replied in and no reply decided: not about one chat decided, one
// only a stranger replied in, or one nobody replied in. An answer about a thread it wasn't asked about files nothing.
func TestDecideLeft(t *testing.T) {
	proc.DataDir = t.TempDir()
	defer func(a func(context.Context, forge.Forge, *forge.Event, string, string) (string, error)) {
		decideAsk = a
	}(decideAsk)
	var prompts []string
	answer := `sure: {"1":"Intended","2":"wrong","3":"fixed","4":"fixed","5":"maybe","6":"","99":"fixed"}`
	decideAsk = func(_ context.Context, _ forge.Forge, _ *forge.Event, prompt, _ string) (string, error) {
		prompts = append(prompts, prompt)
		return answer, nil
	}
	ann := func(s string) forge.Note { return forge.Note{Login: "ann", Body: s} }
	pr := &votePR{asked: map[string]int{}, ts: []forge.Thread{
		{Comment: 1, Body: "Leaks", Path: "a.go", Resolved: true, Notes: []forge.Note{ann("we want that"), {Login: "bot", Body: "ok", Ours: true}}},
		{Comment: 2, Body: "Races", Path: "b.go", Notes: []forge.Note{ann("no")}}, // chat decided it
		{Comment: 3, Body: "Naming", Path: "c.go", Notes: []forge.Note{{Login: "mallory", Body: "SECRET fixed"}}},
		{Comment: 4, Body: "Typo", Path: "d.go"},
		{Comment: 5, Body: "Spam", Path: "e.go", Notes: []forge.Note{ann("hm")}},
		{Comment: 6, Body: "Stale", Path: "f.go", Notes: []forge.Note{ann("why?"), {Login: "mallory", Body: "SECRET"}}},
	}}
	pr.cfg = `{}`
	e := &forge.Event{Forge: "github"}
	e.Repository.FullName, e.PullRequest.Number = "o/r", 7
	if err := decide("o/r", 7, 2, "declined"); err != nil {
		t.Fatal(err)
	}
	if err := Outcomes(t.Context(), voteForge{pr: pr}, e); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range tallyOf(t, "o/r").Findings {
		got = append(got, f.Decision)
	}
	if want := []string{"intended", "declined", "", "", "", ""}; !slices.Equal(got, want) {
		t.Errorf("decisions %q, want %q", got, want)
	}
	if len(prompts) != 1 || strings.Contains(prompts[0], "SECRET") || strings.Contains(prompts[0], "### 2 ") ||
		!strings.Contains(prompts[0], "### 1 a.go\nLeaks\n- maintainer: we want that\n- bot: ok\n") || !strings.Contains(prompts[0], "### 6 f.go\nStale\n- maintainer: why?\n\n") {
		t.Errorf("prompts %q", prompts)
	}
	// Nothing left to ask: no call. A failed call or an answer without JSON costs the decisions, not the count.
	e.PullRequest.Number, pr.ts = 8, pr.ts[2:4]
	if err := Outcomes(t.Context(), voteForge{pr: pr}, e); err != nil || len(prompts) != 1 {
		t.Fatalf("%v, %d calls", err, len(prompts))
	}
	answer = "I can't"
	e.PullRequest.Number, pr.ts = 9, []forge.Thread{{Comment: 1, Body: "Leaks", Path: "a.go", Notes: []forge.Note{ann("no")}}}
	if err := Outcomes(t.Context(), voteForge{pr: pr}, e); err != nil || len(prompts) != 2 || len(tallyOf(t, "o/r").PRs) != 3 {
		t.Fatalf("%v, %d calls", err, len(prompts))
	}
}

// A finding's row says what found it, from the stamp on its comment, whatever the version and models are at the
// close; one without a stamp, an oversized or forged one, or one in a reply counts with the fields empty.
func TestOutcomesFoundBy(t *testing.T) {
	defer func(v string) { Version = v }(Version)
	proc.DataDir, Version = t.TempDir(), "v2.0.0"
	stamp := forge.Stamp(forge.Run{Version: "v1.0.0", Review: "lab/big", Verify: "lab/small", SHA: "abcdef1234"})
	edge := "edge-" + strings.Repeat("a", 40)
	if stamp == "" || forge.Stamp(forge.Run{Version: "v1", Review: "a b", SHA: "abcdef1"}) != "" ||
		forge.Stamp(forge.Run{Version: edge, SHA: "abcdef1"}) == "" || forge.Stamp(forge.Run{Version: edge + edge, SHA: "abcdef1"}) != "" ||
		forge.Stamp(forge.Run{Version: "v1", SHA: "ABCDEF1"}) != "" {
		t.Fatalf("stamp %q", stamp)
	}
	if c := forge.Parse(forge.Clean(forge.Seal(forge.Finding{Run: stamp}))); strings.Contains(c.Body, "diffharbor") ||
		strings.Contains(forge.Unseal("x"+forge.Seal(forge.Finding{Run: stamp})), "diffharbor") {
		t.Fatalf("stamp left in %q", c.Body)
	}
	body := "_🟠 Major_ · **Leaks**\n\nbody"
	long := strings.Repeat("m", 81)
	pr := &votePR{asked: map[string]int{}, ts: []forge.Thread{
		{Comment: 1, Body: body + forge.Seal(forge.Finding{Run: stamp}), Path: "a.go"},
		{Comment: 2, Body: body + forge.Mark, Path: "b.go"}, // an older diffharbor
		{Comment: 3, Body: body + "\n\n" + strings.Replace(stamp, "lab/big", long, 1) + forge.Mark, Path: "c.go"},
		{Comment: 4, Body: body + "\n\n" + stamp + "\nmore, quoted from the PR\n" + forge.Mark, Path: "d.go"},
		{Comment: 5, Body: body + forge.Mark, Path: "e.go", Notes: []forge.Note{{Login: "mallory", Body: stamp + forge.Mark}}},
	}}
	pr.cfg = `{"review_model":"lab/other"}` // not the models of the run
	e := &forge.Event{Forge: "github"}
	e.Repository.FullName = "o/r"
	e.PullRequest.Number = 7
	if err := Outcomes(t.Context(), voteForge{pr: pr}, e); err != nil {
		t.Fatal(err)
	}
	fs := tallyOf(t, "o/r").Findings
	if len(fs) != 5 || fs[0].Version != "v2.0.0" || fs[0].FoundBy != "v1.0.0" || fs[0].ReviewModel != "lab/big" ||
		fs[0].VerifyModel != "lab/small" || fs[0].FoundSHA != "abcdef1234" {
		t.Fatalf("%+v", fs)
	}
	for _, f := range fs[1:] {
		if f.FoundBy+f.ReviewModel+f.VerifyModel+f.FoundSHA != "" {
			t.Errorf("no valid stamp, got %+v", f)
		}
	}
	var b bytes.Buffer
	if err := Stats(&b, "o/r"); err != nil || !strings.Contains(strings.Join(strings.Fields(b.String()), " "), "- 4 ") ||
		!strings.Contains(strings.Join(strings.Fields(b.String()), " "), "v1.0.0 1 ") {
		t.Fatalf("%v\n%s", err, b.String())
	}
}
