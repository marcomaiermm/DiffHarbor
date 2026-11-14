---
title: How a review works
description: The steps of a DiffHarbor review and how to talk to the bot.
---

1. **Diff.** On open, the whole PR against its merge base. On a push, only what changed since the last review.
2. **Context.** Your [checks](#checks), codegraph, co-change from git history, the PR's commit messages, the repo's rules and skills from the base branch, learnings and optionally the linked ticket.
3. **Review.** The agent returns up to 10 findings. Then it looks at the diff again in the same session without tools: up to 10 more at about a tenth of the cost. [`passes`](../per-repo/#passes) add up to two more finders.
4. **Verify.** A cheaper model checks each finding in the code and drops what it can't back up. See [Verification](#verification).
5. **Result.** A walkthrough comment, inline comments and a commit status `diffharbor`.

The agent runs on [Pi](https://github.com/earendil-works/pi), a very slim agent harness, in a checkout of the PR with read, grep and bash.

A run posts at most 5 inline findings. Critical and major ones all stay inline, the others beyond that fold into the summary.

## Checks

Run on the changed lines, when the repo uses them:

gitleaks, tsc, biome, eslint, oxlint, gofmt, vet, staticcheck, golangci-lint, deadcode, fallow, squawk, shellcheck, luacheck, actionlint, zizmor, osv-scanner and the tests of changed packages.

Your own go in [`.review/checks.sh`](../per-repo/).

## What the review reads

- Up to 60k characters per pass: code first, then tests, data, stylesheets and docs. Larger PRs can be reviewed [in parts](../per-repo/#large-prs).
- Left out: lockfiles, build output, vendored code, binaries and generated code (by name, Drizzle's migration snapshots or a "Code generated … DO NOT EDIT" header).

## Pushes

- Only files whose changed lines changed are reviewed. A rebase or a merge of main reviews nothing.
- Minor findings only on the lines the push touched, folded into the summary unless the profile is `assertive`.
- A push that finds only folded ones posts no review. A PR with a report still lists them there.

## Verification

**Trigger.** For each kept defect the verifier names what reaches the failing case in normal operation today: a caller, request, job, config or data source. Untrusted input counts. The comment shows it.

- A defect with no trigger folds into the summary under every profile, unless it is major or critical.
- A verify pass that gives no such answer folds nothing.

**Proof test.** The test the review names as proof of the change runs against the base versions of the PR's code files. If it still passes there, it doesn't prove the change, and the test gets a minor finding of its own: "Test passes without the change". Not run when [`tests`](../per-repo/#reviewjson-reference) is `false`.

**Repro.** For up to 3 major or critical findings per review the verifier writes a minimal test that fails if the finding is right. It runs once, one at a time, in the sandbox and is deleted after. Only where the repo's tests run, see [`tests`](../per-repo/#reviewjson-reference).

| The test | The finding |
|---|---|
| fails as predicted | shows the run in its comment |
| passes | drops to minor |
| doesn't run or doesn't build | stays as it is |

Runners for both:

- Go, or JS and TS with the package's vitest, jest, `bun test` or `node --test`. `bun test` needs bun on the server, the image has none.
- A repro gets 60 s, in Go with `-timeout=30s`.
- A proof test gets 2 minutes, in Go with `-timeout=1m30s`.
- A test the file does not define is not run.

## Drafts and skipped PRs

**Drafts** aren't reviewed until they are marked ready, unless someone asks with `@<bot> review`. [`drafts`](../per-repo/) can also review a draft's pushes once asked, or never.

**Skipped PRs** get a green status. `@<bot> review` still reviews them.

- PRs from bots: Dependabot, Renovate, also a self-hosted one on a user token. Its branch starts with `renovate/` or `dependabot/`, or Renovate's hidden comments are in the description.
- PRs into a branch other than the default branch or one in [`base_branches`](../per-repo/#reviewjson-reference).

## The queue

- Jobs run one at a time, or up to [`MAX_JOBS`](../configuration/#storage-and-docker) at once.
- A repo's jobs never run at the same time, nor those of a repo recreated under its name.
- Repos take turns, so one with many PRs doesn't hold up the others.
- Within a repo, answers to comments go before reviews, not before an earlier close.
- A newer push replaces a waiting review of the same PR and stops a running one.
- Up to 100 jobs wait. Past that the webhook gets a 503.
- GitHub doesn't retry a 503 on its own: DiffHarbor asks GitHub to redeliver what got no 2xx in the last 6 hours, on start and every 30 minutes ([GitHub](../configuration/#github)). A GitLab webhook is retried only briefly.
- Waiting reviews survive a restart, as many as the queue takes. The rest run on their PR's next push or the next restart.

## Talking to the bot

| Comment | What happens |
|---|---|
| Reply in a bot thread | The bot answers, checks a claimed fix, or concedes and resolves the thread. |
| `@<bot> <question>` | An answer about the PR, from the checkout. |
| `@<bot> review` | A full review of the PR. |
| `@<bot> review all` | A large PR reviewed in parts. |
| `@<bot> review quiet` | A full review with another profile: `quiet`, `chill` or `assertive`, also after `all`. |
| `@<bot> learnings` | Lists the saved learnings of the repo and its owner. |
| `@<bot> forget <text>` | Drops the learnings and declined findings containing the text. Only owners and members drop the owner's. |

## Learnings

When a maintainer corrects the bot or states a convention, the bot saves the rule.

- Saved with its reason, the PR and the date in `/data/learnings/`.
- Not saved when one already says it or it only holds for now.
- The newest 100 are kept per repo and per owner.
- A review follows the newest 30 of each, a newer one over an older one it contradicts. [`learnings`](../per-repo/#reviewjson-reference) in `review.json` changes that.

## Outcomes

When a PR closes, the bot counts what became of each finding.

| Outcome | |
|---|---|
| addressed | The bot's "Addressed in" reply. |
| accepted | 👍 |
| dismissed | 👎 |
| resolved | Resolved by hand: fixed or declined, the thread alone doesn't say. |
| ignored | Still open. |

- Only findings with a thread of their own count. One folded into the summary has none to vote on or resolve.
- Only 👍 and 👎 by people who can push to the repo count.
- A PR counts once, however often it closes.

### Declined findings

A finding becomes a declined finding when the PR didn't fix it and

- a person who can push gave it a 👎, or
- its [decision](#decisions) is intended, unlikely or wrong. That reason goes in the line.

A resolve alone never makes one. `learnings.outcomes` in `review.json` turns this off.

- Only the bot's own title, severity and path go in, never a reply.
- Kept in `/data/declined/`, the newest 20 per repo, apart from the learnings so they never push the rules out.
- Reviews and the chat get them as their own section: maintainers declined these findings on these paths, don't report the same point on that code again unless the change makes it newly wrong.
- `@<bot> learnings` lists them, `@<bot> forget <text>` drops them.

## Decisions

A reply often says what a thread's state doesn't. When the bot reads a reply in a finding's thread to answer it, it also notes what that reply decided:

| Decision | |
|---|---|
| fixed | Or agreed and about to be. |
| wrong | The finding is incorrect. |
| intended | |
| unlikely | The case doesn't happen in practice. |
| deferred | A ticket, another PR. |
| declined | Not fixed, for another reason or none. |

- It is part of the answer the bot writes anyway, not a model call of its own.
- Only replies by people who can push to the repo count.
- The last reply the bot read on a finding stands, also one after the PR closed.
- A reply it never read changes nothing: one for someone else after the PR closed, or one before the bot answered another person in the thread.
- A reply that decides nothing (a question, a plain thanks) stays undecided, and so does a finding nobody replied to.

**On close**, some threads go to the verify model in one call without tools. It reads the findings and their replies, nothing else, a fraction of a cent per PR. These are the threads that

- a maintainer replied in and that still have no decision (a reply the bot never answered, or answered without one), or
- end with a maintainer's reply the bot never answered.

## Stats

```sh
diffharbor stats [owner/repo]   # a GitLab project is gitlab/group/project
```

Prints, from `/data/outcomes/`:

- the counts per repo, by severity and by profile (the PR's, else `review.json`'s top-level one),
- the decisions per outcome,
- the outcomes per finding version, `-` for findings without one.

A repo's file there also lists its findings one by one:

- PR, path, title, severity, profile, outcome and decision.
- The day and diffharbor version that counted it.
- What found it: the diffharbor version, review and verify model and head commit of the review that posted it. Read from a hidden marker on its comment, empty for findings an older version posted.

PRs counted before findings were listed stay in the counts only: the decisions start with the first PR closed after the upgrade.

## Usage

Every review, chat answer and close that ran a model adds a line to `/data/usage/<yyyy-mm>.jsonl`:

- the job (`review`, `chat`, `decide`), the PR and, for a review, its head commit and whether it was full or incremental,
- the result (`ok`, `partial` with files left unreviewed, `stopped` by a newer push or a close, `failed`), the seconds in all and, for a review, per stage: checkout, context (threads, rules, ticket, docs), analyze (checks and the model passes), post. A failed review's stages end where it stopped,
- the characters of diff reviewed, the findings verify kept and dropped, and those dropped as off a push's lines,
- per pass the model that answered, its turns, input, cache-read, cache-write, output and reasoning tokens, Pi's cost and the model time, with Pi's context compactions. `unread` counts sessions that couldn't be read: the cost is then a floor. A pass with tools the fallback took over from is listed apart as `<name>.failed`; one without tools lists both models joined by `+`.

Only counts and names, never what a prompt or reply said. What a month cost, by job:

```sh
jq -s 'group_by(.job) | map({job: .[0].job, runs: length, cost: (map(.cost) | add)})' /data/usage/2026-11.jsonl
```
