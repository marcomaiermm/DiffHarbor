package report

import (
	"cmp"
	"diffharbor/internal/diff"
	"diffharbor/internal/review"
	"html/template"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The v4 page's view (spec sections 2–5): what each section draws, computed from Data and the view prepare builds.
// The templates only lay it out.

// clock is the page's clock: the overline's review date. Tests fix it.
var clock = time.Now

// chip is an evidence chip (component 1). Kind: "shown" and "run" a machine's (Label its source), "fail" a failed
// run, "word" a person's quoted word, "ai" a second model's, "inf" one model's alone.
type chip struct{ Kind, Label string }

var (
	evDiff    = chip{"shown", "Diff"}
	evGit     = chip{"shown", "Git log"}
	evBase    = chip{"shown", "Base branch"}
	evRun     = chip{"shown", "Review run"}
	evTest    = chip{"run", "Test run"}
	evAI      = chip{"ai", "AI cross-check"}
	evNotSeen = chip{"inf", "Not verified"}
)

// page is the v4 page: each field one section or part of one.
type page struct {
	Over, Meta string
	Status     mstatus
	Brief      []bline
	Map        *mapView
	Before     []*card // Before you merge, in order
	Gap        *card   // the code the review didn't read
	Parts      []*pblock
	Others     *pblock // major findings in no part and no deploy box
	Minors     []*card // minor findings in files outside every part
	Open       int     // open items (5.3): Before, Read the change's, Test changes', the gap
	Tests      tests
	Tickets    tsec
	Covered    []crow
	OpenKinds  []cline // What the review covered, Open items: each kind a link to its first item
	KindBar    *kindsBar
	Kinds      []kindRow // without Data.Kinds: the files outside the parts
	Team       team
	Rail       []railRow
	Keys       string // the evidence key's shortcut line
	given      int    // the files the review was given: the diff's, without lockfiles and generated ones
}

// mstatus is the merge status panel (component 3): Kind "blocked", "waits", "open" or "none".
type mstatus struct {
	Kind, Head, Short string
	Lines             []mline
	Note              []string // the lines under the head of "{n} open items" and "No open items"
	Clears            string   // what ends the wait
	Gate, GateText    string   // the link to the gate
	Stats             []stat
}

// mline is a line of the merge status, led by its chip, with a link after it.
type mline struct {
	Chip           chip
	Text           string
	Href, HrefText string
}

// stat is a stat cell (component 4): "84 of 173", its label (a link when Href is set) and its second line.
type stat struct {
	N, Of, Label, Href, Second string
}

// bline is a sentence of the change in brief, its place as a link named from its part (component 27).
type bline struct {
	Text, Place, Href, Parts string
}

// card is an item card (component 6).
type card struct {
	ID, Kind, Title, Line string
	Chip                  chip
	St                    *schip // the status chip, before the evidence chip
	Ticket                string // the ticket chip
	Place, PlaceHref      string
	Step                  int
	Tags                  []tag
	Box                   string // the map box it sits on: a part's ID, "deploy:N", "" off the map
	Body                  string // a finding's text, the model's
	Blocks                []cblock
	Excerpt               *excerpt
	Cov                   string // the coverage note: the review didn't read the file
	GH, GHText, GHPlace   string // the GitHub link (component 9)
	Files                 []fileGroup
	Sev                   string
	Gate                  bool   // the gate variant: always open, Clr its last line
	Clr                   string // what ends the wait and how a run starts
	Stats                 string // a test group's stats line
	Tables                []ttable
	Samples               []sample
	Keys                  []string // its Version.Items as "kind\tplace", a group's kind alone: Since's New marks
	Paths                 []string // its files: Since's Changed marks
}

// cblock is a block of a card: its label row with one chip, an excerpt, then its lines.
type cblock struct {
	Label   string
	Chip    chip
	Excerpt *excerpt
	Lines   []cline
}

// cline is a line of a block: text with code spans, a link to the diff, and a note after it; a Quote is a quote
// block (component 8) with its Cite.
type cline struct {
	Lead, Text, Href, Code, Note string // Lead goes before Code: "Test:"
	Quote                        bool
	Cite                         string
}

// fileGroup is a gap card's files of one part.
type fileGroup struct {
	Label string
	Files []string
}

// excerpt is a code excerpt (component 7): rows of the file at the head, cited ones marked.
type excerpt struct {
	Path, At, Range, Href, Cited string // At: "at 722b251" or "on the base"
	Rows                         []xrow
	More                         []xrow // past 8 rows: behind "Show {n} more lines"
}

// xrow is a row of an excerpt; a Gap row stands for lines left out.
type xrow struct {
	No            int
	Text          string
	Cited, Remark bool
	Gap           int
}

// railRow is a row of the rail (component 22).
type railRow struct {
	Label, Href, Box string
	N                int
	Sub              bool
}

// crow is a row of What the review covered: a label and its lines.
type crow struct {
	Label string
	Lines []mline
}

// kindRow is a kind of file What the review covered lists: its diffs, and links to the files a part shows.
type kindRow struct {
	Kind, Name, Text string
	Chip             chip
	N, Add, Del      int
	Files            []*file
	Links            []string
}

// team is Who changed this code (5.11).
type team struct {
	People    string // the git log line; "" without history
	NoHistory bool
	Rows      []teamRow
	New       string
	Removed   string
	NoMember  string // the parts no current member changed or reviewed in 12 months (Data.Members read)
	Members   string // why the page can't say who of the team is current
}

type teamRow struct {
	ID, Label, Path, Files, Changed, Days string
}

// newLines is the diff's new lines by path:line, for links to a line in the PR's files view.
func (v *view) newLines() map[string]string {
	if v.lines == nil {
		v.lines = diff.NewLines(v.Diff)
	}
	return v.lines
}

// build is the v4 page from the view.
func (v *view) build() {
	p := &v.Page
	p.Over, p.Meta = v.over(), v.meta()
	v.mapView()
	p.Brief = v.brief()
	p.Tickets = v.ticketsView()
	v.cards()
	v.blocks()
	p.Open = len(p.Before) + p.ReadN() + len(p.Tests.Cards)
	p.Status = v.status()
	p.KindBar = v.kindBar()
	if p.KindBar == nil {
		p.Kinds = v.kinds()
	}
	p.Covered = v.covered()
	p.Team = v.team()
	p.Rail = v.rail()
}

func (v *view) over() string {
	s := cmp.Or(v.Forge.Ref, "#")
	o := "PR " + s + strconv.Itoa(v.Number)
	if !v.PR.Created.IsZero() {
		o += " · open since " + v.PR.Created.Format(time.DateOnly)
	}
	return o + " · reviewed " + clock().Format(time.DateOnly) + " at " + short(v.SHA)
}

func (v *view) meta() string {
	var p []string
	if v.PR.Author != "" {
		p = append(p, v.PR.Author)
	}
	if v.PR.Head != "" && v.PR.Base != "" {
		p = append(p, v.PR.Head+" → "+v.PR.Base)
	}
	if n := v.PR.Commits; n > 0 {
		c := plural(n, "commit", "commits")
		if v.PR.Merges {
			c += " without merges"
		}
		p = append(p, c)
	} else if n := len(v.Commits); n > 0 {
		p = append(p, plural(n, "commit", "commits"))
	}
	add, del := 0, 0
	for _, f := range v.files {
		add, del = add+f.Add, del+f.Del
	}
	p = append(p, plural(len(v.files), "file", "files"), "+"+thousands(add)+" −"+thousands(del))
	return strings.Join(p, " · ")
}

// thousands is n with commas: 12,756.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// brief is the change in brief: the sentences whose place is a line of the diff's files.
func (v *view) brief() []bline {
	var out []bline
	for _, s := range v.Brief {
		r := v.Ref(s.At)
		if r == nil || r.Out || v.files[r.Path] == nil {
			continue // a place the page can't show drops its sentence
		}
		name := path.Base(r.Path)
		if pt := v.partOf(r.Path); pt != nil && pt.Path != "." {
			name = pt.Label + "/" + strings.TrimPrefix(r.Path, pt.Path+"/")
		}
		out = append(out, bline{Text: strings.TrimSuffix(strings.TrimSpace(s.Text), "."), Place: name + ":" + strconv.Itoa(r.Line), Href: r.Href, Parts: v.boxOf(r.Path)})
	}
	return out
}

// status is the merge status (section 4), by its precedence: Blocked by a failed run or a conflict, Waits for a gate,
// the open items, none. The review run computes it; nothing on the page changes it.
func (v *view) status() mstatus {
	var st mstatus
	sha := short(v.SHA)
	var fails []string
	for _, n := range slices.Sorted(maps.Keys(v.Data.Tests.Ran)) {
		if v.Data.Tests.Ran[n] == "fail" {
			fails = append(fails, n)
		}
	}
	var ciFails []Check
	if v.CI != nil {
		ciFails = slices.DeleteFunc(slices.Clone(v.CI.Checks), func(c Check) bool { return !c.Failed() })
	}
	base := cmp.Or(v.PR.Base, "the base branch")
	if v.Conflicts {
		st.Lines = append(st.Lines, mline{Chip: evBase, Text: "The PR conflicts with " + base + "."})
	}
	for _, c := range ciFails {
		l := mline{Chip: chip{"fail", "Run failed"}, Text: c.Name + " failed at " + sha + "."}
		if c.URL != "" {
			l.Href, l.HrefText = c.URL, "Open the run on "+cmp.Or(v.Forge.Name, "GitHub")
		}
		st.Lines = append(st.Lines, l)
	}
	if len(fails) > 0 {
		names := fails
		if len(names) > 3 {
			names = append(names[:3:3], plural(len(fails)-3, "more", "more"))
		}
		st.Lines = append(st.Lines, mline{Chip: chip{"fail", "Run failed"}, Text: plural(len(fails), "test", "tests") + " failed at " + sha + ": " + join(names) + "."})
	}
	if b := v.Data.Tests.Broken; len(b) > 0 {
		st.Lines = append(st.Lines, mline{Chip: chip{"fail", "Run failed"}, Text: join(b) + map[bool]string{true: " doesn't", false: " don't"}[len(b) == 1] + " build at " + sha + "."})
	}
	n := v.Page.Open
	items := plural(n, "open item", "open items")
	waits := v.waiting()
	failed := func(pass string) bool {
		return slices.ContainsFunc(v.Terms.Errs, func(e string) bool { return strings.HasPrefix(e, pass+":") })
	}
	switch {
	case v.Conflicts:
		st.Kind, st.Head, st.Short = "blocked", "Blocked: a conflict with "+base, "Blocked · conflict with "+base
	case len(ciFails) == 1:
		st.Kind, st.Head, st.Short = "blocked", "Blocked: "+ciFails[0].Name+" failed at "+sha, "Blocked · "+ciFails[0].Name+" failed"
	case len(ciFails) > 1:
		st.Kind, st.Head, st.Short = "blocked", "Blocked: "+plural(len(ciFails), "CI check", "CI checks")+" failed at "+sha, "Blocked · "+plural(len(ciFails), "CI check", "CI checks")+" failed"
	case len(fails) > 0:
		st.Kind, st.Head, st.Short = "blocked", "Blocked: "+plural(len(fails), "test", "tests")+" failed at "+sha, "Blocked · "+plural(len(fails), "test", "tests")+" failed"
	case len(v.Data.Tests.Broken) > 0:
		st.Kind, st.Head, st.Short = "blocked", "Blocked: the build failed at "+sha, "Blocked · the build failed"
	case len(waits) > 0:
		g := waits[0]
		st.Kind, st.Head, st.Short = "waits", "Waits for "+g.Name, "Waits · "+items
		st.Lines = append(st.Lines, mline{Chip: wordChip(g.In), Text: "“" + g.Text + "”"}, v.gateTracker(g, false))
		st.Clears = v.clears(g)
	case n > 0:
		st.Kind, st.Head, st.Short, st.Note = "open", items, items, []string{"Nothing blocks the merge, and nothing says to wait."}
	default:
		st.Kind, st.Head, st.Short = "none", "No open items", "No open items"
		st.Note = []string{"No difference, nothing to finish before merging, no major finding, no test change and no gap in the review."}
		if failed("contract") { // no comparison ran: no "No difference"
			st.Note = []string{"Nothing to finish before merging, no major finding, no test change and no gap in the review."}
		} else if len(v.Tickets) == 0 {
			st.Note = append(st.Note, "No ticket was read, so nothing compared the code with what the tickets ask.")
		}
	}
	if len(waits) > 0 {
		st.Gate, st.GateText = "#"+slug(waits[0].Name)+"-gate", "Go to "+gateLink(waits[0].Name)
		if st.Kind == "blocked" {
			st.Lines = append(st.Lines, mline{Text: "Also waits for " + waits[0].Name + "."})
		}
	}
	if failed("gates") {
		st.Lines = append(st.Lines, mline{Chip: evRun, Text: "The gate pass failed, so nothing checked whether the description or a ticket asks to wait."})
	}
	if failed("contract") {
		st.Lines = append(st.Lines, mline{Chip: evRun, Text: "The contract pass failed, so nothing compared the code with what the tickets ask."})
	}
	// the open items
	open := stat{N: strconv.Itoa(n), Label: map[bool]string{true: "open item", false: "open items"}[n == 1]}
	switch {
	case n == 0:
		open.Second = "Nothing found that's open"
	case v.Page.Map != nil:
		open.Second = strconv.Itoa(v.Page.Map.OnMap) + " of them on the map"
	default:
		var k []string
		for _, c := range v.openKinds() {
			k = append(k, c.Text)
		}
		open.Second = strings.Join(k, ", ")
	}
	st.Stats = append(st.Stats, open)
	// the tests run here, else the CI they're left to
	ran := stat{N: strconv.Itoa(len(v.Data.Tests.Ran)), Label: map[bool]string{true: "test run here", false: "tests run here"}[len(v.Data.Tests.Ran) == 1]}
	switch c := v.CI; {
	case len(v.Data.Tests.Ran) > 0:
		ran.Second = v.ranText() + " at " + sha
	case c != nil && c.Tests != "":
		ran.Second = "Left to CI (" + c.Tests + ")"
		if c.Edited {
			ran.Second += ", which this PR edits"
		}
	case c != nil && len(c.Workflows) == 0:
		ran.Second = "No CI workflow on the base branch."
	default:
		ran.Second = "No test result reached this page"
	}
	st.Stats = append(st.Stats, ran)
	// the files the review didn't read
	unread, code := v.unread()
	total := 0
	for p := range v.files {
		if !diff.Skip.MatchString(p) {
			total++
		}
	}
	v.Page.given = total
	nr := stat{N: strconv.Itoa(len(unread)), Of: strconv.Itoa(total), Label: "files not read by the review", Href: "#covered"}
	if len(unread) == 0 {
		nr.Of, nr.Second = "", "Every file fit the review"
	} else {
		nr.Second = strconv.Itoa(code) + " of them code"
	}
	st.Stats = append(st.Stats, nr)
	// the tickets
	if t := v.Page.Tickets; t.Shown {
		ts := stat{N: strconv.Itoa(len(t.Cards)), Label: map[bool]string{true: "ticket", false: "tickets"}[len(t.Cards) == 1], Href: "#tickets"}
		if len(v.Tickets) == 0 {
			ts.Label += " named in commits"
			ts.Second = "None was read for this review"
		} else {
			tk := v.Tickets[0] // open tickets come first
			ts.Second = strings.TrimSpace(tk.Key + " " + tk.State)
			n := len(v.Terms.Differences) // the PR's, not the first ticket's (5.3)
			ts.Second += " · " + map[bool]string{true: "no difference", false: plural(n, "difference", "differences")}[n == 0]
		}
		st.Stats = append(st.Stats, ts)
	}
	return st
}

// openKinds is the open items by kind, each with a link to its first item: differences, before merging, major
// findings, test changes, the gap.
func (v *view) openKinds() []cline {
	p := &v.Page
	var all []*card
	all = append(all, p.Before...)
	for _, b := range p.Parts {
		all = append(all, b.Cards...)
	}
	if p.Others != nil {
		all = append(all, p.Others.Cards...)
	}
	all = append(all, p.Tests.Cards...)
	var out []cline
	for _, k := range []struct{ kind, one, many string }{
		{"Difference", "difference", "differences"},
		{"Before merging", "before merging", "before merging"},
		{"Major finding", "major finding", "major findings"},
		{"Test change", "test change", "test changes"},
	} {
		var first *card
		n := 0
		for _, c := range all {
			if c.Kind == k.kind {
				n++
				first = cmp.Or(first, c)
			}
		}
		if n > 0 {
			out = append(out, cline{Text: plural(n, k.one, k.many), Href: "#" + first.ID})
		}
	}
	if p.Gap != nil {
		out = append(out, cline{Text: "1 gap in the review", Href: "#gap"})
	}
	return out
}

// ranText is how the tests run here ended: "3 passed, 1 failed".
func (v *view) ranText() string {
	c := map[string]int{}
	for _, r := range v.Data.Tests.Ran {
		c[r]++
	}
	var k []string
	for _, s := range []struct{ k, say string }{{"pass", "passed"}, {"fail", "failed"}, {"skip", "skipped"}} {
		if c[s.k] > 0 {
			k = append(k, strconv.Itoa(c[s.k])+" "+s.say)
		}
	}
	return strings.Join(k, ", ")
}

// runBacked is a finding a test run showed: a proof on the base or a reproducing test.
func runBacked(c review.Comment) bool {
	return strings.HasPrefix(c.Evidence, review.Ran) || strings.HasPrefix(c.Evidence, review.Repro)
}

// unread is the files of the diff the review left out or failed on, and how many of them are code.
func (v *view) unread() (paths []string, code int) {
	for _, p := range slices.Concat(v.Omitted, v.Failed) {
		if v.files[p] != nil && !slices.Contains(paths, p) {
			paths = append(paths, p)
			if v.files[p].Kind == "code" {
				code++
			}
		}
	}
	return paths, code
}

// majors is the major findings: the ones that count as open items.
func (v *view) majors() int {
	n := 0
	for _, c := range v.Findings {
		if review.Blocks(c.Severity) {
			n++
		}
	}
	return n
}

// codeSpan is a `code` span of the copy.
var codeSpan = regexp.MustCompile("`([^`\n]+)`")

// md is text with its `code` spans as code tokens, the rest escaped; a slash in a code span may break the line.
func md(s string) template.HTML {
	var b strings.Builder
	last := 0
	for _, m := range codeSpan.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(template.HTMLEscapeString(s[last:m[0]]))
		b.WriteString(`<code class="cd" translate="no">` + wbr(s[m[2]:m[3]]) + `</code>`)
		last = m[1]
	}
	b.WriteString(template.HTMLEscapeString(s[last:]))
	return template.HTML(b.String()) //nolint:gosec // escaped above
}

