package report

import (
	"bufio"
	"cmp"
	"context"
	"diffharbor/internal/checks"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"errors"
	"maps"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Rules is what the review was told to follow, read on the base branch at SHA: the PR can't change it. No Files:
// the base branch has no rules file.
type Rules struct {
	SHA   string
	Files []RuleFile
}

// RuleFile is a rules file the review read: Cut when the prompt's cuts (12,100 in all) left part of it out.
type RuleFile struct {
	Path string
	Cut  bool
}

// ReadRules is the Rules of the files read (name, text, in the order read) at sha, given the rules text the prompt
// got: a file whose text it doesn't hold whole was cut.
func ReadRules(sha, prompt string, read [][2]string) Rules {
	r := Rules{SHA: sha}
	for _, f := range read {
		if !slices.ContainsFunc(r.Files, func(rf RuleFile) bool { return rf.Path == f[0] }) {
			r.Files = append(r.Files, RuleFile{Path: f[0], Cut: !strings.Contains(prompt, f[1])})
		}
	}
	return r
}

// TestChanges is Test changes (spec 5.8): the tests the PR deletes or weakens, in three groups, from the diff and a
// git grep at the head; no model.
type TestChanges struct {
	With     []TestFile // group 1, deleted with their code: Imports names a file the PR deletes, or no file at the head names what it tests
	Kept     []TestFile // group 2, deleted while what they test still exists: Found holds where the head names it
	Unsorted []TestFile // deleted, with nothing to search for or a search that stopped early: in neither group
	Stay     []TestStay // group 3, changed in files that stay, the most flags first
}

// TestFile is a deleted test file: its tests on the base and why its group holds it.
type TestFile struct {
	Path    string
	Tests   []TestCase // its it and test blocks on the base, an .each table once; Go and Python test functions
	Imports string     // the file the PR deletes that it imports
	Names   []string   // what it tests: its describe titles' names, else what it imports from the repo's code
	Found   []string   // "path:line" where code at the head names one of them, at most maxFound
}

// TestCase is a test of a deleted file: its title and line on the base. An .each table's title can be "".
type TestCase struct {
	Title string
	Line  int
}

// TestStay is a test file the PR keeps and weakens: its flags (diff.Outline.Weakened).
type TestStay struct {
	Path  string
	Flags []diff.Flag
}

// FileKind is a row of What the review covered (spec 5.10): the files of one kind by built-in path rules, their lines,
// and how many of them the review read.
type FileKind struct {
	Kind      string   // "code" (code and config), "tests", "tooling" (docs, CI and tool config), "gone" (deleted or generated)
	Files     []string // in the diff's order, deleted files after
	Add, Del  int
	Read      int // of Files, those the review read: in a part that didn't fail and not left out (Data.Omitted, Failed)
	Deleted   int // tests: the deleted ones; gone: the deleted files no code at the head names
	Generated int // gone: files a path rule or a generated header marks
	Named     int // gone: deleted files that docs at the head still name
	Cut       bool
}

// EnvVar is an environment variable the diff starts or stops reading, and who reads it at the head (Before you merge,
// Environment). Before the page calls it no longer read, Reads and Named must be empty and Cut false; the page names
// each reader's part with its own part lookup.
type EnvVar struct {
	Reads []string // "path:line" of code that reads it: process.env.NAME, getenv("NAME"), a validated env object's env.NAME
	Named []string // other lines of code that name it, such as its key in an env schema
	Cut   bool     // the search stopped early or failed: the lists may be short
}

// Migration is a migration the diff adds or changes and what at the head names it (Before you merge, Order).
type Migration struct {
	Path        string
	Docs, Tests []string // "path:line"
	Cut         bool     // the search stopped early, failed or wasn't run (a name too short to search)
}

// Excerpt is lines of a file the page quotes that Data.Sources doesn't hold: an unchanged file at the head, or a file
// the PR changes as it was on the base.
type Excerpt struct {
	Path  string
	Base  bool // on the base, else at the head
	From  int  // the first line's number
	Lines []string
}

// Span is lines a page wants as an Excerpt, From to To.
type Span struct {
	Path     string
	Base     bool
	From, To int
}

const (
	staticTime  = time.Minute      // what all of Static's searches may take
	maxHits     = 200              // the lines one search reads
	maxFound    = 5                // the places a test file's search keeps
	maxPlaces   = 20               // the places an env variable or a migration keeps per list
	maxSearches = 200              // the deleted files and test files searched by name; past them no evidence
	excerptPad  = 10               // the lines an Excerpt takes around a place
	maxSpan     = 300              // the lines of a Span: a longer one, or one out of order, stays out
	excerptTime = 10 * time.Second // what Excerpts may take, apart from the searches
	maxLineLen  = 500              // the bytes of an Excerpt's line
)

var (
	// jsBlock is a test block at a line's start, its modifiers in group 1: it(…), test.skip(…), it.each`…`
	jsBlock  = regexp.MustCompile("^\\s*(?:it|test)((?:\\.(?:only|skip|todo|concurrent|sequential|each|fails|failing))*)\\s*[(`]")
	goTestFn = regexp.MustCompile(`^func (Test\w*)\(\s*\w+ \*testing\.T\)`)
	pyTestFn = regexp.MustCompile(`^\s*(?:async\s+)?def (test_\w+)\(`)
	describe = regexp.MustCompile(`^\s*describe(?:\.\w+)*\s*\(\s*(?:"([^"]*)"|'([^']*)'|` + "`([^`]*)`)")
	quoted   = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'|` + "`([^`]*)`")
	// loads is a module a test loads without import … from: require, import(), vi.mock, a bare import
	loads     = regexp.MustCompile(`\b(?:require|import|mock|doMock)\(\s*['"]([^'"]+)['"]|\bimport\s+['"]([^'"]+)['"]`)
	dotted    = regexp.MustCompile(`^[A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*$`)
	indexRe   = regexp.MustCompile(`(?m)^index ([0-9a-f]+)\.\.`)
	jsExt     = map[string]bool{".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".mjs": true, ".cjs": true, ".mts": true, ".cts": true}
	aliases   = []string{"~/", "@/", "#/"}
	tooling   = regexp.MustCompile(`(^|/)(\.github|\.gitlab|\.husky|\.vscode|\.devcontainer|\.changeset)/|(^|/)(\.gitlab-ci\.ya?ml|Jenkinsfile|CODEOWNERS|Makefile|justfile|biome\.jsonc?|tsconfig[^/]*\.json|jsconfig\.json|renovate\.json5?|lefthook\.ya?ml|\.pre-commit-config\.ya?ml|\.golangci\.ya?ml|\.editorconfig|\.[a-z]+ignore|\.gitattributes|\.[^/.]+rc(\.[a-z]+)?)$`)
	shortName = regexp.MustCompile(`^[\d_.-]*$`)
)

// hit is a line git grep found: "path:line" and its text.
type hit struct {
	Path string
	Line int
	Text string
}

func (h hit) at() string { return h.Path + ":" + strconv.Itoa(h.Line) }

// search is git grep at the head with args, reading at most limit lines; cut: it stopped there, ran out of time or failed.
type search func(limit int, args ...string) ([]hit, bool)

// grepHead runs git grep in sb's checkout, the head's worktree. Its args come from the diff's paths and names, after
// -e or as fixed strings: never an option.
func grepHead(ctx context.Context, sb *proc.Sandbox, limit int, args ...string) ([]hit, bool) {
	if ctx.Err() != nil {
		return nil, true
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := sb.Command(ctx, sb.Repo(), nil, "git", append([]string{"-c", "core.quotepath=off", "grep", "-n", "-z", "-I", "--no-color"}, args...)...)
	pipe, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return nil, true
	}
	var hits []hit
	cut := false
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if len(hits) == limit {
			cut = true
			break
		}
		f := strings.SplitN(sc.Text(), "\x00", 3)
		if len(f) != 3 {
			continue
		}
		n, _ := strconv.Atoi(f[1])
		hits = append(hits, hit{f[0], n, f[2]})
	}
	if cut = cut || sc.Err() != nil; cut { // a line past 1 MiB, or enough read: stop it
		cancel()
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	return hits, cut || err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) // 1: nothing found
}

// Static fills what the page takes from the diff, git and path rules, without a model: TestChanges, Kinds, Env,
// Migrations and Excerpts. entered is the files the review's parts held. Its searches share staticTime, Before you
// merge's first; Excerpts has its own excerptTime.
func Static(ctx context.Context, sb *proc.Sandbox, d *Data, entered []string) {
	sctx, cancel := context.WithTimeout(ctx, staticTime)
	defer cancel()
	g := func(limit int, args ...string) ([]hit, bool) { return grepHead(sctx, sb, limit, args...) }
	d.Env = envVars(g, d.Diff)
	d.Migrations = migrations(g, d.Diff)
	d.Schema = schema(*d, func(names []string) sdb { return baseSchema(sctx, sb, names) })
	d.TestChanges = testChanges(g, d.Diff, d.Outline)
	d.Kinds = fileKinds(g, sb.Repo(), *d, entered)
	ectx, cancel := context.WithTimeout(ctx, excerptTime)
	defer cancel()
	d.Excerpts = Excerpts(ectx, sb, *d, spans(*d))
}

// code is whether p is code a search counts: no doc, no test.
func code(p string) bool { return diff.Kind(p) != "doc" && !isTest(p) }

func isTest(p string) bool { return checks.IsTest(p) || diff.Kind(p) == "test" }

// baseLines is a deleted file's text on the base, from its diff.
func baseLines(f string) []string {
	_, body, ok := strings.Cut(f, "\n@@")
	if !ok {
		return nil
	}
	var ls []string
	for l := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(l, "-") {
			ls = append(ls, l[1:])
		}
	}
	return ls
}

