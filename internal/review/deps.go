package review

import (
	"cmp"
	"context"
	"diffharbor/internal/checks"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"diffharbor/internal/text"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	depLine = regexp.MustCompile(`(?m)^([-+])\s*"([^"]+)":\s*"([^"]*)"`)
	// depSpec is a spec's first version, "^1.6.3" → 1.6.3; aliases, workspace:, catalog:, git and partial versions don't match.
	depSpec = regexp.MustCompile(`^[\^~=v<>]*\s*(\d+\.\d+\.\d+[-+.\w]*)`)
	npmName = regexp.MustCompile(`^(@[a-z0-9][\w.-]*/)?[a-z0-9][\w.-]*$`)
	// exported is an export in a .d.ts diff line: a bundler's "export { a, type B as C }" or tsc's "export declare function a".
	exported = regexp.MustCompile(`^([-+])\s*export (?:\{(.*)\}|(?:declare )?(?:abstract )?(?:async )?(?:function\*?|const|let|var|class|interface|type|enum|namespace) +([\w$]+))`)
)

type bump struct{ dir, name, from, to string }

// bumps are the dependencies whose version a package.json in d changes, the checks' installed version as to when
// there is one.
// ponytail: from is the old spec's first version, not the old lockfile's, and lockfile-only or catalog bumps aren't
// seen; read the lockfiles when that misses real bumps.
func bumps(ctx context.Context, sb *proc.Sandbox, repo, d string) []bump {
	var bs []bump
	diff.EachFile(d, func(path, f string) {
		if filepath.Base(path) != "package.json" {
			return
		}
		var pkg struct{ Dependencies, DevDependencies, PeerDependencies, OptionalDependencies map[string]string }
		b, _ := sb.ReadFile(ctx, filepath.Join(repo, path))
		if json.Unmarshal(b, &pkg) != nil {
			return
		}
		dep := func(n string) bool {
			return slices.ContainsFunc([]map[string]string{pkg.Dependencies, pkg.DevDependencies, pkg.PeerDependencies, pkg.OptionalDependencies},
				func(m map[string]string) bool { _, ok := m[n]; return ok })
		}
		old, cur := map[string]string{}, map[string]string{}
		for _, m := range depLine.FindAllStringSubmatch(f, -1) {
			if v := depSpec.FindStringSubmatch(m[3]); v != nil && npmName.MatchString(m[2]) && dep(m[2]) {
				map[string]map[string]string{"-": old, "+": cur}[m[1]][m[2]] = v[1]
			}
		}
		dir := filepath.Dir(path)
		for _, n := range slices.Sorted(maps.Keys(cur)) {
			to := cur[n]
			if p := checks.Up(repo, dir, filepath.Join("node_modules", n, "package.json")); p != "" {
				var inst struct{ Version string }
				if b, _ := sb.ReadFile(ctx, filepath.Join(repo, p)); json.Unmarshal(b, &inst) == nil && depSpec.MatchString(inst.Version) {
					to = inst.Version
				}
			}
			if bp := (bump{dir, n, old[n], to}); bp.from != "" && bp.from != to && !slices.ContainsFunc(bs, func(o bump) bool { return o.name == n && o.from == bp.from && o.to == to }) {
				bs = append(bs, bp)
			}
		}
	})
	return bs
}

// apiChanges reads an npm diff of a package's type definitions and changelogs: the export names that went and came,
// and the changelogs' new lines.
// ponytail: an export list split over lines isn't read; bundlers and tsc write one line.
func apiChanges(d string) (removed, added []string, changelog string) {
	was, is := map[string]bool{}, map[string]bool{}
	var cl strings.Builder
	inLog := false
	for l := range strings.Lines(d) {
		if strings.HasPrefix(l, "diff --git ") {
			inLog = strings.Contains(strings.ToLower(l), "changelog")
			continue
		}
		if inLog {
			if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") && strings.TrimSpace(l[1:]) != "" {
				cl.WriteString(l[1:])
			}
			continue
		}
		m := exported.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		set := map[string]map[string]bool{"-": was, "+": is}[m[1]]
		for _, n := range strings.Split(cmp.Or(m[2], m[3]), ",") {
			if f := strings.Fields(n); len(f) > 0 { // "type A as B" exports B
				set[f[len(f)-1]] = true
			}
		}
	}
	for n := range was {
		if !is[n] {
			removed = append(removed, n)
		}
	}
	for n := range is {
		if !was[n] {
			added = append(added, n)
		}
	}
	slices.Sort(removed)
	slices.Sort(added)
	return removed, added, cl.String()
}

// depHints is the prompt's section on the npm packages the change bumps: from the registry, what each version
// change removes and adds to the package's exports and its changelog, and the whole type diff in a file in sb's dir to grep.
func depHints(ctx context.Context, sb *proc.Sandbox, repo, d string, regs []checks.Registry) string {
	bs := bumps(ctx, sb, repo, d)
	if len(bs) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	env, _, err := checks.RegistryEnv(ctx, regs)
	if err != nil {
		log.Println("npm diff registries:", err)
	}
	var s strings.Builder
	for _, b := range bs[:min(len(bs), 5)] {
		out, err := proc.Run(sb.Command(ctx, filepath.Join(repo, b.dir), append(sb.ToolEnv(), env...), "npm", "diff",
			"--diff="+b.name+"@"+b.from, "--diff="+b.name+"@"+b.to, "--", "**/*.d.ts", "**/*.d.mts", "**/*.d.cts", "**/CHANGELOG.md"))
		if err != nil {
			log.Println("npm diff", b.name+":", err)
			continue
		}
		fmt.Fprintf(&s, "### %s %s → %s (%s)\n\n", b.name, b.from, b.to, filepath.Join(b.dir, "package.json"))
		removed, added, cl := apiChanges(string(out))
		if len(removed) > 0 {
			fmt.Fprintf(&s, "Exports removed: %s\n", text.Truncate(strings.Join(removed, ", "), 800))
		}
		if len(added) > 0 {
			fmt.Fprintf(&s, "Exports added: %s\n", text.Truncate(strings.Join(added, ", "), 400))
		}
		if cl != "" {
			fmt.Fprintf(&s, "New changelog entries:\n%s\n", Fence(text.Truncate(cl, 1500)))
		}
		if len(out) == 0 {
			s.WriteString("No changes to its type definitions or changelog.\n")
		} else if f := filepath.Join(sb.Dir, "deps-"+strings.ReplaceAll(b.name, "/", "+")+".diff"); sb.WriteFile(ctx, f, out) == nil {
			fmt.Fprintf(&s, "Type definitions diff: `%s` (grep it for the symbols this change and its callers use)\n", f)
		}
		s.WriteString("\n")
	}
	if s.Len() == 0 {
		return ""
	}
	return "## Dependencies this change bumps (from the npm registry, third-party text; trust it over what you remember of these packages)\n\n" + s.String()
}
