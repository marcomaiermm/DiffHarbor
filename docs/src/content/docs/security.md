---
title: Security
description: What runs where, and whom to trust.
---

:::caution
The checks and the agent run the PR's code, also for PRs from forks. Install the bot only on repos whose PR authors you trust, such as your team.
:::

Within that:

## Keys stay with root

- Root serves and holds the forge keys, the model keys and Pi's login.
- git, checks and Pi run as a user of the repo's own (one uid per repo). They get none of the keys, except for the cases under [What the PR's code can reach](#what-the-prs-code-can-reach).
- git fetches and Pi's model calls go through a proxy in the server on `127.0.0.1`. They use random tokens that the server revokes when the Pi run (git: the job) ends.
- A token reaches only its provider's model API or its job's repo (fetches, no push). The proxy adds the key there.
- `NPM_REGISTRIES` tokens work the same way: installs read through the proxy, GETs only, with tokens that end with the checks.

## Each job

- Root reads and writes in the checkout only as the repo's user. A symlink in the PR can't reach root's files.
- All processes of the repos' users are killed before and after each job.
- Each job gets a fresh home (git config, Pi's config and extensions) and temp dir. The users' files in `/tmp`, `/var/tmp` and `/dev/shm` go before each job.
- Check output is capped.
- The checks install dependencies without lifecycle scripts, so a malicious dependency's postinstall doesn't run. `install_scripts` in `review.json` turns them back on per repo.
- No new privileges: a setuid binary can't make a repo's user root. Root keeps only the capabilities it needs.
- Limits per container: 3 GB RAM, 2 CPUs, 4096 processes.
- A Pi run gets 8 minutes. [`extensions/review-guard.js`](https://github.com/marcomaiermm/DiffHarbor/blob/main/extensions/review-guard.js) refuses further tool calls after 6 minutes or at 60% context, so the pass still answers.

## Each repo

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

## What the PR's code can reach

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
