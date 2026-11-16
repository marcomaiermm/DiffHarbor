package report

import (
	"cmp"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Read the change (5.7): a part block per part of the map (component 29), in map order, each with its open items,
// how it connects, the call paths to its findings, its steps and the rest of its files.

// pblock is a part block.
type pblock struct {
	ID, Label, Path, Key    string // ID the anchor; Key the map box's
	New, Removed, Generated bool
	Others                  bool // the major findings in no part, after the parts
	Kept, Gone              []*file
	Add, Del                int
	Second                  string
	N                       int // open items
	Open                    bool
	Conn, ConnMore          []connRow
	Tables, TablesMore      []connRow
	Outside                 []string
	Reach                   string
	ReachList               []MapReachDecl
	DB                      string
	Cards                   []*card
	Paths                   []cpath
	Steps                   []pstep
	Minors                  []*card
	Loose                   []*file
	Folders                 []MapFolder
	NoMember                bool // no current member changed or reviewed it in 12 months (team)
}

// connRow is a row of How it connects: the direction and the other part, its calls' names, more behind "and {j}
// more". A table row has Table set and no Part.
type connRow struct {
	Dir, Part, Href, Via, Table, Count string
	Names, More, NoCall                []nameN
}

// nameN is a name and how often it is used.
type nameN struct {
	Name string
	N    int
}

// cpath is a call path from an entry to a finding (component 30, to a finding): the path and what the way runs.
type cpath struct {
	Path *pathView
	Says string
}

// pstep is a step of the reading order, with this part's files only.
type pstep struct {
	N                 int
	Title, Why, Check string
	Files             []*file
	Tests             []string
	More              int
	Ran               bool // a test of Tests ran here: no "not run here"
	Graph             *flow
}

// Anchor is the block's id.
func (b *pblock) Anchor() string { return b.ID }

// files is the block's files, kept and gone.
func (b *pblock) files() []*file { return slices.Concat(b.Kept, b.Gone) }

// FilesText is "{n} files", ", {j} new" or ", new", ", plus {k} deleted".
func (b *pblock) FilesText() string {
	if b.Removed {
		return plural(len(b.Gone), "file", "files") + " in " + plural(len(b.Folders), "folder", "folders") + ", all deleted"
	}
	s := plural(len(b.Kept), "file", "files")
	n := 0
	for _, f := range b.Kept {
		if f.New {
			n++
		}
	}
	switch {
	case n > 0 && n == len(b.Kept):
		s += ", new"
	case n > 0:
		s += ", " + strconv.Itoa(n) + " new"
	}
	if len(b.Gone) > 0 {
		s += ", plus " + strconv.Itoa(len(b.Gone)) + " deleted"
	}
	return s
}

// maxConn is the rows How it connects shows before "and {k} more parts", and the table rows before "and {k} more".
const maxConn, maxConnNames = 4, 3

// blocks is the part blocks, the cards on each, and the major findings in no part.
//
//nolint:gocognit // each part's files, findings and steps, step by step
func (v *view) blocks() {
	p := &v.Page
	m := v.Map
	byPart := map[string]*pblock{}
	var order []*pblock
	if m == nil || len(m.Parts) == 0 { // no map: one block holds the code
		b := &pblock{ID: "part-code", Label: "Code"}
		for _, s := range v.Steps {
			b.Kept = append(b.Kept, s.Files...)
		}
		b.Gone = slices.DeleteFunc(slices.Clone(v.Deleted), func(f *file) bool { return f.Kind != "code" })
		if len(b.Kept)+len(b.Gone) > 0 {
			order = append(order, b)
		}
	} else {
		ids := slices.Clone(m.Order)
		for i := range m.Parts {
			if !slices.Contains(ids, m.Parts[i].ID) {
				ids = append(ids, m.Parts[i].ID)
			}
		}
		for _, id := range ids {
			i := slices.IndexFunc(m.Parts, func(pt MapPart) bool { return pt.ID == id })
			pt := &m.Parts[i]
			b := &pblock{ID: "part-" + slug(pt.ID), Label: pt.Label, Path: pt.Path, Key: "part:" + pt.ID, New: pt.New}
			if pt.Path == "." {
				b.Path = ""
			}
			for _, f := range pt.Files {
				if fl := v.files[f]; fl != nil {
					if fl.Gone {
						b.Gone = append(b.Gone, fl)
					} else {
						b.Kept = append(b.Kept, fl)
					}
				}
			}
			byPart[pt.ID] = b
			order = append(order, b)
		}
		if r := m.Removed; r != nil {
			b := &pblock{ID: "part-removed", Label: "Removed", Key: "removed", Removed: true, Folders: r.Folders}
			for _, f := range v.Deleted {
				if v.partOf(f.Path) == nil && f.Kind == "code" {
					b.Gone = append(b.Gone, f)
				}
			}
			order = append(order, b)
		}
		if g := m.Generated; g != nil {
			b := &pblock{ID: "part-generated", Label: "Generated", Key: "generated", Generated: true}
			for _, f := range g.Files {
				if fl := v.files[f]; fl != nil {
					b.Kept = append(b.Kept, fl)
				}
			}
			order = append(order, b)
		}
	}
	// the cards on each part: major findings, minor ones apart; the rest go with the others
	others := &pblock{ID: "part-others", Label: "Other files", Others: true}
	for _, k := range slices.Concat(v.lead, v.found) {
		counts := k.Kind != "Minor finding" // a difference, a test group or a major finding: an open item
		if strings.HasPrefix(k.Box, "deploy:") && counts {
			continue // read in Before you merge
		}
		var b *pblock
		if pt := v.partOf(strings.SplitN(k.GHPlace, ":", 2)[0]); pt != nil {
			b = byPart[pt.ID]
		}
		if b == nil && len(order) == 1 && order[0].ID == "part-code" && slices.ContainsFunc(order[0].files(), func(f *file) bool { return strings.HasPrefix(k.GHPlace, f.Path) }) {
			b = order[0]
		}
		switch {
		case b == nil && counts:
			others.Cards = append(others.Cards, k)
		case b == nil:
			p.Minors = append(p.Minors, k)
		case counts:
			b.Cards = append(b.Cards, k)
		default:
			b.Minors = append(b.Minors, k)
		}
	}
	for _, b := range order {
		b.N = len(b.Cards)
		for _, f := range b.Kept {
			b.Add, b.Del = b.Add+f.Add, b.Del+f.Del
		}
		v.connects(b)
		v.stepsOf(b)
	}
	// open by default: the three with the most open items, a tie to the one further left
	top := slices.Clone(order)
	slices.SortStableFunc(top, func(a, b *pblock) int { return cmp.Compare(b.N, a.N) })
	for i, b := range top {
		b.Open = i < 3 && b.N > 0
	}
	if len(order) == 1 {
		order[0].Open = true
	}
	for _, b := range order {
		b.Second = v.secondLine(b)
	}
	p.Parts = order
	if len(others.Cards) > 0 {
		others.N = len(others.Cards)
		others.Open = true
		p.Others = others
	}
	if mv := p.Map; mv != nil {
		mv.items(v)
	}
}

// secondLine is a block's second line: files, lines, and when its files last changed (5.7).
func (v *view) secondLine(b *pblock) string {
	if b.Removed {
		return b.FilesText()
	}
	s := b.FilesText() + " · +" + thousands(b.Add)
	if b.Del > 0 {
		s += " −" + thousands(b.Del)
	}
	if len(v.Knowledge) == 0 {
		return s
	}
	var paths []string
	for _, f := range b.Kept {
		if !f.New {
			paths = append(paths, f.Path)
		}
	}
	g := Aggregate(v.Knowledge, paths)
	switch {
	case g.Known == 0:
	case g.Median == 0 && (g.Known == 1 || g.Oldest == 0):
		s += " · last changed the day the PR opened"
	case g.Known == 1 || g.Oldest == g.Median:
		s += " · last changed " + plural(g.Median, "day", "days") + " ago"
	default:
		s += " · last changed " + plural(g.Median, "day", "days") + " ago (median), " + strconv.Itoa(g.Oldest) + " at most"
	}
	return s
}

// connects is a block's How it connects: its lines, its tables, its outside calls, its reach (5.7).
//
//nolint:gocognit,gocyclo // the rows of How it connects, one kind of link at a time
func (v *view) connects(b *pblock) {
	m := v.Map
	if m == nil || b.Key == "" || !strings.HasPrefix(b.Key, "part:") {
		return
	}
	id := strings.TrimPrefix(b.Key, "part:")
	label := func(pid string) (string, string) {
		for _, pt := range m.Parts {
			if pt.ID == pid {
				return pt.Label, "#part-" + slug(pt.ID)
			}
		}
		return pid, ""
	}
	type sorted struct {
		r     connRow
		calls int
	}
	var rows []sorted
	names := func(ns []MapName) (calls, nocall []nameN, n int) {
		for _, x := range ns {
			if x.Kind == "named" {
				nocall = append(nocall, nameN{x.Name, x.N})
			} else {
				calls, n = append(calls, nameN{x.Name, x.N}), n+x.N
			}
		}
		by := func(a, b nameN) int { return cmp.Or(cmp.Compare(b.N, a.N), strings.Compare(a.Name, b.Name)) }
		slices.SortStableFunc(calls, by)
		slices.SortStableFunc(nocall, by)
		return calls, nocall, n
	}
	add := func(dir, other string, ns []MapName, via []string) {
		lb, href := label(other)
		r := connRow{Dir: dir, Part: lb, Href: href}
		calls, nocall, n := names(ns)
		if len(via) > 0 && strings.HasPrefix(dir, "Call") {
			r.Via = "through " + viaWords(via, true)
		}
		switch {
		case n > 0:
			r.Count = " · " + plural(n, "call", "calls") + ":"
		case len(calls) == 0:
			r.Count = " · no call:"
			calls, nocall = nocall, nil
		}
		r.Names, r.NoCall = calls, nocall
		if len(r.Names) > maxConnNames {
			r.Names, r.More = r.Names[:maxConnNames], r.Names[maxConnNames:]
		}
		rows = append(rows, sorted{r, n})
	}
	for _, l := range m.Lines {
		if l.Style == "" {
			continue
		}
		named := l.Calls+l.Through == 0
		switch id {
		case l.From: // its code is used by To
			add(map[bool]string{true: "Named by", false: "Called from"}[named], l.To, l.Names, l.Via)
			if l.TwoWay {
				add("Calls", l.To, l.Back, nil)
			}
		case l.To:
			add(map[bool]string{true: "Names", false: "Calls"}[named], l.From, l.Names, l.Via)
			if l.TwoWay {
				add("Called from", l.From, l.Back, nil)
			}
		}
	}
	slices.SortStableFunc(rows, func(a, b sorted) int {
		return cmp.Or(cmp.Compare(map[bool]int{true: 0, false: 1}[a.calls > 0], map[bool]int{true: 0, false: 1}[b.calls > 0]), cmp.Compare(b.calls, a.calls))
	})
	for i, r := range rows {
		if i < maxConn {
			b.Conn = append(b.Conn, r.r)
		} else {
			b.ConnMore = append(b.ConnMore, r.r)
		}
	}
	// the tables: writes, then reads, each in the map's order
	var trs []connRow
	for _, kind := range []string{"writes", "reads"} {
		for _, s := range m.Stores {
			for _, sp := range s.Parts {
				if sp.Part != id {
					continue
				}
				var q, c []nameN
				nq, nc := 0, 0
				for _, u := range sp.Uses {
					if u.Kind != kind {
						continue
					}
					if u.Through {
						c, nc = append(c, nameN{u.Decl, u.N}), nc+u.N
					} else {
						q, nq = append(q, nameN{u.Decl, u.N}), nq+u.N
					}
				}
				if nq+nc == 0 {
					continue
				}
				r := connRow{Dir: sentenceCase(kind), Table: s.Name}
				by := func(a, b nameN) int { return cmp.Or(cmp.Compare(b.N, a.N), strings.Compare(a.Name, b.Name)) }
				slices.SortStableFunc(q, by)
				slices.SortStableFunc(c, by)
				switch {
				case nq > 0:
					r.Count = " · " + plural(nq, "query", "queries") + ":"
					r.Names = q
					if nc > 0 { // both ways: the calls after "; and"
						r.NoCall = c
						r.Via = "; and through " + plural(nc, "call", "calls") + ":"
					}
				default:
					r.Count = " through " + plural(nc, "call", "calls") + ":"
					r.Names = c
				}
				if len(r.Names) > maxConnNames {
					r.Names, r.More = r.Names[:maxConnNames], r.Names[maxConnNames:]
				}
				trs = append(trs, r)
			}
		}
	}
	for i, r := range trs {
		if i < maxConn {
			b.Tables = append(b.Tables, r)
		} else {
			b.TablesMore = append(b.TablesMore, r)
		}
	}
	// the outside calls it holds (6.4)
	for _, s := range m.Systems {
		for _, c := range s.Calls {
			pt := v.partOf(c.Path)
			if pt == nil || !slices.Contains(s.Parts, id) {
				continue
			}
			at := "`" + path.Base(c.Path) + ":" + strconv.Itoa(c.Line) + "`"
			t := "`" + s.Name + "`, called at " + at
			if pt.ID != id {
				t += " through " + pt.Label + "."
			} else {
				t += "."
				switch {
				case c.URL != "" && strings.Contains(c.URL, "/") || strings.Contains(c.URL, "."):
					t += " Its URL is `" + c.URL + "`"
					if c.URLAt != "" {
						t += " (`" + path.Base(c.URLAt) + "`)"
					}
					t += "."
				case c.URL != "":
					t += " Its URL is read from `" + c.URL + "`"
					if c.URLAt != "" {
						t += " (`" + path.Base(c.URLAt) + "`)"
					}
					t += "."
				}
			}
			if !slices.Contains(b.Outside, t) {
				b.Outside = append(b.Outside, t)
			}
		}
	}
	// the reach: this part's rows of the reach list
	var decls []string
	places := 0
	for _, d := range m.Reach.List {
		if d.Part == id {
			b.ReachList = append(b.ReachList, d)
			decls = append(decls, "`"+d.Name+"`")
			places += d.N
		}
	}
	if places > 0 {
		b.Reach = "Outside this PR, " + plural(places, "place", "places") + " in code name " + join(decls) + "."
	}
	// the database mark: the files that imported a table a migration drops (6.11)
	files := 0
	var migs []string
	for _, t := range m.Tables {
		for _, tp := range t.Parts {
			if tp.Part == id {
				files += len(tp.Files)
				if l := migrationLabel(t.Path); !slices.Contains(migs, l) {
					migs = append(migs, l)
				}
			}
		}
	}
	if files > 0 {
		b.DB = "Before this PR, " + plural(files, "of its files", "of its files") + " imported tables that " + strings.ToLower(join(migs)) + " drops."
	}
}

// stepsOf is a block's steps with its own files, the call paths to its findings, and its files in no step.
func (v *view) stepsOf(b *pblock) {
	in := map[*file]bool{}
	for _, f := range b.Kept {
		in[f] = true
	}
	seen := map[*file]bool{}
	for i, s := range v.Steps[:v.NSteps] {
		var fs []*file
		for _, f := range s.Files {
			if in[f] {
				fs, seen[f] = append(fs, f), true
			}
		}
		if len(fs) == 0 {
			continue
		}
		ran := slices.ContainsFunc(s.Tests, func(t string) bool { _, n, _ := strings.Cut(t, " "); return ranOf(v.Data.Tests.Ran, n) != "" })
		b.Steps = append(b.Steps, pstep{N: i + 1, Title: s.Title, Why: s.Why, Check: s.Check, Files: fs, Tests: s.Tests, More: s.More, Ran: ran, Graph: s.Graph})
	}
	for _, f := range b.Kept {
		if !seen[f] {
			b.Loose = append(b.Loose, f)
		}
	}
	// a call path per finding in the part: the first way that reaches it
	var hops map[string]Hop
	for _, k := range b.Cards {
		for _, w := range v.Ways {
			if w.Finding != "#"+k.ID {
				continue
			}
			at := slices.IndexFunc(w.Rows, func(r wrow) bool { return r.St == "major" })
			if at < 0 {
				continue
			}
			chain := []wrow{w.Rows[at]}
			for d, i := w.Rows[at].D, at-1; i >= 0 && d > 0; i-- {
				if w.Rows[i].D < d {
					chain, d = append([]wrow{w.Rows[i]}, chain...), w.Rows[i].D
				}
			}
			entryPart := ""
			if pt := v.partOf(w.At.Path); pt != nil && "part:"+pt.ID != b.Key {
				entryPart = pt.Label
			}
			if hops == nil {
				hops = v.hops()
			}
			cp := cpath{Path: v.findingPath(w, chain, entryPart, b.Label, "#"+k.ID, hops), Says: w.Says()}
			b.Paths = append(b.Paths, cp)
			break
		}
	}
}

// items is what the map's boxes carry once the cards are made: each box's open items and panel, the items line.
func (mv *mapView) items(v *view) {
	p := &v.Page
	on := map[string][]*card{}
	add := func(k *card) {
		if mv.byKey[k.Box] != nil {
			on[k.Box] = append(on[k.Box], k)
		}
	}
	for _, k := range p.Before {
		add(k)
	}
	for _, b := range p.Parts {
		for _, k := range b.Cards {
			add(k)
		}
	}
	mv.OnMap = 0
	for key, ks := range on {
		mv.OnMap += len(ks)
		mv.byKey[key].Open = len(ks)
	}
	// the items line: what sits off the map
	if p.Gap != nil {
		n := 0
		for _, g := range p.Gap.Files {
			n += len(g.Files)
		}
		mv.Off = append(mv.Off, offLink{"the " + plural(n, "code file", "code files") + " the review didn't read", "#gap"})
	}
	for _, k := range p.Before {
		if mv.byKey[k.Box] == nil {
			mv.Off = append(mv.Off, offLink{strings.ToLower(k.Title[:1]) + k.Title[1:], "#" + k.ID})
		}
	}
	if p.Others != nil {
		mv.Off = append(mv.Off, offLink{plural(len(p.Others.Cards), "major finding in no part", "major findings in no part"), "#part-others"})
	}
	// the deploy boxes' second line and every box's panel
	for _, d := range mv.Deploy {
		src := v.deployOf(d.Key)
		var t []string
		if d.Open > 0 {
			t = append(t, strconv.Itoa(d.Open)+" open")
		}
		if src != nil && len(src.Drops) > 0 {
			t = append(t, "drops "+plural(len(src.Drops), "table", "tables"))
		}
		d.Line2 = strings.Join(t, " · ")
		d.Aria = d.Label + ", " + map[bool]string{true: plural(d.Open, "open item", "open items"), false: "no open items"}[d.Open > 0]
		if src != nil && len(src.Drops) > 0 {
			d.Aria += ", drops " + plural(len(src.Drops), "table", "tables")
		}
	}
	for _, b := range mv.byKey {
		b.Panel = mv.panel(v, b, on[b.Key])
	}
	for _, b := range mv.Fig { // the phone's list: the parts with lines; the rows under the figure stay rows
		if b.Kind == "part" {
			mv.List = append(mv.List, b)
		}
	}
	mv.words(v) // the boxes' names say their open items
}

// Kinds is a table panel's groups, "writes" then "reads", those it has.
func (pn *panel) Kinds() []string {
	var ks []string
	for _, k := range []string{"writes", "reads"} {
		if slices.ContainsFunc(pn.Rows, func(r prow2) bool { return r.Kind == k }) {
			ks = append(ks, k)
		}
	}
	return ks
}

// ReadN is Read the change's count: the open items in it.
func (p page) ReadN() int {
	n := 0
	for _, b := range p.Parts {
		n += b.N
	}
	if p.Others != nil {
		n += p.Others.N
	}
	if p.Gap != nil {
		n++
	}
	return n
}

// maxPanelRows is the item rows a selection panel shows before "{n} more in Read the change".
const maxPanelRows = 6

// panel is a box's selection panel (component 26).
func (mv *mapView) panel(v *view, b *mbox, ks []*card) *panel {
	pn := &panel{Head: b.Label, Path: b.Path, Kind: b.Kind, Key: b.Key}
	files := ""
	switch b.Kind {
	case "part":
		files = plural(b.Files, "file", "files")
		pn.Read, pn.ReadHref = "Read "+b.Label, "#part-"+slug(b.part.ID)
	case "system":
		pn.Code = true
		for _, s := range mv.src.Systems {
			if s.Name == b.Label && len(s.Calls) > 0 {
				pn.Path = s.Calls[0].Path + ":" + strconv.Itoa(s.Calls[0].Line)
			}
		}
	case "removed":
		r := mv.src.Removed
		pn.Count = plural(r.N, "file", "files") + " in " + plural(len(r.Folders), "folder", "folders") + ", all deleted"
		for _, f := range r.Folders {
			pn.Subs = append(pn.Subs, psub{Lines: []string{"`" + f.Path + "` · " + plural(f.N, "file", "files")}})
		}
		pn.Read, pn.ReadHref = "Read Removed", "#part-removed"
		return pn
	case "generated":
		pn.Count = plural(mv.src.Generated.N, "file", "files")
		pn.Read, pn.ReadHref = "Read Generated", "#part-generated"
		return pn
	case "table":
		return mv.tablePanel(v, b)
	}
	open := "No open items"
	if len(ks) > 0 {
		open = plural(len(ks), "open item", "open items")
	}
	pn.Count = open
	if files != "" {
		pn.Count += " · " + files
	}
	for i, k := range ks {
		if i == maxPanelRows {
			pn.More = len(ks) - i
			break
		}
		pn.Rows = append(pn.Rows, prow2{Kind: k.Kind, Title: k.Title, Place: k.Place, Href: "#" + k.ID})
	}
	if d := v.deployOf(b.Key); d != nil && len(d.Drops) > 0 {
		s := psub{Head: "Tables it drops", Chip: evDiff, Lines: []string{join(codeAll(d.Drops)) + "."}}
		parts := map[string]int{}
		var order []string
		tests := 0
		for _, t := range mv.src.Tables {
			if t.Path != d.Path {
				continue
			}
			tests += t.Tests
			for _, tp := range t.Parts {
				if parts[tp.Part] == 0 {
					order = append(order, tp.Part)
				}
				parts[tp.Part] += len(tp.Files)
			}
		}
		if len(order) > 0 {
			n := 0
			var ls []string
			for _, id := range order {
				n += parts[id]
				lb := id
				if pb := mv.partBox[id]; pb != nil {
					lb = pb.Label
				}
				ls = append(ls, lb+" · "+plural(parts[id], "file", "files"))
			}
			s.Lines = append(s.Lines, "Before this PR, "+plural(n, "code file", "code files")+" in "+plural(len(order), "part", "parts")+" imported them:")
			s.Lines = append(s.Lines, ls...)
		}
		if tests > 0 {
			s.Lines = append(s.Lines, plural(tests, "test file", "test files")+" did too.")
		}
		pn.Subs = append(pn.Subs, s)
	}
	return pn
}

// tablePanel is a table's selection panel (6.13): its writers and readers, its other names, its migrations.
//
//nolint:gocognit // a table's writers, readers and migrations, step by step
func (mv *mapView) tablePanel(v *view, b *mbox) *panel {
	var s *MapStore
	for i := range mv.src.Stores {
		if mv.src.Stores[i].Name == b.Label {
			s = &mv.src.Stores[i]
		}
	}
	pn := &panel{Head: b.Label, Code: true, Path: "named in SQL", Kind: "table", Key: b.Key}
	if s.Path != "" {
		pn.Path = s.Path + ":" + strconv.Itoa(s.Line)
	}
	total := 0
	for _, kind := range []string{"writes", "reads"} {
		for _, sp := range s.Parts {
			nq, nc := 0, 0
			var first string
			var via []string
			for _, u := range sp.Uses {
				if u.Kind != kind {
					continue
				}
				if u.Through {
					nc += u.N
					if pt := v.partOf(strings.SplitN(u.At, ":", 2)[0]); pt != nil && pt.ID != sp.Part {
						via = appendNew(via, pt.Label)
					}
				} else {
					if first == "" {
						first = u.Decl
					} else {
						first = "-"
					}
					nq += u.N
				}
			}
			if nq+nc == 0 {
				continue
			}
			total += nq
			lb := sp.Part
			if pb := mv.partBox[sp.Part]; pb != nil {
				lb = pb.Label
			}
			var bits []string
			if nq > 0 {
				q := plural(nq, "query", "queries")
				if first != "-" && first != "" {
					q += ", in `" + first + "`"
				}
				bits = append(bits, q)
			}
			if nc > 0 {
				c := plural(nc, "call", "calls")
				if len(via) > 0 {
					c += " through " + join(via)
				}
				if nq > 0 {
					c = "and " + c
				}
				bits = append(bits, c)
			}
			pn.Rows = append(pn.Rows, prow2{Kind: kind, Title: lb, Place: strings.Join(bits, ", "), Href: "#part-" + slug(sp.Part)})
		}
	}
	pn.Count = plural(len(s.Parts), "part", "parts") + " · " + plural(total, "query", "queries")
	var tail []string
	if s.Subquery {
		tail = append(tail, "A subquery the project declares. The scan doesn't follow it to the tables it reads.")
	}
	for _, n := range s.Names {
		t := "Also `" + n.Name + "`"
		if n.At != "" {
			t += " at `" + path.Base(n.At) + "`"
		}
		tail = append(tail, t+".")
	}
	if len(tail) > 0 {
		pn.Subs = append(pn.Subs, psub{Lines: tail})
	}
	for _, mg := range s.Migrations {
		at := make([]string, len(mg.Lines))
		for i, l := range mg.Lines {
			at[i] = "`:" + strconv.Itoa(l) + "`"
		}
		t := mg.Label + " " + map[string]string{"created": "creates", "dropped": "drops"}[mg.Kind]
		if mg.Kind != "created" && mg.Kind != "dropped" {
			t = mg.Label + " changes"
		}
		t += " it"
		if len(at) > 0 {
			t += " at " + join(at)
		}
		pn.Subs = append(pn.Subs, psub{Chip: evDiff, Lines: []string{t + "."}})
	}
	return pn
}
