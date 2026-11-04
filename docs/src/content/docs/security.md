---
title: Security
description: What runs where, and whom to trust.
---


:::caution
The checks and the agent run the PR's code, also for PRs from forks. Install the bot only on repos whose PR authors you trust, such as your team.
:::

Within that:

- Root serves and holds the forge keys and Pi's login. git, checks and Pi run as `node`; git gets a read-only token (short-lived on GitHub) by env, not argv.
- Root reads and writes in the checkout only as `node`: a symlink in the PR can't reach root's files.
- Processes a job leaves behind are killed before and after each job, and node's home (git config, Pi's config and extensions) and temp files are reset before each. Check output is capped.
- The cache (`/cache`: Go build cache, module and pnpm stores) is shared by all repos and node can write it: a PR in one repo can change what a later job of another repo runs. Run one instance per group of repos whose authors trust each other.
- PR code shares the container's network: it reaches `localhost:3000/r/` (no SSO there, it is in front of the proxy only, but a report opens only by the secret in its link, which only that PR's comment holds) and, on a cloud VM, the metadata endpoint. Block `169.254.169.254` for the container there.
- During a Pi run, the PR's code can read Pi's model key from its env (same uid). A gateway that adds the key itself avoids that.
- No new privileges (a setuid binary can't make node root) and only the capabilities root needs. Limits: 3 GB RAM, 2 CPUs and 4096 processes per container. A Pi run gets 8 minutes; [`extensions/review-guard.js`](https://github.com/marcomaiermm/DiffHarbor/blob/main/extensions/review-guard.js) refuses further tool calls after 6 minutes or at 60% context, so the pass still answers.

