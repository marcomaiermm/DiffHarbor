package report

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"diffharbor/internal/checks"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"fmt"
	"io"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Map is the map of the change: its parts, the lines between them, where they sit, what lies on them. All of it is
// computed here, nothing is left to the page but drawing it; the routes of the lines, the ports on a box's edge and
// the tracks in a channel are the page's, from the Col and Row of what a line joins. nil when it has fewer than two
// parts and no outside system: there is no map to draw.
type Map struct {
	Parts     []MapPart // folder order, "Other" last
	Order     []string  // part IDs in map order: by column, then row; parts with no line after them, in folder order
	Removed   *MapBox   // the folders whose changed code files are all deleted
	Generated *MapBox
	Lines     []MapLine
	Systems   []MapSystem // outside systems, column 0
	Deploy    []MapDeploy // what a merge alone doesn't finish
	Off       []MapItem   // items with no box: a test, a data file
	Reach     MapReach
	Tables    []MapTable // the tables a migration drops
	Stores    []MapStore // the tables the change writes and reads, writers first (spec 6.13)
	Trace     *MapTrace  // one path from an entry to an outside call; nil without one
	Cols      int        // the highest column; the parts start at 1 when there are systems at 0
	NoGraph   bool       // no code graph: the parts sit in one row, without lines
	NoKinds   bool       // the index told no kind of any use: every use is a call
}

// MapPart is a folder of the PR's code. Col and Row place it; Alone: no line reaches it, it sits in the row
// "No line to other parts".
type MapPart struct {
	ID, Label, Path string // Path is its folder, "." the top level; ID is Path, otherID for the rest
	Files           []string
	Deleted         int  // its code files the PR deletes
	New             bool // every file is new
	Folders         int  // "Other": the folders it joins
	Col, Row        int
	Alone           bool
	Items           []MapItem
	Unread          int  // files the review left out or failed on
	Reach, Tests    int  // places outside the PR that name its changed code: in code, in tests
	DB              bool // its files used a table this PR drops
}

// MapBox is the Removed and the Generated box.
type MapBox struct {
	Files   []string    // Generated: its files
	N       int         // files
	Folders []MapFolder // Removed
	Items   []MapItem
}

// MapFolder is a folder of the Removed box and its deleted files.
type MapFolder struct {
	Path string
	N    int
}

// MapItem is something that needs a person, sitting on a box: Kind "finding", "risk" or "pr".
type MapItem struct {
	Kind, Title, Path string
	Line              int
	Sev               string
}

// MapLine is the use of one part's changed code by another: it runs from the part that declares to the part that
// uses. Names are what From declares and To uses, Back the other way when TwoWay. A store's reads and writes are
// no use here: Map.Stores carries them.
type MapLine struct {
	From, To string
	Names    []MapName
	Back     []MapName
	TwoWay   bool
	// Calls, Through (calls through an interface) and Named (uses without a call) count both ways.
	Calls, Through, Named int
	Weight                int      // Calls + Through
	Width                 int      // the line's px: 1 for under 3 calls, 2 for under 10, else 3
	Style                 string   // "solid": a direct call; "dashed": through an interface or named
	Chevron               string   // where the arrow heads sit, the way data goes: "to" (at To), "from", "both", "" for names alone
	Via                   []string // the interfaces its calls go through
}

// MapName is a declaration a line carries and how it is used, N times, first at At (path:line). Kind: "calls",
// "through" (Via the interface), "named".
type MapName struct {
	Name, Kind, Via, At string
	N                   int
	// calls: the tables its code writes, itself or within 3 calls, the call into it the first: the call sends data to
	// it, so the line's chevron sits at its end (decision 27)
	Writes []string
}

// OutsideCall is a call out of the repo that code names (Outside): the system, the line of changed code that sends
// the request or calls what does, and the URL's literal or the environment variable it is read from, with its place.
type OutsideCall struct {
	System, Path string
	Line         int
	URL, URLAt   string
	Calls        []StoreCall `json:",omitempty"` // the calls from the changed declaration down to the sending one, as StorePath.Calls
}

// MapSystem is an outside system and the parts that hold its calls, a dotted line to each.
type MapSystem struct {
	Name     string
	Calls    []OutsideCall
	Parts    []string
	Col, Row int
}

// MapDeploy is a box in the row "Before merging": Kind "migration", "environment", "dependencies", "ci",
// "configuration", "prs", "docs"; N the items on it.
type MapDeploy struct {
	Kind, Label, Path string
	N                 int
	Items             []MapItem
	Drops             []string // a migration: the tables it drops
}

// MapReach is what code outside the PR names of the changed declarations: Places in code, Tests the places in tests
// left out, Decls the declarations with a place in code. List keeps at most maxAffected of them, More the rest.
type MapReach struct {
	Places, Tests, Decls, More int
	List                       []MapReachDecl
}

// MapReachDecl is a changed declaration that code outside the PR names.
type MapReachDecl struct {
	Part, Name, Path string
	Line, N          int   // N places in code
	Places           []Ref // the first of them
}

// MapTable is a table a migration drops, at Path:Line. Parts are those whose code files imported its declaration
// at the base, Tests the test files that did, Elsewhere the files in no part. Imports and Named are what the head
// still has: code that imports it, code that names it in SQL (path:line).
type MapTable struct {
	Name, Path, Decl string
	Line             int
	Parts            []MapTablePart
	Tests, Elsewhere int
	Imports, Named   []string
	Cut              bool // its importers' search stopped at its file cap or in time, or was past maxDrops: the counts are partial
}

// MapStore is a table the changed code writes or reads, one box however many names the code gives it. Path and
// Line are its declaration, "" when the code only names it (SQL, Table("…")); Names its other names, each with
// its place. Subquery: the declaration is a subquery, whose own tables the scan doesn't follow. Migrations are the
// PR's migrations that create, change or drop it.
type MapStore struct {
	Name, Path string
	Line       int
	Names      []MapStoreName
	Subquery   bool
	Parts      []MapStorePart // in map order
	Migrations []MapStoreMigration
}

// MapStoreName is another name of a table: an alias at its declaration, the name SQL gives it at its first query.
type MapStoreName struct{ Name, At string }

// MapStorePart is how a part uses a table: each use is a declaration that holds the query, or a call whose
// callee reaches one.
type MapStorePart struct {
	Part string
	Uses []MapStoreUse
}

// MapStoreUse is one use: Kind "reads" or "writes"; Decl the declaration that holds the query, or the callee of a
// call that reaches it (Through); At its first place (path:line), N how many.
type MapStoreUse struct {
	Kind, Decl, At string
	Through        bool
	N              int
}

// MapStoreMigration is a migration of the PR that names the table: Kind "created", "changed" or "dropped", Lines
// its statements' lines.
type MapStoreMigration struct {
	Path, Label, Kind string
	Lines             []int
}

// MapTablePart is the files of a part that imported a dropped table.
type MapTablePart struct {
	Part  string
	Files []string
}

// MapTrace is the first way, in the report's order, that reaches a declaration holding an outside call: Rows from
// the entry down to that declaration, then the call.
type MapTrace struct {
	Method, Route string // the way's entry
	At            ref
	Rows          []wrow
	Call          OutsideCall
	Part          string // the part that holds the call
}

const (
	maxParts, maxDepth = 16, 3
	otherID            = "/other folders" // the part "Other": no repo path starts with "/", so no folder shares it
	mapCols            = "_($["
)

