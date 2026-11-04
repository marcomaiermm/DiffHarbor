package checks

import (
	"context"
	"diffharbor/internal/proc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestChecks(t *testing.T) {
	bin, repo := t.TempDir(), t.TempDir() // fake tools print how they were called
	for _, tool := range []string{"gitleaks", "shellcheck", "actionlint", "zizmor", "osv-scanner"} {
		os.WriteFile(filepath.Join(bin, tool), []byte("#!/bin/sh\n[ \"$1\" = --version ] && exec cat .ver\necho \"$(basename \"$0\") $*\"\n"), 0o755)
	}
	// squawk prints its flags and how many lines it read in its JSON
	os.WriteFile(filepath.Join(bin, "squawk"), []byte(`#!/bin/sh
printf '[{"file":"m.sql","line":0,"rule_name":"%s","message":"%s lines"}]' "$*" "$(grep -c .)"
`), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi = false
	os.WriteFile(filepath.Join(repo, "m.sql"), []byte("-- +goose Up\nALTER TABLE t ADD c int;\n-- +goose Down\nALTER TABLE t DROP c;\n"), 0o644)
	for _, f := range []string{".github/workflows/ci.yml", "a.sh", "web/package.json", "repo.sh"} {
		os.MkdirAll(filepath.Join(repo, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(repo, f), []byte("echo repo \"$@\"\n"), 0o644)
	}
	out := Run(context.Background(), repo, []Script{{".review/a.sh", "repo.sh"}, {"b.sh", "repo.sh"}}, "", []string{".github/workflows/ci.yml", "a.sh", "web/package.json", "gone.go", "m.sql"}, func(string) bool { return true }, false)
	for _, want := range []string{
		"gitleaks dir --no-banner --redact --log-level warn -v a.sh", "shellcheck -f gcc a.sh",
		"actionlint -no-color .github/workflows/ci.yml", "zizmor -q --offline --format github .github/workflows/ci.yml", "osv-scanner scan source --format table web",
		"### repo checks (.review/a.sh)\nrepo -- .github/workflows/ci.yml a.sh web/package.json gone.go m.sql",
		"### repo checks (b.sh)\nrepo --",
		"m.sql:1: --reporter json --stdin-filepath m.sql --exclude " + squawkExclude + " --assume-in-transaction: 2 lines",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "go vet") {
		t.Errorf("Go checks for a deleted file:\n%s", out)
	}
	fl := "unused-export:a.ts:3:x\nunused-export:old.ts:3:y\nunused-file:a.ts\nunused-file:old.ts\nunused-dep:left-pad\n" +
		"code-duplication:a.ts:1-4:fingerprint=dup:ab,group=1\ncode-duplication:old.ts:9-12:fingerprint=dup:ab,group=1\n" +
		"code-duplication:old.ts:20-30:fingerprint=dup:cd,group=2\ncircular-dependency:old.ts:1:old.ts → b.ts → old.ts\n[X] Exit code 1"
	if got := fallowNew(fl, map[string]bool{"a.ts:1": true, "a.ts:3": true}, []string{"a.ts", "b.ts"}); got !=
		"unused-export:a.ts:3:x\nunused-file:a.ts\ncode-duplication:a.ts:1-4:fingerprint=dup:ab,group=1\n"+
			"code-duplication:old.ts:9-12:fingerprint=dup:ab,group=1\ncircular-dependency:old.ts:1:old.ts → b.ts → old.ts" {
		t.Errorf("fallowNew: %q", got)
	}
	if got := lines("a\nb\nc", 2) + "|" + lines("a\nb\nc", -2); got != "a\nb\n…|…\nb\nc" {
		t.Errorf("lines: %q", got)
	}
	js := `{"Action":"output","Package":"p","Test":"TestA","Output":"=== RUN   TestA\n"}
{"Action":"output","Package":"p","Test":"TestA","Output":"request log\n"}
{"Action":"output","Package":"p","Test":"TestA","Output":"--- PASS: TestA (0.00s)\n"}
{"Action":"pass","Package":"p","Test":"TestA"}
{"Action":"output","Package":"p","Test":"TestB","Output":"    b_test.go:9: want 2\n"}
{"Action":"fail","Package":"p","Test":"TestB"}
{"Action":"output","Package":"p","Test":"TestC","Output":"    c_test.go:4: want 2\n"}
{"Action":"output","Package":"p","Test":"TestC","Output":"--- FAIL: TestC (0.00s)\n"}
{"Action":"fail","Package":"p","Test":"TestC"}
{"Action":"build-output","ImportPath":"q [q.test]","Output":"q.go:3: undefined: x\n"}
{"Action":"build-fail","ImportPath":"q [q.test]"}
go: downloading example.com/m v1.0.0`
	if got := testFailures(js); got != "go: downloading example.com/m v1.0.0\n    b_test.go:9: want 2\n(1 more tests failed the same way)\nq.go:3: undefined: x" {
		t.Errorf("testFailures: %q", got)
	}
}

func TestUnlisted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git") // the golang:alpine build stage
	}
	repo := t.TempDir()
	for f, s := range map[string]string{
		"ci.yml":       "steps:\n  for k in AWS_KEY_ID AWS_SECRET CF_TOKEN NEW_KEY; do\n",
		"catalog.json": "{\n  \"required\": [\"AWS_KEY_ID\", \"AWS_SECRET\", \"CF_TOKEN\"]\n}\n",
		"docs.md":      "Set AWS_KEY_ID,\nAWS_SECRET and CF_TOKEN.\n", // at its line naming the most
		"one.md":       "AWS_KEY_ID only\n",
		"new.md":       "AWS_KEY_ID, AWS_SECRET, NEW_KEY\n", // changed, has the new name
	} {
		os.WriteFile(filepath.Join(repo, f), []byte(s), 0o644)
	}
	output := func(name string, args ...string) string {
		b, _ := proc.Command(context.Background(), repo, nil, name, args...).CombinedOutput()
		return string(b)
	}
	output("git", "init", "-q")
	output("git", "add", ".")
	got := unlisted(t.Context(), repo, map[string]bool{"ci.yml:2": true, "new.md:1": true}, []string{"ci.yml", "new.md"}, output)
	if want := "catalog.json:2 names AWS_KEY_ID, AWS_SECRET, CF_TOKEN but not NEW_KEY (added beside them at ci.yml:2)\n" +
		"docs.md:2 names AWS_KEY_ID, AWS_SECRET, CF_TOKEN but not NEW_KEY (added beside them at ci.yml:2)"; got != want {
		t.Errorf("unlisted:\n%s", got)
	}
}

