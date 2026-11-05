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
  "large_pr": "all",
  "max_parts": 4,
  "passes": [{}, {"max_lines": 1500}],
  "report": "auto",
  "suggest_split": {"lines": 300, "files": 20},
  "tickets": ["ABC"],
  "install_scripts": false,
  "base_branches": ["develop", "release/*"],
  "learnings": {"max": 30, "max_age_days": 180},
  "checks": [".review/checks.sh"],
  "packages": {
    "apps/web": {"min_severity": "major"},
    "services/*": {"tests": false}
  }
}
```

| Key | |
|---|---|
| `ignore` | Globs; a path without one takes its folder, `!glob` takes paths back in (`["src/ui", "!src/ui/{chip,toast}.tsx"]`). Neither the model nor a check sees these files. |
| `min_severity` | `minor`, `major` or `critical`: findings below are dropped. |
| `tests` | Run the tests of changed packages. Unset, they run only when no CI workflow does. |
| `review_model`, `verify_model` | Apply where the server sets none; the provider's key must be on the server. |
| `large_pr`, `max_parts` | See [Large PRs](#large-prs). |
| `passes` | Finders per part, at most 3. See [Passes](#passes). |
| `report` | `auto` (from 10 changed files), `always` or `off`. |
| `suggest_split` | See [Split suggestion](#split-suggestion). |
| `tickets` | The itsaplan or Linear projects (team keys) a PR may name. |
| `install_scripts` | `true` runs lifecycle scripts (`postinstall`, native builds) in the checks' installs. Off by default: a dependency's script would run next to the PR's code. |
| `base_branches` | Globs of base branches besides the default branch whose PRs get reviewed on their own. A PR into any other (`main` → `prod`) is skipped with a green status; `@<bot> review` still reviews it. |
| `learnings` | `max`: how many of the newest learnings of the repo and of the owner a review and the chat follow each, `0` none, at most 100, default 30. `max_age_days`: leave out older ones, default any age; a learning saved before dates were kept never ages. Both only hide, nothing is deleted. |
| `rules`, `skills`, `checks` | Other paths for the files above. |
| `syntax_theme` | The report's code colors: `{"light": "…", "dark": "…"}`, [Shiki theme](https://shiki.style/themes) names. |
| `packages` | Overrides per monorepo part; the longest glob wins. |

For editor validation and completion add `"$schema": "https://marcomaiermm.github.io/DiffHarbor/review.schema.json"` as the first key; the server ignores it.

A broken `review.json` falls back to the defaults and says so in the review footer.


## Large PRs

A diff over 60k characters can be reviewed in parts: the files of a folder stay together, a file over 60k is cut at its hunks, a hunk over 60k at its lines, each part gets its own review and verify pass, and the findings are merged, most severe first. Each part costs about one normal review.

| `large_pr` | |
|---|---|
| `all` (default) | Always in parts. |
| `ask` | Code up to 60k first. On GitHub the walkthrough offers a checkbox to review all files in parts; on both forges `@<bot> review all` does the same. Later pushes keep reviewing in parts. |
| `off` | Never. |

`max_parts`: default 4, at most 8.

## Passes

Each pass is a finder with tools, followed by its second look; one verify pass checks the findings of all of them. Without `passes` there is one, the review model's.

```json
"passes": [{}, {"max_lines": 1500}]
```

| Key | |
|---|---|
| `model` | Unset: the second model of the review model's provider, e.g. `openai/gpt-6-luna:high`. A server that sets `REVIEW_MODEL` sets this one too. |
| `again` | `false`: no second look. |
| `min_lines`, `max_lines` | Run only on PRs with that many changed lines in the reviewed files. |

The first entry is the review model's and always runs: it takes `again` only. A further pass reads the files in reverse order. The verify pass drops a finding that another pass reported too. On four PRs of this repo, a second pass with `openai/gpt-6-luna:high` found about 1.5 more of 41 known defects per run for 14% more cost, one with `openai/gpt-6.1-terra:medium` about 3 more for 80% more: mostly the same defects the first pass found.

## Split suggestion

The AI suggests splitting a big PR into PRs that could each merge on their own. Off by default.

```json
"suggest_split": {"lines": 300, "files": 20}
```

| Key | |
|---|---|
| `lines` | Changed lines from which it suggests. |
| `files` | Changed files from which it suggests. |
| `count` | Which files count, by class: `code`, `test`, `data` (JSON, YAML, TOML, CSV, XML), `bulk` (stylesheets, notebooks, translations, seeds), `doc`. Default `["code"]`. |
| `skip` | Globs, like `ignore`, of files that don't count. `"!glob"` counts a file whatever its class. |

- **Either one is enough**: 400 lines in 3 files or 25 files with 100 lines both get a suggestion.
- **Only code counts by default**: thorough tests, fixtures or docs are no reason to split.
- Lockfiles, generated, ignored and deleted files never count.

```json
"suggest_split": {"lines": 300, "count": ["code", "test"], "skip": ["migrations/**", "!deploy/**/*.yaml"]}
```

## Monorepos

Checks run per package (the nearest `package.json` or `go.mod`, at most 5 packages) and install once per lockfile. The `AGENTS.md` (else `CLAUDE.md`) files in the folders above the changed files are added to the rules, at most 5.

