package report

import (
	"cmp"
	"context"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"log"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Know is what git says of a file at the base: how long ago it last changed and who changed it lately.
type Know struct {
	Days   int      // since its last change, counted to the PR's creation
	People []string // who committed to it in the 12 months before, alphabetical; never counted or ranked
}

// bulk is the files a commit touches or more: it is no change to any of them, a mass format, a move, an import.
const bulk = 150

// bots: a [bot] account, or GitLab's project and group tokens (project_12_bot_ab12).
var bots = regexp.MustCompile(`(?i)\[bot\]|^(project|group)_\d+_bot`)

// Learn reads, for each file of the diff raw (by its path in the PR), what Know says, from one pass of git log over
// the history Checkout fetched, up to created (-n bounds it after an unshallow). A file the PR renames has the history
// of its old name. Renames in the history are followed, exact ones only: the checkout has no blobs, and a similarity
// check would need them, which fails the whole log (no lazy fetch).
// A file with no change in that history, and every file when git fails or created is unknown, has no entry. Names go
// through the base's mailmap; commits of bulk files or more don't count, and a bot's counts for Days but is no person. The first commit of the history
// reads as no change: in a shallow checkout it is only where the fetch stopped, and it would add every file.
func Learn(ctx context.Context, sb *proc.Sandbox, raw string, created time.Time) map[string]Know {
	paths := diff.Paths(raw)
	if created.IsZero() || len(paths) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	// From the git dir, with the mailmap of the base: the worktree is the PR's, and a PR doesn't decide who is who. No lazy
	// fetch: a base without .mailmap in the partial clone only warns. ponytail: no -z, quotepath off keeps most names whole
	out, err := proc.Run(sb.Command(ctx, filepath.Join(sb.Repo(), ".git"), []string{"GIT_NO_LAZY_FETCH=1"}, "git", "-c", "core.quotepath=off",
		"-c", "log.showRoot=false", "-c", "mailmap.blob=refs/base:.mailmap", "log", "-n", "3000", "--no-merges", "--find-renames=100%", "--name-status",
		"--until="+created.Format(time.RFC3339), "--format=%x01%ct%x01%aN%x01%aE", "refs/base"))
	if err != nil {
		log.Println("knowledge:", err)
		return nil
	}
	return learn(string(out), paths, renamed(raw), created)
}

var renameOf = regexp.MustCompile(`(?m)^rename from (.+)\nrename to (.+)$`)

// renamed maps the old name of each file the diff renames to its new one.
func renamed(raw string) map[string]string {
	m := map[string]string{}
	for _, r := range renameOf.FindAllStringSubmatch(raw, -1) {
		m[r[1]] = r[2]
	}
	return m
}

// learn walks the log newest first, keeping for each name the file it is at the base as it reads back through renames.
// prs maps what the PR renamed to its new path, which keys the result.
func learn(log string, paths []string, prs map[string]string, created time.Time) map[string]Know {
	at := map[string]string{} // a path at this point of the history -> the path in the PR
	for _, p := range paths {
		at[p] = p
	}
	for old, cur := range prs {
		at[old] = cur
	}
	who := map[string]map[string]bool{} // path in the PR -> emails
	names := map[string]string{}        // email -> its newest name, so one person is one name in every file
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
				e := strings.ToLower(email)
				if who[o] == nil {
					who[o] = map[string]bool{}
				}
				who[o][e] = true
				if _, ok := names[e]; !ok {
					names[e] = name
				}
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
		for e := range m {
			k.People = append(k.People, names[e])
		}
		sortPeople(k.People)
		k.People = slices.Compact(k.People)
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
	sortPeople(g.People)
	if g.Known = len(days); g.Known > 0 {
		g.Median, g.Oldest = (days[(g.Known-1)/2]+days[g.Known/2])/2, days[g.Known-1]
	}
	return g
}

// sortPeople puts names in the order a person reads: "alice" before "Zed".
func sortPeople(n []string) {
	slices.SortFunc(n, func(a, b string) int {
		return cmp.Or(strings.Compare(strings.ToLower(a), strings.ToLower(b)), strings.Compare(a, b))
	})
}