var mapExt = map[string]bool{".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true}

// toolConfig is vite.config.ts and the like: CI's, not the Configuration box's.
func toolConfig(p string) bool {
	return strings.Contains(path.Base(p), ".config.") && mapExt[path.Ext(p)]
}

// mapCode is a code file the map groups: tests, tool config (vite.config.ts goes to CI) and the rest aren't.
func mapCode(p string) bool {
	return mapExt[path.Ext(p)] && diff.Kind(p) == "code" && !toolConfig(p)
}

// folder is p's first n directories, "." at the top.
func folder(p string, n int) string {
	dir := path.Dir(p)
	if dir == "." {
		return dir
	}
	segs := strings.Split(dir, "/")
	return strings.Join(segs[:min(n, len(segs))], "/")
}

type mapper struct {
	d     Data
	v     view
	m     *Map
	part  map[string]int // a kept code file -> its part
	depth int
	other map[string]bool // the folders "Other" joins
	box   map[string]*MapDeploy
	gen   map[string]bool
	added map[string]bool
	drawn []drawnCall          // the calls the map draws: the store walk starts at them too
	wrote map[*Symbol][]string // the tables a declaration writes, for its callers' chevrons
}

// BuildMap is the map of d's change, with the outside calls (Outside). sb is the checkout: its
// generated headers, and git for the tables a migration drops; nil reads neither. Symbol.State is set as Render's
// page does, by prepare.
func BuildMap(sb *proc.Sandbox, d Data, outside []OutsideCall) *Map {
	b := &mapper{
		d: d, v: prepare(d), m: &Map{}, part: map[string]int{}, box: map[string]*MapDeploy{}, gen: map[string]bool{},
		added: diff.RightLines(d.Diff, false), other: map[string]bool{},
	}
	root := ""
	if sb != nil {
		root = sb.Repo()
	}
	b.parts(root)
	b.systems(outside)
	if len(b.m.Parts) < 2 && len(b.m.Systems) == 0 {
		return nil
	}
	b.lines()
	b.layout()
	b.orderStores()
	b.reach()
	b.tables(sb)
	b.deploy()
	b.trace()
	return b.m
}

// parts groups the PR's code files by folder (6.1): the first n levels, n the largest up to maxDepth that gives
// at most maxParts; past that at one level, the biggest stay and the rest are "Other".
func (b *mapper) parts(root string) {
	var kept, gone []string
	fresh := map[string]bool{}
	diff.EachFile(b.d.Diff, func(p, f string) {
		switch {
		case !mapCode(p):
		case diff.Skip.MatchString(p) || diff.Generated(root, p, f):
			b.gen[p] = true
		default:
			kept = append(kept, p)
			fresh[p] = strings.Contains(f, "\n--- /dev/null\n")
		}
	})
	diff.EachDeleted(b.d.Diff, func(p, _ string) {
		if mapCode(p) && !diff.Skip.MatchString(p) {
			gone = append(gone, p)
		}
	})
	b.depth = 1
	for n := maxDepth; n > 1; n-- {
		if len(foldersOf(kept, n)) <= maxParts {
			b.depth = n
			break
		}
	}
	groups := foldersOf(kept, b.depth)
	keys := slices.Sorted(maps.Keys(groups))
	if len(keys) > maxParts {
		slices.SortStableFunc(keys, func(x, y string) int { return cmp.Compare(len(groups[y]), len(groups[x])) }) // the biggest first
		for _, k := range keys[maxParts-1:] {
			b.other[k] = true
		}
		keys = slices.Sorted(slices.Values(keys[:maxParts-1]))
	}
	at := map[string]int{}
	for _, k := range keys {
		at[k] = len(b.m.Parts)
		b.m.Parts = append(b.m.Parts, MapPart{ID: k, Path: k, Files: groups[k]})
	}
	if len(b.other) > 0 {
		at[otherID] = len(b.m.Parts)
		o := MapPart{ID: otherID, Path: "other", Label: fmt.Sprintf("Other · %d folders", len(b.other)), Folders: len(b.other)}
		for k := range b.other {
			o.Files = append(o.Files, groups[k]...)
			at[k] = len(b.m.Parts)
		}
		slices.Sort(o.Files)
		b.m.Parts = append(b.m.Parts, o)
	}
	labels := map[string]int{}
	for i := range b.m.Parts {
		p := &b.m.Parts[i]
		if p.Label == "" {
			p.Label = partLabel(p.Path, false)
		}
		labels[p.Label]++
		p.New = len(p.Files) > 0
		for _, f := range p.Files {
			b.part[f], p.New = i, p.New && fresh[f]
		}
	}
	for i := range b.m.Parts {
		if p := &b.m.Parts[i]; labels[p.Label] > 1 && p.ID != otherID {
			p.Label = partLabel(p.Path, true)
		}
	}
	removed := map[string]int{}
	for _, f := range gone {
		k := folder(f, b.depth)
		if i, ok := at[k]; ok {
			b.m.Parts[i].Deleted++
		} else {
			removed[k]++
		}
	}
	if len(removed) > 0 {
		r := &MapBox{}
		for _, k := range slices.Sorted(maps.Keys(removed)) {
			r.Folders, r.N = append(r.Folders, MapFolder{k, removed[k]}), r.N+removed[k]
		}
		slices.SortStableFunc(r.Folders, func(x, y MapFolder) int { return cmp.Compare(y.N, x.N) })
		b.m.Removed = r
	}
	if len(b.gen) > 0 {
		b.m.Generated = &MapBox{Files: slices.Sorted(maps.Keys(b.gen)), N: len(b.gen)}
	}
	unread := map[string]bool{}
	for _, u := range slices.Concat(b.d.Failed, b.d.Omitted) {
		unread[u] = true
	}
	for i := range b.m.Parts {
		for _, f := range b.m.Parts[i].Files {
			if unread[f] {
				b.m.Parts[i].Unread++
			}
		}
	}
}

func foldersOf(files []string, n int) map[string][]string {
	g := map[string][]string{}
	for _, f := range files {
		k := folder(f, n)
		g[k] = append(g[k], f)
	}
	return g
}

// partLabel is a folder's last directory; a name that starts with _, (, $ or [ keeps its parent, and so does one
// that shares its label with another part.
func partLabel(key string, parent bool) string {
	if key == "." {
		return "top level"
	}
	segs := strings.Split(key, "/")
	last := segs[len(segs)-1]
	if len(segs) > 1 && (parent || strings.ContainsRune(mapCols, rune(last[0]))) {
		return segs[len(segs)-2] + "/" + last
	}
	return last
}

// stated is whether the PR adds or changes s: the page's State, or a method whose lines the diff adds to (the
// class methods the code graph knows have no State).
func (b *mapper) stated(s *Symbol) bool {
	if s.State != "" {
		return true
	}
	if s.Kind != "method" || s.To == 0 {
		return false
	}
	for l := s.From; l <= s.To; l++ {
		if b.added[s.Path+":"+strconv.Itoa(l)] {
			return true
		}
	}
	return false
}

// useKind is what a use of the index's kind is: a call (a JSX element too), a store's read or write, or a name.
func useKind(k string) string {
	switch k {
	case "", "calls", "renders":
		return "calls"
	case "reads", "writes":
		return k
	}
	return "named"
}

func kindAt(s *Symbol, i int) string {
	if i < len(s.HereKind) {
		return s.HereKind[i]
	}
	return ""
}

func placeOf(site string) string { return site[:max(strings.LastIndexByte(site, ':'), 0)] }

type use struct{ decl, user int }

type nameKey struct{ name, kind, via string }

// lines reads the uses of changed declarations between parts (6.2): a direct call, a call through an interface, a
// name used without one; and the tables the change writes and reads (6.13), whose writes move a call's chevron.
//
// Through an interface: the index ties an interface's method to the methods behind it with a use on the line where
// the interface declares it, and the callers of the interface's method are uses of the interface's. VTA, which
// resolves a call through an interface to the method it reaches, adds an edge into that method: a call. So a
// call site VTA resolved is a direct call, solid; the index's alone, tied to every method of the name, is a call
// through the interface, dashed.
func (b *mapper) lines() {
	syms := b.d.Symbols
	exact := false // the name scan alone says nothing of who uses what: it needs the index's edges
	for _, ss := range syms {
		exact = exact || slices.ContainsFunc(ss, func(s Symbol) bool { return s.Exact })
	}
	if !exact {
		b.m.NoGraph = true
		for i := range b.m.Parts {
			b.m.Parts[i].Col = i + 1
		}
		for i := range b.m.Systems {
			b.m.Systems[i].Row = i
		}
		b.m.Cols = len(b.m.Parts)
		b.m.Order = b.ids(nil)
		return
	}
	names := slices.Sorted(maps.Keys(syms))
	iface := map[string]string{} // where an interface declares a method -> the interface
	for _, n := range names {
		for _, s := range syms[n] {
			if t, ok := strings.CutPrefix(s.Kind, "method of "); ok {
				iface[s.Path+":"+strconv.Itoa(s.Line)] = t
			}
		}
	}
	used := usage{}
	behind, kinds := b.direct(used, names, iface)
	b.through(used, names, iface, behind)
	b.stores(names)
	for _, c := range b.drawn { // what the called declaration writes, not another of its name in the part
		if n := used[use{b.part[c.s.Path], c.up}][nameKey{c.name, "calls", ""}]; n != nil {
			n.Writes = append(n.Writes, b.wrote[c.s]...)
		}
	}
	for _, ns := range used {
		for _, n := range ns {
			slices.SortFunc(n.Writes, func(x, y string) int {
				return cmp.Or(cmp.Compare(strings.ToLower(x), strings.ToLower(y)), cmp.Compare(x, y))
			})
			n.Writes = slices.Compact(n.Writes)
		}
	}
	b.m.NoKinds = !kinds && len(used) > 0
	done := map[use]bool{}
	for _, u := range slices.SortedFunc(maps.Keys(used), func(x, y use) int { return cmp.Or(cmp.Compare(x.decl, y.decl), cmp.Compare(x.user, y.user)) }) {
		if done[u] {
			continue
		}
		done[u], done[use{u.user, u.decl}] = true, true
		l := MapLine{From: b.m.Parts[u.decl].ID, To: b.m.Parts[u.user].ID, Names: sortNames(used[u]), Back: sortNames(used[use{u.user, u.decl}])}
		l.TwoWay = len(l.Names) > 0 && len(l.Back) > 0
		l.count()
		b.m.Lines = append(b.m.Lines, l)
	}
}

// usage is the uses between two parts, by declaration and kind.
type usage map[use]map[nameKey]*MapName

func (us usage) add(decl, user int, name, kind, via, site string) {
	u := use{decl, user}
	if us[u] == nil {
		us[u] = map[nameKey]*MapName{}
	}
	k := nameKey{name, kind, via}
	if us[u][k] == nil {
		us[u][k] = &MapName{Name: name, Kind: kind, Via: via, At: site}
	}
	us[u][k].N++
}

// direct adds the uses of the declarations the PR changes in another part's code file; behind is the methods the index
// ties to each interface method's declaring line, kinds whether any use told its kind.
func (b *mapper) direct(used usage, names []string, iface map[string]string) (behind map[string][]*Symbol, kinds bool) {
	behind = map[string][]*Symbol{}
	for _, n := range names {
		for i := range b.d.Symbols[n] {
			s := &b.d.Symbols[n][i]
			dp, ok := b.part[s.Path]
			if !ok || !s.Exact || !b.stated(s) || strings.HasPrefix(s.Kind, "method of ") {
				continue
			}
			kinds = kinds || slices.ContainsFunc(s.HereKind, func(k string) bool { return k != "" })
			for j, h := range s.Here {
				k := useKind(kindAt(s, j))
				if iface[h] != "" { // the index's tie to an interface: no call
					behind[h], k = append(behind[h], s), "named"
				}
				if up, ok := b.part[placeOf(h)]; ok && up != dp && k != "reads" && k != "writes" {
					used.add(dp, up, n, k, "", h)
					if k == "calls" {
						b.drawn = append(b.drawn, drawnCall{s, n, h, up})
					}
				}
			}
		}
	}
	return behind, kinds
}

// through adds the calls of an interface's method, each to the part of every method behind it, unless VTA resolved the
// call site to one of them: that is a direct call.
func (b *mapper) through(used usage, names []string, iface map[string]string, behind map[string][]*Symbol) {
	for _, n := range names {
		for i := range b.d.Symbols[n] {
			s := &b.d.Symbols[n][i]
			t, ok := strings.CutPrefix(s.Kind, "method of ")
			tied := behind[s.Path+":"+strconv.Itoa(s.Line)]
			if !ok || !s.Exact || len(tied) == 0 {
				continue
			}
			for j, h := range s.Here {
				up, ok := b.part[placeOf(h)]
				if !ok || useKind(kindAt(s, j)) != "calls" || iface[h] != "" || slices.ContainsFunc(tied, func(c *Symbol) bool { return calledAt(c, h) }) {
					continue
				}
				seen := map[int]bool{}
				for _, c := range tied {
					if dp, ok := b.part[c.Path]; ok && dp != up && !seen[dp] {
						seen[dp] = true
						used.add(dp, up, n, "through", t, h)
					}
				}
			}
		}
	}
}

// count sets what a line's names add up to: its counts, weight, width, style and chevrons.
func (l *MapLine) count() {
	vias := map[string]bool{}
	for _, ns := range [][]MapName{l.Names, l.Back} {
		for _, n := range ns {
			switch n.Kind {
			case "calls":
				l.Calls += n.N
			case "through":
				l.Through += n.N
				vias[n.Via] = true
			case "named":
				l.Named += n.N
			}
		}
	}
	l.Via = slices.Sorted(maps.Keys(vias))
	l.Weight = l.Calls + l.Through
	l.Width = 1
	switch {
	case l.Calls > 0:
		l.Style = "solid"
		l.Width += map[bool]int{true: 1}[l.Weight >= 3] + map[bool]int{true: 1}[l.Weight >= 10] // dashed lines stay at 1px
	case l.Through+l.Named > 0:
		l.Style = "dashed"
	}
	// a call gives its result back to the caller; a call into code that writes sends its data to the callee
	at := map[string]bool{}
	for _, d := range []struct {
		ns               []MapName
		caller, declarer string
	}{{l.Names, "to", "from"}, {l.Back, "from", "to"}} {
		for _, n := range d.ns {
			if n.Kind == "calls" || n.Kind == "through" {
				at[map[bool]string{false: d.caller, true: d.declarer}[len(n.Writes) > 0]] = true
			}
		}
	}
	l.Chevron = map[[2]bool]string{{true, true}: "both", {true, false}: "to", {false, true}: "from"}[[2]bool{at["to"], at["from"]}]
}

// calledAt is whether s has a call at site h.
func calledAt(s *Symbol, h string) bool {
	i := slices.Index(s.Here, h)
	return i >= 0 && useKind(kindAt(s, i)) == "calls"
}

// sortNames puts calls first, then those through an interface, then names; in each the most uses first, ties in name
// order ignoring case.
func sortNames(m map[nameKey]*MapName) []MapName {
	rank := map[string]int{"calls": 0, "through": 1, "named": 2}
	var out []MapName
	for _, n := range m {
		out = append(out, *n)
	}
	slices.SortFunc(out, func(x, y MapName) int {
		return cmp.Or(cmp.Compare(rank[x.Kind], rank[y.Kind]), cmp.Compare(y.N, x.N),
			cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)), cmp.Compare(x.Name, y.Name), cmp.Compare(x.Via, y.Via))
	})
	return out
}

