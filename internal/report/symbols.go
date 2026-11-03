package report

import (
	"bufio"
	"bytes"
	"diffharbor/internal/diff"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Symbol is a declaration the page explains when a name in the diff is hovered: what it is, where it lives,
// what names it elsewhere.
type Symbol struct {
	Kind  string   `json:"kind"`          // func, method, type, field, const, var; other languages: the keyword
	Pkg   string   `json:"pkg,omitempty"` // Go: the package, to tell pkg.Name apart
	Sig   string   `json:"sig"`           // the declaration without its body
	Doc   string   `json:"doc,omitempty"`
	Path  string   `json:"path"`
	Line  int      `json:"line"`
	Refs  []Ref    `json:"refs,omitempty"`  // the first maxRefs places that name it
	Uses  int      `json:"uses"`            // all of them
	Files int      `json:"files"`           // in how many files
	Here  []string `json:"here,omitempty"`  // exact: the lines of the diff's files that name it, path:line
	Exact bool     `json:"exact,omitempty"` // its uses are codegraph's, resolved; else lines that name it
	Calls []string `json:"-"`               // exact: the declarations it names, path NUL name: the page's graphs
	// exact: the first maxRefs places outside the diff's files that name it, and how many: what the change reaches
	Outside  []Ref `json:"-"`
	NOutside int   `json:"-"`
}

// Ref is a line that names a symbol, and the declaration it sits in.
type Ref struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	In   string `json:"in,omitempty"`
	Text string `json:"text"` // the line, trimmed and cut
}

const (
	maxRefs, maxSymbols = 20, 3000
	maxFiles, maxSize   = 20000, 512 << 10
	maxSig, maxDoc      = 1500, 800
	maxText, maxCalls   = 120, 80
)

