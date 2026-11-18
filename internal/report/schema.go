package report

import (
	"cmp"
	"context"
	"diffharbor/internal/proc"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

// Table is a table the PR's migrations create, change or drop (decision 42), as they leave it. Kind "created",
// "changed" or "dropped", or "context": a table outside the PR that one of its keys joins, with only the columns the
// key names. Path:Line the PR's first statement that names it. Base: the base's migrations declare it, so Cols holds
// the columns the PR leaves alone too, and a change says what the column was. Risks: what the statements may break,
// read from the schema alone.
type Table struct {
	Name, Kind, Path string
	Line             int
	Base             bool
	Cols             []Column
	Indexes          []Index
	Risks            []string
	Joins            []string // a join table's: the tables it joins many to many
}

// Column is one column. State "new", "changed", "renamed", "dropped", or "" when the PR leaves it alone; Was what a
// change changed ("text, nullable"), a rename's old name; Ref the table.column it references, OnDelete and OnUpdate
// its key's actions ("cascade"); Guess: no key, the column's name points there (customer_id); Line the PR's line that
// changes it.
type Column struct {
	Name, Type, State, Was, Ref string
	OnDelete, OnUpdate          string
	Guess, Part                 bool     // Part: its primary key or unique constraint spans other columns too
	Notes                       []string // "primary key", "not null", "unique", "default now()"
	Line                        int
}

// Index is an index the PR creates or drops: State "new" or "dropped".
type Index struct {
	Name, Cols, State string
	Unique            bool
	Line              int
}

// The schema is read from the migrations' text, not a database: the statements most migration tools write. What
// the parser doesn't know (a trigger, a function, a view) it skips.
var (
	ddlCreate    = regexp.MustCompile(`(?is)^CREATE\s+(?:(?:GLOBAL|LOCAL)\s+)?(?:TEMP(?:ORARY)?\s+|UNLOGGED\s+)?TABLE\s+(IF\s+NOT\s+EXISTS\s+)?([^\s(]+)\s*\(`)
	ddlAlter     = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?(\S+)\s+(.+)$`)
	ddlIndex     = regexp.MustCompile(`(?is)^CREATE\s+(UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?(\S+?\s+)?ON\s+(?:ONLY\s+)?([^\s(]+)\s*(?:USING\s+\w+\s*)?\(`)
	ddlDropIndex = regexp.MustCompile(`(?is)^DROP\s+INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+EXISTS\s+)?([^\s,;]+)`)
	ddlDrop      = regexp.MustCompile(`(?is)^DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?(.+?)(?:\s+(?:CASCADE|RESTRICT))?$`)
	// colStop ends a column's type: what follows is a constraint
	colStop = map[string]bool{
		"NOT": true, "NULL": true, "DEFAULT": true, "PRIMARY": true, "REFERENCES": true, "UNIQUE": true, "CHECK": true,
		"CONSTRAINT": true, "GENERATED": true, "COLLATE": true, "AUTO_INCREMENT": true, "AUTOINCREMENT": true, "COMMENT": true,
	}
)

// scol and stab are a column and a table as the statements so far leave them. line: the PR's line that last
// changed the column; from: its name before a rename in the PR; gone: the PR drops it; guess: the PR changes a
// column the base doesn't declare.
type scol struct {
	name, typ, def, ref string
	onDelete, onUpdate  string
	notNull, pk, unique bool
	keyPos              int // its place in a key or unique constraint of several columns, from 1; 0 for one
	from                string
	line                int
	gone, guess         bool
	auto                bool // its value fills itself: GENERATED, AUTO_INCREMENT
}

type stab struct {
	name          string
	cols          []*scol
	idx           []Index
	named         map[string]named // a constraint's name, lower case, as declared or as Postgres names it
	from          string           // its name before the PR renames it
	created, gone bool
}

// named is a constraint by name: kind "p" primary key, "u" unique, "f" foreign key, and its columns.
type named struct {
	kind string
	cols []string
}

type sdb map[string]*stab

type sqlLine struct {
	text string
	line int
	add  bool   // the PR adds the line; a statement: it adds a line of it or removes one inside it
	gone string // the lines the PR removes right before the line, each with its newline
}

// schema is the tables the PR's migrations name (decision 42): the base's migrations that name them (base, nil for
// none), then the PR's statements in what runs on migrate (upStatements).
func schema(d Data, base func(names []string) sdb) []Table {
	type site struct {
		path string
		line int
	}
	var order []string
	first := map[string]site{}
	var files []string
	pr := map[string][]sqlLine{} // the statements the PR changes, whole: a column added to a CREATE TABLE too
	old := upStatements(d, func(p string, st sqlLine) {
		if _, ok := pr[p]; !ok {
			files = append(files, p)
		}
		pr[p] = append(pr[p], st)
	})
	touch := func(p string, line int) func(string) {
		return func(name string) {
			if _, ok := first[name]; !ok {
				first[name] = site{p, line}
				order = append(order, name)
			}
		}
	}
	probe := sdb{} // the PR's statements alone, to learn the tables they name
	for _, p := range files {
		for _, st := range pr[p] {
			probe.apply(st, true, touch(p, st.line))
		}
	}
	was := sdb{}
	fill := func(names []string) { // the base's tables of names that was lacks
		for n, t := range base(names) {
			if was[n] == nil {
				was[n] = t
			}
		}
	}
	if base != nil {
		if len(order) > 0 {
			fill(order)
		}
		if more := lostIndexes(files, pr, probe, was, base, func(p string, line int, table string) { touch(p, line)(table) }); len(more) > 0 {
			fill(more)
		}
		// the tables the keys reach that the files naming the PR's don't declare: what a key references, what a
		// column's name points at
		if more := joined(was, probe, order); len(more) > 0 {
			fill(more)
		}
	}
	if len(order) == 0 {
		return nil
	}
	head, before := was.clone(), sdb{}
	for _, p := range files {
		before.read(old[p])
	}
	for _, p := range files {
		for _, st := range pr[p] {
			if !head.recreate(st, before) {
				head.apply(st, true, func(string) {})
			}
		}
	}
	out := make([]Table, 0, len(order))
	moved := map[string]bool{} // the old names of the tables the PR renames: their new name has the card
	for _, h := range head {
		if h.from != "" {
			moved[h.from] = true
		}
	}
	for _, name := range order {
		h, b := head[name], was[name]
		if h == nil && moved[name] {
			continue
		}
		if h != nil && h.from != "" {
			b = was[h.from]
		}
		t := compare(b, h)
		t.Name, t.Path, t.Line = name, first[name].path, first[name].line
		t.Risks = schemaRisks(t, b, h, head)
		if h != nil && h.from != "" {
			t.Risks = append(t.Risks, fmt.Sprintf("Renamed from `%s`: code and SQL that still name it break.", h.from))
		}
		t.Joins = h.joins()
		out = append(out, t)
	}
	for i := range out {
		for j, c := range out[i].Cols {
			if c.Ref == "" && c.State != "dropped" {
				out[i].Cols[j].Ref = head.byName(c.Name)
				out[i].Cols[j].Guess = out[i].Cols[j].Ref != ""
			}
		}
	}
	return append(out, around(out, head)...)
}

// lostIndexes is the tables outside was of the indexes the PR drops that neither its own statements nor was create:
// the base's migrations that name an index declare its table. found hears each drop's site and its table.
func lostIndexes(files []string, pr map[string][]sqlLine, probe, was sdb, base func([]string) sdb, found func(p string, line int, table string)) []string {
	type site struct {
		path string
		line int
	}
	held := map[string]bool{}
	for _, db := range []sdb{probe, was} {
		for _, t := range db {
			for _, ix := range t.idx {
				held[ix.Name] = true
			}
		}
	}
	at := map[string]site{}
	for _, p := range files {
		for _, st := range pr[p] {
			if m := ddlDropIndex.FindStringSubmatch(st.text); m != nil && !held[bareName(m[1])] {
				at[bareName(m[1])] = site{p, st.line}
			}
		}
	}
	if len(at) == 0 {
		return nil
	}
	var more []string
	db := base(slices.Sorted(maps.Keys(at)))
	for _, table := range slices.Sorted(maps.Keys(db)) {
		for _, ix := range db[table].idx {
			if s, ok := at[ix.Name]; ok {
				found(s.path, s.line, table)
				if was[table] == nil && !slices.Contains(more, table) {
					more = append(more, table)
				}
			}
		}
	}
	return more
}

// capped notes on each card of the PR's tables that the base had more migrations naming them than were read: a
// table they declare, or a change they make to one, may be missing.
func capped(ts []Table) {
	for i := range ts {
		if ts[i].Kind != "context" {
			ts[i].Risks = append(ts[i].Risks, fmt.Sprintf("More than %d of the base's migrations name these tables: only the first %d were read, so what later ones declare or change may be missing here.", maxTableFiles, maxTableFiles))
		}
	}
}

// maxContext caps the cards of tables outside the PR.
const maxContext = 6

// joined is the tables the PR's tables' keys and key-like names reach that dbs don't declare, sorted.
func joined(was, probe sdb, order []string) []string {
	var out []string
	add := func(n string) {
		if n != "" && was[n] == nil && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	for _, db := range []sdb{was, probe} {
		for _, name := range order {
			if t := db[name]; t != nil {
				for _, c := range t.cols {
					ref, _, _ := strings.Cut(c.ref, ".")
					add(ref)
					if c.ref == "" {
						for _, n := range keyNames(c.name) {
							add(n)
						}
					}
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// keyNames is the tables a column's name points at: customer_id or customerId is customer, customers.
func keyNames(col string) []string {
	var stem string
	switch {
	case len(col) > 3 && strings.HasSuffix(strings.ToLower(col), "_id"):
		stem = strings.ToLower(col[:len(col)-3])
	case len(col) > 2 && strings.HasSuffix(col, "Id"):
		var b strings.Builder
		for i, r := range col[:len(col)-2] {
			if i > 0 && r >= 'A' && r <= 'Z' {
				b.WriteByte('_')
			}
			b.WriteRune(r)
		}
		stem = strings.ToLower(b.String())
	default:
		return nil
	}
	out := []string{stem, stem + "s", stem + "es"}
	if s, ok := strings.CutSuffix(stem, "y"); ok {
		out = append(out, s+"ies")
	}
	return out
}

// byName is table.column the name of a column without a key points at, its key or id; "" for none.
func (db sdb) byName(col string) string {
	for _, n := range keyNames(col) {
		if t := db[n]; t != nil && !t.gone {
			return n + "." + t.key()
		}
	}
	return ""
}

// joins is the tables t joins many to many when it is a join table: two keys or more, to different tables, that
// share its primary key or a unique constraint, or no other column but an id and timestamps.
func (t *stab) joins() []string {
	if t == nil || t.gone {
		return nil
	}
	var to []string
	keyed, bare := 0, true
	for _, c := range t.cols {
		ref, _, _ := strings.Cut(c.ref, ".")
		switch {
		case c.gone:
		case ref != "":
			if !slices.Contains(to, ref) {
				to = append(to, ref)
			}
			if c.keyPos > 0 {
				keyed++
			}
		case !c.pk && !strings.HasSuffix(strings.ToLower(c.name), "_at"):
			bare = false
		}
	}
	if len(to) < 2 || keyed < 2 && !bare {
		return nil
	}
	return to
}

// key is a table's primary key column, id when it declares none.
func (t *stab) key() string {
	for _, c := range t.cols {
		if c.pk && !c.gone {
			return c.name
		}
	}
	return "id"
}

// around is the cards of tables outside the PR its tables' keys join (decision 42): what a key references, with
// that column; what references a PR table, with the referencing column.
func around(out []Table, head sdb) []Table {
	inPR := map[string]bool{}
	for _, t := range out {
		inPR[t.Name] = true
	}
	var names []string
	cols := map[string][]string{}
	want := func(t, col string) {
		if cols[t] == nil {
			if len(names) >= maxContext {
				return
			}
			names = append(names, t)
		}
		if !slices.Contains(cols[t], col) {
			cols[t] = append(cols[t], col)
		}
	}
	for _, t := range out {
		for _, c := range t.Cols {
			ref, col, _ := strings.Cut(c.Ref, ".")
			if h := head[ref]; ref != "" && !inPR[ref] && h != nil && !h.gone {
				want(ref, cmp.Or(col, h.key()))
			}
		}
	}
	for _, n := range slices.Sorted(maps.Keys(head)) {
		if h := head[n]; !inPR[n] && !h.gone {
			for _, c := range h.cols {
				if ref, _, _ := strings.Cut(c.ref, "."); inPR[ref] {
					want(n, c.name)
				}
			}
		}
	}
	ctx := make([]Table, 0, len(names))
	for _, n := range names {
		t := Table{Name: n, Kind: "context", Base: true}
		for _, c := range head[n].cols {
			if slices.Contains(cols[n], c.name) {
				t.Cols = append(t.Cols, c.column())
			}
		}
		ctx = append(ctx, t)
	}
	return ctx
}

// schemaRisks is what the PR's statements to table t may break, read from the schema alone: b and h the table in the base
// and as the PR leaves it, head every table as it does.
func schemaRisks(t Table, b, h *stab, head sdb) []string {
	var out []string
	if t.Kind == "dropped" {
		if by := head.refsTo(t.Name, ""); by != "" {
			out = append(out, by+" still references it.")
		}
		return out
	}
	if h == nil {
		return nil
	}
	for _, c := range h.cols {
		if c.line == 0 {
			continue
		}
		var was *scol
		if b != nil {
			was = b.find(cmp.Or(c.from, c.name))
		}
		switch {
		case c.gone:
			if by := head.refsTo(t.Name, c.name); by != "" {
				out = append(out, fmt.Sprintf("`%s` is dropped while %s still references it.", c.name, by))
			}
			continue
		case !h.created && was == nil && !c.guess && (c.notNull || c.pk) && (c.def == "" || strings.EqualFold(c.def, "null")) && !c.auto && !strings.Contains(c.typ, "serial"):
			out = append(out, fmt.Sprintf("`%s` is NOT NULL with no default: adding it fails while `%s` has rows.", c.name, t.Name))
		case was != nil && !was.notNull && !was.pk && (c.notNull || c.pk): // a primary key is NOT NULL too
			out = append(out, fmt.Sprintf("Setting `%s` NOT NULL fails while any row holds a null.", c.name))
		}
		out = append(out, keyRisks(c, was, h, head)...)
	}
	return out
}

// keyRisks is what the key of a column the PR touches may break: a type its target doesn't share, no index.
func keyRisks(c, was *scol, h *stab, head sdb) []string {
	ref, col, _ := strings.Cut(c.ref, ".")
	if ref == "" {
		return nil
	}
	var out []string
	if to := head[ref]; to != nil { // a target the migrations don't declare has no types to compare; the index check stands
		if tc := to.find(cmp.Or(col, to.key())); tc != nil && c.typ != "" && tc.typ != "" && keyType(c.typ) != keyType(tc.typ) {
			out = append(out, fmt.Sprintf("`%s` is %s, `%s.%s` is %s: the key compares across types.", c.name, c.typ, ref, tc.name, tc.typ))
		}
	}
	indexed := (c.pk || c.unique) && c.keyPos <= 1 || slices.ContainsFunc(h.idx, func(ix Index) bool {
		cols := strings.FieldsFunc(ix.Cols, func(r rune) bool { return r == ',' || r == ' ' })
		return ix.State != "dropped" && len(cols) > 0 && strings.EqualFold(bareName(cols[0]), c.name)
	})
	if !indexed && (was == nil || was.ref == "") {
		out = append(out, fmt.Sprintf("No index on `%s`: Postgres doesn't create one for a foreign key.", c.name))
	}
	return out
}

// keyType is a column type as a key compares it: serial is integer, int8 bigint.
func keyType(t string) string {
	t = strings.ToLower(t)
	switch t {
	case "serial", "int", "int4", "serial4":
		return "integer"
	case "bigserial", "int8", "serial8":
		return "bigint"
	case "smallserial", "int2", "serial2":
		return "smallint"
	}
	return t
}

// refsTo is the columns of tables the PR keeps that reference table (its column col; "" any, from other tables
// only): "`invoices.order_id`".
func (db sdb) refsTo(table, col string) string {
	var by []string
	pk := "" // REFERENCES table with no column names its primary key, dropped or not
	if t := db[table]; t != nil {
		if i := slices.IndexFunc(t.cols, func(c *scol) bool { return c.pk }); i >= 0 {
			pk = t.cols[i].name
		}
	}
	for _, n := range slices.Sorted(maps.Keys(db)) {
		t := db[n]
		if t.gone {
			continue
		}
		for _, c := range t.cols {
			ref, rc, _ := strings.Cut(c.ref, ".")
			rc = cmp.Or(rc, pk)
			self := strings.EqualFold(n, table)
			itself := self && strings.EqualFold(c.name, col) // the dropped column referencing itself
			if !c.gone && strings.EqualFold(ref, table) && (col == "" && !self || col != "" && strings.EqualFold(rc, col) && !itself) {
				by = append(by, "`"+n+"."+c.name+"`")
			}
		}
	}
	return strings.Join(by, ", ")
}

// compare is a table as the PR leaves it, h, against the base's b: each column and index marked by what the PR does.
func compare(b, h *stab) Table {
	t := Table{Base: b != nil, Kind: "changed"}
	switch {
	case h == nil || h.gone:
		t.Kind = "dropped"
		if b != nil {
			for _, c := range b.cols {
				t.Cols = append(t.Cols, c.column())
			}
		}
		return t
	case b == nil && h.created:
		t.Kind = "created"
	}
	for _, c := range h.cols {
		col := c.column()
		var was *scol
		if b != nil {
			was = b.find(cmp.Or(c.from, c.name))
		}
		switch {
		case t.Kind == "created":
		case c.gone:
			col.State = "dropped"
			if was != nil {
				col.Type = was.typ
			}
		case c.from != "" && c.from != c.name && (was != nil || c.guess):
			col.State, col.Was = "renamed", c.from
		case was == nil && c.guess:
			col.State = "changed"
		case was == nil:
			col.State = "new"
		default:
			if w := was.differs(c); w != "" {
				col.State, col.Was = "changed", w
			}
		}
		if col.State == "" && t.Kind != "created" {
			col.Line = 0
		}
		t.Cols = append(t.Cols, col)
	}
	for _, ix := range h.idx {
		switch {
		case ix.Line == 0:
			continue
		case ix.State == "dropped":
		case b != nil && slices.ContainsFunc(b.idx, func(x Index) bool { return x.Name == ix.Name && ix.Name != "" }):
			continue // the PR re-creates it
		default:
			ix.State = "new"
		}
		t.Indexes = append(t.Indexes, ix)
	}
	return t
}

// baseSchema is the tables names as the base's .sql files that name them declare them, read at refs/base: the files
// in path order, which is the order migration tools number them in. partial: some files that name them went unread.
func baseSchema(ctx context.Context, sb *proc.Sandbox, names []string) (db sdb, partial bool) {
	failed := false // a search git didn't finish
	naming := func(names []string) []string {
		args := []string{"grep", "-l", "-i", "-w", "-I"}
		for _, n := range names {
			args = append(args, "-e", n)
		}
		out, err := proc.Run(sb.Command(ctx, sb.Repo(), nil, "git", append(args, "refs/base", "--", "*.sql")...))
		var exit *exec.ExitError
		if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) { // exit 1: nothing found
			failed = true
		}
		var specs []string
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if l != "" && !strings.HasSuffix(l, ".down.sql") {
				specs = append(specs, l)
			}
		}
		return specs
	}
	read := func(specs []string) {
		slices.Sort(specs)
		partial = len(specs) > maxTableFiles
		if partial {
			specs = specs[:maxTableFiles]
		}
		texts := catFiles(ctx, sb, specs)
		partial = partial || len(texts) < len(specs) // a blob too big, or past what one read takes
		db = sdb{}
		for _, sp := range specs {
			db.read(texts[sp])
		}
	}
	specs := naming(names)
	read(specs)
	// a later migration may drop an index without naming its table: the files naming the indexes count too
	var idx []string
	for _, t := range db {
		for _, ix := range t.idx {
			if ix.Name != "" {
				idx = append(idx, ix.Name)
			}
		}
	}
	if len(idx) > 0 {
		slices.Sort(idx)
		all := slices.Concat(specs, naming(slices.Compact(idx)))
		slices.Sort(all)
		if all = slices.Compact(all); len(all) > len(specs) {
			read(all)
		}
	}
	return db, partial || failed
}

// read runs the statements of one migration's text, up to its Down part.
func (db sdb) read(text string) {
	var lines []sqlLine
	in := ""
	for i, l := range strings.Split(text, "\n") {
		if in == "" && downMark.MatchString(l) {
			break
		}
		l, in = uncomment(l, in)
		lines = append(lines, sqlLine{text: l, line: i + 1})
	}
	for _, st := range statements(lines) {
		db.apply(st, false, func(string) {})
	}
}

// statements joins lines into statements at the semicolons outside quotes and dollar-quoted bodies, comments cut;
// each starts at the line of its first word, or of the PR's first change to it.
func statements(lines []sqlLine) []sqlLine {
	var out []sqlLine
	var b strings.Builder
	start, in, add := 0, "", false // in: what closes the quote or dollar-quoted body the text is in
	for _, l := range lines {
		if l.gone != "" && b.Len() > 0 && !add { // lines removed inside the statement
			add, start = true, l.line
		}
		t := l.text
		for i := 0; i < len(t); i++ {
			c := t[i]
			switch {
			case in == escString && c == '\\' && i+1 < len(t):
				b.WriteByte(c)
				i++
				c = t[i]
			case in != "":
				if closer := strings.TrimPrefix(in, `\`); strings.HasPrefix(t[i:], closer) {
					b.WriteString(closer)
					i += len(closer) - 1
					in = ""
					continue
				}
			case c == '\'':
				in = quoteOpen(t, i)
			case c == '$' && dollarOpen.MatchString(t[i:]):
				in = dollarOpen.FindString(t[i:])
				b.WriteString(in)
				i += len(in) - 1
				continue
			case strings.HasPrefix(t[i:], "--"):
				i = len(t)
				continue
			case c == ';':
				if s := strings.TrimSpace(b.String()); s != "" {
					out = append(out, sqlLine{text: s, line: start, add: add})
				}
				b.Reset()
				add = false
				continue
			}
			if b.Len() == 0 && (c == ' ' || c == '\t') {
				continue
			}
			if b.Len() == 0 {
				start = l.line
			}
			if l.add && !add {
				add, start = true, l.line
			}
			b.WriteByte(c)
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		out = append(out, sqlLine{text: s, line: start, add: add})
	}
	return out
}

// apply runs one statement on db. pr: the PR's, which marks what it changes and keeps what it drops; touch hears each
// table it names.
func (db sdb) apply(s sqlLine, pr bool, touch func(string)) {
	text, line := s.text, s.line
	if !pr {
		line = 0
	}
	if m := ddlCreate.FindStringSubmatchIndex(text); m != nil {
		name := bareName(text[m[4]:m[5]])
		touch(name)
		if old := db[name]; old != nil && !old.gone && m[2] >= 0 { // IF NOT EXISTS
			return
		}
		body, _ := inParens(text, m[1]-1)
		t := &stab{name: name, created: pr}
		db[name] = t
		var keys [][]string
		for _, def := range splitTop(body) { // the columns first: a table constraint may come before them
			switch ws := words(def); {
			case len(ws) == 0:
			case isConstraint(ws):
				keys = append(keys, ws)
			default:
				t.add(ws, line)
			}
		}
		for _, ws := range keys {
			t.constraint(ws, line)
		}
		return
	}
	if m := ddlAlter.FindStringSubmatch(text); m != nil {
		name := bareName(m[1])
		touch(name)
		t := db.table(name)
		if ws := words(m[2]); len(ws) == 3 && strings.EqualFold(ws[0], "RENAME") && strings.EqualFold(ws[1], "TO") {
			db.move(t, bareName(ws[2]), pr)
			touch(t.name)
			return
		}
		for _, a := range splitTop(m[2]) {
			if from, to := t.alter(words(a), line, pr); from != "" {
				db.retarget(t.name, from, to)
			}
		}
		return
	}
	if m := ddlIndex.FindStringSubmatchIndex(text); m != nil {
		name := bareName(text[m[6]:m[7]])
		touch(name)
		cols, _ := inParens(text, m[1]-1)
		ix := Index{Cols: strings.Join(strings.Fields(cols), " "), Unique: m[2] >= 0, Line: line}
		if m[4] >= 0 {
			ix.Name = bareName(strings.TrimSpace(text[m[4]:m[5]]))
		}
		t := db.table(name)
		t.idx = append(t.idx, ix)
		return
	}
	if m := ddlDropIndex.FindStringSubmatch(text); m != nil {
		n := bareName(m[1])
		for name, t := range db {
			for i := range t.idx {
				if t.idx[i].Name != n || t.idx[i].State == "dropped" {
					continue
				}
				touch(name)
				if pr {
					t.idx[i].State, t.idx[i].Line = "dropped", line
				} else {
					t.idx = slices.Delete(t.idx, i, i+1)
				}
				return
			}
		}
		return
	}
	if m := ddlDrop.FindStringSubmatch(text); m != nil {
		for _, n := range strings.Split(m[1], ",") {
			if fs := strings.Fields(n); len(fs) > 0 {
				name := bareName(fs[0])
				touch(name)
				if pr {
					db.table(name).gone = true
				} else {
					delete(db, name)
				}
			}
		}
	}
}

// move renames table t to "to", in what references it too; from keeps the PR's first old name.
func (db sdb) move(t *stab, to string, pr bool) {
	if pr && t.from == "" {
		t.from = t.name
	}
	for _, o := range db { // its own keys too
		for _, c := range o.cols {
			if ref, col, _ := strings.Cut(c.ref, "."); strings.EqualFold(ref, t.name) {
				c.ref = strings.TrimSuffix(to+"."+col, ".")
			}
		}
	}
	delete(db, t.name)
	t.name, db[to] = to, t
}

// retarget points the keys that reference table.from at table.to: the column was renamed.
func (db sdb) retarget(table, from, to string) {
	for _, o := range db {
		for _, c := range o.cols {
			if strings.EqualFold(c.ref, table+"."+from) {
				c.ref = table + "." + to
			}
		}
	}
}

func (db sdb) table(name string) *stab {
	if db[name] == nil {
		db[name] = &stab{name: name}
	}
	return db[name]
}

// clone is a deep copy: the PR's statements change the head's columns, not the base's.
func (db sdb) clone() sdb {
	out := sdb{}
	for n, t := range db {
		c := *t
		c.cols = make([]*scol, len(t.cols))
		for i, col := range t.cols {
			x := *col
			c.cols[i] = &x
		}
		c.idx = slices.Clone(t.idx)
		c.named = maps.Clone(t.named)
		out[n] = &c
	}
	return out
}

func (t *stab) find(name string) *scol {
	for _, c := range t.cols {
		if strings.EqualFold(c.name, name) && !c.gone {
			return c
		}
	}
	return nil
}

// col is the column name, marked as changed at line; the PR may change one the base doesn't declare (a guess), the
// base's own statements only one it does.
func (t *stab) col(name string, line int, pr bool) *scol {
	if c := t.find(name); c != nil {
		if pr {
			c.line = line
		}
		return c
	}
	c := &scol{name: name, guess: true, line: line}
	if pr {
		t.cols = append(t.cols, c)
	}
	return c
}

// alter runs one action of an ALTER TABLE: ADD, DROP, ALTER, MODIFY, CHANGE, RENAME.
// from and to: the column it renames.
func (t *stab) alter(ws []string, line int, pr bool) (from, to string) {
	if len(ws) < 2 {
		return
	}
	op := strings.ToUpper(ws[0])
	ws = skip(ws[1:], "COLUMN")
	switch op {
	case "ADD":
		guarded := len(ws) > 0 && strings.EqualFold(ws[0], "IF")
		ws = skip(ws, "IF", "NOT", "EXISTS")
		if len(ws) == 0 || t.constraint(ws, line) || guarded && t.find(bareName(ws[0])) != nil {
			return // IF NOT EXISTS on a column it has: it stays as it is
		}
		t.add(ws, line)
	case "DROP":
		ws = skip(ws, "IF", "EXISTS")
		if len(ws) == 0 {
			return
		}
		switch strings.ToUpper(ws[0]) {
		case "CONSTRAINT", "FOREIGN": // FOREIGN KEY name: MySQL's
			if ws = skip(ws[1:], "KEY", "IF", "EXISTS"); len(ws) > 0 {
				t.unname(bareName(ws[0]), line, pr)
			}
			return
		case "PRIMARY":
			t.unname(t.name+"_pkey", line, pr)
			return
		case "INDEX", "KEY", "CHECK":
			return
		}
		c := t.col(bareName(ws[0]), line, pr)
		if pr {
			c.gone = true
		} else {
			t.cols = slices.DeleteFunc(t.cols, func(x *scol) bool { return x == c })
		}
	case "ALTER":
		if len(ws) < 3 {
			return
		}
		c, r := t.col(bareName(ws[0]), line, pr), upper(ws[1:])
		switch {
		case r[0] == "TYPE":
			c.typ = typeOf(ws[2:])
		case len(r) > 3 && r[0] == "SET" && r[1] == "DATA":
			c.typ = typeOf(ws[4:])
		case len(r) > 2 && r[1] == "NOT" && r[2] == "NULL":
			c.notNull = r[0] == "SET"
		case len(r) > 2 && r[0] == "SET" && r[1] == "DEFAULT":
			c.def = ws[3]
		case r[0] == "DROP" && r[1] == "DEFAULT":
			c.def = ""
		}
	case "MODIFY", "CHANGE":
		old := ""
		if op == "CHANGE" {
			old, ws = bareName(ws[0]), ws[1:]
		}
		if len(ws) == 0 {
			return
		}
		n, _ := colDef(ws)
		c := t.col(cmp.Or(old, n.name), line, pr)
		if !strings.EqualFold(c.name, n.name) {
			if pr && c.from == "" {
				c.from = c.name
			}
			from, to = c.name, n.name
		}
		t.rename(c, n.name)
		c.typ, c.def, c.notNull, c.pk, c.unique = n.typ, n.def, n.notNull, n.pk, n.unique
	case "RENAME":
		if len(ws) < 3 || !strings.EqualFold(ws[1], "TO") || strings.EqualFold(ws[0], "TO") {
			return // RENAME TO: the table's own name, which the page doesn't follow
		}
		c := t.col(bareName(ws[0]), line, pr)
		if pr && c.from == "" {
			c.from = c.name
		}
		from, to = c.name, bareName(ws[2])
		t.rename(c, to)
	}
	return from, to
}

// rename renames column c, in the constraints that name it too. The lists are replaced, not changed: a clone shares
// them.
func (t *stab) rename(c *scol, to string) {
	for n, k := range t.named {
		if i := slices.IndexFunc(k.cols, func(x string) bool { return strings.EqualFold(x, c.name) }); i >= 0 {
			k.cols = slices.Clone(k.cols)
			k.cols[i] = to
			t.named[n] = k
		}
	}
	c.name = to
}

// constraint reads a table constraint (PRIMARY KEY, UNIQUE, FOREIGN KEY) onto its columns; false: ws is a column.
func (t *stab) constraint(ws []string, line int) bool {
	if !isConstraint(ws) {
		return false
	}
	name := ""
	if strings.EqualFold(ws[0], "CONSTRAINT") {
		if len(ws) < 3 {
			return true
		}
		name, ws = bareName(ws[1]), ws[2:]
	}
	kw := constraintWord(ws)
	cols, rest := firstList(ws)
	pr := line > 0
	switch kw {
	case "PRIMARY", "UNIQUE":
		for i, n := range cols {
			c := t.col(n, line, pr)
			c.pk, c.unique = c.pk || kw == "PRIMARY", c.unique || kw == "UNIQUE"
			if len(cols) > 1 {
				c.keyPos = i + 1
			}
		}
		t.keep(pick(kw == "PRIMARY", "p", "u"), name, cols)
	case "FOREIGN":
		if len(cols) > 0 && len(rest) > 1 && strings.EqualFold(rest[0], "REFERENCES") {
			to, refs := refsOf(rest[1:])
			for i, n := range cols { // a composite key: each column to its own
				c := t.col(n, line, pr)
				c.fk(rest[1:])
				if i < len(refs) {
					c.ref = to + "." + refs[i]
				}
			}
			t.keep("f", name, cols)
		}
	}
	return true
}

// isConstraint tells a table constraint from a column definition.
func isConstraint(ws []string) bool {
	if strings.EqualFold(ws[0], "CONSTRAINT") {
		return true
	}
	switch constraintWord(ws) {
	case "PRIMARY", "UNIQUE", "FOREIGN", "CHECK", "INDEX", "KEY", "EXCLUDE", "FULLTEXT", "SPATIAL":
		return true
	}
	return false
}

// constraintWord is a table constraint's first word, upper case, without a list that follows it unspaced.
func constraintWord(ws []string) string {
	kw := strings.ToUpper(ws[0])
	if i := strings.IndexByte(kw, '('); i > 0 {
		kw = kw[:i]
	}
	return kw
}

// colDef is a column's definition: its name, its type up to the first constraint, then the constraints, and the
// names of its keys (kind as in named, and the name, "" for none).
func colDef(ws []string) (*scol, [][2]string) {
	c := &scol{name: bareName(ws[0])}
	var keys [][2]string
	name := ""
	i := 1
	for i < len(ws) && !colStop[strings.ToUpper(ws[i])] {
		i++
	}
	c.typ = typeOf(ws[1:i])
	for ; i < len(ws); i++ {
		next := ""
		if i+1 < len(ws) {
			next = strings.ToUpper(ws[i+1])
		}
		switch strings.ToUpper(ws[i]) {
		case "CONSTRAINT":
			if next != "" {
				name = bareName(ws[i+1])
				i++
			}
			continue
		case "NOT":
			c.notNull = c.notNull || next == "NULL"
		case "GENERATED", "AUTO_INCREMENT", "AUTOINCREMENT":
			c.auto = true
		case "PRIMARY":
			c.pk = true
			keys = append(keys, [2]string{"p", name})
		case "UNIQUE":
			c.unique = true
			keys = append(keys, [2]string{"u", name})
		case "DEFAULT":
			if next != "" {
				c.def = ws[i+1]
				i++
			}
		case "REFERENCES":
			i += c.fk(ws[i+1:])
			keys = append(keys, [2]string{"f", name})
		}
		name = ""
	}
	return c, keys
}

// add adds the column ws defines at line, or replaces the one of its name, with its keys' names.
func (t *stab) add(ws []string, line int) {
	c, keys := colDef(ws)
	c.line = line
	for _, k := range keys {
		t.keep(k[0], k[1], []string{c.name})
	}
	if old := t.find(c.name); old != nil {
		*old = *c
		return
	}
	t.cols = append(t.cols, c)
}

// keep keeps a constraint's name, the one Postgres gives it when it has none: table_pkey, table_col_key,
// table_col_fkey.
func (t *stab) keep(kind, name string, cols []string) {
	switch {
	case name != "":
	case kind == "p":
		name = t.name + "_pkey"
	case kind == "u":
		name = t.name + "_" + strings.Join(cols, "_") + "_key"
	default:
		name = t.name + "_" + strings.Join(cols, "_") + "_fkey"
	}
	if t.named == nil {
		t.named = map[string]named{}
	}
	t.named[strings.ToLower(name)] = named{kind, cols}
}

// unname drops the constraint of that name: its columns lose the key it made.
func (t *stab) unname(name string, line int, pr bool) {
	k, ok := t.named[strings.ToLower(name)]
	if !ok {
		return
	}
	delete(t.named, strings.ToLower(name))
	still := func(kind, col string) bool { // another constraint keeps the key
		for _, o := range t.named {
			if o.kind == kind && slices.ContainsFunc(o.cols, func(x string) bool { return strings.EqualFold(x, col) }) {
				return true
			}
		}
		return false
	}
	for _, n := range k.cols {
		if still(k.kind, n) {
			continue
		}
		c := t.col(n, line, pr)
		switch k.kind {
		case "p":
			c.pk = false
		case "u":
			c.unique = false
		case "f":
			c.ref, c.onDelete, c.onUpdate = "", "", ""
		}
		if !c.pk && !c.unique {
			c.keyPos = 0
		}
	}
}

// edit applies what changed from was to now to each part of c a later migration left as was; true when one did.
func (c *scol) edit(was, now *scol) bool {
	return slices.Contains([]bool{ // every part, not up to the first that changed
		take(&c.typ, was.typ, now.typ), take(&c.def, was.def, now.def), take(&c.notNull, was.notNull, now.notNull),
		take(&c.pk, was.pk, now.pk), take(&c.unique, was.unique, now.unique), take(&c.keyPos, was.keyPos, now.keyPos),
		take(&c.ref, was.ref, now.ref), take(&c.onDelete, was.onDelete, now.onDelete), take(&c.onUpdate, was.onUpdate, now.onUpdate),
	}, true)
}

// take sets *v to now when it is still was and now differs; true when it did.
func take[T comparable](v *T, was, now T) bool {
	if *v != was || was == now {
		return false
	}
	*v = now
	return true
}

// recreate runs the PR's edit of a CREATE TABLE the base ran, before the tables as the PR's files declared them
// first: what the edit adds, changes and removes lands on the table as the base's later migrations left it. false:
// s is no such edit.
func (db sdb) recreate(s sqlLine, before sdb) bool {
	m := ddlCreate.FindStringSubmatchIndex(s.text)
	if m == nil {
		return false
	}
	name := bareName(s.text[m[4]:m[5]])
	h, was := db[name], before[name]
	if h == nil || h.gone || was == nil {
		return false
	}
	now := sdb{}
	now.apply(sqlLine{text: s.text}, false, func(string) {})
	for _, c := range now[name].cols {
		w, old := was.find(c.name), h.find(c.name)
		switch {
		case w == nil && old == nil: // the edit adds it; a later migration that adds it too wins
			x := *c
			x.line = s.line
			h.cols = append(h.cols, &x)
		case w != nil && old != nil && old.edit(w, c): // a later migration that drops it, or changes the same part, wins
			old.line = s.line
		}
	}
	for _, c := range was.cols {
		if now[name].find(c.name) == nil {
			if old := h.find(c.name); old != nil {
				old.gone, old.line = true, s.line
			}
		}
	}
	for n := range was.named {
		if _, ok := now[name].named[n]; !ok {
			delete(h.named, n) // the edit removes the constraint, or renames it
		}
	}
	for n, k := range now[name].named {
		h.keep(k.kind, n, k.cols)
	}
	return true
}

// fk reads what REFERENCES names and its ON DELETE and ON UPDATE actions; the number of words it read.
func (c *scol) fk(ws []string) int {
	c.ref = refOf(ws)
	i := 1
	if i < len(ws) && strings.HasPrefix(ws[i], "(") {
		i++
	}
	for i+2 < len(ws) && strings.EqualFold(ws[i], "ON") {
		act, n := strings.ToLower(ws[i+2]), 3
		if (act == "set" || act == "no") && i+3 < len(ws) {
			act, n = act+" "+strings.ToLower(ws[i+3]), 4
		}
		switch strings.ToUpper(ws[i+1]) {
		case "DELETE":
			c.onDelete = act
		case "UPDATE":
			c.onUpdate = act
		}
		i += n
	}
	return min(i, len(ws))
}

func typeOf(ws []string) string {
	for i, w := range ws {
		if u := strings.ToUpper(w); u == "USING" || u == "COLLATE" {
			ws = ws[:i]
			break
		}
	}
	return strings.ToLower(strings.Join(ws, " "))
}

// refOf is what REFERENCES names: users(id) or users (id) is users.id.
func refOf(ws []string) string {
	t, cols := refsOf(ws)
	if len(cols) > 0 {
		return t + "." + cols[0]
	}
	return t
}

// refsOf is the table and the columns REFERENCES names.
func refsOf(ws []string) (string, []string) {
	if len(ws) == 0 {
		return "", nil
	}
	t, cols := ws[0], ""
	if i := strings.IndexByte(t, '('); i > 0 {
		t, cols = t[:i], t[i:]
	} else if len(ws) > 1 && strings.HasPrefix(ws[1], "(") {
		cols = ws[1]
	}
	return bareName(t), listOf(cols)
}

// firstList is the names in the first parenthesized list of ws, and the words after it.
func firstList(ws []string) (cols, rest []string) {
	for i, w := range ws {
		if j := strings.IndexByte(w, '('); j >= 0 {
			return listOf(w[j:]), ws[i+1:]
		}
	}
	return nil, nil
}

func listOf(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "(") {
		return nil
	}
	in, _ := inParens(s, 0)
	var out []string
	for _, p := range splitTop(in) {
		if fs := strings.Fields(p); len(fs) > 0 {
			out = append(out, bareName(fs[0]))
		}
	}
	return out
}

// inParens is what's inside the parenthesis at s[i] and the index after its match; the rest of s when it has none.
func inParens(s string, i int) (string, int) {
	depth, quote := 0, false
	for j := i; j < len(s); j++ {
		switch c := s[j]; {
		case quote:
			quote = c != '\''
		case c == '\'':
			quote = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return s[i+1 : j], j + 1
			}
		}
	}
	return s[min(i+1, len(s)):], len(s)
}

// splitTop splits s at the commas outside parentheses and quotes.
func splitTop(s string) []string {
	return splitSQL(s, func(c byte) bool { return c == ',' }, false)
}

// words splits s at the white space outside parentheses and quotes: numeric(10, 2) is one word.
func words(s string) []string {
	return splitSQL(s, func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }, true)
}

func splitSQL(s string, sep func(byte) bool, dropEmpty bool) []string {
	var out []string
	depth, quote, from := 0, false, 0
	add := func(p string) {
		if p = strings.TrimSpace(p); p != "" || !dropEmpty {
			out = append(out, p)
		}
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote:
			quote = c != '\''
		case c == '\'':
			quote = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && sep(c):
			add(s[from:i])
			from = i + 1
		}
	}
	add(s[from:])
	return out
}

// skip drops the leading words of ws that are among kw, in any case.
func skip(ws []string, kw ...string) []string {
	for len(ws) > 0 && slices.ContainsFunc(kw, func(k string) bool { return strings.EqualFold(ws[0], k) }) {
		ws = ws[1:]
	}
	return ws
}

func upper(ws []string) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = strings.ToUpper(w)
	}
	return out
}

// column is the page's column: its notes from its constraints.
func (c *scol) column() Column {
	col := Column{Name: c.name, Type: c.typ, Ref: c.ref, OnDelete: c.onDelete, OnUpdate: c.onUpdate, Part: c.keyPos > 0, Line: c.line}
	if c.pk {
		col.Notes = append(col.Notes, "primary key")
	}
	if c.notNull && !c.pk {
		col.Notes = append(col.Notes, "not null")
	}
	if c.unique {
		col.Notes = append(col.Notes, "unique")
	}
	if c.def != "" {
		col.Notes = append(col.Notes, "default "+c.def)
	}
	return col
}

// differs is what the base's column c was where the head's h differs from it: "text, nullable, no default".
func (c *scol) differs(h *scol) string {
	var w []string
	if c.typ != h.typ && h.typ != "" {
		w = append(w, cmp.Or(c.typ, "no type"))
	}
	if c.notNull != h.notNull {
		w = append(w, pick(c.notNull, "not null", "nullable"))
	}
	if c.def != h.def {
		w = append(w, pick(c.def != "", "default "+c.def, "no default"))
	}
	switch {
	case c.ref != h.ref:
		w = append(w, pick(c.ref != "", "references "+c.ref, "no reference"))
	case c.onDelete != h.onDelete || c.onUpdate != h.onUpdate:
		if c.onDelete != h.onDelete {
			w = append(w, "on delete "+cmp.Or(c.onDelete, "no action"))
		}
		if c.onUpdate != h.onUpdate {
			w = append(w, "on update "+cmp.Or(c.onUpdate, "no action"))
		}
	}
	if c.pk != h.pk || c.unique != h.unique {
		w = append(w, pick(c.unique || c.pk, "unique", "not unique"))
	}
	return strings.Join(w, ", ")
}

func pick(c bool, yes, no string) string {
	if c {
		return yes
	}
	return no
}

// dbView is Changes, Database (decision 42): the tables on one canvas, what the legend names (only what the canvas
// shows), and the open items in the migrations' files.
type dbView struct {
	Tables                     []dbTable
	Ref, New, Changed, Dropped bool
	Guess, Context             bool
	Many, ZeroOne, Exact       bool // the ends the lines show: zero or many, zero or one, exactly one
	Items                      []prow2
}

// dbTable is a table's card: Label its migration's ("Migration 0008"); Who the parts that write and read it (6.13);
// Join what a join table joins; Cols its rows, More the columns the PR leaves alone when a changed table has more
// than maxCols.
type dbTable struct {
	Table
	ID, Label, Who, Join string
	Cols, More           []dbCol
}

// dbCol is a column's row: Sign the diff's mark for what the PR does (+ new, ~ changed or renamed, − dropped), Note
// what it was and its constraints but the key, RefID and RefCol the card and row its reference points at when the
// canvas shows them, Act its key's actions; One: its key joins one row to one (the column is unique), not many to one.
type dbCol struct {
	Column
	Path, Note, Sign, RefID, RefCol, Act string
	PK, One, Need                        bool // Need: its key must reference a row (not null)
}

const maxCols = 10

var signs = map[string]string{"new": "+", "changed": "~", "renamed": "~", "dropped": "−"}

// database is Changes, Database (decision 42); nil when the migrations name no table.
func (v *view) database() *dbView {
	if len(v.Schema) == 0 {
		return nil
	}
	shown := map[string]bool{}
	for _, t := range v.Schema {
		shown[t.Name] = true
	}
	db := &dbView{}
	for _, t := range v.Schema {
		x := dbTable{Table: t, ID: "db-" + t.Name, Label: migrationLabel(t.Path), Who: v.usedBy(t.Name)}
		if len(t.Joins) > 0 {
			names := make([]string, len(t.Joins))
			for i, n := range t.Joins {
				names[i] = "`" + n + "`"
			}
			x.Join = "Joins " + join(names) + ", many to many"
		}
		var touched, same []dbCol
		for _, c := range t.Cols {
			r := dbCol{Column: c, Path: t.Path, Sign: signs[c.State], Note: colNote(c)}
			if t.Kind == "dropped" {
				r.State, r.Sign = "dropped", ""
			}
			r.PK = slices.Contains(c.Notes, "primary key")
			r.One = (r.PK || slices.Contains(c.Notes, "unique")) && !c.Part
			r.Need = r.PK || slices.Contains(c.Notes, "not null")
			r.Act = keyActs(c)
			if ref, col, _ := strings.Cut(c.Ref, "."); shown[ref] {
				r.RefID, r.RefCol = "db-"+ref, col
				db.Ref = db.Ref || !c.Guess
				db.Guess = db.Guess || c.Guess
				if !c.Guess {
					db.Many = db.Many || !r.One
					db.ZeroOne = db.ZeroOne || r.One || !r.Need
					db.Exact = db.Exact || r.Need
				}
			}
			db.Context = db.Context || t.Kind == "context"
			db.New = db.New || c.State == "new" || t.Kind == "created"
			db.Changed = db.Changed || c.State == "changed" || c.State == "renamed"
			db.Dropped = db.Dropped || c.State == "dropped" || t.Kind == "dropped"
			x.Cols = append(x.Cols, r)
			if c.State == "" {
				same = append(same, r)
			} else {
				touched = append(touched, r)
			}
		}
		if t.Kind == "changed" && len(t.Cols) > maxCols {
			x.Cols, x.More = touched, same
		}
		db.Tables = append(db.Tables, x)
	}
	// dropped tables last, a table outside the PR right after the first card its key joins: the cards a key joins
	// stay side by side
	slices.SortStableFunc(db.Tables, func(x, y dbTable) int {
		return cmp.Compare(pick(x.Kind == "dropped", "1", "0"), pick(y.Kind == "dropped", "1", "0"))
	})
	db.Tables = nextTo(db.Tables)
	// the items in the migrations' files, each once: what Before you merge and the parts hold about them
	seen := map[string]bool{}
	for _, k := range slices.Concat(v.Page.Before, v.lead, v.found) {
		at := cmp.Or(k.GHPlace, k.Place)
		if seen[k.ID] || k.ID == "" || !slices.ContainsFunc(v.Schema, func(t Table) bool { return strings.HasPrefix(at, t.Path) }) {
			continue
		}
		seen[k.ID] = true
		db.Items = append(db.Items, prow2{Kind: k.Kind, Title: k.Title, Place: k.Place, Href: "#" + k.ID})
	}
	return db
}

// nextTo is ts with each table outside the PR moved after the first of the PR's that a key joins it to.
func nextTo(ts []dbTable) []dbTable {
	joins := func(x, y dbTable) bool {
		return slices.ContainsFunc(x.Cols, func(c dbCol) bool { return c.RefID == y.ID }) ||
			slices.ContainsFunc(y.Cols, func(c dbCol) bool { return c.RefID == x.ID })
	}
	out := make([]dbTable, 0, len(ts))
	placed := map[string]bool{}
	for _, t := range ts {
		if t.Kind == "context" {
			continue
		}
		out = append(out, t)
		for _, c := range ts {
			if c.Kind == "context" && !placed[c.ID] && joins(t, c) {
				placed[c.ID] = true
				out = append(out, c)
			}
		}
	}
	for _, c := range ts {
		if c.Kind == "context" && !placed[c.ID] {
			out = append(out, c)
		}
	}
	return out
}

// keyActs is what a key does when the row it references goes or changes: "on delete set null".
func keyActs(c Column) string {
	var bits []string
	if c.OnDelete != "" {
		bits = append(bits, "on delete "+c.OnDelete)
	}
	if c.OnUpdate != "" {
		bits = append(bits, "on update "+c.OnUpdate)
	}
	return strings.Join(bits, ", ")
}

// colNote is a column's note: what the PR did to it, then its constraints, the key aside (its row shows a key).
func colNote(c Column) string {
	var bits []string
	switch {
	case c.State == "changed" && c.Was != "":
		bits = append(bits, "was "+c.Was)
	case c.State == "renamed":
		bits = append(bits, "renamed from "+c.Was)
	case c.State == "dropped":
		bits = append(bits, "dropped")
	}
	for _, n := range c.Notes {
		if n != "primary key" {
			bits = append(bits, n)
		}
	}
	return strings.Join(bits, " · ")
}

// usedBy is the parts that write and read table name, as the map found them: "Written in orders, read in api".
func (v *view) usedBy(name string) string {
	if v.Map == nil {
		return ""
	}
	label := map[string]string{}
	for _, p := range v.Map.Parts {
		label[p.ID] = p.Label
	}
	for _, st := range v.Map.Stores {
		if !strings.EqualFold(bareName(st.Name), name) {
			continue
		}
		var w, r []string
		for _, p := range st.Parts {
			if slices.ContainsFunc(p.Uses, func(u MapStoreUse) bool { return u.Kind == "writes" }) {
				w = append(w, label[p.Part])
			} else {
				r = append(r, label[p.Part])
			}
		}
		switch {
		case len(w) > 0 && len(r) > 0:
			return "Written in " + join(w) + ", read in " + join(r)
		case len(w) > 0:
			return "Written in " + join(w)
		case len(r) > 0:
			return "Read in " + join(r)
		}
	}
	return ""
}
