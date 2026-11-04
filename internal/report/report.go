// Package report renders the page a person reads a large PR with: the reading order through the code with its
// diff, what only a person can decide, the commits, the tests as a list of cases, the mechanical rest folded away.
package report

import (
	"bytes"
	"cmp"
	"context"
	"diffharbor/internal/assets"
	"diffharbor/internal/checks"
	"diffharbor/internal/diff"
	"diffharbor/internal/review"
	"diffharbor/internal/text"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Data is what a report shows.
type Data struct {
	Repo        string // owner/name
	Number      int
	Title, URL  string // URL: the PR's page
	SHA         string
	Summary     string
	Diagram     string // mermaid
	Effort      int
	Decisions   []review.Decision
	Story       review.Story
	Flow        []review.Step
	Walkthrough map[string]string // path -> one line
	Commits     []review.Commit
	Outline     diff.Outline
	Findings    []review.Comment // open ones, Line 0 = not on a line of the diff
	Diff        string           // the whole PR diff
	Routine     []review.Routine // hunks the review calls routine: folded
	Symbols     map[string][]Symbol
	Themes      review.SyntaxTheme // Shiki's, by color mode; unset: Tokyo Night
	Whys        map[string]string  // path NUL name -> what a declaration Asks named is for: review.Explain's
	Others      []Other            // open PRs that change files this one does
	Behind      []review.Commit    // the base's commits since the fork point (review.Behind)
	Conflicts   bool               // the forge says the PR conflicts with its base
	Forge       Forge
	Prev        *Version          // the version before: the report marks what changed since; nil for the first
	Dropped     []review.Comment  // what the verifier dropped, its reason in Evidence
	Tests       checks.Tests      // what the checks' test runs report at SHA; empty when none ran
	Sources     map[string]string // the head's text of changed files, for the lines between hunks (see Sources)
}

// Forge is how the forge hosting the repository names and links what a report points to.
type Forge struct {
	Name     string // "GitHub": "Open on GitHub"
	Ref      string // what goes before a PR's number: "#"; GitLab's merge requests take "!"
	Blob     string // a file at the head: Blob + path + "#L" + line
	Commit   string // a commit of the repository: Commit + SHA
	PRCommit string // a commit in the PR's own view: PRCommit + SHA
	Search   string // code search in the repository: Search + the escaped query; "" when the forge has none
}

// tab is a tab of the card over the overview: what needs a person, a kind each.
type tab struct {
	ID, Title string
	N         int
	Warn      bool // something in it a merge can't wait for
	On        bool // the first one: shown
}

// maxLines and maxBytes are the diff lines and their text a file shows; the rest is on the PR's page. Bytes too:
// a generated file's lines (minified JSON, seed SQL) can be megabytes each.
const maxLines, maxBytes = 3000, 100_000

// maxGone is the lines a deleted file shows, and the code that moved out of it: what the PR drops is rarely read.
const maxGone = 500

// wip matches commit subjects that tell nothing about the change.
var wip = regexp.MustCompile(`(?i)^(wip\b|fixup!|squash!|amend!|typo|lint|fmt\b|format\b|oops|tmp\b|\.+$|fix(es)?$|changes?$|updates?$|address(ed)? (review|comments|feedback)|(pr |review )(comments|feedback))`)

// templates is the page: report.html and the templates it calls, a file per part (see Load).
//
//go:embed templates/*.html
var templates embed.FS

// css is styles.css with the Tailwind classes the templates use, compiled at build time: Tailwind's browser build
// rescanned the whole page on each change, a tenth of a second on a big PR. tailwindcss is Tailwind's standalone
// CLI, 4.3.4 like the Dockerfile's: the npm one resolves @import "tailwindcss" from whatever node_modules is nearby.
//
//go:generate tailwindcss -i styles.css -o styles.gen.css --minify
//go:embed styles.gen.css
var css string

//go:embed styles.css
var styles string

// compile is styles.css compiled with the classes of ours and dir's templates. Without the CLI (the image has it),
// ours: a class only dir's templates use does nothing.
func compile(dir string) (string, error) {
	if _, err := exec.LookPath("tailwindcss"); err != nil {
		log.Println("report: no tailwindcss, the classes only", dir, "uses do nothing")
		return css, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "diffharbor-css-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	src := styles + "@source " + strconv.Quote(filepath.Join(abs, "*.html")) + ";\n"
	if err := os.CopyFS(tmp, templates); err != nil { // tmp/templates/*.html, where styles.css looks
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, "styles.css"), []byte(src), 0o600); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var out, stderr bytes.Buffer
	c := exec.CommandContext(ctx, "tailwindcss", "-i", filepath.Join(tmp, "styles.css"), "--minify")
	c.Env, c.Stdout, c.Stderr = []string{"PATH=" + os.Getenv("PATH")}, &out, &stderr // none of our keys
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("tailwindcss: %w: %s", err, text.Truncate(stderr.String(), 300))
	}
	return out.String(), nil
}