// wbr is s escaped, with a break opportunity after each slash and before each dot: names wrap without U+200B, so a
// copied name still matches a code search.
func wbr(s string) string {
	s = template.HTMLEscapeString(s)
	s = strings.ReplaceAll(s, "/", "/<wbr>")
	return strings.ReplaceAll(s, ".", "<wbr>.")
}

// findingCard is a finding as an item card: its body, the excerpt at its line, a coverage note, the GitHub link.
func (v *view) findingCard(i int, c review.Comment) *card {
	k := &card{ID: "finding-" + strconv.Itoa(i+1), Title: strings.TrimSpace(c.Title), Sev: c.Severity, Body: strings.TrimSpace(c.Body)}
	k.Kind = "Major finding"
	if !review.Blocks(c.Severity) {
		k.Kind = "Minor finding"
	}
	if k.Title == "" {
		k.Title = lead(k.Body)
	} else {
		k.Line = lead(k.Body)
	}
	switch {
	case runBacked(c):
		k.Chip = evTest
	case c.Evidence != "":
		k.Chip = evAI
	default:
		k.Chip = evNotSeen
	}
	k.Place, k.PlaceHref = v.placeOf(c.Path, c.Line)
	k.Box = v.boxOf(c.Path)
	for i, s := range v.Steps[:v.NSteps] {
		if slices.ContainsFunc(s.Files, func(f *file) bool { return f.Path == c.Path }) {
			k.Step = i + 1
		}
	}
	if c.Earlier {
		k.Tags = append(k.Tags, tag{"history", "From an earlier review, still open"})
	}
	k.Paths = []string{c.Path}
	if review.Blocks(c.Severity) {
		k.Keys = []string{"finding\t" + c.Path + ": " + c.Title}
	}
	k.Excerpt = v.excerpt(c.Path, c.Line, c.Line, c.Line)
	if slices.Contains(v.Omitted, c.Path) || slices.Contains(v.Failed, c.Path) {
		k.Cov = "The review didn't read this file, so no finding of this run covers it."
	}
	if id := v.Threads[ThreadKey(c)]; id != 0 && v.Forge.ThreadURL(id) != "" {
		k.GH, k.GHText = v.Forge.ThreadURL(id), "Open its thread on "+cmp.Or(v.Forge.Name, "GitHub")
	} else {
		k.GH, k.GHText = v.ghLink(c.Path, c.Line)
	}
	k.GHPlace = c.Path
	if c.Line > 0 {
		k.GHPlace += ":" + strconv.Itoa(c.Line)
	}
	return k
}

