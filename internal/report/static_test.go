package report

import (
	"context"
	"diffharbor/internal/diff"
	"diffharbor/internal/proc"
	"diffharbor/internal/review"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// staticRepo is a checkout of a PR on its base: each file of base, then head over it ("" deletes). It returns the
// sandbox, pinned as Checkout leaves it, and the diff base..HEAD.
func staticRepo(t *testing.T, base, head map[string]string) (*proc.Sandbox, string) {
	t.Helper()
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi = false
	sb := &proc.Sandbox{Dir: t.TempDir()}
	dir := sb.Repo()
	git := func(args ...string) string {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(c.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	write := func(files map[string]string) {
		for p, s := range files {
			f := filepath.Join(dir, p)
			if s == "" {
				git("rm", "-q", p)
				continue
			}
			if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git("add", "-A")
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git("init", "-q", "-b", "main")
	write(base)
	git("commit", "-qm", "base")
	git("update-ref", "refs/base", "HEAD")
	write(head)
	git("commit", "-qm", "pr")
	if err := sb.Pin(); err != nil {
		t.Fatal(err)
	}
	return sb, git("diff", "-M", "refs/base", "HEAD")
}

// The groups of Test changes, files by kind, the readers of env variables, what names a migration and excerpts of
// unchanged files and of the base, from one PR.
func TestStatic(t *testing.T) {
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi = false
	base := map[string]string{
		"src/lib/gone.ts":  "export const gone = 1\n",
		"src/lib/still.ts": "export const s = 1\n",
		"src/router.ts":    "import { s } from './lib/still'\n\nexport const router = {\n  quickAdd: () => ({ inserted: 0 }),\n}\n",
		"src/env.ts":       "export const env = {\n  DATABASE_URL: z.string(),\n}\n",
		"src/db.ts":        "const a = process.env.DATABASE_URL\nconst b = process.env.OLD_URL\nconst c = 3\n",
		"docs/old.md":      "The gone module is gone.\n",
		"go/total.go":      "package total\n\nfunc totalNet() int { return 1 }\n",
		"go/total_test.go": "package total\n\nimport \"testing\"\n\nfunc TestTotalNet(t *testing.T) {}\n",
		// group 1 by its imports: two blocks, an .each table once
		"tests/gone.test.ts": "import { gone } from '../src/lib/gone'\n\nit('reads one', () => {})\nit.each([['a'], ['b']])('row %s', (n) => {})\ntest(\"reads two\", () => {})\n",
		// group 1 by the search: what it tests, its describe title, no code at the head names
		"tests/vanished.test.ts": "import { setup } from '../tests/helpers'\n\ndescribe('lib.vanishedFn', () => {\n  it('works', () => {})\n})\n",
		// group 2: the router at the head still has quickAdd
		"tests/quick.test.ts": "import { router } from '~/router'\n\ndescribe(\"router.quickAdd\", () => {\n  it('skips players', () => {})\n  it('uses owners', () => {})\n})\n",
		// nothing to search for
		"tests/plain.test.ts": "import { x } from 'vitest'\n\nit('a', () => {})\n",
		"tests/stay.test.ts":  "it('one', () => {\n  expect(a).toBe(1)\n})\nit('two', () => {\n  expect(b).toBe(2)\n})\n",
		"tests/fixture.json":  "{}\n",
		"tests/helpers.ts":    "export const setup = 1\n", // no tests: in no group
	}
	head := map[string]string{
		"src/lib/gone.ts":                 "",
		"src/lib/still.ts":                "",
		"go/total.go":                     "",
		"go/total_test.go":                "",
		"src/env.ts":                      "export const env = {\n  DATABASE_URL: z.string(),\n  NEW_URL: process.env.NEW_URL,\n}\n",
		"src/db.ts":                       "const c = 3\n",
		"src/client.ts":                   "fetch(env.NEW_URL)\n",
		"tests/gone.test.ts":              "",
		"tests/vanished.test.ts":          "",
		"tests/quick.test.ts":             "",
		"tests/plain.test.ts":             "",
		"tests/fixture.json":              "",
		"tests/helpers.ts":                "",
		"tests/stay.test.ts":              "it('one', () => {\n  expect(a).toBe(1)\n})\n",
		"drizzle/0059_drop_items.sql":     "DROP TABLE items;\n",
		"drizzle/meta/0059_snapshot.json": "{\"id\": 59}\n",
		"docs/runbook.md":                 "Run 0059_drop_items before the image.\n",
		"tests/migrate.test.ts":           "it('applies 0059_drop_items', () => {})\n",
		".github/workflows/ci.yml":        "on: push\n",
	}
	sb, raw := staticRepo(t, base, head)
	d := Data{Diff: raw, Outline: diff.Analyze(raw), Omitted: []string{"src/client.ts"}, Sources: Sources(sb, raw)}
	Static(context.Background(), sb, &d, []string{"src/env.ts", "src/db.ts", "src/client.ts", "tests/stay.test.ts", "tests/migrate.test.ts"})

	tc := d.TestChanges
	paths := func(fs []TestFile) (out []string) {
		for _, f := range fs {
			out = append(out, fmt.Sprintf("%s %s %v %d", f.Path, f.Imports, f.Names, len(f.Tests)))
		}
		slices.Sort(out)
		return out
	}
	want := []string{
		"go/total_test.go go/total.go [] 1",
		"tests/gone.test.ts src/lib/gone.ts [] 3",
		"tests/vanished.test.ts  [vanishedFn] 1",
	}
	if got := paths(tc.With); !slices.Equal(got, want) {
		t.Errorf("group 1:\n%q\nwant\n%q", got, want)
	}
	if got := paths(tc.Kept); !slices.Equal(got, []string{"tests/quick.test.ts  [quickAdd] 2"}) {
		t.Errorf("group 2: %q", got)
	}
	if k := tc.Kept[0]; !slices.Equal(k.Found, []string{"src/router.ts:4"}) || k.Tests[1] != (TestCase{"uses owners", 5}) {
		t.Errorf("group 2's evidence: %+v", k)
	}
	if got := paths(tc.Unsorted); !slices.Equal(got, []string{"tests/plain.test.ts  [] 1"}) {
		t.Errorf("unsorted: %q", got)
	}
	if g := tc.With[1].Tests; !reflect.DeepEqual(g, []TestCase{{"reads one", 3}, {"row %s", 4}, {"reads two", 5}}) {
		t.Errorf("cases: %+v", g)
	}
	if len(tc.Stay) != 1 || tc.Stay[0].Path != "tests/stay.test.ts" || len(tc.Stay[0].Flags) != 2 {
		t.Errorf("group 3: %+v", tc.Stay)
	}

	kinds := map[string]string{}
	for _, k := range d.Kinds {
		kinds[k.Kind] = fmt.Sprintf("%v +%d -%d read %d deleted %d generated %d named %d cut %v", k.Files, k.Add, k.Del, k.Read, k.Deleted, k.Generated, k.Named, k.Cut)
	}
	wantKinds := map[string]string{
		// src/lib/still.ts: the router at the head still imports it
		"code":    "[drizzle/0059_drop_items.sql src/client.ts src/db.ts src/env.ts src/lib/still.ts] +3 -3 read 2 deleted 0 generated 0 named 0 cut false",
		"tests":   "[tests/migrate.test.ts tests/stay.test.ts go/total_test.go tests/gone.test.ts tests/helpers.ts tests/plain.test.ts tests/quick.test.ts tests/vanished.test.ts] +1 -28 read 2 deleted 6 generated 0 named 0 cut false",
		"tooling": "[.github/workflows/ci.yml docs/runbook.md] +2 -0 read 0 deleted 0 generated 0 named 0 cut false",
		// the fixture is no test; docs/old.md still names gone
		"gone": "[drizzle/meta/0059_snapshot.json go/total.go src/lib/gone.ts tests/fixture.json] +1 -5 read 0 deleted 3 generated 1 named 1 cut false",
	}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Errorf("kinds:\n%q\nwant\n%q", kinds, wantKinds)
	}

	env := map[string]string{}
	for n, e := range d.Env {
		env[n] = fmt.Sprintf("%v %v %v", e.Reads, e.Named, e.Cut)
	}
	wantEnv := map[string]string{
		"DATABASE_URL": "[] [src/env.ts:2] false", // its schema's key at the head: still read
		"OLD_URL":      "[] [] false",
		"NEW_URL":      "[src/client.ts:1 src/env.ts:3] [] false", // through the env object too
	}
	if !reflect.DeepEqual(env, wantEnv) {
		t.Errorf("env: %q", env)
	}
	var unread []string
	for _, r := range prepare(d).Risks {
		for _, it := range r.Items {
			if it.Note == "no longer read" {
				unread = append(unread, it.What)
			}
		}
	}
	if !slices.Equal(unread, []string{"OLD_URL"}) {
		t.Errorf("no longer read: %q", unread)
	}

	if m := d.Migrations; len(m) != 1 || m[0].Path != "drizzle/0059_drop_items.sql" || !slices.Equal(m[0].Docs, []string{"docs/runbook.md:1"}) ||
		!slices.Equal(m[0].Tests, []string{"tests/migrate.test.ts:1"}) || m[0].Cut {
		t.Errorf("migrations: %+v", m)
	}

	// router.ts is unchanged: its excerpt, 10 lines around :4, as far as the file goes; db.ts on the base
	if len(d.Excerpts) != 1 || !reflect.DeepEqual(d.Excerpts[0], Excerpt{Path: "src/router.ts", From: 1, Lines: strings.Split(strings.TrimSuffix(base["src/router.ts"], "\n"), "\n")}) {
		t.Errorf("excerpts: %+v", d.Excerpts)
	}
	ex := Excerpts(context.Background(), sb, d, []Span{{Path: "src/db.ts", Base: true, From: 2, To: 2}, {Path: "src/lib/gone.ts", From: 1, To: 1}})
	if len(ex) != 1 || ex[0].Path != "src/db.ts" || !ex[0].Base || ex[0].Lines[1] != "const b = process.env.OLD_URL" {
		t.Errorf("base excerpt: %+v", ex) // gone.ts has no head
	}
}

// A search that reads its cap is cut, and a file it can't sort stays out of both groups; a search finding nothing is not.
func TestGrepHead(t *testing.T) {
	sb, _ := staticRepo(t, map[string]string{"a.ts": "x\nx\nx\n"}, map[string]string{"b.ts": "y\n"})
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi = false
	ctx := context.Background()
	if h, cut := grepHead(ctx, sb, 2, "-e", "x"); len(h) != 2 || !cut {
		t.Errorf("capped: %v %v", h, cut)
	}
	if h, cut := grepHead(ctx, sb, 5, "-e", "x"); len(h) != 3 || cut || h[2] != (hit{"a.ts", 3, "x"}) {
		t.Errorf("whole: %v %v", h, cut)
	}
	if h, cut := grepHead(ctx, sb, 5, "-e", "nowhere"); len(h) != 0 || cut {
		t.Errorf("none: %v %v", h, cut)
	}
	done, cancel := context.WithCancel(ctx)
	cancel()
	if _, cut := grepHead(done, sb, 5, "-e", "x"); !cut {
		t.Error("out of time: not cut")
	}
	cutSearch := func(int, ...string) ([]hit, bool) { return []hit{{"tests/a.test.ts", 1, "f"}}, true }
	raw := "diff --git a/tests/f.test.ts b/tests/f.test.ts\ndeleted file mode 100644\n--- a/tests/f.test.ts\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-describe('f', () => {\n-it('x', () => {})\n"
	if tc := testChanges(cutSearch, raw, diff.Outline{}); len(tc.Unsorted) != 1 || len(tc.With)+len(tc.Kept) != 0 {
		t.Errorf("cut search: %+v", tc)
	}
}

func TestReadRules(t *testing.T) {
	long := strings.Repeat("r", 20)
	r := ReadRules("abc", "AGENTS text\n"+long[:5], [][2]string{{".review/rules.md", long}, {"AGENTS.md", "AGENTS text"}, {"AGENTS.md", "AGENTS text"}})
	if want := (Rules{SHA: "abc", Files: []RuleFile{{".review/rules.md", true}, {"AGENTS.md", false}}}); !reflect.DeepEqual(r, want) {
		t.Errorf("%+v", r)
	}
	if r := ReadRules("abc", "", nil); r.Files != nil {
		t.Errorf("none: %+v", r)
	}
}

// A version keeps its open items by kind and place, minor findings aside; one stored before them still reads.
func TestVersionItems(t *testing.T) {
	d := Data{
		SHA: "1111111aaa", Diff: "diff --git a/.env.example b/.env.example\n--- a/.env.example\n+++ b/.env.example\n@@ -1 +1 @@\n-OLD_A=1\n+NEW_B=1\n",
		Findings:    []review.Comment{{Path: "a.go", Line: 3, Severity: "major"}, {Path: "a.go", Line: 4, Severity: "minor"}},
		TestChanges: TestChanges{With: []TestFile{{Path: "t1.test.ts"}}, Kept: []TestFile{{Path: "t2.test.ts"}}, Stay: []TestStay{{Path: "t3.test.ts"}}},
		Omitted:     []string{"big.go"},
	}
	_, v, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{{"before", "NEW_B"}, {"before", "OLD_A"}, {"finding", "a.go:3"}, {"gap", "big.go"}, {"test-kept", "t2.test.ts"}, {"test-stay", "t3.test.ts"}, {"test-with", "t1.test.ts"}}
	if !reflect.DeepEqual(v.Items, want) {
		t.Errorf("items %+v", v.Items)
	}
	var old Version
	if err := json.Unmarshal([]byte(`{"sha":"abc","at":"2026-01-01T00:00:00Z","files":{"a.go":"1"}}`), &old); err != nil || old.Items != nil {
		t.Errorf("old version: %v %+v", err, old)
	}
}

func TestPartOf(t *testing.T) {
	m := &Map{Parts: []MapPart{{ID: "src", Path: "src", Files: []string{"src/a.ts"}}, {ID: "src/lib", Path: "src/lib"}, {ID: "top", Path: ".", Files: []string{"main.go"}}}}
	for p, want := range map[string]string{"src/a.ts": "src", "src/lib/x/b.ts": "src/lib", "src/c.ts": "src", "go.mod": "top", "web/x.ts": ""} {
		if got := m.PartOf(p); got != want {
			t.Errorf("%s: %q, want %q", p, got, want)
		}
	}
}