var tmpl = template.Must(parseTemplates(nil))

var funcs = template.FuncMap{
	"short":  short,
	"plural": plural,
	"join":   join,
	"sep": func(i, n int) string { // before the i-th of n in a list: "", ", ", " and "
		return map[bool]string{true: "", false: map[bool]string{true: " and ", false: ", "}[i == n-1]}[i == 0]
	},
	"inc":      func(i int) int { return i + 1 },
	"add":      func(a, b int) int { return a + b },
	"icon":     icon,
	"css":      func() template.CSS { return template.CSS(css) }, //nolint:gosec // ours, compiled by Tailwind
	"favicons": func() template.HTML { return favicons },
	"logo":     func() template.HTML { return logo },
	"has":      slices.Contains[[]string],
	"seq":      func(n int) []int { return make([]int, n) },
	"wip":      func(subject string) bool { return wip.MatchString(strings.TrimSpace(subject)) },
	"dir":      func(p string) string { return p[:strings.LastIndex(p, "/")+1] }, // "a/b/" of a/b/c.go: muted, the name stands out
	"base":     func(p string) string { return p[strings.LastIndex(p, "/")+1:] },
	"test": func(t string) string { // "a_test.go TestSave" of step.Tests: "TestSave"; a file alone stays
		_, n, ok := strings.Cut(t, " ")
		return map[bool]string{true: n, false: t}[ok]
	},
	"tab": func(id, title string, n int, warn bool, first string) tab {
		return tab{id, title, n, warn, id == first}
	},
	"finding": func(s string) string { // "a/b/c.go: title" of Version.Findings: "c.go: title"
		p, t, _ := strings.Cut(s, ": ")
		return p[strings.LastIndex(p, "/")+1:] + ": " + t
	},
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
	"percent": func(n, of int) string {
		if of == 0 {
			return "0%"
		}
		return strconv.Itoa(n*100/of) + "%"
	},
}

// parseTemplates is the embedded templates, then over's *.html: what over defines again replaces ours.
func parseTemplates(over fs.FS) (*template.Template, error) {
	t, err := template.New("").Funcs(funcs).ParseFS(templates, "templates/*.html")
	if err != nil || over == nil {
		return t, err
	}
	if m, _ := fs.Glob(over, "*.html"); len(m) > 0 {
		return t.ParseFS(over, "*.html")
	}
	return t, nil
}

// Load takes an operator's branding from dir, like Django's template dirs: a *.html file there defines a template
// again ({{define "theme"}}…{{end}}) or replaces a whole file of templates/ by its name; the files go in name order,
// the last definition wins. logo.png (64 px), favicon.svg and favicon-32x32.png replace the images. Reports rendered
// before keep theirs. "" keeps ours.
func Load(dir string) error {
	if dir == "" {
		return nil
	}
	d := os.DirFS(dir)
	t, err := parseTemplates(d)
	if err != nil {
		return fmt.Errorf("report: templates in %s: %w", dir, err)
	}
	img := map[string][]byte{"logo.png": assets.Logo, "favicon.svg": assets.FaviconSVG, "favicon-32x32.png": assets.FaviconPNG}
	for name := range img {
		b, err := fs.ReadFile(d, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("report: %w", err)
		}
		img[name] = b
	}
	// a template naming a field the page has none of fails here, not at the first review
	if err := t.ExecuteTemplate(io.Discard, "report.html", prepare(Data{})); err != nil {
		return fmt.Errorf("report: templates in %s: %w", dir, err)
	}
	c := css
	if m, _ := fs.Glob(d, "*.html"); len(m) > 0 {
		if c, err = compile(dir); err != nil {
			return fmt.Errorf("report: the CSS for %s: %w", dir, err)
		}
	}
	tmpl, css = t, c
	logo, favicons = images(img["logo.png"], img["favicon.svg"], img["favicon-32x32.png"])
	return nil
}

