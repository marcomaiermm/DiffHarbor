---
title: Per-repo settings
description: Configure reviews per repository with .review/ and review.json.
---

Everything lives under `.review/`. `review.json` can change the paths.

| File | |
|---|---|
| `.review/rules.md` | Rules in prose for the prompt: what to report, which risks you accept. The root `AGENTS.md` (else `CLAUDE.md`) follows it; without a rules file it stands alone. |
| `.review/skills/` | Review skills in the [Agent Skills format](https://agentskills.io/specification). On GitHub, skills in `<owner>/.github` under `.review/skills/` apply to all of the owner's repos. |
| `.review/checks.sh` | Your own checks from the base branch, run in the PR checkout with the changed files after `--`. Their output (120 lines each) goes into the prompt. The exit code doesn't count. |
| `review.json` | Settings, in the repo root. All keys are optional. |

## `review.json` reference

```json
{
  "ignore": ["drizzle/meta", "locales/*.json", "**/*.fixture.ts"],
  "profile": "chill",
  "tests": true,
  "review_model": "openai/gpt-6.1-sol",
  "verify_model": "anthropic/claude-haiku-5-5:low",
  "large_pr": "all",
  "max_parts": 4,
  "passes": [{}, {"max_lines": 1500}],
  "report": "auto",
  "suggest_split": {"lines": 300, "files": 20},
  "tickets": ["ABC"],
  "knowledge": {"**": ["docs"]},
  "install_scripts": false,
  "base_branches": ["develop", "release/*"],
  "drafts": "manual",
  "learnings": {"max": 30, "max_age_days": 180, "outcomes": true},
  "gitlab_findings": "review",
  "checks": [".review/checks.sh"],
  "packages": {
    "apps/web": {"min_severity": "major", "profile": "quiet"},
    "services/*": {"tests": false}
  }
}
```

| Key | |
|---|---|
| `ignore` | Globs. A path without one takes its folder. `!glob` takes paths back in: `["src/ui", "!src/ui/{chip,toast}.tsx"]`. Neither the model nor a check sees these files. |
| `profile` | `quiet`, `chill` (default) or `assertive`: which findings go inline and which fold into the review's summary. See [Review profiles](#review-profiles). |
| `min_severity` | `minor`, `major` or `critical`: findings below are dropped before verification, nowhere posted. `minor` drops the nits. |
| `tests` | Run the tests of changed packages. Unset, they run only when no CI workflow does. |
| `review_model`, `verify_model` | Apply where the server sets none. The provider's key must be on the server. |
| `large_pr`, `max_parts` | See [Large PRs](#large-prs). |
| `passes` | Finders per part, at most 3. See [Passes](#passes). |
| `report` | `auto` (from 10 changed files), `always` or `off`. |
| `suggest_split` | See [Split suggestion](#split-suggestion). |
| `tickets` | The itsaplan or Linear projects (team keys) a PR may name. |
| `knowledge`, `knowledge_public` | Reference docs for the review. See [Reference docs](../configuration/#reference-docs). |
| `install_scripts` | `true` runs lifecycle scripts (`postinstall`, native builds) in the checks' installs. Off by default: a dependency's script would run next to the PR's code. |
| `base_branches` | Globs of base branches whose PRs get reviewed, besides the default branch. A PR into any other (`main` → `prod`) is skipped with a green status. `@<bot> review` still reviews it. |
| `drafts` | `manual` (default): a draft PR gets a review on `@<bot> review`, then once marked ready. `opt_in`: once someone asked with `@<bot> review`, every later push of the draft gets one too, like any PR. `off`: never; `@<bot> review` says so. |
| `learnings` | `max`: how many of the newest learnings of the repo and of the owner a review and the chat follow, each. `0` is none, at most 100, default 30. `max_age_days`: older ones are left out. Default: any age. A learning saved before dates were kept never ages. Both only hide, nothing is deleted. `outcomes`: when a PR closes, a finding a maintainer declined (👎 by someone who can push; a resolve alone says nothing) is saved as a learning; `false` only counts them. Default `true`. |
| `gitlab_findings` | `review`: the findings, the answers to fixed threads and the summary are one review, one email; a suggestion GitLab can't place goes into the summary as code. `discussions`: each on its own, an email each; a refused suggestion is retried as plain code on its line. Default: the server's `GITLAB_FINDINGS`. |
| `rules`, `skills`, `checks` | Other paths for the files above. |
| `syntax_theme` | The report's code colors: `{"light": "…", "dark": "…"}`, [Shiki theme](https://shiki.style/themes) names. |
| `packages` | Overrides per monorepo part. The longest glob wins. |

- For validation and completion in your editor, add `"$schema": "https://marcomaiermm.github.io/DiffHarbor/review.schema.json"` as the first key. The server ignores it.
- A broken `review.json` falls back to the defaults and says so in the review footer.


## Review profiles

Reviews can report defects (critical, major, minor) and, unless `min_severity` drops them, nits: naming, readability, a dead branch, an idiom of the codebase the change ignores, at most 5 per pass and never in place of a defect. The profile decides what goes inline as a thread and what folds into the review's summary, where nothing is lost and the prompt for AI agents still has it. Inline means on a diff line: a finding outside the diff goes into the summary whatever the profile.

| `profile` | Inline | Folded into the summary |
|---|---|---|
| `quiet` | critical, major | minor, nits |
| `chill` (default) | critical, major, minor | nits |
| `assertive` | everything, nits too | |

A push's review folds minor findings under `chill` too: most of a push fixes earlier findings, and the edge cases of each fix would open the next round of threads. `min_severity` drops findings before that, `minor` the nits. A folded finding has no thread: a later review of its file may report it again, and an incremental review that finds only folded ones posts nothing.

The first that sets a profile wins:

1. `@<bot> review quiet` (also `review all assertive`): this review only.
2. The PR description: a line `review: quiet`, typed by hand or a ticked box of the PR template. Unticked boxes and HTML comments don't count, so a template can offer all three:

   ```markdown
   - [ ] review: quiet
   - [x] review: chill
   - [ ] review: assertive
   ```

3. `packages` in `review.json`, by path.
4. `profile` in `review.json`.

The review footer names a profile set by the comment or the description.

## Large PRs

A diff over 60k characters can be reviewed in parts. Each part costs about one normal review.

- The files of a folder stay together.
- A file over 60k is cut at its hunks, a hunk over 60k at its lines.
- Each part gets its own review and verify pass. The findings are merged, most severe first.

| `large_pr` | |
|---|---|
| `all` (default) | Always in parts. |
| `ask` | Code up to 60k first. On GitHub the walkthrough offers a checkbox to review all files in parts. On both forges `@<bot> review all` does the same. Later pushes keep reviewing in parts. |
| `off` | Never. |

`max_parts`: default 4, at most 8.

## Passes

Each pass is a finder with tools, followed by its second look. One verify pass checks the findings of all of them. Without `passes` there is one, the review model's.

```json
"passes": [{}, {"max_lines": 1500}]
```

| Key | |
|---|---|
| `model` | Unset: the second model of the review model's provider, e.g. `openai/gpt-6-luna:high`. A server that sets `REVIEW_MODEL` sets this one too. |
| `again` | `false`: no second look. |
| `min_lines`, `max_lines` | Run only on PRs with that many changed lines in the reviewed files. |

- The first entry is the review model's and always runs. It takes `again` only.
- A further pass reads the files in reverse order.
- The verify pass drops a finding that another pass reported too.

Measured on four PRs of this repo with 41 known defects, per run:

| Second pass | More defects found | More cost |
|---|---|---|
| `openai/gpt-6-luna:high` | about 1.5 | 14% |
| `openai/gpt-6.1-terra:medium` | about 3 | 80% |

Mostly they find the same defects the first pass found.

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

- Checks run per package: the nearest `package.json` or `go.mod`, at most 5 packages. They install once per lockfile.
- The `AGENTS.md` (else `CLAUDE.md`) files in the folders above the changed files are added to the rules, at most 5.
