package report

import (
	"diffharbor/internal/diff"
	"diffharbor/internal/review"
	"diffharbor/internal/ticket"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// TicketStat is one ticket's share of the PR, from git and the review's steps: its card's stats line.
type TicketStat struct {
	Key     string
	Commits int // commits whose subject names it
	Files   int // files those commits change
	Own     int // of Files, those no other ticket's commit changes
	Steps   int // the review's steps (Data.Flow) that hold one of its files
}

// Split is how the PR's commits divide among its tickets: the cards' stats lines and the tickets section's footer.
type Split struct {
	Tickets  []TicketStat // one per ticket key, in the order of the keys
	Nameless int          // commits that name no ticket
	Shared   int          // files that carry commits of two or more tickets: the tickets can't merge one at a time
}

// SplitTickets counts each ticket's commits by the key in their subject, and their files, from the PR's commits
// without merges (review.Changes). A commit naming two tickets counts for both. nil without keys.
func SplitTickets(keys []string, commits []review.Commit, flow []review.Step) *Split {
	if len(keys) == 0 {
		return nil
	}
	s := &Split{}
	n := map[string]int{}
	files := map[string]map[string]bool{} // key -> the files of its commits
	by := map[string]map[string]bool{}    // file -> the tickets whose commits change it
	for _, c := range commits {
		named := slices.DeleteFunc(ticket.Named(nil, c.Subject), func(k string) bool { return !slices.Contains(keys, k) })
		if len(named) == 0 {
			s.Nameless++
		}
		for _, k := range named {
			n[k]++
			for _, p := range c.Paths {
				if files[k] == nil {
					files[k] = map[string]bool{}
				}
				if by[p] == nil {
					by[p] = map[string]bool{}
				}
				files[k][p], by[p][k] = true, true
			}
		}
	}
	for _, ks := range by {
		if len(ks) > 1 {
			s.Shared++
		}
	}
	for _, k := range keys {
		st := TicketStat{Key: k, Commits: n[k], Files: len(files[k])}
		for p := range files[k] {
			if len(by[p]) == 1 {
				st.Own++
			}
		}
		for _, step := range flow {
			if slices.ContainsFunc(step.Files, func(f string) bool { return files[k][f] }) {
				st.Steps++
			}
		}
		s.Tickets = append(s.Tickets, st)
	}
	return s
}

// tsec is The tickets (5.9): the intro lines, a card per ticket, the footer.
type tsec struct {
	Intro  []mline
	Follow string // the follow-ups the author names, plain
	ReadOn string // the date the tracker answered
	Cards  []*tcard
	Foot   []mline
	Shown  bool // any ticket named: the section and its rail row show
}

// tcard is a ticket card (component 11).
type tcard struct {
	ID, Key, State, Due, Title, Stats, Diffs string
	Open                                     bool
	Outcome                                  string
	Answers                                  []answer
	AnsChip                                  chip
	Rows                                     []trow
	Reason                                   string // the tracker didn't answer for it: why, in the page's words
}

// answer is one of the four answers (component 12).
type answer struct {
	Label, Text string
	At          []cline
}

// trow is a contract row (component 10): the ask, its status and evidence, the places and lines that back it.
type trow struct {
	Ask   string
	St    *schip
	Chip  chip
	Lines []cline
}

// ticketsView is The tickets: the cards of the tickets read, in Data.Tickets' order, then the ones the tracker
// didn't answer for; with none read, a card per key the commits name, with its git stats alone.
func (v *view) ticketsView() tsec {
	var t tsec
	stat := map[string]TicketStat{}
	var keys []string
	if v.Split != nil {
		for _, s := range v.Split.Tickets {
			stat[s.Key], keys = s, append(keys, s.Key)
		}
	}
	t.Shown = len(keys) > 0 || len(v.Tickets) > 0 || len(v.TicketErrs) > 0
	if !t.Shown {
		return t
	}
	steps := v.NSteps
	diffs := map[string]int{}
	for _, d := range v.Terms.Differences {
		diffs[d.Ticket]++
	}
	statsOf := func(key string, asks int, read bool) string {
		s, ok := stat[key]
		var p []string
		if ok {
			p = append(p, plural(s.Commits, "commit", "commits"), plural(s.Files, "file", "files")+", "+strconv.Itoa(s.Own)+" only its own")
			if s.Steps > 0 && steps > 0 {
				p = append(p, "in "+strconv.Itoa(s.Steps)+" of "+plural(steps, "step", "steps"))
			} else if steps > 0 {
				p = append(p, "in no step")
			}
		}
		if read {
			p = append(p, plural(asks, "ask", "asks"))
		}
		return strings.Join(p, " · ")
	}
	contract := map[string]review.Contract{}
	for _, c := range v.Terms.Contracts {
		contract[c.Ticket] = c
	}
	for _, tk := range v.Tickets {
		c := &tcard{ID: "ticket-" + slug(tk.Key), Key: tk.Key, State: tk.State, Title: tk.Title, Outcome: tk.Outcome}
		if !tk.Due.IsZero() {
			c.Due = "due " + tk.Due.Format(time.DateOnly)
		}
		c.Stats = statsOf(tk.Key, len(tk.DoneWhen), true)
		if n := diffs[tk.Key]; n > 0 {
			c.Diffs = plural(n, "difference", "differences")
		}
		if ct, ok := contract[tk.Key]; ok {
			v.contractOf(c, ct)
		}
		t.Cards = append(t.Cards, c)
	}
	if len(v.Tickets) == 0 {
		for _, k := range keys {
			t.Cards = append(t.Cards, &tcard{ID: "ticket-" + slug(k), Key: k, Stats: statsOf(k, 0, false)})
		}
	}
	for _, k := range slices.Sorted(maps.Keys(v.TicketErrs)) {
		c := &tcard{ID: "ticket-" + slug(k), Key: k, Stats: statsOf(k, 0, false), Reason: readFailed(v.TicketErrs[k])}
		if i := slices.IndexFunc(t.Cards, func(x *tcard) bool { return x.Key == k }); i >= 0 {
			t.Cards[i] = c
		} else {
			t.Cards = append(t.Cards, c)
		}
	}
	if len(t.Cards) == 1 {
		t.Cards[0].Open = true // one ticket: read it open
	}
	v.ticketLines(&t, keys)
	return t
}

// ticketLines is The tickets' intro and footer: the tracker's relations, the split setting, the follow-ups and the
// read date, the passes that failed; the files and commits the tickets share.
func (v *view) ticketLines(t *tsec, keys []string) {
	var follow []string
	for _, f := range v.FollowUps {
		follow = append(follow, strings.TrimSpace(f.Key+" "+f.State))
	}
	var read time.Time
	for _, tk := range v.Tickets {
		if tk.Read.After(read) {
			read = tk.Read
		}
	}
	if len(v.Tickets) == 0 {
		if len(keys) > 0 {
			t.Intro = append(t.Intro, mline{Chip: evGit, Text: "Commits name " + plural(len(keys), "ticket", "tickets") + ": " + join(keys) + ". No ticket text was read."})
		}
	} else {
		s := "This PR carries " + plural(len(v.Tickets)+len(v.TicketErrs), "ticket", "tickets") + "." // one the tracker didn't answer for too
		in := map[string]bool{}
		for _, tk := range v.Tickets {
			in[tk.Key] = true
		}
		for _, tk := range v.Tickets {
			by := slices.DeleteFunc(slices.Clone(tk.BlockedBy), func(k string) bool { return !in[k] })
			if len(by) > 0 {
				s += " The tracker says " + join(by) + map[bool]string{true: " blocks ", false: " block "}[len(by) == 1] + tk.Key + "."
			}
		}
		t.Intro = append(t.Intro, mline{Chip: chip{"shown", "Tracker"}, Text: s})
		if len(v.Tickets) > 1 && v.SplitOff {
			t.Intro = append(t.Intro, mline{Chip: evRun, Text: "Split suggestions are off for this repo, so the review proposed none."})
		}
		t.Follow = strings.Join(follow, " · ")
		if !read.IsZero() {
			t.ReadOn = read.Format(time.DateOnly)
		}
	}
	if v.TicketsCapped {
		t.Intro = append(t.Intro, mline{Chip: evRun, Text: "The PR names more tickets than one review reads, so some have no card."})
	}
	for _, e := range v.Terms.Errs {
		switch strings.SplitN(e, ":", 2)[0] {
		case "contract":
			t.Intro = append(t.Intro, mline{Chip: evRun, Text: "The contract pass failed, so nothing compared the code with what the tickets ask."})
		case "check":
			t.Intro = append(t.Intro, mline{Chip: evRun, Text: "The check pass failed, so no second model checked the contract rows."})
		}
	}
	if s := v.Split; s != nil && len(keys) > 1 {
		var f []string
		if s.Shared > 0 {
			f = append(f, plural(s.Shared, "file carries", "files carry")+" commits of two or more tickets, so the tickets can't be merged one at a time.")
		}
		if s.Nameless > 0 {
			f = append(f, plural(s.Nameless, "commit names", "commits name")+" no ticket.")
		}
		if len(f) > 0 {
			t.Foot = []mline{{Chip: evGit, Text: strings.Join(f, " ")}}
		}
	}
}

// readFailed is why the tracker didn't answer for a ticket, in the page's words: never its URL or its raw error.
func readFailed(err string) string {
	e := strings.ToLower(err)
	switch {
	case strings.Contains(e, "deadline") || strings.Contains(e, "timeout") || strings.Contains(e, "timed out"):
		return "The tracker didn't answer in time, so this ticket wasn't read."
	case strings.Contains(e, "401") || strings.Contains(e, "403"):
		return "The tracker refused the request, so this ticket wasn't read."
	case strings.Contains(e, "404"):
		return "The tracker doesn't know this key, so this ticket wasn't read."
	}
	return "The tracker answered with an error, so this ticket wasn't read."
}

// contractOf fills a ticket card's four answers and contract rows.
func (v *view) contractOf(c *tcard, ct review.Contract) {
	c.AnsChip = evOf(ct.Evidence)
	for _, a := range []struct {
		label string
		a     review.Answer
	}{{"What changed and why", ct.Changed}, {"Where it runs", ct.Runs}, {"What it relies on", ct.Relies}, {"Where to look first", ct.First}} {
		if a.a.Text == "" {
			continue
		}
		an := answer{Label: a.label, Text: a.a.Text}
		for _, at := range a.a.At {
			an.At = append(an.At, cline{Code: at, Href: v.placeHref(at)})
		}
		c.Answers = append(c.Answers, an)
	}
	for _, r := range ct.Rows {
		ask, _, _ := strings.Cut(r.Ask, ":")
		if ask != "Outcome" {
			ask = r.Ask
		}
		tr := trow{Ask: ask, Chip: evOf(r.Evidence)}
		tr.St = statusChip(r.Status, tr.Chip)
		for _, at := range r.At {
			p, _, _ := span(at)
			l := cline{Code: at, Href: v.placeHref(at)}
			if diff.Kind(p) == "test" {
				l.Lead, l.Note = "Test:", v.notRun(at)
			}
			tr.Lines = append(tr.Lines, l)
		}
		if len(r.Docs) > 0 {
			tr.Lines = append(tr.Lines, cline{Text: "Docs at " + short(v.SHA) + " still name it: " + join(codeAll(r.Docs)) + "."})
		}
		if r.Note != "" {
			tr.Lines = append(tr.Lines, cline{Text: r.Note})
		}
		c.Rows = append(c.Rows, tr)
	}
}