// ghLink is the link to a line, or a file without one, on the forge, and its text.
func (v *view) ghLink(p string, line int) (href, text string) {
	name := cmp.Or(v.Forge.Name, "GitHub")
	if line > 0 {
		return v.Forge.Line(p, line, v.newLines()), "Open this line on " + name
	}
	return v.Forge.File(p, v.files[p] != nil), "Open this file on " + name
}

// placeOf is a place as a card shows it, below its part's folder, and its link into the page's diff.
func (v *view) placeOf(p string, line int) (string, string) {
	name := p
	if pt := v.partOf(p); pt != nil && pt.Path != "." {
		name = strings.TrimPrefix(p, pt.Path+"/")
	}
	href := ""
	if v.files[p] != nil {
		href = "#" + p
	}
	if line > 0 {
		name += ":" + strconv.Itoa(line)
		if href != "" {
			href = lineHref(p, line)
		}
	}
	return name, href
}

// maxExcerpt is the rows an excerpt shows before "Show {n} more lines".
const maxExcerpt = 8

var remark = regexp.MustCompile(`^\s*(//|#|/\*|\*|--|<!--)`)

// excerpt is lines from..to of p at the head with three around them, cited cited..to; nil when the page has no text
// of the file.
func (v *view) excerpt(p string, from, to, cited int) *excerpt {
	if from <= 0 {
		return nil
	}
	var lines []string
	if src := v.Sources[p]; src != "" {
		lines = strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	} else if f := v.files[p]; f != nil && f.New {
		for _, l := range f.Lines {
			if l.Class == "add" {
				lines = append(lines, l.Text)
			}
		}
	}
	return v.cut(p, lines, 1, from, to, cited)
}

