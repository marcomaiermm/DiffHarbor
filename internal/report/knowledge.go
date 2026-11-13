package report

import (
	"context"
	"diffharbor/internal/proc"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// PR is what the header says of the pull request: from the forge's payload, the commits from git.
type PR struct {
	Created    time.Time
	Author     string
	Head, Base string // branch names
	Body       string // the description, uncut
	Commits    int    // without merges
	Merges     bool   // the branch holds merges: the header says "without merges" only then
}

// Header sets pr's commit count and whether merges were left out, from a Checkout's history: refs/base..HEAD.
// Both stay zero when git fails.
func Header(ctx context.Context, sb *proc.Sandbox, pr PR) PR {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := proc.Run(sb.Command(ctx, sb.Repo(), nil, "git", "log", "--format=%P", "refs/base..HEAD"))
	if err != nil {
		return pr
	}
	for l := range strings.Lines(string(out)) {
		if strings.Contains(strings.TrimSpace(l), " ") { // two parents
			pr.Merges = true
		} else {
			pr.Commits++
		}
	}
	return pr
}

// Know is what git says of a file at the base: how long ago it last changed and who changed it lately.
type Know struct {
	Days   int      // since its last change, counted to the PR's creation
	People []string // who committed to it in the 12 months before, alphabetical; never counted or ranked
}

// bulk is the files a commit touches from which on it isn't a change to any of them: a mass format, a move, an import.
const bulk = 150

// bots: a [bot] account, or GitLab's project and group tokens (project_12_bot_ab12).
var bots = regexp.MustCompile(`(?i)\[bot\]|^(project|group)_\d+_bot`)

// Learn reads, for each of paths (as the base has them), what Know says, from one pass of git log over the history
// Checkout fetched, up to created. Renames are followed, exact ones only: the checkout has no blobs, and a
// similarity check would fetch them one by one. A file with no change in that history, and every file when git fails
// or created is unknown, has no entry. Names go through the repo's mailmap; commits of bulk files or more and bots
// don't count.
func Learn(ctx context.Context, sb *proc.Sandbox, paths []string, created time.Time) map[string]Know {
	if created.IsZero() || len(paths) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	// quotepath off: a name with a tab, quote or backslash still comes quoted and finds no file; ponytail: no -z
	out, err := proc.Run(sb.Command(ctx, sb.Repo(), nil, "git", "-c", "core.quotepath=off", "log", "--no-merges",
		"--find-renames=100%", "--name-status", "--until="+created.Format(time.RFC3339), "--format=%x01%ct%x01%aN%x01%aE", "refs/base"))
	if err != nil {
		return nil
	}
	return learn(string(out), paths, created)
}

// learn walks the log newest first, keeping for each name the file it is at the base as it reads back through renames.
func learn(log string, paths []string, created time.Time) map[string]Know {
	at := map[string]string{} // a path at this point of the history -> the path at the base
	for _, p := range paths {
		at[p] = p
	}
	who := map[string]map[string]string{} // path at the base -> email -> name
	ks := map[string]Know{}
	since := created.AddDate(-1, 0, 0)
	var when time.Time
	var name, email string
	var n int // files the commit touches
	var touched []string
	var renames [][2]string // old, new
	flush := func() {
		if n > 0 && n < bulk {
			bot := bots.MatchString(name) || bots.MatchString(email)
			for _, p := range touched {
				o := at[p]
				if _, ok := ks[o]; !ok {
					ks[o] = Know{Days: max(0, int(created.Sub(when).Hours()/24))}
				}
				if bot || when.Before(since) {
					continue
				}
				if who[o] == nil {
					who[o] = map[string]string{}
				}
				who[o][strings.ToLower(email)] = name
			}
		}
		for _, r := range renames { // older commits know the file by its old name
			at[r[0]] = at[r[1]]
			delete(at, r[1])
		}
		n, touched, renames = 0, nil, nil
	}
	for l := range strings.Lines(log) {
		l = strings.TrimRight(l, "\n")
		if rest, ok := strings.CutPrefix(l, "\x01"); ok {
			flush()
			f := strings.Split(rest, "\x01")
			if len(f) != 3 {
				continue
			}
			ts, _ := strconv.ParseInt(f[0], 10, 64)
			when, name, email = time.Unix(ts, 0), f[1], f[2]
			continue
		}
		f := strings.Split(l, "\t")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		n++
		cur := f[len(f)-1] // a rename's new name
		if _, ok := at[cur]; ok {
			touched = append(touched, cur)
			if f[0][0] == 'R' && len(f) == 3 {
				renames = append(renames, [2]string{f[1], cur})
			}
		}
	}
	flush()
	for o, m := range who {
		k := ks[o]
		k.People = slices.Compact(slices.Sorted(maps.Values(m)))
		ks[o] = k
	}
	return ks
}

// Group is what Know says of a group of files: a part of the map.
type Group struct {
	Known          int      // files of the group git knows
	Median, Oldest int      // days since their last change
	People         []string // who changed any of them, alphabetical, once each: never counted or ranked
}

// Aggregate sums up paths' entries in k. Files without one don't count; with none known, Known is 0.
func Aggregate(k map[string]Know, paths []string) (g Group) {
	var days []int
	seen := map[string]bool{}
	for _, p := range paths {
		if e, ok := k[p]; ok {
			days = append(days, e.Days)
			for _, w := range e.People {
				if !seen[w] {
					seen[w] = true
					g.People = append(g.People, w)
				}
			}
		}
	}
	slices.Sort(days)
	slices.Sort(g.People)
	if g.Known = len(days); g.Known > 0 {
		g.Median, g.Oldest = (days[(g.Known-1)/2]+days[g.Known/2])/2, days[g.Known-1]
	}
	return g
}
