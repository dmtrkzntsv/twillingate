# CLAUDE.md

## Layout

One binary, one SQLite file, three surfaces (ingest HTTP, MCP, CLI).
`internal/archtest` fails `make check` when an import points up or sideways.

```
cmd/twillingate/     subcommands and flags; the only importer of internal/app
internal/app/        composition root: opens the store, wires the surfaces
internal/server/     ingest HTTP API; serves the JS SDK and the helper scripts
internal/api/        private API: MCP endpoint and REST routes, one auth
internal/jobs/       daily pass: salt rotation, aggregation, prune, view rebuild
internal/pipeline/   write buffer between ingest and the store
internal/dashboards/ Evidence build and snapshot for the reporting image
internal/manage/     project registry snapshot and its audited operations
internal/store/      Store interface and row types; store/sqlite owns every migration and view
internal/config/     environment loading
internal/identity/   actor hashing and salt rotation
internal/enrich/     User-Agent and URL parsing for web hits
internal/geo/        MaxMind lookup
internal/civil/      calendar dates
internal/version/    build version
docs/                the two contract pages, embedded and served over MCP
sdk/                 browser SDK source (TypeScript); the built file is embedded by internal/server
evidence/            the Evidence dashboards project
deploy/              installer, systemd units, compose files, litestream config, UPGRADES.md
```

`app` imports the surfaces (`server`, `api`, `jobs`, `pipeline`, `dashboards`);
surfaces import `manage` and the leaves, never each other; `manage` and the
leaves import only leaves. A surface that needs another's behaviour takes an
interface that `app` satisfies. Each consumer declares the slice of the store
it uses (`jobs.Store`, `manage.Store`) rather than `store.Store`. A new package
goes into the rank table in `internal/archtest/archtest_test.go`.

Refusals are typed (`manage.ErrNotFound`, `manage.ErrConflict`,
`manage.ErrInvalid`) and matched with `errors.Is`; message text is for humans.

## Commits and releases

[Conventional Commits](https://www.conventionalcommits.org/):
`<type>(<scope>): <subject>`, imperative, lower case, no trailing period.
The release notes are generated from the log, so the subject is the changelog
entry. Only `feat`, `fix` and `perf` appear in the notes; `!` before the colon
(or a `BREAKING CHANGE:` footer) marks a breaking change. Scopes match the
tree: `store`, `server`, `jobs`, `config`, `api`, `manage`, `pipeline`, `geo`,
`dashboards`, `sdk`, `cmd`, `deploy`, `ci`; omit for repo-wide changes.

Releases are cut by hand with `gh workflow run release.yml` (optional `version`
input; blank means next patch). Pushing to `main` publishes nothing. Pre-1.0:
breaking changes bump the minor.

## Checks

`make check` (vet, coverage, restore test) is what CI runs; run it before
pushing. `make build` compiles; `make test` runs the race-enabled suite. The
SDK bundle `internal/server/twillingate.js` is committed: after any change in
`sdk/`, run `npm run build` there and commit the result, or CI's drift check
fails.

## Documentation

Two pages, both served over MCP, both the contract rather than a summary:
`docs/twillingate.md` (`docs://twillingate`) for using twillingate, and
`docs/deployment.md` (`docs://deployment`) for running it. `docs/` holds those
two plus `docs/plausible/README.md`; do not add files there. Per-migration
upgrade runbooks live in `deploy/UPGRADES.md`.

Update in the **same commit** as the change:

| Change to | Update |
| --- | --- |
| reserved keys or event names (`internal/server/ingest.go`), the wire format (`handlers.go`) | `docs/twillingate.md` |
| the SDK's public API, `data-` attributes or defaults (`sdk/src/`) | `docs/twillingate.md` |
| project fields or the CLI/MCP surface that edits them (`internal/manage/`) | `docs/twillingate.md` |
| MCP tools, REST routes, resources (`internal/api/ops_*.go`, `expose.go`, `rest.go`, `resources.go`) | `docs/twillingate.md` |
| queryable views (`internal/store/sqlite/migrations/`) | `docs/twillingate.md` and `schemaViews` in `internal/api/resources.go` |
| a migration with pre-checks or a visible change on upgrade day | `deploy/UPGRADES.md` |
| environment variables (`internal/config/`) | `docs/deployment.md` |
| install, upgrade, replication or restore procedure (`deploy/`, `Makefile`) | `docs/deployment.md` |
| API auth modes or client setup (`internal/api/auth.go`, `oauth*.go`) | `docs/deployment.md` |
| the Evidence dashboards (`internal/dashboards/`, `evidence/`) | `docs/deployment.md` |

`internal/api/docs_sync_test.go` binds part of this in both directions
(reserved keys, tool names, routes, views, SDK symbols, environment variables,
the closed vocabularies), reading the specific table that claims each fact.
The rest is on you.
