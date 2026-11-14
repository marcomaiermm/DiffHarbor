package job

import (
	"context"
	"diffharbor/internal/proc"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// turn is an assistant message of a Pi session as Pi 1.5 writes it.
func turn(provider, model string, in, read, write, out int, cost float64) string {
	return fmt.Sprintf(`{"type":"message","message":{"role":"assistant","provider":%q,"model":%q,"durationMs":1500,`+
		`"usage":{"input":%d,"output":%d,"cacheRead":%d,"cacheWrite":%d,"reasoning":5,"cost":{"total":%g}}}}`, provider, model, in, out, read, write, cost)
}

func writeSession(t *testing.T, dir, name string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".session"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLogUsage(t *testing.T) {
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi, proc.DataDir = false, t.TempDir() // as root sb.ReadFile would read as node, outside the temp dir
	dir := t.TempDir()
	sb, empty := &proc.Sandbox{Dir: dir}, &proc.Sandbox{Dir: t.TempDir()}
	// PR code runs as the sessions' user: a session that never ends is skipped, not read
	if err := os.Symlink("/dev/zero", filepath.Join(dir, "evil.session")); err != nil {
		t.Fatal(err)
	}
	user := `{"type":"message","message":{"role":"user","content":"the diff"}}`
	writeSession(t, dir, "review-1", `{"type":"session","version":3}`, user,
		turn("openai", "gpt-6.1-terra", 1000, 0, 0, 100, 0.01), turn("openai", "gpt-6.1-terra", 200, 1000, 0, 50, 0.002))
	writeSession(t, dir, "verify-1.failed", turn("openai", "gpt-6-luna", 500, 0, 0, 10, 0.001))
	writeSession(t, dir, "verify-1", turn("anthropic", "claude-haiku-5-5", 400, 0, 300, 20, 0.003))

	start := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	logUsage(context.Background(), sb, Spend{Time: start, Job: "review", Key: "o/r#1", Result: "ok", Findings: 2}, nil)
	logUsage(context.Background(), sb, Spend{Time: start, Job: "chat", Key: "o/r#1", Result: "ok"}, errors.New("reply failed"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logUsage(ctx, sb, Spend{Time: start, Job: "review", Key: "o/r#1", Result: "failed"}, context.Canceled)
	logUsage(context.Background(), empty, Spend{Time: start, Job: "review", Key: "o/r#2"}, nil) // no model ran: no line

	b, err := os.ReadFile(filepath.Join(proc.DataDir, "usage", "2026-11.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d:\n%s", len(lines), b)
	}
	var s Spend
	if err := json.Unmarshal([]byte(lines[0]), &s); err != nil {
		t.Fatal(err)
	}
	if s.Result != "ok" || s.Findings != 2 || s.Cost != 0.016 || len(s.Passes) != 3 {
		t.Errorf("line: %+v", s)
	}
	byName := map[string]int{}
	for i, p := range s.Passes {
		byName[p.Name] = i
	}
	r := s.Passes[byName["review-1"]]
	if r.Turns != 2 || r.Input != 1200 || r.CacheRead != 1000 || r.Output != 150 || r.Reasoning != 10 || r.Ms != 3000 || r.Model != "openai/gpt-6.1-terra" {
		t.Errorf("review pass: %+v", r)
	}
	if v := s.Passes[byName["verify-1"]]; v.CacheWrite != 300 || v.Model != "anthropic/claude-haiku-5-5" {
		t.Errorf("verify pass: %+v", v)
	}
	if _, ok := byName["verify-1.failed"]; !ok {
		t.Error("the pass the fallback took over from is missing")
	}
	for i, want := range []string{"ok", "failed", "stopped"} {
		var got Spend
		_ = json.Unmarshal([]byte(lines[i]), &got)
		if got.Result != want {
			t.Errorf("line %d: result %q, want %q", i, got.Result, want)
		}
	}
	if strings.Contains(string(b), "the diff") {
		t.Error("prompt text in the usage log")
	}
}

func TestStage(t *testing.T) {
	s := Spend{Time: time.Now().Add(-3 * time.Second)}
	s.stage("checkout")
	s.mark = s.mark.Add(-2 * time.Second) // two more seconds pass
	s.stage("analyze")
	if s.Stages["checkout"] != 3 || s.Stages["analyze"] != 2 {
		t.Errorf("stages: %v", s.Stages)
	}
}
