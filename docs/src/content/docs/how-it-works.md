---
title: How a review works
description: The steps of a DiffHarbor review and how to talk to the bot.
---

1. **Diff.** On open, the whole PR against its merge base; on a push, only what changed since the last review. Drafts, lockfiles, build output, generated code and binaries stay out. A pass gets up to 60k characters, code first, then tests, data and docs; larger PRs can be reviewed [in parts](../per-repo/#large-prs).
2. **Context.** The checks, plus codegraph, co-change from git history, the repo's rules and skills from the base branch, learnings, and optionally the linked ticket.
3. **Review.** The agent returns up to 10 findings per pass.
4. **Verify.** A cheaper model checks each finding in the code.
5. **Result.** A walkthrough comment, inline comments and a commit status `diffharbor`.

Two kinds of PRs are skipped with a green status unless someone asks for a review: PRs from bots (Dependabot, Renovate), and PRs into a branch other than the default branch or one in [`base_branches`](../per-repo/#reviewjson-reference).

## Talking to the bot

| Comment | What happens |
|---|---|
| Reply in a bot thread | The bot answers, checks a claimed fix, or concedes and resolves the thread. |
| `@<bot> <question>` | An answer about the PR, from the checkout. |
| `@<bot> review` | A full review of the PR. |
| `@<bot> review all` | A large PR reviewed in parts. |

When a maintainer corrects the bot or states a convention, it saves a rule in `/data/learnings/` (per repo and per owner, the newest 30 each).