// drawnCall is a call the map draws, into s (named name) at h, from the part up.
type drawnCall struct {
	s    *Symbol
	name string
	h    string
	up   int
}

// storeUse is a part's use of a table, before its places are counted.
type storeUse struct {
	kind, decl string
	through    bool
}

var (
	// quotedName is a name in quotes, as SQL calls a table: pgTable("items", …)
	quotedName = regexp.MustCompile("[\"'`]([A-Za-z_][\\w.]*)[\"'`]")
	// madeBy is a declaration made by a call, the call's first argument in group 1 when it is a name: alias(t, "x")
	madeBy = regexp.MustCompile(`=\s*(?:await\s+)?[\w.]+\s*(?:<[^>]*>)?\(\s*(\w*)`)
	// the statements of a migration that create or change a table, its name in group 1
	createTable = regexp.MustCompile(`(?i)\bCREATE\s+(?:UNLOGGED\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([\w."` + "`" + `\[\]]+)`)
	alterTable  = regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([\w."` + "`" + `\[\]]+)`)
	createIndex = regexp.MustCompile(`(?i)\bCREATE\s+(?:UNIQUE\s+)?INDEX\b.*?\bON\s+(?:ONLY\s+)?([\w."` + "`" + `\[\]]+)`)
)

// storeKey is a table as an edge names it: its declaration's place, else its name, in any case.
func storeKey(p StorePath) string {
	if p.TPath != "" {
		return p.TPath + ":" + strconv.Itoa(p.TLine)
	}
	return strings.ToLower(p.Table)
}

// tableNames is what a table's declaration made by a call says of it: the name it quotes on its line or the next,
// and the call's first argument when that is a name, the table an alias is of (alias(t, "x")); "" for none.
func tableNames(text string) (quoted, of string) {
	first, next, _ := strings.Cut(text, "\n")
	m := madeBy.FindStringSubmatchIndex(first)
	if m == nil {
		return "", ""
	}
	of = first[m[2]:m[3]]
	if q := quotedName.FindStringSubmatch(first[m[0]:]); q != nil {
		return q[1], of
	}
	if q := quotedName.FindStringSubmatch(next); q != nil {
		return q[1], of
	}
	return "", of
}

// storeHit is a part's use of a table at a place: a query, or the call a path counts at.
type storeHit struct {
	key  string // storeKey
	part int
	use  storeUse
	at   string
}

// storeScan gathers the uses: the tables as first named, each with its first query, and every place once.
type storeScan struct {
	b       *mapper
	tables  map[string]StorePath
	firstAt map[string]string
	hits    []storeHit
	seen    map[storeHit]bool
}

// note keeps a table under its key as first named, with its first query, and gives the key.
func (sc *storeScan) note(p StorePath) string {
	k := storeKey(p)
	if _, ok := sc.tables[k]; !ok {
		sc.tables[k], sc.firstAt[k] = p, p.At
	}
	return k
}

// add keeps a place once, whichever start reached it, under the name it came first with: the map's for a drawn call.
func (sc *storeScan) add(h storeHit) {
	if k := (storeHit{h.key, h.part, storeUse{kind: h.use.kind, through: h.use.through}, h.at}); !sc.seen[k] {
		sc.seen[k] = true
		sc.hits = append(sc.hits, h)
	}
}

// through is a query that part pi's calls reach: it counts at the last call in the part, named by its callee, and
// not when the query is the part's own.
func (sc *storeScan) through(pi int, calls []StoreCall, p StorePath) {
	if q, ok := sc.b.part[placeOf(p.At)]; ok && q == pi {
		return
	}
	for i := len(calls) - 1; i >= 0; i-- {
		if q, ok := sc.b.part[placeOf(calls[i].At)]; ok && q == pi {
			sc.add(storeHit{sc.note(p), pi, storeUse{p.Kind, calls[i].Callee, true}, calls[i].At})
			return
		}
	}
}

// joinTables is one table per box (6.13): a name-only table joins the declaration that quotes it, an alias the table
// it is of, a name is one table. root gives a table's box key; named, by a name SQL may give it (lower case), a table.
func joinTables(tables map[string]StorePath) (root func(string) string, named map[string]string) {
	keys := slices.Sorted(maps.Keys(tables))
	byName, parent := map[string][]string{}, map[string]string{} // byName: the declarations of a name
	for _, k := range keys {
		if p := tables[k]; p.TPath != "" {
			byName[strings.ToLower(p.Table)] = append(byName[strings.ToLower(p.Table)], k)
		}
	}
	named = map[string]string{}
	for _, k := range keys {
		p := tables[k]
		if p.TPath == "" { // a name only: SQL's own, before a declaration's
			named[k] = k
			continue
		}
		named[strings.ToLower(p.Table)] = cmp.Or(named[strings.ToLower(p.Table)], k)
		q, of := tableNames(p.Text)
		if t := ofDecl(tables, byName[strings.ToLower(of)], p.TPath); of != "" && t != "" && t != k {
			parent[k] = t
		}
		if q = strings.ToLower(q); q != "" {
			if _, ok := tables[q]; ok && parent[q] == "" {
				parent[q] = k
			}
			named[q] = cmp.Or(named[q], k)
		}
	}
	return func(k string) string {
		for range len(keys) { // an alias of an alias; a cycle ends somewhere
			p, ok := parent[k]
			if !ok {
				break
			}
			k = p
		}
		return k
	}, named
}

// ofDecl is the declaration an alias in file names among ds: the one in its file, else the only one; "" when two
// modules declare the name and the alias's file neither.
func ofDecl(tables map[string]StorePath, ds []string, file string) string {
	for _, d := range ds {
		if tables[d].TPath == file {
			return d
		}
	}
	if len(ds) == 1 {
		return ds[0]
	}
	return ""
}

// stores is Map.Stores (6.13), but for their order, and the tables each part's declaration writes (wrote). A query
// of a declaration the PR adds or changes is its part's. A part's call reaches more, within maxHops calls: a call the
// map draws and a call on a line the diff adds start a path (storeScan.through).
func (b *mapper) stores(names []string) {
	sc := &storeScan{b: b, tables: map[string]StorePath{}, firstAt: map[string]string{}, seen: map[storeHit]bool{}}
	for _, c := range b.drawn { // first: the map's name for the callee
		for _, p := range c.s.Stores {
			if len(p.Calls) < maxHops {
				sc.through(c.up, append([]StoreCall{{c.h, c.name}}, p.Calls...), p)
			}
		}
	}
	wrote := map[*Symbol][]string{} // table keys
	for _, n := range names {
		for i := range b.d.Symbols[n] {
			s := &b.d.Symbols[n][i]
			pi, ok := b.part[s.Path]
			if !ok || !s.Exact || !b.stated(s) {
				continue
			}
			for _, p := range s.Stores {
				switch {
				case len(p.Calls) == 0:
					sc.add(storeHit{sc.note(p), pi, storeUse{p.Kind, n, false}, p.At})
				case b.added[p.Calls[0].At]:
					sc.through(pi, p.Calls, p)
				}
				if p.Kind == "writes" && len(p.Calls) < maxHops {
					wrote[s] = append(wrote[s], sc.note(p))
				}
			}
		}
	}
	root, named := joinTables(sc.tables)
	box := map[string]int{}
	uses := map[int]map[int]map[storeUse]*MapStoreUse{} // box -> part -> use
	for _, h := range sc.hits {
		r := root(h.key)
		i, ok := box[r]
		if !ok {
			p := sc.tables[r]
			st := MapStore{Name: p.Table, Subquery: p.Sub}
			if p.TPath != "" {
				st.Path, st.Line = p.TPath, p.TLine
			}
			i, box[r] = len(b.m.Stores), len(b.m.Stores)
			b.m.Stores = append(b.m.Stores, st)
			uses[i] = map[int]map[storeUse]*MapStoreUse{}
		}
		if uses[i][h.part] == nil {
			uses[i][h.part] = map[storeUse]*MapStoreUse{}
		}
		if uses[i][h.part][h.use] == nil {
			uses[i][h.part][h.use] = &MapStoreUse{Kind: h.use.kind, Decl: h.use.decl, Through: h.use.through, At: h.at}
		}
		uses[i][h.part][h.use].N++
	}
	for _, k := range slices.Sorted(maps.Keys(sc.tables)) {
		i, ok := box[root(k)]
		if p := sc.tables[k]; ok && k != root(k) && !strings.EqualFold(p.Table, b.m.Stores[i].Name) {
			at := sc.firstAt[k]
			if p.TPath != "" {
				at = p.TPath + ":" + strconv.Itoa(p.TLine)
			}
			b.m.Stores[i].Names = append(b.m.Stores[i].Names, MapStoreName{p.Table, at})
		}
	}
	for i := range b.m.Stores {
		b.storeParts(&b.m.Stores[i], uses[i])
	}
	b.wrote = map[*Symbol][]string{}
	for s, ks := range wrote {
		for _, k := range ks {
			b.wrote[s] = append(b.wrote[s], sc.tables[root(k)].Table)
		}
	}
	b.storeMigrations(func(name string) (int, bool) {
		// the name with its schema, then without: public.orders is the table public.orders, else orders
		full := strings.NewReplacer(`"`, "", "`", "", "'", "", "[", "", "]", "").Replace(name)
		for _, n := range []string{full, bareName(name)} {
			if k := named[strings.ToLower(n)]; k != "" {
				if i, ok := box[root(k)]; ok {
					return i, true
				}
			}
		}
		return 0, false
	})
}

// storeParts sets a table's names in name order and its parts' uses: writes first, a query before a call, the
// most first, then by name ignoring case.
func (b *mapper) storeParts(st *MapStore, uses map[int]map[storeUse]*MapStoreUse) {
	slices.SortFunc(st.Names, func(x, y MapStoreName) int {
		return cmp.Or(cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)), cmp.Compare(x.Name, y.Name))
	})
	for _, pi := range slices.Sorted(maps.Keys(uses)) {
		var us []MapStoreUse
		for _, u := range uses[pi] {
			us = append(us, *u)
		}
		slices.SortFunc(us, func(x, y MapStoreUse) int {
			return cmp.Or(-cmp.Compare(x.Kind, y.Kind), cmp.Compare(map[bool]int{true: 1}[x.Through], map[bool]int{true: 1}[y.Through]),
				cmp.Compare(y.N, x.N), cmp.Compare(strings.ToLower(x.Decl), strings.ToLower(y.Decl)), cmp.Compare(x.Decl, y.Decl), cmp.Compare(x.At, y.At))
		})
		st.Parts = append(st.Parts, MapStorePart{b.m.Parts[pi].ID, us})
	}
}

// storeMigrations adds to the tables the PR's migrations name (6.13): CREATE TABLE, ALTER TABLE, CREATE INDEX … ON
// and DROP TABLE in what runs on migrate (upLines). A migration that creates a table created it, also when it changes
// it; else one that drops it dropped it; else it changed it.
func (b *mapper) storeMigrations(table func(name string) (int, bool)) {
	type key struct {
		store int
		path  string
	}
	kinds, lines := map[key]map[string]bool{}, map[key][]int{}
	upStatements(b.d, func(p string, stmt sqlLine) {
		no, text := stmt.line, strings.ReplaceAll(stmt.text, "\n", " ")
		for _, st := range []struct {
			re   *regexp.Regexp
			kind string
		}{{createTable, "created"}, {alterTable, "changed"}, {createIndex, "changed"}, {dropTable, "dropped"}} {
			m := st.re.FindStringSubmatch(text)
			if m == nil {
				continue
			}
			for _, t := range strings.Split(m[1], ",") { // DROP TABLE a, b
				fs := strings.Fields(t)
				if len(fs) == 0 {
					continue
				}
				i, ok := table(fs[0])
				if !ok {
					continue
				}
				k := key{i, p}
				if kinds[k] == nil {
					kinds[k] = map[string]bool{}
				}
				kinds[k][st.kind] = true
				lines[k] = append(lines[k], no)
			}
		}
	})
	for _, k := range slices.SortedFunc(maps.Keys(kinds), func(x, y key) int { return cmp.Or(cmp.Compare(x.store, y.store), cmp.Compare(x.path, y.path)) }) {
		kind := "changed"
		if kinds[k]["created"] {
			kind = "created"
		} else if kinds[k]["dropped"] {
			kind = "dropped"
		}
		ls := lines[k]
		slices.Sort(ls)
		b.m.Stores[k.store].Migrations = append(b.m.Stores[k.store].Migrations, MapStoreMigration{k.path, migrationLabel(k.path), kind, slices.Compact(ls)})
	}
}

// orderStores puts Map.Stores in order (6.13): the tables some part writes first, then more parts first, then by
// name ignoring case; each table's parts in map order.
func (b *mapper) orderStores() {
	pos := map[string]int{}
	for i, id := range b.m.Order {
		pos[id] = i
	}
	writes := func(s MapStore) int {
		for _, p := range s.Parts {
			if slices.ContainsFunc(p.Uses, func(u MapStoreUse) bool { return u.Kind == "writes" }) {
				return 0
			}
		}
		return 1
	}
	for i := range b.m.Stores {
		slices.SortStableFunc(b.m.Stores[i].Parts, func(x, y MapStorePart) int { return cmp.Compare(pos[x.Part], pos[y.Part]) })
	}
	slices.SortStableFunc(b.m.Stores, func(x, y MapStore) int {
		return cmp.Or(cmp.Compare(writes(x), writes(y)), cmp.Compare(len(y.Parts), len(x.Parts)),
			cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)), cmp.Compare(x.Name, y.Name), cmp.Compare(x.Path, y.Path))
	})
}

// ids is the parts' IDs in folder order, minus skip.
func (b *mapper) ids(skip map[string]bool) []string {
	var out []string
	for _, p := range b.m.Parts {
		if !skip[p.ID] {
			out = append(out, p.ID)
		}
	}
	return out
}

// systems are the outside calls (Outside) grouped by system, kept when the call sits in a part's code file (6.4).
func (b *mapper) systems(calls []OutsideCall) {
	at := map[string]int{}
	for _, c := range calls {
		pi, ok := b.part[c.Path]
		if !ok {
			continue
		}
		i, ok := at[c.System]
		if !ok {
			i = len(b.m.Systems)
			at[c.System] = i
			b.m.Systems = append(b.m.Systems, MapSystem{Name: c.System})
		}
		s := &b.m.Systems[i]
		s.Calls = append(s.Calls, c)
		if id := b.m.Parts[pi].ID; !slices.Contains(s.Parts, id) {
			s.Parts = append(s.Parts, id)
		}
	}
	slices.SortFunc(b.m.Systems, func(x, y MapSystem) int { return strings.Compare(x.Name, y.Name) })
}

// mapEdge is a weighted line between two nodes of the layout.
type mapEdge struct{ to, w int }

// rows gives each node its row in its column, sweeping the columns by the weighted mean row of their neighbours;
// it also returns the last column.
func rows(node []string, cols []int, adj [][]mapEdge) (top int, row []int) {
	for _, c := range cols {
		top = max(top, c)
	}
	row = make([]int, len(node))
	rank := func(c int, near func(o int) bool) { // the column c by the weighted mean row of its near neighbours
		var m []int
		for i := range node {
			if cols[i] == c {
				m = append(m, i)
			}
		}
		mean := map[int]float64{}
		for _, i := range m {
			sum, tot := 0, 0
			for _, e := range adj[i] {
				if near(e.to) {
					sum, tot = sum+e.w*row[e.to], tot+e.w
				}
			}
			mean[i] = float64(row[i])
			if tot > 0 {
				mean[i] = float64(sum) / float64(tot)
			}
		}
		slices.SortStableFunc(m, func(x, y int) int {
			return cmp.Or(cmp.Compare(mean[x], mean[y]), cmp.Compare(len(adj[y]), len(adj[x])), strings.Compare(node[x], node[y]))
		})
		for r, i := range m {
			row[i] = r
		}
	}
	for c := 0; c <= top; c++ { // folder order to start with
		r := 0
		for i := range node {
			if cols[i] == c {
				row[i] = r
				r++
			}
		}
	}
	for range 4 {
		for c := 1; c <= top; c++ {
			rank(c, func(o int) bool { return cols[o] < c })
		}
		for c := top - 1; c >= 0; c-- {
			rank(c, func(o int) bool { return cols[o] > c })
		}
	}
	return top, row
}

// weights is the matrix of the drawn lines: w[i][j] is how much part i uses part j, at least 1 when it does.
func (b *mapper) weights(at map[string]int) [][]int {
	n := len(b.m.Parts)
	w := make([][]int, n) // w[i][j]: i uses j, by the weight of the line, at least 1
	for i := range w {
		w[i] = make([]int, n)
	}
	for _, l := range b.m.Lines {
		from, to := at[l.From], at[l.To]
		if len(l.Names) > 0 { // To uses what From declares
			w[to][from] += max(l.Weight, 1)
		}
		if len(l.Back) > 0 {
			w[from][to] += max(l.Weight, 1)
		}
	}
	return w
}

// layout sets the columns and rows (6.3): a part is one column right of the parts it uses, a cycle shares one; each
// column is ordered by four sweeps of the weighted mean row of the parts a part touches.
func (b *mapper) layout() {
	if b.m.NoGraph {
		return
	}
	n, at := len(b.m.Parts), map[string]int{}
	for i, p := range b.m.Parts {
		at[p.ID] = i
	}
	w := b.weights(at)
	_, reach := order(w)
	same := func(i, j int) bool { return i == j || reach[i][j] && reach[j][i] }
	memo := make([]int, n)
	var col func(i int) int
	col = func(i int) int {
		if memo[i] > 0 {
			return memo[i]
		}
		top := 0
		for j := range n {
			if !same(i, j) {
				continue
			}
			for k := range n {
				if w[j][k] > 0 && !same(i, k) {
					top = max(top, col(k))
				}
			}
		}
		for j := range n {
			if same(i, j) {
				memo[j] = top + 1
			}
		}
		return top + 1
	}
	// nodes: the parts with a line, then the systems
	var node []string // IDs, parts first
	idx := map[string]int{}
	touch := make([]bool, n)
	for _, l := range b.m.Lines {
		if l.Style != "" {
			touch[at[l.From]], touch[at[l.To]] = true, true
		}
	}
	for _, s := range b.m.Systems {
		for _, p := range s.Parts {
			touch[at[p]] = true
		}
	}
	var cols []int
	for i, p := range b.m.Parts {
		if touch[i] {
			idx[p.ID] = len(node)
			node, cols = append(node, p.ID), append(cols, col(i))
		} else {
			b.m.Parts[i].Alone = true
		}
	}
	nparts := len(node)
	for _, s := range b.m.Systems {
		idx["system "+s.Name] = len(node)
		node, cols = append(node, "system "+s.Name), append(cols, 0)
	}
	adj := make([][]mapEdge, len(node))
	link := func(a, c, wt int) {
		adj[a], adj[c] = append(adj[a], mapEdge{c, wt}), append(adj[c], mapEdge{a, wt})
	}
	for _, l := range b.m.Lines {
		if l.Style != "" {
			link(idx[l.From], idx[l.To], max(l.Weight, 1))
		}
	}
	for _, s := range b.m.Systems {
		for _, p := range s.Parts {
			link(idx["system "+s.Name], idx[p], 1)
		}
	}
	top, row := rows(node, cols, adj)
	b.m.Cols = top
	for i, id := range node[:nparts] {
		p := &b.m.Parts[at[id]]
		p.Col, p.Row = cols[i], row[i]
	}
	for k := range b.m.Systems {
		i := nparts + k
		b.m.Systems[k].Col, b.m.Systems[k].Row = cols[i], row[i]
	}
	ord := slices.Clone(b.m.Parts)
	slices.SortStableFunc(ord, func(x, y MapPart) int {
		return cmp.Or(cmp.Compare(map[bool]int{true: 1}[x.Alone], map[bool]int{true: 1}[y.Alone]), cmp.Compare(x.Col, y.Col), cmp.Compare(x.Row, y.Row))
	})
	for _, p := range ord {
		b.m.Order = append(b.m.Order, p.ID)
	}
}

// reach is what code outside the PR names of the changed declarations, in code only: a place in a test names the code
// to test it. The sums count all of it; the list keeps the first maxAffected declarations, signature changes first.
func (b *mapper) reach() {
	type cand struct {
		d   MapReachDecl
		sig bool
	}
	var cs []cand
	for _, n := range slices.Sorted(maps.Keys(b.d.Symbols)) {
		for i := range b.d.Symbols[n] {
			s := &b.d.Symbols[n][i]
			pi, ok := b.part[s.Path]
			if !ok || !s.Exact || !b.stated(s) || s.NOutside == 0 {
				continue
			}
			p := &b.m.Parts[pi]
			code := s.NOutside - s.NOutsideTests
			p.Tests, b.m.Reach.Tests = p.Tests+s.NOutsideTests, b.m.Reach.Tests+s.NOutsideTests
			if code <= 0 {
				continue
			}
			p.Reach, b.m.Reach.Places = p.Reach+code, b.m.Reach.Places+code
			d := MapReachDecl{Part: p.ID, Name: n, Path: s.Path, Line: s.Line, N: code}
			for _, r := range s.Outside {
				if !checks.IsTest(r.Path) {
					d.Places = append(d.Places, r)
				}
			}
			cs = append(cs, cand{d, s.State == "signature"})
		}
	}
	slices.SortStableFunc(cs, func(x, y cand) int {
		return cmp.Or(cmp.Compare(map[bool]int{true: 0, false: 1}[x.sig], map[bool]int{true: 0, false: 1}[y.sig]), cmp.Compare(y.d.N, x.d.N))
	})
	b.m.Reach.Decls = len(cs)
	if len(cs) > maxAffected {
		b.m.Reach.More, cs = len(cs)-maxAffected, cs[:maxAffected]
	}
	pos := map[string]int{}
	for i, id := range b.m.Order {
		pos[id] = i
	}
	slices.SortStableFunc(cs, func(x, y cand) int { return cmp.Compare(pos[x.d.Part], pos[y.d.Part]) })
	for _, c := range cs {
		b.m.Reach.List = append(b.m.Reach.List, c.d)
	}
}

// deploy is the row "Before merging" and the items on the map (6.5): a box for each of risks()' kinds with items, a
// box for each migration, one for open PRs and one for docs; a finding sits on the box that holds its file: a part,
// Generated, a deploy box, else it is Off.
func (b *mapper) deploy() {
	boxes := map[string]*MapDeploy{}
	box := func(kind, label, p string) *MapDeploy {
		k := label // a migration is its file, whatever its label
		if kind == "migration" {
			k = p
		}
		if boxes[k] == nil {
			boxes[k] = &MapDeploy{Kind: kind, Label: label, Path: p}
		}
		return boxes[k]
	}
	kinds := map[string]string{"Environment": "environment", "Dependencies": "dependencies", "Build, deploy and CI": "ci", "Configuration": "configuration"}
	label := map[string]string{"environment": "Environment", "dependencies": "Dependencies", "ci": "CI", "configuration": "Configuration"}
	for _, r := range b.v.Risks {
		var bx *MapDeploy
		for _, it := range r.Items {
			mi := MapItem{Kind: "risk", Title: strings.TrimSpace(it.What + " " + it.Note)}
			if r.Title == "Migrations" {
				mi.Path = it.What
				bx = box("migration", migrationLabel(it.What), it.What)
			} else {
				bx = box(kinds[r.Title], label[kinds[r.Title]], "")
				if r.Title == "Configuration" && toolConfig(it.What) {
					bx = box("ci", "CI", "")
				}
				if r.Title != "Environment" && r.Title != "Dependencies" {
					mi.Path = it.What
				}
			}
			bx.Items, bx.N = append(bx.Items, mi), bx.N+1
		}
		if bx != nil && r.Title != "Migrations" {
			bx.N += r.More
		}
	}
	for _, o := range b.d.Others {
		bx := box("prs", "Open PRs", "")
		bx.Items, bx.N = append(bx.Items, MapItem{Kind: "pr", Title: o.Title}), bx.N+1
	}
	for _, t := range b.m.Tables {
		bx := box("migration", migrationLabel(t.Path), t.Path)
		bx.Drops = append(bx.Drops, t.Name)
	}
	for _, f := range b.d.Findings {
		mi := MapItem{Kind: "finding", Title: f.Title, Path: f.Path, Line: f.Line, Sev: f.Severity}
		on := func(bx *MapDeploy) { bx.Items, bx.N = append(bx.Items, mi), bx.N+1 }
		switch pi, ok := b.part[f.Path]; {
		case ok:
			b.m.Parts[pi].Items = append(b.m.Parts[pi].Items, mi)
		case b.gen[f.Path]:
			b.m.Generated.Items = append(b.m.Generated.Items, mi)
		case diff.Kind(f.Path) == "doc":
			on(box("docs", "Docs", ""))
		case migration.MatchString(f.Path):
			on(box("migration", migrationLabel(f.Path), f.Path))
		case deploy.MatchString(f.Path) || toolConfig(f.Path):
			on(box("ci", "CI", ""))
		case config.MatchString(f.Path) && diff.Kind(f.Path) != "test":
			on(box("configuration", "Configuration", ""))
		default:
			b.m.Off = append(b.m.Off, mi)
		}
	}
	rank := map[string]int{"migration": 0, "environment": 1, "dependencies": 2, "ci": 3, "configuration": 4, "prs": 5, "docs": 6}
	for _, bx := range slices.SortedFunc(maps.Values(boxes), func(x, y *MapDeploy) int {
		return cmp.Or(cmp.Compare(rank[x.Kind], rank[y.Kind]), strings.Compare(x.Label, y.Label), strings.Compare(x.Path, y.Path))
	}) {
		b.m.Deploy = append(b.m.Deploy, *bx)
	}
}

// migrationLabel is "Migration 0059" for drizzle/0059_drop_x.sql, the file's name without its extension otherwise; a
// Prisma migration.sql is named by its folder, 20240101120000_init.
func migrationLabel(p string) string {
	base := path.Base(p)
	base = strings.TrimSuffix(base, path.Ext(base))
	if base == "migration" {
		base = path.Base(path.Dir(p))
	}
	if n := strings.IndexFunc(base, func(r rune) bool { return r < '0' || r > '9' }); n > 0 {
		base = base[:n]
	}
	return "Migration " + base
}

// trace is the first way into the change, in the report's order, to a declaration that holds an outside call
// (6.12); nil without a call or a way that reaches one.
func (b *mapper) trace() {
	for _, w := range b.v.Ways {
		for ri, r := range w.Rows {
			for _, s := range b.d.Symbols[r.Name] {
				if s.Path != r.Path || !s.Exact {
					continue
				}
				for _, sys := range b.m.Systems {
					for _, c := range sys.Calls {
						if c.Path != r.Path || c.Line < s.From || c.Line > s.To {
							continue
						}
						rows := []wrow{r}
						for k, d := ri-1, r.D; k >= 0 && d > 1; k-- { // the rows above that lead down to it
							if w.Rows[k].D < d {
								rows, d = append([]wrow{w.Rows[k]}, rows...), w.Rows[k].D
							}
						}
						b.m.Trace = &MapTrace{Method: w.Method, Route: w.Route, At: w.At, Rows: rows, Call: c, Part: b.m.Parts[b.part[c.Path]].ID}
						return
					}
				}
			}
		}
	}
}

var (
	dropTable = regexp.MustCompile(`(?i)\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?([^;]+)`)
	// downMark starts what runs on the way back: goose's and dbmate's
	downMark  = regexp.MustCompile(`(?i)^\s*--\s*(?:\+goose\s+down|migrate:down)\b`)
	tableName = regexp.MustCompile(`^\w+$`)
	// tableDecl is a table's declaration, its name in group 1: export const items = pgTable(
	tableDecl = regexp.MustCompile(`(?:const|let|var)\s+(\w+)\s*(?::[^=]+)?=\s*[\w.]*[Tt]able\w*\s*(?:<[^>]*>)?\(`)
	// reexport is an import or export of names from a module, the names in group 1 and the module in group 2
	reexport = regexp.MustCompile(`(?s)\b(import|export)\b([^;'"]*?)\bfrom\s*['"]([^'"]+)['"]`)
	jsSpecs  = []string{"*.ts", "*.tsx", "*.js", "*.jsx", "*.mjs"}
)

// maxTableFiles is the files a search of the base or the head reads; maxNamed the places it keeps per table.
const (
	maxTableFiles, maxNamed = 300, 20
	maxDrops                = 50 // the dropped tables that tables searches: each costs git calls within tableTime
	tableTime               = time.Minute
)

// upLines calls f with each line of d's SQL files that the PR adds and that runs on migrate, comments aside: a
// goose file's Down part, a .down.sql file and a test's SQL don't count. A file's text is its head's, the diff's lines
// without it.
func upLines(d Data, f func(p string, no int, text string)) {
	migrationLines(d, func(p string, l sqlLine) {
		if t := strings.TrimSpace(l.text); l.add && t != "" && !strings.HasPrefix(t, "--") {
			f(p, l.line, l.text)
		}
	})
}

// upStatements calls f with each statement of d's migrations that the PR adds a line to or removes one inside, whole:
// a column added inside a CREATE TABLE comes with its table. Its line is the PR's first change to it.
func upStatements(d Data, f func(p string, st sqlLine)) {
	var files []string
	byFile := map[string][]sqlLine{}
	migrationLines(d, func(p string, l sqlLine) {
		if _, ok := byFile[p]; !ok {
			files = append(files, p)
		}
		byFile[p] = append(byFile[p], l)
	})
	for _, p := range files {
		for _, st := range statements(byFile[p]) {
			if st.add {
				f(p, st)
			}
		}
	}
}

// migrationLines calls f with each line of d's SQL files that runs on migrate, add when the PR adds it, cut when it
// removes lines right before it, its /* */ comments cut: what upLines reads, with the unchanged lines around for a
// statement the PR edits. The changed lines come from the whole diff, not the page's capped view of it.
func migrationLines(d Data, f func(p string, l sqlLine)) {
	diff.EachFile(d.Diff, func(p, fd string) {
		if path.Ext(p) != ".sql" || strings.HasSuffix(p, ".down.sql") || isTest(p) {
			return
		}
		src := d.Sources[p]
		var lines []sqlLine
		added, cut := map[int]bool{}, map[int]bool{}
		diff.EachLine(fd, func(at, l string) {
			n, _ := strconv.Atoi(at[strings.LastIndexByte(at, ':')+1:])
			if l[0] == '-' {
				cut[n] = true
				return
			}
			added[n] = l[0] == '+'
			if src == "" {
				lines = append(lines, sqlLine{text: l[1:], line: n, add: added[n], cut: cut[n]})
			}
		})
		if src != "" {
			for i, t := range strings.Split(src, "\n") {
				lines = append(lines, sqlLine{text: t, line: i + 1, add: added[i+1], cut: cut[i+1]})
			}
		}
		block := false
		for _, l := range lines {
			if !block && downMark.MatchString(l.text) {
				break
			}
			l.text, block = uncomment(l.text, block)
			f(p, l)
		}
	})
}

// uncomment is a line of SQL without its /* */ comments, block when one is open at its start; open, one is at its end.
// A -- comment stays: statements cuts it.
func uncomment(t string, block bool) (string, bool) {
	var b strings.Builder
	for t != "" {
		if block {
			i := strings.Index(t, "*/")
			if i < 0 {
				return b.String(), true
			}
			t, block = t[i+2:], false
			continue
		}
		i := strings.Index(t, "/*")
		if j := strings.Index(t, "--"); i < 0 || j >= 0 && j < i {
			b.WriteString(t)
			break
		}
		b.WriteString(t[:i])
		t, block = t[i+2:], true
	}
	return b.String(), block
}

// bareName is a table's name in SQL without its schema and quotes: public."items" is items.
func bareName(s string) string {
	return strings.Trim(s[strings.LastIndexByte(s, '.')+1:], "\"`'[]")
}

// drops is the tables the migrations of the diff drop: a DROP TABLE in upLines.
func (b *mapper) drops() []MapTable {
	var out []MapTable
	seen := map[[2]string]bool{}
	upLines(b.d, func(p string, no int, text string) {
		m := dropTable.FindStringSubmatch(text)
		if m == nil {
			return
		}
		for _, t := range strings.Split(m[1], ",") {
			fs := strings.Fields(t)
			if len(fs) == 0 {
				continue
			}
			name := bareName(fs[0])
			if tableName.MatchString(name) && !seen[[2]string{p, name}] {
				seen[[2]string{p, name}] = true
				out = append(out, MapTable{Name: name, Path: p, Line: no})
			}
		}
	})
	return out
}

// tableSite is the file and id a table's declaration names.
type tableSite struct{ file, id string }

// declarations finds, at the base, the file whose code quotes each table name with a table call on that line or the one above.
func declarations(git func(args ...string) []byte, names []string) map[string]tableSite {
	// the declaration: the file whose code quotes the name, a table call on that line or the one above
	quoted := regexp.MustCompile("[\"'`](" + strings.Join(names, "|") + ")[\"'`]")
	text := map[string]map[int]string{}
	for _, l := range strings.Split(string(git(append([]string{"grep", "-n", "-z", "-I", "-B1", "-E", quoted.String(), "refs/base", "--"}, jsSpecs...)...)), "\n") {
		if f := strings.Split(l, "\x00"); len(f) == 3 {
			p, n := strings.TrimPrefix(f[0], "refs/base:"), 0
			n, _ = strconv.Atoi(f[1])
			if text[p] == nil {
				text[p] = map[int]string{}
			}
			text[p][n] = f[2]
		}
	}
	decls := map[string]tableSite{}
	for _, p := range slices.Sorted(maps.Keys(text)) {
		if diff.Kind(p) == "test" {
			continue
		}
		for _, n := range slices.Sorted(maps.Keys(text[p])) {
			q := quoted.FindStringSubmatch(text[p][n])
			if q == nil || decls[q[1]].id != "" {
				continue
			}
			if m := tableDecl.FindStringSubmatch(text[p][n-1] + "\n" + text[p][n]); m != nil {
				decls[q[1]] = tableSite{p, m[1]}
			}
		}
	}
	return decls
}

// sqlNames notes where the head names a table in SQL.
func sqlNames(git func(args ...string) []byte, ts []MapTable, names []string) {
	// the head names a table in SQL: a keyword before it (git's patterns are POSIX: no (?:))
	pat := "\\b(from|join|into|update|table)\\s+[\"`]?(" + strings.Join(names, "|") + ")\\b"
	sql := regexp.MustCompile("(?i)" + pat)
	args := []string{"grep", "-n", "-z", "-i", "-I", "-E", pat, "--", "*.go"}
	for _, l := range strings.Split(string(git(append(args, jsSpecs...)...)), "\n") {
		f := strings.Split(l, "\x00")
		if len(f) != 3 || diff.Kind(f[0]) == "test" {
			continue
		}
		if m := sql.FindStringSubmatch(f[2]); m != nil {
			for i := range ts {
				if strings.EqualFold(ts[i].Name, m[2]) && len(ts[i].Named) < maxNamed {
					ts[i].Named = append(ts[i].Named, f[0]+":"+f[1])
				}
			}
		}
	}
}

// tables is Map.Tables (6.11): the tables the diff's migrations drop, who imported their declaration at the base,
// and what the head still has. Git calls, bounded by the minute tableTime gives them and, per table, at most
// twelve (two greps and one blob read per round, two rounds, base and head); without sb none.
func (b *mapper) tables(sb *proc.Sandbox) {
	ts := b.drops()
	if sb == nil || len(ts) == 0 {
		b.m.Tables = ts
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), tableTime)
	defer cancel()
	git := func(args ...string) []byte {
		out, _ := proc.Run(sb.Command(ctx, sb.Repo(), nil, "git", args...)) // exit 1: nothing found
		return out
	}
	names := make([]string, min(len(ts), maxDrops))
	for i := range names {
		names[i] = ts[i].Name
	}
	decls := declarations(git, names)
	find := func(ref string, d tableSite, seed map[string]bool) (importing, bool) {
		return findImporters(git, func(specs []string) map[string]string { return catFiles(ctx, sb, specs) }, ref, d, seed, maxTableFiles)
	}
	for i := range ts {
		d, ok := decls[ts[i].Name]
		if i >= maxDrops || ctx.Err() != nil { // past the cap or out of time: the rest of the tables were not searched
			ts[i].Cut = true
		}
		if !ok || ts[i].Cut {
			continue
		}
		ts[i].Decl = d.file
		mods, cut := find("refs/base", d, nil)
		at := map[int][]string{} // the part -> files
		for _, p := range slices.Sorted(maps.Keys(mods.files)) {
			pi, ok := b.part[p]
			switch {
			case diff.Kind(p) == "test":
				ts[i].Tests++
			case ok:
				at[pi] = append(at[pi], p)
			default: // a deleted file: its folder's part
				if pj, ok := b.byFolder(folder(p, b.depth)); ok {
					at[pj] = append(at[pj], p)
				} else {
					ts[i].Elsewhere++
				}
			}
		}
		for _, pi := range slices.Sorted(maps.Keys(at)) {
			ts[i].Parts = append(ts[i].Parts, MapTablePart{b.m.Parts[pi].ID, at[pi]})
			b.m.Parts[pi].DB = true
		}
		head, cutHead := find("", d, mods.mods)
		for _, p := range slices.Sorted(maps.Keys(head.files)) {
			if diff.Kind(p) != "test" && len(ts[i].Imports) < maxNamed {
				ts[i].Imports = append(ts[i].Imports, p)
			}
		}
		ts[i].Cut = cut || cutHead || ctx.Err() != nil
	}
	sqlNames(git, ts, names)
	b.m.Tables = ts
}

// findImporters is the files of ref ("" the head) that import the declaration d, and whether a search was cut at limit
// files. A search reads the files that name its id and one of the modules that hold it (the file, the barrels that
// re-export it): the first round the declaring file's, a second the barrels the first found. seed: the modules the
// base found, for the head.
func findImporters(git func(args ...string) []byte, read func(specs []string) map[string]string, ref string, d tableSite, seed map[string]bool, limit int) (importing, bool) {
	tag := cmp.Or(ref, "HEAD") + ":"
	mods := map[string]bool{strings.TrimSuffix(d.file, path.Ext(d.file)): true}
	maps.Copy(mods, seed)
	texts, cut := map[string]string{}, false
	var im importing
	for range 2 {
		var names []string
		for m := range mods {
			n := path.Base(m)
			if n == "index" {
				n = path.Base(path.Dir(m))
			}
			names = append(names, regexp.QuoteMeta(n))
		}
		slices.Sort(names)
		alt := strings.Join(slices.Compact(names), "|")
		// the files that name the id and a module, and the files that re-export a module whole (export * names no id)
		var specs []string
		seen := map[string]bool{}
		for _, a := range [][]string{
			{"-w", "-E", "--all-match", "-e", d.id, "-e", "(" + alt + ")"},
			{"-E", "-e", `export[[:space:]]*\*[^;]*from[[:space:]]*["'](([^"']*/)?(` + alt + `)(\.[cm]?[jt]sx?)?|[^"']*/(` + alt + `)/index)["']`},
		} {
			args := append([]string{"grep", "-l", "-z", "-I"}, a...)
			if ref != "" {
				args = append(args, ref)
			}
			for _, p := range strings.Split(string(git(append(args, append([]string{"--"}, jsSpecs...)...)...)), "\x00") {
				if p = strings.TrimPrefix(p, ref+":"); p == "" || seen[p] {
					continue
				}
				if len(specs) == limit {
					cut = true
					break
				}
				seen[p] = true
				specs = append(specs, tag+p)
			}
		}
		maps.Copy(texts, read(specs))
		im = importers(texts, d.file, d.id, tag, seed)
		if len(im.mods) == len(mods) {
			break
		}
		mods = im.mods
	}
	return im, cut
}

// byFolder is the part that holds folder k, "Other" when it joins it.
func (b *mapper) byFolder(k string) (int, bool) {
	for i, p := range b.m.Parts {
		if p.Path == k || p.ID == otherID && b.other[k] {
			return i, true
		}
	}
	return 0, false
}

type importing struct {
	files map[string]bool // who imports the declaration
	mods  map[string]bool // the modules that hold it: its file, the barrels that re-export it
}

// importers is the files of texts (by "ref:path") that import id, declared in file, directly or through a module
// that re-exports it, a name in a multi-line import too. mods seeds the modules, else the declaring file's.
func importers(texts map[string]string, file, id, ref string, seed ...map[string]bool) importing {
	im := importing{files: map[string]bool{}, mods: map[string]bool{}}
	noExt := func(p string) string { return strings.TrimSuffix(p, path.Ext(p)) }
	im.mods[noExt(file)] = true
	for _, s := range seed {
		maps.Copy(im.mods, s)
	}
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(id) + `\b`)
	// resolves is whether spec, written in from, names a module of the set: an alias (~/, @/, #/) by its tail
	resolves := func(from, spec string) bool {
		spec = strings.TrimSuffix(strings.TrimSuffix(spec, ".js"), ".ts")
		if strings.HasPrefix(spec, ".") {
			spec = path.Join(path.Dir(from), spec)
			return im.mods[spec] || im.mods[spec+"/index"]
		}
		for _, p := range []string{"~/", "@/", "#/"} {
			spec = strings.TrimPrefix(spec, p)
		}
		for m := range im.mods {
			if m == spec || strings.HasSuffix(m, "/"+spec) || m == spec+"/index" || strings.HasSuffix(m, "/"+spec+"/index") {
				return true
			}
		}
		return false
	}
	for range 3 { // barrels of barrels
		grew := false
		for key, t := range texts {
			p := strings.TrimPrefix(key, ref)
			if noExt(p) == noExt(file) {
				continue
			}
			for _, m := range reexport.FindAllStringSubmatch(t, -1) {
				if m[1] == "export" && (word.MatchString(m[2]) || strings.Contains(m[2], "*")) && resolves(p, m[3]) && !im.mods[noExt(p)] {
					im.mods[noExt(p)], grew = true, true
				}
			}
		}
		if !grew {
			break
		}
	}
	for key, t := range texts {
		p := strings.TrimPrefix(key, ref)
		if noExt(p) == noExt(file) {
			continue
		}
		for _, m := range reexport.FindAllStringSubmatch(t, -1) {
			if (word.MatchString(m[2]) || m[1] == "export" && strings.Contains(m[2], "*")) && resolves(p, m[3]) {
				im.files[p] = true
			}
		}
	}
	return im
}