type line struct {
	Class, No, Text string
	ID              string // a jump target: where a declaration's change starts
	Old             int    // the line in the old file: del and ctx lines
	Run             int    // the folded run the line is in, 0 = shown
	Findings        []*finding
}

type file struct {
	Path, Kind, Note string
	Short            string // Path without the directory all files share, for the side bar
	ID               string // diff.PatchIDs: "viewed" holds until the file's change changes
	Open             bool   // unfolded: code in the reading order
	Add, Del         int
	Lines            []line
	Parts            []part     // Lines as shown: plain runs and folded ones
	Moved            int        // added lines that only moved here
	Cut              int        // lines left out past maxLines
	Findings         []*finding // on the file but no line it shows
	Changes          []change   // the declarations it changes
	Moves            []change   // the declarations that only moved here, ID their fold
	Long             bool       // lists Changes above its diff
	Changed          string     // Prev's short SHA when its change differs from Prev's, or it's new to the PR
	New, Gone        bool       // the PR adds or deletes it
}

// change is a declaration a file's diff touches: State "new", "gone" or "" (changed), ID its first changed line.
type change struct {
	Name, State, ID string
	Sev             string // the heaviest finding on its lines: "critical", "major", "minor"
	added, removed  bool   // its declaring line
	sig             bool   // what callers see of it differs: see signature
}

type step struct {
	Title, Why string
	Files      []*file
	Graph      *flow    // how its changed declarations name each other, nil when they don't
	Tests      []string // the tests that reach what it changes, the first maxTests: "a_test.go TestSave"
	More       int      // past those
	Partial    bool     // at least: a walk stopped short
	Untested   bool     // no test reaches what it changes: see tested
	Cycles     []cycle  // directories its changes make use each other
}

type group struct {
	What  string
	Files []*file
	Names []string // what has no diff to show (moves, lockfiles)
}

type view struct {
	Data
	Steps                []step
	Tests, Other         []*file
	Mechanical           []group
	Deleted              []*file // what moved out of them folds, the rest is what the PR drops
	Stepwise             bool    // the commits read one by one
	Code, NTests, NOther int
	InSteps, NSteps      int // of Code, the files in the reading order's steps; those steps
	Added, Moved         int // added lines, and those of them that only moved
	Tree                 []*node
	Inventory            []bucket        // what the PR did to its declarations
	Deps                 *deps           // which changed directories use which
	Affected             []affected      // changed code the rest of the repo uses
	Graphs               bool            // a graph to draw: the page loads cytoscape
	Risks                []risk          // what a merge doesn't finish
	BaseMoved            []review.Commit // of Behind, those that change the PR's files
	Since                *since          // what changed from Prev
	Groups               []fgroup        // the findings and weakened tests by weight
	NFound               int             // in Groups
	Outside              []*finding      // on a file the diff doesn't show: in full under the list
	Verdict              verdict
	Draft                draft
	MergeN               int       // rows of the merge risks: Risks, BaseMoved, Conflicts, Others
	MergeWarn            bool      // one of them is sure trouble
	Authors              []string  // of the commits, first one first
	Retired              []retired // deleted tests with the code they tested: no finding
	now                  Version   // what the next version compares with
	files                map[string]*file
}

// node is a directory of the side bar's file tree, or a file when File is set.
type node struct {
	Name string
	File *file
	Kids []*node
}

// shared is the directory all paths share, with its slash; none for a single path.
func shared(paths []string) string {
	if len(paths) < 2 {
		return ""
	}
	pre := paths[0][:strings.LastIndex(paths[0], "/")+1]
	for _, p := range paths[1:] {
		for !strings.HasPrefix(p, pre) {
			pre = pre[:strings.LastIndex(strings.TrimSuffix(pre, "/"), "/")+1]
		}
	}
	return pre
}

// Render returns the report page and the version it is, for the next one to compare with; the caller sets its time.
func Render(d Data) ([]byte, Version, error) {
	var b bytes.Buffer
	v := prepare(d)
	if err := tmpl.ExecuteTemplate(&b, "report.html", v); err != nil {
		return nil, Version{}, fmt.Errorf("report: %w", err)
	}
	return b.Bytes(), v.now, nil
}

