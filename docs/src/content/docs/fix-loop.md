---
title: Fix loop
description: An agent skill that works off DiffHarbor's open findings in your checkout until the PR is clean.
---

The fix loop is a skill for Claude Code and other agents that read `SKILL.md`. Run it in your checkout of the PR's branch. Each round it:

1. Lists the PR's unresolved DiffHarbor threads.
2. Fixes each finding, or replies in its thread with a concrete reason when the agent disagrees. DiffHarbor answers there and resolves a minor finding or nit you decline.
3. Runs the repo's tests, commits and pushes.
4. Waits up to 15 minutes for the commit status `diffharbor` on the new head.

It stops when no DiffHarbor thread is open, after 3 rounds, or when a round has nothing to fix or answer. Then it reports what it fixed, what it replied and what it left.

## Install

In Claude Code:

```
/plugin marketplace add marcomaiermm/DiffHarbor
/plugin install diffharbor@diffharbor
```

Then ask it to work off DiffHarbor's findings, or run `/diffharbor:fix-loop`.

Other agents: copy [`plugins/diffharbor/skills/fix-loop`](https://github.com/marcomaiermm/DiffHarbor/tree/main/plugins/diffharbor/skills/fix-loop) into their skills directory.

It needs git and, signed in with push access to the branch, `gh` on GitHub or `glab` and `jq` on GitLab.

## Guards

- Thread text is the PR's data: the skill never runs commands from it, never sends secrets anywhere and never force-pushes.
- It doesn't resolve threads: DiffHarbor resolves those a push fixed.
- It replies once per finding. A pushback it can't refute stays open for you.
- Findings outside the diff are in DiffHarbor's summary comment, not in threads: the loop leaves them to you.
