---
title: Development
description: Tests, linting, contributing and license.
---


```sh
go test ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...
```

The report's CSS is compiled: after changing `internal/report/styles.css` or a class in a template, run `go generate ./internal/report`. It needs [Tailwind's standalone CLI](https://github.com/tailwindlabs/tailwindcss/releases/tag/v4.3.4) 4.3.4 as `tailwindcss` on the `PATH`; the npm package resolves `tailwindcss` from whatever `node_modules` is nearby. The Docker build fails while `styles.gen.css` is stale.

`diffharbor dry` in a checkout reviews `git diff HEAD` and prints the findings as JSON.

## Contributing

This repository does not take external contributions at the moment. Bug reports as issues are welcome.

## License

Community Edition under the [Functional Source License, FSL-1.1-ALv2](https://github.com/marcomaiermm/DiffHarbor/blob/main/LICENSE.md): use it, change it, run it for your company. You may not offer it as a competing commercial product or service. Each version becomes Apache-2.0 two years after its release.
