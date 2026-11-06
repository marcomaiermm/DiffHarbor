---
title: How a review works
description: The steps of a DiffHarbor review and how to talk to the bot.
---

1. **Diff.** On open, the whole PR against its merge base; on a push, only what changed since the last review, and minor findings only on the lines the push touched. Drafts, lockfiles, build output, vendored and generated code (by name, Drizzle's migration snapshots, or a "Code generated … DO NOT EDIT" header) and binaries stay out. A pass gets up to 60k characters, code first, then tests, data, stylesheets and docs; larger PRs can be reviewed [in parts](../per-repo/#large-prs).
2. **Context.** The checks, plus codegraph, co-change from git history, the PR's commit messages, the repo's rules and skills from the base branch, learnings, and optionally the linked ticket.
3. **Review.** The agent returns up to 10 findings, then looks at the diff again for what it missed: the same session without tools, up to 10 more at about a tenth of the cost. With [`passes`](../per-repo/#passes), up to two more finders review the diff too.
4. **Verify.** A cheaper model checks each finding in the code.
5. **Result.** A walkthrough comment, inline comments and a commit status `diffharbor`.

Two kinds of PRs are skipped with a green status unless someone asks for a review: PRs from bots (Dependabot, Renovate, also a self-hosted one on a user token: its branch starts with `renovate/` or `dependabot/`, or Renovate's hidden comments are in the description), and PRs into a branch other than the default branch or one in [`base_branches`](../per-repo/#reviewjson-reference).

## Talking to the bot

| Comment | What happens |
|---|---|
| Reply in a bot thread | The bot answers, checks a claimed fix, or concedes and resolves the thread. |
| `@<bot> <question>` | An answer about the PR, from the checkout. |
| `@<bot> review` | A full review of the PR. |
| `@<bot> review all` | A large PR reviewed in parts. |
| `@<bot> learnings` | Lists the saved learnings of the repo and its owner. |
| `@<bot> forget <text>` | Drops the learnings containing the text. Only owners and members drop the owner's. |

When a maintainer corrects the bot or states a convention, it saves a dated rule in `/data/learnings/` (per repo and per owner, the newest 100 each). A review follows the newest 30 of each; `learnings` in `review.json` changes that.