var (
	ident = regexp.MustCompile(`[A-Za-z_$][\w$]+`)
	// decl is a declaration at the start of a line, its name in group 1: Go, TypeScript, Python, Rust, Kotlin …
	decl    = regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:pub(?:\([\w:]+\))?\s+)?(?:async\s+)?(func|function\*?|def|class|type|interface|enum|struct|trait|impl|fn|const|let|var)\s+(?:\([^)]*\)\s*)?([A-Za-z_$][\w$]*)`)
	codeExt = map[string]bool{
		".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
		".py": true, ".rs": true, ".java": true, ".kt": true, ".swift": true, ".rb": true, ".php": true, ".cs": true,
	}
)

// Symbols indexes the checkout at repo for the names the diff shows: their declarations (Go parsed, other languages
// by the line that declares them) and who names them: codegraph's resolved edges where the review indexed the
// checkout, else the lines that name them, by name.
func Symbols(repo, diffText string) map[string][]Symbol {
	wanted := map[string]bool{}
	for l := range strings.SplitSeq(diffText, "\n") {
		if strings.HasPrefix(l, "@@") { // the declaration a hunk is in
			l = " " + l[strings.LastIndex(l, "@@")+2:]
		}
		if l != "" && strings.ContainsRune("+- ", rune(l[0])) && !strings.HasPrefix(l, "+++") && !strings.HasPrefix(l, "---") {
			for _, w := range ident.FindAllString(l[1:], -1) {
				wanted[w] = true
			}
		}
	}
	syms := map[string][]Symbol{}
	ms := mentions{refs: map[string][]Ref{}, uses: map[string]int{}, files: map[string]map[string]bool{}}
	n := 0
	_ = filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable directory: index the rest
		}
		if d.IsDir() {
			if p != repo && (strings.HasPrefix(d.Name(), ".") || skipDir[d.Name()]) {
				return filepath.SkipDir
			}
			return nil
		}
		if !codeExt[filepath.Ext(p)] || n >= maxFiles {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxSize {
			return nil //nolint:nilerr // left out, like a large file
		}
		src, err := os.ReadFile(p) //nolint:gosec // a file of the checkout
		if err != nil {
			return nil //nolint:nilerr // the same
		}
		n++
		rel, _ := filepath.Rel(repo, p)
		rel = filepath.ToSlash(rel)
		if filepath.Ext(p) == ".go" {
			goDecls(rel, src, wanted, syms)
		} else {
			lineDecls(rel, src, wanted, syms)
		}
		ms.scan(rel, src, wanted)
		return nil
	})
	total := 0
	for name, ss := range syms {
		own := map[Ref]bool{}
		for _, s := range ss {
			own[Ref{Path: s.Path, Line: s.Line}] = true
		}
		var rs []Ref
		for _, r := range ms.refs[name] {
			if !own[Ref{Path: r.Path, Line: r.Line}] && len(rs) < maxRefs {
				rs = append(rs, r)
			}
		}
		for i := range ss {
			ss[i].Refs, ss[i].Uses, ss[i].Files = rs, max(ms.uses[name]-len(ss), 0), len(ms.files[name])
		}
		if total += len(ss); total > maxSymbols {
			delete(syms, name)
		}
	}
	paths := diff.Paths(diffText)
	if idx := codegraph(repo, wanted, paths); idx != nil {
		changed := map[string]bool{}
		for _, p := range paths {
			changed[p] = true
		}
		idx.exact(repo, changed, syms)
	}
	return syms
}

// mentions is the lines that name what the diff shows, by name: the first few, how many, in which files.
type mentions struct {
	refs  map[string][]Ref
	uses  map[string]int
	files map[string]map[string]bool
}

// scan adds a file's lines that name a wanted name, comments aside, each with the declaration it sits in.
func (ms mentions) scan(path string, src []byte, wanted map[string]bool) {
	in := ""
	sc := bufio.NewScanner(bytes.NewReader(src))
	sc.Buffer(nil, maxSize)
	for i := 1; sc.Scan(); i++ {
		l := sc.Text()
		if m := decl.FindStringSubmatch(l); m != nil {
			in = m[2]
		}
		if commentLine.MatchString(strings.TrimSpace(l)) {
			continue
		}
		for _, w := range ident.FindAllString(l, -1) {
			if !wanted[w] {
				continue
			}
			if len(ms.refs[w]) < maxRefs*2 { // room for the declarations' own lines, dropped later
				ms.refs[w] = append(ms.refs[w], Ref{path, i, in, cut(strings.TrimSpace(l), maxText)})
			}
			ms.uses[w]++
			if ms.files[w] == nil {
				ms.files[w] = map[string]bool{}
			}
			ms.files[w][path] = true
		}
	}
}

var skipDir = map[string]bool{"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, "out": true, "testdata": true}

// goDecls adds the package-level declarations of a Go file, and the fields and methods of its types.
func goDecls(path string, src []byte, wanted map[string]bool, syms map[string][]Symbol) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return
	}
	pkg := f.Name.Name
	add := func(name *ast.Ident, kind string, sig ast.Node, prefix string, docs ...*ast.CommentGroup) {
		if !wanted[name.Name] {
			return
		}
		var b strings.Builder
		b.WriteString(prefix)
		_ = printer.Fprint(&b, fset, sig)
		doc := ""
		for _, d := range docs {
			if doc = strings.TrimSpace(d.Text()); doc != "" {
				break
			}
		}
		syms[name.Name] = append(syms[name.Name], Symbol{
			Kind: kind, Pkg: pkg, Sig: cut(b.String(), maxSig), Doc: cut(doc, maxDoc),
			Path: path, Line: fset.Position(name.Pos()).Line,
		})
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			kind := "func"
			if d.Recv != nil {
				kind = "method"
			}
			add(d.Name, kind, &ast.FuncDecl{Recv: d.Recv, Name: d.Name, Type: d.Type}, "", d.Doc)
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					add(s.Name, "type", s, "type ", s.Doc, d.Doc)
					var fields *ast.FieldList
					of := "field of "
					switch t := s.Type.(type) {
					case *ast.StructType:
						fields = t.Fields
					case *ast.InterfaceType:
						fields, of = t.Methods, "method of "
					}
					if fields == nil {
						continue
					}
					for _, fl := range fields.List {
						for _, n := range fl.Names {
							add(n, of+s.Name.Name, fl.Type, n.Name+" ", fl.Doc, fl.Comment)
						}
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						add(n, d.Tok.String(), s, d.Tok.String()+" ", s.Doc, s.Comment, d.Doc)
					}
				}
			}
		}
	}
}

// lineDecls adds what a line at the start of a file of another language declares, with the comment above it as
// its doc and, for a block that only declares (an interface, a type), the block.
func lineDecls(path string, src []byte, wanted map[string]bool, syms map[string][]Symbol) {
	ls := strings.Split(string(src), "\n")
	for i, l := range ls {
		m := decl.FindStringSubmatch(l)
		if m == nil || !wanted[m[2]] {
			continue
		}
		sig := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(l), "{"))
		if strings.HasSuffix(strings.TrimSpace(l), "{") && typeDecl.MatchString(l) {
			end := i + 1
			for end < len(ls) && end-i < 25 && !strings.HasPrefix(ls[end], "}") {
				end++
			}
			sig = strings.Join(ls[i:min(end+1, len(ls))], "\n")
		}
		var doc []string
		for j := i - 1; j >= 0; j-- {
			t := strings.TrimSpace(ls[j])
			if !commentLine.MatchString(t) && !strings.HasPrefix(t, `"""`) {
				break
			}
			doc = append([]string{strings.TrimSpace(strings.TrimSuffix(strings.TrimLeft(t, "/*#-\" "), "*/"))}, doc...)
		}
		syms[m[2]] = append(syms[m[2]], Symbol{
			Kind: strings.TrimSuffix(m[1], "*"), Sig: cut(sig, maxSig),
			Doc: cut(strings.TrimSpace(strings.Join(doc, "\n")), maxDoc), Path: path, Line: i + 1,
		})
	}
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + " …"
}
