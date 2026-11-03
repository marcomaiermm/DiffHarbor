---
title: Configuration
description: Server settings as environment variables.
---


All settings are environment variables, set in `.env` next to `compose.yml`.

## Forges

### GitHub

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


### GitLab

Add a webhook on the project or group: `https://<host>/webhook/gitlab`, triggers Merge request events and Comments.

| Variable | |
|---|---|
| `GITLAB_URL` | Default `https://gitlab.com`. |
| `GITLAB_TOKEN` | A bot user's token (or a project/group access token) with scope `api` and at least the Developer role. Its username is the bot: `@<username> review`. |
| `GITLAB_READ_TOKEN` | Recommended: a token with only `read_repository`, for git. Without it git gets the `api` token next to the MR's code; the server warns on start. |
| `GITLAB_WEBHOOK_SECRET` | The webhook's secret token. |

Differences to GitHub: findings are diff discussions, fixes `suggestion:-N+0` blocks, and a finding GitLab refuses to place goes into the summary note. Large MRs in parts start with `@<bot> review all` (there is no checkbox). There is no redelivery: GitLab retries a failed webhook only briefly. Maintainers count as members, Developers as collaborators.


## Models

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

:::note
Use API keys. Pi can also log in with a ChatGPT subscription (`openai/…` without `OPENAI_API_KEY`, then `docker compose exec -it diffharbor diffharbor login`). Whether your plan's terms allow that for a review bot is yours to check, and you use it at your own risk.
:::

## Optional

| Variable | |
|---|---|
| `REPORT_URL` | An HTML report per PR under `/r/<host>/<repo>/<n>`: reading order, diffs with findings, call graphs, what the change reaches outside the PR, and before you merge, base commits the PR is behind on, conflicts, and open PRs changing the same files or lines. Each reviewed head keeps a version, the newest 10 stay; closing the PR deletes them. **The report contains the PR's code: serve `/r/` only behind your SSO.** |
| `REPORT_S3_URL`, `REPORT_S3_ACCESS_KEY`, `REPORT_S3_SECRET_KEY`, `REPORT_S3_REGION` | The reports in an S3 bucket instead of `DATA_DIR`: `https://host[:port]/bucket[/prefix]`, path-style, for SeaweedFS, MinIO, Garage, R2 or AWS. `/r/` still serves them, so the bucket stays private. The key needs `GetObject`, `PutObject` and `DeleteObject` on `bucket/prefix/*` and `ListBucket` on the bucket. The region defaults to `us-east-1`. Only the server holds the key; checks and Pi never get it. On the first start with it, the reports on disk move into the bucket and their folder goes; a failed move retries on the next start. A delete that fails when a PR closes retries every 30 minutes. |
| `TEMPLATE_DIR` | Your branding for the report, see [Branding the report](#branding-the-report). |
| `ITSAPLAN_URL`, `ITSAPLAN_API_KEY` | The itsaplan ticket a PR names (`ABC-12` in title, branch or description) goes into the review as what the PR must do. Never for PRs from forks. |
| `DOCKER_HOST` | With the `dind` service: Docker for testcontainers in the checks, isolated with [Sysbox](https://github.com/nestybox/sysbox) (install it on the host). Drop both if your tests don't need Docker. |

## Branding the report

The report page is the templates in [`internal/report/templates/`](https://github.com/marcomaiermm/DiffHarbor/blob/main/internal/report/templates/). Files in `TEMPLATE_DIR` override them, like Django's template dirs:

- `logo.png` (64 px), `favicon.svg`, `favicon-32x32.png` replace the images.
- A `*.html` file that defines a template again replaces ours. A file with the name of one of ours (`header.html`) replaces all of that file. Files are read in name order, the last definition wins.

```html
<!-- $TEMPLATE_DIR/brand.html -->
{{define "theme"}}<style>:root { --primary: #0b5fff; --ring: #0b5fff; }</style>{{end}}
{{define "footer"}}<footer class="py-6 text-center text-xs text-muted-foreground">Acme Engineering</footer>{{end}}
```

Blocks meant for this: `theme` (the color tokens, see `theme.html`), `logo`, `head` (more in `<head>`), `header`, `footer`. The others work too but follow the page's data, which can change between versions. DiffHarbor checks the templates at start and doesn't start with a broken one. Reports already rendered keep their look.

