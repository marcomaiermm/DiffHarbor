---
title: Configuration
description: Server settings as environment variables.
---

All settings are environment variables in `.env` next to `compose.yml`.

## Forges

### GitHub

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

### GitLab

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

## Models

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

:::note
Use API keys. Pi can also log in with a ChatGPT subscription: `openai/…` without `OPENAI_API_KEY`, then `docker compose exec -it diffharbor diffharbor login`. Whether your plan's terms allow that for a review bot is yours to check. You use it at your own risk.
:::

## Report

`REPORT_URL` turns on an HTML page per PR, linked in the bot's PR comment.

- **What's on it:** a verdict, the findings by weight, a reading order, diffs with findings, call graphs and what the change reaches outside the PR. Before you merge: base commits the PR is behind on, conflicts, and open PRs changing the same files or lines. At the end a draft review to send from the forge yourself. The page posts nothing.
- **Versions:** each reviewed head keeps one, the newest 10 stay. Closing the PR deletes them.
- **Link:** `/r/<host>/<repo>/<n>/<secret>`. The secret is random per PR (130 bits) and only the PR comment holds it. Any other `/r/` path is a 404.
- **Older reports:** reports from before the secret no longer open. The next review writes one at the new link, closing the PR deletes the old.
- **Reviewing banner:** while a review runs, the PR comment shows `/reviewing.svg` (and `-light`, `-dark` for GitHub's theme setting) from this server. Keep them public: GitHub fetches them through its image proxy. Without `REPORT_URL` it comes from the docs site.

:::caution
The report contains the PR's code. Put `/r/` behind your SSO anyway: links get forwarded and stay in browser histories.
:::

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

## Tickets

The ticket a PR names goes into the review as what the PR must do.

| Variable | |
|---|---|
| `ITSAPLAN_URL`, `ITSAPLAN_API_KEY` | itsaplan. |
| `LINEAR_API_KEY` | Linear. A personal API key, read-only is enough. |
| `TICKET_PROVIDER` | `itsaplan` or `linear`. Required when both are configured, else the server doesn't start. |

- The main ticket is named in title, branch or description: `ABC-12`.
- Up to 3 more it names (`closes ABC-7`, a link in the description) follow as context.
- Never for PRs from forks.

## npm registries

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

## Storage and Docker

| Variable | |
|---|---|
| `DATA_DIR`, `CACHE_DIR` | Data (learnings, pending reviews, reports, logins, the repos' uids) and per-repo caches. `/data` and `/cache` in the image. Keep both on volumes, apart. |
| `MAX_JOBS` | How many jobs (reviews, answers) run at once. Default `1`. Jobs of one repo always run one after another, so a higher value helps when several repos have PRs at the same time. Raise `mem_limit`, `cpus` and `pids_limit` with it. Above `1`, jobs of different repos share `/tmp`, `/var/tmp`, `/dev/shm`, the loopback network, the process list and the container's limits while they run, see [Security](../security/#jobs-at-once). |
| `DOCKER_HOST` | With the `dind` service: Docker for testcontainers in the checks, isolated with [Sysbox](https://github.com/nestybox/sysbox) (install it on the host). Drop both if your tests don't need Docker. |

## Branding the report

The report page is the templates in [`internal/report/templates/`](https://github.com/marcomaiermm/DiffHarbor/blob/main/internal/report/templates/). Files in `TEMPLATE_DIR` override them, like Django's template dirs:

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