// Asks is the declarations of the report's inventory that neither a doc comment nor a commit explains, new or with
// a changed signature, for review.Explain: their keys for Data.Whys, and what the model reads of each.
func Asks(d Data) (keys, texts []string) {
	for _, g := range prepare(d).Inventory {
		for _, it := range g.Items {
			if it.ask != nil && it.Doc == "" && !it.Commit {
				keys, texts = append(keys, it.key), append(texts, strings.Join(it.ask, "\n"))
			}
		}
	}
	return keys, texts
}

// prepare is the page's view of d.
func prepare(d Data) view {
	v := view{Data: d, files: map[string]*file{}}
	v.Themes.Light = cmp.Or(v.Themes.Light, "tokyo-night-day") // the page carries this one: Shiki has only the dark
	v.Themes.Dark = cmp.Or(v.Themes.Dark, "tokyo-night")
	files := v.files
	var order []string
	ids := diff.PatchIDs(d.Diff)
	diff.EachFile(d.Diff, func(p, f string) {
		files[p] = parse(p, f, d.Walkthrough[p])
		files[p].ID = ids[p]
		order = append(order, p)
	})
	var gone []string
	diff.EachDeleted(d.Diff, func(p, f string) {
		files[p] = parse(p, f, d.Walkthrough[p])
		files[p].ID = ids[p]
		files[p].Gone = true
		gone, v.Deleted = append(gone, p), append(v.Deleted, files[p])
	})
	skim := map[string]map[int]string{}
	for _, r := range d.Routine {
		if skim[r.Path] == nil {
			skim[r.Path] = map[int]string{}
		}
		skim[r.Path][r.Line] = r.Why
	}
	runs := fold(slices.Concat(order, gone), files, skim)
	for _, p := range gone { // after fold: the moves it finds are in the whole file
		trim(files[p], runs)
	}
	var found []*finding
	for i, c := range d.Findings {
		fd := &finding{Comment: c}
		found = append(found, fd)
		f := files[c.Path]
		fd.ID = "finding-" + strconv.Itoa(i+1)
		fd.Href = "#" + fd.ID
		if f == nil { // outside the diff: in full under the list, a link to the file on the forge
			if d.Forge.Blob != "" {
				fd.Blob = d.Forge.Blob + c.Path
				if c.Line > 0 {
					fd.Blob += "#L" + strconv.Itoa(c.Line)
				}
			}
			v.Outside = append(v.Outside, fd)
			continue
		}
		at := slices.IndexFunc(f.Lines, func(l line) bool { return l.No == strconv.Itoa(c.Line) })
		if c.Line == 0 || at < 0 {
			f.Findings = append(f.Findings, fd)
		} else {
			f.Lines[at].Findings = append(f.Lines[at].Findings, fd)
		}
	}
	var all []*file
	for _, p := range slices.Concat(order, gone) {
		all = append(all, files[p])
	}
	v.Risks, v.BaseMoved = risks(all), baseMoved(d.Behind, files) // before parts, like spans: risks marks the lines it jumps to
	v.MergeN, v.MergeWarn = len(v.Risks), d.Conflicts
	for _, r := range v.Risks {
		for _, it := range r.Items {
			v.MergeWarn = v.MergeWarn || it.Warn
		}
	}
	for _, b := range []bool{len(v.BaseMoved) > 0, d.Conflicts, len(d.Others) > 0} {
		if b {
			v.MergeN++
		}
	}
	for _, o := range d.Others {
		v.MergeWarn = v.MergeWarn || len(o.Clash) > 0
	}
	v.now = stamp(v, ids)
	v.Since = compare(d.Prev, v.now, files)
	var ss []*span
	for _, p := range slices.Concat(order, gone) {
		f := files[p]
		f.Changes, f.Moves = changes(f, runs)
		ss = append(ss, spans(f, runs)...) // before parts: it marks the lines it jumps to
		f.Long = len(f.Lines) >= minChanges
		f.Parts = parts(f.Lines, runs)
		v.Added += f.Add
		v.Moved += f.Moved
	}
	skip := d.Outline.Skippable()
	used := map[string]bool{}
	for _, g := range d.Outline.Mechanical {
		mg := group{What: g.What}
		for _, p := range g.Files {
			if f := files[p]; f != nil && !used[p] {
				mg.Files, used[p] = append(mg.Files, f), true
			} else {
				mg.Names = append(mg.Names, p)
			}
		}
		v.Mechanical = append(v.Mechanical, mg)
	}
	for _, s := range d.Flow {
		st := step{Title: s.Title, Why: s.Why}
		for _, p := range s.Files {
			if f := files[p]; f != nil && !used[p] && !skip[p] {
				f.Open = f.Moved*5 < f.Add*4 // mostly moved: the folds would be all there is
				st.Files, used[p] = append(st.Files, f), true
			}
		}
		if len(st.Files) > 0 {
			v.Steps = append(v.Steps, st)
		}
	}
	var rest []*file // code the reading order left out, in the diff's order
	for _, p := range order {
		f := files[p]
		switch {
		case used[p]:
		case f.Kind == "code":
			f.Open = len(d.Flow) == 0 // with a reading order, only its files start open
			rest = append(rest, f)
		case f.Kind == "test":
			v.Tests = append(v.Tests, f)
		default:
			v.Other = append(v.Other, f)
		}
	}
	v.NSteps = len(v.Steps)
	for _, s := range v.Steps {
		v.InSteps += len(s.Files)
	}
	if len(rest) > 0 {
		title := "The rest of the code"
		if len(v.Steps) == 0 {
			title = "Code"
		}
		v.Steps = append(v.Steps, step{Title: title, Files: rest})
	}
	for _, s := range v.Steps {
		v.Code += len(s.Files)
		// a file with a finding that blocks the merge goes first in its step: what to fix is what to read first
		slices.SortStableFunc(s.Files, func(a, b *file) int { return cmp.Compare(rank(b), rank(a)) })
	}
	v.NTests, v.NOther = len(v.Tests), len(v.Other)
	pre := shared(order) // the side bar drops the directory all files share
	for _, f := range files {
		f.Short = strings.TrimPrefix(f.Path, pre)
	}
	v.Tree = tree(files)
	for i := range v.Steps {
		v.Steps[i].Graph = graph(v.Steps, i, d.Symbols)
		st := &v.Steps[i]
		st.Tests, st.More, st.Partial, st.Untested = tested(*st, d.Symbols)
		st.Cycles = cycles(v.Steps, i, d.Symbols)
		v.Graphs = v.Graphs || v.Steps[i].Graph != nil
	}
	v.Inventory = inventory(ss, d.Symbols, d.Commits, d.Title, d.Whys)
	v.Deps, v.Affected = matrix(v.Steps, d.Symbols), reach(v.Steps, d.Symbols)
	messy := 0
	for _, c := range d.Commits {
		if wip.MatchString(strings.TrimSpace(c.Subject)) {
			messy++
		}
	}
	v.Stepwise = len(d.Commits) > 1 && messy*3 < len(d.Commits) // under a third wip
	for _, c := range d.Commits {
		if c.Author != "" && !slices.Contains(v.Authors, c.Author) {
			v.Authors = append(v.Authors, c.Author)
		}
	}
	v.Retired = retire(d.Outline.Removed, files)
	judge(&v, found)
	if len(d.Tests.Ran) > 0 || len(d.Tests.Broken) > 0 {
		v.now.Tests = map[string]string{}
		names := []string{d.Story.Proof}
		for _, s := range v.Steps {
			for _, t := range s.Tests {
				_, n, _ := strings.Cut(t, " ")
				names = append(names, n)
			}
		}
		for _, c := range d.Outline.Added {
			_, n, _ := strings.Cut(c, ": ")
			names = append(names, n)
		}
		for _, n := range names {
			if n != "" {
				v.now.Tests[testKey(n)] = cmp.Or(ranOf(d.Tests.Ran, n), "none")
			}
		}
	}
	return v
}

