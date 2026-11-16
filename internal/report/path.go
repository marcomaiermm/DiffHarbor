package report

import (
	"cmp"
	"path"
	"strconv"
	"strings"
)

// pathView is a path the script draws (component 30, uses spec 4): its screen reader label and its items, top down.
// The traced path and the call paths to a finding come from here; the uses sheet builds the same items from
// Data.Nodes in the script, so the three draw with one implementation.
type pathView struct {
	Label string  `json:"label"`
	Items []pitem `json:"items"`
}

// pitem is a station ("s"), the joint below it ("j") or the end ("e"). A station: its name, kind (decl, out, route,
// callin, system, sysrepo, finding), a route's method, its words, part label, the blocking finding's severity and card,
// its place, and the name the page knows it by (its card). A joint: the relation, the interface it goes through, its
// place (none: Line 0), the line's text, the structure there (1-based into Data.Shapes), the name it marks, and who
// uses whom for the screen reader's sentence; the user is always the station above. An end: its sentence.
type pitem struct {
	T    string `json:"t"`
	Name string `json:"n,omitempty"`
	Kind string `json:"k,omitempty"`
	Meth string `json:"m,omitempty"`
	Says string `json:"s,omitempty"`
	Part string `json:"pt,omitempty"`
	Sev  string `json:"v,omitempty"`
	Find string `json:"f,omitempty"`
	Path string `json:"p,omitempty"`
	Line int    `json:"l,omitempty"`
	Sym  string `json:"sym,omitempty"`
	Rel  string `json:"r,omitempty"`
	Via  string `json:"via,omitempty"`
	Text string `json:"x,omitempty"`
	Sh   int    `json:"sh,omitempty"`
	Mark string `json:"mk,omitempty"`
	User string `json:"u,omitempty"`
	Used string `json:"d,omitempty"`
}

// hops are the hops of every way in the page, by declaration and the place that names it, so a way's row finds the
// structure and relation of its joint (Hop.Shape, Hop.Rel); a hop the index gives no place has its declaration's.
func (v *view) hops() map[string]Hop {
	out := map[string]Hop{}
	for _, ss := range v.Symbols {
		for _, s := range ss {
			for _, w := range s.Ways {
				for _, h := range w.Chain {
					k := h.Path + "\x00" + h.Name + "\x00" + h.In + "\x00" + strconv.Itoa(h.At)
					if h.At == 0 {
						k = h.Path + "\x00" + h.Name + "\x00" + h.Path + "\x00" + strconv.Itoa(h.Line)
					}
					if _, ok := out[k]; !ok {
						out[k] = h
					}
				}
			}
		}
	}
	return out
}

// entryItem is a way's entry as the path's first station: a route, or "Call in" and its file.
func (v *view) entryItem(method, route string, at ref, part string) pitem {
	if method == "CALL" || method == "" {
		return pitem{T: "s", Kind: "callin", Name: at.Path, Part: part}
	}
	it := pitem{T: "s", Kind: "route", Meth: method, Name: route, Says: "route", Part: part, Path: at.Path, Line: at.Line}
	if v.files[at.Path] == nil {
		it.Says += " · outside the PR"
	}
	return it
}

