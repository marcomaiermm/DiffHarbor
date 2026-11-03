---
title: Per-repo settings
description: Configure reviews per repository with .review/ and review.json.
---


Everything lives under `.review/`; `review.json` can change the paths.

| File | |
|---|---|
| `.review/rules.md` | Rules in prose for the prompt. Without it, `AGENTS.md` or `CLAUDE.md`. |
| `.review/skills/` | Review skills in the [Agent Skills format](https://agentskills.io/specification). On GitHub, skills in `<owner>/.github` under `.review/skills/` apply to all of the owner's repos. |
| `.review/checks.sh` | Your own checks from the base branch, run in the PR checkout with the changed files after `--`. Their output (120 lines each) goes into the prompt; the exit code doesn't count. |
| `review.json` | Settings, in the repo root. All keys are optional. |

## `review.json` reference

```json
{
  "ignore": ["drizzle/meta", "locales/*.json", "**/*.fixture.ts"],
  "min_severity": "minor",
  "tests": true,
  "review_model": "openai/gpt-6.1-sol",
  "verify_model": "anthropic/claude-haiku-5-5:low",
  "large_pr": "ask",
  "max_parts": 4,
  "report": "auto",
  "tickets": ["ABC"],
  "checks": [".review/checks.sh"],
  "packages": {
    "apps/web": {"min_severity": "major"},
    "services/*": {"tests": false}
  }
}
```

| Key | |
|---|---|
| `ignore` | Globs; a path without one takes its folder. Neither the model nor a check sees these files. |
| `min_severity` | `minor`, `major` or `critical`: findings below are dropped. |
| `tests` | Run the tests of changed packages. Unset, they run only when no CI workflow does. |
| `review_model`, `verify_model` | Apply where the server sets none; the provider's key must be on the server. |
| `large_pr`, `max_parts` | See [Large PRs](#large-prs). |
| `report` | `auto` (from 10 changed files), `always` or `off`. |
| `tickets` | The itsaplan or Linear projects (team keys) a PR may name. |
| `rules`, `skills`, `checks` | Other paths for the files above. |
| `syntax_theme` | The report's code colors: `{"light": "…", "dark": "…"}`, [Shiki theme](https://shiki.style/themes) names. |
| `packages` | Overrides per monorepo part; the longest glob wins. |

For editor validation and completion add `"$schema": "https://marcomaiermm.github.io/DiffHarbor/review.schema.json"` as the first key; the server ignores it.

A broken `review.json` falls back to the defaults and says so in the review footer.


## Large PRs

A diff over 60k characters can be reviewed in parts: the files of a folder stay together, each part gets its own review and verify pass, and the findings are merged, most severe first. Each part costs about one normal review.

| `large_pr` | |
|---|---|
| `ask` (default) | Code up to 60k first. On GitHub the walkthrough offers a checkbox to review all files in parts; on both forges `@<bot> review all` does the same. Later pushes keep reviewing in parts. |
| `all` | Always in parts. |
| `off` | Never. |

`max_parts`: default 4, at most 8.

## Monorepos

Checks run per package (the nearest `package.json` or `go.mod`, at most 5 packages) and install once per lockfile. The `AGENTS.md` (else `CLAUDE.md`) files in the folders above the changed files are added to the rules, at most 5.