// cut is the excerpt of lines, the file's from line 1, around from..to, starting at floor or later.
func (v *view) cut(p string, lines []string, floor, from, to, cited int) *excerpt {
	if from > len(lines) {
		return nil
	}
	a, b := max(floor, from-2), min(len(lines), to+3)
	x := &excerpt{Path: p, At: "at " + short(v.SHA), Range: strconv.Itoa(a) + "–" + strconv.Itoa(b), Cited: strconv.Itoa(cited)}
	if a == b {
		x.Range = strconv.Itoa(a)
	}
	if to > cited {
		x.Cited += "–" + strconv.Itoa(to)
	}
	if v.files[p] != nil {
		x.Href = lineHref(p, cited)
	}
	for n := a; n <= b; n++ {
		r := xrow{No: n, Text: strings.ReplaceAll(lines[n-1], "\t", "  "), Cited: n >= cited && n <= to}
		r.Remark = remark.MatchString(r.Text)
		if len(x.Rows) < maxExcerpt {
			x.Rows = append(x.Rows, r)
		} else {
			x.More = append(x.More, r)
		}
	}
	return x
}

// cards is Before you merge, the gap card and the findings, each where its box says it is read (6.5).
//
//nolint:gocognit // each finding and risk to its card, step by step
func (v *view) cards() {
	p := &v.Page
	mv := p.Map
	for i, c := range v.Findings {
		k := v.findingCard(i, c)
		v.found = append(v.found, k)
	}
	// the gates first, then the differences: those on a deploy box are read in Before you merge, the rest in their part
	for _, g := range v.waiting() {
		p.Before = append(p.Before, v.gateCard(g))
	}
	for i, d := range v.Terms.Differences {
		k := v.diffCard(i, d)
		if strings.HasPrefix(k.Box, "deploy:") {
			p.Before = append(p.Before, k)
		} else {
			v.lead = append(v.lead, k)
		}
	}
	// a card per deploy box: its risks, then the major findings on it
	for _, r := range v.Risks {
		kind := map[string]string{"Environment": "environment", "Migrations": "migration", "Dependencies": "dependencies", "Build, deploy and CI": "ci", "Configuration": "configuration"}[r.Title]
		if kind == "migration" {
			for _, it := range r.Items {
				p.Before = append(p.Before, v.riskCard(r, []riskItem{it}, "deploy:"+it.What, migrationLabel(it.What)+" changes the database"))
			}
			continue
		}
		title := map[string]string{
			"environment":   "Environment variables the code reads",
			"dependencies":  "Dependencies change",
			"ci":            "This PR edits how it builds, deploys and runs CI",
			"configuration": "Settings files change",
		}[kind]
		if kind == "environment" {
			var news, gone []string
			for _, it := range r.Items {
				if strings.HasPrefix(it.Note, "newly read") {
					news = append(news, "`"+it.What+"`")
				} else {
					gone = append(gone, "`"+it.What+"`")
				}
			}
			switch {
			case len(news) > 0 && len(gone) == 0:
				title = map[bool]string{true: "A new variable: ", false: "New variables: "}[len(news) == 1] + join(news)
			case len(news) == 0:
				title = map[bool]string{true: "A variable no longer read: ", false: "Variables no longer read: "}[len(gone) == 1] + join(gone)
			}
		}
		k := v.riskCard(r, r.Items, "deploy:"+kind, title)
		if r.More > 0 {
			k.Blocks[0].Lines = append(k.Blocks[0].Lines, cline{Text: plural(r.More, "more", "more") + " on the PR's page."})
		}
		p.Before = append(p.Before, k)
	}
	if mv != nil {
		for _, d := range mv.src.Deploy { // a migration that drops tables without a risk of its own
			if d.Kind == "migration" && len(d.Drops) > 0 && !slices.ContainsFunc(p.Before, func(k *card) bool { return k.Box == "deploy:"+d.Path }) {
				it := riskItem{What: d.Path, ID: d.Path}
				p.Before = append(p.Before, v.riskCard(risk{Title: "Migrations"}, []riskItem{it}, "deploy:"+d.Path, d.Label+" changes the database"))
			}
		}
	}
	for _, o := range v.Others {
		if len(o.Clash) == 0 {
			continue
		}
		k := &card{ID: "pr-" + strconv.Itoa(o.Number), Kind: "Before merging", Box: "deploy:prs", Chip: chip{"shown", "Open PRs"}}
		k.Title = cmp.Or(v.Forge.Ref, "#") + strconv.Itoa(o.Number) + " changes the same lines"
		k.Line = strconv.Itoa(len(o.Clash)) + " of the files both change: whichever merges second meets a conflict."
		lines := []cline{{Text: o.Title + " by " + cmp.Or(o.Author, "someone")}}
		for _, f := range o.Clash {
			lines = append(lines, cline{Code: f, Href: v.fileHref(f)})
		}
		k.Blocks = []cblock{{Label: "The other PR", Chip: chip{"shown", "Open PRs"}, Lines: lines}}
		if o.URL != "" {
			k.GH, k.GHText, k.GHPlace = o.URL, "Open "+cmp.Or(v.Forge.Ref, "#")+strconv.Itoa(o.Number)+" on "+cmp.Or(v.Forge.Name, "GitHub"), o.URL
		}
		p.Before = append(p.Before, k)
	}
	if len(v.BaseMoved) > 0 {
		k := &card{ID: "base-moved", Kind: "Before merging", Chip: evGit, Title: cmp.Or(v.PR.Base, "The base branch") + " changed files of this PR since it forked"}
		var lines []cline
		files := map[string]bool{}
		for _, c := range v.BaseMoved {
			for _, f := range c.Paths {
				files[f] = true
			}
			lines = append(lines, cline{Code: short(c.SHA), Text: c.Subject})
		}
		k.Line = plural(len(v.BaseMoved), "commit", "commits") + " on " + cmp.Or(v.PR.Base, "the base") + " change " + plural(len(files), "file", "files") + " this PR changes."
		k.Blocks = []cblock{{Label: "The base branch", Chip: evGit, Lines: lines}}
		p.Before = append(p.Before, k)
	}
	v.beforeExtras()
	// the major findings on a deploy box are read there; the rest in their part, else with the others
	for _, k := range v.found {
		if review.Blocks(k.Sev) && strings.HasPrefix(k.Box, "deploy:") {
			k.Kind = "Major finding"
			p.Before = append(p.Before, k)
		}
	}
	for _, k := range p.Before {
		if k.Kind == "" {
			k.Kind = "Before merging"
		}
	}
	// the gap in the review: the code files it didn't read
	unread, code := v.unread()
	if code > 0 {
		var fit, failed []string
		for _, f := range unread {
			if v.files[f].Kind != "code" {
				continue
			}
			if slices.Contains(v.Failed, f) {
				failed = append(failed, f)
			} else {
				fit = append(fit, f)
			}
		}
		g := &card{ID: "gap", Kind: "Gap in the review", Chip: evRun, Place: plural(code, "file", "files")}
		g.Title = "The review didn't read " + plural(code, "code file", "code files")
		switch {
		case len(failed) > 0 && len(fit) > 0:
			g.Line = "A review part failed on " + strconv.Itoa(len(failed)) + ", and " + strconv.Itoa(len(fit)) + " didn't fit the review. No finding of this run covers them."
		case len(failed) > 0:
			g.Line = "A review part failed on them. No finding of this run covers them."
		default:
			g.Line = "They didn't fit the review. No finding of this run covers them."
		}
		if len(failed) > 0 {
			g.Files = append(g.Files, fileGroup{Label: "A review part failed: " + plural(len(failed), "code file", "code files"), Files: failed})
		}
		if len(fit) > 0 {
			g.Files = append(g.Files, fileGroup{Label: "Didn't fit: " + plural(len(fit), "code file", "code files"), Files: fit})
		}
		g.Keys = []string{"gap"}
		g.Paths = unread
		p.Gap = g
	}
	v.lead = append(v.lead, v.testCards()...)
}