// cases is the tests of a test file's lines: JS blocks (an .each table once), Go and Python test functions.
func cases(ls []string) []TestCase {
	var out []TestCase
	for i, l := range ls {
		title := ""
		if m := jsBlock.FindStringSubmatchIndex(l); m != nil {
			rest := l[m[1]:]
			if strings.Contains(l[m[2]:m[3]], ".each") { // the title follows the table: it.each([…])("title")
				_, rest, _ = strings.Cut(rest, ")(")
			}
			if q := quoted.FindStringSubmatch(rest); q != nil {
				title = cmp.Or(q[1], q[2], q[3])
			}
		} else if m := goTestFn.FindStringSubmatch(l); m != nil {
			title = m[1]
		} else if m := pyTestFn.FindStringSubmatch(l); m != nil {
			title = m[1]
		} else {
			continue
		}
		out = append(out, TestCase{title, i + 1})
	}
	return out
}

// noExt is p without a JS or TS extension.
func noExt(p string) string {
	if jsExt[path.Ext(p)] {
		return strings.TrimSuffix(p, path.Ext(p))
	}
	return p
}

// resolve is the file of mods (by path without extension) that spec, imported in from, names: relative, or an alias
// (~/, @/, #/) or a rooted path by its tail. "" for a package or none.
func resolve(from, spec string, mods map[string]string) string {
	spec = noExt(spec)
	if strings.HasPrefix(spec, ".") {
		p := path.Join(path.Dir(from), spec)
		return cmp.Or(mods[p], mods[p+"/index"])
	}
	tail := spec
	for _, a := range aliases {
		tail = strings.TrimPrefix(tail, a)
	}
	if tail == spec && !strings.HasPrefix(spec, "src/") { // a package: "react", "@tanstack/query"
		return ""
	}
	for _, k := range slices.Sorted(maps.Keys(mods)) {
		if k == tail || strings.HasSuffix(k, "/"+tail) || k == tail+"/index" || strings.HasSuffix(k, "/"+tail+"/index") {
			return mods[k]
		}
	}
	return ""
}

