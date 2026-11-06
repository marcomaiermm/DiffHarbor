package review

import (
	"diffharbor/internal/agent"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestLevel(t *testing.T) {
	if Level("x", []string{"src/a.ts"}) != "low" || Level(strings.Repeat("x", 9000), nil) != "medium" ||
		Level("x", []string{"src/auth/session.ts"}) != "high" || Level("x", []string{"infra/main.tf"}) != "high" {
		t.Fatal("level")
	}
}

func TestCoChanged(t *testing.T) {
	log := strings.Repeat("\x00\na.ts\nb.ts\nc.ts\n", 4) + "\x00\na.ts\nd.ts\n" + "\x00\n" + strings.Repeat("x.ts\n", 31)
	if got := coChanged(log, []string{"a.ts", "c.ts"}); got != "- a.ts: b.ts (4×)\n- c.ts: b.ts (4×)\n" {
		t.Fatalf("coChanged: %q", got)
	}
}

func TestParse(t *testing.T) {
	if r, ok := parse("ok:\n```json\n{\"summary\":\"s\",\"comments\":[]}\n```"); !ok || r.Summary != "s" || r.Comments == nil || len(r.Comments) != 0 {
		t.Fatalf("parse json: %+v", r)
	}
	if r, ok := parse("no json here"); ok || r.Summary != "no json here" || len(r.Comments) != 0 {
		t.Fatalf("parse text: %+v", r)
	}
	if _, ok := parse(`{"summary":"s"}`); ok {
		t.Fatal("parse without comments")
	}
}

func TestTrustedContext(t *testing.T) {
	if TrustedContext(" ", "") != "" || !strings.Contains(TrustedContext("use pnpm", "- x"), "## Learnings") {
		t.Fatal("trustedContext")
	}
}

func TestGoModules(t *testing.T) {
	d := t.TempDir()
	for _, f := range []string{"go.mod", "svc/go.mod", "svc/x/a.go", "b.go"} {
		os.MkdirAll(filepath.Join(d, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(d, f), nil, 0o644)
	}
	if got := goModules(d, []string{"svc/x/a.go", "svc/go.mod", "b.go", "svc/c.go"}); !slices.Equal(got, []string{"svc"}) {
		t.Fatalf("goModules: %q", got)
	}
}

func TestStack(t *testing.T) {
	d := t.TempDir()
	for _, f := range []string{"apps/web/package.json", "go.mod", "Addon.toc"} {
		os.MkdirAll(filepath.Join(d, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(d, f), nil, 0o644)
	}
	if got := stack(t.Context(), d, nil); got != "npm (JavaScript/TypeScript), Go modules, World of Warcraft addon (Lua, WoW API)" {
		t.Fatalf("stack: %q", got)
	}
	if got := stack(t.Context(), t.TempDir(), []string{"a.lua"}); got != "Lua" {
		t.Fatalf("stack lua: %q", got)
	}
	os.WriteFile(filepath.Join(d, "apps/web/package.json"), []byte(`{"dependencies":{"@tanstack/react-start":"1"}}`), 0o644)
	if got := stack(t.Context(), d, []string{"apps/web/src/a.ts"}); !strings.Contains(got, "SSR with @tanstack/react-start (") || strings.Contains(stack(t.Context(), d, nil), "SSR") {
		t.Fatalf("stack in a package: %q", got)
	}
	os.WriteFile(filepath.Join(d, "package.json"), []byte(`{"dependencies":{"@tanstack/react-start":"1"}}`), 0o644)
	if got := stack(t.Context(), d, nil); !strings.Contains(got, "SSR with @tanstack/react-start (") {
		t.Fatalf("stack ssr: %q", got)
	}
}

func TestVerdicts(t *testing.T) {
	cs := []Comment{{Path: "a.go", Line: 3, Body: "b", Prompt: "In a.go around line 3, fix"}, {Path: "b.go", Line: 9}}
	kept, dropped, fixed, ok := verdicts(cs, []int{2, 4}, `{"verdicts":[{"i":0,"keep":true,"path":"c.go","line":5,"evidence":"e"},{"i":0,"keep":true},{"i":1,"keep":false,"evidence":"handled in b.go:3"},{"i":7,"keep":true}],"fixed":[4,4,3]}`, nil)
	if !ok || len(kept) != 1 || kept[0].Path != "c.go" || kept[0].Line != 5 || kept[0].Prompt != "In c.go around line 5, fix" || kept[0].Body != "b\n\n_Verified:_ e" {
		t.Fatalf("%+v", kept)
	}
	if !slices.Equal(fixed, []int{4}) {
		t.Fatalf("fixed: %v", fixed)
	}
	if len(dropped) != 1 || dropped[0].Path != "b.go" || dropped[0].Evidence != "handled in b.go:3" {
		t.Fatalf("dropped: %+v", dropped)
	}
	// c.go is another part's: the finding stays where its part put it
	other := func(path string, _ int) bool { return path != "c.go" }
	if kept, _, _, _ := verdicts(cs, nil, `{"verdicts":[{"i":0,"keep":true,"path":"c.go","line":5},{"i":1,"keep":false}]}`, other); len(kept) != 1 || kept[0].Path != "a.go" || kept[0].Line != 3 {
		t.Fatalf("moved to another part: %+v", kept)
	}
	if _, _, _, ok := verdicts(cs, nil, "no json", nil); ok {
		t.Fatal("no json accepted")
	}
	if _, _, _, ok := verdicts(cs, nil, `{"verdicts":[{"i":0,"keep":true}]}`, nil); ok {
		t.Fatal("a finding without a verdict accepted")
	}
	if kept, _, _, ok := verdicts([]Comment{{Existing: "a", Fix: "b"}}, nil, `{"verdicts":[{"i":0,"keep":true,"fix":false}]}`, nil); !ok || kept[0].Fix != "" {
		t.Fatalf("fix=false kept the fix: %+v", kept)
	}
	two := []Comment{{Severity: "major"}, {Severity: "minor"}}
	if kept, _, _, _ := verdicts(two, nil, `{"verdicts":[{"i":0,"keep":true,"severity":"minor"},{"i":1,"keep":true,"severity":"critical"}]}`, nil); kept[0].Severity != "minor" || kept[1].Severity != "minor" {
		t.Fatalf("severity: %+v", kept)
	}
}

// A decision is an object with its place; a plain string, as states before it hold, is its text.
func TestDecision(t *testing.T) {
	var ds []Decision
	if err := json.Unmarshal([]byte(`["a?",{"text":"b?","at":"x.go:3"}]`), &ds); err != nil || !reflect.DeepEqual(ds, []Decision{{Text: "a?"}, {Text: "b?", At: "x.go:3"}}) {
		t.Fatalf("%v %+v", err, ds)
	}
}

func TestGrownPast(t *testing.T) {
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "a.go"), []byte("1\n2\n3\n4\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "b.go"), []byte("1\n2\n3\n4\n"), 0o644)
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,4 @@\n 1\n+2\n+3\n 4\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,4 +1,4 @@\n-x\n+1\n 2\n 3\n 4\n" +
		"diff --git a/x b/x\n+++ b/../../etc/passwd\n@@ -0,0 +1 @@\n+x\n"
	if got := grownPast(t.Context(), repo, diff, 3); !slices.Equal(got, []string{"- a.go: 2 → 4 lines"}) {
		t.Fatal(got)
	}
}

func TestParseWalkthrough(t *testing.T) {
	r, ok := parse(`{"summary":"s","walkthrough":{"a.go":"adds x"},"diagram":"sequenceDiagram\nA->>B: x","effort":3,"fixed":[1],"comments":[]}`)
	if !ok || r.Walkthrough["a.go"] != "adds x" || r.Effort != 3 || !slices.Equal(r.Fixed, []int{1}) {
		t.Fatalf("%+v", r)
	}
}

func TestSettings(t *testing.T) {
	cfg, err := LoadSettings(`{"_comment":"x","ignore":["!ui/{chip,toast}.tsx","x/{a,{b,c}d}.ts","[!.]*.lock","n/a**.go","b/\\{c\\}","drizzle/meta","**/*.snap.ts","locales/*.json","ui"],"min_severity":"major","tests":false}`)
	if err != nil || cfg.Tests == nil || *cfg.Tests {
		t.Fatalf("%+v %v", cfg, err)
	}
	for p, want := range map[string]bool{
		"drizzle/meta/0001.json": true, "drizzle/meta": true, "drizzle/metax.ts": false,
		"a/b/c.snap.ts": true, "c.snap.ts": true, "locales/de.json": true, "locales/x/de.json": false, "src/a.ts": false,
		"ui/button.tsx": true, "ui/chip.tsx": false, "ui/toast.tsx": false, "ui/chip,toast.tsx": true,
		"x/a.ts": true, "x/cd.ts": true, "x/d.ts": false, "go.lock": true, ".x.lock": false, "n/ab.go": true, "n/a/b.go": false,
		"b/{c}": true, "b/c": false,
	} {
		if cfg.Ignored(p) != want {
			t.Errorf("ignore %s: want %v", p, want)
		}
	}
	if cfg.Keep(Comment{Severity: "minor"}) || !cfg.Keep(Comment{Severity: "critical"}) || !(Settings{}).Keep(Comment{Severity: "minor"}) {
		t.Error("keep")
	}
	if _, err := LoadSettings(`{"min_severity":"high"}`); err == nil {
		t.Error("min_severity high accepted")
	}
	for _, bad := range []string{`{"ignore":["a/{b"]}`, `{"ignore":["!a}"]}`, `{"ignore":["[z-a]"]}`, `{"packages":{"[a":{}}}`, `{"large_pr":"yes"}`, `{"max_parts":9}`, `{"suggest_split":{"files":-1}}`, `{"suggest_split":{"count":["tests"]}}`, `{"suggest_split":{"skip":["a/{b"]}}`, `{"learnings":{"max":-1}}`, `{"learnings":{"max":101}}`, `{"learnings":{"max_age_days":-1}}`, `{"syntax_theme":{"dark":"x\");alert(1)//"}}`} {
		if _, err := LoadSettings(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	all, _ := LoadSettings(`{"large_pr":"all","max_parts":6}`)
	off, _ := LoadSettings(`{"large_pr":"off"}`)
	ask, _ := LoadSettings(`{"large_pr":"ask"}`)
	if (Settings{}).Parts(false) != 4 || (Settings{}).Asks() || all.Parts(false) != 6 || all.Asks() ||
		off.Parts(true) != 1 || off.Asks() || ask.Parts(false) != 1 || ask.Parts(true) != 4 || !ask.Asks() {
		t.Error("large_pr parts")
	}
	// pr is a diff of files with n added lines each.
	pr := func(n int, paths ...string) string {
		var b strings.Builder
		for _, p := range paths {
			fmt.Fprintf(&b, "diff --git a/%s b/%[1]s\n--- a/%[1]s\n+++ b/%[1]s\n@@ -0,0 +1,%d @@\n%s", p, n, strings.Repeat("+x\n", n))
		}
		return b.String()
	}
	twenty := make([]string, 20)
	for k := range twenty {
		twenty[k] = fmt.Sprintf("p%d/a.go", k)
	}
	lines, _ := LoadSettings(`{"suggest_split":{"lines":300}}`)
	either, _ := LoadSettings(`{"suggest_split":{"lines":300,"files":20}}`)
	if (Settings{}).SuggestsSplit(pr(1000, twenty...)) || lines.SuggestsSplit(pr(299, "a.go")) || !lines.SuggestsSplit(pr(300, "a.go")) ||
		either.SuggestsSplit(pr(10, twenty[:19]...)) || !either.SuggestsSplit(pr(10, twenty...)) || !either.SuggestsSplit(pr(150, "a.go", "a.go")) {
		t.Error("suggest_split")
	}
	// Only code counts by default; count picks the classes, skip's globs leave paths out and "!" takes them in.
	if lines.SuggestsSplit(pr(299, "a.go", "a_test.go", "x.json", "s.css", "README.md")) {
		t.Error("suggest_split counted tests, data, bulk or docs")
	}
	tuned, err := LoadSettings(`{"suggest_split":{"lines":300,"count":["code","test"],"skip":["gen/**","!deploy/*.yaml"]}}`)
	if err != nil || !tuned.SuggestsSplit(pr(150, "a.go", "a_test.go")) || tuned.SuggestsSplit(pr(150, "a.go", "gen/b.go")) ||
		!tuned.SuggestsSplit(pr(150, "a.go", "deploy/k8s.yaml")) || tuned.SuggestsSplit(pr(150, "a.go", "ci.yaml")) {
		t.Error("suggest_split count, skip", err)
	}
	if _, err := LoadSettings(`{"review_model":"--tools=bash"}`); err == nil {
		t.Error("flag as model accepted")
	}
	for _, bad := range []string{`{"passes":[{},{},{},{}]}`, `{"passes":[{"max_lines":10}]}`, `{"passes":[{"model":"x/y"}]}`,
		`{"passes":[{},{"model":"--tools=bash"}]}`, `{"passes":[{},{"min_lines":10,"max_lines":5}]}`} {
		if _, err := LoadSettings(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	cfg, err = LoadSettings(`{"review_model":"openai/gpt-6.1-sol","passes":[{"again":false},{"max_lines":100},{"model":"anthropic/claude-haiku-5-5:high","min_lines":50}]}`)
	finders := func(lines int) string {
		var ms []string
		for _, p := range cfg.Finders(lines) {
			ms = append(ms, p.Model)
		}
		return strings.Join(ms, ",")
	}
	if err != nil || finders(10) != "openai/gpt-6.1-sol,openai/gpt-6-luna:high" || finders(500) != "openai/gpt-6.1-sol,anthropic/claude-haiku-5-5:high" ||
		len(cfg.Finders(75)) != 3 || *cfg.Finders(0)[0].Again || len((Settings{}).Finders(0)) != 1 {
		t.Errorf("passes: %v %q %q", err, finders(10), finders(500))
	}
	cfg, err = LoadSettings(`{"review_model":"openai/gpt-6.1-sol","verify_model":"anthropic/claude-haiku-4-5:low"}`)
	if r, v := cfg.Models(); err != nil || r != "openai/gpt-6.1-sol" || v != "anthropic/claude-haiku-4-5:low" {
		t.Errorf("models from review.json: %v %q %q", err, r, v)
	}
	defer func(m string) { agent.Model = m }(agent.Model)
	agent.Model = "deepseek/x" // REVIEW_MODEL pins the review model, verify still from review.json
	if r, v := cfg.Models(); r != "deepseek/x" || v != "anthropic/claude-haiku-4-5:low" {
		t.Errorf("env over review.json: %q %q", r, v)
	}
	pinned, _ := LoadSettings(`{"passes":[{},{"model":"anthropic/claude-haiku-5-5:high"}]}`)
	if fs := pinned.Finders(0); fs[0].Model != "deepseek/x" || fs[1].Model != "deepseek/deepseek-flash:high" {
		t.Errorf("REVIEW_MODEL pins the passes too: %+v", fs)
	}
	agent.Model = ""
	t.Setenv("OPENAI_API_KEY", "k")
	if r, v := (Settings{}).Models(); r != "openai/gpt-6.1-terra:high" || v != "openai/gpt-6-luna:high" {
		t.Errorf("defaults: %q %q", r, v)
	}
	cfg, _ = LoadSettings(`{"review_model":"anthropic/claude-sonnet-5-5"}`)
	if _, v := cfg.Models(); v != "anthropic/claude-haiku-5-5:low" {
		t.Errorf("verify model %q doesn't follow the review model's provider", v)
	}
	rel, err := LoadSettings(`{"base_branches":["release/*","develop"]}`)
	if err != nil || !rel.Reviews("main", "main") || !rel.Reviews("release/1.2", "main") || !rel.Reviews("develop", "main") ||
		rel.Reviews("prod", "main") || rel.Reviews("release/1/x", "main") || (Settings{}).Reviews("prod", "main") || !(Settings{}).Reviews("prod", "") {
		t.Errorf("base_branches: %v", err)
	}
	for _, c := range []struct {
		drafts              string
		push, asked, manual bool
	}{{"", false, false, true}, {"manual", false, false, true}, {"opt_in", false, true, true}, {"off", false, false, false}} {
		cfg, err := LoadSettings(`{"drafts":"` + c.drafts + `"}`)
		if err != nil || cfg.ReviewsDraft(false, false) != c.push || cfg.ReviewsDraft(false, true) != c.asked || cfg.ReviewsDraft(true, false) != c.manual {
			t.Errorf("drafts %q: %v", c.drafts, err)
		}
	}
	if _, err := LoadSettings(`{"drafts":"auto"}`); err == nil {
		t.Error("bad drafts accepted")
	}
	if _, err := LoadSettings(`{"base_branches":["["]}`); err == nil {
		t.Error("bad base_branches glob accepted")
	}
	if _, err := LoadSettings(`{"ignore":`); err == nil {
		t.Error("broken json accepted")
	}
	if cfg, err := LoadSettings(""); err != nil || cfg.Ignored("a.go") || !cfg.RunsTests("a.go") {
		t.Error("empty")
	}
	if on, _ := LoadSettings(`{"install_scripts":true}`); !on.InstallScripts || cfg.InstallScripts {
		t.Error("install_scripts: off unless review.json turns it on")
	}
	ci, _ := LoadSettings(`{"packages":{"svc/":{"tests":true}}}`)
	if ci.CITests = "ci.yml"; ci.RunsTests("a.go") || !ci.RunsTests("svc/a.go") || !ci.LeftToCI("a.go") || ci.LeftToCI("svc/a.go") {
		t.Error("tests in CI: off unless review.json turns them on")
	}
	cfg, err = LoadSettings(`{"min_severity":"minor","packages":{"apps/*":{"min_severity":"major"},"apps/web/":{"tests":false},"services/**":{"tests":false}}}`)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"apps/api/x.ts": "major true", "apps/web/x.ts": "minor false", "services/a/b.go": "minor false", "lib/x.ts": "minor true"} {
		if m, tests := cfg.At(p); fmt.Sprint(m, " ", tests) != want {
			t.Errorf("at %s: %s %v, want %s", p, m, tests, want)
		}
	}
	if _, err := LoadSettings(`{"packages":{"x":{"min_severity":"high"}}}`); err == nil {
		t.Error("package min_severity high accepted")
	}
	if cfg, _ := LoadSettings(""); cfg.RulePaths()[0] != ".review/rules.md" || cfg.SkillPaths()[0] != ".review/skills" || cfg.CheckPaths()[0] != ".review/checks.sh" {
		t.Errorf("default paths: %v %v %v", cfg.RulePaths(), cfg.SkillPaths(), cfg.CheckPaths())
	}
	cfg, err = LoadSettings(`{"rules":"docs/md","skills":"tools/skills","checks":["a.sh","ci/b.sh"]}`)
	if err != nil || !slices.Equal(cfg.RulePaths(), []string{"docs/md", "AGENTS.md", "CLAUDE.md"}) ||
		!slices.Equal(cfg.SkillPaths(), []string{"tools/skills"}) || !slices.Equal(cfg.CheckPaths(), []string{"a.sh", "ci/b.sh"}) {
		t.Errorf("set paths: %v %v %v %v", err, cfg.RulePaths(), cfg.SkillPaths(), cfg.CheckPaths())
	}
	if cfg, _ := LoadSettings(`{"checks":[]}`); len(cfg.CheckPaths()) != 0 {
		t.Error("checks [] should turn the scripts off")
	}
	for _, p := range []string{"../x.sh", "/etc/x", "a/../../x", "x?ref=evil", ""} {
		if _, err := LoadSettings(`{"checks":["` + p + `"]}`); err == nil {
			t.Errorf("checks %q: err %v", p, err)
		}
	}
}

func TestParseCommits(t *testing.T) {
	log := "\x00aaa\tKim\tAdd x\n\ndiff --git a/q.sql b/q.sql\n--- a/q.sql\n+++ b/q.sql\n@@ -1 +1 @@\n--- old note\n+select 1;\n\x00bbb\tKim\twip\n"
	got := parseCommits(log)
	want := []Commit{{SHA: "aaa", Author: "Kim", Subject: "Add x", Files: 1, Add: 1, Del: 1, Lines: []string{"--- old note", "+select 1;"}}, {SHA: "bbb", Author: "Kim", Subject: "wip"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseCommits %+v", got)
	}
}

func TestBumps(t *testing.T) {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "apps/web/node_modules/better-auth"), 0o755)
	os.WriteFile(filepath.Join(d, "apps/web/package.json"), []byte(`{"name":"web","dependencies":{"better-auth":"^1.7.0","zod":"^4.1.0","x":"workspace:*"}}`), 0o644)
	os.WriteFile(filepath.Join(d, "apps/web/node_modules/better-auth/package.json"), []byte(`{"version":"1.7.4"}`), 0o644)
	patch := "diff --git a/apps/web/package.json b/apps/web/package.json\n--- a/apps/web/package.json\n+++ b/apps/web/package.json\n@@ -1,6 +1,6 @@\n" +
		"-  \"version\": \"1.0.0\",\n+  \"version\": \"1.1.0\",\n" +
		"-    \"better-auth\": \"^1.6.3\",\n+    \"better-auth\": \"^1.7.0\",\n" +
		"-    \"zod\": \"^4.1.0\",\n+    \"zod\": \"^4.1.0\",\n" +
		"-    \"x\": \"workspace:^1.0.0\",\n+    \"x\": \"workspace:*\",\n"
	if got := bumps(t.Context(), d, patch); !reflect.DeepEqual(got, []bump{{"apps/web", "better-auth", "1.6.3", "1.7.4"}}) {
		t.Fatalf("bumps: %+v", got)
	}
}

func TestAPIChanges(t *testing.T) {
	d := "diff --git a/dist/index.d.mts b/dist/index.d.mts\n@@ -9,2 +9,2 @@\n" +
		"-export { getIp, type Session as S, oidcProvider };\n+export { getIP, type Session as S };\n" +
		"-export declare function old(): void;\n+export declare function old(): string;\n" +
		"diff --git a/CHANGELOG.md b/CHANGELOG.md\n--- a/CHANGELOG.md\n+++ b/CHANGELOG.md\n@@ -1,0 +1,3 @@\n+## 1.7.0\n+\n+- rename getIp to getIP\n"
	removed, added, cl := apiChanges(d)
	if !slices.Equal(removed, []string{"getIp", "oidcProvider"}) || !slices.Equal(added, []string{"getIP"}) || cl != "## 1.7.0\n- rename getIp to getIP\n" {
		t.Fatalf("apiChanges: %v %v %q", removed, added, cl)
	}
}

func TestFence(t *testing.T) {
	if got := Fence("a\n`````\n``````````b"); got != "```````````\na\n`````\n``````````b\n```````````" {
		t.Fatalf("fence: %q", got)
	}
}
