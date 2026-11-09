FROM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
# Pinned so a release tag rebuilds the same checks.
# actionlint and osv-scanner too: built here on current Go, not Alpine's stale builds.
RUN CGO_ENABLED=0 go install honnef.co/go/tools/cmd/staticcheck@v0.8.2 && CGO_ENABLED=0 go install github.com/zricethezav/gitleaks/v8@v8.30.1 \
    && CGO_ENABLED=0 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 && CGO_ENABLED=0 go install golang.org/x/tools/cmd/deadcode@v0.52.0 \
    && CGO_ENABLED=0 go install golang.org/x/vuln/cmd/govulncheck@v1.9.0 && CGO_ENABLED=0 go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 \
    && CGO_ENABLED=0 go install github.com/google/osv-scanner/v2/cmd/osv-scanner@v2.7.0
# Tests run as a user: as root the code drops its children to a repo's uid, which can't write root's temp dirs.
RUN apk add --no-cache git && adduser -D -u 1000 node
# Tailwind's standalone CLI (go:generate's): the build checks styles.gen.css, start compiles TEMPLATE_DIR's classes.
RUN apk add --no-cache libstdc++ libgcc && wget -qO /usr/local/bin/tailwindcss https://github.com/tailwindlabs/tailwindcss/releases/download/v4.3.4/tailwindcss-linux-x64-musl \
    && echo "2a5456987c41ce3311e423b603842af425ea5ac44505162dd348d43ed3b909fd  /usr/local/bin/tailwindcss" | sha256sum -c - && chmod 755 /usr/local/bin/tailwindcss
USER node
WORKDIR /home/node/src
COPY --chown=node go.mod ./
COPY --chown=node cmd cmd
COPY --chown=node internal internal
# assets_test checks the docs site serves the same lighthouse.
COPY --chown=node docs/public/reviewing*.svg docs/public/
ARG VERSION=dev
# govulncheck fails the build on a known vulnerability the code reaches, the stdlib's too, until Renovate bumps Go.
RUN tailwindcss -i internal/report/styles.css -o /tmp/styles.css --minify && { cmp -s /tmp/styles.css internal/report/styles.gen.css || { echo "styles.gen.css is stale: go generate ./internal/report"; exit 1; }; } \
    && go vet ./... && go test ./... && govulncheck ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -X main.version=$VERSION" -o /home/node/diffharbor ./cmd/diffharbor

FROM node:26-alpine@sha256:8996bb5821d62a8fd9bb27c1cb07462f3fdce6e47d640c6a881ead0b064ce216
# fallow's musl binary needs a CPU with AVX2.
RUN apk add --no-cache curl git ripgrep fd bash ast-grep shellcheck luacheck zizmor && npm install -g --ignore-scripts --no-audit --no-fund @earendil-works/pi-coding-agent@1.5.0 \
    squawk-cli@2.70.0 oxlint@1.90.0 oxlint-tsgolint@7.0.2005 fallow@3.38.1 corepack@0.36.0 && npm cache clean --force && corepack enable && mkdir -p /cache /data && chmod 755 /cache && chmod 700 /data
# codegraph bundles a glibc Node; run its JS on Alpine's Node 26 instead (gcompat for the native kernel).
# Its Node >=25 guard is for a turboshaft WASM bug; --liftoff-only never tiers up to turboshaft.
RUN apk add --no-cache gcompat libgcc libstdc++ && npm install -g --ignore-scripts --no-audit --no-fund @colbymchenry/codegraph@1.6.2 && npm cache clean --force \
    && P=/usr/local/lib/node_modules/@colbymchenry/codegraph/node_modules/@colbymchenry/codegraph-linux-x64 && rm "$P/node" /usr/local/bin/codegraph \
    && printf '#!/bin/sh\nCODEGRAPH_ALLOW_UNSAFE_NODE=1 exec node --liftoff-only --disable-warning=ExperimentalWarning %s/lib/dist/bin/codegraph.js "$@"\n' "$P" > /usr/local/bin/codegraph \
    && chmod 755 /usr/local/bin/codegraph && DO_NOT_TRACK=1 codegraph version
USER node
RUN pi install npm:@ff-labs/pi-fff@0.11.0 && rm -rf /home/node/.npm
USER root
COPY --from=build /usr/local/go /usr/local/go
COPY --from=build /go/bin/staticcheck /go/bin/gitleaks /go/bin/golangci-lint /go/bin/deadcode /go/bin/actionlint /go/bin/osv-scanner /usr/local/bin/tailwindcss /usr/local/bin/
COPY --from=build /home/node/diffharbor /usr/local/bin/diffharbor
RUN fallow --version && squawk --version && oxlint --version && tailwindcss --help > /dev/null
# extensions/review-guard.js refuses tool calls past the soft deadline or 60% context, so a pass still answers.
# node-home.tar: each job's fresh home (proc.Workspace).
COPY extensions/review-guard.js extensions/review-guard.test.mjs /opt/
RUN echo '{"type":"module"}' > /opt/package.json && node --test /opt/review-guard.test.mjs \
    && mkdir -p /home/node/.pi/agent/extensions && cp /opt/review-guard.js /home/node/.pi/agent/extensions/ \
    && chown -R node:node /home/node/.pi/agent/extensions && tar -C /home/node -cf /opt/node-home.tar .
ENV PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin DATA_DIR=/data CACHE_DIR=/cache
EXPOSE 3000
CMD ["diffharbor"]
