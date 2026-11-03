// GitHub/GitLab webhook -> checks + Pi review + Pi verification -> one PR review with inline comments. Stdlib only.
//
//	diffharbor       serve on :3000
//	diffharbor login log in to a model subscription (ChatGPT/Codex) in Pi, stored in $DATA_DIR/pi
//	diffharbor dry   review `git diff HEAD` of cwd, print JSON
//	diffharbor bench [-n 3] [-j 4] [case...]   score reviews of known PRs (internal/bench)
//	go test         self-check
package main

import (
	"context"
	"diffharbor/internal/agent"
	"diffharbor/internal/bench"
	"diffharbor/internal/proc"
	"diffharbor/internal/review"
	"diffharbor/internal/server"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
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
	c.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+agent.AuthDir)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return err
	}
	_, err := agent.OpenAIToken()
	return err
}

func main() {
	if err := agent.Proxies(); err != nil {
		log.Fatal(err)
	}
	if len(os.Args) > 1 && os.Args[1] == "login" {
		if err := login(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "dry" || os.Args[1] == "bench") {
		agent.NoFallback = true // benches run on the configured model only: a usage limit stops them
		f := dry
		if os.Args[1] == "bench" {
			f = func() error { return bench.Run(os.Args[2:]) }
		}
		if err := f(); err != nil {
			log.Fatal(err)
		}
		return
	}
	server.Serve()
}
