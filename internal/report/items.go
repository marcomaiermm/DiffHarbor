package report

import (
	"cmp"
	"diffharbor/internal/diff"
	"diffharbor/internal/review"
	"diffharbor/internal/ticket"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The items Phase 3's data adds to the page: the gates and differences of the contract passes (spec 4, 5.6, 5.7),
// Before you merge's order, environment, outside service and CI lines (5.6), and Test changes (5.8).

// schip is a status chip (component 2): the status and its icon.
type schip struct{ Label, Icon string }

// tag is a card's tag in its meta row: "From an earlier review, still open"; the script adds Since's.
type tag struct{ Icon, Text string }

// ttable is a table of a test-change group (component 18) under its label and chip; More waits behind a disclosure.
type ttable struct {
	Label string
	Chip  chip
	Cols  []string
	Rows  [][]string // cells as copy with `code` spans
	More  [][]string
}

// Rest is the rows behind the disclosure, as a table of their own.
func (t ttable) Rest() ttable { return ttable{Cols: t.Cols, Rows: t.More} }

// sample is one example of a kind of test flag: its place, what it says, and an expectation's two sides.
type sample struct {
	Place, Text, Before, After string
}

// tests is Test changes (5.8): its intro and a card per group, group 2 in its part when it has one.
type tests struct {
	Intro string
	Cards []*card
}

var evSearch = chip{"shown", "Search at head"}

// evOf is the chip of a contract pass's evidence label.
func evOf(e string) chip {
	switch e {
	case review.CrossCheck:
		return evAI
	case review.SearchAtHead:
		return evSearch
	case "":
		return chip{}
	}
	return chip{"inf", review.Inferred}
}

// wordChip is the chip of a quote's source: the author's for the description, else its ticket's.
func wordChip(in string) chip {
	if in == "" || in == "description" {
		return chip{"word", "Author"}
	}
	return chip{"tk", in + " says"}
}

// cite is where a quote comes from: "From the PR description", "From BIS-247, Notes".
func cite(q review.Quote) string {
	if q.In == "" || q.In == "description" {
		return "From the PR description"
	}
	return "From " + q.In + ", " + cmp.Or(q.Section, "Description")
}

// statusChip is a status with its icon (section 4): Met's circle checks only on a machine's evidence.
func statusChip(status string, ev chip) *schip {
	icon := map[string]string{review.Deviates: "split", review.Decided: "git-pull-request", review.NotChecked: "circle-question-mark", review.NotInScope: "circle-slash"}[status]
	switch {
	case status == "":
		return nil
	case status == review.Met || status == review.MetInCode:
		icon = "circle"
		if ev.Kind == "shown" || ev.Kind == "run" {
			icon = "circle-check"
		}
	case strings.HasPrefix(status, "Beyond"):
		icon = "square-plus"
	}
	return &schip{status, icon}
}

// span is a place "path:line", "path:from-to" or "path": its path and lines, 0 without.
func span(at string) (p string, from, to int) {
	p, r, ok := strings.Cut(at, ":")
	if !ok {
		return at, 0, 0
	}
	a, b, _ := strings.Cut(r, "-")
	from, _ = strconv.Atoi(a)
	to, err := strconv.Atoi(b)
	if err != nil || to < from {
		to = from
	}
	return p, from, to
}

// waiting is the gates that make the merge wait, one per name: its card's ID.
func (v *view) waiting() []review.Gate {
	var out []review.Gate
	for _, g := range v.Terms.Gates {
		if g.Waits && !slices.ContainsFunc(out, func(o review.Gate) bool { return o.Name == g.Name }) {
			out = append(out, g)
		}
	}
	return out
}

// gateKey is a gate's Version.Items place: its ticket, else its exact quote; the model words its name anew each run.
func gateKey(g review.Gate) string { return cmp.Or(g.Ticket, g.Text) }

// gateLink is what a link to a gate names it: "the cutover gate", else "the gate".
func gateLink(name string) string {
	if strings.HasPrefix(name, "the ") {
		return name + " gate"
	}
	return "the gate"
}

// gateTracker is the gate's tracker line: the ticket's state as read, or why the page doesn't have it.
func (v *view) gateTracker(g review.Gate, long bool) mline {
	for _, t := range v.Tickets {
		if t.Key != g.Ticket || g.Ticket == "" {
			continue
		}
		s := t.Key
		if long && t.Title != "" {
			s += ", “" + t.Title + "”,"
		}
		s += " is " + cmp.Or(t.State, "in no state")
		if !t.Due.IsZero() {
			s += ", due " + t.Due.Format(time.DateOnly)
		}
		s += "."
		if !t.Read.IsZero() {
			s += " Read " + t.Read.Format(time.DateOnly) + "."
		}
		c := chip{"shown", "Tracker"}
		if g.Cross {
			c = evAI // the model matched the ticket: the text doesn't name it
		}
		return mline{Chip: c, Text: s}
	}
	if len(v.Tickets) == 0 {
		if g.Ticket != "" {
			return mline{Text: g.Ticket + "'s state isn't on this page: no ticket was read."}
		}
		return mline{Text: "No ticket was read, so the state of what the gate waits for isn't on this page."}
	}
	if g.Ticket == "" {
		return mline{Text: "No ticket read for this review names " + g.Name + "."}
	}
	return mline{Text: g.Ticket + "'s state isn't on this page."}
}

// clears is what ends the wait (rule 2): a later run that reads the ticket as Done, or no longer finds the words.
func (v *view) clears(g review.Gate) string {
	ticketRead := slices.ContainsFunc(v.Tickets, func(t ticket.Ticket) bool { return t.Key == g.Ticket && g.Ticket != "" })
	who := cmp.Or(g.Ticket, "the ticket it waits for")
	if ticketRead {
		return "Clears when " + who + " is Done. DiffHarbor reads the tracker again on its next run."
	}
	from := "the description"
	if g.In != "" && g.In != "description" {
		from = g.In
	}
	return "Clears when a later run reads " + who + " as Done, or " + from + " no longer asks to wait."
}

// gateCard is a gate in Before you merge (5.6): always open, its quote, the tracker line and how a run starts.
func (v *view) gateCard(g review.Gate) *card {
	k := &card{ID: slug(g.Name) + "-gate", Kind: "Before merging", Gate: true, Title: "The merge waits for " + g.Name, Chip: wordChip(g.In), Keys: []string{"gate\t" + gateKey(g)}}
	k.Blocks = []cblock{{Chip: k.Chip, Lines: []cline{{Text: g.Text, Quote: true, Cite: cite(g.Quote)}}}}
	t := v.gateTracker(g, true)
	k.Blocks = append(k.Blocks, cblock{Chip: t.Chip, Lines: []cline{{Text: t.Text}}})
	c := v.clears(g)
	run := "a run starts on a push"
	if v.Bot != "" {
		run += ", or when someone comments “@" + v.Bot + " review” on the PR"
	}
	if strings.HasSuffix(c, "next run.") {
		k.Clr = strings.TrimSuffix(c, ".") + ": " + run + "."
	} else {
		k.Clr = c + " " + sentenceCase(run) + "."
	}
	return k
}

// excerptAt is lines from..to of p, cited cited..to: the head's text from Sources or the new file, else a quoted
// excerpt of Data.Excerpts, which may be the base's.
func (v *view) excerptAt(p string, from, to, cited int) *excerpt {
	if x := v.excerpt(p, from, to, cited); x != nil {
		return x
	}
	for _, e := range v.Excerpts {
		if e.Path != p || from < e.From || from >= e.From+len(e.Lines) {
			continue
		}
		lines := make([]string, e.From-1, e.From-1+len(e.Lines))
		lines = append(lines, e.Lines...)
		x := v.cut(p, lines, e.From, from, to, cited)
		if e.Base {
			x.At, x.Href = "on the base", ""
		}
		return x
	}
	return nil
}

// diffCard is a difference (5.7): what the ticket asks, what the code does with its excerpt, the coverage note.
func (v *view) diffCard(i int, d review.Difference) *card {
	p, line, _ := span(d.At)
	k := &card{ID: "difference-" + strconv.Itoa(i+1), Kind: "Difference", Title: d.Title, Line: d.Line, Chip: evOf(d.Evidence), Ticket: d.Ticket, Keys: []string{"difference\t" + d.At}, Paths: []string{p}}
	k.St = statusChip(d.Status, k.Chip)
	k.Place, k.PlaceHref = v.placeOf(p, line)
	k.Box = v.boxOf(p)
	if len(d.Asks) > 0 {
		b := cblock{Label: "Ticket", Chip: wordChip(cmp.Or(d.Ticket, d.Asks[0].In))}
		for _, q := range d.Asks {
			b.Lines = append(b.Lines, cline{Text: q.Text, Quote: true, Cite: cite(q)})
		}
		k.Blocks = append(k.Blocks, b)
	}
	does := cblock{Label: "Code", Chip: chip{"shown", "Code at head"}}
	if v.files[p] != nil {
		does.Chip = evDiff
	}
	if xp, from, to := span(d.Excerpt); xp == p && from > 0 {
		does.Excerpt = v.excerptAt(p, from, to, max(line, from))
	}
	if d.Does != "" {
		does.Lines = []cline{{Text: d.Does}}
	}
	k.Blocks = append(k.Blocks, does)
	if slices.Contains(v.Omitted, p) || slices.Contains(v.Failed, p) {
		k.Cov = "The review didn't read this file, so no finding of this run covers it."
	}
	k.GH, k.GHText = v.ghLink(p, line)
	k.GHPlace = d.At
	return k
}

// orderBlock is the docs and tests at the head that name a migration (Data.Migrations).
func (v *view) orderBlock(path string) *cblock {
	for _, m := range v.Migrations {
		if m.Path != path {
			continue
		}
		b := cblock{Label: "Docs and tests that name it", Chip: evSearch}
		for _, d := range m.Docs {
			b.Lines = append(b.Lines, cline{Lead: "Doc:", Code: d, Href: v.placeHref(d)})
		}
		for _, t := range m.Tests {
			b.Lines = append(b.Lines, cline{Lead: "Test:", Code: t, Href: v.placeHref(t), Note: v.notRun(t)})
		}
		switch {
		case m.Cut:
			b.Lines = append(b.Lines, cline{Text: "The search stopped early, so this list may be short."})
		case len(b.Lines) == 0:
			b.Lines = []cline{{Text: "No doc or test at " + short(v.SHA) + " names it."}}
		}
		return &b
	}
	return nil
}

// placeHref is a place's link into the page's diff, "" when the page doesn't show its file.
func (v *view) placeHref(at string) string {
	p, from, _ := span(at)
	if v.files[p] == nil {
		return ""
	}
	if from > 0 {
		return lineHref(p, from)
	}
	return "#" + p
}

// lineHref is the page's link to line n of p. html/template reads a colon before any slash as a URL scheme and drops
// the link, so a root file's colon is escaped: the script decodes the hash.
func lineHref(p string, n int) string {
	if !strings.Contains(p, "/") {
		return "#" + p + "%3A" + strconv.Itoa(n)
	}
	return "#" + p + ":" + strconv.Itoa(n)
}

// notRun is the note of the test at at: no result for it reached this page, unless a run backs the Go or Python test
// declared on that line (section 4). A line that declares no test keeps the note.
func (v *view) notRun(at string) string {
	p, n, _ := span(at)
	l := v.headLine(p, n)
	m := goTestFn.FindStringSubmatch(l)
	if m == nil {
		m = pyTestFn.FindStringSubmatch(l)
	}
	if m != nil && ranOf(v.Data.Tests.Ran, m[1]) != "" {
		return ""
	}
	return "· not run here"
}

// headLine is line n of p at the head, from Sources or a head Excerpt; "" when the page has neither.
func (v *view) headLine(p string, n int) string {
	if src := v.Sources[p]; src != "" {
		if ls := strings.Split(src, "\n"); n > 0 && n <= len(ls) {
			return ls[n-1]
		}
		return ""
	}
	for _, e := range v.Excerpts {
		if e.Path == p && !e.Base && n >= e.From && n < e.From+len(e.Lines) {
			return e.Lines[n-e.From]
		}
	}
	return ""
}

// envBlock is where the head reads the card's variables (Data.Env), each with its part.
func (v *view) envBlock(items []riskItem) *cblock {
	if v.Env == nil {
		return nil
	}
	b := cblock{Label: "Where they're read", Chip: evSearch}
	cut := false
	for _, it := range items {
		e, ok := v.Env[it.What]
		if !ok {
			continue
		}
		cut = cut || e.Cut
		for _, r := range e.Reads {
			p, _, _ := span(r)
			note := "`" + it.What + "`"
			if pt := v.partOf(p); pt != nil {
				note += " in " + pt.Label
			}
			b.Lines = append(b.Lines, cline{Code: r, Href: v.placeHref(r), Text: note})
		}
	}
	if cut {
		b.Lines = append(b.Lines, cline{Text: "The search stopped early, so this list may be short."})
	}
	if len(b.Lines) == 0 {
		return nil
	}
	return &b
}

// quotes is the author's lines as quote lines of a block.
func quotes(label string, lines []string) cblock {
	b := cblock{Label: label, Chip: wordChip("")}
	for _, l := range lines {
		b.Lines = append(b.Lines, cline{Text: l, Quote: true, Cite: "From the PR description"})
	}
	return b
}

// beforeExtras adds to Before you merge what the passes and the forge read: the order's docs, tests and the author's
// lines, where variables are read, what an outside service serves, and the CI that judges the PR.
func (v *view) beforeExtras() {
	p := &v.Page
	order := v.Terms.Order
	for _, k := range p.Before {
		switch {
		case strings.HasPrefix(k.Box, "deploy:") && migration.MatchString(strings.TrimPrefix(k.Box, "deploy:")):
			path := strings.TrimPrefix(k.Box, "deploy:")
			if b := v.orderBlock(path); b != nil {
				k.Blocks = append(k.Blocks, *b)
			}
			if len(order) > 0 {
				k.Blocks = append(k.Blocks, quotes("Author", order))
				order = nil
			}
		case k.Box == "deploy:environment":
			var items []riskItem
			for _, r := range v.Risks {
				if r.Title == "Environment" {
					items = r.Items
				}
			}
			if b := v.envBlock(items); b != nil {
				k.Blocks = append(k.Blocks, *b)
			}
		case k.Box == "deploy:ci" && v.CI != nil && v.CI.Edited:
			k.Title = "This PR edits the CI that judges it"
			l := cline{Text: "`" + v.CI.Tests + "` runs the tests on a pull request, and this PR changes it."}
			k.Blocks = append(k.Blocks, cblock{Label: "Checks", Chip: evBase, Lines: []cline{l}})
			for _, c := range v.CI.Checks {
				if c.URL != "" && (c.Path == ".github/workflows/"+v.CI.Tests || c.Path == v.CI.Tests) {
					k.GH, k.GHText, k.GHPlace = c.URL, "Open the run on "+cmp.Or(v.Forge.Name, "GitHub"), c.URL
					break
				}
			}
		}
	}
	if len(order) > 0 {
		k := &card{ID: "order", Kind: "Before merging", Title: "The author names an order to merge and deploy in", Line: order[0], Chip: wordChip("")}
		k.Blocks = []cblock{quotes("Author", order)}
		p.Before = append(p.Before, k)
	}
	if s := v.Terms.Serves; len(s) > 0 {
		k := &card{ID: "serves", Kind: "Before merging", Title: "What an outside service serves, in the author's words", Line: s[0], Chip: wordChip("")}
		k.Blocks = []cblock{quotes("Author", s)}
		p.Before = append(p.Before, k)
	}
}

// testCards is Test changes' groups (5.8), each one open item; group 2 goes in the part of the code it tests.
func (v *view) testCards() (in []*card) {
	tc := v.TestChanges
	t := &v.Page.Tests
	n := 0
	for _, k := range []*card{v.withCard(), v.keptCard(), v.stayCard()} {
		switch {
		case k == nil:
			continue
		case k.Box != "":
			in = append(in, k)
		default:
			t.Cards = append(t.Cards, k)
		}
		n++
	}
	if files := len(tc.With) + len(tc.Kept) + len(tc.Stay); files > 0 {
		t.Intro = "The review flags every test the PR deletes or weakens: " + plural(files, "file", "files") + ", in " + plural(n, "group", "groups") + "."
		if len(in) > 0 {
			t.Intro += " The group of tests deleted while their code stays sits in " + v.partOf(in[0].GHPlace).Label + ", beside that code."
		}
	}
	if u := len(tc.Unsorted); u > 0 {
		t.Intro = strings.TrimSpace(t.Intro + " " + plural(u, "more deleted test file fits", "more deleted test files fit") + " no group: nothing named what it tests, or the search stopped early.")
	}
	return in
}

// caseCount is the test cases of fs.
func caseCount(fs []TestFile) (n int) {
	for _, f := range fs {
		n += len(f.Tests)
	}
	return n
}

// withCard is group 1, the tests deleted with their code: those that import a file the PR deletes, and those whose
// names no file at the head names.
func (v *view) withCard() *card {
	w, sha := v.TestChanges.With, short(v.SHA)
	if len(w) == 0 {
		return nil
	}
	var imp, gone [][]string
	for _, f := range w {
		n := strconv.Itoa(len(f.Tests))
		if f.Imports != "" {
			imp = append(imp, []string{"`" + f.Path + "`", n, "`" + f.Imports + "`"})
			continue
		}
		one := ""
		if len(f.Tests) > 0 {
			one = "“" + f.Tests[0].Title + "”"
		}
		gone = append(gone, []string{"`" + f.Path + "`", n, join(codeAll(f.Names)), one})
	}
	k := &card{ID: "tests-with", Kind: "Test change", Title: "Deleted with their code", Chip: chip{"shown", "Imports"}, Place: plural(len(w), "file", "files"), Keys: []string{"test-with"}}
	var why []string
	if len(imp) > 0 {
		why = append(why, strconv.Itoa(len(imp))+" import code this PR deletes")
		k.Tables = append(k.Tables, ttable{Label: plural(len(imp), "file imports", "files import") + " code this PR deletes", Chip: k.Chip, Cols: []string{"File", "Tests", "Imports"}, Rows: imp})
	}
	if len(gone) > 0 {
		why = append(why, strconv.Itoa(len(gone))+" test code that no file at "+sha+" names")
		k.Tables = append(k.Tables, ttable{Label: plural(len(gone), "file tests", "files test") + " code that no file at " + sha + " names", Chip: evSearch, Cols: []string{"File", "Tests", "Tests what", "One of its tests"}, Rows: gone})
		if len(imp) == 0 {
			k.Chip = evSearch
		}
	}
	k.Line = plural(len(w), "file", "files") + ", " + plural(caseCount(w), "test", "tests") + ". " + sentenceCase(join(why)) + "."
	k.Stats = plural(len(w), "file", "files") + " · " + plural(caseCount(w), "test", "tests") + " deleted"
	return k
}

// keptCard is group 2, tests deleted while the head still names what they test; its Box is its part's when the
// first place the head names it has one.
func (v *view) keptCard() *card {
	kp := v.TestChanges.Kept
	if len(kp) == 0 {
		return nil
	}
	k := &card{ID: "tests-kept", Kind: "Test change", Title: "Tests deleted while the code they test stays", Chip: evSearch, Keys: []string{"test-kept"}}
	k.Line = plural(len(kp), "file", "files") + ", " + plural(caseCount(kp), "test", "tests") + ". Code at " + short(v.SHA) + " still names what they test."
	k.Stats = plural(len(kp), "file", "files") + " · " + plural(caseCount(kp), "test", "tests") + " deleted"
	del := cblock{Label: "Deleted", Chip: evDiff}
	found := cblock{Label: "Still at the head", Chip: evSearch}
	for _, f := range kp {
		k.Paths = append(k.Paths, f.Path)
		del.Lines = append(del.Lines, cline{Code: f.Path, Text: "on the base:"})
		for _, c := range f.Tests {
			del.Lines = append(del.Lines, cline{Text: "“" + c.Title + "” (:" + strconv.Itoa(c.Line) + ")"})
		}
		for _, at := range f.Found {
			found.Lines = append(found.Lines, cline{Code: at, Href: v.placeHref(at), Text: "names " + join(codeAll(f.Names))})
		}
	}
	k.Blocks = []cblock{del, found}
	if at := kp[0].Found; len(at) > 0 {
		p, line, _ := span(at[0])
		k.Place, k.PlaceHref = v.placeOf(p, line)
		k.GHPlace = p // blocks() places a card by it
		if pt := v.partOf(p); pt != nil {
			k.Box = "part:" + pt.ID
		}
		k.GH, k.GHText = v.ghLink(p, line)
	}
	return k
}

// stayCard is group 3, tests changed in files that stay: the files with the most flags, then one flag of each kind.
func (v *view) stayCard() *card {
	st := v.TestChanges.Stay
	if len(st) == 0 {
		return nil
	}
	k := &card{ID: "tests-stay", Kind: "Test change", Title: "Changed in files that stay", Chip: evDiff, Place: plural(len(st), "file", "files"), Keys: []string{"test-stay"}}
	by, flags := map[string]int{}, 0
	var rows [][]string
	for _, f := range st {
		flags += len(f.Flags)
		rows = append(rows, []string{"`" + f.Path + "`", strconv.Itoa(len(f.Flags))})
		for _, fl := range f.Flags {
			kind := flagKind(fl.What)
			if by[kind] == 0 {
				k.Samples = append(k.Samples, sampleOf(f.Path, fl))
			}
			by[kind]++
		}
	}
	var what []string
	for _, c := range []struct{ kind, one, many string }{
		{"deleted", "test deleted", "tests deleted"},
		{"assertions", "file loses assertions", "files lose assertions"},
		{"expectation", "expectation changed", "expectations changed"},
		{"skip", "test skipped or narrowed", "tests skipped or narrowed"},
	} {
		if n := by[c.kind]; n > 0 {
			what = append(what, plural(n, c.one, c.many))
		}
	}
	k.Line = plural(len(st), "file", "files") + ": " + join(what) + "."
	k.Stats = plural(len(st), "file", "files") + " · " + plural(flags, "flag", "flags") + ": " + join(what)
	tb := ttable{Label: "Files with the most flags", Chip: evDiff, Cols: []string{"File", "Flags"}, Rows: rows[:min(6, len(rows))]}
	if len(rows) > 6 {
		tb.More = rows[6:]
	}
	k.Tables = []ttable{tb}
	return k
}

// flagKind is a test flag's kind by its words (diff.Outline.Weakened).
func flagKind(what string) string {
	switch {
	case what == "test deleted":
		return "deleted"
	case strings.HasPrefix(what, "expectation changed"):
		return "expectation"
	case strings.HasPrefix(what, "skip/only"):
		return "skip"
	case strings.Contains(what, "assertions removed"):
		return "assertions"
	}
	return what
}

// sampleOf is a flag as Test changes shows one of its kind.
func sampleOf(path string, f diff.Flag) sample {
	s := sample{Place: path, Text: sentenceCase(f.What)}
	switch flagKind(f.What) {
	case "deleted":
		if _, title, ok := strings.Cut(f.At, ": "); ok {
			s.Text = "Test deleted: “" + title + "”"
		}
	case "expectation", "skip":
		s.Place = cmp.Or(f.At, path)
		if a, b, ok := strings.Cut(strings.TrimPrefix(f.What, "expectation changed: "), " → "); ok && flagKind(f.What) == "expectation" {
			s.Text, s.Before, s.After = "Expectation changed", strings.Trim(a, "`"), strings.Trim(b, "`")
		}
	}
	return s
}

// kindsBar is the files-by-kind bar and its rows (component 14), from Data.Kinds: the files in a part link to it,
// the rest show their diffs here.
type kindsBar struct {
	Total int
	Rows  []kindRow
}

// kindBar is the bar of What the review covered, nil under 4 files (7.5).
func (v *view) kindBar() *kindsBar {
	if len(v.Kinds) == 0 {
		return nil
	}
	in := map[string]bool{}
	for _, b := range v.Page.Parts {
		for _, f := range b.files() {
			in[f.Path] = true
		}
	}
	sha := short(v.SHA)
	kb := &kindsBar{}
	for _, k := range v.Kinds {
		n := len(k.Files)
		kb.Total += n
		r := kindRow{Kind: k.Kind, N: n, Add: k.Add, Del: k.Del}
		r.Name = map[string]string{"code": "Code and config", "tests": "Tests", "tooling": "Docs, CI and tool config", "gone": "Deleted or generated"}[k.Kind]
		switch k.Kind {
		case "code":
			r.Text = "The review read " + strconv.Itoa(k.Read) + "."
			if n == k.Read {
				r.Text = "The review read all " + strconv.Itoa(n) + "."
			} else if v.Page.Gap != nil {
				r.Text += " " + strconv.Itoa(n-k.Read) + map[bool]string{true: " wasn't read; it is", false: " weren't read; they are"}[n-k.Read == 1] + " the gap in the review."
			}
		case "tests":
			stay := n - k.Deleted
			r.Text = strconv.Itoa(stay) + " stay and " + strconv.Itoa(k.Deleted) + " are deleted. The review read " + strconv.Itoa(k.Read) + " of the " + strconv.Itoa(stay) + "."
			if len(v.Page.Tests.Cards) > 0 || k.Deleted > 0 {
				r.Text += " Tests lists the deleted and weakened ones."
			}
		case "tooling":
			r.Text = "The review read " + strconv.Itoa(k.Read) + "."
		case "gone":
			r.Chip = evSearch
			var s []string
			if k.Deleted > 0 {
				s = append(s, plural(k.Deleted, "deleted file", "deleted files")+" that no code at "+sha+" names")
			}
			if k.Generated > 0 {
				s = append(s, plural(k.Generated, "generated file", "generated files")+", found by a path rule")
			}
			r.Text = sentenceCase(join(s)) + ". The review skips them."
			if k.Named > 0 {
				r.Text += " " + strconv.Itoa(k.Named) + " of the " + strconv.Itoa(k.Deleted) + map[bool]string{true: " is", false: " are"}[k.Named == 1] + " still named in docs."
			}
		}
		if k.Cut {
			r.Text += " The search stopped early, so these counts may be short."
		}
		for _, p := range k.Files {
			if f := v.files[p]; f != nil && !in[p] {
				r.Files = append(r.Files, f)
			} else {
				r.Links = append(r.Links, p)
			}
		}
		kb.Rows = append(kb.Rows, r)
	}
	if kb.Total < 4 {
		kb.Total = 0 // no bar: the rows stay
	}
	return kb
}
