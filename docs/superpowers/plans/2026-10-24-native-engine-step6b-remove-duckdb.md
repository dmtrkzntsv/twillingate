# Native engine, step 6b: DuckDB leaves the repository — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** twillingate builds with `CGO_ENABLED=0` into one static binary with no DuckDB in its module graph. The binary has no `internal/engine`, no in-tree reader, no `engine` subcommand, no `ENGINE_*` settings and no DuckDB oracle tests, and every check keeps passing against the frozen goldens.

**Architecture:**
- **Goldens.** The goldens frozen in 6a become the only reference. `-update-golden` writes native's own answers from now on.
- **Fake engine.** SQL-path tests keep running through a scripted fake engine that returns canned protocol answers, since no real SQL engine exists in the repository. The real SQL behaviour is tested where the engine lives.
- **Leftovers.** What `reporting` and `api` still use from `enginesql` (binding the parameters, trimming, the advice text) moves next to its callers.
- **Build.** The Makefile, Dockerfile, release workflow, installer and docs drop cgo, glibc and the per-architecture runners.

**Tech Stack:** Go, GitHub Actions, Docker.

**Spec:** `docs/superpowers/specs/2026-10-21-native-engine-design.md`, order of work step 6 ("DuckDB, cgo and `internal/engine` leave this repository in the same release"). The checklist comes from step 6a's final review and is copied into the tasks below.

## Global Constraints

- **The module graph.** `go list -deps ./...` must contain no `duckdb` package, and `go.mod` must not require it. `internal/archtest` gets a test that fails if any package imports a DuckDB module or uses cgo (`import "C"`).
- **Same behaviour.** Everything except the removed pieces behaves as it did:
  - native answers, through the goldens;
  - SQL through the hook, through the fake engine's contract tests;
  - the daily pass, through archive's own tests.
- **Goldens.**
  - No golden changes value in this step. A golden that cannot be met without DuckDB is a finding to report, not a file to rewrite.
  - Digest goldens are recomputed by reading Go-written files back with parquet-go, using the same text encoding, and must equal the stored digests.
  - They run under race again: remove `skipDigest`.
- **Build and release.**
  - `make build`, `make dist` and the Dockerfile use `CGO_ENABLED=0`.
  - The release builds both architectures from one runner with `GOARCH`.
  - The Docker runtime image is minimal (distroless static or similar), keeping uid/gid 10001 and the existing volume and env contract.
  - The installer's glibc check goes.
  - `scripts/smoke.sh` and `scripts/test-install.sh` follow.
  - If arm/v7 or Alpine become possible again, the docs may say so, but they are not added to the release in this step.
- **Config.** `ENGINE_MEMORY_LIMIT` and `ENGINE_MAX_CHILDREN` are removed. An install that still sets them starts normally and logs one warning naming them as ignored. `docs/deployment.md` and `.env.example` drop them, and `docs_sync_test` stays green.
- **Docs.**
  - CLAUDE.md: the layout, the build notes (no C toolchain) and the scopes (drop `engine`).
  - `docs/deployment.md`: requirements (no glibc floor), the image, and `engine -check`.
  - README.
  - `deploy/UPGRADES.md`: one entry for this release. It folds in 6a's entry if both ship together.
- **Measurements.** Measure binary size and idle RSS of `serve`, with the console and ingest, before and after. Record them in the spec's measurements and in the PR. The spec's idle target is about 20 MB.
- **Race budget.** Every package stays under go test's 600 s with `-race`. Report the times; archive should drop a lot.
- **Checks.** `make check` passes before each commit (`PATH=/usr/local/go/bin:$PATH GOTMPDIR=~/.cache/gotmp make check`). Web tasks also run `cd web && npm test && npm run e2e`. Never use `/tmp`. Name no engine product or service.
- **Commits.** Conventional Commits. The release-defining change is `feat!` (or `build!`), with a `BREAKING CHANGE:` footer covering the removed `engine` subcommand and the ignored `ENGINE_*` settings.

## Review Focus

