package job

import (
	"cmp"
	"crypto/sha256"
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"diffharbor/internal/review"
	"diffharbor/internal/text"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/url"
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
	dismissed = "dismissed" // 👎 by someone who can push
	resolved  = "resolved"  // resolved by hand, no such vote: fixed or declined, we can't tell
	ignored   = "ignored"   // still open
)

var outcomeNames = []string{addressed, accepted, dismissed, resolved, ignored}

// counts is outcome -> findings.
type counts map[string]int

// tally is a repo's outcomes: PRs are the closed PRs counted already, so a reopened one isn't counted twice.
type tally struct {
	Repo     string
	PRs      []int
	Total    counts
	Severity map[string]counts
	Profile  map[string]counts
}

// tallyFile escapes key's slashes: a__b/c and a/b__c are two files. A file name holds 255 bytes: a longer one, a
// GitLab project deep in groups, is the key's hash, and the tally has the name.
func tallyFile(key string) string {
	name := url.PathEscape(key)
	if len(name) > 200 {
		name = fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	}
	return filepath.Join(proc.DataDir, "outcomes", name+".json")
}

func readTally(key string) (tally, error) { return loadTally(tallyFile(key)) }

// loadTally reads the tally in f, an empty one if there's none yet. One that doesn't read is an error: record
// mustn't start it over.
func loadTally(f string) (t tally, err error) {
	b, err := os.ReadFile(f) //nolint:gosec // our data dir
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return t, fmt.Errorf("%s: %w", f, err)
	}
	return t, nil
}

// maxLearnedPerClose bounds the new learnings one close saves (ones the repo has don't count): a PR of many 👎
// mustn't push the curated ones out of the newest a review follows.
const maxLearnedPerClose = 3

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
		return resolved
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
	t, err := readTally(key)
	if err != nil || slices.Contains(t.PRs, pr) {
		return false, err
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
// review.json's learnings.outcomes is false. On an error before the count the PR isn't counted: its next close
// tries again.
func Outcomes(f forge.Forge, e *forge.Event) (err error) {
	defer proc.Recovered(func(pe error) { err = pe })
	key, n := e.Repo(), e.PullRequest.Number
	if t, err := readTally(key); err != nil || slices.Contains(t.PRs, n) {
		return err
	}
	p, err := f.Open(e)
	if err != nil {
		return err
	}
	if err := p.Refresh(); err != nil { // it may have emptied the description: the profile would be a guess
		return err
	}
	cfg, cfgErr := review.LoadSettings(p.File(e.PullRequest.Base.SHA, "review.json"))
	if cfgErr != nil {
		log.Println(e.Key(), "review.json:", cfgErr) // learnings.outcomes unknown: no learnings
	}
	profile := cmp.Or(bodyProfile(e.PullRequest.Body), cfg.Profile, "default")
	ts, err := p.Voted()
	if err != nil {
		return err
	}
	writers := map[string]bool{}
	var canErr error
	can := func(login string) bool {
		if login == "" || canErr != nil {
			return false
		}
		ok, seen := writers[login]
		if !seen {
			if ok, canErr = p.CanWrite(login); canErr != nil {
				return false
			}
			writers[login] = ok
		}
		return ok
	}
	var sevs, outs, lost []string
	for _, t := range ts {
		c := forge.Parse(t.Body)
		sev := cmp.Or(c.Severity, "other")
		o := classify(t, can)
		sevs, outs = append(sevs, sev), append(outs, o)
		// Only a writer's 👎 is a learning: a resolve may as well mean fixed.
		if o == dismissed {
			lost = append(lost, fmt.Sprintf("- Maintainers declined a %s finding: %s (%s)", sev,
				text.OneLine(cmp.Or(c.Title, c.Body), 100), text.OneLine(t.Path, 100)))
		}
	}
	if canErr != nil {
		return fmt.Errorf("can write: %w", canErr) // a non-writer's vote would count
	}
	// The count before the learnings: only the close that counts the PR saves them, so a retry never adds past
	// maxLearnedPerClose. A failed save isn't tried again: they are a bounded few anyway.
	counted, err := record(key, n, profile, sevs, outs)
	if err != nil || !counted || cfgErr != nil || !cfg.Learnings.Learns() {
		return err
	}
	saved := 0
	for _, l := range lost {
		if saved == maxLearnedPerClose {
			break
		}
		ok, err := learnOnce(key, l)
		if err != nil {
			return err
		}
		if ok {
			saved++
		}
	}
	return nil
}

// learnOnce saves entry as a learning unless the repo has it, whatever its date (addLearning's ", 2006-01-02)"),
// and says whether it did.
func learnOnce(key, entry string) (bool, error) {
	p := strings.TrimSuffix(entry, ")") + ", "
	if slices.ContainsFunc(learningsOf(key), func(l string) bool {
		d, ok := strings.CutPrefix(l, p)
		_, err := time.Parse(time.DateOnly+")", d)
		return ok && err == nil
	}) {
		return false, nil
	}
	return true, addLearning(key, entry, time.Now())
}

// Stats prints the tally of repo, or of every repo with one, as plain text.
func Stats(w io.Writer, repo string) error {
	fs := []string{tallyFile(repo)}
	if repo == "" {
		fs, _ = filepath.Glob(filepath.Join(proc.DataDir, "outcomes", "*.json"))
	}
	var ts []tally
	for _, f := range fs {
		t, err := loadTally(f)
		if err != nil {
			return err
		}
		ts = append(ts, t)
	}
	if len(ts) == 0 || repo != "" && ts[0].Repo == "" {
		return fmt.Errorf("no outcomes yet: they are counted as PRs close")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, t := range ts {
		fmt.Fprintf(tw, "%s\t%d PR(s)\n\n", t.Repo, len(t.PRs))
		fmt.Fprintln(tw, "\tfindings\t"+strings.Join(outcomeNames, "\t"))
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