// local is whether spec, imported in from, is the repo's own code, no test helper: relative or an alias.
func local(from, spec string) bool {
	p := spec
	if strings.HasPrefix(spec, ".") {
		p = path.Join(path.Dir(from), spec)
	} else {
		for _, a := range aliases {
			p = strings.TrimPrefix(p, a)
		}
		if p == spec {
			return false
		}
	}
	return !isTest(noExt(p) + ".ts")
}

// testedNames is what a deleted test file tests: its describe titles' last names, else the names it imports from the
// repo's code, and for Go the names its test functions are named after (TestTotalNet: TotalNet). Sorted.
func testedNames(p string, ls []string) []string {
	set := map[string]bool{}
	for _, l := range ls {
		if m := describe.FindStringSubmatch(l); m != nil {
			if t := cmp.Or(m[1], m[2], m[3]); dotted.MatchString(t) {
				set[t[strings.LastIndexByte(t, '.')+1:]] = true
			}
		}
		if m := goTestFn.FindStringSubmatch(l); m != nil {
			if n, _, _ := strings.Cut(strings.TrimPrefix(m[1], "Test"), "_"); len(n) >= 3 {
				set[n] = true
			}
		}
	}
	if len(set) == 0 {
		for _, m := range reexport.FindAllStringSubmatch(strings.Join(ls, "\n"), -1) {
			names := strings.TrimSpace(m[2])
			if m[1] != "import" || strings.HasPrefix(names, "type ") || !local(p, m[3]) {
				continue
			}
			_, list, ok := strings.Cut(names, "{")
			list, _, _ = strings.Cut(list, "}")
			for n := range strings.SplitSeq(list, ",") {
				n, _, _ = strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(n), "type ")), " ")
				if ok && dotted.MatchString(n) && !strings.Contains(n, ".") {
					set[n] = true
				}
			}
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// testChanges sorts the deleted test files into groups 1 and 2 and the weakened ones that stay into group 3.
func testChanges(g search, raw string, o diff.Outline) TestChanges {
	var tc TestChanges
	gone := map[string]string{} // the deleted files that aren't tests, by path without extension
	deleted := map[string]bool{}
	type del struct {
		path string
		ls   []string
	}
	var tests []del
	diff.EachDeleted(raw, func(p, f string) {
		deleted[p] = true
		if isTest(p) {
			if len(cases(baseLines(f))) > 0 { // a helper or fixture deletes no test
				tests = append(tests, del{p, baseLines(f)})
			}
			return
		}
		gone[noExt(p)] = p
	})
	searched := 0
	for _, t := range tests {
		tf := TestFile{Path: t.path, Tests: cases(t.ls)}
		text := strings.Join(t.ls, "\n")
		var specs []string
		for _, m := range reexport.FindAllStringSubmatch(text, -1) {
			specs = append(specs, m[3])
		}
		for _, m := range loads.FindAllStringSubmatch(text, -1) {
			specs = append(specs, cmp.Or(m[1], m[2]))
		}
		for _, s := range specs {
			tf.Imports = cmp.Or(tf.Imports, resolve(t.path, s, gone))
		}
		if strings.HasSuffix(t.path, "_test.go") { // a Go test imports its package: its own file, deleted with it
			tf.Imports = cmp.Or(tf.Imports, gone[strings.TrimSuffix(t.path, "_test.go")+".go"])
		}
		if tf.Imports != "" {
			tc.With = append(tc.With, tf)
			continue
		}
		tf.Names = testedNames(t.path, t.ls)
		if len(tf.Names) == 0 || searched == maxSearches {
			tc.Unsorted = append(tc.Unsorted, tf)
			continue
		}
		searched++
		args := []string{"-w", "-F"}
		if strings.HasSuffix(t.path, ".go") {
			args = append(args, "-i") // TestTotalNet tests totalNet
		}
		for _, n := range tf.Names {
			args = append(args, "-e", n)
		}
		hits, cut := g(maxHits, args...)
		for _, h := range hits {
			if code(h.Path) && len(tf.Found) < maxFound {
				tf.Found = append(tf.Found, h.at())
			}
		}
		switch {
		case len(tf.Found) > 0:
			tc.Kept = append(tc.Kept, tf)
		case cut: // the code that names it may lie past what the search read
			tc.Unsorted = append(tc.Unsorted, tf)
		default:
			tc.With = append(tc.With, tf)
		}
	}
	at := map[string]int{}
	for _, w := range o.Weakened {
		p, _, _ := strings.Cut(w.At, ":")
		if deleted[p] {
			continue
		}
		i, ok := at[p]
		if !ok {
			i, at[p] = len(tc.Stay), len(tc.Stay)
			tc.Stay = append(tc.Stay, TestStay{Path: p})
		}
		tc.Stay[i].Flags = append(tc.Stay[i].Flags, w)
	}
	slices.SortStableFunc(tc.Stay, func(a, b TestStay) int { return len(b.Flags) - len(a.Flags) })
	return tc
}

