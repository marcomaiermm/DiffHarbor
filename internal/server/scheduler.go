package server

import (
	"diffharbor/internal/forge"
	"diffharbor/internal/job"
	"maps"
	"slices"
)

// scheduler picks the queued job that runs next. Jobs of different repos run at once, up to limit. A repo has two
// lanes, each one job at a time: its reviews (with closes and comments that ask for a review) and its answers. A
// lane's jobs share its uid (job.AnswersUID), which is what fences a job's PR code off from another's. So do repos of one
// name, a recreated one and the old one's, or an event without the repo's id: they share the learnings and the reports
// from before the repo id, keyed by name. A PR runs one job at a time: running, pending and stop know one per PR.
// Lanes take turns, so a repo with many PRs doesn't hold up the others. pool.mu guards it.
type scheduler struct {
	limit   int                       // jobs at once
	room    int                       // jobs that may still wait: past it a webhook gets a 503, GitHub's is redelivered (Redeliver)
	waiting map[string][]*forge.Event // by lane: comments before reviews, a human waits for them; else in order
	turns   []string                  // the lanes with waiting jobs, the next turn first
	busy    map[string]busy           // the lanes with a running job
}

// busy is a lane's running job: its repo's name with the lane, and its PR (prKey).
type busy struct{ name, pr string }

func busyOf(e *forge.Event) busy { return busy{lane(e.Repo(), e), prKey(e)} }

func newScheduler(limit, room int) *scheduler {
	return &scheduler{limit: limit, room: room, waiting: map[string][]*forge.Event{}, busy: map[string]busy{}}
}

// lane is repo's lane for e: answers get their own, beside the reviews. An unknown forge's comment (no mention) stays
// with the reviews.
func lane(repo string, e *forge.Event) string {
	if m := mentions[e.Forge]; e.Kind != "pull_request" && m != nil && !job.AsksReview(m, e) {
		return repo + job.AnswersUID
	}
	return repo
}

// add queues e on its lane of repo; false when the queue is full. A review takes the place of the PR's waiting one:
// only the newest push's would run (review), and a PR pushed to again and again must not fill the queue.
func (s *scheduler) add(repo string, e *forge.Event) bool {
	repo = lane(repo, e)
	q, ok := s.waiting[repo]
	if isReview(e) {
		n := len(q)
		q = slices.DeleteFunc(q, func(o *forge.Event) bool { return isReview(o) && o.PullRequest.Number == e.PullRequest.Number })
		s.room += n - len(q)
	}
	if s.room == 0 { // none replaced: q is as it was
		return false
	}
	s.room--
	if !ok {
		s.turns = append(s.turns, repo)
	}
	i := len(q)
	for e.Kind != "pull_request" && i > 0 && isReview(q[i-1]) { // after the answers and closes queued before it
		i--
	}
	s.waiting[repo] = slices.Insert(q, i, e)
	return true
}

// next takes the job to start and its lane, if a slot is free and a lane without a running job has one waiting
// whose name and PR run nothing; the lane is busy until free. A job of a PR that runs one waits, the lane's others
// may go first. A repo's answers go before its reviews, as when they shared a queue: a human waits for them.
func (s *scheduler) next() (string, *forge.Event, bool) {
	if len(s.busy) >= s.limit {
		return "", nil, false
	}
	for i, repo := range s.turns {
		j := s.first(repo)
		if j < 0 || isReview(s.waiting[repo][j]) && s.first(repo+job.AnswersUID) >= 0 {
			continue // keeps its place: its turn comes once its job is done
		}
		q := s.waiting[repo]
		e := q[j]
		s.turns = slices.Delete(s.turns, i, i+1)
		if q = slices.Delete(q, j, j+1); len(q) > 0 {
			s.waiting[repo], s.turns = q, append(s.turns, repo) // to the back: the other lanes first
		} else {
			delete(s.waiting, repo)
		}
		s.busy[repo] = busyOf(e)
		s.room++
		return repo, e, true
	}
	return "", nil, false
}

// first is the index of the first job of lane repo that may start now, -1 for none: the lane runs nothing, nor do
// the job's name and PR.
func (s *scheduler) first(repo string) int {
	if _, ok := s.busy[repo]; ok {
		return -1
	}
	running := slices.Collect(maps.Values(s.busy))
	return slices.IndexFunc(s.waiting[repo], func(e *forge.Event) bool {
		b := busyOf(e)
		return !slices.ContainsFunc(running, func(o busy) bool { return o.name == b.name || o.pr == b.pr })
	})
}

// isReview is a push's review, not a closed PR's delete or an answer.
func isReview(e *forge.Event) bool { return e.Kind == "pull_request" && !closing(e) }

// free ends the running job of lane repo, as next returned it.
func (s *scheduler) free(repo string) { delete(s.busy, repo) }
