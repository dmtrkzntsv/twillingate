BIN := twillingate
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# --exclude-libs keeps the ~60,000 C++ symbols of DuckDB's static library
# out of the binary's dynamic symbol table: 6.6 MB the loader reads at every
# start, resident in every process, server and engine children alike (#195).
# Nothing resolves them at run time (the engine loads no extension). GNU ld
# and lld take it; macOS's linker does not, and has no such table to trim.
comma := ,
EXTLDFLAGS := $(if $(filter Linux,$(shell uname -s)),-extldflags=-Wl$(comma)--exclude-libs$(comma)ALL)
LDFLAGS := -s -w $(EXTLDFLAGS) -X github.com/dmtrkzntsv/twillingate/internal/shared/version.Version=$(VERSION)

.PHONY: build ui test check vet dist docker run smoke test-install test-compose seed-demo clean

# The dashboards app (web/) builds into internal/reporting/ui, which the
# binary embeds. Only its components.json is committed, so every Go build
# and test run needs this first; it reruns only when a web/ input is newer
# than the last build, and reinstalls only when the lockfile changed.
UI := internal/reporting/ui/index.html
WEB_SRC := $(shell find web/src web/public web/scripts -type f) web/index.html web/package.json web/vite.config.ts $(wildcard web/tsconfig*.json)

ui: $(UI)

$(UI): web/node_modules/.package-lock.json $(WEB_SRC)
	cd web && npm run build

web/node_modules/.package-lock.json: web/package-lock.json
	cd web && npm ci

# cgo: internal/engine links DuckDB's static library (raw-first spec D13),
# so a build needs a C toolchain and targets the host. Release binaries come
# from ubuntu-22.04 runners, one per architecture, for a glibc 2.35 floor
# (release.yml); a binary built here needs this host's glibc.
build: $(UI)
	CGO_ENABLED=1 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/twillingate

# GOPKGS excludes node_modules: some npm packages ship Go source that
# go list ./... would otherwise treat as part of this module.
GOPKGS = $(shell go list ./... | grep -v '/node_modules/')

# The full suite under the race detector. modernc.org/sqlite is C translated
# to Go, so -race instruments every SQLite memory access and the suite runs
# about ten times slower than without it (~9 min against ~1 on a CI runner).
# CI runs it in release.yml before tagging, not on every pull request push.
test: $(UI)
	go test -race $(GOPKGS)

# The packages whose own code runs goroutines and whose tests never open
# SQLite: under -race they take seconds, so check keeps them race-tested on
# every pull request. A package that opens SQLite in its tests belongs in
# `make test` only.
RACE_PKGS = ./internal/pipeline/... ./internal/identity/... ./internal/geo/...

# vet covers the two release targets' code paths alike: both are 64-bit,
# and 32-bit ARM is no longer built (DuckDB has no build for it, raw-first
# spec D10).
vet:
	go vet $(GOPKGS)

# What pull request CI runs: vet, the coverage gate without -race, and the
# SQLite-free packages with it.
check: vet $(UI)
	./scripts/coverage.sh
	go test -race $(RACE_PKGS)

# A release tarball for this host's architecture: binary + deploy/
# (installer, units, configs). release.yml runs it on an amd64 and an arm64
# runner and writes the SHA256SUMS the installer verifies over both.
ARCH = $(shell go env GOARCH)
dist: $(UI)
	@set -e; [ "$$(go env GOOS)" = linux ] || { echo "dist builds the Linux release, on Linux"; exit 1; }; \
	stage=dist/stage-$(ARCH); rm -rf $$stage; mkdir -p $$stage; \
	CGO_ENABLED=1 go build -trimpath -ldflags '$(LDFLAGS)' -o $$stage/$(BIN) ./cmd/twillingate; \
	cp -r deploy $$stage/deploy; \
	cp .env.example LICENSE $$stage/; \
	tar -czf dist/$(BIN)-linux-$(ARCH).tar.gz -C $$stage .; \
	rm -rf $$stage; \
	echo "packaged dist/$(BIN)-linux-$(ARCH).tar.gz"

docker:
	docker build --target runtime -t twillingate:$(VERSION) .

# ---- local development / testing ----

define LOCAL_ENV
INGEST_ADDR=127.0.0.1:8080
DATABASE_DSN=sqlite://local/twillingate.db
GEO_DSN=none://
LOG_LEVEL=debug
LOG_FORMAT=text
BUFFER_FLUSH_INTERVAL=1s
endef
export LOCAL_ENV

local/.env:
	@mkdir -p local
	@printf '%s\n' "$$LOCAL_ENV" > $@
	@echo "wrote $@"

# The binary reads plain env vars; the launcher loads the env file (same
# division of labour as systemd's EnvironmentFile= in production). Projects
# live in the database; make sure one exists, since names are not unique
# and a blind create would add another each run. Issue a key for it with
# `./twillingate key issue -project-id 1 -label local`.
run: build local/.env
	set -a; . ./local/.env; set +a; \
	./$(BIN) project list | grep -q . || ./$(BIN) project create -name dev; \
	./$(BIN) serve

smoke: build
	./scripts/smoke.sh

test-install: build
	./scripts/test-install.sh

# Builds the image and runs the compose stack end to end. Needs docker, so it
# is not part of `make check`.
test-compose:
	./scripts/test-compose.sh

# Fills local/twillingate.db with 180 days of believable traffic so the dashboards
# have something to plot. Projects come from the database, one profile per
# project name in scripts/seed-demo.py (dev, marketing, docs, app, legacy):
# create them first with `./twillingate project create -name <name>`; whether
# a profile sends ids (and so fills the users, groups and retention pages) is
# its `ids` key in the script. The events go into raw/ beside the database as
# closed logs; the first start dumps them to Parquet. Re-running replaces the
# closed logs not yet dumped, so seed into an empty raw/ after a start.
seed-demo: local/.env build
	@DATABASE_DSN="sqlite://$(PWD)/local/twillingate.db" ./$(BIN) migrate
	python3 scripts/seed-demo.py local/twillingate.db
	@echo
	@echo "The seeded days are closed logs under local/raw/. The first start"
	@echo "('make run') dumps them to Parquet and derives the sessions and"
	@echo "identities, so the dashboards and retention have data once it ends."

clean:
	rm -rf $(BIN) dist local coverage.out