// lines counts a file diff's added and removed lines.
func lines(f string) (add, del int) {
	_, body, _ := strings.Cut(f, "\n@@")
	for l := range strings.SplitSeq(body, "\n") {
		switch {
		case strings.HasPrefix(l, "+"):
			add++
		case strings.HasPrefix(l, "-"):
			del++
		}
	}
	return add, del
}

// fileKinds sorts the diff's files by kind (spec 5.10). A deleted file is "gone" only when a search at the head finds no
// code that names it (its name without extension; an index file's folder's); a deleted test file stays a test.
func fileKinds(g search, root string, d Data, entered []string) []FileKind {
	ks := []FileKind{{Kind: "code"}, {Kind: "tests"}, {Kind: "tooling"}, {Kind: "gone"}}
	read := map[string]bool{}
	for _, p := range entered {
		read[p] = !slices.Contains(d.Omitted, p) && !slices.Contains(d.Failed, p)
	}
	kindOf := func(p string) int {
		switch {
		case isTest(p):
			return 1
		case diff.Kind(p) == "doc" || tooling.MatchString(p) || toolConfig(p) || codeConfig.MatchString(p):
			return 2
		}
		return 0
	}
	add := func(k int, p, f string) {
		a, r := lines(f)
		ks[k].Files, ks[k].Add, ks[k].Del = append(ks[k].Files, p), ks[k].Add+a, ks[k].Del+r
		if read[p] {
			ks[k].Read++
		}
	}
	diff.EachFile(d.Diff, func(p, f string) {
		if diff.Skip.MatchString(p) || diff.Generated(root, p, f) {
			ks[3].Generated++
			add(3, p, f)
			return
		}
		add(kindOf(p), p, f)
	})
	searched := 0
	diff.EachDeleted(d.Diff, func(p, f string) {
		k := kindOf(p)
		if isTest(p) { // a fixture under tests/ goes as other deleted files do
			ks[1].Deleted++
			add(1, p, f)
			return
		}
		name := strings.TrimSuffix(path.Base(p), path.Ext(p))
		if name == "index" {
			name = path.Base(path.Dir(p))
		}
		if diff.Skip.MatchString(p) {
			ks[3].Generated++
			add(3, p, f)
			return
		}
		if searched == maxSearches || shortName.MatchString(name) {
			ks[k].Cut = true
			add(k, p, f)
			return
		}
		searched++
		hits, cut := g(maxHits, "-w", "-F", "-e", name)
		named, docs := false, false
		for _, h := range hits {
			named, docs = named || code(h.Path), docs || diff.Kind(h.Path) == "doc"
		}
		if named || cut {
			ks[k].Cut = ks[k].Cut || cut && !named
			add(k, p, f)
			return
		}
		ks[3].Deleted++
		if docs {
			ks[3].Named++
		}
		add(3, p, f)
	})
	return ks
}

