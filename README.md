# DiffHarbor

Self-hosted AI code review for GitHub and GitLab. A small Go server (standard library only) takes the webhook, runs the linters and tests your repo already has, lets an agent ([Pi](https://github.com/earendil-works/pi)) review the diff with read/grep/bash in a checkout, has a second model verify every finding against the code, and posts what survives as inline comments with committable suggestions.

Community Edition, [FSL-1.1-ALv2](LICENSE.md): use it, change it, run it for your company. You may not offer it as a competing commercial product or service. Each version turns into Apache-2.0 two years after its release.

## What a review does

1. **Diff.** On open, the whole PR against its merge base. On a push, only files whose changed lines changed since the last review: a rebase or a merge of main reviews nothing. Drafts, lockfiles, build output, generated code and binaries are left out. A pass gets up to 60k characters of diff, code first, then tests, data, docs; larger PRs can be reviewed in parts (see [Large PRs](#large-prs)).
2. **Context.** Checks on the changed lines: gitleaks, tsc/biome/eslint, oxlint (type-aware), gofmt/vet/staticcheck, golangci-lint (the repo's config, else a broad default), deadcode, fallow (unused code, cycles, duplicates, complexity, security candidates), Drizzle migrations, squawk, shellcheck, luacheck, actionlint, zizmor, osv-scanner, and the tests of changed packages when the CI doesn't run them. Plus codegraph, co-change from git history, the repo's rules and skills from the base branch, learnings, and optionally the linked ticket.
3. **Review.** The agent returns up to 10 findings per pass.
4. **Verify.** A cheaper model checks each finding in the code and drops what it can't back up.
5. **Result.** A walkthrough comment, inline comments, a commit status `diffharbor`. Fixed findings get resolved. A local fix becomes a suggestion block.

Chat: reply to a bot thread or write `@<bot> <question>`. `@<bot> review` re-reviews the whole PR, `@<bot> review all` reviews a large PR in parts. Corrections become rules in `/data/learnings/` (per repo and per owner, 30 each). PRs from bots (Dependabot, Renovate) are skipped with a green status unless asked.

## Setup

```sh
cp compose.example.yml compose.yml   # values go into .env next to it
docker compose up -d --build
```

The server listens on :3000. Expose `POST /webhook/github` and/or `POST /webhook/gitlab` to your forge; nothing else needs to be public. At least one forge must be configured.

### GitHub

Create a GitHub App:

- Webhook URL `https://<host>/webhook/github`, a random secret of 20+ characters.
- Permissions: Contents read/write (GitHub requires write to resolve threads; reviews get a read-only token), Pull requests read/write, Commit statuses read/write, Issues read, Metadata read.
- Events: Pull request, Pull request review comment, Issue comment.

Env: `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY_B64` (the `.pem`, base64), `GITHUB_WEBHOOK_SECRET`. Mention the bot by the App's slug. Webhooks that got no 2xx (server down, queue full) are redelivered on start and every 30 minutes.

### GitLab

- `GITLAB_URL`: default `https://gitlab.com`.
- `GITLAB_TOKEN`: a bot user's token (or a project/group access token) with scope `api` and at least the Developer role. Its username is the bot: `@<username> review`.
- `GITLAB_READ_TOKEN` (recommended): a token with only `read_repository`, for git. Without it git gets the `api` token next to the MR's code; the server warns on start.
- `GITLAB_WEBHOOK_SECRET`: the webhook's secret token.
- Webhook on the project or group: `https://<host>/webhook/gitlab`, triggers Merge request events and Comments.

Differences to GitHub: findings are diff discussions, fixes `suggestion:-N+0` blocks; a finding GitLab refuses to place goes into the summary note. Large MRs in parts start with `@<bot> review all` (there is no checkbox). No redelivery: GitLab retries a failed webhook only briefly. Maintainers count as members, Developers as collaborators.

### Models

Models are `provider/id[:level]` as Pi names them. Without settings the server picks by the keys present; the first provider reviews, the next one is the fallback for 30 minutes when the first fails (usage limit, outage).

| Provider (order) | Review | Verify |
|---|---|---|
| OpenAI (`OPENAI_API_KEY`) | `openai/gpt-6.1-terra:high` | `openai/gpt-6-luna:low` |
| Anthropic (`ANTHROPIC_API_KEY`) | `anthropic/claude-sonnet-5-5:high` | `anthropic/claude-haiku-5-5:low` |
| DeepSeek (`DEEPSEEK_API_KEY`) | `deepseek/deepseek-flash:high` | `deepseek/deepseek-flash:off` |
| AWS Bedrock (`AWS_REGION` and `AWS_BEARER_TOKEN_BEDROCK` or `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY`) | `amazon-bedrock/global.anthropic.claude-sonnet-5-5:high` | `amazon-bedrock/global.anthropic.claude-haiku-5-5:low` |
| Azure OpenAI (`AZURE_OPENAI_API_KEY` and `AZURE_OPENAI_BASE_URL`) | `azure/gpt-6.1-sol:high` | `azure/gpt-6-luna:low` |

Override with `REVIEW_MODEL`, `VERIFY_MODEL`, `FALLBACK_MODEL`, `FALLBACK_VERIFY_MODEL` (env wins over a repo's `review.json`). A gateway in front of a provider (LiteLLM, your company's AI gateway) needs only its URL: `ANTHROPIC_BASE_URL`, `OPENAI_BASE_URL` or `DEEPSEEK_BASE_URL`, with the gateway's key. Bedrock in the EU: `amazon-bedrock/eu.anthropic.…`. Azure deployments not named like the models: `AZURE_OPENAI_DEPLOYMENT_NAME_MAP=gpt-6.1-sol=review,gpt-6-luna=verify`. Each pass gets only its own provider's key.

Use API keys. Pi can also log in with a ChatGPT subscription (`openai/…` without `OPENAI_API_KEY`, then `docker compose exec -it diffharbor diffharbor login`); whether your plan's terms allow that for a review bot is yours to check, and you use it at your own risk.

### Optional

- `REPORT_URL`: an HTML page per PR under `/r/<host>/<repo>/<n>` (reading order, diffs with findings, call graphs, what the change reaches outside the PR, and before you merge: base commits the PR is behind on its files, conflicts, open PRs changing the same files or lines). Each reviewed head keeps a version, the newest 10 stay; closing the PR deletes them. It contains the PR's code: serve `/r/` only behind your SSO. Empty = no reports.
- `ITSAPLAN_URL` + `ITSAPLAN_API_KEY`: the itsaplan ticket a PR names (`ABC-12` in title, branch or description) goes into the review as what the PR must do. Never for PRs from forks.
- `DOCKER_HOST` and the `dind` service: Docker for testcontainers in the checks, isolated with [Sysbox](https://github.com/nestybox/sysbox) (install it on the host). Drop both if your tests don't need Docker.

## Per repo

Everything lives under `.review/`, paths can be changed in `review.json`:

- `.review/rules.md`: rules in prose, go into the prompt. Without it, `AGENTS.md` or `CLAUDE.md`.
- `.review/skills/`: review skills in the [Agent Skills format](https://agentskills.io/specification). On GitHub, skills in `<owner>/.github` under `.review/skills/` apply to all of the owner's repos.
- `.review/checks.sh`: your own check scripts from the base branch, run in the PR checkout with the changed files after `--`. Their output (120 lines each) goes into the prompt; the exit code doesn't count.
- `review.json` in the root, all keys optional:

  ```json
  {
    "ignore": ["drizzle/meta", "locales/*.json", "**/*.fixture.ts"],
    "min_severity": "minor",
    "tests": true,
    "review_model": "openai/gpt-6.1-sol",
    "verify_model": "anthropic/claude-haiku-5-5:low",
    "large_pr": "ask",
    "max_parts": 4,
    "report": "auto",
    "tickets": ["ABC"],
    "checks": [".review/checks.sh"],
    "packages": {
      "apps/web": {"min_severity": "major"},
      "services/*": {"tests": false}
    }
  }
  ```

  `ignore`: globs, a path without one takes its folder; neither the model nor a check sees these files. `min_severity` (`minor|major|critical`): findings below are dropped. `tests`: run the tests of changed packages; unset, they run only when no CI workflow does. `review_model`, `verify_model`: apply where the server sets none; the provider's key must be on the server. `report` (`auto` from 10 files, `always`, `off`). `tickets`: itsaplan projects a PR may name. `packages`: overrides per monorepo part, longest glob wins. A broken `review.json` falls back to the defaults and says so in the review footer.

## Large PRs

A diff over 60k characters can be reviewed in parts: files of a folder stay together, each part gets its own review and verify pass, the findings are merged, most severe first. Each part costs about one normal review.

- `"large_pr": "ask"` (default): only code up to 60k first. On GitHub the walkthrough offers a checkbox to review all files in parts; on both forges `@<bot> review all` does the same. Later pushes keep reviewing in parts.
- `"all"`: always in parts. `"off"`: never.
- `max_parts` (default 4, at most 8).

## Monorepos

Checks run per package (nearest `package.json` or `go.mod`, at most 5 packages), installs once per lockfile. Rules: `AGENTS.md` (else `CLAUDE.md`) from the folders above the changed files are added, at most 5.

## Security

The checks and the agent run the PR's code, also for PRs from forks. Install the bot only on repos whose PR authors you trust (your team). Within that:

- Root serves and holds the forge keys and Pi's login. git, checks and Pi run as `node`; git gets a read-only token (short-lived on GitHub), by env, not argv.
- Root reads and writes in the checkout only as `node`: a symlink in the PR can't reach root's files.
- Processes a job leaves behind are killed before and after each job. Check output is capped.
- During a Pi run, the PR's code can read Pi's model key from its env (same uid). A gateway that adds the key itself avoids that.
- Limits: 3 GB RAM, 2 CPUs per container. A Pi run gets 8 minutes; [extensions/review-guard.js](extensions/review-guard.js) refuses further tool calls after 6 minutes or 60% context so the pass still answers.

## Development

```sh
go test ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...
```

`diffharbor dry` in a checkout reviews `git diff HEAD` and prints the findings as JSON. `diffharbor bench` scores reviews of known PRs against a key ([internal/bench](internal/bench/bench.go)); the cases come from your own repos.

## Contributing

This repository does not take external contributions at the moment. Issues with bug reports are welcome.
