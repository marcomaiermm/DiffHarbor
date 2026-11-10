// Package agent runs the review agent: Pi with read, grep, find, ls and bash in the checkout, on the configured
// models, with a fallback provider for passes the primary one can't serve.
package agent

import (
	"bytes"
	"cmp"
	"context"
	"diffharbor/internal/proc"
	"diffharbor/internal/vault"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxTools is the tool calls a pass gets (review-guard.js): every call re-reads the context, so calls are most of the cost.
const maxTools = 15

// Matrix is the default review, verify and further-pass model per provider, in order of preference, and what its passes
// authenticate with. Without REVIEW_MODEL or review.json the first provider with credentials reviews and the next
// one is the fallback. A review model's provider here also sets its verify model unless VERIFY_MODEL or review.json
// name one. A provider not listed authenticates with KeyEnv alone.
var Matrix = []Provider{
	{Name: "openai", Review: "openai/gpt-6.1-terra:high", Verify: "openai/gpt-6-luna:high", Second: "openai/gpt-6-luna:high", Login: true, Proxy: true}, // verify at high: low dropped 7% of valid findings, high 1%
	{Name: "anthropic", Review: "anthropic/claude-sonnet-5-5:high", Verify: "anthropic/claude-haiku-5-5:low", Second: "anthropic/claude-haiku-5-5:high", Proxy: true},
	{Name: "deepseek", Review: "deepseek/deepseek-flash:high", Verify: "deepseek/deepseek-flash:off", Second: "deepseek/deepseek-flash:high", Proxy: true}, // off: low's recall, cheaper
	// Claude and GPT hosted by a cloud. global.: any region serves; eu./us. (REVIEW_MODEL) keep data in one.
	// Bedrock signs with AWS credentials or a Bedrock API key.
	{
		Name: "amazon-bedrock", Review: "amazon-bedrock/global.anthropic.claude-sonnet-5-5:high", Verify: "amazon-bedrock/global.anthropic.claude-haiku-5-5:low",
		Second: "amazon-bedrock/global.anthropic.claude-haiku-5-5:high",
		Need:   [][]string{{"AWS_REGION", "AWS_BEARER_TOKEN_BEDROCK"}, {"AWS_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}},
	},
	// Azure has no gpt-6.1-terra. It needs its endpoint and maps model IDs to the deployment names
	// (AZURE_OPENAI_DEPLOYMENT_NAME_MAP="gpt-6.1-sol=review,gpt-6-luna=verify"; unset = deployments named like the models).
	{
		Name: "azure", Review: "azure/gpt-6.1-sol:high", Verify: "azure/gpt-6-luna:high", Second: "azure/gpt-6-luna:high",
		Need: [][]string{{"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_BASE_URL"}}, Opt: []string{"AZURE_OPENAI_DEPLOYMENT_NAME_MAP"},
	},
}

// Provider is a Matrix row.
type Provider struct {
	Name, Review, Verify string
	Second               string     // review.json's further passes that name no model: another model finds other defects
	Need                 [][]string // the env a pass needs: all of one set; nil = KeyEnv
	Opt                  []string   // the env a pass also gets when set
	Login                bool       // the ChatGPT login (`diffharbor login`) stands in for the key
	Proxy                bool       // <NAME>_BASE_URL routes it through a proxy, see proxies
}

// providerFor is name's Matrix row with Need filled in, for a provider not listed KeyEnv alone.
func providerFor(name string) Provider {
	p := Provider{Name: name}
	if i := slices.IndexFunc(Matrix, func(r Provider) bool { return r.Name == name }); i >= 0 {
		p = Matrix[i]
	}
	if p.Need == nil {
		p.Need = [][]string{{KeyEnv(name)}}
	}
	return p
}

// keyed reports whether one of p's Need sets is all set.
func (p Provider) keyed() bool {
	return slices.ContainsFunc(p.Need, func(set []string) bool {
		return !slices.ContainsFunc(set, func(k string) bool { return os.Getenv(k) == "" })
	})
}

var (
	// NoFallback stops passes from moving to a fallback: a dry run uses the configured model only.
	NoFallback bool
	// Model is REVIEW_MODEL: set, it wins over review.json's review_model. Without ":level" the level is routed per PR.
	Model string
	// VerifyModel is VERIFY_MODEL (or review.json's verify_model) verifies the findings: a small model does,
	// reviews stay on REVIEW_MODEL.
	VerifyModel string
	// FallbackModel is FALLBACK_MODEL (provider/id[:level], key in KeyEnv; unset = the next provider in Matrix) takes over passes
	// the primary can't serve: usage limit, outage, lost ChatGPT login. Pi already retried transient errors by then.
	// deepseek-flash has no medium: Pi rounds it up to high.
	FallbackModel string
	// FallbackVerifyModel is FALLBACK_VERIFY_MODEL, the fallback for the verify pass.
	FallbackVerifyModel string
	secretVar           = regexp.MustCompile(`KEY|SECRET|TOKEN`)
	unavailable         = regexp.MustCompile(`(?i)usage limit|quota|rate.?limit|too many requests|\b(429|5\d\d)\b|overloaded|unavailable|capacity|ChatGPT login`)
	// FallbackUntil is unix ms: until then passes go straight to the fallback.
	FallbackUntil atomic.Int64
)

// keyEnvs are Pi's key env vars that don't follow <PROVIDER>_API_KEY (pi-ai env-api-keys.js).
var keyEnvs = map[string]string{
	"google": "GEMINI_API_KEY", "google-vertex": "GOOGLE_CLOUD_API_KEY", "azure": "AZURE_OPENAI_API_KEY",
	"vercel-ai-gateway": "AI_GATEWAY_API_KEY", "opencode-go": "OPENCODE_API_KEY", "moonshotai": "MOONSHOT_API_KEY",
	"moonshotai-cn": "MOONSHOT_API_KEY", "cloudflare-workers-ai": "CLOUDFLARE_API_KEY", "cloudflare-ai-gateway": "CLOUDFLARE_API_KEY",
	"amazon-bedrock": "AWS_BEARER_TOKEN_BEDROCK",
}

// vaulted are the providers whose key can stay in the vault: Pi's upstream for them (pi-ai providers/<name>.js;
// Azure's is AZURE_OPENAI_BASE_URL, Bedrock's AWS_REGION's) and what a pass posts under it (pi-ai api/*.js).
var vaulted = map[string]struct {
	base  string
	paths []string
}{
	"openai":         {"https://api.openai.com/v1", []string{"responses"}},
	"anthropic":      {"https://api.anthropic.com", []string{"v1/messages"}},
	"deepseek":       {"https://api.deepseek.com", []string{"chat/completions"}},
	"azure":          {"", []string{"responses", "chat/completions", "deployments/*/chat/completions"}},
	"amazon-bedrock": {"", []string{"model/*/converse-stream"}},
}

// vaultURL is vault.URL; tests that start the vault turn it off for the others, as Start can't be undone.
var vaultURL = vault.URL

// upstream is the API provider's passes reach through the vault; "" = they get the key itself: no vault (not root),
// a provider the vault doesn't know, or one that authenticates another way. The ChatGPT login: Pi treats only
// requests to api.openai.com itself as ChatGPT's (pi-ai api/openai-responses.js isChatGPTSignIn). Bedrock's IAM keys:
// SigV4 signs the host.
func upstream(provider string) string {
	v, ok := vaulted[provider]
	if !ok || vaultURL() == "" || os.Getenv(KeyEnv(provider)) == "" || !providerFor(provider).keyed() {
		return ""
	}
	switch provider {
	case "azure":
		return azureBase(os.Getenv("AZURE_OPENAI_BASE_URL"))
	case "amazon-bedrock":
		return "https://bedrock-runtime." + os.Getenv("AWS_REGION") + ".amazonaws.com"
	}
	return cmp.Or(os.Getenv(envName(provider)+"_BASE_URL"), v.base) // a gateway's
}

// azureBase is Pi's Azure endpoint for AZURE_OPENAI_BASE_URL u: an Azure host's bare URL gets /openai/v1
// (pi-ai api/azure-openai-config.js normalizeAzureBaseUrl).
func azureBase(u string) string {
	p, err := url.Parse(strings.TrimRight(strings.TrimSpace(u), "/"))
	if err != nil {
		return u // the vault refuses it: the pass fails, rather than Pi getting the key
	}
	h := p.Hostname()
	azure := strings.HasSuffix(h, ".openai.azure.com") || strings.HasSuffix(h, ".cognitiveservices.azure.com") || strings.HasSuffix(h, ".ai.azure.com")
	if azure && slices.Contains([]string{"", "/openai", "/openai/v1/responses"}, strings.TrimRight(p.Path, "/")) {
		p.Path, p.RawQuery = "/openai/v1", ""
	}
	return strings.TrimRight(p.String(), "/")
}

// KeyEnv is the env var holding provider's API key. A pass gets only its own provider's key.
func KeyEnv(provider string) string {
	if k, ok := keyEnvs[provider]; ok {
		return k
	}
	return envName(provider) + "_API_KEY"
}

// envName is provider as an env var prefix: amazon-bedrock -> AMAZON_BEDROCK.
func envName(provider string) string { return strings.ToUpper(strings.ReplaceAll(provider, "-", "_")) }

func providerOf(model string) string {
	p, _, _ := strings.Cut(model, "/")
	return p
}

// Available reports whether passes on provider can authenticate: its Need, or the ChatGPT login.
func Available(provider string) bool {
	p := providerFor(provider)
	if p.keyed() {
		return true
	}
	var logins map[string]json.RawMessage
	tokenMu.Lock() // not while a refresh rewrites it
	b, err := os.ReadFile(filepath.Join(AuthDir(), "auth.json"))
	tokenMu.Unlock()
	return p.Login && err == nil && json.Unmarshal(b, &logins) == nil && logins[provider] != nil
}

// needs says what to set for provider's passes.
func needs(provider string) string {
	p := providerFor(provider)
	var sets []string
	for _, set := range p.Need {
		sets = append(sets, strings.Join(set, " + "))
	}
	if p.Login {
		sets = append(sets, "run `diffharbor login`")
	}
	return strings.Join(sets, " or ")
}

// passEnv is the env a pass on provider authenticates with until ctx is done: its Need and Opt that are set, without
// them the ChatGPT login's access token as the key. Through the vault the key is a token, and the other secrets and
// the endpoint stay out: models.json points Pi at the vault.
func passEnv(ctx context.Context, provider string) ([]string, error) {
	p := providerFor(provider)
	if !p.keyed() {
		if !p.Login {
			return nil, errors.New("set " + needs(provider))
		}
		token, err := OpenAIToken()
		return []string{KeyEnv(provider) + "=" + token}, err
	}
	var env []string
	up := upstream(provider)
	ks := slices.Concat(slices.Concat(p.Need...), p.Opt)
	for i, k := range ks {
		v := os.Getenv(k)
		if v == "" || slices.Index(ks, k) != i {
			continue
		}
		switch {
		case up != "" && k == KeyEnv(provider):
			tok, err := vault.Key(ctx, provider, up, v, vaulted[provider].paths...)
			if err != nil {
				return nil, err
			}
			v = tok
		case up != "" && (secretVar.MatchString(k) || strings.HasSuffix(k, "_BASE_URL")):
			continue
		}
		env = append(env, k+"="+v)
	}
	if up != "" && provider == "amazon-bedrock" {
		env = append(env, "AWS_BEDROCK_FORCE_HTTP1=1") // the vault is plain HTTP/1, the SDK's default HTTP/2
	}
	return env, nil
}

// Workspace is proc.Workspace with Pi's models.json in the job's fresh home, see proxies. A local run (not root)
// keeps its own Pi config.
func Workspace(ctx context.Context, repo string) (*proc.Sandbox, error) {
	sb, err := proc.Workspace(repo)
	if err != nil || !proc.AsPi {
		return sb, err
	}
	if err := proxies(ctx, sb, filepath.Join(sb.Home(), ".pi", "agent", "models.json")); err != nil {
		sb.Close()
		return nil, err
	}
	return sb, nil
}

// proxies writes Pi's models.json f: it routes built-in providers through a proxy or gateway (LiteLLM, a company's
// AI gateway): <PROVIDER>_BASE_URL, e.g. ANTHROPIC_BASE_URL, becomes the provider's baseUrl; its models and auth stay
// Pi's, the key is the proxy's. A provider whose key the vault keeps points at the vault, which forwards to the gateway.
func proxies(ctx context.Context, sb *proc.Sandbox, f string) error {
	providers := map[string]map[string]any{}
	for _, r := range Matrix {
		switch u := os.Getenv(envName(r.Name) + "_BASE_URL"); {
		case upstream(r.Name) != "":
			providers[r.Name] = map[string]any{"baseUrl": vaultURL() + "/" + r.Name}
		case u != "" && r.Proxy:
			providers[r.Name] = map[string]any{"baseUrl": u}
		}
		// A gateway (LiteLLM, one on Bedrock) rejects the effort Pi sends as a system message mid-conversation
		// (messages.N.output_config): the effort goes in output_config instead (pi-ai api/anthropic-messages.js).
		// Likewise the tool_removal block Pi sends when a session goes on with other tools (messages.N.content.0: 400).
		if p := providers[r.Name]; p != nil && r.Name == "anthropic" && os.Getenv("ANTHROPIC_BASE_URL") != "" {
			p["compat"] = map[string]bool{"supportsMidConvoEffort": false, "supportsMidConvoToolChanges": false}
		}
	}
	if len(providers) == 0 {
		return sb.Remove(f) // as the job's user: Pi's config dir is its
	}
	b, _ := json.Marshal(map[string]any{"providers": providers})
	return sb.WriteFile(ctx, f, b)
}

// Default is the review model when neither REVIEW_MODEL nor review.json names one: the first provider in Matrix
// with credentials. With none it's the first, whose pass then says which key is missing.
func Default() string {
	for _, r := range Matrix {
		if Available(r.Name) {
			return r.Review
		}
	}
	return Matrix[0].Review
}

// maker is the lab behind a provider's models: Bedrock hosts Claude, Azure GPT, Vertex Gemini.
func maker(provider string) string {
	return cmp.Or(map[string]string{"amazon-bedrock": "anthropic", "azure": "openai", "google-vertex": "google"}[provider], provider)
}

// Invert is the review and verify model of the first provider in Matrix with credentials and another maker than
// review's, when review's maker wrote most of the commits: authors has the maker of each commit an AI wrote. by
// names that maker. "" = review stays.
func Invert(review string, authors []string) (rm, vm, by string) {
	n := map[string]int{}
	for _, a := range authors {
		n[a]++
	}
	own := maker(providerOf(review))
	if n[own] == 0 || n[own] < slices.Max(slices.Collect(maps.Values(n))) {
		return "", "", ""
	}
	for _, r := range Matrix {
		if maker(r.Name) != own && Available(r.Name) {
			return r.Review, r.Verify, own
		}
	}
	return "", "", ""
}

// VerifyFor is the verify model to pair with review: the cheap one of its provider, "" = review verifies.
func VerifyFor(review string) string { return providerFor(providerOf(review)).Verify }

// SecondFor is the model of a further pass beside review: its provider's Second, for a provider not listed review.
func SecondFor(review string) string { return cmp.Or(providerFor(providerOf(review)).Second, review) }

// FallbackFor is the model that takes over the pass called name from model: FALLBACK_MODEL, else the first other
// provider in Matrix with credentials. "" = none.
func FallbackFor(model, name string) string {
	if NoFallback {
		return ""
	}
	fm := FallbackModel
	for _, r := range Matrix {
		if fm == "" && r.Name != providerOf(model) && Available(r.Name) {
			fm = r.Review
		}
	}
	if (strings.HasPrefix(name, "verify") || name == "explain") && fm != "" {
		return cmp.Or(FallbackVerifyModel, VerifyFor(fm), fm)
	}
	return fm
}

// splitModel splits "provider/model:level" into the model and its fixed thinking level.
func splitModel(m string) (base, lvl string, fixed bool) {
	if i := strings.LastIndex(m, ":"); i > strings.LastIndex(m, "/") {
		return m[:i], m[i+1:], true
	}
	return m, "", false
}

// reviewLevel resolves the pass level against REVIEW_MODEL's fixed ":level".
func reviewLevel(m, lvl string) string {
	if _, own, fixed := splitModel(m); fixed {
		return own
	}
	return lvl
}

// PassModel is the --model of a pass: model at level lvl, unless model fixes its own.
func PassModel(model, lvl string) string {
	base, _, _ := splitModel(model)
	return base + ":" + reviewLevel(model, lvl)
}

// Object unmarshals the outermost {...} in a model reply into v.
func Object(text string, v any) bool {
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	return i >= 0 && j > i && json.Unmarshal([]byte(text[i:j+1]), v) == nil
}

// Usage sums the tokens and cost of the pass sessions in dir, one line per pass and a total, for benches.
func Usage(dir string) string {
	type use struct {
		in, cached, out, turns int
		cost                   float64
	}
	var b strings.Builder
	var sum use
	fs, _ := filepath.Glob(filepath.Join(dir, "*.session"))
	for _, f := range fs {
		data, err := os.ReadFile(f) //nolint:gosec // our own workspace
		if err != nil {
			continue
		}
		var u use
		for l := range bytes.SplitSeq(data, []byte("\n")) {
			var e struct {
				Message struct {
					Usage *struct {
						Input, Output, CacheRead int
						Cost                     struct{ Total float64 }
					}
				}
			}
			if json.Unmarshal(l, &e) != nil || e.Message.Usage == nil {
				continue
			}
			x := e.Message.Usage
			u.in, u.cached, u.out, u.turns, u.cost = u.in+x.Input, u.cached+x.CacheRead, u.out+x.Output, u.turns+1, u.cost+x.Cost.Total
		}
		fmt.Fprintf(&b, "%-10s %2d turns  in %8d  cached %8d  out %6d  $%.3f\n", strings.TrimSuffix(filepath.Base(f), ".session"), u.turns, u.in, u.cached, u.out, u.cost)
		sum = use{sum.in + u.in, sum.cached + u.cached, sum.out + u.out, sum.turns + u.turns, sum.cost + u.cost}
	}
	fmt.Fprintf(&b, "%-10s %2d turns  in %8d  cached %8d  out %6d  $%.3f", "total", sum.turns, sum.in, sum.cached, sum.out, sum.cost)
	return b.String()
}

// Pi runs passes with the pi coding agent as a subprocess.
type Pi struct{}

// Run runs one agent pass with tools and returns its reply. The pass keeps a
// session so a reply without JSON can be repaired without redoing the tool work.
func (Pi) Run(ctx context.Context, sb *proc.Sandbox, name, system, prompt, task, m string) (string, error) {
	file := filepath.Join(sb.Dir, name+".md")
	if err := sb.WriteFile(ctx, file, []byte(prompt)); err != nil {
		return "", err
	}
	return runPi(ctx, sb, name, system, m, true, "", "@"+file, task) // task is ours, the same for every PR
}

// Continue resumes the pass's session without tools, so a model that ended
// without JSON has to answer with text/JSON.
func (Pi) Continue(ctx context.Context, sb *proc.Sandbox, name, system, task, m string) (string, error) {
	return runPi(ctx, sb, name, system, m, false, task)
}

// runPi runs pi with m, a full --model value (provider/id:level); stdin, when set, is sent to pi before promptArgs.
// An openai/ pass the subscription can't serve reruns on FALLBACK_MODEL at the same level, and the next 30 minutes of
// passes skip straight to it.
func runPi(ctx context.Context, sb *proc.Sandbox, name, system, m string, tools bool, stdin string, promptArgs ...string) (string, error) {
	// One switch for all passes: with mixed providers a failing verify model also moves the reviews to the fallback.
	fm := FallbackFor(m, name)
	if fm == "" {
		return runPiOn(ctx, sb, name, system, m, tools, stdin, promptArgs...)
	}
	if time.Now().UnixMilli() >= FallbackUntil.Load() {
		out, err := runPiOn(ctx, sb, name, system, m, tools, stdin, promptArgs...)
		if err == nil || ctx.Err() != nil || !unavailable.MatchString(err.Error()) {
			return out, err
		}
		log.Printf("%s: %v; %s for 30 min", m, err, fm)
		FallbackUntil.Store(time.Now().Add(30 * time.Minute).UnixMilli())
		if tools { // a fresh pass: the failed run's session would hold the prompt twice
			os.Remove(filepath.Join(sb.Dir, name+".session"))
		}
	}
	_, lvl, _ := splitModel(m)
	fb, _, _ := splitModel(fm)
	return runPiOn(ctx, sb, name, system, fb+":"+reviewLevel(fm, lvl), tools, stdin, promptArgs...)
}

// Private text (the system prompt's rules and learnings, a task with findings or code) goes through a file or stdin,
// never argv: /proc/<pid>/cmdline is world-readable, also to another repo's job running at the same time.
func runPiOn(ctx context.Context, sb *proc.Sandbox, name, system, m string, tools bool, stdin string, promptArgs ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	// Soft deadline (review-guard.js refuses tool calls after it): 2 minutes left for the answer.
	deadline := fmt.Sprintf("DIFFHARBOR_DEADLINE=%d", time.Now().Add(6*time.Minute).UnixMilli())
	creds, err := passEnv(ctx, providerOf(m)) // a vault token dies with ctx, when this run ends
	if err != nil {
		return "", fmt.Errorf("%s: %w", m, err)
	}
	sys := filepath.Join(sb.Dir, name+".system.md") // --system-prompt reads a path that exists
	if err := sb.WriteFile(ctx, sys, []byte(system)); err != nil {
		return "", err
	}
	// --no-approve: a .pi/ in the checkout (PR-controlled) is ignored. fff replaces grep/find.
	args := []string{"-p", "--session", filepath.Join(sb.Dir, name+".session"), "--no-approve", "--model", m}
	if tools {
		args = append(args, "--tools", "read,grep,find,ls,bash")
	} else {
		args = append(args, "--no-builtin-tools")
	}
	args = append(args, "--no-mcp", "--no-skills", "--no-context-files", "--no-prompt-templates",
		"--system-prompt", sys)
	// Review skills (github.Skills): listed in both passes and repairs, so the prompt prefix stays cacheable.
	for _, src := range []string{"repo", "owner"} { // repo first: it wins a name clash
		if sk := filepath.Join(sb.Dir, "skills", src); proc.Exists(sk) {
			args = append(args, "--skill", sk)
		}
	}
	args = append(args, promptArgs...)
	env := append(append(sb.ToolEnv(), creds...), "PI_FFF_MODE=override", deadline, fmt.Sprintf("DIFFHARBOR_MAX_TOOLS=%d", maxTools))
	// stdin is a pipe, never a terminal: pi -p would wait for typed input otherwise.
	c := sb.Command(ctx, sb.Repo(), env, "pi", args...)
	c.Stdin = strings.NewReader(stdin)
	out, err := proc.Run(c)
	if err != nil {
		return "", err
	}
	for _, c := range creds {
		if k, v, _ := strings.Cut(c, "="); secretVar.MatchString(k) && bytes.Contains(out, []byte(v)) {
			return "", errors.New("secret in model output, not posting")
		}
	}
	return string(out), nil
}

// AuthDir holds Pi's logins, written by `diffharbor login`.
func AuthDir() string { return filepath.Join(proc.DataDir, "pi") }

// Check reports a review model nobody can log in to: no key, or a ChatGPT login that doesn't work.
func Check() error {
	m := cmp.Or(Model, Default())
	switch p := providerOf(m); {
	case !Available(p):
		return fmt.Errorf("%s: set %s", m, needs(p))
	case p == "openai" && os.Getenv("OPENAI_API_KEY") == "":
		_, err := OpenAIToken()
		return err
	}
	return nil
}

// tokenMu serializes OpenAIToken: Pi may rotate the refresh token, and two refreshes at once lose the login. A mutex,
// not singleflight: each caller wants a token that outlives its own pass, and x/sync would be a new dependency.
var tokenMu sync.Mutex

// OpenAIToken is a bearer token from the ChatGPT login, valid for 20 more minutes at least.
// Pi's ChatGPT login lives on the root-only data volume (log in once, see README). Pi runs as the job's user
// next to PR code, so it only gets an access token that outlives the pass, never the refresh token.
func OpenAIToken() (string, error) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, "pi", "auth", "print-bearer-token", "--provider", "openai", "--min-expiry", "20m")
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "PI_CODING_AGENT_DIR=" + AuthDir()}
	out, err := proc.Run(c)
	token := strings.TrimSpace(string(out))
	if err == nil && token == "" {
		err = errors.New("pi auth: empty token")
	}
	if err != nil {
		return "", fmt.Errorf("ChatGPT login missing, run `diffharbor login`: %w", err)
	}
	return token, nil
}
