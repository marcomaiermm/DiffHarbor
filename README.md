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

A small Go server takes the webhook and runs your checks on the changed lines. An agent ([Pi](https://github.com/earendil-works/pi)) reviews the diff in a checkout with read, grep and bash. A second model verifies every finding against the code. What survives is posted as inline comments with committable suggestions.

## Highlights

- **Verified findings.** A second, cheaper model checks each finding in the code and drops what it can't back up.
- **Your own checks as context.** gitleaks, tsc, biome, eslint, oxlint, gofmt, vet, staticcheck, golangci-lint, deadcode, fallow, squawk, shellcheck, luacheck, actionlint, zizmor, osv-scanner and the tests of changed packages, on the changed lines.
- **Incremental.** A push reviews only files whose changed lines changed. A rebase or a merge of main reviews nothing.
- **Committable fixes.** A local fix becomes a suggestion block. Fixed findings get their threads resolved.
- **Chat and learnings.** Reply to a finding or mention the bot. Corrections become rules for the next reviews.
- **Any model Pi supports.** OpenAI, Anthropic, DeepSeek, AWS Bedrock, Azure OpenAI or a gateway in front of them, with automatic fallback.
- **PR reports.** An optional HTML page per PR with a reading order, diffs with findings, call graphs and merge risks.
- **Small and self-hosted.** One container, Go standard library only. Your code stays on your server and with your model provider.

## Quick start

```sh
cp compose.example.yml compose.yml   # the values go into .env next to it
docker compose up -d
```

1. The server listens on `:3000`.
2. Expose `POST /webhook/github` and/or `POST /webhook/gitlab` to your forge. Nothing else needs to be public.
3. Set up at least one [forge](#forges) and one [model provider](#models).

## How a review works

1. **Diff.** On open, the whole PR against its merge base. On a push, only what changed since the last review.
2. **Context.** The checks above, codegraph, co-change from git history, the PR's commit messages, the repo's rules and skills from the base branch, learnings and optionally the linked ticket.
3. **Review.** The agent returns up to 10 findings. Then it looks at the diff again in the same session without tools: up to 10 more at about a tenth of the cost. [`passes`](#passes) add up to two more finders.
4. **Verify.** A cheaper model checks each finding in the code.
   For Go, the test the report names as proof runs against the base versions of the PR's code files. If it still passes there, that is a minor finding.
5. **Result.** A walkthrough comment, inline comments and a commit status `diffharbor`.

**What the review reads:**

- Up to 60k characters per pass: code first, then tests, data, stylesheets and docs. Larger PRs can be reviewed [in parts](#large-prs).
- Left out: lockfiles, build output, vendored code, binaries and generated code (by name, Drizzle's migration snapshots or a "Code generated … DO NOT EDIT" header).

**Drafts** aren't reviewed until they are marked ready, unless someone asks with `@<bot> review`; [`drafts`](#per-repo-settings) can also review a draft's pushes once asked, or never.

**Skipped PRs** get a green status. `@<bot> review` still reviews them.

- PRs from bots: Dependabot, Renovate, also a self-hosted one on a user token. Its branch starts with `renovate/` or `dependabot/`, or Renovate's hidden comments are in the description.
- PRs into a branch other than the default branch or one in [`base_branches`](#per-repo-settings).

### Talking to the bot

| Comment | What happens |
|---|---|
| Reply in a bot thread | The bot answers, checks a claimed fix, or concedes and resolves the thread. |
| `@<bot> <question>` | An answer about the PR, from the checkout. |
| `@<bot> review` | A full review of the PR. |
| `@<bot> review all` | A large PR reviewed in parts. |
| `@<bot> review quiet` | A full review with another profile: `quiet`, `chill` or `assertive`, also after `all`. |
| `@<bot> learnings` | Lists the saved learnings of the repo and its owner. |
| `@<bot> forget <text>` | Drops the learnings containing the text. Only owners and members drop the owner's. |

When a maintainer corrects the bot or states a convention, the bot saves a dated rule in `/data/learnings/`. It keeps the newest 100 per repo and per owner. A review follows the newest 30 of each. [`learnings`](#per-repo-settings) in `review.json` changes that.

## Configuration

All settings are environment variables in `.env` next to `compose.yml`.

### Forges

<details open>
<summary><b>GitHub</b></summary>

Create a GitHub App:

- **Webhook URL:** `https://<host>/webhook/github`, with a random secret of 20+ characters.
- **Permissions:**
  - Contents: read/write. GitHub needs write to resolve threads. Reviews get a read-only token.
  - Pull requests: read/write
  - Commit statuses: read/write
  - Issues and Metadata: read
- **Events:** Pull request, Pull request review comment, Issue comment.

| Variable | |
|---|---|
| `GITHUB_APP_ID` | The App's ID. |
| `GITHUB_APP_PRIVATE_KEY_B64` | The App's `.pem`, base64. |
| `GITHUB_WEBHOOK_SECRET` | The webhook secret. |

- Mention the bot by the App's slug.
- Webhooks that got no 2xx (server down, queue full) are redelivered on start and every 30 minutes.

</details>

<details>
<summary><b>GitLab</b></summary>

Add a webhook on the project or group: `https://<host>/webhook/gitlab`, with the triggers Merge request events and Comments.

| Variable | |
|---|---|
| `GITLAB_URL` | Default `https://gitlab.com`. |
| `GITLAB_TOKEN` | A bot user's token (or a project/group access token) with scope `api` and at least the Developer role. Its username is the bot: `@<username> review`. |
| `GITLAB_READ_TOKEN` | Optional. A token with only `read_repository` for git's fetches. Without it they use the `api` token. Both stay in the server: git fetches through its proxy. |
| `GITLAB_WEBHOOK_SECRET` | The webhook's secret token. |
| `GITLAB_FINDINGS` | `review` (default): the findings, the answers to fixed threads and the summary are one review, one email; a suggestion GitLab can't place goes into the summary as code. `discussions`: each on its own, an email each; a refused suggestion is retried as plain code on its line. Drafts a person has on the token's user when a review starts make it fall back to `discussions`; ones drafted during it go out with it, so give the bot an account of its own. A repo's `review.json` can say otherwise (`gitlab_findings`). |

Differences to GitHub:

- Findings are diff discussions, published as one review (`GITLAB_FINDINGS`), and fixes are `suggestion:-N+0` blocks.
- A finding GitLab refuses to place goes into the summary note.
- Large MRs in parts start with `@<bot> review all`. There is no checkbox.
- No redelivery. GitLab retries a failed webhook only briefly.
- Maintainers count as members, Developers as collaborators.

</details>

### Models

- Models are `provider/id[:level]` as Pi names them.
- Without settings the server picks by the keys present. The first provider reviews. The next one takes over for 30 minutes when the first fails (usage limit, outage).

| Provider (in this order) | Credentials | Review | Verify |
|---|---|---|---|
| OpenAI | `OPENAI_API_KEY` | `openai/gpt-6.1-terra:high` | `openai/gpt-6-luna:high` |
| Anthropic | `ANTHROPIC_API_KEY` | `anthropic/claude-sonnet-5-5:high` | `anthropic/claude-haiku-5-5:low` |
| DeepSeek | `DEEPSEEK_API_KEY` | `deepseek/deepseek-flash:high` | `deepseek/deepseek-flash:off` |
| AWS Bedrock | `AWS_REGION` and `AWS_BEARER_TOKEN_BEDROCK`, or `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY` | `amazon-bedrock/global.anthropic.claude-sonnet-5-5:high` | `amazon-bedrock/global.anthropic.claude-haiku-5-5:low` |
| Azure OpenAI | `AZURE_OPENAI_API_KEY` and `AZURE_OPENAI_BASE_URL` | `azure/gpt-6.1-sol:high` | `azure/gpt-6-luna:high` |

| Variable | |
|---|---|
| `REVIEW_MODEL`, `VERIFY_MODEL` | Pin the models. The env wins over a repo's `review.json`. |
| `FALLBACK_MODEL`, `FALLBACK_VERIFY_MODEL` | The fallback instead of the next provider. |
| `ANTHROPIC_BASE_URL`, `OPENAI_BASE_URL`, `DEEPSEEK_BASE_URL` | A gateway in front of the provider (LiteLLM, your company's AI gateway), with the gateway's key. |
| `AZURE_OPENAI_DEPLOYMENT_NAME_MAP` | Deployments not named like the models: `gpt-6.1-sol=review,gpt-6-luna=verify`. |

- Bedrock in the EU: `amazon-bedrock/eu.anthropic.…`.
- Each pass gets only its own provider's key.

> [!NOTE]
> Use API keys. Pi can also log in with a ChatGPT subscription: `openai/…` without `OPENAI_API_KEY`, then `docker compose exec -it diffharbor diffharbor login`. Whether your plan's terms allow that for a review bot is yours to check. You use it at your own risk.

### Report

`REPORT_URL` turns on an HTML page per PR, linked in the bot's PR comment.

- **What's on it:** a verdict, the findings by weight, a reading order, diffs with findings, call graphs and what the change reaches outside the PR. Before you merge: base commits the PR is behind on, conflicts, and open PRs changing the same files or lines. At the end a draft review to send from the forge yourself. The page posts nothing.
- **Versions:** each reviewed head keeps one, the newest 10 stay. Closing the PR deletes them.
- **Link:** `/r/<host>/id:<repo id>/<n>/<secret>`, by the repo's ID: a rename keeps it, a new repo under an old name gets its own. Links from before the ID (`/r/<host>/<repo>/<n>/<secret>`) keep working until the PR closes. The secret is random per PR (130 bits) and only the PR comment holds it. Any other `/r/` path is a 404.
- **Older reports:** reports from before the secret no longer open. The next review writes one at the new link, closing the PR deletes the old.
- **Reviewing banner:** while a review runs, the PR comment shows `/reviewing.svg` (and `-light`, `-dark` for GitHub's theme setting) from this server. Keep them public: GitHub fetches them through its image proxy. Without `REPORT_URL` it comes from the docs site.

> [!WARNING]
> The report contains the PR's code. Put `/r/` behind your SSO anyway: links get forwarded and stay in browser histories.

**Reports in S3** instead of `DATA_DIR`:

| Variable | |
|---|---|
| `REPORT_S3_URL` | `https://host[:port]/bucket[/prefix]`, path-style. SeaweedFS, MinIO, Garage, R2 or AWS. |
| `REPORT_S3_ACCESS_KEY`, `REPORT_S3_SECRET_KEY` | Needs `GetObject`, `PutObject` and `DeleteObject` on `bucket/prefix/*` and `ListBucket` on the bucket. |
| `REPORT_S3_REGION` | Default `us-east-1`. |

- `/r/` still serves the reports, so the bucket stays private.
- Only the server holds the key. Checks and Pi never get it.
- Leave versioning off. With it, a delete only hides a report and its code stays.
- On the first start with S3, the reports on disk move into the bucket and their folder goes. A failed move retries on the next start.
- A delete that fails when a PR closes retries every 30 minutes.
- Your own S3: uncomment the `seaweedfs` service in `compose.example.yml` and set `REPORT_S3_URL=http://seaweedfs:8333/reports` with any key and secret. They become its admin. The bucket comes with the first report.

**Branding:** `TEMPLATE_DIR`, see [Branding the report](#branding-the-report).

### Tickets

The ticket a PR names goes into the review as what the PR must do.

| Variable | |
|---|---|
| `ITSAPLAN_URL`, `ITSAPLAN_API_KEY` | itsaplan. |
| `LINEAR_API_KEY` | Linear. A personal API key, read-only is enough. |
| `TICKET_PROVIDER` | `itsaplan` or `linear`. Required when both are configured, else the server doesn't start. |

- The main ticket is named in title, branch or description: `ABC-12`.
- Up to 3 more it names (`closes ABC-7`, a link in the description) follow as context.
- Never for PRs from forks.

### npm registries

`NPM_REGISTRIES` sets the registries the checks' installs read from, such as your Artifactory's pull-through cache and internal repos.

```text
* https://art.corp/artifactory/api/npm/npm-virtual/ <token>,
team-a @team-a https://art.corp/artifactory/api/npm/team-a/ <token>
```

- **Entries:** `<owner> [@scope] <url> <token>`, split by commas or newlines. A token with a colon is `user:password`.
- **Owner:** the repo's owner (GitLab: `gitlab/<group>`) or `*` for all. An owner's own entry beats `*` for its scope.
- **Scope:** an entry without one replaces the default registry (npmjs, also in lockfiles). One with `@corp` serves that scope.
- **Tokens:** as root (the image) they stay in the server. Installs read through its proxy (GETs only, no publish) and lockfile URLs point at it during the install. Run as another user (`diffharbor dry`), the tokens go in the installs' env.
- Never for PRs from forks.
- Yarn Berry gets the default registry only. Its scopes come from `.yarnrc.yml`.
- Use read-only tokens.

### Storage and Docker

| Variable | |
|---|---|
| `DATA_DIR`, `CACHE_DIR` | Data (learnings, pending reviews, reports, logins, the repos' uids) and per-repo caches. `/data` and `/cache` in the image. Keep both on volumes, apart. |
| `MAX_JOBS` | How many jobs (reviews, answers) run at once. Default `1`. Jobs of one repo always run one after another, so a higher value helps when several repos have PRs at the same time. Raise `mem_limit`, `cpus` and `pids_limit` with it. Above `1`, jobs of different repos share `/tmp`, `/var/tmp`, `/dev/shm`, the loopback network, the process list and the container's limits while they run, see [Jobs at once](#jobs-at-once). |
| `DOCKER_HOST` | With the `dind` service: Docker for testcontainers in the checks, isolated with [Sysbox](https://github.com/nestybox/sysbox) (install it on the host). Drop both if your tests don't need Docker. |

### Branding the report

The report page is the templates in [`internal/report/templates/`](internal/report/templates/). Files in `TEMPLATE_DIR` override them, like Django's template dirs:

- `logo.png` (64 px), `favicon.svg` and `favicon-32x32.png` replace the images.
- A `*.html` file that defines a template again replaces ours.
- A file named like one of ours (`header.html`) replaces all of that file.
- Files are read in name order. The last definition wins.

```html
<!-- $TEMPLATE_DIR/brand.html -->
{{define "theme"}}<style>:root { --primary: #0b5fff; --ring: #0b5fff; }</style>{{end}}
{{define "footer"}}<footer class="py-6 text-center text-xs text-muted-foreground">Acme Engineering</footer>{{end}}
```

**Blocks meant for this:**

- `theme`: the color tokens, see `theme.html`. `--major` colors what to fix, `--verified` what is shown without a model.
- `logo`, `head` (more in `<head>`), `header`, `footer`.

The others work too but follow the page's data, which can change between versions. `finish` (`finish.html`) is the draft review at the end.

- The page's CSS is compiled with the classes its templates use. With your templates, DiffHarbor compiles it again at start (Tailwind's CLI is in the image).
- DiffHarbor checks the templates at start and doesn't start with a broken one.
- Reports already rendered keep their look.

## Per-repo settings

Everything lives under `.review/`. `review.json` can change the paths.

| File | |
|---|---|
| `.review/rules.md` | Rules in prose for the prompt. Without it, `AGENTS.md` or `CLAUDE.md`. |
| `.review/skills/` | Review skills in the [Agent Skills format](https://agentskills.io/specification). On GitHub, skills in `<owner>/.github` under `.review/skills/` apply to all of the owner's repos. |
| `.review/checks.sh` | Your own checks from the base branch, run in the PR checkout with the changed files after `--`. Their output (120 lines each) goes into the prompt. The exit code doesn't count. |
| `review.json` | Settings, in the repo root. All keys are optional. |

<details>
<summary><b><code>review.json</code> reference</b></summary>

```json
{
  "ignore": ["drizzle/meta", "locales/*.json", "**/*.fixture.ts"],
  "profile": "chill",
  "tests": true,
  "review_model": "openai/gpt-6.1-sol",
  "verify_model": "anthropic/claude-haiku-5-5:low",
  "large_pr": "all",
  "max_parts": 4,
  "passes": [{}, {"max_lines": 1500}],
  "report": "auto",
  "suggest_split": {"lines": 300, "files": 20},
  "tickets": ["ABC"],
  "install_scripts": false,
  "base_branches": ["develop", "release/*"],
  "drafts": "manual",
  "learnings": {"max": 30, "max_age_days": 180},
  "gitlab_findings": "review",
  "checks": [".review/checks.sh"],
  "packages": {
    "apps/web": {"min_severity": "major", "profile": "quiet"},
    "services/*": {"tests": false}
  }
}
```

| Key | |
|---|---|
| `ignore` | Globs. A path without one takes its folder. `!glob` takes paths back in: `["src/ui", "!src/ui/{chip,toast}.tsx"]`. Neither the model nor a check sees these files. |
| `profile` | `quiet`, `chill` (default) or `assertive`: which findings go inline and which fold into the review's summary. See [Review profiles](#review-profiles). |
| `min_severity` | `minor`, `major` or `critical`: findings below are dropped before verification, nowhere posted. `minor` drops the nits. |
| `tests` | Run the tests of changed packages. Unset, they run only when no CI workflow does. |
| `review_model`, `verify_model` | Apply where the server sets none. The provider's key must be on the server. |
| `large_pr`, `max_parts` | See [Large PRs](#large-prs). |
| `passes` | Finders per part, at most 3. See [Passes](#passes). |
| `report` | `auto` (from 10 changed files), `always` or `off`. |
| `suggest_split` | See [Split suggestion](#split-suggestion). |
| `tickets` | The itsaplan or Linear projects (team keys) a PR may name. |
| `install_scripts` | `true` runs lifecycle scripts (`postinstall`, native builds) in the checks' installs. Off by default: a dependency's script would run next to the PR's code. |
| `base_branches` | Globs of base branches whose PRs get reviewed, besides the default branch. A PR into any other (`main` → `prod`) is skipped with a green status. `@<bot> review` still reviews it. |
| `drafts` | `manual` (default): a draft PR gets a review on `@<bot> review`, then once marked ready. `opt_in`: once someone asked with `@<bot> review`, every later push of the draft gets one too, like any PR. `off`: never; `@<bot> review` says so. |
| `learnings` | `max`: how many of the newest learnings of the repo and of the owner a review and the chat follow, each. `0` is none, at most 100, default 30. `max_age_days`: older ones are left out. Default: any age. A learning saved before dates were kept never ages. Both only hide, nothing is deleted. |
| `gitlab_findings` | `review`: the findings, the answers to fixed threads and the summary are one review, one email; a suggestion GitLab can't place goes into the summary as code. `discussions`: each on its own, an email each; a refused suggestion is retried as plain code on its line. Default: the server's `GITLAB_FINDINGS`. |
| `rules`, `skills`, `checks` | Other paths for the files above. |
| `syntax_theme` | The report's code colors: `{"light": "…", "dark": "…"}`, [Shiki theme](https://shiki.style/themes) names. |
| `packages` | Overrides per monorepo part. The longest glob wins. |

- For validation and completion in your editor, add `"$schema": "https://marcomaiermm.github.io/DiffHarbor/review.schema.json"` as the first key. The server ignores it.
- A broken `review.json` falls back to the defaults and says so in the review footer.

</details>

### Review profiles

Reviews can report defects (critical, major, minor) and, unless `min_severity` drops them, nits: naming, readability, a dead branch, an idiom of the codebase the change ignores, at most 5 per pass and never in place of a defect. The profile decides what goes inline as a thread and what folds into the review's summary, where nothing is lost and the prompt for AI agents still has it. Inline means on a diff line: a finding outside the diff goes into the summary whatever the profile.

| `profile` | Inline | Folded into the summary |
|---|---|---|
| `quiet` | critical, major | minor, nits |
| `chill` (default) | critical, major, minor | nits |
| `assertive` | everything, nits too | |

`min_severity` drops findings before that, `minor` the nits. A folded finding has no thread: a later review of its file may report it again, and an incremental review that finds only folded ones posts nothing.

The first that sets a profile wins:

1. `@<bot> review quiet` (also `review all assertive`): this review only.
2. The PR description: a line `review: quiet`, typed by hand or a ticked box of the PR template. Unticked boxes and HTML comments don't count, so a template can offer all three:

   ```markdown
   - [ ] review: quiet
   - [x] review: chill
   - [ ] review: assertive
   ```

3. `packages` in `review.json`, by path.
4. `profile` in `review.json`.

The review footer names a profile set by the comment or the description.

### Large PRs

A diff over 60k characters can be reviewed in parts. Each part costs about one normal review.

- The files of a folder stay together.
- A file over 60k is cut at its hunks, a hunk over 60k at its lines.
- Each part gets its own review and verify pass. The findings are merged, most severe first.

| `large_pr` | |
|---|---|
| `all` (default) | Always in parts. |
| `ask` | Code up to 60k first. On GitHub the walkthrough offers a checkbox to review all files in parts. On both forges `@<bot> review all` does the same. Later pushes keep reviewing in parts. |
| `off` | Never. |

`max_parts`: default 4, at most 8.

### Passes

Each pass is a finder with tools, followed by its second look. One verify pass checks the findings of all of them. Without `passes` there is one, the review model's.

```json
"passes": [{}, {"max_lines": 1500}]
```

| Key | |
|---|---|
| `model` | Unset: the second model of the review model's provider, e.g. `openai/gpt-6-luna:high`. A server that sets `REVIEW_MODEL` sets this one too. |
| `again` | `false`: no second look. |
| `min_lines`, `max_lines` | Run only on PRs with that many changed lines in the reviewed files. |

- The first entry is the review model's and always runs. It takes `again` only.
- A further pass reads the files in reverse order.
- The verify pass drops a finding that another pass reported too.

Measured on four PRs of this repo with 41 known defects, per run:

| Second pass | More defects found | More cost |
|---|---|---|
| `openai/gpt-6-luna:high` | about 1.5 | 14% |
| `openai/gpt-6.1-terra:medium` | about 3 | 80% |

Mostly they find the same defects the first pass found.

### Split suggestion

The AI suggests splitting a big PR into PRs that could each merge on their own. Off by default.

```json
"suggest_split": {"lines": 300, "files": 20}
```

| Key | |
|---|---|
| `lines` | Changed lines from which it suggests. |
| `files` | Changed files from which it suggests. |
| `count` | Which files count, by class: `code`, `test`, `data` (JSON, YAML, TOML, CSV, XML), `bulk` (stylesheets, notebooks, translations, seeds), `doc`. Default `["code"]`. |
| `skip` | Globs, like `ignore`, of files that don't count. `"!glob"` counts a file whatever its class. |

- **Either one is enough**: 400 lines in 3 files or 25 files with 100 lines both get a suggestion.
- **Only code counts by default**: thorough tests, fixtures or docs are no reason to split.
- Lockfiles, generated, ignored and deleted files never count.

```json
"suggest_split": {"lines": 300, "count": ["code", "test"], "skip": ["migrations/**", "!deploy/**/*.yaml"]}
```

### Monorepos

- Checks run per package: the nearest `package.json` or `go.mod`, at most 5 packages. They install once per lockfile.
- The `AGENTS.md` (else `CLAUDE.md`) files in the folders above the changed files are added to the rules, at most 5.

## Security

> [!WARNING]
> The checks and the agent run the PR's code, also for PRs from forks. Install the bot only on repos whose PR authors you trust, such as your team.

Within that:

### Keys stay with root

- Root serves and holds the forge keys, the model keys and Pi's login.
- git, checks and Pi run as a user of the repo's own (one uid per repo). They get none of the keys, except for the cases under [What the PR's code can reach](#what-the-prs-code-can-reach).
- git fetches and Pi's model calls go through a proxy in the server on `127.0.0.1`. They use random tokens that the server revokes when the Pi run (git: the job) ends.
- A token reaches only its provider's model API or its job's repo (fetches, no push). The proxy adds the key there.
- `NPM_REGISTRIES` tokens work the same way: installs read through the proxy, GETs only, with tokens that end with the checks.

### Each job

- Root reads and writes in the checkout only as the repo's user. A symlink in the PR can't reach root's files.
- All processes of the repo's user are killed before and after each job, those of all repos' users when the server starts.
- Each job gets a fresh home (git config, Pi's config and extensions) and temp dir. The repo's user's files in `/tmp`, `/var/tmp` and `/dev/shm` go before and after each job, all repos' users' when the server starts.
- Check output is capped.
- The checks install dependencies without lifecycle scripts, so a malicious dependency's postinstall doesn't run. `install_scripts` in `review.json` turns them back on per repo.
- No new privileges: a setuid binary can't make a repo's user root. Root keeps only the capabilities it needs.
- Limits per container: 3 GB RAM, 2 CPUs, 4096 processes.
- A Pi run gets 8 minutes. [`extensions/review-guard.js`](extensions/review-guard.js) refuses further tool calls after 6 minutes or at 60% context, so the pass still answers.

### Jobs at once

- A repo's jobs run one after another: they share its user, which could otherwise signal or read the other job's processes.
- With `MAX_JOBS` above `1`, jobs of different repos run at the same time. Home, temp dir (`TMPDIR`), caches and uid stay per repo. Shared while they run:
  - `/tmp`, `/var/tmp` and `/dev/shm`: a PR's code could leave a file for another repo's job.
  - The loopback network, abstract unix sockets included: it could talk to a port or socket another job listens on.
  - The process list, arguments included. Tokens, prompts, rules and learnings stay out of it (git's token goes in a header from a file, npm's and the models' in the env, prompts in files and stdin), unless a repo's own scripts put them in a command's arguments. The changed files' paths, commit SHAs, test names and the bumped npm packages' names and versions are in it.
  - The container's memory, CPUs and processes: one PR's memory hog or fork bomb can starve the other jobs or get them killed. Raise `mem_limit`, `cpus` and `pids_limit` with `MAX_JOBS`.
- That's fine when all repos belong to one team. Keep `MAX_JOBS=1` (the default) when untrusted repos or forks of different owners are reviewed side by side.

### Each repo

- Each repo has its own cache (`/cache/<uid>`: Go build cache, module and pnpm stores). Other repos' users can't read or write it, so a PR can't change what another repo's jobs run.
- A PR can change what later jobs of its own repo run, its other PRs' too. The cache stays.
- `diffharbor dry` runs as `node`.
- A repo's uid is keyed by its forge, the forge's host and the repo's ID in `DATA_DIR/uids.json`, never its name. A renamed repo keeps its uid and cache. A new repo under an old name gets its own.
- A broken `uids.json`, or two entries sharing a uid, stops the server.

<details>
<summary>Upgrading from a version that keyed uids by name</summary>

- Who owned a name-keyed entry can't be told. It stays in the file to keep its uid taken (never handed out again), and the server drops its cache at startup.
- Each repo gets a new uid and an empty cache on its next job, so the first installs and builds are cold.
- The file isn't rewritten, so an interrupted upgrade or a repeated start changes nothing.

</details>

### What the PR's code can reach

- **The container's network** and its limits:
  - `localhost:3000/r/`. There is no SSO there, it is in front of the proxy only. A report still opens only by the secret in its link, which only that PR's comment holds.
  - On a cloud VM, the metadata endpoint. Block `169.254.169.254` for the container there.
- **The job's tokens** (same uid): it can spend model tokens and fetch the repo it already has. It can't keep a key.
- **The registries' packages** while the checks install. So a PR from a fork gets no `NPM_REGISTRIES`.
- **Credentials Pi gets itself**, readable by the PR's code during the run:
  - A ChatGPT login: an access token that outlives the run, never the refresh token. Pi sends it only to OpenAI directly.
  - Bedrock with IAM keys, since SigV4 signs the host. `AWS_BEARER_TOKEN_BEDROCK` goes through the proxy.
  - A provider outside the models table.

  A gateway that adds the key itself avoids that.

## Development

```sh
go test ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...
```

- `diffharbor dry` in a checkout reviews `git diff HEAD` and prints the findings as JSON.
- The report's CSS is compiled. After changing `internal/report/styles.css` or a class in a template, run `go generate ./internal/report`. It needs [Tailwind's standalone CLI](https://github.com/tailwindlabs/tailwindcss/releases/tag/v4.3.4) 4.3.4 as `tailwindcss` on the `PATH`. The npm package resolves `tailwindcss` from whatever `node_modules` is nearby. The Docker build fails while `styles.gen.css` is stale.

## Contributing

This repository does not take external contributions at the moment. Bug reports as issues are welcome.

## License

Community Edition under the [Functional Source License, FSL-1.1-ALv2](LICENSE.md): use it, change it, run it for your company. You may not offer it as a competing commercial product or service. Each version becomes Apache-2.0 two years after its release.