// fileHref is a path's link into the page's diff, "" when the page doesn't show it.
func (v *view) fileHref(p string) string {
	if v.files[p] != nil {
		return "#" + p
	}
	return ""
}

// riskCard is a before-merge card of a deploy box from risks()' items: the lines it changes, a coverage note when
// the review didn't read them, the link to the one file.
func (v *view) riskCard(r risk, items []riskItem, box, title string) *card {
	k := &card{ID: "before-" + slug(box), Kind: "Before merging", Title: title, Chip: evDiff, Box: box, Line: r.Hint}
	var lines []cline
	paths := map[string]bool{}
	for _, it := range items {
		at := it.ID
		if i := strings.Index(at, "~"); i > 0 {
			at = at[:i]
		}
		if !paths[at] {
			k.Paths = append(k.Paths, at)
		}
		paths[at] = true
		k.Keys = append(k.Keys, "before\t"+it.What)
		l := cline{Code: it.What, Href: "#" + it.ID, Note: it.Note}
		if at != it.What {
			l.Note = strings.TrimSpace(it.Note + " · in " + path.Base(at))
		}
		lines = append(lines, l)
	}
	if d := v.deployOf(box); d != nil && len(d.Drops) > 0 {
		lines = append(lines, cline{Text: "Drops " + join(codeAll(d.Drops)) + "."})
		k.Title = d.Label + " drops " + plural(len(d.Drops), "table", "tables")
		k.Line = "They run against live data: order them with the deploy and know the way back."
	}
	k.Blocks = []cblock{{Label: "The code does", Chip: evDiff, Lines: lines}}
	if len(paths) == 1 {
		for p := range paths {
			k.Place, k.PlaceHref = p, v.fileHref(p)
			k.GH, k.GHText = v.ghLink(p, 0)
			k.GHPlace = p
			if slices.Contains(v.Omitted, p) || slices.Contains(v.Failed, p) {
				k.Cov = "The review didn't read this file, so no finding of this run covers it."
			}
		}
	}
	return k
}

