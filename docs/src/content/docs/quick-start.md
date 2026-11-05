---
title: Quick start
description: Run DiffHarbor with Docker Compose.
---

```sh
cp compose.example.yml compose.yml   # the values go into .env next to it
docker compose up -d
```

1. The server listens on `:3000`.
2. Expose `POST /webhook/github` and/or `POST /webhook/gitlab` to your forge. Nothing else needs to be public.
3. Set up at least one [forge](../configuration/#forges) and one [model provider](../configuration/#models).
