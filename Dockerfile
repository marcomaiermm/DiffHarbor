FROM golang:1.27-alpine AS build
# Pinned so a release tag rebuilds the same checks.
RUN CGO_ENABLED=0 go install honnef.co/go/tools/cmd/staticcheck@v0.8.2 && CGO_ENABLED=0 go install github.com/zricethezav/gitleaks/v8@v8.30.1 \
    && CGO_ENABLED=0 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 && CGO_ENABLED=0 go install golang.org/x/tools/cmd/deadcode@v0.52.0
# Tests run as a user: as root the code drops its children to uid 1000, which can't write root's temp dirs.
RUN apk add --no-cache git && adduser -D -u 1000 node
USER node
WORKDIR /home/node/src
COPY --chown=node go.mod ./
COPY --chown=node cmd cmd
COPY --chown=node internal internal
ARG VERSION=dev
RUN go vet ./... && go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -X main.version=$VERSION" -o /home/node/diffharbor ./cmd/diffharbor

FROM node:24-alpine
# fallow's musl binary needs a CPU with AVX2.
RUN apk add --no-cache curl git ripgrep fd bash ast-grep shellcheck luacheck actionlint zizmor osv-scanner && npm install -g --ignore-scripts --no-audit --no-fund @earendil-works/pi-coding-agent@1.4.0 \
    squawk-cli@2.70.0 oxlint@1.90.0 oxlint-tsgolint@7.0.2005 fallow@3.36.0 && npm cache clean --force && corepack enable && mkdir -p /cache /data && chown node:node /cache && chmod 700 /data
# codegraph bundles a glibc Node; run its JS on Alpine's Node 24 instead (gcompat for the native kernel).
RUN apk add --no-cache gcompat libgcc && npm install -g --ignore-scripts --no-audit --no-fund @colbymchenry/codegraph@1.6.2 && npm cache clean --force \
    && P=/usr/local/lib/node_modules/@colbymchenry/codegraph/node_modules/@colbymchenry/codegraph-linux-x64 && rm "$P/node" /usr/local/bin/codegraph \
    && printf '#!/bin/sh\nexec node --liftoff-only --disable-warning=ExperimentalWarning %s/lib/dist/bin/codegraph.js "$@"\n' "$P" > /usr/local/bin/codegraph \
    && chmod 755 /usr/local/bin/codegraph && DO_NOT_TRACK=1 codegraph version
USER node
RUN pi install npm:@ff-labs/pi-fff@0.11.0 && rm -rf /home/node/.npm
USER root
COPY --from=build /usr/local/go /usr/local/go
COPY --from=build /go/bin/staticcheck /go/bin/gitleaks /go/bin/golangci-lint /go/bin/deadcode /usr/local/bin/
COPY --from=build /home/node/diffharbor /usr/local/bin/diffharbor
RUN fallow --version && squawk --version && oxlint --version
# extensions/review-guard.js refuses tool calls past the soft deadline or 60% context, so a pass still answers.
COPY extensions/review-guard.js extensions/review-guard.test.mjs /opt/
RUN echo '{"type":"module"}' > /opt/package.json && node --test /opt/review-guard.test.mjs \
    && mkdir -p /home/node/.pi/agent/extensions && cp /opt/review-guard.js /home/node/.pi/agent/extensions/ \
    && chown -R node:node /home/node/.pi/agent/extensions
ENV PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin DATA_DIR=/data CACHE_DIR=/cache
EXPOSE 3000
CMD ["diffharbor"]