// Settled is the line over what left the findings: fixed since the last review, dropped by the cross-check,
// tests deleted with their code; "" for none.
func (v view) Settled() string {
	var p []string
	if v.Since != nil && len(v.Since.Fixed) > 0 {
		p = append(p, strconv.Itoa(len(v.Since.Fixed))+" fixed")
	}
	if n := len(v.Dropped); n > 0 {
		p = append(p, strconv.Itoa(n)+" dropped by the cross-check")
	}
	n := 0
	for _, r := range v.Retired {
		n += len(r.Names)
	}
	if n > 0 {
		p = append(p, plural(n, "test", "tests")+" removed with their code")
	}
	return strings.Join(p, " · ")
}

// retired is the tests the PR deletes with the declaration they are named after: nothing is left to test.
type retired struct {
	Names []string // "TestTotalNet"
	With  string   // "totalNet"
}

// retire is those of the deleted cases ("path: TestTotalNet") whose name, past "Test", starts with a declaration
// the PR deletes and adds nowhere: TestTotalNet and TestTotalNetRounding with totalNet.
func retire(removed []string, files map[string]*file) []retired {
	gone, kept := map[string]bool{}, map[string]bool{}
	for _, f := range files {
		if f.Kind != "code" {
			continue
		}
		for _, c := range f.Changes {
			if c.State == "gone" {
				gone[c.Name] = true
			} else {
				kept[c.Name] = true
			}
		}
	}
	var out []retired
	for _, rc := range removed {
		_, n, _ := strings.Cut(rc, ": ")
		rest, ok := strings.CutPrefix(n, "Test")
		if !ok {
			continue
		}
		for g := range gone {
			if kept[g] || len(g) < 3 || len(rest) < len(g) || !strings.EqualFold(rest[:len(g)], g) {
				continue
			}
			if r := rest[len(g):]; r != "" && !(r[0] == '_' || r[0] >= 'A' && r[0] <= 'Z') { // TestTotalNet is no test of Total's
				continue
			}
			i := slices.IndexFunc(out, func(r retired) bool { return r.With == g })
			if i < 0 {
				i, out = len(out), append(out, retired{With: g})
			}
			out[i].Names = append(out[i].Names, n)
			break
		}
	}
	return out
}

