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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

// maxTools is the tool calls a pass gets (review-guard.js): every call re-reads the context, so calls are most of the cost.
const maxTools = 15

// Matrix is the default review and verify model per provider, in order of preference, and what its passes
// authenticate with. Without REVIEW_MODEL or review.json the first provider with credentials reviews and the next
// one is the fallback. A review model's provider here also sets its verify model unless VERIFY_MODEL or review.json
// name one. A provider not listed authenticates with KeyEnv alone.
var Matrix = []Provider{
	{Name: "openai", Review: "openai/gpt-6.1-terra:high", Verify: "openai/gpt-6-luna:low", Login: true, Proxy: true},
	{Name: "anthropic", Review: "anthropic/claude-sonnet-5-5:high", Verify: "anthropic/claude-haiku-5-5:low", Proxy: true},
	{Name: "deepseek", Review: "deepseek/deepseek-flash:high", Verify: "deepseek/deepseek-flash:off", Proxy: true}, // off: low's recall, cheaper
	// Claude and GPT hosted by a cloud. global.: any region serves; eu./us. (REVIEW_MODEL) keep data in one.
	// Bedrock signs with AWS credentials or a Bedrock API key.
	{
		Name: "amazon-bedrock", Review: "amazon-bedrock/global.anthropic.claude-sonnet-5-5:high", Verify: "amazon-bedrock/global.anthropic.claude-haiku-5-5:low",
		Need: [][]string{{"AWS_REGION", "AWS_BEARER_TOKEN_BEDROCK"}, {"AWS_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}},
	},
	// Azure has no gpt-6.1-terra. It needs its endpoint and maps model IDs to the deployment names
	// (AZURE_OPENAI_DEPLOYMENT_NAME_MAP="gpt-6.1-sol=review,gpt-6-luna=verify"; unset = deployments named like the models).
	{
		Name: "azure", Review: "azure/gpt-6.1-sol:high", Verify: "azure/gpt-6-luna:low",
		Need: [][]string{{"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_BASE_URL"}}, Opt: []string{"AZURE_OPENAI_DEPLOYMENT_NAME_MAP"},
	},
}

// Provider is a Matrix row.
type Provider struct {
	Name, Review, Verify string
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
	b, err := os.ReadFile(filepath.Join(AuthDir(), "auth.json"))
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
func Workspace(repo string) (string, error) {
	dir, err := proc.Workspace(repo)
	if err != nil || !proc.AsPi {
		return dir, err
	}
	if err := proxies(filepath.Join(proc.Home(), ".pi", "agent", "models.json")); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// proxies writes Pi's models.json f: it routes built-in providers through a proxy or gateway (LiteLLM, a company's
// AI gateway): <PROVIDER>_BASE_URL, e.g. ANTHROPIC_BASE_URL, becomes the provider's baseUrl; its models and auth stay
// Pi's, the key is the proxy's. A provider whose key the vault keeps points at the vault, which forwards to the gateway.
func proxies(f string) error {
	providers := map[string]any{}
	for _, r := range Matrix {
		switch u := os.Getenv(envName(r.Name) + "_BASE_URL"); {
		case upstream(r.Name) != "":
			providers[r.Name] = map[string]string{"baseUrl": vaultURL() + "/" + r.Name}
		case u != "" && r.Proxy:
			providers[r.Name] = map[string]string{"baseUrl": u}
		}
	}
	if len(providers) == 0 {
		return proc.Remove(f) // as the job's user: Pi's config dir is its
	}
	b, _ := json.Marshal(map[string]any{"providers": providers})
	return proc.WriteFile(f, b)
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

// VerifyFor is the verify model to pair with review: the cheap one of its provider, "" = review verifies.
func VerifyFor(review string) string {
	for _, r := range Matrix {
		if r.Name == providerOf(review) {
			return r.Verify
		}
	}
	return ""
}

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

// Pi runs passes with the pi coding agent as a subprocess.
type Pi struct{}

// Run runs one agent pass with tools and returns its reply. The pass keeps a
// session so a reply without JSON can be repaired without redoing the tool work.
func (Pi) Run(ctx context.Context, dir, name, system, prompt, task, m string) (string, error) {
	file := filepath.Join(dir, name+".md")
	if err := proc.WriteFile(file, []byte(prompt)); err != nil {
		return "", err
	}
	return runPi(ctx, dir, name, system, m, true, "@"+file, task)
}

// Continue resumes the pass's session without tools, so a model that ended
// without JSON has to answer with text/JSON.
func (Pi) Continue(ctx context.Context, dir, name, system, task, m string) (string, error) {
	return runPi(ctx, dir, name, system, m, false, task)
}

// runPi runs pi with m, a full --model value (provider/id:level). An openai/ pass the subscription can't
// serve reruns on FALLBACK_MODEL at the same level, and the next 30 minutes of passes skip straight to it.
func runPi(ctx context.Context, dir, name, system, m string, tools bool, promptArgs ...string) (string, error) {
	// One switch for all passes: with mixed providers a failing verify model also moves the reviews to the fallback.
	fm := FallbackFor(m, name)
	if fm == "" {
		return runPiOn(ctx, dir, name, system, m, tools, promptArgs...)
	}
	if time.Now().UnixMilli() >= FallbackUntil.Load() {
		out, err := runPiOn(ctx, dir, name, system, m, tools, promptArgs...)
		if err == nil || ctx.Err() != nil || !unavailable.MatchString(err.Error()) {
			return out, err
		}
		log.Printf("%s: %v; %s for 30 min", m, err, fm)
		FallbackUntil.Store(time.Now().Add(30 * time.Minute).UnixMilli())
		if tools { // a fresh pass: the failed run's session would hold the prompt twice
			os.Remove(filepath.Join(dir, name+".session"))
		}
	}
	_, lvl, _ := splitModel(m)
	fb, _, _ := splitModel(fm)
	return runPiOn(ctx, dir, name, system, fb+":"+reviewLevel(fm, lvl), tools, promptArgs...)
}

func runPiOn(ctx context.Context, dir, name, system, m string, tools bool, promptArgs ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	// Soft deadline (review-guard.js refuses tool calls after it): 2 minutes left for the answer.
	deadline := fmt.Sprintf("DIFFHARBOR_DEADLINE=%d", time.Now().Add(6*time.Minute).UnixMilli())
	creds, err := passEnv(ctx, providerOf(m)) // a vault token dies with ctx, when this run ends
	if err != nil {
		return "", fmt.Errorf("%s: %w", m, err)
	}
	// --no-approve: a .pi/ in the checkout (PR-controlled) is ignored. fff replaces grep/find.
	args := []string{"-p", "--session", filepath.Join(dir, name+".session"), "--no-approve", "--model", m}
	if tools {
		args = append(args, "--tools", "read,grep,find,ls,bash")
	} else {
		args = append(args, "--no-builtin-tools")
	}
	args = append(args, "--no-mcp", "--no-skills", "--no-context-files", "--no-prompt-templates",
		"--system-prompt", system)
	// Review skills (github.Skills): listed in both passes and repairs, so the prompt prefix stays cacheable.
	for _, src := range []string{"repo", "owner"} { // repo first: it wins a name clash
		if sk := filepath.Join(dir, "skills", src); proc.Exists(sk) {
			args = append(args, "--skill", sk)
		}
	}
	args = append(args, promptArgs...)
	env := append(append(proc.ToolEnv(), creds...), "PI_FFF_MODE=override", deadline, fmt.Sprintf("DIFFHARBOR_MAX_TOOLS=%d", maxTools))
	// stdin is /dev/null: pi -p waits for piped stdin otherwise.
	out, err := proc.Run(proc.Command(ctx, filepath.Join(dir, "repo"), env, "pi", args...))
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

// OpenAIToken is a bearer token from the ChatGPT login, valid for 20 more minutes at least.
// Pi's ChatGPT login lives on the root-only data volume (log in once, see README). Pi runs as the job's user
// next to PR code, so it only gets an access token that outlives the pass, never the refresh token.
func OpenAIToken() (string, error) {
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
