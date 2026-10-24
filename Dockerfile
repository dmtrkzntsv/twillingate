# syntax=docker/dockerfile:1
#
# The published image (--target runtime): one static binary (no cgo) on
# distroless static, which holds CA certificates, tzdata and nothing else:
# no shell, no package manager. Node is a build-time dependency only, for
# the dashboards app the binary embeds.

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
# It runs on the build platform and cross-compiles to the target (no cgo),
# so a multi-platform build compiles natively for every architecture.
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS go-build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY . .
COPY --from=web-build /src/internal/reporting/ui ./internal/reporting/ui
ARG VERSION=dev
# `COPY . .` changes on every commit, so this layer never comes from the layer
# cache; the build cache mount is what makes it incremental, and release.yml
# carries it between runs, since a CI builder starts empty. The module cache
# is a mount too, not a `go mod download` layer, so the modules stay out of
# the layer cache. The linker flags are the Makefile's (LDFLAGS).
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X github.com/dmtrkzntsv/twillingate/internal/shared/version.Version=${VERSION}" \
    -o /out/twillingate ./cmd/twillingate
# The data directory, owned by the image's user. Docker seeds a fresh named
# volume from the image directory, ownership included; without this the
# volume mounts root-owned and the non-root process cannot create the
# database file on first boot. The runtime stage has no shell to mkdir with.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
LABEL org.opencontainers.image.licenses="AGPL-3.0-only"
COPY --from=go-build /out/twillingate /usr/local/bin/twillingate
COPY --from=go-build --chown=10001:10001 /out/data /var/lib/twillingate
VOLUME ["/var/lib/twillingate"]
# uid and gid 10001 as in every earlier image, so existing volumes stay
# owned; not distroless's own nonroot (65532). No passwd entry is needed.
USER 10001:10001
# Container defaults; override per-deployment via compose env_file/environment.
# INGEST_ADDR binds all interfaces here (unlike the bare-metal loopback
# default) because published ports reach the container's own IP, not lo.
ENV INGEST_ADDR=0.0.0.0:8080 \
    DATABASE_DSN=sqlite:///var/lib/twillingate/twillingate.db
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/twillingate"]
CMD ["serve"]
