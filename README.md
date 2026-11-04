<p align="center">
  <img src="assets/logo.png" alt="DiffHarbor logo: a lighthouse over waves" width="160">
</p>

<h1 align="center">DiffHarbor</h1>

<p align="center">
  <strong>Self-hosted AI code review for GitHub and GitLab.</strong><br>
  It runs the linters and tests your repo already has, verifies every finding against the code,<br>
  and comments only what holds up.
</p>

<p align="center">
  <a href="LICENSE.md"><img src="https://img.shields.io/badge/license-FSL--1.1--ALv2-blue?style=flat-square" alt="License: FSL-1.1-ALv2"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/marcomaiermm/DiffHarbor?style=flat-square&logo=go&logoColor=white" alt="Go version"></a>
  <img src="https://img.shields.io/badge/dependencies-stdlib%20only-success?style=flat-square" alt="Dependencies: Go standard library only">
  <img src="https://img.shields.io/badge/forges-GitHub%20%7C%20GitLab-181717?style=flat-square&logo=git&logoColor=white" alt="Forges: GitHub and GitLab">
  <a href="https://github.com/earendil-works/pi"><img src="https://img.shields.io/badge/agent-Pi-8A2BE2?style=flat-square" alt="Agent: Pi"></a>
  <a href="https://github.com/marcomaiermm/DiffHarbor/commits/main"><img src="https://img.shields.io/github/last-commit/marcomaiermm/DiffHarbor?style=flat-square" alt="Last commit"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="#how-a-review-works">How it works</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="#per-repo-settings">Per repo</a> ·
  <a href="#security">Security</a>
</p>

---