// envVars is the environment variables the diff starts or stops reading (risks' Environment) and their readers at the
// head, as Before you merge needs them: a git grep for each name, at most maxRisks*2 of them.
func envVars(g search, raw string) map[string]EnvVar {
	n := names{env: map[string]*[2]string{}, defaults: map[string]bool{}, deps: map[string]*lib{}}
	scan := func(p, f string) {
		if !isTest(p) && !diff.Skip.MatchString(p) {
			n.scan(parse(p, f, ""), nil)
		}
	}
	diff.EachFile(raw, scan)
	diff.EachDeleted(raw, scan)
	out := map[string]EnvVar{}
	its := n.vars(nil)
	unread := slices.DeleteFunc(slices.Clone(its), func(it riskItem) bool { return it.Note != "no longer read" })
	for _, it := range slices.Concat(unread, its) { // the ones no longer read first: they need the head's word
		if _, ok := out[it.What]; ok {
			continue
		}
		if len(out) == 2*maxRisks {
			break
		}
		name := it.What
		q := regexp.QuoteMeta(name)
		reads := regexp.MustCompile(`(?i:\w*env\w*)(?:\.` + q + `\b|\[\s*["']` + q + `["'])`)
		hits, cut := g(maxHits, "-w", "-F", "-e", name)
		var e EnvVar
		for _, h := range hits {
			if !code(h.Path) || envFile.MatchString(h.Path) { // an env example names it, nothing reads it there
				continue
			}
			read := reads.MatchString(h.Text)
			for _, m := range envRead.FindAllStringSubmatch(h.Text, -1) {
				read = read || cmp.Or(m[1:]...) == name
			}
			switch {
			case read && len(e.Reads) < maxPlaces:
				e.Reads = append(e.Reads, h.at())
			case !read && len(e.Named) < maxPlaces:
				e.Named = append(e.Named, h.at())
			}
		}
		e.Cut = cut
		out[name] = e
	}
	return out
}

// migrations is the migrations the diff adds or changes, with the docs and tests at the head that name them by their
// file's name without extension.
func migrations(g search, raw string) []Migration {
	var out []Migration
	diff.EachFile(raw, func(p, _ string) {
		if !migration.MatchString(p) || diff.Skip.MatchString(p) || isTest(p) || diff.Kind(p) == "doc" || len(out) == maxRisks {
			return
		}
		m := Migration{Path: p}
		name := path.Base(p)
		name = strings.TrimSuffix(name, path.Ext(name))
		if len(name) < 6 || shortName.MatchString(name) || !strings.ContainsAny(name, "0123456789") { // "001" and "schema" name too much
			m.Cut = true
			out = append(out, m)
			return
		}
		hits, cut := g(maxHits, "-F", "-e", name)
		for _, h := range hits {
			switch {
			case h.Path == p:
			case isTest(h.Path) && len(m.Tests) < maxPlaces:
				m.Tests = append(m.Tests, h.at())
			case diff.Kind(h.Path) == "doc" && len(m.Docs) < maxPlaces:
				m.Docs = append(m.Docs, h.at())
			}
		}
		m.Cut = cut
		out = append(out, m)
	})
	return out
}