1. **A golden that only DuckDB satisfied.** After the oracle halves go, each native test still compares against its golden with the same tolerance and allowances. Pinned by every test passing with no golden diff.
2. **Digest goldens read back without DuckDB.** The parquet-go read-back must produce DuckDB's text encoding: timestamps, doubles, NULL, and the column order. Pinned by Task 1.
3. **An install with `ENGINE_*` still set.** It starts and logs one warning. Pinned by Task 3.
4. **The Docker image.** It runs as uid 10001, holds the data volume and socket path contract, and `serve` starts in it. Pinned by Task 4's image smoke run.
5. **SQL tests without a real engine.** The fake engine's canned answers still exercise every mode, kind and the stale retry. Pinned by Task 2.

---

### Task 1: Goldens become the reference

**Files:** every parity test with a `frozen`/golden helper (archive, native, api, app, reporting), and `internal/shared/golden`.

**Changes:**
- Drop the DuckDB half of each oracle comparison.
- `-update-golden` writes native's answer.
- Reproduce archive's DESCRIBE and rows read-backs with parquet-go, so the digests match.
- Remove `skipDigest`.
- `TestAnswerManyEqualsAnswer`'s parity rerun compares against the goldens, or against `Answer` alone.
- Delete `testdata/system-sql`'s DuckDB runner, keeping its goldens.

**Tests:** every test passes with no golden change, and no test imports `internal/engine`.

- [ ] Commit: `test: check native answers against the goldens alone`.

### Task 2: SQL tests through a scripted fake engine

**Files:**
- Replace `archivetest.ServeSQL`, which runs DuckDB, with `archivetest.FakeSQL`: an `sqlengine` server on a socket whose answers are scripted per request (a matcher on mode, SQL and scope), and which records the requests it gets.
- Move every reporting, api and app SQL-path test onto it with canned answers taken from today's ServeSQL outputs: columns, rows, truncation, page and count shapes, and each error kind including stale.
- Delete the tests whose only point was DuckDB SQL behaviour itself. The engine's own repository covers those.

**Tests:** the SQL paths still pass for modes, scope, confine, args, max_rows, the deadline, each kind's mapping, the stale retry, the no-engine refusals, the stream and GET loads, the start-up check, and `reporting dev`.

- [ ] Commit: `test: answer SQL tests from a scripted engine`.

### Task 3: Remove the code

**Remove:**
- `internal/engine`;
- `archive.Reader` and its SQL: `reader.go`, logSelect, eventsView, scopedSQL and the derive SELECTs used only by it;
- `cmd/twillingate/engine.go` and the subcommand;
- `internal/app/engine.go`;
- `internal/shared/enginesql`.

Move `Params`, `Bind` and `Trim`, plus the advice strings, to where reporting and api use them: a small `internal/reporting/sqlbind` or the like, ranked in archtest.

**Config and checks:**
- Remove `ENGINE_*` and add the ignored-setting warning.
- Remove the `duckdbDir` exclusion and `TestDuckDBSitesAreNotPrepared` from sqlcheck.
- Add the archtest no-DuckDB/no-cgo test.
- Run `go mod tidy`.

**Tests:** all packages pass, with the no-DuckDB test green.

- [ ] Commit: `feat!: remove DuckDB from twillingate`, with the BREAKING CHANGE footer.

### Task 4: Build, release, image, installer

**Files:**
- Makefile;
- Dockerfile;
- `.github/workflows/release.yml` and `ci.yml`, pushed by SSH if the token lacks workflow scope;
- `deploy/systemd/install.sh`;
- `scripts/smoke.sh` and `scripts/test-install.sh`;
- `deploy/compose`.

**Changes:** `CGO_ENABLED=0`, cross-compile both arches on one runner, a minimal static runtime image with uid 10001, and drop the glibc checks.

**Tests:**
- `make dist` builds both arches.
- `docker build` works, then `docker run … serve` starts and `/healthz` answers.
- The installer test passes.

- [ ] Commit: `build: ship one static binary without cgo`.

### Task 5: Docs, measurements, and the release note

**Files:**
- CLAUDE.md, README, `docs/deployment.md`, `.env.example`;
- `deploy/UPGRADES.md`: one entry for the release, folding in 6a's;
- the spec's measurements, with binary size and idle RSS before and after.

**Tests:** the docs-sync tests pass.

- [ ] Commit: `docs: twillingate without DuckDB`.
