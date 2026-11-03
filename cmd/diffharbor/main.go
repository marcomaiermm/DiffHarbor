// GitHub/GitLab webhook -> checks + Pi review + Pi verification -> one PR review with inline comments. Stdlib only.
//
//	diffharbor       serve on :3000
//	diffharbor login log in to a model subscription (ChatGPT/Codex) in Pi, stored in $DATA_DIR/pi
//	diffharbor dry   review `git diff HEAD` of cwd, print JSON
//	go test         self-check
package main

import (
	"cmp"
	"context"
	"diffharbor/internal/agent"
	"diffharbor/internal/forge/github"
	"diffharbor/internal/forge/gitlab"
	"diffharbor/internal/job"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
	"diffharbor/internal/review"
	"diffharbor/internal/server"
	"diffharbor/internal/store"
	"diffharbor/internal/ticket"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

func dry() error {
	dir, err := proc.Workspace()
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	r, stats, err := review.Tree(context.Background(), cwd, dir, "", nil, false)
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b), "\n"+stats)
	return nil
}

// login opens Pi on the bot's credential store; Pi refreshes the token from then on.
func login() error {
	fmt.Println("In Pi: /login -> OpenAI (ChatGPT Plus/Pro) -> Device code login, then /quit.")
	c := exec.Command("pi")
	c.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+agent.AuthDir())
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return err
	}
	_, err := agent.OpenAIToken()
	return err
}

// configure reads the server's settings from the env, the one place that does. Model keys and
// <PROVIDER>_BASE_URL stay in the env: Pi's passes take them by name (agent.Matrix, agent.Proxies).
func configure() server.Config {
	proc.DataDir = cmp.Or(os.Getenv("DATA_DIR"), proc.DataDir)
	proc.CacheDir = cmp.Or(os.Getenv("CACHE_DIR"), proc.CacheDir)
	agent.Model, agent.VerifyModel = os.Getenv("REVIEW_MODEL"), os.Getenv("VERIFY_MODEL")
	agent.FallbackModel, agent.FallbackVerifyModel = os.Getenv("FALLBACK_MODEL"), os.Getenv("FALLBACK_VERIFY_MODEL")
	ticket.URL, ticket.APIKey = os.Getenv("ITSAPLAN_URL"), os.Getenv("ITSAPLAN_API_KEY")
	job.ReportURL = strings.TrimSuffix(os.Getenv("REPORT_URL"), "/")
	if u := os.Getenv("REPORT_S3_URL"); u != "" {
		s, err := store.NewS3(u, os.Getenv("REPORT_S3_ACCESS_KEY"), os.Getenv("REPORT_S3_SECRET_KEY"),
			cmp.Or(os.Getenv("REPORT_S3_REGION"), "us-east-1"))
		if err != nil {
			log.Fatal(err)
		}
		job.ReportS3 = s
	}
	return server.Config{
		GitHub: github.Config{
			AppID: os.Getenv("GITHUB_APP_ID"), KeyB64: os.Getenv("GITHUB_APP_PRIVATE_KEY_B64"), Secret: os.Getenv("GITHUB_WEBHOOK_SECRET"),
		},
		GitLab: gitlab.Config{
			URL: os.Getenv("GITLAB_URL"), Token: os.Getenv("GITLAB_TOKEN"), ReadToken: os.Getenv("GITLAB_READ_TOKEN"),
			Secret: os.Getenv("GITLAB_WEBHOOK_SECRET"),
		},
	}
}

func main() {
	cfg := configure()
	if err := agent.Proxies(); err != nil {
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
