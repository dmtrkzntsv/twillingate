# syntax=docker/dockerfile:1
#
# The published image (--target runtime): the binary on debian:bookworm-slim.
# The binary links glibc and libstdc++ (DuckDB, cgo), so no Alpine. Node is a
# build-time dependency only, for the dashboards app the binary embeds.

# The dashboards app the binary embeds (internal/reporting/ui): only its
# components.json is in the repository. Built once on the native platform;
# the output is the same for every target arch.
FROM --platform=$BUILDPLATFORM node:22-alpine AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Build stage must satisfy the toolchain floor in go.mod; bumping go.mod
# without bumping this tag fails the build rather than silently degrading.
# No --platform=$BUILDPLATFORM: cgo does not cross-compile here, so this
# stage runs on the target's own platform. release.yml builds each
# architecture on a runner of that architecture; building the other one
# locally runs under QEMU, slowly.
FROM golang:1.25-bookworm AS go-build
WORKDIR /src
COPY . .
COPY --from=web-build /src/internal/reporting/ui ./internal/reporting/ui
ARG VERSION=dev
# `COPY . .` changes on every commit, so this layer never comes from the layer
# cache; the build cache mount is what makes it incremental, and release.yml
# carries it between runs, since a CI builder starts empty. The module cache
# is a mount too, not a `go mod download` layer: the DuckDB libraries of all
# five platforms are large and would land in the image layers and the layer
# cache. CI downloads the modules afresh each run. The linker flags are the
# Makefile's (LDFLAGS): --exclude-libs keeps DuckDB's symbols out of the
# dynamic symbol table, 7 MB less memory per process.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -extldflags=-Wl,--exclude-libs,ALL -X github.com/dmtrkzntsv/twillingate/internal/shared/version.Version=${VERSION}" \
    -o /out/twillingate ./cmd/twillingate

FROM debian:bookworm-slim AS runtime
LABEL org.opencontainers.image.licenses="AGPL-3.0-only"
# uid and gid 10001 as in the Alpine image, so existing volumes stay owned.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 twillingate \
    && useradd --system --no-create-home --uid 10001 --gid 10001 twillingate
COPY --from=go-build /out/twillingate /usr/local/bin/twillingate
# The engine child must start in this image before it ships.
RUN DATABASE_DSN=sqlite:///tmp/engine-check/check.db twillingate engine -check && rm -rf /tmp/engine-check
# Docker seeds a fresh named volume from the image directory, ownership
# included. Without this the volume mounts root-owned and the non-root
# process cannot create the database file on first boot.
RUN mkdir -p /var/lib/twillingate && chown twillingate:twillingate /var/lib/twillingate
VOLUME ["/var/lib/twillingate"]
USER twillingate
# Container defaults; override per-deployment via compose env_file/environment.
# INGEST_ADDR binds all interfaces here (unlike the bare-metal loopback
# default) because published ports reach the container's own IP, not lo.
ENV INGEST_ADDR=0.0.0.0:8080 \
    DATABASE_DSN=sqlite:///var/lib/twillingate/twillingate.db
ENTRYPOINT ["/usr/local/bin/twillingate"]
CMD ["serve"]
