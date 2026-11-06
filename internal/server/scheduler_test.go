package server

import (
	"diffharbor/internal/forge"
	"slices"
	"testing"
)

// ev is a job on PR n of repo: a review, or with kind an answer.
func ev(repo string, n int, kind string) *forge.Event {
	e := &forge.Event{Kind: "pull_request"}
	if kind != "" {
		e.Kind = kind
	}
	e.Repository.FullName, e.PullRequest.Number = repo, n
	return e
}

// started is the job s starts now, "" when none.
func started(s *scheduler) string {
	if _, e, ok := s.next(); ok {
		return e.Key()
	}
	return ""
}

func queued(s *scheduler, es ...*forge.Event) {
	for _, e := range es {
		if !s.add(e.Repo(), e) {
			panic("full")
		}
	}
}

// Repos run at once up to the limit, a repo's jobs one at a time.
func TestSchedulerLimit(t *testing.T) {
	s := newScheduler(2, 100)
	queued(s, ev("a", 1, ""), ev("a", 2, ""), ev("b", 1, ""), ev("c", 1, ""))
	if a, b, none := started(s), started(s), started(s); a != "a#1" || b != "b#1" || none != "" {
		t.Fatal(a, b, none)
	}
	s.free("a")
	if c := started(s); c != "c#1" { // a's second waits behind c's turn
		t.Fatal(c)
	}
	if none := started(s); none != "" {
		t.Fatal("past the limit:", none)
	}
	s.free("b")
	if a := started(s); a != "a#2" {
		t.Fatal(a)
	}
}

// A repo with a running job starts no second one, however many slots are free.
func TestSchedulerRepoAtATime(t *testing.T) {
	s := newScheduler(4, 100)
	queued(s, ev("a", 1, ""), ev("a", 2, ""), ev("a", 1, "issue_comment"))
	if a := started(s); a != "a#1" {
		t.Fatal(a)
	}
	if none := started(s); none != "" {
		t.Fatal("a twice:", none)
	}
	s.free("a")
	if a := started(s); a != "a#1" { // the answer, before the review queued earlier
		t.Fatal(a)
	}
}

// A repo with many PRs doesn't hold up another: they take turns.
func TestSchedulerTurns(t *testing.T) {
	s := newScheduler(1, 100)
	for n := 1; n <= 10; n++ {
		queued(s, ev("a", n, ""))
	}
	queued(s, ev("b", 1, ""))
	var got []string
	for range 4 {
		got = append(got, started(s))
		s.free("a")
		s.free("b")
	}
	if want := []string{"a#1", "b#1", "a#2", "a#3"}; !slices.Equal(got, want) {
		t.Fatal(got)
	}
}

// Answers go before reviews within a repo, each in order; a drop is a review's kind and keeps its place.
func TestSchedulerAnswersFirst(t *testing.T) {
	s := newScheduler(1, 100)
	closed := ev("a", 3, "")
	closed.Action = "closed"
	queued(s, ev("a", 1, ""), ev("a", 1, "issue_comment"), closed, ev("a", 2, "pull_request_review_comment"))
	var got []string
	for range 4 {
		_, e, _ := s.next()
		got = append(got, e.Kind+" "+e.Key())
		s.free("a")
	}
	want := []string{"issue_comment a#1", "pull_request_review_comment a#2", "pull_request a#1", "pull_request a#3"}
	if !slices.Equal(got, want) {
		t.Fatal(got)
	}
}

// Past room waiting jobs the queue is full; a started job makes room. Running ones don't count.
func TestSchedulerFull(t *testing.T) {
	s := newScheduler(1, 2)
	if !s.add("a", ev("a", 1, "")) || !s.add("b", ev("b", 1, "")) || s.add("c", ev("c", 1, "")) {
		t.Fatal("room for 2")
	}
	started(s)
	if !s.add("c", ev("c", 1, "")) || s.add("c", ev("c", 2, "")) {
		t.Fatal("a start makes room for one")
	}
}

// A limit of 1 is one job at a time, whatever the repos, in the order they came.
func TestSchedulerOne(t *testing.T) {
	s := newScheduler(1, 100)
	queued(s, ev("a", 1, ""), ev("b", 1, ""), ev("c", 1, ""))
	for _, want := range []string{"a#1", "b#1", "c#1"} {
		if got := started(s); got != want {
			t.Fatal(got, want)
		}
		if none := started(s); none != "" {
			t.Fatal("two at once:", none)
		}
		s.free(want[:1])
	}
}
