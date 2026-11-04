package review

import (
	"diffharbor/internal/agent"
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
	if got := stack(d, nil); got != "npm (JavaScript/TypeScript), Go modules, World of Warcraft addon (Lua, WoW API)" {
		t.Fatalf("stack: %q", got)
	}
	if got := stack(t.TempDir(), []string{"a.lua"}); got != "Lua" {
		t.Fatalf("stack lua: %q", got)
	}
	os.WriteFile(filepath.Join(d, "apps/web/package.json"), []byte(`{"dependencies":{"@tanstack/react-start":"1"}}`), 0o644)
	if got := stack(d, []string{"apps/web/src/a.ts"}); !strings.Contains(got, "SSR with @tanstack/react-start (") || strings.Contains(stack(d, nil), "SSR") {
		t.Fatalf("stack in a package: %q", got)
	}
	os.WriteFile(filepath.Join(d, "package.json"), []byte(`{"dependencies":{"@tanstack/react-start":"1"}}`), 0o644)
	if got := stack(d, nil); !strings.Contains(got, "SSR with @tanstack/react-start (") {
		t.Fatalf("stack ssr: %q", got)
	}
}

func TestVerdicts(t *testing.T) {
	cs := []Comment{{Path: "a.go", Line: 3, Body: "b", Prompt: "In a.go around line 3, fix"}, {Path: "b.go", Line: 9}}
	kept, fixed, ok := verdicts(cs, []int{2, 4}, `{"verdicts":[{"i":0,"keep":true,"path":"c.go","line":5,"evidence":"e"},{"i":0,"keep":true},{"i":1,"keep":false},{"i":7,"keep":true}],"fixed":[4,4,3]}`)
	if !ok || len(kept) != 1 || kept[0].Path != "c.go" || kept[0].Line != 5 || kept[0].Prompt != "In c.go around line 5, fix" || kept[0].Body != "b\n\n_Verified:_ e" {
		t.Fatalf("%+v", kept)
	}
	if !slices.Equal(fixed, []int{4}) {
		t.Fatalf("fixed: %v", fixed)
	}
	if _, _, ok := verdicts(cs, nil, "no json"); ok {
		t.Fatal("no json accepted")
	}
	if _, _, ok := verdicts(cs, nil, `{"verdicts":[{"i":0,"keep":true}]}`); ok {
		t.Fatal("a finding without a verdict accepted")
	}
	if kept, _, ok := verdicts([]Comment{{Existing: "a", Fix: "b"}}, nil, `{"verdicts":[{"i":0,"keep":true,"fix":false}]}`); !ok || kept[0].Fix != "" {
		t.Fatalf("fix=false kept the fix: %+v", kept)
	}
}

func TestGrownPast(t *testing.T) {
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "a.go"), []byte("1\n2\n3\n4\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "b.go"), []byte("1\n2\n3\n4\n"), 0o644)
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,4 @@\n 1\n+2\n+3\n 4\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,4 +1,4 @@\n-x\n+1\n 2\n 3\n 4\n" +
		"diff --git a/x b/x\n+++ b/../../etc/passwd\n@@ -0,0 +1 @@\n+x\n"
	if got := grownPast(repo, diff, 3); !slices.Equal(got, []string{"- a.go: 2 → 4 lines"}) {
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
	cfg, err := LoadSettings(`{"_comment":"x","ignore":["drizzle/meta","**/*.snap.ts","locales/*.json"],"min_severity":"major","tests":false}`)
	if err != nil || cfg.Tests == nil || *cfg.Tests {
		t.Fatalf("%+v %v", cfg, err)
	}
	for p, want := range map[string]bool{
		"drizzle/meta/0001.json": true, "drizzle/meta": true, "drizzle/metax.ts": false,
		"a/b/c.snap.ts": true, "c.snap.ts": true, "locales/de.json": true, "locales/x/de.json": false, "src/a.ts": false,
	} {
		if cfg.IgnoreRe.MatchString(p) != want {
			t.Errorf("ignore %s: want %v", p, want)
		}
	}
	if cfg.Keep(Comment{Severity: "minor"}) || !cfg.Keep(Comment{Severity: "critical"}) || !(Settings{}).Keep(Comment{Severity: "minor"}) {
		t.Error("keep")
	}
	if _, err := LoadSettings(`{"min_severity":"high"}`); err == nil {
		t.Error("min_severity high accepted")
	}
	for _, bad := range []string{`{"large_pr":"yes"}`, `{"max_parts":9}`, `{"syntax_theme":{"dark":"x\");alert(1)//"}}`} {
		if _, err := LoadSettings(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	all, _ := LoadSettings(`{"large_pr":"all","max_parts":6}`)
	off, _ := LoadSettings(`{"large_pr":"off"}`)
	if (Settings{}).Parts(false) != 1 || (Settings{}).Parts(true) != 4 || !(Settings{}).Asks() || all.Parts(false) != 6 || all.Asks() ||
		off.Parts(true) != 1 || off.Asks() {
		t.Error("large_pr parts")
	}
	if _, err := LoadSettings(`{"review_model":"--tools=bash"}`); err == nil {
		t.Error("flag as model accepted")
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
	agent.Model = ""
	t.Setenv("OPENAI_API_KEY", "k")
	if r, v := (Settings{}).Models(); r != "openai/gpt-6.1-terra:high" || v != "openai/gpt-6-luna:low" {
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
	if _, err := LoadSettings(`{"base_branches":["["]}`); err == nil {
		t.Error("bad base_branches glob accepted")
	}
	if _, err := LoadSettings(`{"ignore":`); err == nil {
		t.Error("broken json accepted")
	}
	if cfg, err := LoadSettings(""); err != nil || cfg.IgnoreRe != nil || !cfg.RunsTests("a.go") {
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
	log := "\x00aaa\tAdd x\n\ndiff --git a/q.sql b/q.sql\n--- a/q.sql\n+++ b/q.sql\n@@ -1 +1 @@\n--- old note\n+select 1;\n\x00bbb\twip\n"
	got := parseCommits(log)
	want := []Commit{{SHA: "aaa", Subject: "Add x", Files: 1, Add: 1, Del: 1, Lines: []string{"--- old note", "+select 1;"}}, {SHA: "bbb", Subject: "wip"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseCommits %+v", got)
	}
}
