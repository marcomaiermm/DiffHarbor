package report

import (
	"diffharbor/internal/review"
	"diffharbor/internal/ticket"
	"slices"
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
