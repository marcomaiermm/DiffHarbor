---
title: How a review works
description: The steps of a DiffHarbor review and how to talk to the bot.
---

1. **Diff.** On open, the whole PR against its merge base. On a push, only what changed since the last review, and minor findings only on the lines the push touched, folded into the summary unless the profile is `assertive`. A push that finds only folded ones posts no review; a PR with a report still lists them there.
2. **Context.** The checks, codegraph, co-change from git history, the PR's commit messages, the repo's rules and skills from the base branch, learnings and optionally the linked ticket.
3. **Review.** The agent returns up to 10 findings. Then it looks at the diff again in the same session without tools: up to 10 more at about a tenth of the cost. [`passes`](../per-repo/#passes) add up to two more finders.
4. **Verify.** A cheaper model checks each finding in the code.
5. **Result.** A walkthrough comment, inline comments and a commit status `diffharbor`.

**What the review reads:**

- Up to 60k characters per pass: code first, then tests, data, stylesheets and docs. Larger PRs can be reviewed [in parts](../per-repo/#large-prs).
- Left out: lockfiles, build output, vendored code, binaries and generated code (by name, Drizzle's migration snapshots or a "Code generated … DO NOT EDIT" header).

**Drafts** aren't reviewed until they are marked ready, unless someone asks with `@<bot> review`; [`drafts`](../per-repo/) can also review a draft's pushes once asked, or never.

**The queue.** Jobs run one at a time, or up to [`MAX_JOBS`](../configuration/#storage-and-docker) at once. A repo's jobs never run at the same time, nor those of a repo recreated under its name; repos take turns, so one with many PRs doesn't hold up the others. Within a repo, answers to comments go before reviews, not before an earlier close. A newer push replaces a waiting review of the same PR and stops a running one. Up to 100 jobs wait; past that the webhook gets a 503. GitHub doesn't retry it on its own: DiffHarbor asks GitHub to redeliver what got no 2xx in the last 6 hours, on start and every 30 minutes ([GitHub](../configuration/#github)). A GitLab webhook is retried only briefly. Waiting reviews survive a restart, as many as the queue takes; the rest run on their PR's next push or the next restart.

**Skipped PRs** get a green status. `@<bot> review` still reviews them.

- PRs from bots: Dependabot, Renovate, also a self-hosted one on a user token. Its branch starts with `renovate/` or `dependabot/`, or Renovate's hidden comments are in the description.
- PRs into a branch other than the default branch or one in [`base_branches`](../per-repo/#reviewjson-reference).

## Talking to the bot

| Comment | What happens |
|---|---|
| Reply in a bot thread | The bot answers, checks a claimed fix, or concedes and resolves the thread. |
| `@<bot> <question>` | An answer about the PR, from the checkout. |
| `@<bot> review` | A full review of the PR. |
| `@<bot> review all` | A large PR reviewed in parts. |
| `@<bot> review quiet` | A full review with another profile: `quiet`, `chill` or `assertive`, also after `all`. |
| `@<bot> learnings` | Lists the saved learnings of the repo and its owner. |
| `@<bot> forget <text>` | Drops the learnings containing the text. Only owners and members drop the owner's. |

When a maintainer corrects the bot or states a convention, the bot saves the rule with its reason, the PR and the date in `/data/learnings/`, unless one already says it or it only holds for now. It keeps the newest 100 per repo and per owner. A review follows the newest 30 of each, a newer one over an older one it contradicts. [`learnings`](../per-repo/#reviewjson-reference) in `review.json` changes that.

**Outcomes.** When a PR closes, the bot counts what became of each finding: addressed (its "Addressed in" reply), accepted (👍), dismissed (👎), resolved (resolved by hand, fixed or declined, the bot can't tell) or ignored (still open). Only 👍 and 👎 by people who can push to the repo count. A dismissed finding, a 👎 by such a person, also becomes a learning (`learnings.outcomes` in `review.json` turns that off); a resolve alone never does. Each close is a line in `/data/outcomes/<owner>%2F<repo>.jsonl`: the PR, when, its profile and each finding's severity, outcome, path and title. `diffharbor stats [owner/repo]` sums them per repo, by severity and by profile (the PR's, else `review.json`'s top-level one). A PR counts once: closed again after a reopen, its last close counts.
