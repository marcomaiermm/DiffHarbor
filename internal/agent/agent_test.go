package agent

import (
	"context"
	"diffharbor/internal/proc"
	"diffharbor/internal/vault"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewLevel(t *testing.T) {
	const m = "openai/gpt-6-luna"
	for _, lvl := range []string{"low", "medium", "high", "xhigh", "max"} {
		if got := reviewLevel(m, lvl); got != lvl {
			t.Errorf("reviewLevel(%q) = %q", lvl, got)
		}
	}
	if got := reviewLevel(m+":high", "low"); got != "high" {
		t.Fatalf("fixed level: %q", got)
	}
}

func TestFallback(t *testing.T) {
	bin := t.TempDir() // a fake pi: the subscription is at its usage limit, the fallback answers
	os.WriteFile(filepath.Join(bin, "pi"), []byte(`#!/bin/sh
echo "$*" >> "$(dirname "$0")/calls"
cat >> "$(dirname "$0")/stdin"
case "$*" in
auth*) echo token ;;
*openai/*) echo "You have hit your ChatGPT usage limit (plus). Try again in 2h." >&2; exit 1 ;;
*"--model deepseek/x:low"*) echo answer ;;
*"--model deepseek/v:low"*) echo verified ;;
*) exit 1 ;;
esac
`), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("DEEPSEEK_API_KEY", "k")
	defer func(pi bool) { FallbackModel, FallbackVerifyModel, proc.AsPi = "", "", pi; FallbackUntil.Store(0) }(proc.AsPi)
	FallbackModel, FallbackVerifyModel, proc.AsPi = "deepseek/x", "deepseek/v:low", false // as root (image build) pi would run as node, outside the temp dir
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "repo"), 0o755)
	for range 2 {
		if out, err := runPi(context.Background(), &proc.Sandbox{Dir: dir}, "t", "private rules", "openai/sol:low", false, "private task"); err != nil || out != "answer\n" {
			t.Fatal(out, err)
		}
	}
	if out, err := runPi(context.Background(), &proc.Sandbox{Dir: dir}, "verify", "private rules", "openai/luna:high", false, "private task"); err != nil || out != "verified\n" {
		t.Fatal(out, err)
	}
	calls, _ := os.ReadFile(filepath.Join(bin, "calls"))
	if n := strings.Count(string(calls), "openai/sol"); n != 1 {
		t.Fatalf("subscription tried %d times, want once, then 30 min on the fallback:\n%s", n, calls)
	}
	// the system prompt and the task in a file and stdin, not in argv: other repos' jobs read that
	stdin, _ := os.ReadFile(filepath.Join(bin, "stdin"))
	if sys, _ := os.ReadFile(filepath.Join(dir, "verify.system.md")); string(sys) != "private rules" {
		t.Fatalf("system prompt file: %q", sys)
	}
	if strings.Contains(string(calls), "private") || strings.Count(string(stdin), "private task") != 4 ||
		!strings.Contains(string(calls), "--system-prompt "+filepath.Join(dir, "verify.system.md")) {
		t.Fatalf("argv:\n%s\nstdin: %q", calls, stdin)
	}
}

