// GitHub/GitLab webhook -> checks + Pi review + Pi verification -> one PR review with inline comments. Stdlib only.
//
//	diffharbor       serve on :3000
//	diffharbor login log in to a model subscription (ChatGPT/Codex) in Pi, stored in $DATA_DIR/pi
//	diffharbor dry   review `git diff HEAD` of cwd, print JSON
//	diffharbor stats [owner/repo]  print what became of the findings of closed PRs, per repo
//	go test         self-check
package main

import (
	"cmp"
	"context"
	"diffharbor/internal/agent"
	"diffharbor/internal/checks"
	"diffharbor/internal/forge/github"
	"diffharbor/internal/forge/gitlab"
	"diffharbor/internal/job"
	"diffharbor/internal/knowledge"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
	"diffharbor/internal/review"
	"diffharbor/internal/server"
	"diffharbor/internal/store"
	"diffharbor/internal/ticket"
	"diffharbor/internal/vault"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

func dry() error {
	sb, err := agent.Workspace(context.Background(), "")
	if err != nil {
		return err
	}
	defer sb.Close()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	r, stats, err := review.Tree(context.Background(), sb, cwd, "", nil, false)
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b), "\n"+stats)
	fmt.Fprintln(os.Stderr, agent.Usage(sb.Dir))
	return nil
}

// login opens Pi on the bot's credential store; Pi refreshes the token from then on.
func login() error {
	fmt.Println("In Pi: /login -> OpenAI (ChatGPT Plus/Pro) -> Device code login, then /quit.")
	c := exec.Command("pi")
	c.Env = slices.DeleteFunc(os.Environ(), func(e string) bool { return strings.HasPrefix(e, "REPORT_S3_") }) // the server's
	c.Env = append(c.Env, "PI_CODING_AGENT_DIR="+agent.AuthDir())
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return err
	}
	_, err := agent.OpenAIToken()
	return err
}

// configure reads the server's settings from the env, the one place that does. Model keys and
// <PROVIDER>_BASE_URL stay in the env: Pi's passes take them by name (agent.Matrix, agent.Workspace).
func configure() server.Config {
	proc.DataDir = cmp.Or(os.Getenv("DATA_DIR"), proc.DataDir)
	proc.CacheDir = cmp.Or(os.Getenv("CACHE_DIR"), proc.CacheDir)
	agent.Model, agent.VerifyModel = os.Getenv("REVIEW_MODEL"), os.Getenv("VERIFY_MODEL")
	agent.FallbackModel, agent.FallbackVerifyModel = os.Getenv("FALLBACK_MODEL"), os.Getenv("FALLBACK_VERIFY_MODEL")
	ticket.URL, ticket.APIKey = os.Getenv("ITSAPLAN_URL"), os.Getenv("ITSAPLAN_API_KEY")
	ticket.LinearKey, ticket.Provider = os.Getenv("LINEAR_API_KEY"), os.Getenv("TICKET_PROVIDER")
	job.ReportURL = strings.TrimSuffix(os.Getenv("REPORT_URL"), "/")
	if u := os.Getenv("REPORT_S3_URL"); u != "" {
		s, err := store.NewS3(u, os.Getenv("REPORT_S3_ACCESS_KEY"), os.Getenv("REPORT_S3_SECRET_KEY"),
			cmp.Or(os.Getenv("REPORT_S3_REGION"), "us-east-1"))
		if err != nil {
			log.Fatal(err)
		}
		job.ReportS3 = s
	}
	// MAX_JOBS>1 runs jobs of different repos at once: they share /tmp, /var/tmp, /dev/shm, the loopback network, the
	// process list and the container's limits. Fine when all repos are one team's; keep 1 when untrusted repos of
	// different owners are reviewed side by side.
	jobs, err := strconv.Atoi(cmp.Or(os.Getenv("MAX_JOBS"), "1"))
	if err != nil || jobs < 1 {
		log.Fatalf("MAX_JOBS=%q: want a positive number of jobs that run at once", os.Getenv("MAX_JOBS"))
	}
	return server.Config{
		MaxJobs: jobs,
		GitHub: github.Config{
			AppID: os.Getenv("GITHUB_APP_ID"), KeyB64: os.Getenv("GITHUB_APP_PRIVATE_KEY_B64"), Secret: os.Getenv("GITHUB_WEBHOOK_SECRET"),
		},
		GitLab: gitlab.Config{
			URL: os.Getenv("GITLAB_URL"), Token: os.Getenv("GITLAB_TOKEN"), ReadToken: os.Getenv("GITLAB_READ_TOKEN"),
			Secret: os.Getenv("GITLAB_WEBHOOK_SECRET"), Findings: os.Getenv("GITLAB_FINDINGS"),
		},
	}
}

func main() {
	log.Printf("diffharbor %s", version)
	cfg := configure()
	if len(os.Args) > 1 && os.Args[1] == "stats" {
		repo := ""
		if len(os.Args) > 2 {
			repo = os.Args[2]
		}
		if err := job.Stats(os.Stdout, repo); err != nil {
			log.Fatal(err)
		}
		return
	}
	if proc.AsPi { // git and Pi run as a repo's user, next to the PR's code: they get the vault's tokens, not the keys
		if err := vault.Start(); err != nil {
			log.Fatal(err)
		}
	}
	if _, err := ticket.Active(); err != nil {
		log.Fatal(err)
	}
	if _, err := checks.Registries(""); err != nil {
		log.Fatal(err)
	}
	if _, err := knowledge.Remotes(""); err != nil {
		log.Fatal(err)
	}
	if err := report.Load(os.Getenv("TEMPLATE_DIR")); err != nil {
		log.Fatal(err)
	}
	if len(os.Args) > 1 && os.Args[1] == "login" {
		if err := login(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "dry" {
		agent.NoFallback = true // the configured model only: a usage limit stops it
		if err := dry(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := job.MoveReports(); err != nil {
		log.Println("moving the reports to S3:", err) // the next start tries again; DeleteReports deletes them on disk too
	}
	server.Serve(cfg)
}