// ref is a stretch of a model's text and the place it names after it: "invoice.go:42", "(a/invoice.go:42)".
type ref struct {
	Text, Path, Href string // Path "": the text after the last place
	Line             int
	Out              bool // Href is the forge's: the file is not in the diff
}

var place = regexp.MustCompile(`\(([\w./-]+\.[A-Za-z]\w*):(\d+)\)|\b([\w./-]+\.[A-Za-z]\w*):(\d+)\b`)

// Refs is s cut at the places it names, each one resolved to a file of the diff when one ends in it.
func (v view) Refs(s string) []ref {
	var out []ref
	last := 0
	for _, m := range place.FindAllStringSubmatchIndex(s, -1) {
		g := 2
		if m[2] < 0 {
			g = 6
		}
		n, _ := strconv.Atoi(s[m[g+2]:m[g+3]])
		r := v.place(s[m[g]:m[g+1]], n)
		r.Text, last = s[last:m[0]], m[1]
		out = append(out, r)
	}
	return append(out, ref{Text: s[last:]})
}

// Ref is the place at names, "path:line"; nil when it names none.
func (v view) Ref(at string) *ref {
	if rs := v.Refs(at); len(rs) > 1 {
		return &rs[0]
	}
	return nil
}

func (v view) place(p string, line int) ref {
	if v.files[p] == nil {
		var hit []string
		for q := range v.files {
			if strings.HasSuffix(q, "/"+p) {
				hit = append(hit, q)
			}
		}
		if len(hit) == 1 {
			p = hit[0]
		}
	}
	r := ref{Path: p, Line: line}
	if v.files[p] != nil {
		r.Href = "#" + p + ":" + strconv.Itoa(line) // the script finds the line
	} else if v.Forge.Blob != "" {
		r.Href, r.Out = v.Forge.Blob+p+"#L"+strconv.Itoa(line), true
	}
	return r
}

// testRun is how a test fared at the head and at the last review: State the head's, for its mark.
type testRun struct{ State, Text string }

// Run is test name's run, its spaces as go test writes them; none when no test ran at either version.
func (v view) Run(name string) testRun {
	say := map[string]string{"pass": "passed", "fail": "failed", "skip": "skipped", "none": "not run"}
	var r testRun
	var parts []string
	if v.Prev != nil && v.Prev.Tests != nil {
		if st := v.Prev.Tests[testKey(name)]; st != "" {
			parts = append(parts, say[st]+" on "+short(v.Prev.SHA))
		}
	}
	if t := v.Data.Tests; len(t.Ran) > 0 || len(t.Broken) > 0 {
		r.State = cmp.Or(ranOf(t.Ran, name), "none")
		now := say[r.State] + " on " + short(v.SHA)
		if len(parts) > 0 && strings.HasPrefix(parts[0], say[r.State]+" ") {
			parts = nil // the same as at the last review
		}
		parts = append(parts, now)
	}
	r.Text = strings.Join(parts, ", ")
	return r
}

