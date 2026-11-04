---
title: Security
description: What runs where, and whom to trust.
---


:::caution
The checks and the agent run the PR's code, also for PRs from forks. Install the bot only on repos whose PR authors you trust, such as your team.
:::

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
- No new privileges (a setuid binary can't make a repo's user root) and only the capabilities root needs. Limits: 3 GB RAM, 2 CPUs and 4096 processes per container. A Pi run gets 8 minutes; [`extensions/review-guard.js`](https://github.com/marcomaiermm/DiffHarbor/blob/main/extensions/review-guard.js) refuses further tool calls after 6 minutes or at 60% context, so the pass still answers.