A small Go server takes the webhook, runs your checks on the changed lines, lets an agent ([Pi](https://github.com/earendil-works/pi)) review the diff with read, grep and bash in a checkout, has a second model verify every finding against the code, and posts what survives as inline comments with committable suggestions.

## Highlights

- **Verified findings.** A second, cheaper model checks each finding in the code and drops what it can't back up.
- **Your own checks as context.** gitleaks, tsc, biome, eslint, oxlint, gofmt, vet, staticcheck, golangci-lint, deadcode, fallow, squawk, shellcheck, luacheck, actionlint, zizmor, osv-scanner and the tests of changed packages, on the changed lines.
- **Incremental.** A push reviews only files whose changed lines changed. A rebase or a merge of main reviews nothing.
- **Committable fixes.** A local fix becomes a suggestion block; fixed findings get their threads resolved.
- **Chat and learnings.** Reply to a finding or mention the bot; corrections become rules for the next reviews.
- **Any model Pi supports.** OpenAI, Anthropic, DeepSeek, AWS Bedrock, Azure OpenAI or a gateway in front of them, with automatic fallback.
- **PR reports.** An optional HTML page per PR with a reading order, diffs with findings, call graphs and merge risks.
- **Small and self-hosted.** One container, Go standard library only. Your code stays on your server and with your model provider.

## Quick start

```sh
cp compose.example.yml compose.yml   # the values go into .env next to it
docker compose up -d
```

The server listens on `:3000`. Expose `POST /webhook/github` and/or `POST /webhook/gitlab` to your forge; nothing else needs to be public. Then set up at least one [forge](#forges) and one [model provider](#models).

## How a review works

1. **Diff.** On open, the whole PR against its merge base; on a push, only what changed since the last review. Drafts, lockfiles, build output, generated code and binaries stay out. A pass gets up to 60k characters, code first, then tests, data and docs; larger PRs can be reviewed [in parts](#large-prs).
2. **Context.** The checks above, plus codegraph, co-change from git history, the repo's rules and skills from the base branch, learnings, and optionally the linked ticket.
3. **Review.** The agent returns up to 10 findings, then looks at the diff again for what it missed: the same session without tools, up to 10 more at about a tenth of the cost.
4. **Verify.** A cheaper model checks each finding in the code.
5. **Result.** A walkthrough comment, inline comments and a commit status `diffharbor`.

Two kinds of PRs are skipped with a green status unless someone asks for a review: PRs from bots (Dependabot, Renovate), and PRs into a branch other than the default branch or one in [`base_branches`](#per-repo-settings).

### Talking to the bot

| Comment | What happens |
|---|---|
| Reply in a bot thread | The bot answers, checks a claimed fix, or concedes and resolves the thread. |
| `@<bot> <question>` | An answer about the PR, from the checkout. |
| `@<bot> review` | A full review of the PR. |
| `@<bot> review all` | A large PR reviewed in parts. |

When a maintainer corrects the bot or states a convention, it saves a rule in `/data/learnings/` (per repo and per owner, the newest 30 each).

## Configuration

All settings are environment variables, set in `.env` next to `compose.yml`.

### Forges

<details open>
<summary><b>GitHub</b></summary>

Create a GitHub App:

- Webhook URL `https://<host>/webhook/github`, a random secret of 20+ characters.
- Permissions: Contents read/write (GitHub requires write to resolve threads; reviews get a read-only token), Pull requests read/write, Commit statuses read/write, Issues read, Metadata read.
- Events: Pull request, Pull request review comment, Issue comment.

| Variable | |
|---|---|
| `GITHUB_APP_ID` | The App's ID. |
| `GITHUB_APP_PRIVATE_KEY_B64` | The App's `.pem`, base64. |
| `GITHUB_WEBHOOK_SECRET` | The webhook secret. |

Mention the bot by the App's slug. Webhooks that got no 2xx (server down, queue full) are redelivered on start and every 30 minutes.

</details>

<details>
<summary><b>GitLab</b></summary>

Add a webhook on the project or group: `https://<host>/webhook/gitlab`, triggers Merge request events and Comments.

| Variable | |
|---|---|
| `GITLAB_URL` | Default `https://gitlab.com`. |
| `GITLAB_TOKEN` | A bot user's token (or a project/group access token) with scope `api` and at least the Developer role. Its username is the bot: `@<username> review`. |
| `GITLAB_READ_TOKEN` | Optional: a token with only `read_repository` for git's fetches, else they use the `api` token. Either stays in the server: git fetches through its proxy. |
| `GITLAB_WEBHOOK_SECRET` | The webhook's secret token. |

Differences to GitHub: findings are diff discussions, fixes `suggestion:-N+0` blocks, and a finding GitLab refuses to place goes into the summary note. Large MRs in parts start with `@<bot> review all` (there is no checkbox). There is no redelivery: GitLab retries a failed webhook only briefly. Maintainers count as members, Developers as collaborators.

</details>

### Models

Models are `provider/id[:level]` as Pi names them. Without settings the server picks by the keys present: the first provider reviews, the next one takes over for 30 minutes when the first fails (usage limit, outage).

| Provider (in this order) | Credentials | Review | Verify |
|---|---|---|---|
| OpenAI | `OPENAI_API_KEY` | `openai/gpt-6.1-terra:high` | `openai/gpt-6-luna:low` |
| Anthropic | `ANTHROPIC_API_KEY` | `anthropic/claude-sonnet-5-5:high` | `anthropic/claude-haiku-5-5:low` |
| DeepSeek | `DEEPSEEK_API_KEY` | `deepseek/deepseek-flash:high` | `deepseek/deepseek-flash:off` |
| AWS Bedrock | `AWS_REGION` and `AWS_BEARER_TOKEN_BEDROCK`, or `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY` | `amazon-bedrock/global.anthropic.claude-sonnet-5-5:high` | `amazon-bedrock/global.anthropic.claude-haiku-5-5:low` |
| Azure OpenAI | `AZURE_OPENAI_API_KEY` and `AZURE_OPENAI_BASE_URL` | `azure/gpt-6.1-sol:high` | `azure/gpt-6-luna:low` |

| Variable | |
|---|---|
| `REVIEW_MODEL`, `VERIFY_MODEL` | Pin the models. The env wins over a repo's `review.json`. |
| `FALLBACK_MODEL`, `FALLBACK_VERIFY_MODEL` | The fallback instead of the next provider. |
| `ANTHROPIC_BASE_URL`, `OPENAI_BASE_URL`, `DEEPSEEK_BASE_URL` | A gateway in front of the provider (LiteLLM, your company's AI gateway), with the gateway's key. |
| `AZURE_OPENAI_DEPLOYMENT_NAME_MAP` | Deployments not named like the models: `gpt-6.1-sol=review,gpt-6-luna=verify`. |

Bedrock in the EU: `amazon-bedrock/eu.anthropic.…`. Each pass gets only its own provider's key.

> [!NOTE]
> Use API keys. Pi can also log in with a ChatGPT subscription (`openai/…` without `OPENAI_API_KEY`, then `docker compose exec -it diffharbor diffharbor login`). Whether your plan's terms allow that for a review bot is yours to check, and you use it at your own risk.

### Optional

| Variable | |
|---|---|
| `REPORT_URL` | An HTML report per PR under `/r/<host>/<repo>/<n>/<secret>`, linked in the bot's PR comment: a verdict and the findings by weight, reading order, diffs with findings, call graphs, what the change reaches outside the PR, and before you merge, base commits the PR is behind on, conflicts, and open PRs changing the same files or lines, and at the end a draft review to send from the forge yourself: the page posts nothing. Each reviewed head keeps a version, the newest 10 stay; closing the PR deletes them. The secret is random per PR (130 bits) and only that comment holds it, so only who can read the PR has the link; any other path is a 404. **The report contains the PR's code: still put `/r/` behind your SSO, links get forwarded and stay in browser histories.** Reports from before the secret no longer open: the next review writes one at the new link, closing the PR deletes the old. |
| `REPORT_S3_URL`, `REPORT_S3_ACCESS_KEY`, `REPORT_S3_SECRET_KEY`, `REPORT_S3_REGION` | The reports in an S3 bucket instead of `DATA_DIR`: `https://host[:port]/bucket[/prefix]`, path-style, for SeaweedFS, MinIO, Garage, R2 or AWS. `/r/` still serves them, so the bucket stays private. The key needs `GetObject`, `PutObject` and `DeleteObject` on `bucket/prefix/*` and `ListBucket` on the bucket. Leave versioning off on it: there a delete only hides a report, its code stays. The region defaults to `us-east-1`. Only the server holds the key; checks and Pi never get it. On the first start with it, the reports on disk move into the bucket and their folder goes; a failed move retries on the next start. A delete that fails when a PR closes retries every 30 minutes. For your own S3, uncomment the `seaweedfs` service in `compose.example.yml` and set `REPORT_S3_URL=http://seaweedfs:8333/reports` with any key and secret: they become its admin, and the bucket comes with the first report. |
| `TEMPLATE_DIR` | Your branding for the report, see [Branding the report](#branding-the-report). |
| `ITSAPLAN_URL`, `ITSAPLAN_API_KEY` | The itsaplan ticket a PR names (`ABC-12` in title, branch or description) goes into the review as what the PR must do. Never for PRs from forks. |
| `LINEAR_API_KEY` | The same for Linear: a personal API key (read-only is enough). The ticket (`ENG-12`) is fetched by its identifier. |
| `TICKET_PROVIDER` | `itsaplan` or `linear`. Required when both are configured (the server doesn't start otherwise); with one configured it is that one. |
| `NPM_REGISTRIES` | The npm registries the checks' installs read from, such as your Artifactory's pull-through cache and internal repos: entries `<owner> [@scope] <url> <token>`, split by commas or newlines. `<owner>` is the repo's owner (GitLab: `gitlab/<group>`) or `*` for all; an owner's own entry beats `*` for its scope. The entry without a scope replaces the default registry (npmjs, also in lockfiles), one with `@corp` serves that scope. A token with a colon is `user:password`. Example: `* https://art.corp/artifactory/api/npm/npm-virtual/ <token>, team-a @team-a https://art.corp/artifactory/api/npm/team-a/ <token>`. As root (the image) the tokens stay in the server: installs read through its proxy (GETs only, no publish); lockfile URLs point at it during the install. Run as another user (`diffharbor dry`), the tokens go in the installs' env. Never for PRs from forks. Yarn Berry gets the default registry only, its scopes come from `.yarnrc.yml`. Use read-only tokens. |
| `DATA_DIR`, `CACHE_DIR` | Where the data (learnings, pending reviews, reports, logins, the repos' uids) and the per-repo caches live. `/data` and `/cache` in the image; keep both on volumes, apart. |
| `DOCKER_HOST` | With the `dind` service: Docker for testcontainers in the checks, isolated with [Sysbox](https://github.com/nestybox/sysbox) (install it on the host). Drop both if your tests don't need Docker. |

### Branding the report

The report page is the templates in [`internal/report/templates/`](internal/report/templates/). Files in `TEMPLATE_DIR` override them, like Django's template dirs:

- `logo.png` (64 px), `favicon.svg`, `favicon-32x32.png` replace the images.
- A `*.html` file that defines a template again replaces ours. A file with the name of one of ours (`header.html`) replaces all of that file. Files are read in name order, the last definition wins.

```html
<!-- $TEMPLATE_DIR/brand.html -->
{{define "theme"}}<style>:root { --primary: #0b5fff; --ring: #0b5fff; }</style>{{end}}
{{define "footer"}}<footer class="py-6 text-center text-xs text-muted-foreground">Acme Engineering</footer>{{end}}
```

Blocks meant for this: `theme` (the color tokens, see `theme.html`; `--major` and `--verified` color what to fix and what is shown without a model), `logo`, `head` (more in `<head>`), `header`, `footer`. The others work too but follow the page's data, which can change between versions; `finish` (`finish.html`) is the draft review at the end. The page's CSS is compiled with the classes its templates use; with your templates, DiffHarbor compiles it again at start with theirs (Tailwind's CLI is in the image). DiffHarbor checks the templates at start and doesn't start with a broken one. Reports already rendered keep their look.

## Per-repo settings

Everything lives under `.review/`; `review.json` can change the paths.

| File | |
|---|---|
| `.review/rules.md` | Rules in prose for the prompt. Without it, `AGENTS.md` or `CLAUDE.md`. |
| `.review/skills/` | Review skills in the [Agent Skills format](https://agentskills.io/specification). On GitHub, skills in `<owner>/.github` under `.review/skills/` apply to all of the owner's repos. |
| `.review/checks.sh` | Your own checks from the base branch, run in the PR checkout with the changed files after `--`. Their output (120 lines each) goes into the prompt; the exit code doesn't count. |
| `review.json` | Settings, in the repo root. All keys are optional. |

<details>
<summary><b><code>review.json</code> reference</b></summary>

```json
{
  "ignore": ["drizzle/meta", "locales/*.json", "**/*.fixture.ts"],
  "min_severity": "minor",
  "tests": true,
  "review_model": "openai/gpt-6.1-sol",
  "verify_model": "anthropic/claude-haiku-5-5:low",
  "large_pr": "all",
  "max_parts": 4,
  "report": "auto",
  "tickets": ["ABC"],
  "install_scripts": false,
  "base_branches": ["develop", "release/*"],
  "checks": [".review/checks.sh"],
  "packages": {
    "apps/web": {"min_severity": "major"},
    "services/*": {"tests": false}
  }
}
```

| Key | |
|---|---|
| `ignore` | Globs; a path without one takes its folder. Neither the model nor a check sees these files. |
| `min_severity` | `minor`, `major` or `critical`: findings below are dropped. |
| `tests` | Run the tests of changed packages. Unset, they run only when no CI workflow does. |
| `review_model`, `verify_model` | Apply where the server sets none; the provider's key must be on the server. |
| `large_pr`, `max_parts` | See [Large PRs](#large-prs). |
| `report` | `auto` (from 10 changed files), `always` or `off`. |
| `tickets` | The itsaplan or Linear projects (team keys) a PR may name. |
| `install_scripts` | `true` runs lifecycle scripts (`postinstall`, native builds) in the checks' installs. Off by default: a dependency's script would run next to the PR's code. |
| `base_branches` | Globs of base branches besides the default branch whose PRs get reviewed on their own. A PR into any other (`main` → `prod`) is skipped with a green status; `@<bot> review` still reviews it. |
| `rules`, `skills`, `checks` | Other paths for the files above. |
| `syntax_theme` | The report's code colors: `{"light": "…", "dark": "…"}`, [Shiki theme](https://shiki.style/themes) names. |
| `packages` | Overrides per monorepo part; the longest glob wins. |

A broken `review.json` falls back to the defaults and says so in the review footer.

</details>

### Large PRs

A diff over 60k characters can be reviewed in parts: the files of a folder stay together, each part gets its own review and verify pass, and the findings are merged, most severe first. Each part costs about one normal review.

| `large_pr` | |
|---|---|
| `all` (default) | Always in parts. |
| `ask` | Code up to 60k first. On GitHub the walkthrough offers a checkbox to review all files in parts; on both forges `@<bot> review all` does the same. Later pushes keep reviewing in parts. |
| `off` | Never. |

`max_parts`: default 4, at most 8.

### Monorepos

Checks run per package (the nearest `package.json` or `go.mod`, at most 5 packages) and install once per lockfile. The `AGENTS.md` (else `CLAUDE.md`) files in the folders above the changed files are added to the rules, at most 5.

## Security

> [!WARNING]
> The checks and the agent run the PR's code, also for PRs from forks. Install the bot only on repos whose PR authors you trust, such as your team.

Within that:

- Root serves and holds the forge keys, the model keys and Pi's login. git, checks and Pi run as a user of the repo's own (one uid per repo) and, but for the cases below, get none of them: git fetches and Pi calls its model through a proxy in the server on `127.0.0.1`, with random tokens that the server revokes when the Pi run (git: the job) ends. A token reaches only its provider's model API or its job's repo (fetches, no push); the proxy adds the key there.
- Root reads and writes in the checkout only as that user: a symlink in the PR can't reach root's files.
- All processes of the repos' users are killed before and after each job. Each job gets a fresh home (git config, Pi's config and extensions) and temp dir; the users' files in `/tmp`, `/var/tmp` and `/dev/shm` go before each job. Check output is capped.
- The checks install dependencies without lifecycle scripts, so a malicious dependency's postinstall doesn't run; `install_scripts` in `review.json` turns them back on per repo.
- `NPM_REGISTRIES` tokens stay in the server like the model keys: the installs read through its proxy, GETs only, with tokens that end with the checks. During that time the PR's code can read the registries' packages too (same uid), so a PR from a fork gets none.
- Each repo has its own cache (`/cache/<uid>`: Go build cache, module and pnpm stores) that other repos' users can't read or write, so a PR can't change what another repo's jobs run. It can change what later jobs of its own repo run, its other PRs' too: the cache stays. Still shared: the container's network and its limits below. `diffharbor dry` runs as `node`.
- A repo's uid is keyed by its forge, the forge's host and the repo's ID in `DATA_DIR/uids.json`, never its name: a renamed repo keeps its uid and cache, a new repo under an old name gets its own. Upgrading from a version that keyed by name: who owned a name-keyed entry can't be told, so it stays in the file to keep its uid taken (never handed out again), and the server drops its cache at startup. Each repo gets a new uid and an empty cache on its next job, so the first installs and builds are cold. The file isn't rewritten, so an interrupted upgrade or a repeated start changes nothing. A broken `uids.json`, or two entries sharing a uid, still stops the server.
- PR code shares the container's network: it reaches `localhost:3000/r/` (no SSO there, it is in front of the proxy only, but a report opens only by the secret in its link, which only that PR's comment holds) and, on a cloud VM, the metadata endpoint. Block `169.254.169.254` for the container there.
- During its job the PR's code can use those tokens too (same uid): spend model tokens, fetch the repo it already has. It can't keep a key. Pi still gets the credential itself, readable by the PR's code during the run, for a ChatGPT login (an access token that outlives the run, never the refresh token; Pi sends it only to OpenAI directly), Bedrock with IAM keys (SigV4 signs the host; `AWS_BEARER_TOKEN_BEDROCK` goes through the proxy) and a provider outside the models table. A gateway that adds the key itself avoids that.
- No new privileges (a setuid binary can't make a repo's user root) and only the capabilities root needs. Limits: 3 GB RAM, 2 CPUs and 4096 processes per container. A Pi run gets 8 minutes; [`extensions/review-guard.js`](extensions/review-guard.js) refuses further tool calls after 6 minutes or at 60% context, so the pass still answers.

## Development

```sh
go test ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...
```

`diffharbor dry` in a checkout reviews `git diff HEAD` and prints the findings as JSON.

## Contributing

This repository does not take external contributions at the moment. Bug reports as issues are welcome.

## License

Community Edition under the [Functional Source License, FSL-1.1-ALv2](LICENSE.md): use it, change it, run it for your company. You may not offer it as a competing commercial product or service. Each version becomes Apache-2.0 two years after its release.
