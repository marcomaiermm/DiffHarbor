---
title: Quick start
description: Run DiffHarbor with Docker Compose.
---

```sh
cp compose.example.yml compose.yml
```

Create `.env` next to it:

```ini
GITHUB_APP_ID=123456
GITHUB_APP_PRIVATE_KEY_B64=...
GITHUB_WEBHOOK_SECRET=...
# or ANTHROPIC_API_KEY, DEEPSEEK_API_KEY, ...
OPENAI_API_KEY=...
```

```sh
docker compose up -d        # listens on :3000
```

Point your GitHub App's webhook at `https://<host>/webhook/github`. Nothing else needs to be public.

- [Create the GitHub App](../configuration/#github): permissions and events
- [GitLab instead](../configuration/#gitlab): `GITLAB_TOKEN`, `/webhook/gitlab`
- [Other model providers](../configuration/#models)

:::caution
The checks and the agent run the PR's code, also for PRs from forks. Install the bot only on repos whose PR authors you trust. See [Security](../security/).
:::
