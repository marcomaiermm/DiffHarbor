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
	Since      *sinceLine
	Status     mstatus
	Brief      []bline
	Map        *mapView
	Before     []*card // Before you merge, in order
	Gap        *card   // the code the review didn't read
	Parts      []*pblock
	Others     *pblock // major findings in no part and no deploy box
	Minors     []*card // minor findings in files outside every part
	Open       int     // open items: Before, the major findings, the gap
	Covered    []crow
	Kinds      []kindRow
	Team       team
	Rail       []railRow
	Keys       string // the evidence key's shortcut line
	given      int    // the files the review was given: the diff's, without lockfiles and generated ones
}

// sinceLine is the header's last line (component 28): what changed since the previous review.
type sinceLine struct {
	SHA, Text string
}

// mstatus is the merge status panel (component 3): Kind "blocked", "open" or "none".
type mstatus struct {
	Kind, Head, Short string
	Lines             []mline
	Note              string // the line under the head of "{n} open items" and "No open items"
	Stats             []stat
}

// mline is a line of the merge status, led by its chip.
type mline struct {
	Chip chip
	Text string
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
	Place, PlaceHref      string
	Step                  int
	Tags                  []string
	Box                   string // the map box it sits on: a part's ID, "deploy:N", "" off the map
	Body                  string // a finding's text, the model's
	Blocks                []cblock
	Excerpt               *excerpt
	Cov                   string // the coverage note: the review didn't read the file
	GH, GHText, GHPlace   string // the GitHub link (component 9)
	Files                 []fileGroup
	Sev                   string
}

// cblock is a block of a card: its label row with one chip, then its lines.
type cblock struct {
	Label string
	Chip  chip
	Lines []cline
}

// cline is a line of a block: text with code spans, a link to the diff, and a note after it.
type cline struct {
	Text, Href, Code, Note string
}

// fileGroup is a gap card's files of one part.
type fileGroup struct {
	Label string
	Files []string
}

