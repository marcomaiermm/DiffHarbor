package job

import (
	"cmp"
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"diffharbor/internal/review"
	"diffharbor/internal/text"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// What became of a finding when its PR closed.
const (
	addressed = "addressed" // our "✅ Addressed in" reply
	accepted  = "accepted"  // 👍 by someone who can push
	dismissed = "dismissed" // resolved by hand, or 👎 by someone who can push
	ignored   = "ignored"   // still open
)

var outcomeNames = []string{addressed, accepted, dismissed, ignored}

// counts is outcome -> findings.
type counts map[string]int

// Tally is a repo's outcomes: PRs are the closed PRs counted already, so a reopened one isn't counted twice.
type Tally struct {
	Repo     string
	PRs      []int
	Total    counts
	Severity map[string]counts
	Profile  map[string]counts
}

func tallyFile(key string) string {
	return filepath.Join(proc.DataDir, "outcomes", strings.ReplaceAll(key, "/", "__")+".json")
}

func readTally(key string) (t Tally) {
	b, _ := os.ReadFile(tallyFile(key))
	_ = json.Unmarshal(b, &t)
	return t
}

// tallyMu guards the read-modify-write of record, like learningsMu.
var tallyMu sync.Mutex

// classify is the outcome of a finding's thread; can says whether a login may push: on a public repo anyone can react.
// A 👎 wins over a 👍.
func classify(t forge.Thread, can func(login string) bool) string {
	switch {
	case t.Addressed:
		return addressed
	case slices.ContainsFunc(t.Down, can):
		return dismissed
	case slices.ContainsFunc(t.Up, can):
		return accepted
	case t.Resolved:
		return dismissed
	}
	return ignored
}

func bump(m map[string]counts, k, o string) {
	if m[k] == nil {
		m[k] = counts{}
	}
	m[k][o]++
}

// record adds a closed PR's outcomes (severity and the PR's profile per finding) to key's tally. It reports false
// when the PR was counted before.
func record(key string, pr int, profile string, sevs, outs []string) (bool, error) {
	tallyMu.Lock()
	defer tallyMu.Unlock()
	t := readTally(key)
	if slices.Contains(t.PRs, pr) {
		return false, nil
	}
	t.Repo, t.PRs = key, append(t.PRs, pr)
	if t.Total == nil {
		t.Total, t.Severity, t.Profile = counts{}, map[string]counts{}, map[string]counts{}
	}
	for i, o := range outs {
		t.Total[o]++
		bump(t.Severity, sevs[i], o)
		bump(t.Profile, profile, o)
	}
	b, _ := json.Marshal(t)
	f := tallyFile(key)
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return false, err
	}
	// Through a rename: stats never reads half a file.
	if err := os.WriteFile(f+".tmp", b, 0o600); err != nil {
		return false, err
	}
	return true, os.Rename(f+".tmp", f)
}

// Outcomes counts what became of our findings on e's closed PR, and saves the declined ones as learnings unless
// review.json's learnings.outcomes is false. Its errors only log: the PR's reports go whatever happens here.
// ponytail: a thread resolved by the chat pass after a decline counts as dismissed and learns the decline too;
// telling it apart means reading the whole thread, not Answers' last three.
func Outcomes(f forge.Forge, e *forge.Event) (err error) {
	defer proc.Recovered(func(pe error) { err = pe })
	key, n := e.Repo(), e.PullRequest.Number
	if slices.Contains(readTally(key).PRs, n) {
		return nil
	}
	p, err := f.Open(e)
	if err != nil {
		return err
	}
	if err := p.Refresh(); err != nil {
		log.Println(e.Key(), "refresh:", err) // the event has the PR, as it was when it closed
	}
	cfg, _ := review.LoadSettings(p.File(e.PullRequest.Base.SHA, "review.json"))
	profile := cmp.Or(bodyProfile(e.PullRequest.Body), cfg.Profile, "default")
	ts, err := p.Voted()
	if err != nil {
		return err
	}
	writers := map[string]bool{}
	can := func(login string) bool {
		ok, seen := writers[login]
		if !seen {
			var err error
			if ok, err = p.CanWrite(login); err != nil {
				return false // not cached: a later thread asks again
			}
			writers[login] = ok
		}
		return ok
	}
	var sevs, outs, lost []string
	for _, t := range ts {
		c := forge.Parse(t.Body)
		sevs, outs = append(sevs, cmp.Or(c.Severity, "other")), append(outs, classify(t, can))
		if outs[len(outs)-1] == dismissed {
			lost = append(lost, fmt.Sprintf("- Maintainers declined a %s finding: %s (%s)", cmp.Or(c.Severity, "minor"),
				text.OneLine(cmp.Or(c.Title, c.Body), 100), text.OneLine(t.Path, 100)))
		}
	}
	if fresh, err := record(key, n, profile, sevs, outs); err != nil || !fresh || !cfg.Learnings.Learns() {
		return err
	}
	for _, l := range lost {
		if err := learnOnce(key, l); err != nil {
			return err
		}
	}
	return nil
}

// learnOnce saves entry as a learning unless the repo has it, whatever its date.
func learnOnce(key, entry string) error {
	if slices.ContainsFunc(learningsOf(key), func(l string) bool { return strings.HasPrefix(l, strings.TrimSuffix(entry, ")")+", ") }) {
		return nil
	}
	return addLearning(key, entry, time.Now())
}

// Stats prints the tally of repo, or of every repo with one, as plain text.
func Stats(w io.Writer, repo string) error {
	var ts []Tally
	if repo != "" {
		ts = append(ts, readTally(repo))
	} else {
		fs, _ := filepath.Glob(filepath.Join(proc.DataDir, "outcomes", "*.json"))
		for _, f := range fs {
			b, _ := os.ReadFile(f) //nolint:gosec // Glob of our own dir
			var t Tally
			if json.Unmarshal(b, &t) == nil {
				ts = append(ts, t)
			}
		}
	}
	if len(ts) == 0 || repo != "" && ts[0].Repo == "" {
		return fmt.Errorf("no outcomes yet: they are counted as PRs close")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, t := range ts {
		fmt.Fprintf(tw, "%s\t%d PRs\n\n", t.Repo, len(t.PRs))
		fmt.Fprintln(tw, "\tfindings\taddressed\taccepted\tdismissed\tignored")
		row(tw, "all", t.Total)
		for _, g := range []struct {
			name string
			m    map[string]counts
		}{{"severity", t.Severity}, {"profile", t.Profile}} {
			for _, k := range slices.Sorted(maps.Keys(g.m)) {
				row(tw, g.name+" "+k, g.m[k])
			}
		}
		fmt.Fprintln(tw)
	}
	return tw.Flush()
}

// row prints a table row: the findings of c and the share of each outcome.
func row(w io.Writer, name string, c counts) {
	total := 0
	for _, o := range outcomeNames {
		total += c[o]
	}
	cells := []string{fmt.Sprint(total)}
	for _, o := range outcomeNames {
		cells = append(cells, fmt.Sprintf("%.0f%%", 100*float64(c[o])/float64(max(total, 1))))
	}
	fmt.Fprintln(w, name+"\t"+strings.Join(cells, "\t"))
}
