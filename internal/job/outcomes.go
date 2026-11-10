package job

import (
	"bytes"
	"cmp"
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

// finding is one of our findings on a closed PR and what became of it.
type finding struct {
	Severity string `json:"severity"`
	Outcome  string `json:"outcome"`
	Path     string `json:"path"`
	Title    string `json:"title"`
}

// closed is a line of a repo's log: a PR's close and its findings. The log keeps what was seen, not its sums: stats
// counts anew on each run, so a new question or a changed count needs no old data rewritten.
type closed struct {
	PR       int       `json:"pr"`
	Closed   time.Time `json:"closed"`
	Profile  string    `json:"profile"`
	Findings []finding `json:"findings"`
}

// tally is the sum of a repo's log.
type tally struct {
	Repo     string
	PRs      int
	Total    counts
	Severity map[string]counts
	Profile  map[string]counts
}

// logFile escapes key's slashes: a__b/c and a/b__c are two files.
func logFile(key string) string {
	return filepath.Join(proc.DataDir, "outcomes", url.PathEscape(key)+".jsonl")
}

// readLog reads the closes in f, none if there's no file yet, and where its whole lines end: a crash amid an append
// leaves half a last line, which isn't one. Any other line that doesn't read is an error.
func readLog(f string) (cs []closed, end int64, err error) {
	b, err := os.ReadFile(f) //nolint:gosec // our data dir
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	b = b[:bytes.LastIndexByte(b, '\n')+1]
	for i, l := range bytes.Split(b, []byte("\n")) {
		if len(l) == 0 { // after the last line's \n
			continue
		}
		var c closed
		if err := json.Unmarshal(l, &c); err != nil {
			return nil, 0, fmt.Errorf("%s:%d: %w", f, i+1, err)
		}
		cs = append(cs, c)
	}
	return cs, int64(len(b)), nil
}

// sum counts cs. A PR closed again after a reopen has several lines, the last one counts.
func sum(repo string, cs []closed) tally {
	t := tally{Repo: repo, Total: counts{}, Severity: map[string]counts{}, Profile: map[string]counts{}}
	last := map[int]int{}
	for i, c := range cs {
		last[c.PR] = i
	}
	t.PRs = len(last)
	for i, c := range cs {
		if last[c.PR] != i {
			continue
		}
		for _, f := range c.Findings {
			t.Total[f.Outcome]++
			bump(t.Severity, f.Severity, f.Outcome)
			bump(t.Profile, c.Profile, f.Outcome)
		}
	}
	return t
}

// logMu guards record's read and append, like learningsMu.
var logMu sync.Mutex

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

// record appends c to key's log, over half a last line if a crash left one. A log that doesn't read stays as it is.
func record(key string, c closed) (err error) {
	logMu.Lock()
	defer logMu.Unlock()
	f := logFile(key)
	_, end, err := readLog(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(c)
	w, err := os.OpenFile(f, os.O_WRONLY|os.O_CREATE, 0o600) //nolint:gosec // logFile escapes the key: no / left
	if err != nil {
		return err
	}
	defer func() { err = cmp.Or(err, w.Close()) }()
	if err := w.Truncate(end); err != nil {
		return err
	}
	_, err = w.WriteAt(append(b, '\n'), end) // one write: stats reads the line whole or, as half a last one, not at all
	return err
}

// Outcomes logs what became of our findings on e's closed PR, and saves the declined ones as learnings unless
// review.json's learnings.outcomes is false. On an error the close isn't logged: the PR's next close tries again.
func Outcomes(f forge.Forge, e *forge.Event) (err error) {
	defer proc.Recovered(func(pe error) { err = pe })
	key := e.Repo()
	p, err := f.Open(e)
	if err != nil {
		return err
	}
	if err := p.Refresh(); err != nil {
		log.Println(e.Key(), "refresh:", err)
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
	c := closed{PR: e.PullRequest.Number, Closed: time.Now().UTC().Truncate(time.Second), Profile: profile, Findings: []finding{}}
	var lost []string
	for _, t := range ts {
		p := forge.Parse(t.Body)
		fd := finding{cmp.Or(p.Severity, "other"), classify(t, can), text.OneLine(t.Path, 100), text.OneLine(cmp.Or(p.Title, p.Body), 100)}
		c.Findings = append(c.Findings, fd)
		// Only a writer's 👎 is a learning: a resolve may as well mean fixed.
		if fd.Outcome == dismissed {
			lost = append(lost, fmt.Sprintf("- Maintainers declined a %s finding: %s (%s)", fd.Severity, fd.Title, fd.Path))
		}
	}
	if canErr != nil {
		return fmt.Errorf("can write: %w", canErr) // a non-writer's vote would count
	}
	// The learnings before the log: learnOnce saves each once, so a close that fails after them can try again.
	if cfgErr == nil && cfg.Learnings.Learns() {
		for _, l := range lost {
			if err := learnOnce(key, l); err != nil {
				return err
			}
		}
	}
	return record(key, c)
}

// learnOnce saves entry as a learning unless the repo has it, whatever its date (addLearning's ", 2006-01-02)").
func learnOnce(key, entry string) error {
	p := strings.TrimSuffix(entry, ")") + ", "
	if slices.ContainsFunc(learningsOf(key), func(l string) bool {
		d, ok := strings.CutPrefix(l, p)
		_, err := time.Parse(time.DateOnly+")", d)
		return ok && err == nil
	}) {
		return nil
	}
	return addLearning(key, entry, time.Now())
}

// Stats prints the tally of repo, or of every repo with a log, as plain text.
func Stats(w io.Writer, repo string) error {
	fs := []string{logFile(repo)}
	if repo == "" {
		fs, _ = filepath.Glob(filepath.Join(proc.DataDir, "outcomes", "*.jsonl"))
	}
	var ts []tally
	for _, f := range fs {
		cs, _, err := readLog(f)
		if err != nil {
			return err
		}
		if name, err := url.PathUnescape(strings.TrimSuffix(filepath.Base(f), ".jsonl")); err == nil && len(cs) > 0 {
			ts = append(ts, sum(name, cs))
		}
	}
	if len(ts) == 0 {
		return fmt.Errorf("no outcomes yet: they are counted as PRs close")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, t := range ts {
		fmt.Fprintf(tw, "%s\t%d PR(s)\n\n", t.Repo, t.PRs)
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