func codeAll(s []string) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = "`" + x + "`"
	}
	return out
}

var slugBad = regexp.MustCompile(`[^a-z0-9]+`)

// slug is s as an id: lowercase letters and digits joined by "-".
func slug(s string) string {
	return strings.Trim(slugBad.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// covered is What the review covered: the open items, the rules, the tests, the findings, the coverage (5.10).
//
//nolint:gocognit,gocyclo // a row per source, each with its states
func (v *view) covered() []crow {
	p := &v.Page
	sha := short(v.SHA)
	var rows []crow
	p.OpenKinds = v.openKinds()
	if len(p.OpenKinds) == 0 {
		rows = append(rows, crow{"Open items", []mline{{Text: "None."}}})
	} else {
		rows = append(rows, crow{Label: "Open items"}) // the template draws p.OpenKinds as links
	}
	// the rules the review followed, as on the base
	base := cmp.Or(v.PR.Base, "the base branch")
	switch r := v.Rules; {
	case len(r.Files) == 0:
		rows = append(rows, crow{"Rules", []mline{{Chip: evBase, Text: "No rules file on the base branch."}}})
	default:
		var whole, cut []string
		for _, f := range r.Files {
			if f.Cut {
				cut = append(cut, "`"+f.Path+"`")
			} else {
				whole = append(whole, "`"+f.Path+"`")
			}
		}
		at := ", as on " + base + " at " + short(r.SHA)
		var ls []mline
		if len(whole) > 0 {
			ls = append(ls, mline{Chip: evBase, Text: join(whole) + at + ", read whole. This PR can't change the rules it is reviewed by."})
		}
		if len(cut) > 0 {
			ls = append(ls, mline{Chip: evBase, Text: join(cut) + at + ", cut short to fit the review. This PR can't change the rules it is reviewed by."})
		}
		rows = append(rows, crow{"Rules", ls})
	}
	// the tests and linters: the runs here, then CI's
	t := v.Data.Tests
	var ls []mline
	if len(t.Ran) > 0 {
		c := chip{"run", "Run passed"}
		if slices.Contains(slices.Collect(maps.Values(t.Ran)), "fail") {
			c = chip{"fail", "Run failed"}
		}
		ls = append(ls, mline{Chip: c, Text: plural(len(t.Ran), "test", "tests") + " ran at " + sha + ": " + v.ranText() + "."})
	}
	if len(t.Broken) > 0 {
		ls = append(ls, mline{Chip: chip{"fail", "Run failed"}, Text: join(t.Broken) + map[bool]string{true: " doesn't", false: " don't"}[len(t.Broken) == 1] + " build at " + sha + "."})
	}
	ci := v.CI
	if len(ls) == 0 {
		s := "None ran here."
		switch {
		case ci != nil && ci.Tests != "" && len(ci.Checks) == 0:
			s += " Left to CI (" + ci.Tests + "); its result isn't on this page."
		case ci != nil && len(ci.Workflows) == 0:
			s += " No CI workflow on the base branch."
		case ci == nil:
			s += " No test result reached this page."
		}
		ls = append(ls, mline{Text: s})
	}
	if ci != nil {
		var passed, pending int
		for _, c := range ci.Checks {
			switch {
			case c.Failed():
				l := mline{Chip: chip{"fail", "Run failed"}, Text: "`" + c.Name + "` failed at " + sha + "."}
				if c.URL != "" {
					l.Href, l.HrefText = c.URL, "Open the run on "+cmp.Or(v.Forge.Name, "GitHub")
				}
				ls = append(ls, l)
			case c.Status == "pending":
				pending++
			case c.Conclusion == "success":
				passed++
			}
		}
		if passed > 0 {
			ls = append(ls, mline{Chip: chip{"run", "Run passed"}, Text: plural(passed, "CI check", "CI checks") + " passed at " + sha + "."})
		}
		if pending > 0 {
			ls = append(ls, mline{Chip: evRun, Text: plural(pending, "CI check was", "CI checks were") + " still running when the review read them."})
		}
		if ci.Err != "" {
			ls = append(ls, mline{Chip: evRun, Text: "The forge's checks weren't read for this review, so no CI result is on this page."})
		}
	}
	rows = append(rows, crow{"Tests and linters", ls})
	if pr := v.Story.Proof; pr != "" {
		res := map[string]string{"pass": "passed", "fail": "failed", "skip": "was skipped"}[ranOf(t.Ran, pr)]
		switch res {
		case "":
			rows = append(rows, crow{"Proof on the base", []mline{{Text: "Not run: `" + pr + "`, the test the review picked as proof."}}})
		default:
			c := chip{"run", "Run passed"}
			if res == "failed" {
				c = chip{"fail", "Run failed"}
			}
			rows = append(rows, crow{"Proof on the base", []mline{{Chip: c, Text: "`" + pr + "` " + res + " at " + sha + "."}}})
		}
	} else {
		rows = append(rows, crow{"Proof on the base", []mline{{Text: "None."}}})
	}
	if n := len(v.Findings); n > 0 {
		m, ran, checked, earlier := v.majors(), 0, 0, 0
		for _, c := range v.Findings {
			switch {
			case runBacked(c):
				ran++
			case c.Evidence != "":
				checked++
			}
			if c.Earlier {
				earlier++
			}
		}
		var s string
		switch m {
		case 0:
			s = plural(n, "minor finding", "minor findings") + ", none major."
		case n:
			s = plural(n, "major finding", "major findings") + ", none minor."
		default:
			s = plural(n, "finding", "findings") + ": " + strconv.Itoa(m) + " major and " + strconv.Itoa(n-m) + " minor."
		}
		// a finding a test run showed is no second model's word: it's counted apart
		rest, them, each := n-ran, "them", map[bool]string{true: "it", false: "each"}[n == 1]
		if ran > 0 {
			them = "the other " + map[bool]string{true: "one", false: strconv.Itoa(rest)}[rest == 1]
		}
		c := evAI
		switch {
		case rest == 0:
			c, s = evTest, s+" A test run showed "+each+"."
		case checked == rest:
			s += " A second model agreed with " + map[bool]string{true: each, false: them}[ran == 0] + ", and neither ran the code."
		case checked == 0:
			c, s = evNotSeen, s+" No second model checked "+them+"."
		default:
			s += " A second model agreed with " + strconv.Itoa(checked) + " of " + them + ", and neither ran the code."
		}
		if earlier > 0 {
			s += " " + strconv.Itoa(earlier) + map[bool]string{true: " comes", false: " come"}[earlier == 1] + " from an earlier review."
		}
		ls := []mline{{Chip: c, Text: s}}
		if ran > 0 && rest > 0 {
			ls = append(ls, mline{Chip: evTest, Text: strconv.Itoa(ran) + " shown by a test run."})
		}
		if d := len(v.Dropped); d > 0 {
			ls = append(ls, mline{Chip: evAI, Text: plural(d, "finding was", "findings were") + " dropped by the cross-check."})
		}
		rows = append(rows, crow{"Findings", ls})
	} else {
		rows = append(rows, crow{"Findings", []mline{{Chip: evRun, Text: "The review found nothing to flag."}}})
	}
	unread, _ := v.unread()
	cov := "The review read all " + plural(v.Page.given, "file", "files") + " it was given"
	if len(unread) > 0 {
		var p []string
		var fit, failed int
		for _, f := range unread {
			if slices.Contains(v.Failed, f) {
				failed++
			} else {
				fit++
			}
		}
		if fit > 0 {
			p = append(p, strconv.Itoa(fit)+" of "+strconv.Itoa(v.Page.given)+" files didn't fit the review")
		}
		if failed > 0 {
			p = append(p, plural(failed, "file was", "files were")+" in a review part that failed")
		}
		cov = join(p)
	}
	if v.Parts > 1 {
		cov += ", which ran in " + strconv.Itoa(v.Parts) + " parts"
	}
	cov += "."
	if skip := len(v.files) - v.Page.given; skip > 0 {
		cov += " " + strconv.Itoa(skip) + " more were skipped by rule."
	}
	rows = append(rows, crow{"Coverage", []mline{{Chip: evRun, Text: cov}}})
	return rows
}

func countKind(ks []*card, kind string) int {
	n := 0
	for _, k := range ks {
		if k.Kind == kind {
			n++
		}
	}
	return n
}

// kinds is the files outside the parts, by kind, for What the review covered: tests, then docs, data and config.
func (v *view) kinds() []kindRow {
	in := map[string]bool{}
	for _, b := range v.Page.Parts {
		for _, f := range b.files() {
			in[f.Path] = true
		}
	}
	rows := []kindRow{{Name: "Tests"}, {Name: "Docs, data and config"}, {Name: "Other code"}}
	for _, p := range slices.Sorted(maps.Keys(v.files)) {
		f := v.files[p]
		if in[p] {
			continue
		}
		i := 1
		switch f.Kind {
		case "test":
			i = 0
		case "code":
			i = 2
		}
		rows[i].Files = append(rows[i].Files, f)
		rows[i].Add, rows[i].Del = rows[i].Add+f.Add, rows[i].Del+f.Del
	}
	return slices.DeleteFunc(rows, func(r kindRow) bool { return len(r.Files) == 0 })
}

// team is Who changed this code (5.11): the people git names, team-level only, and each part's last change.
func (v *view) team() team {
	var t team
	if len(v.Knowledge) == 0 {
		t.NoHistory = true
		return t
	}
	var all []string
	for p := range v.files {
		all = append(all, p)
	}
	g := Aggregate(v.Knowledge, all)
	when := "the PR opened"
	if !v.PR.Created.IsZero() {
		when = v.PR.Created.Format(time.DateOnly)
	}
	switch n := len(g.People); n {
	case 0:
		t.People = "Nobody committed to these files in the 12 months before " + when + "."
	case 1:
		t.People = "1 person committed to these files in the 12 months before " + when + ": " + g.People[0] + ". Git can't tell a person from an agent committing under their name."
	default:
		t.People = strconv.Itoa(n) + " people committed to these files in the 12 months before " + when + ": " + join(g.People) + "."
	}
	type kr struct {
		r        teamRow
		med, old int
		known    bool
	}
	var rs []kr
	var news, gone, quiet []string
	for _, b := range v.Page.Parts {
		if b.Removed || b.Generated || b.Others {
			continue
		}
		var paths []string
		for _, f := range b.Kept {
			paths = append(paths, f.Path)
		}
		ag := Aggregate(v.Knowledge, paths)
		r := kr{r: teamRow{ID: b.ID, Label: b.Label, Path: b.Path, Files: b.FilesText(), Changed: "+" + thousands(b.Add) + " −" + thousands(b.Del)}, med: ag.Median, old: ag.Oldest, known: ag.Known > 0}
		// git names can't be matched to the forge's logins: only a part nobody changed in 12 months is known to have
		// no current member who did
		if m := v.Members; m != nil && m.Err == "" && !b.New && ag.Known > 0 && len(ag.People) == 0 {
			b.NoMember = true
			quiet = append(quiet, b.Label)
		}
		if ag.Known > 0 {
			r.r.Days = strconv.Itoa(ag.Median)
			if ag.Oldest != ag.Median && ag.Known > 1 {
				r.r.Days += " · " + strconv.Itoa(ag.Oldest)
			}
		}
		if b.New {
			news = append(news, b.Label+" ("+plural(len(b.Kept), "file", "files")+", +"+thousands(b.Add)+")")
			continue
		}
		rs = append(rs, r)
	}
	slices.SortStableFunc(rs, func(a, b kr) int {
		return cmp.Or(cmp.Compare(map[bool]int{true: 0, false: 1}[a.known], map[bool]int{true: 0, false: 1}[b.known]), cmp.Compare(b.med, a.med), cmp.Compare(b.old, a.old))
	})
	for _, r := range rs {
		t.Rows = append(t.Rows, r.r)
	}
	if len(news) > 0 {
		t.New = "New in this PR: " + join(news) + "."
	}
	if len(quiet) > 0 {
		t.NoMember = "No current member changed or reviewed " + join(quiet) + " in 12 months."
	}
	if m := v.Members; m != nil && m.Err != "" {
		t.Members = "The forge's member list wasn't read, so the page can't say whether a current member changed this code."
	}
	if m := v.Page.Map; m != nil && m.src.Removed != nil {
		for _, f := range m.src.Removed.Folders {
			gone = append(gone, "`"+f.Path+"` ("+plural(f.N, "file", "files")+")")
		}
		t.Removed = "Removed, every changed file deleted: " + join(gone) + "."
	}
	return t
}

// rail is the rail's rows (component 22): the sections, and a row per part with open items.
func (v *view) rail() []railRow {
	p := &v.Page
	rs := []railRow{{Label: "The change", Href: "#top"}, {Label: "Before you merge", Href: "#before", N: len(p.Before)}}
	rs = append(rs, railRow{Label: "Read the change", Href: "#read", N: p.ReadN()})
	for _, b := range p.Parts {
		if b.N > 0 {
			rs = append(rs, railRow{Label: b.Label, Href: "#" + b.Anchor(), N: b.N, Sub: true, Box: b.Key})
		}
	}
	rs = append(rs, railRow{Label: "Test changes", Href: "#tests", N: len(p.Tests.Cards)})
	if p.Tickets.Shown {
		rs = append(rs, railRow{Label: "The tickets", Href: "#tickets", N: len(p.Tickets.Cards)})
	}
	return append(rs, railRow{Label: "What the review covered", Href: "#covered"}, railRow{Label: "Who changed this code", Href: "#team"})
}
