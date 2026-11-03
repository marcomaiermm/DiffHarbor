// Package bench reviews known PRs (planted bugs, merged and clean, or real PRs at the commit a review thread was
// resolved for, the thread being the key) and score the findings against a key, so a prompt or model change gets
// measured instead of eyeballed. Cases come from your repos: add them to benchCases in a file of your own
// (cases_local.go is gitignored), each with a git bundle of its repo in the cache volume. In the container:
//
//	docker exec -w /cache <c> diffharbor bench -n 3 -j 2 [-tools 25] [case...]   (as root: the ChatGPT login is root-only)
//
// Large cases review in parts and cost about max_parts normal cases: they run only when named.
// Each run appends a line to $CACHE_DIR/bench-results.jsonl; a table per case goes to stdout.
package bench

import (
	"bytes"
	"cmp"
	"context"
	"diffharbor/internal/agent"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"diffharbor/internal/review"
	"diffharbor/internal/text"
	"encoding/json"
	"flag"
	"fmt"
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

type spot struct {
	Path  string   // suffix of the finding's path
	Lines [][2]int // any of these ranges ±5; none = anywhere in the file
}

// bug is a known finding: some spot plus one of Words in the title (lower case). Titles keep neighbours apart
// (a leaked transaction vs. a deletable published dataset in the same function), bodies mention everything.
type bug struct {
	ID    string
	At    []spot // none = any file
	Words []string
}

type benchCase struct {
	Name, Title, Base, Head string // the diff is merge-base(Base, Head)..Head, as GitHub shows it
	Bundle                  string // git bundle of the repo, holding Base and Head, in the cache volume
	Large                   bool   // reviewed in parts (the walkthrough's box ticked); runs only when named
	Bugs                    []bug
	Also                    []bug            // legitimate findings outside the key (docs, tests): not noise
	Closed                  []review.Comment // resolved threads, in the prompt as a review after a push sees them
}

func (b bug) matches(c review.Comment) bool {
	t := strings.ToLower(c.Title)
	return slices.ContainsFunc(b.Words, func(w string) bool { return strings.Contains(t, w) }) &&
		(len(b.At) == 0 || slices.ContainsFunc(b.At, func(s spot) bool {
			return strings.HasSuffix(c.Path, s.Path) && (len(s.Lines) == 0 ||
				slices.ContainsFunc(s.Lines, func(r [2]int) bool { return c.Line >= r[0]-5 && c.Line <= r[1]+5 }))
		}))
}

// score returns the bugs found, the findings the key accepts as legitimate extras and the titles of the rest.
func (bc benchCase) score(cs []review.Comment) (found []string, also int, unknown []string) {
	found, unknown = []string{}, []string{}
	for _, b := range bc.Bugs {
		if slices.ContainsFunc(cs, b.matches) {
			found = append(found, b.ID)
		}
	}
	for _, c := range cs {
		switch {
		case slices.ContainsFunc(bc.Bugs, func(b bug) bool { return b.matches(c) }):
		case slices.ContainsFunc(bc.Also, func(b bug) bool { return b.matches(c) }):
			also++
		default:
			unknown = append(unknown, fmt.Sprintf("%s:%d %s", c.Path, c.Line, c.Title)) // where, to check it later
		}
	}
	return found, also, unknown
}

var benchCases []benchCase

type usage struct {
	Input      int            `json:"input"`
	Output     int            `json:"output"`
	CacheRead  int            `json:"cacheRead"`
	CacheWrite int            `json:"cacheWrite"`
	Cost       float64        `json:"cost"`
	Turns      int            `json:"turns"`
	Tools      map[string]int `json:"tools"` // by name; bash calls that run codegraph count as "codegraph"
}

// sessionUsage sums the assistant turns of the Pi sessions in dir (review, verify, their repairs).
func sessionUsage(dir string) usage {
	u := usage{Tools: map[string]int{}}
	files, _ := filepath.Glob(filepath.Join(dir, "*.session*"))
	for _, f := range files {
		b, err := proc.ReadFile(f) // the work dir: PR code ran there
		if err != nil {
			continue
		}
		for d := json.NewDecoder(bytes.NewReader(b)); ; {
			var e struct {
				Type    string
				Message struct {
					Role    string
					Content json.RawMessage
					Usage   struct {
						Input, Output, CacheRead, CacheWrite int
						Cost                                 struct{ Total float64 }
					}
				}
			}
			if d.Decode(&e) != nil {
				break
			}
			if e.Type != "message" || e.Message.Role != "assistant" {
				continue
			}
			m := e.Message.Usage
			u.Input, u.Output, u.CacheRead, u.CacheWrite = u.Input+m.Input, u.Output+m.Output, u.CacheRead+m.CacheRead, u.CacheWrite+m.CacheWrite
			u.Cost += m.Cost.Total
			u.Turns++
			var blocks []struct {
				Type, Name string
				Arguments  struct{ Command string }
			}
			_ = json.Unmarshal(e.Message.Content, &blocks) // text-only content has no blocks
			for _, c := range blocks {
				if c.Type == "toolCall" {
					u.Tools[c.Name]++
					if c.Name == "bash" && strings.Contains(c.Arguments.Command, "codegraph") {
						u.Tools["codegraph"]++
					}
				}
			}
		}
	}
	return u
}

// prepare clones the case into dir with HEAD at the merge base and the PR's changes staged, as review.Tree expects.
func prepare(c benchCase, dir string) (string, error) {
	git := func(args ...string) (string, error) {
		out, err := proc.Run(proc.Command(context.Background(), filepath.Dir(dir), nil, "git", args...))
		return string(out), err
	}
	if _, err := git("clone", "-q", "--no-checkout", c.Bundle, dir); err != nil {
		return "", err
	}
	base, err := git("-C", dir, "merge-base", c.Base, c.Head)
	if err != nil {
		return "", err
	}
	if _, err := git("-C", dir, "checkout", "-q", c.Head); err != nil {
		return "", err
	}
	if _, err := git("-C", dir, "reset", "-q", "--soft", strings.TrimSpace(base)); err != nil {
		return "", err
	}
	d, err := git("-C", dir, "diff", "HEAD")
	parts, _ := diff.Filter(d, nil, nil, 1)
	return strings.Join(parts, ""), err
}

type benchRun struct {
	Time    time.Time `json:"time"`
	Case    string    `json:"case"`
	Model   string    `json:"model"`
	Verify  string    `json:"verify,omitempty"` // VERIFY_MODEL, if set
	Stats   string    `json:"stats"`
	Err     string    `json:"err,omitempty"`
	Secs    int       `json:"secs"`
	Bugs    int       `json:"bugs"`
	Found   []string  `json:"found"`
	Also    int       `json:"also"`
	Unknown []string  `json:"unknown"`
	// Findings has every comment with the start of its body, to see what a title hides (two places in one finding).
	Findings []string `json:"findings,omitempty"`
	Usage    usage    `json:"usage"`
	Level    string   `json:"level"` // what level() routes this diff to
}

func benchOnce(c benchCase) benchRun {
	r := benchRun{Time: time.Now().UTC(), Case: c.Name, Model: cmp.Or(agent.Model, agent.Default()), Verify: agent.VerifyModel, Bugs: len(c.Bugs)}
	dir, err := proc.Workspace()
	if err != nil {
		r.Err = err.Error()
		return r
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "src")
	d, err := prepare(c, src)
	if err == nil {
		r.Level = review.Level(d, diff.Paths(d))
		t := time.Now()
		var res review.Result
		res, r.Stats, err = review.Tree(context.Background(), src, dir, c.Title, c.Closed, c.Large)
		r.Secs = int(time.Since(t).Seconds())
		r.Found, r.Also, r.Unknown = c.score(res.Comments)
		for _, cm := range res.Comments {
			r.Findings = append(r.Findings, fmt.Sprintf("%s:%d %s: %s", cm.Path, cm.Line, cm.Title, text.OneLine(cm.Body, 300)))
		}
	}
	if err != nil {
		r.Err = err.Error()
	}
	r.Usage = sessionUsage(dir)
	return r
}

// Run runs the cases args name (all without), each -n times, -j at once, and prints a table per case.
func Run(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	n := fs.Int("n", 3, "runs per case")
	j := fs.Int("j", 4, "runs at once")
	fs.IntVar(&agent.MaxTools, "tools", agent.MaxTools, "tool calls per pass")
	_ = fs.Parse(args) // ExitOnError
	cases := slices.DeleteFunc(slices.Clone(benchCases), func(c benchCase) bool {
		return fs.NArg() == 0 && c.Large || fs.NArg() > 0 && !slices.Contains(fs.Args(), c.Name)
	})
	f, err := os.OpenFile(filepath.Join(proc.CacheDir, "bench-results.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "case\trecall\tper bug\talso\tunknown\tsecs\ttokens in/out/cached\tcost\ttools\tlevel")
	// Runs wait on the model API, not the CPU; -j is bounded by the container's 3 GB (Pi + checks per run).
	runs := make([][]benchRun, len(cases))
	sem := make(chan struct{}, max(1, *j))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for ci, c := range cases {
		runs[ci] = make([]benchRun, *n)
		for i := range *n {
			wg.Go(func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				r := benchOnce(c)
				runs[ci][i] = r
				b, _ := json.Marshal(r)
				mu.Lock()
				defer mu.Unlock()
				if _, err := f.Write(append(b, '\n')); err != nil {
					log.Println("bench-results.jsonl:", err)
				}
				log.Printf("%s #%d: %s found %v, %d also, %d unknown, %ds %s", c.Name, i+1, r.Stats, r.Found, r.Also, len(r.Unknown), r.Secs, r.Err)
			})
		}
	}
	wg.Wait()
	var noise []string
	for ci, c := range cases {
		fmt.Fprintln(tw, summary(c, runs[ci]))
		for _, r := range runs[ci] {
			for _, t := range r.Unknown {
				noise = append(noise, c.Name+": "+t)
			}
		}
	}
	_ = tw.Flush()
	if len(noise) > 0 {
		fmt.Println("\nnot in the key (noise, or a real bug to add):\n  " + strings.Join(noise, "\n  "))
	}
	return nil
}

// summary is one table row: sums over the runs, averages per run.
func summary(c benchCase, runs []benchRun) string {
	n := len(runs)
	var found, also, unknown, secs, in, out, cached int
	var cost float64
	tools := map[string]int{}
	hits := map[string]int{}
	for _, r := range runs {
		found += len(r.Found)
		also += r.Also
		unknown += len(r.Unknown)
		secs += r.Secs
		in, out, cached = in+r.Usage.Input, out+r.Usage.Output, cached+r.Usage.CacheRead
		cost += r.Usage.Cost
		for k, v := range r.Usage.Tools {
			tools[k] += v
		}
		for _, id := range r.Found {
			hits[id]++
		}
	}
	var perBug, tl []string
	for _, b := range c.Bugs {
		perBug = append(perBug, fmt.Sprintf("%s %d/%d", b.ID, hits[b.ID], n))
	}
	for _, k := range slices.Sorted(maps.Keys(tools)) {
		tl = append(tl, fmt.Sprintf("%s %.1f", k, float64(tools[k])/float64(n)))
	}
	recall := "-"
	if len(c.Bugs) > 0 {
		recall = fmt.Sprintf("%d/%d", found, len(c.Bugs)*n)
	}
	avg := func(v int) int { return v / n }
	return fmt.Sprintf("%s\t%s\t%s\t%.1f\t%.1f\t%d\t%dk/%dk/%dk\t$%.3f\t%s\t%s", c.Name, recall, strings.Join(perBug, " "),
		float64(also)/float64(n), float64(unknown)/float64(n), avg(secs), avg(in)/1000, avg(out)/1000, avg(cached)/1000,
		cost/float64(n), strings.Join(tl, " "), runs[0].Level)
}
