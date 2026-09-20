# CLAUDE.md

## Layout

One binary, one SQLite file, three surfaces (ingest HTTP, MCP, CLI).
Packages are layered, and `internal/archtest` fails `make check` when an
import points up or sideways:

```
cmd/twillingate/ ....... subcommand dispatch and flag parsing; the only importer of internal/app
internal/app/ .......... composition root: opens the store, wires the surfaces, owns shutdown order
internal/server/ ....... ingest HTTP API, plus the JS SDK and docs it serves
internal/api/ .......... private API: MCP endpoint and REST routes over the same operations, one auth
internal/jobs/ ......... daily pass: salt rotation, aggregation, prune, flat-view rebuild
internal/pipeline/ ..... write buffer between ingest and the store
internal/dashboards/ ... Evidence build and snapshot for the reporting image
internal/manage/ ....... project registry snapshot and its audited operations
internal/store/ ........ the Store interface and row types; store/sqlite implements it and owns every migration and view
internal/config/ ....... environment loading
internal/identity/ ..... actor hashing and salt rotation
internal/enrich/ ....... user-agent and URL parsing for web hits
internal/geo/ .......... MaxMind lookup
internal/civil/ ........ calendar dates
internal/version/ ...... build version
docs/ .................. the two contract pages, embedded and served over MCP
sdk/ ................... browser SDK source (TypeScript); the built file is embedded by internal/server
evidence/ .............. the Evidence dashboards project
deploy/ ................ installer, systemd units, compose files, litestream config
```

The rule, top to bottom: `app` imports the surfaces; the surfaces
(`server`, `api`, `jobs`, `pipeline`, `dashboards`) import `manage`
and the leaves but never each other; `manage` imports only leaves; leaves
(`store` and below) import only leaves. A surface that needs another
surface's behaviour takes an interface and `app` passes the
implementation (`server.Enqueuer` is `pipeline.Buffer`). Each consumer
declares the slice of the store it uses (`jobs.Store`, `manage.Store`,
`server.NameStore`) rather than taking `store.Store` whole. A new package
is added to the rank table in `internal/archtest/archtest_test.go` when
it is created.

Refusals are typed: `manage.ErrNotFound`, `manage.ErrConflict` and
`manage.ErrInvalid` (the first two are the store's own values). Edges map
them with `errors.Is`; message text is for humans, never for matching.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/). The release
notes are generated from the log, so the message is the changelog entry — write
the subject for someone reading the release page, not the diff.

```
<type>(<scope>): <subject>
```

`feat`, `fix` and `perf` are the only types that appear in the release notes,
each under its own heading. Everything else (`docs`, `refactor`, `test`,
`build`, `ci`, `chore`, `style`) is still valid and still expected, but is
omitted from the notes entirely — so anything a user should read about on the
release page needs one of the three published types.

The scope is the package or area the change lands in, matching the tree:
`store`, `server`, `jobs`, `config`, `api`, `manage`, `pipeline`, `geo`,
`dashboards`, `sdk`, `cmd`, `deploy`, `ci`. Omit it when a change is genuinely
repo-wide.

Breaking changes take a `!` before the colon (`feat(store)!: ...`), or a
`BREAKING CHANGE:` footer — both surface under 🚨 Breaking Changes.

Subject in the imperative, lower case, no trailing period:

```
feat(api): expose retention cohorts as a tool
fix(jobs): stop the daily pass double-counting identities
ci: do not cut a release for prose-only commits
```

## Releases

Releases are cut by hand: run the `release` workflow from the Actions tab, or
`gh workflow run release.yml`. It tags, runs `make check`, builds the tarballs
and hands off to `npx changelogithub`, which creates the GitHub release,
groups the notes by commit type and uploads the artifacts. Container images
publish from a separate job.

Versions are semver and the project is pre-1.0, so the minor carries breaking
changes and the major stays at zero until someone decides otherwise. The
workflow takes one optional `version` input: leave it blank for the next
patch, or type a version (`0.10.0`, `1.0.0`) for anything larger.

Two consequences worth remembering:

- Pushing to `main` publishes nothing. A commit waits until someone dispatches
  the workflow, so several of them can ship under one version.
- Release notes are only as good as the commit subjects, and they are not
  hand-edited afterwards.

## Checks

`make check` (vet + coverage + restore test) is what CI runs — run it before
pushing. `make build` compiles the binary; `make test` runs the race-enabled
suite on its own.

## Documentation

Two pages, split on using versus running, and both served over MCP.
`docs/twillingate.md` (`docs://twillingate`) is what an agent needs to set
up a project, get it tracking, and answer questions from the data.
`docs/deployment.md` (`docs://deployment`) is what an operator needs to run
twillingate on their own server. Neither is a summary of the code — they
are the contract.

Update `docs/twillingate.md` **in the same commit** as any change to:

- reserved attribute keys or event names (`internal/server/ingest.go`)
- the ingest wire format or its responses (`internal/server/handlers.go`)
- the JS SDK's public API, `data-` attributes or defaults
  (`sdk/src/twillingate.ts`)
- project fields, or the CLI/MCP surface that edits them (`internal/manage/`)
- the MCP tools, REST routes or resources offered (`internal/api/ops_*.go`,
  `expose.go`, `rest.go`, `resources.go`)
- queryable views (`internal/store/sqlite/migrations/`)

Update `docs/deployment.md` in the same commit as any change to environment
variables (`internal/config/`), the install, upgrade, replication or restore
procedure (`deploy/`, `Makefile`), API auth modes or client setup
(`internal/api/auth.go`, `oauth*.go`), or the Evidence dashboards
(`internal/dashboards/`, `evidence/`).

Update `schemaViews` in `internal/api/resources.go` in the same commit
as any migration that adds or changes a queryable view.

Those two plus `docs/plausible/README.md` are the whole of `docs/` — the
seven files they absorbed are gone, so do not resurrect one. The plausible
page stays separate because it documents bytes the collector serves at
`/js/plausible-shim.js` and a test binds it to them.

`docs_sync_test.go` enforces part of this — reserved keys, MCP tool names
and environment variables, each checked in both directions against the
source, plus the SDK's public symbols, every queryable web view and the
HTTP API route table. Those
checks read the specific **table** that claims a fact, not the whole file:
a document-wide match passes for the wrong reason when the same word
appears in prose. The rest is on you. No push publishes anything on its own,
so a same-commit update costs no extra release.