func TestMonorepoChecks(t *testing.T) {
	bin, repo := t.TempDir(), t.TempDir() // fake tools print where and how they were called
	for _, tool := range []string{"gofmt", "go", "staticcheck", "oxlint", "fallow", "pnpm"} {
		os.WriteFile(filepath.Join(bin, tool), []byte("#!/bin/sh\n[ \"$1\" = --version ] && exec cat .ver\necho \"$(basename \"$0\") @$(basename \"$PWD\") $*\"\n"), 0o755)
	}
	// Findings on an added and an old line; the repo's v1 config doesn't load, so the bot's runs.
	os.WriteFile(filepath.Join(bin, "golangci-lint"), []byte("#!/bin/sh\ncase \"$*\" in *.golangci.yml*) echo \"can't load config\";;\n"+
		"*) echo \"$PWD/b.go:1:1: golangci $1\"; echo \"$PWD/b.go:2:1: old\";; esac\n"), 0o755)
	os.WriteFile(filepath.Join(bin, "deadcode"), []byte("#!/bin/sh\n[ \"$1\" = --version ] && exec cat .ver\necho 'b.go:1:6: unreachable func: x'; echo 'cmd/a.go:1:6: unreachable func: y'\n"), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi = false
	for f, s := range map[string]string{
		"svc/go.mod": "module svc", "svc/.golangci.yml": "linters: {}", "svc/cmd/a.go": "package main", "svc/b.go": "package svc",
		"package.json": "{}", "pnpm-lock.yaml": "", "apps/web/package.json": "{}", "apps/web/tsconfig.json": "{}", "apps/web/src/a.ts": "",
		"node_modules/.bin/vitest": "#!/bin/sh\necho \"vitest @$(basename \"$PWD\") $*\"\n",
	} {
		os.MkdirAll(filepath.Join(repo, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(repo, f), []byte(s), 0o755)
	}
	patch := filepath.Join(t.TempDir(), "pr.diff")
	os.WriteFile(patch, []byte("diff --git a/svc/b.go b/svc/b.go\n--- a/svc/b.go\n+++ b/svc/b.go\n@@ -1 +1 @@\n-x\n+package svc\n"), 0o644)
	out := Run(context.Background(), repo, nil, patch, []string{"svc/cmd/a.go", "svc/b.go", "apps/web/src/a.ts"}, func(f string) bool { return f != "svc/b.go" }, false)
	for _, want := range []string{
		"pnpm @" + filepath.Base(repo) + " install", "gofmt @svc -l cmd/a.go b.go", "go @svc vet ./. ./cmd",
		"go @svc test -json -short -count=1 -timeout=4m ./cmd", "vitest @web related --run --reporter=dot src/a.ts",
		"### golangci-lint (added lines) in svc/ (paths relative to it)\nb.go:1:1: golangci run", "b.go:1:6: unreachable func: x",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "### No findings\ngitleaks, oxlint --type-aware in apps/web/, fallow in apps/web/, fallow security in apps/web/") {
		t.Errorf("clean checks not in one line:\n%s", out)
	}
	if strings.Contains(out, ": old") || strings.Contains(out, "func: y") {
		t.Errorf("kept a finding on an old line:\n%s", out)
	}
	if strings.Contains(out, "timeout=4m ./.") { // svc/b.go has tests off
		t.Errorf("tested a package with tests off:\n%s", out)
	}
}

func TestFallowSecurity(t *testing.T) {
	out := "warning: x\n" + `{"security_findings":[{"path":"a.ts","line":3,"category":"ssrf","cwe":918,"severity":"medium","evidence":"Non-literal URL passed to fetch(). Candidate for verification: x."},` +
		`{"path":"a.ts","line":9,"category":"ssrf","cwe":918}]}` + "\nmore"
	if got := fallowSecurity(out, map[string]bool{"a.ts:3": true}); got != "a.ts:3: medium ssrf (CWE-918): Non-literal URL passed to fetch()\n" {
		t.Errorf("%q", got)
	}
	if fallowSecurity("no json", nil) != "" {
		t.Error("no json")
	}
}

func TestCITests(t *testing.T) {
	repo := t.TempDir()
	wf := filepath.Join(repo, ".github", "workflows")
	os.MkdirAll(wf, 0o755)
	write := func(name, y string) { os.WriteFile(filepath.Join(wf, name), []byte(y), 0o644) }
	write("lint.yml", "on: [push, pull_request]\njobs:\n  lint:\n    steps:\n      - run: pnpm lint # pnpm test once it is fast\n#      - run: go test ./...\n")
	write("nightly.yml", "on:\n  schedule:\n    - cron: '0 3 * * *'\n  workflow_dispatch:\n    inputs:\n      push-image: {}\njobs:\n  t:\n    steps:\n      - run: go test ./...\n")
	write("tests.yml", "on: workflow_call\njobs:\n  check:\n    steps:\n      - run: |\n          pnpm test:unit --run\n")
	if got := CITests(t.Context(), repo); got != "" {
		t.Fatalf("lint, a commented test, nightly tests and a workflow no pull request calls: %q", got)
	}
	write("pr.yml", "\"on\":\n  push:\n    branches: [main]\n\n  pull_request:\njobs:\n  tests:\n    uses: ./.github/workflows/tests.yml\n")
	if got := CITests(t.Context(), repo); got != "pr.yml" {
		t.Fatalf("pull requests run tests.yml: %q", got)
	}
	for _, run := range []string{"pnpm run test", "pnpm -r test", "pnpm --filter web run test:unit", "npm test -- --run", "turbo run test", "nx affected -t test"} {
		if !ciTest.MatchString("run: " + run) {
			t.Error(run)
		}
	}
	for _, run := range []string{"pnpm run typecheck", "pnpm --filter test-utils build", "pnpm lint"} {
		if ciTest.MatchString("run: " + run) {
			t.Error("no tests:", run)
		}
	}
}

func TestInstallScripts(t *testing.T) {
	bin := t.TempDir() // fake installers print how they were called
	for _, tool := range []string{"npm", "pnpm", "yarn"} {
		os.WriteFile(filepath.Join(bin, tool), []byte("#!/bin/sh\n[ \"$1\" = --version ] && exec cat .ver\necho \"$(basename \"$0\") $*\"\n"), 0o755)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi = false
	for _, c := range []struct{ lock, version, off, on string }{
		{"package-lock.json", "{}", "npm ci --no-audit --no-fund --loglevel=error --ignore-scripts", "npm ci --no-audit --no-fund --loglevel=error"},
		{"pnpm-lock.yaml", "", "pnpm install --frozen-lockfile --prefer-offline --reporter=silent --ignore-scripts", "pnpm install --frozen-lockfile --prefer-offline --reporter=silent"},
		{"yarn.lock", "1.22.22\n", "yarn install --immutable --silent --ignore-scripts", "yarn install --immutable --silent"},
		{"yarn.lock", "", "yarn install --immutable --silent --ignore-scripts", "yarn install --immutable --silent"}, // unknown: the flag
		{"yarn.lock", "! Corepack is about to download yarn\n4.9.2\n", "yarn install --immutable --silent --mode=skip-build", "yarn install --immutable --silent"},
	} {
		repo := t.TempDir()
		os.WriteFile(filepath.Join(repo, "package.json"), []byte("{}"), 0o644)
		os.WriteFile(filepath.Join(repo, "a.ts"), nil, 0o644)
		os.WriteFile(filepath.Join(repo, c.lock), nil, 0o644)
		os.WriteFile(filepath.Join(repo, ".ver"), []byte(c.version), 0o644)
		for scripts, want := range map[bool]string{false: c.off, true: c.on} {
			out := Run(context.Background(), repo, nil, "", []string{"a.ts"}, func(string) bool { return false }, scripts)
			if !strings.Contains(out, "\n"+want+"\n") {
				t.Errorf("%s (%q) with scripts=%v, want %q in:\n%s", c.lock, c.version, scripts, want, out)
			}
		}
	}
}
