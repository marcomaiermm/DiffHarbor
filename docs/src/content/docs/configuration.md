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
| `REVIEW_MODEL`, `VERIFY_MODEL` | The models. The env wins over a repo's `review.json`; model inversion can still replace them. |
| `FALLBACK_MODEL`, `FALLBACK_VERIFY_MODEL` | The fallback instead of the next provider. |
| `ANTHROPIC_BASE_URL`, `OPENAI_BASE_URL`, `DEEPSEEK_BASE_URL` | A gateway in front of the provider (LiteLLM, your company's AI gateway), with the gateway's key. |
| `AZURE_OPENAI_DEPLOYMENT_NAME_MAP` | Deployments not named like the models: `gpt-6.1-sol=review,gpt-6-luna=verify`. |

- Bedrock in the EU: `amazon-bedrock/eu.anthropic.…`.
- Each pass gets only its own provider's key.
- Model inversion: when the AI of the review model's provider wrote most of a PR's commits (Claude Code, Codex or Gemini trailers, bot authors), the next provider with a key reviews and verifies, `REVIEW_MODEL` included. The walkthrough footer says so. A `review_model` in `review.json` or `"model_inversion": false` keeps the model.

:::note
Use API keys. Pi can also log in with a ChatGPT subscription: `openai/…` without `OPENAI_API_KEY`, then `docker compose exec -it diffharbor diffharbor login`. Whether your plan's terms allow that for a review bot is yours to check. You use it at your own risk.
:::

## Report

`REPORT_URL` turns on an HTML page per PR, linked in the bot's PR comment.

- **What's on it:** a verdict, the findings by weight, a reading order, diffs with findings, call graphs and what the change reaches outside the PR. Before you merge: base commits the PR is behind on, conflicts, and open PRs changing the same files or lines. At the end a draft review to send from the forge yourself. The page posts nothing.
- **Versions:** each reviewed head keeps one, the newest 10 stay. Closing the PR deletes them.
- **Link:** `/r/<host>/id:<repo id>/<n>/<secret>`, by the repo's ID: a rename keeps it, a new repo under an old name gets its own. Links from before the ID (`/r/<host>/<repo>/<n>/<secret>`) keep working: the PR's next review moves its reports to the ID and redirects them. A PR that closes without one loses those under its current name; under a name the repo had before a rename they stay. The secret is random per PR (130 bits) and only the PR comment holds it. Any other `/r/` path is a 404.
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

## Reference docs

Before the review runs, the server looks for the doc sections that mention what the PR changes (symbols it declares, config keys like `REFUND_WINDOW_DAYS`, changed paths) and puts them into the prompt as reference material. The model never searches docs itself and holds no token for them. If the change makes a claim in a section false, the review reports a minor finding that cites the section's link.

For example: the PR raises `REFUND_WINDOW_DAYS` from 14 to 30, and the wiki's "Refunds" page still says 14 days. The review puts a minor finding on the changed line ("The Refunds page says 14 days", with the link), so the page gets fixed with the code.

What happens per review, in this order:

1. **Names** from the diff: the declarations on changed lines with names of more than one word (`refundOrder`, `refund_order`; a one-word name like `posts` is in any prose), `SCREAMING_CASE` config keys on changed lines, and the changed paths. Config keys first, then symbols, then paths, at most 40.
2. **Adapters:** the ones whose globs in `review.json` match a changed file, asked in parallel with the names. They also get the links in the PR description, its commits and the linked ticket, and their `name:ref` entries, so a page the PR links can be found by its link. The server opens none of these links.
3. **Check:** the server keeps a section only if one of those links or `name:ref` entries points at it, or it names one of the names as a whole word. A generic file name (`index.ts`, `main.go`, `README.md`) doesn't count alone, at the repo's root neither: a path like `cmd/api/main.go` does.
4. **Order:** sections a link or `name:ref` entry points at first, then the ones that name more of the names, then by id. Each at most 2000 characters, 8000 in all, whole sections only: what doesn't fit is left out.
5. **Prompt:** the sections go to the review as reference that may be outdated, the same for every pass. The verifier sees only the sections a finding cites.

Per repo, in `review.json`: a glob of changed files, and the adapters to ask. Names only, never URLs or tokens. Without the key nothing is asked.

```json
{
  "knowledge": {
    "**": ["docs"],
    "internal/refund/**": ["wiki", "linear:<doc-id>", "acmewiki"]
  }
}
```

| Adapter | |
|---|---|
| `docs` | The repo's `*.md`, read from the checkout as the merge base left it: files the PR changes, changelogs (`CHANGELOG.md`, `CHANGES.md`, `HISTORY.md`, `NEWS.md`), symlinks, hidden folders, `node_modules/`, `vendor/`, `third_party/` and `testdata/` are skipped; at most 300 files of 256 KB. Split at `#` to `###` headings. `docs:<path>` ranks that file's sections first. |
| `wiki` | GitLab: the project's wiki (markdown pages, the first 100), read with the bot's token. GitHub: nothing, an App's installation token can't read wikis. |
| `linear` | Linear documents: a search for the first 5 changed names, `linear:<doc-id>` by id, and the Linear document links in the PR description, its commits and ticket (the server asks Linear's API for the id, it doesn't open the link). Uses `LINEAR_API_KEY`, only for owners listed for it below. |
| any name in `KNOWLEDGE_ADAPTERS` | A [remote adapter](#remote-adapters-experimental). |

`KNOWLEDGE_ADAPTERS` is the server's list of what an owner may use, in the style of `NPM_REGISTRIES`:

```text
acme acmewiki https://knowledge.corp/lookup <token>,
acme linear
```

- **Entries:** `<owner> <name> <url> <token>` for a remote adapter, `<owner> linear` to let that owner use the server's `LINEAR_API_KEY`. Split by commas or newlines. A name is `[a-z][a-z0-9-]{0,30}` and not `docs`, `wiki`, `linear` or `itsaplan`.
- **Owner:** like `NPM_REGISTRIES`: the repo's owner (GitLab: `gitlab/<group>`) or `*`. An owner's own entry beats `*` for its name.
- **URLs** only come from this variable, never from a repo or a PR. An invalid entry stops the server at start, naming its number, never the entry.
- `itsaplan` has no adapter yet.

**Guards:**

- Only for PRs from the repo itself, never from a fork.
- Only for private repos. `"knowledge_public": true` in `review.json` opts a public (or GitLab internal) repo in. Where the forge doesn't say, the repo counts as public.
- The sections go into the prompt's head as reference material that may be outdated, never into the trusted rules. The verifier gets only the sections a finding cites.
- The server checks every section itself: it keeps one only if it came from an explicit ref, or names a changed symbol, config key or path (whole words; short symbols and generic file names like `index.ts` don't count). 2000 characters per section, 8000 in all, whole sections only.
- `forge.Redact` strips the secrets from report links in the text. It does **not** scrub credentials or other secrets a doc contains: whoever can edit the docs can reach the model, and a finding that quotes it reaches the PR's comments. Keep secrets out of what an adapter serves.
- The server never fetches a URL it finds in a PR, commit or ticket: those only go to adapters as data.
- An adapter's error or timeout (10 s) costs that adapter only, and only what it hadn't returned yet: `linear` keeps the documents it fetched before a failed search, `docs` the files it read in time.
- The report lists the sections that were read (title and link, not their text).

**What it read:** the report's overview shows "Docs reached" with each section's title, link and adapter. The server logs one line per review, for example `acme/api#12 knowledge: docs, linear: 9 sections returned, 3 kept, 4120 chars`, and a line per adapter that failed. Nothing kept for weeks? Then the docs rarely name the code: point the code's glob at the page that describes it, `"internal/refund/**": ["docs:docs/refunds.md"]`. A ref names a page by its id or link, or one section of it (`docs/refunds.md#window`), never a folder.

### Remote adapters (experimental)

A remote adapter is an HTTP service. The contract may still change; it is versioned.

```text
POST <url>
Authorization: Bearer <token>
Content-Type: application/json

{"version": 1, "repo": "acme/api", "refs": ["https://wiki.corp/p/12", "page-12"],
 "entities": [{"name": "RefundOrder", "kind": "symbol", "hop": 0}], "max_chars": 2000}

200 {"sections": [{"id": "page-12#window", "title": "Refunds", "url": "https://wiki.corp/p/12#window",
                   "updated": "2026-05-01T12:00:00Z", "text": "markdown"}]}
```

- `kind` is `symbol`, `path` or `config`. `refs` are links the PR names and the refs of the repo's `review.json` for this adapter. `max_chars` is the most a section's `text` may have. `refs` and `entities` are always arrays, maybe empty.
- Read-only, deterministic: the same request and source give the same sections.
- Resolve the refs you know and ignore the rest. An entity may be ignored. **Never fetch a ref as a URL**: it comes from a PR's text. Parse ids out of URLs on hosts you own only.
- Answer `400` to a `version` you don't know. Section `text` at most 2000 characters. The answer at most 1 MB, in 10 s, no redirects (the server doesn't follow them).
- Unknown fields are ignored on both sides. Fields are only added, never removed or redefined; a break bumps `version`.
- A section needs an `id`; one without is skipped. `updated` is RFC 3339 or left out, anything else is dropped. A section's `url` is its page's link plus `#<anchor>`: then a link to the page names all its sections.
- The server checks all of the answer itself (above), so false positives cost nothing but their share of the 1 MB.

An adapter in Go, its handler as `internal/knowledge`'s test runs it:

```go
// A remote adapter: run it with ADAPTER_TOKEN set, list it as "acme acmewiki https://<host>/lookup <token>".
package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"os"
)

// Request and Section are the contract's JSON (version 1); unknown fields are ignored.
type Request struct {
	Version  int      `json:"version"`
	Refs     []string `json:"refs"`
	Entities []struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"entities"`
}

type Section struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Updated string `json:"updated,omitempty"`
	Text    string `json:"text"`
}

func main() {
	token := os.Getenv("ADAPTER_TOKEN")
	if token == "" { // else "Bearer " alone would pass
		log.Fatal("ADAPTER_TOKEN is not set")
	}
	want := sha256.Sum256([]byte("Bearer " + token))
	http.HandleFunc("POST /lookup", func(w http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		var req Request
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 ||
			json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req) != nil || req.Version != 1 {
			http.Error(w, "no", http.StatusBadRequest)
			return
		}
		ss := []Section{}
		for _, e := range req.Entities { // your source goes here: the sections that name e.Name
			ss = append(ss, Section{ID: "wiki-" + e.Name, Title: "About " + e.Name, URL: "https://wiki.example/" + e.Name, Text: "How " + e.Name + " works"})
		}
		json.NewEncoder(w).Encode(map[string]any{"sections": ss})
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}
```

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