func TestKeyEnv(t *testing.T) {
	for p, want := range map[string]string{"anthropic": "ANTHROPIC_API_KEY", "google": "GEMINI_API_KEY", "zai-coding-cn": "ZAI_CODING_CN_API_KEY"} {
		if got := KeyEnv(p); got != want {
			t.Errorf("KeyEnv(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestMatrix(t *testing.T) {
	defer func(d string) { proc.DataDir = d }(proc.DataDir)
	proc.DataDir = t.TempDir() // no ChatGPT login
	for _, k := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "DEEPSEEK_API_KEY", "AWS_REGION", "AZURE_OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	t.Setenv("ANTHROPIC_API_KEY", "k")
	if d := Default(); d != "anthropic/claude-sonnet-5-5:high" || VerifyFor(d) != "anthropic/claude-haiku-5-5:low" || FallbackFor(d, "review") != "" {
		t.Fatalf("anthropic only: %q %q %q", d, VerifyFor(d), FallbackFor(d, "review"))
	}
	t.Setenv("DEEPSEEK_API_KEY", "k")
	if d := Default(); FallbackFor(d, "review") != "deepseek/deepseek-flash:high" || FallbackFor(d, "verify") != "deepseek/deepseek-flash:off" {
		t.Fatalf("anthropic + deepseek: fallback %q %q", FallbackFor(d, "review"), FallbackFor(d, "verify"))
	}
	os.MkdirAll(AuthDir(), 0o700)
	os.WriteFile(filepath.Join(AuthDir(), "auth.json"), []byte(`{"openai":{"type":"oauth"}}`), 0o600)
	if d := Default(); d != "openai/gpt-6.1-terra:high" || FallbackFor(d, "review") != "anthropic/claude-sonnet-5-5:high" {
		t.Fatalf("ChatGPT login first: %q, fallback %q", d, FallbackFor(d, "review"))
	}
}

// Bedrock and Azure need more than one key; a pass gets all of it, a half-configured provider none.
func TestCloudProviders(t *testing.T) {
	for _, k := range []string{"AWS_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_BEARER_TOKEN_BEDROCK", "AZURE_OPENAI_API_KEY", "AZURE_OPENAI_BASE_URL"} {
		t.Setenv(k, "")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "id")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	if _, err := passEnv(context.Background(), "amazon-bedrock"); err == nil || !strings.Contains(err.Error(), "AWS_REGION") {
		t.Fatalf("bedrock without region: %v", err)
	}
	t.Setenv("AWS_REGION", "eu-central-1")
	if env, err := passEnv(context.Background(), "amazon-bedrock"); err != nil || strings.Join(env, " ") != "AWS_REGION=eu-central-1 AWS_ACCESS_KEY_ID=id AWS_SECRET_ACCESS_KEY=secret" {
		t.Fatalf("bedrock: %v %v", env, err)
	}
	t.Setenv("AZURE_OPENAI_API_KEY", "k")
	if Available("azure") {
		t.Fatal("azure without endpoint")
	}
	t.Setenv("AZURE_OPENAI_BASE_URL", "https://x.openai.azure.com/openai/v1")
	if env, err := passEnv(context.Background(), "azure"); err != nil || len(env) != 2 || VerifyFor("azure/gpt-6.1-sol") != "azure/gpt-6-luna:high" {
		t.Fatalf("azure: %v %v", env, err)
	}
}

func TestProxies(t *testing.T) {
	f := filepath.Join(t.TempDir(), "models.json")
	t.Setenv("ANTHROPIC_BASE_URL", "https://llm.example.com/anthropic")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("AZURE_BASE_URL", "https://ignored") // Azure's endpoint is AZURE_OPENAI_BASE_URL, passed per pass
	if err := proxies(t.Context(), &proc.Sandbox{}, f); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f); string(b) != `{"providers":{"anthropic":{"baseUrl":"https://llm.example.com/anthropic","compat":{"supportsMidConvoEffort":false,"supportsMidConvoToolChanges":false}}}}` {
		t.Fatalf("models.json: %s", b)
	}
	t.Setenv("ANTHROPIC_BASE_URL", "")
	if err := proxies(t.Context(), &proc.Sandbox{}, f); err != nil || proc.Exists(f) {
		t.Fatalf("no proxy left, models.json stays: %v", err)
	}
}

// As root the passes get vault tokens, models.json points Pi at the vault, and the keys stay in the server.
// Last: the vault stays up for the rest of the process.
func TestVaulted(t *testing.T) {
	for _, k := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_BASE_URL", "DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL"} {
		t.Setenv(k, "")
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-real")
	t.Setenv("AWS_REGION", "eu-central-1")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "bedrock-real")
	t.Setenv("AWS_ACCESS_KEY_ID", "id")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-real")
	t.Setenv("AZURE_OPENAI_BASE_URL", "https://x.openai.azure.com/")
	if err := vault.Start(); err != nil {
		t.Fatal(err)
	}
	vaultURL = vault.URL
	t.Cleanup(func() { vaultURL = func() string { return "" } })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for p, want := range map[string]string{
		"anthropic":      "ANTHROPIC_API_KEY=tok",
		"amazon-bedrock": "AWS_REGION=eu-central-1 AWS_BEARER_TOKEN_BEDROCK=tok AWS_BEDROCK_FORCE_HTTP1=1",
		"azure":          "AZURE_OPENAI_API_KEY=tok",
	} {
		env, err := passEnv(ctx, p)
		for i, e := range env {
			if k, v, _ := strings.Cut(e, "="); k == KeyEnv(p) && len(v) == 26 { // rand.Text
				env[i] = k + "=tok"
			}
		}
		if got := strings.Join(env, " "); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", p, got, err, want)
		}
	}
	if u := upstream("azure"); u != "https://x.openai.azure.com/openai/v1" {
		t.Errorf("azure upstream %q", u)
	}
	if u := upstream("openai"); u != "" {
		t.Errorf("openai without a key (the ChatGPT login) goes past the vault: %q", u)
	}
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	if env, _ := passEnv(ctx, "amazon-bedrock"); strings.Join(env, " ") != "AWS_REGION=eu-central-1 AWS_ACCESS_KEY_ID=id AWS_SECRET_ACCESS_KEY=secret" {
		t.Errorf("bedrock's IAM keys sign for the host, Pi gets them: %q", env)
	}
	f := filepath.Join(t.TempDir(), "models.json")
	t.Setenv("ANTHROPIC_BASE_URL", "https://llm.example.com/anthropic") // the vault's upstream now
	if err := proxies(t.Context(), &proc.Sandbox{}, f); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"providers":{"anthropic":{"baseUrl":"%[1]s/anthropic","compat":{"supportsMidConvoEffort":false,"supportsMidConvoToolChanges":false}},"azure":{"baseUrl":"%[1]s/azure"}}}`, vault.URL())
	if b, _ := os.ReadFile(f); string(b) != want || upstream("anthropic") != "https://llm.example.com/anthropic" {
		t.Fatalf("models.json: %s, want %s", b, want)
	}
}
