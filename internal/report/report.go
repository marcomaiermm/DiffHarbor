// Package report renders the page a person reads a large PR with: the reading order through the code with its
// diff, what only a person can decide, the commits, the tests as a list of cases, the mechanical rest folded away.
package report

import (
	"bytes"
	"cmp"
	"context"
	"diffharbor/internal/assets"
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
	Decisions   []string
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
	Prev        *Version // the version before: the report marks what changed since; nil for the first
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
	"short":    short,
	"plural":   plural,
	"join":     join,
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
	Long             bool       // lists Changes above its diff
	Changed          bool       // its change differs from Prev's, or it's new to the PR
	Stray            bool       // a line it changes is in none of Changes: a step's tests can't tell what it does
}

// change is a declaration a file's diff touches: State "new", "gone" or "" (changed), ID its first changed line.
type change struct {
	Name, State, ID string
	added, removed  bool // its declaring line
	sig             bool // what callers see of it differs: see signature
}

type step struct {
	Title, Why string
	Files      []*file
	Graph      *flow    // how its changed declarations name each other, nil when they don't
	Tests      []string // the tests that reach what it changes, the first maxTests: "a_test.go TestSave"
	More       int      // past those
	Partial    bool     // at least: a walk stopped short
	Untested   bool     // no test reaches what it changes: see tested
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
	Verdict              verdict
	Draft                draft
	MergeN               int     // rows of the merge risks: Risks, BaseMoved, Conflicts, Others
	MergeWarn            bool    // one of them is sure trouble
	now                  Version // what the next version compares with
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
	v := view{Data: d}
	v.Themes.Light = cmp.Or(v.Themes.Light, "tokyo-night-day") // the page carries this one: Shiki has only the dark
	v.Themes.Dark = cmp.Or(v.Themes.Dark, "tokyo-night")
	files := map[string]*file{}
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
		if f == nil { // outside the diff: the list links to the forge
			if d.Forge.Blob != "" {
				fd.Href, fd.External = d.Forge.Blob+c.Path, true
				if c.Line > 0 {
					fd.Href += "#L" + strconv.Itoa(c.Line)
				}
			}
			continue
		}
		fd.ID = "finding-" + strconv.Itoa(i+1)
		fd.Href = "#" + fd.ID
		at := slices.IndexFunc(f.Lines, func(l line) bool { return l.No == strconv.Itoa(c.Line) })
		if c.Line == 0 || at < 0 {
			f.Findings = append(f.Findings, fd)
		} else {
			f.Lines[at].Findings, fd.inline = append(f.Lines[at].Findings, fd), true
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
		f.Changes = changes(f, runs)
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
	judge(&v, found)
	return v
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
	_, body, ok := strings.Cut(f, "\n@@")
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

// inert is a changed line outside a declaration that runs nothing: blank, a comment, an import, an import block's.
var inert = regexp.MustCompile(`^\s*(?:$|//|/\*|\*|#(?:\s|$)|--|import\b|from\s|use\s|using\s|package\s|#include\b|"[^"]*"\s*[,;]?\s*$|\w+\s+"[^"]*"\s*$|[()]\s*$)`)

// minChanges is the lines a file shows before it lists the declarations it changes.
const minChanges = 60

// changes lists the declarations whose lines the diff adds or removes, past folded runs: the one a hunk's header
// names, then each one a line of the hunk declares.
func changes(f *file, runs map[int]run) []change {
	if f.Kind != "code" && f.Kind != "test" {
		return nil
	}
	var cs []change
	at := map[string]int{}
	heads := map[string]*[2]string{} // name -> its signature before and now, the first the diff shows of each
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
		if cur == "" || l.Class == "ctx" || runs[l.Run].Where != "" { // moved code is no change
			f.Stray = f.Stray || cur == "" && l.Class != "ctx" && runs[l.Run].Where == "" && !inert.MatchString(l.Text)
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
		if c.added != c.removed {
			cs[i].State = map[bool]string{true: "new", false: "gone"}[c.added]
		}
		if h := heads[c.Name]; h != nil {
			cs[i].sig = h[0] != "" && h[1] != "" && !same(h[0], h[1])
		}
	}
	return cs
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
