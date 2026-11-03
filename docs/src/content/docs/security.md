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
- Processes a job leaves behind are killed before and after each job. Check output is capped.
- During a Pi run, the PR's code can read Pi's model key from its env (same uid). A gateway that adds the key itself avoids that.
- Limits: 3 GB RAM and 2 CPUs per container. A Pi run gets 8 minutes; [`extensions/review-guard.js`](https://github.com/marcomaiermm/DiffHarbor/blob/main/extensions/review-guard.js) refuses further tool calls after 6 minutes or at 60% context, so the pass still answers.