// catFiles reads the blobs of specs ("ref:path") with one git process, up to 64 MiB of them.
func catFiles(ctx context.Context, sb *proc.Sandbox, specs []string) map[string]string {
	out := map[string]string{}
	if len(specs) == 0 {
		return out
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := sb.Command(ctx, sb.Repo(), nil, "git", "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(strings.Join(specs, "\n") + "\n")
	pipe, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return out
	}
	r := bufio.NewReader(io.LimitReader(pipe, 64<<20))
	for _, spec := range specs {
		head, err := r.ReadString('\n')
		if err != nil {
			break
		}
		f := strings.Fields(head)
		n, err := strconv.Atoi(f[len(f)-1]) // the size is the last field: a path may hold spaces; "<spec> missing" has none
		if len(f) < 3 || err != nil {
			continue
		}
		if n > maxSize { // skip it unread: its size is git's word, not a bound on memory
			if _, err := io.CopyN(io.Discard, r, int64(n)+1); err != nil {
				break
			}
			continue
		}
		blob := make([]byte, n+1) // and the newline after it
		if _, err := io.ReadFull(r, blob); err != nil {
			break
		}
		if !bytes.Contains(blob, []byte{0}) {
			out[spec] = string(blob[:n])
		}
	}
	cancel()
	_ = cmd.Wait()
	return out
}
