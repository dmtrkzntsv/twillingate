# syntax=docker/dockerfile:1
#
# The published image (--target runtime): the binary on alpine and nothing
# else. Node is a build-time dependency only, for the dashboards app the
# binary embeds.

# Build stage must satisfy the toolchain floor in go.mod; bumping go.mod
# without bumping this tag fails the build rather than silently degrading.
# --platform=$BUILDPLATFORM pins this stage to the native runner and lets Go
# cross-compile to TARGET*, instead of QEMU emulating the compiler itself. The
# binary is CGO_ENABLED=0, so there is no C toolchain to cross-target. Building
# arm64/arm under emulation cost ~11 min per release against ~80s natively.
# The dashboards app the binary embeds (internal/reporting/ui): only its
# components.json is in the repository. Built once on the native platform;
# the output is the same for every target arch.
FROM --platform=$BUILDPLATFORM node:22-alpine AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web-build /src/internal/reporting/ui ./internal/reporting/ui
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
# GOARM takes the bare number ("7"), TARGETVARIANT the tag form ("v7").
# `COPY . .` changes on every commit, so this layer never comes from the layer
# cache; the build cache mount is what makes it incremental. One mount serves
# every target arch (the cache is keyed by GOARCH and safe to share), and
# release.yml carries it between runs, since a CI builder starts empty.
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags "-s -w -X github.com/dmtrkzntsv/twillingate/internal/shared/version.Version=${VERSION}" \
    -o /out/twillingate ./cmd/twillingate

FROM alpine:3.20 AS runtime
LABEL org.opencontainers.image.licenses="AGPL-3.0-only"
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 twillingate
COPY --from=go-build /out/twillingate /usr/local/bin/twillingate
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