func testKey(name string) string { return strings.ReplaceAll(name, " ", "_") }

// ranOf is name's outcome in ran: by its full name, or as the one subtest of that name.
func ranOf(ran map[string]string, name string) string {
	n := testKey(name)
	if st, ok := ran[n]; ok {
		return st
	}
	st := ""
	for k, s := range ran {
		if strings.HasSuffix(k, "/"+n) {
			if st != "" {
				return "" // two of that name: no telling which
			}
			st = s
		}
	}
	return st
}

// trim cuts a deleted file to its first maxGone lines and what moved out of it, which the other side links to.
func trim(f *file, runs map[int]run) {
	if len(f.Lines) <= maxGone {
		return
	}
	ls := f.Lines[:maxGone]
	for _, l := range f.Lines[maxGone:] {
		if runs[l.Run].Where != "" {
			ls = append(ls, l)
		}
	}
	f.Cut += len(f.Lines) - len(ls)
	f.Lines = ls
}

// parse reads one file's diff into lines numbered as in the new file.
func parse(p, f, note string) *file {
	fl := &file{Path: p, Kind: diff.Kind(p), Note: note}
	head, body, ok := strings.Cut(f, "\n@@")
	fl.New = strings.Contains(head+"\n", "\n--- /dev/null\n")
	if !ok {
		return fl
	}
	n, o, size := 0, 0, 0
	for l := range strings.SplitSeq("@@"+body, "\n") {
		var ln line
		switch {
		case strings.HasPrefix(l, "@@"):
			if m := hunk.FindStringSubmatch(l); m != nil {
				o, _ = strconv.Atoi(m[1])
				n, _ = strconv.Atoi(m[2])
			}
			ln = line{Class: "hunk", Text: l}
		case strings.HasPrefix(l, "+"):
			ln = line{Class: "add", No: strconv.Itoa(n), Text: l[1:]}
			fl.Add++
			n++
		case strings.HasPrefix(l, "-"):
			ln = line{Class: "del", Old: o, Text: l[1:]}
			fl.Del++
			o++
		case strings.HasPrefix(l, " "):
			ln = line{Class: "ctx", No: strconv.Itoa(n), Old: o, Text: l[1:]}
			n++
			o++
		default: // "\ No newline at end of file", the trailing ""
			continue
		}
		if size += len(ln.Text); fl.Cut == 0 && len(fl.Lines) < maxLines && size <= maxBytes {
			fl.Lines = append(fl.Lines, ln)
		} else {
			fl.Cut++
		}
	}
	return fl
}

