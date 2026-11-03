---
title: Quick start
description: Run DiffHarbor with Docker Compose.
---

```sh
cp compose.example.yml compose.yml   # the values go into .env next to it
docker compose up -d --build
```

The server listens on `:3000`. Expose `POST /webhook/github` and/or `POST /webhook/gitlab` to your forge; nothing else needs to be public. Then set up at least one [forge](../configuration/#forges) and one [model provider](../configuration/#models).
