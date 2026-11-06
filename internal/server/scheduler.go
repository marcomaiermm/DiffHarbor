package server

import (
	"diffharbor/internal/forge"
	"slices"
)

// scheduler picks the queued job that runs next. Jobs of different repos run at once, up to limit; a repo's run one
// at a time: they share its uid, which is what fences a job's PR code off from another's. Repos take turns, so one
// with many PRs doesn't hold up the others. Not safe for concurrent use: the dispatcher alone owns it.
type scheduler struct {
	limit   int                       // jobs at once
	room    int                       // jobs that may still wait: past it a webhook gets a 503 and is redelivered
	waiting map[string][]*forge.Event // by repo: answers first, a human waits for them; else in order
	turns   []string                  // the repos with waiting jobs, the next turn first
	busy    map[string]bool           // the repos with a running job
}

func newScheduler(limit, room int) *scheduler {
	return &scheduler{limit: limit, room: room, waiting: map[string][]*forge.Event{}, busy: map[string]bool{}}
}

// add queues e on repo; false when the queue is full.
func (s *scheduler) add(repo string, e *forge.Event) bool {
	if s.room == 0 {
		return false
	}
	s.room--
	q := s.waiting[repo]
	if len(q) == 0 {
		s.turns = append(s.turns, repo)
	}
	i := len(q)
	if e.Kind != "pull_request" { // after the answers queued before it
		if j := slices.IndexFunc(q, func(e *forge.Event) bool { return e.Kind == "pull_request" }); j >= 0 {
			i = j
		}
	}
	s.waiting[repo] = slices.Insert(q, i, e)
	return true
}

// next takes the job to start, if a slot is free and a repo without a running job has one waiting; its repo is busy
// until free.
func (s *scheduler) next() (string, *forge.Event, bool) {
	if len(s.busy) >= s.limit {
		return "", nil, false
	}
	for i, repo := range s.turns {
		if s.busy[repo] {
			continue // keeps its place: its turn comes once its job is done
		}
		q := s.waiting[repo]
		e := q[0]
		s.turns = slices.Delete(s.turns, i, i+1)
		if q = slices.Delete(q, 0, 1); len(q) > 0 {
			s.waiting[repo], s.turns = q, append(s.turns, repo) // to the back: the other repos first
		} else {
			delete(s.waiting, repo)
		}
		s.busy[repo] = true
		s.room++
		return repo, e, true
	}
	return "", nil, false
}

// free ends repo's running job.
func (s *scheduler) free(repo string) { delete(s.busy, repo) }