var hunk = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@ ?(.*)`)

// tree is the files by directory, a directory with nothing but one directory merged into it ("internal/report"),
// directories before files.
func tree(files map[string]*file) []*node {
	root := &node{}
	for _, f := range files {
		n := root
		parts := strings.Split(f.Short, "/")
		for _, p := range parts[:len(parts)-1] {
			i := slices.IndexFunc(n.Kids, func(k *node) bool { return k.File == nil && k.Name == p })
			if i < 0 {
				i, n.Kids = len(n.Kids), append(n.Kids, &node{Name: p})
			}
			n = n.Kids[i]
		}
		n.Kids = append(n.Kids, &node{Name: parts[len(parts)-1], File: f})
	}
	var tidy func(n *node)
	tidy = func(n *node) {
		for len(n.Kids) == 1 && n.Kids[0].File == nil && n.Name != "" {
			n.Name, n.Kids = n.Name+"/"+n.Kids[0].Name, n.Kids[0].Kids
		}
		slices.SortFunc(n.Kids, func(a, b *node) int {
			if (a.File == nil) != (b.File == nil) {
				return map[bool]int{true: -1, false: 1}[a.File == nil]
			}
			return cmp.Compare(a.Name, b.Name)
		})
		for _, k := range n.Kids {
			tidy(k)
		}
	}
	tidy(root)
	return root.Kids
}

// inert is a changed line outside any declaration that runs nothing: blank, a comment, an import, an import block's.
var inert = regexp.MustCompile(`^\s*(?:$|//|/\*|\*|#(?:\s|$)|--|import\b|from\s|use\s|using\s|package\s|#include\b|"[^"]*"\s*[,;]?\s*$|\w+\s+"[^"]*"\s*$|[()]\s*$)`)

// minChanges is the lines a file shows before it lists the declarations it changes.
const minChanges = 60

// changes lists the declarations whose lines the diff adds or removes, past folded runs: the one a hunk's header
// names, then each one a line of the hunk declares. moves is those whose head only moved here.
func changes(f *file, runs map[int]run) (cs, moves []change) {
	if f.Kind != "code" && f.Kind != "test" {
		return nil, nil
	}
	at := map[string]int{}
	heads := map[string]*[2]string{} // name -> its signature before and now, the first the diff shows of each
	sev := map[string]string{}
	cur := ""
	for i := range f.Lines {
		l := &f.Lines[i]
		if l.Class == "hunk" {
			cur = ""
			if m := hunk.FindStringSubmatch(l.Text); m != nil {
				if d := decl.FindStringSubmatch(m[3]); d != nil {
					cur = d[2]
				}
			}
			continue
		}
		head := false
		if d := decl.FindStringSubmatch(l.Text); d != nil {
			cur, head = d[2], true
		}
		if head && runs[l.Run].Where == "" {
			before, now, changed := sigs(f, i)
			if h := heads[cur]; h == nil && (changed || l.Class != "ctx") {
				heads[cur] = &[2]string{before, now}
			} else if h != nil {
				h[0], h[1] = cmp.Or(h[0], before), cmp.Or(h[1], now)
			}
		}
		for _, fd := range l.Findings {
			if cur != "" && heavier(fd.Severity, sev[cur]) {
				sev[cur] = fd.Severity
			}
		}
		if head && l.Class == "add" && runs[l.Run].Where != "" && !slices.ContainsFunc(moves, func(c change) bool { return c.Name == cur }) {
			moves = append(moves, change{Name: cur, ID: "fold-" + strconv.Itoa(l.Run), State: "moved"})
		}
		if cur == "" || l.Class == "ctx" || runs[l.Run].Where != "" { // moved code is no change
			continue
		}
		k, ok := at[cur]
		if !ok {
			l.ID = fmt.Sprintf("%s~%d", f.Path, i)
			k, at[cur] = len(cs), len(cs)
			cs = append(cs, change{Name: cur, ID: l.ID})
		}
		if head {
			cs[k].added, cs[k].removed = cs[k].added || l.Class == "add", cs[k].removed || l.Class == "del"
		}
	}
	for i, c := range cs {
		cs[i].Sev = sev[c.Name]
		if c.added != c.removed {
			cs[i].State = map[bool]string{true: "new", false: "gone"}[c.added]
		}
		if h := heads[c.Name]; h != nil {
			cs[i].sig = h[0] != "" && h[1] != "" && !same(h[0], h[1])
		}
	}
	moves = slices.DeleteFunc(moves, func(m change) bool { _, ok := at[m.Name]; return ok }) // moved and changed is a change
	return cs, moves
}

// rank is 1 for a file with a finding that blocks the merge, 0 for the others.
func rank(f *file) int {
	for _, fd := range f.Findings {
		if fd.Severity == "critical" {
			return 1
		}
	}
	for _, l := range f.Lines {
		for _, fd := range l.Findings {
			if fd.Severity == "critical" {
				return 1
			}
		}
	}
	return 0
}

// maxSource and maxSources are the bytes of one changed file and of all that a report carries for the lines
// between hunks: past them a file shows its hunks only.
const maxSource, maxSources = 256 << 10, 4 << 20

// Sources is the text at the checkout repo of the files diffText changes and keeps, for the unchanged lines
// between and around hunks; a file too big, unreadable or not text stays out.
func Sources(repo, diffText string) map[string]string {
	out, total := map[string]string{}, 0
	diff.EachFile(diffText, func(p, _ string) {
		if !filepath.IsLocal(p) {
			return
		}
		b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(p)))
		if err != nil || len(b) > maxSource || total+len(b) > maxSources || bytes.IndexByte(b, 0) >= 0 {
			return
		}
		out[p], total = string(b), total+len(b)
	})
	return out
}
