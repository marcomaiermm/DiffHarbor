---
title: Security
description: What runs where, and whom to trust.
---


:::caution
The checks and the agent run the PR's code, also for PRs from forks. Install the bot only on repos whose PR authors you trust, such as your team.
:::

Within that:

- Root serves and holds the forge keys, the model keys and Pi's login. git, checks and Pi run as `node` and, but for the cases below, get none of them: git fetches and Pi calls its model through a proxy in the server on `127.0.0.1`, with random tokens that the server revokes when the Pi run (git: the job) ends. A token reaches only its provider's model API or its job's repo (fetches, no push); the proxy adds the key there.
- Root reads and writes in the checkout only as `node`: a symlink in the PR can't reach root's files.
- Processes a job leaves behind are killed before and after each job, and node's home (git config, Pi's config and extensions) and temp files are reset before each. Check output is capped.
- The cache (`/cache`: Go build cache, module and pnpm stores) is shared by all repos and node can write it: a PR in one repo can change what a later job of another repo runs. Run one instance per group of repos whose authors trust each other.
- PR code shares the container's network: it reaches `localhost:3000/r/` (all reports, the SSO is in front of the proxy only) and, on a cloud VM, the metadata endpoint. Block `169.254.169.254` for the container there.
- During its job the PR's code can use those tokens too (same uid): spend model tokens, fetch the repo it already has. It can't keep a key. Pi still gets the credential itself, readable by the PR's code during the run, for a ChatGPT login (an access token that outlives the run, never the refresh token; Pi sends it only to OpenAI directly), Bedrock with IAM keys (SigV4 signs the host; `AWS_BEARER_TOKEN_BEDROCK` goes through the proxy) and a provider outside the models table. A gateway that adds the key itself avoids that.
- No new privileges (a setuid binary can't make node root) and only the capabilities root needs. Limits: 3 GB RAM, 2 CPUs and 4096 processes per container. A Pi run gets 8 minutes; [`extensions/review-guard.js`](https://github.com/marcomaiermm/DiffHarbor/blob/main/extensions/review-guard.js) refuses further tool calls after 6 minutes or at 60% context, so the pass still answers.