// excerpt is a code excerpt (component 7): rows of the file at the head, cited ones marked.
type excerpt struct {
	Path, Range, Href, Cited string
	Rows                     []xrow
	More                     []xrow // past 8 rows: behind "Show {n} more lines"
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

// kindRow is a kind of file What the review covered lists with its diffs.
type kindRow struct {
	Name  string
	Add   int
	Del   int
	Files []*file
}

// team is Who changed this code (5.11).
type team struct {
	People    string // the git log line; "" without history
	NoHistory bool
	Rows      []teamRow
	New       string
	Removed   string
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
	p.Since = v.sinceLine() // after the map: it names the boxes
	p.Brief = v.brief()
	v.cards()
	v.blocks()
	p.Status = v.status()
	p.Covered, p.Kinds = v.covered(), v.kinds()
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

// sinceLine is what changed since the previous review (5.13); nil for the first one or when nothing did.
func (v *view) sinceLine() *sinceLine {
	s := v.Since
	if s == nil || len(s.Files) == 0 {
		return nil
	}
	sha := short(s.Prev.SHA)
	where := map[string]int{}
	var code, tests, docs, other int
	var order []string
	for _, f := range s.Files {
		k := v.boxOf(f.Path)
		switch {
		case k != "":
			if where[k] == 0 {
				order = append(order, k)
			}
			where[k]++
		case f.Kind == "code":
			code++
		case f.Kind == "test":
			tests++
		case f.Kind == "doc":
			docs++
		default:
			other++
		}
	}
	var at []string
	for _, k := range v.inMapOrder(order) {
		if strings.HasPrefix(k, "deploy:") {
			at = append(at, strings.ToLower(v.boxLabel(k)))
		} else {
			at = append(at, strconv.Itoa(where[k])+" in "+v.boxLabel(k))
		}
	}
	for _, c := range []struct {
		n          int
		one, other string
	}{{code, "code file", "code files"}, {docs, "doc", "docs"}, {tests, "test file", "test files"}, {other, "other file", "other files"}} {
		if c.n > 0 {
			at = append(at, plural(c.n, c.one, c.other))
		}
	}
	t := plural(len(s.Files), "file", "files") + " changed since " + sha + ", the previous review"
	if !s.Prev.At.IsZero() {
		t += " (" + s.Prev.At.Format(time.DateOnly) + ")"
	}
	t += ": " + join(at) + "."
	news, fixed := 0, 0
	for _, k := range s.New {
		if review.Blocks(s.Sev(k)) {
			news++
		}
	}
	for _, k := range s.Fixed {
		if review.Blocks(s.Sev(k)) {
			fixed++
		}
	}
	news += len(s.Risen)
	if news+fixed > 0 {
		t += " · " + strconv.Itoa(news) + " open items new · " + strconv.Itoa(fixed) + " fixed"
	}
	return &sinceLine{SHA: sha, Text: t}
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

// status is the merge status (section 4): Blocked by a failed run or a conflict, else the open items.
func (v *view) status() mstatus {
	var st mstatus
	sha := short(v.SHA)
	var fails []string
	for _, n := range slices.Sorted(maps.Keys(v.Data.Tests.Ran)) {
		if v.Data.Tests.Ran[n] == "fail" {
			fails = append(fails, n)
		}
	}
	base := cmp.Or(v.PR.Base, "the base branch")
	if v.Conflicts {
		st.Lines = append(st.Lines, mline{evBase, "The PR conflicts with " + base + "."})
	}
	if len(fails) > 0 {
		names := fails
		if len(names) > 3 {
			names = append(names[:3:3], plural(len(fails)-3, "more", "more"))
		}
		st.Lines = append(st.Lines, mline{chip{"fail", "Run failed"}, plural(len(fails), "test", "tests") + " failed at " + sha + ": " + join(names) + "."})
	}
	if b := v.Data.Tests.Broken; len(b) > 0 {
		st.Lines = append(st.Lines, mline{chip{"fail", "Run failed"}, join(b) + map[bool]string{true: " doesn't", false: " don't"}[len(b) == 1] + " build at " + sha + "."})
	}
	n := v.Page.Open
	items := plural(n, "open item", "open items")
	switch {
	case v.Conflicts:
		st.Kind, st.Head, st.Short = "blocked", "Blocked: a conflict with "+base, "Blocked · conflict with "+base
	case len(fails) > 0:
		st.Kind, st.Head, st.Short = "blocked", "Blocked: "+plural(len(fails), "test", "tests")+" failed at "+sha, "Blocked · "+plural(len(fails), "test", "tests")+" failed"
	case len(v.Data.Tests.Broken) > 0:
		st.Kind, st.Head, st.Short = "blocked", "Blocked: the build failed at "+sha, "Blocked · the build failed"
	case n > 0:
		st.Kind, st.Head, st.Short, st.Note = "open", items, items, "Nothing blocks the merge." // nothing reads a wait yet
	default:
		st.Kind, st.Head, st.Short = "none", "No open items", "No open items"
		st.Note = "Nothing to finish before merging, no major finding and no gap in the review."
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
		if b := len(v.Page.Before); b > 0 {
			k = append(k, strconv.Itoa(b)+" before merging")
		}
		if m := v.majors(); m > 0 {
			k = append(k, plural(m, "major finding", "major findings"))
		}
		if v.Page.Gap != nil {
			k = append(k, "1 gap in the review")
		}
		open.Second = strings.Join(k, ", ")
	}
	st.Stats = append(st.Stats, open)
	// the tests run here
	ran := stat{N: strconv.Itoa(len(v.Data.Tests.Ran)), Label: map[bool]string{true: "test run here", false: "tests run here"}[len(v.Data.Tests.Ran) == 1]}
	if len(v.Data.Tests.Ran) == 0 {
		ran.Second = "No test result reached this page"
	} else {
		ran.Second = v.ranText() + " at " + sha
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
		nr.Of, nr.Second = "", "It read all "+strconv.Itoa(total)
	} else {
		nr.Second = strconv.Itoa(code) + " of them code"
	}
	st.Stats = append(st.Stats, nr)
	return st
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
	if v.Since != nil {
		if slices.Contains(v.Since.New, c.Path+": "+c.Title) {
			k.Tags = append(k.Tags, "New since "+short(v.Since.Prev.SHA))
		} else if f := v.files[c.Path]; f != nil && f.Changed != "" {
			k.Tags = append(k.Tags, "Changed since "+f.Changed)
		}
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
			href += ":" + strconv.Itoa(line)
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
	if from > len(lines) {
		return nil
	}
	a, b := max(1, from-2), min(len(lines), to+3)
	x := &excerpt{Path: p, Range: strconv.Itoa(a) + "–" + strconv.Itoa(b), Cited: strconv.Itoa(cited)}
	if a == b {
		x.Range = strconv.Itoa(a)
	}
	if to > cited {
		x.Cited += "–" + strconv.Itoa(to)
	}
	if v.files[p] != nil {
		x.Href = "#" + p + ":" + strconv.Itoa(cited)
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
		p.Gap = g
	}
	p.Open = len(p.Before) + v.majors()
	if p.Gap != nil {
		p.Open++
	}
	for _, k := range p.Before { // a major finding on a deploy box counts once
		if k.Kind == "Major finding" {
			p.Open--
		}
	}
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
		paths[at] = true
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
			if f := v.files[p]; f != nil && f.Changed != "" {
				k.Tags = append(k.Tags, "Changed since "+f.Changed)
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

// covered is What the review covered: the open items, the tests, the findings, the coverage (5.10).
func (v *view) covered() []crow {
	p := &v.Page
	sha := short(v.SHA)
	var rows []crow
	var kinds []string
	if b := len(p.Before) - countKind(p.Before, "Major finding"); b > 0 {
		kinds = append(kinds, strconv.Itoa(b)+" before merging")
	}
	if m := v.majors(); m > 0 {
		kinds = append(kinds, plural(m, "major finding", "major findings"))
	}
	if p.Gap != nil {
		kinds = append(kinds, "1 gap in the review")
	}
	open := "None"
	if len(kinds) > 0 {
		open = strconv.Itoa(p.Open) + ": " + join(kinds)
	}
	rows = append(rows, crow{"Open items", []mline{{Text: open + "."}}})
	t := v.Data.Tests
	switch {
	case len(t.Ran) == 0 && len(t.Broken) == 0:
		rows = append(rows, crow{"Tests and linters", []mline{{Text: "None ran here. No test result reached this page."}}})
	default:
		ls := []mline{}
		if len(t.Ran) > 0 {
			c := chip{"run", "Run passed"}
			if slices.Contains(slices.Collect(maps.Values(t.Ran)), "fail") {
				c = chip{"fail", "Run failed"}
			}
			ls = append(ls, mline{c, plural(len(t.Ran), "test", "tests") + " ran at " + sha + ": " + v.ranText() + "."})
		}
		if len(t.Broken) > 0 {
			ls = append(ls, mline{chip{"fail", "Run failed"}, join(t.Broken) + map[bool]string{true: " doesn't", false: " don't"}[len(t.Broken) == 1] + " build at " + sha + "."})
		}
		rows = append(rows, crow{"Tests and linters", ls})
	}
	if pr := v.Story.Proof; pr != "" {
		res := map[string]string{"pass": "passed", "fail": "failed", "skip": "was skipped"}[ranOf(t.Ran, pr)]
		if res != "" {
			c := chip{"run", "Run passed"}
			if res == "failed" {
				c = chip{"fail", "Run failed"}
			}
			rows = append(rows, crow{"Proof on the base", []mline{{c, "`" + pr + "` " + res + " at " + sha + "."}}})
		}
	}
	if n := len(v.Findings); n > 0 {
		m, ran, checked := v.majors(), 0, 0
		for _, c := range v.Findings {
			switch {
			case runBacked(c):
				ran++
			case c.Evidence != "":
				checked++
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
		ls := []mline{{c, s}}
		if ran > 0 && rest > 0 {
			ls = append(ls, mline{evTest, strconv.Itoa(ran) + " shown by a test run."})
		}
		if d := len(v.Dropped); d > 0 {
			ls = append(ls, mline{evAI, plural(d, "finding was", "findings were") + " dropped by the cross-check."})
		}
		rows = append(rows, crow{"Findings", ls})
	} else {
		rows = append(rows, crow{"Findings", []mline{{evRun, "The review found nothing to flag."}}})
	}
	unread, _ := v.unread()
	cov := "The review read all " + plural(v.Page.given, "file", "files") + " it was given."
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
		cov = join(p) + "."
	}
	if skip := len(v.files) - v.Page.given; skip > 0 {
		cov += " " + strconv.Itoa(skip) + " more were skipped by rule."
	}
	rows = append(rows, crow{"Coverage", []mline{{evRun, cov}}})
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
	var news, gone []string
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
	return append(rs, railRow{Label: "What the review covered", Href: "#covered"}, railRow{Label: "Who changed this code", Href: "#team"})
}