// spans is the places of d's static data and findings in files Sources doesn't hold, for Excerpts.
func spans(d Data) []Span {
	var out []Span
	add := func(at string) {
		p, n, ok := strings.Cut(at, ":")
		if l, err := strconv.Atoi(n); ok && err == nil && d.Sources[p] == "" {
			out = append(out, Span{Path: p, From: l, To: l})
		}
	}
	for _, c := range d.Findings {
		if c.Line > 0 {
			add(c.Path + ":" + strconv.Itoa(c.Line))
		}
	}
	for _, t := range d.TestChanges.Kept {
		for _, at := range t.Found {
			add(at)
		}
	}
	for _, e := range d.Env {
		for _, at := range slices.Concat(e.Reads, e.Named) {
			add(at)
		}
	}
	for _, m := range d.Migrations {
		for _, at := range slices.Concat(m.Docs, m.Tests) {
			add(at)
		}
	}
	return out
}

// Excerpts reads the lines of spans, excerptPad more on each side, merged per file: at the head from sb's checkout
// for a file the PR doesn't delete, on the base from the blob the diff names for a file it changes. A file past
// maxSource, a span out of order or longer than maxSpan (a model may name it) and what exceeds maxSources with Sources
// counted stay out.
func Excerpts(ctx context.Context, sb *proc.Sandbox, d Data, want []Span) []Excerpt {
	if len(want) == 0 {
		return nil
	}
	room := maxSources
	for _, s := range d.Sources {
		room -= len(s)
	}
	blobs := map[string]string{} // path -> its blob on the base
	gone := map[string]bool{}
	read := func(p, f string) {
		if m := indexRe.FindStringSubmatch(f); m != nil && strings.Trim(m[1], "0") != "" {
			blobs[p] = m[1]
		}
	}
	diff.EachFile(d.Diff, read)
	diff.EachDeleted(d.Diff, func(p, f string) { read(p, f); gone[p] = true })
	type key struct {
		path string
		base bool
	}
	by := map[key][][2]int{}
	var order []key
	for _, s := range want {
		k := key{s.Path, s.Base}
		if s.Base && blobs[s.Path] == "" || !s.Base && gone[s.Path] || s.From < 1 || s.To < s.From || s.To-s.From > maxSpan {
			continue
		}
		if by[k] == nil {
			order = append(order, k)
		}
		by[k] = append(by[k], [2]int{max(1, s.From-excerptPad), s.To + excerptPad})
	}
	var specs []string
	for _, k := range order {
		if k.base {
			specs = append(specs, blobs[k.path])
		}
	}
	base := catFiles(ctx, sb, specs)
	root, err := sb.Pinned()
	if err == nil {
		defer root.Close()
	}
	var out []Excerpt
	for _, k := range order {
		var text string
		if k.base {
			text = base[blobs[k.path]]
		} else if err == nil {
			if fi, e := root.Lstat(filepath.FromSlash(k.path)); e == nil && fi.Mode().IsRegular() {
				b, _ := diff.ReadIn(root, filepath.FromSlash(k.path), maxSource)
				text = string(b)
			}
		}
		if text == "" || len(text) > maxSource || strings.IndexByte(text, 0) >= 0 {
			continue
		}
		ls := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		rs := by[k]
		slices.SortFunc(rs, func(a, b [2]int) int { return a[0] - b[0] })
		for i := 0; i < len(rs); i++ {
			from, to := rs[i][0], rs[i][1]
			for i+1 < len(rs) && rs[i+1][0] <= to+1 { // overlapping or touching: one excerpt
				i++
				to = max(to, rs[i][1])
			}
			if from > len(ls) {
				continue
			}
			e := Excerpt{Path: k.path, Base: k.base, From: from}
			for _, l := range ls[from-1 : min(to, len(ls))] {
				if len(l) > maxLineLen {
					l = l[:maxLineLen]
				}
				e.Lines = append(e.Lines, l)
				room -= len(l) + 1
			}
			if room < 0 {
				return out
			}
			out = append(out, e)
		}
	}
	return out
}