// wayItems is a way's rows as joints and stations, each joint the place where the station above names the one below.
func (v *view) wayItems(rows []wrow, prev, user string, hops map[string]Hop) []pitem {
	var out []pitem
	for _, r := range rows {
		j := pitem{T: "j", Mark: r.Name, User: user, Used: r.Name, Path: r.At.Path, Line: r.At.Line}
		if r.Call != r.Name {
			j.Rel = "call"
		}
		if h, ok := hops[r.Path+"\x00"+r.Name+"\x00"+r.At.Path+"\x00"+strconv.Itoa(r.At.Line)]; ok {
			j.Rel, j.Sh = cmp.Or(h.Rel, j.Rel), h.Shape
			if h.At == 0 { // the index names it without a place: a line with no words
				j.Path, j.Line = "", 0
			}
		}
		s := pitem{T: "s", Kind: "decl", Name: r.Call, Says: r.Tag, Path: r.Path}
		for _, d := range v.Symbols[r.Name] {
			if d.Path == r.Path && d.Exact {
				s.Line = d.Line
				break
			}
		}
		if j.Path == s.Path && j.Line == s.Line { // the way gives its declaration's place, not the line that names it
			j.Path, j.Line = "", 0
		}
		j.Text = v.lineText(j.Path, j.Line)
		if r.Sym {
			s.Sym = r.Name
		}
		if v.files[r.Path] == nil {
			s.Kind, s.Says = "out", "outside the PR"
		}
		if r.Sev != nil {
			s.Kind, s.Sev = "finding", r.Sev.Sev
			if f := v.files[r.Path]; f != nil {
				s.Find = findingOf(f, r.Sev.Sev)
			}
		}
		if pb := v.partOf(r.Path); pb != nil && pb.Label != prev {
			s.Part, prev = pb.Label, pb.Label
		}
		out = append(out, j, s)
		user = r.Name
	}
	return out
}

// lineText is the line at p:n as the diff or the head shows it, trimmed; "" when the page has neither.
func (v *view) lineText(p string, n int) string {
	if n == 0 {
		return ""
	}
	if l, ok := v.newLines()[p+":"+strconv.Itoa(n)]; ok {
		return strings.TrimSpace(l)
	}
	return strings.TrimSpace(v.headLine(p, n))
}

// tracePath is the traced path (6.12) as a path: the entry, the way down, the calls down to the request (its
// OutsideCall.Calls), the system.
func (v *view) tracePath(t *MapTrace, entryPart string, hops map[string]Hop) *pathView {
	entry := v.entryItem(t.Method, t.Route, t.At, entryPart)
	items := append([]pitem{entry}, v.wayItems(t.Rows, entryPart, cmp.Or(t.Route, t.At.Path), hops)...)
	last := t.Rows[len(t.Rows)-1].Name
	for _, c := range t.Call.Calls {
		p, n, _ := span(c.At)
		s := pitem{T: "s", Kind: "decl", Name: c.Callee + "(…)"}
		for _, d := range v.Symbols[c.Callee] {
			if d.Exact {
				s.Path, s.Line, s.Sym = d.Path, d.Line, c.Callee
				if v.files[d.Path] == nil {
					s.Kind, s.Says = "out", "outside the PR"
				}
				break
			}
		}
		items = append(items, pitem{T: "j", Rel: "call", Path: p, Line: n, Text: v.lineText(p, n), Mark: c.Callee, User: last, Used: c.Callee}, s)
		last = c.Callee
	}
	sys := pitem{T: "s", Kind: "system", Name: t.Call.System, Says: "outside the repo"}
	if t.Call.System == last { // named after the declaration that sends (6.4's last fallback)
		sys.Kind, sys.Says = "sysrepo", ""
	}
	items = append(items, pitem{T: "j", Rel: "request", Path: t.Call.Path, Line: t.Call.Line, Text: v.lineText(t.Call.Path, t.Call.Line), User: last, Used: t.Call.System}, sys)
	return &pathView{Label: "Path from " + strings.TrimSpace(entry.Meth+" "+entry.Name) + " to " + t.Call.System, Items: items}
}

// findingPath is a call path to a finding (5.7) as a path: the way's entry and its rows down to the finding, whose
// station links to the card.
func (v *view) findingPath(w way, rows []wrow, entryPart, prev, href string, hops map[string]Hop) *pathView {
	items := append([]pitem{v.entryItem(w.Method, w.Route, w.At, entryPart)}, v.wayItems(rows, prev, cmp.Or(w.Route, path.Base(w.At.Path)), hops)...)
	if s := &items[len(items)-1]; s.Kind == "finding" {
		s.Find = href
	}
	return &pathView{Label: w.Label(), Items: items}
}
