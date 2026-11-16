package review

import (
	"context"
	"diffharbor/internal/proc"
	"diffharbor/internal/ticket"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// termsRepo is a checkout at dir/repo, its files added to git for the search at the head.
func termsRepo(t *testing.T) *proc.Sandbox {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	files := map[string]string{
		"a.go":        "package a\n\nconst keep = true\n\nfunc get() { pin() }\n\nfunc pin() {}\n",
		"docs/old.md": "The OLD_PHASE flag picked the phase.\n",
	}
	for f, c := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := exec.Command("sh", "-c", "git init -q && git add .")
	c.Dir = repo
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	return &proc.Sandbox{Dir: dir}
}

// Every quote is in its source and every place at the head, or it is dropped; an ask that names what must be gone
// is answered by a search; the check pass's rejections drop, its keeps carry AI cross-check.
func TestReadTerms(t *testing.T) {
	sb := termsRepo(t)
	open := ticket.Ticket{
		Key: "ABC-1", Title: "Pin the dataset", Outcome: "Every request pins the dataset.",
		DoneWhen: []string{"Requests carry `dataset`.", "No `OLD_PHASE` remains.", "Tests cover paging."},
		Notes:    "Needs confirmation: keep accounts or wipe them.",
		Body:     "Intro.\n\n## Merge gate\n\nDo not merge   before the cutover.\n",
	}
	done := ticket.Ticket{Key: "ABC-2", Title: "Move the data", Done: true, Body: "Merge after the data is moved."}
	gates := `{"gates":[
{"text":"Merge after XYZ-9.","in":"description","gate":"XYZ-9 to merge","ticket":"XYZ-9","clears":"XYZ-9 is done"},
{"text":"Do not merge before the cutover.","in":"ABC-1","gate":"the cutover","ticket":"ABC-1","clears":"ABC-1 is done"},
{"text":"Merge after the data is moved.","in":"ABC-2","gate":"the data move","ticket":"ABC-2","clears":"ABC-2 is done"},
{"text":"Merge after the moon landing.","in":"description","gate":"the moon","ticket":"","clears":""}],
"serves":["… serves only one raid.","The service serves … one raid.","It serves everything."],"order":[]}`
	contract := `{"tickets":[{"key":"ABC-1","rows":[
{"ask":"outcome","status":"Met","at":["a.go:5"],"note":"get pins it"},
{"ask":"done 1","status":"Met","at":["a.go:99"]},
{"ask":"done 2","status":"Met","absent":["OLD_PHASE","pin"]},
{"ask":"done 3","status":"Deviates","at":["a.go:2-3"],"note":"no paging test"}],
"beyond":"adds a retry",
"answers":{"changed":{"text":"Requests pin the dataset in a.go:5.","at":["a.go:5","gone.go:1"]},"runs":{"text":"The client.","at":["a.go"]},"relies":{"text":"Nothing.","at":[]},"first":{"text":"zzz.go:999: the bug.","at":["a.go:5"]}}}],
"differences":[
{"ticket":"ABC-1","status":"Decided in the PR","title":"The PR keeps accounts","line":"ABC-1 left it open; the code keeps accounts.","asks":["Needs confirmation: keep accounts or wipe them."],"does":"Keeps accounts.","at":"a.go:3","excerpt":"a.go:1-4"},
{"ticket":"ABC-1","status":"Deviates","title":"Made up","line":"x","asks":["Words the ticket never said."],"does":"x","at":"a.go:3"},
{"ticket":"","status":"Beyond the tickets","title":"Retries were added","line":"No ticket asks.","does":"Retries.","at":"a.go:99"},
{"ticket":"","status":"Beyond the tickets","title":"one two three four five six seven eight nine ten eleven twelve thirteen","line":"x","at":"a.go:4"},
{"ticket":"ABC-1","status":"Approved","title":"A status off the list","line":"x","asks":["Every request pins the dataset."],"at":"a.go:5"},
{"ticket":"","status":"Beyond the tickets","title":"A retry loop was added","line":"No ticket asks for it.","does":"Retries.","at":"a.go:5","excerpt":"a.go:1-3"},
{"ticket":"","status":"Beyond the tickets","title":"Logging was added","line":"No ticket asks for it.","does":"Logs.","at":"a.go:7"},
{"ticket":"","status":"Beyond the tickets","title":"Paging was added","line":"one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty twentyone","does":"Pages.","at":"a.go:7"},
{"ticket":"","status":"Beyond the tickets","title":"Caching was added","line":"No ticket asks for it.","does":"Caches in zzz.go:4.","at":"a.go:7"}]}`
	// claims: 0 the decided difference, 1 the retry loop, 2 the logging, 3 the Met outcome, 4 the Deviates row, 5 the
	// answers; the retry loop gets no verdict
	check := `{"verdicts":[{"i":0,"keep":true},{"i":2,"keep":false},{"i":3,"keep":true},{"i":4,"keep":false}]}`
	f := &fakeAgent{replies: map[string][]string{"gates": {gates}, "contract": {contract}, "check": {check}}}
	r := ReadTerms(context.Background(), TermsInput{
		Agent: f, Sandbox: sb, Body: "Ships the client. Merge after XYZ-9. The service serves only one raid.",
		Tickets: []ticket.Ticket{open, done}, Summary: "The client pins its dataset.",
		Flow: []Step{{Title: "Pin", Why: "get calls pin", Files: []string{"a.go"}}}, Walk: map[string]string{"a.go": "pins"},
		Findings: []Comment{{Path: "a.go", Line: 5, Title: "pin ignores errors"}}, Decls: []string{"a.go:5 get"},
	})
	if len(r.Errs) > 0 {
		t.Fatal(r.Errs)
	}

	want := []Gate{
		{Quote: Quote{Text: "Merge after XYZ-9.", In: "description"}, Name: "XYZ-9 to merge", Ticket: "XYZ-9", Clears: "XYZ-9 is done", Waits: true},
		{Quote: Quote{Text: "Do not merge before the cutover.", In: "ABC-1", Section: "Merge gate"}, Name: "the cutover", Ticket: "ABC-1", Cross: true, Clears: "ABC-1 is done", Waits: true},
		{Quote: Quote{Text: "Merge after the data is moved.", In: "ABC-2", Section: "Description"}, Name: "the data move", Ticket: "ABC-2", Cross: true, Clears: "ABC-2 is done"},
	}
	if !slices.Equal(r.Gates, want) {
		t.Errorf("gates:\n%+v\nwant\n%+v", r.Gates, want)
	}
	if !slices.Equal(r.Serves, []string{"… serves only one raid."}) {
		t.Errorf("serves: %q", r.Serves)
	}

	if len(r.Contracts) != 1 || r.Contracts[0].Ticket != "ABC-1" {
		t.Fatalf("one contract, for the ticket with one: %+v", r.Contracts)
	}
	c := r.Contracts[0]
	rows := []string{
		"Outcome: Every request pins the dataset.|Met|AI cross-check|a.go:5", "Done when 1: Requests carry `dataset`.|Not checked||",
		"Done when 2: No `OLD_PHASE` remains.|Met in the code|Search at head|", "Done when 3: Tests cover paging.|Not checked||",
		"Beyond the tickets|Beyond the tickets|Inferred by the review|",
	}
	var got []string
	for _, w := range c.Rows {
		got = append(got, strings.Join([]string{w.Ask, w.Status, w.Evidence, strings.Join(w.At, ",")}, "|"))
	}
	if !slices.Equal(got, rows) {
		t.Errorf("rows:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(rows, "\n"))
	}
	if !slices.Equal(c.Rows[2].Docs, []string{"docs/old.md"}) {
		t.Errorf("the docs that still name it: %q", c.Rows[2].Docs)
	}
	if !slices.Equal(c.Changed.At, []string{"a.go:5"}) || !slices.Equal(c.Runs.At, []string{"a.go"}) || c.Evidence != Inferred {
		t.Errorf("answers: places at the head, no verdict stays inferred: %+v", c)
	}
	if c.First.Text != "" || c.First.At != nil {
		t.Errorf("an answer with a place in its text the head lacks is dropped: %+v", c.First)
	}

	if len(r.Differences) != 2 {
		t.Fatalf("two differences survive: %+v", r.Differences)
	}
	d := r.Differences[0]
	if d.Status != Decided || d.Evidence != CrossCheck || d.Excerpt != "a.go:1-4" || len(d.Asks) != 1 || d.Asks[0].Section != "Notes" {
		t.Errorf("difference: %+v", d)
	}
	if d := r.Differences[1]; d.Title != "A retry loop was added" || d.Evidence != Inferred || d.Excerpt != "a.go:5" {
		t.Errorf("an excerpt that doesn't hold its place falls back to it; no verdict stays inferred: %+v", d)
	}

	for name, in := range map[string][]string{
		"gates":    {"Merge after XYZ-9", "## ABC-2", "Merge after the data is moved"},
		"contract": {"Needs confirmation", "3. Tests cover paging.", "Pin: get calls pin (a.go)", "a.go:5 pin ignores errors", "a.go:5 get"},
	} {
		for _, s := range in {
			if !strings.Contains(f.contTask[name], s) {
				t.Errorf("%s prompt lacks %q:\n%s", name, s, f.contTask[name])
			}
		}
	}
	if strings.Contains(f.contTask["gates"], "Intro.") || strings.Contains(f.contTask["gates"], "Needs confirmation") {
		t.Error("of a ticket, the gate pass reads only the paragraphs on the merge")
	}
	if strings.Contains(f.contTask["contract"], "ABC-2") {
		t.Error("a ticket without a contract isn't in the contract prompt")
	}
	if task := f.tasks["check"]; strings.Contains(task, "Done when 2") || !strings.Contains(task, "Every request pins the dataset.") ||
		!strings.Contains(task, "5. ABC-1, four answers") {
		t.Errorf("the check pass checks what a model claimed, not the search:\n%s", task)
	}
}

// Nothing to read, no call; a ticket without a contract gets no contract pass; a failed pass leaves its fields
// empty and says so, a failed check leaves the contract inferred.
func TestReadTermsFails(t *testing.T) {
	sb := termsRepo(t)
	f := &fakeAgent{replies: map[string][]string{}}
	none := TermsInput{Agent: f, Sandbox: sb, Body: "  ", Tickets: []ticket.Ticket{{Key: "ABC-3", Body: "Do it."}}}
	if r := ReadTerms(context.Background(), none); len(f.calls) > 0 || len(r.Errs) > 0 {
		t.Errorf("no description, no ticket on the merge or with a contract: no call: %v %v", f.calls, r.Errs)
	}

	r := ReadTerms(context.Background(), TermsInput{Agent: f, Sandbox: sb, Body: "Adds a flag.", Tickets: []ticket.Ticket{{Key: "ABC-3", Body: "Do it."}}})
	if !slices.Equal(f.calls, []string{"gates repair"}) || len(r.Errs) != 1 || !strings.HasPrefix(r.Errs[0], "gates: no JSON") || r.Gates != nil {
		t.Errorf("only the gate pass, failed: %v %v", f.calls, r.Errs)
	}

	diff := `{"tickets":[],"differences":[{"ticket":"","status":"Beyond the tickets","title":"A retry loop was added","line":"No ticket asks for it.","at":"a.go:5"}]}`
	f = &fakeAgent{replies: map[string][]string{"gates": {`{"gates":[]}`}, "contract": {diff}, "check": {"thinking", "still no JSON"}}}
	r = ReadTerms(context.Background(), TermsInput{Agent: f, Sandbox: sb, Tickets: []ticket.Ticket{{Key: "ABC-4", Outcome: "It retries."}}})
	if len(r.Errs) != 1 || !strings.HasPrefix(r.Errs[0], "check: no JSON") {
		t.Fatalf("errs: %v, calls %v", r.Errs, f.calls)
	}
	if len(r.Differences) != 1 || r.Differences[0].Evidence != Inferred || r.Differences[0].Status != "Beyond the ticket" {
		t.Errorf("unchecked, one ticket's label: %+v", r.Differences)
	}
	if len(r.Contracts) != 1 || r.Contracts[0].Rows[0].Status != NotChecked {
		t.Errorf("an ask the reply skips reads Not checked: %+v", r.Contracts)
	}
}

func TestQuoted(t *testing.T) {
	const src = "Order matters: **wipe → migrate → start**.\nApp rollback alone does not work."
	for q, want := range map[string]bool{
		"Order matters: **wipe → migrate → start**. App rollback": true, // a line break is a space
		"Order matters: …":                true, // a cut at the end
		"… does not work.":                true,
		"Order matters: … does not work.": false, // no stitching across a cut
		"Do … not":                        false,
		"Order matters: wipe → migrate":   false, // bold stays
		"…":                               false,
	} {
		if quoted(src, q) != want {
			t.Errorf("quoted(%q) = %v", q, !want)
		}
	}
}

// A quote loses the Markdown around it and stays in its source: "**Merge after X.**", "- cmd/a records …".
func TestBareQuote(t *testing.T) {
	const body = "Notes\n\n**Merge after WID-101.**\n\n- cmd/ingest -backfill records past builds.\n1. Run 0011 first.\n**a** and **b**"
	for q, want := range map[string]string{
		"**Merge after WID-101.**":                    "Merge after WID-101.",
		"- cmd/ingest -backfill records past builds.": "cmd/ingest -backfill records past builds.",
		"1. Run 0011 first.":                          "Run 0011 first.",
		"Merge after WID-101.":                        "Merge after WID-101.",
		"**a** and **b**":                             "**a** and **b**",                // not one emphasis
		"-backfill records past builds.":              "-backfill records past builds.", // a flag isn't a marker
	} {
		got, ok := quote(q, "description", body, nil)
		if !ok || got.Text != want {
			t.Errorf("quote(%q) = %q, %v; want %q", q, got.Text, ok, want)
		}
	}
}

// A place stands when the head has every line of it, at most 40; a bare path only where one may stand.
func TestPlaced(t *testing.T) {
	line := func(p string, n int) (string, bool) { return "", p == "a.go" && n >= 1 && n <= 60 }
	for _, c := range []struct {
		at   string
		bare bool
		want string
	}{
		{"a.go:5", false, "a.go:5"},
		{"a.go:5–7", false, "a.go:5-7"},
		{"a.go:1-40", false, "a.go:1-40"},
		{"a.go:5-99", false, ""}, // its end is past the file
		{"a.go:1-41", false, ""}, // 41 lines
		{"a.go:9-5", false, ""},
		{"gone.go:1", false, ""},
		{"a.go", true, "a.go"},
		{"a.go", false, ""},
	} {
		if got := placed(line, c.at, c.bare); got != c.want {
			t.Errorf("placed(%q, %v) = %q, want %q", c.at, c.bare, got, c.want)
		}
	}
}
