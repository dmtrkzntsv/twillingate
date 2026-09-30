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
internal/reporting/  dashboards, widgets and components: validation, operations, system dashboards, cache; embeds the web app
internal/store/      Store interface and row types; store/sqlite owns every migration and view
internal/config/     environment loading
internal/identity/   actor hashing and salt rotation
internal/enrich/     User-Agent and URL parsing for web hits
internal/geo/        MaxMind lookup
internal/shared/     small generic leaf packages: civil (calendar dates), readsql, sortkey, version
docs/                the three contract pages, embedded and served over MCP
sdk/                 browser SDK source (TypeScript); the built file is embedded by internal/server
web/                 dashboard web app source (React, TypeScript); the build is embedded by internal/reporting
evidence/            the Evidence dashboards project
deploy/              installer, systemd units, compose files, litestream config, UPGRADES.md
```

`app` imports the surfaces (`server`, `api`, `jobs`, `pipeline`, `dashboards`);
surfaces import `manage`, `reporting` and the leaves, never each other; `manage` and the
leaves import only leaves. A surface that needs another's behaviour takes an
interface and `app` passes the implementation (`server.Enqueuer` is
`pipeline.Buffer`). Each consumer declares the slice of the store it uses
(`jobs.Store`, `manage.Store`) rather than `store.Store`. A new package goes
into the rank table in `internal/archtest/archtest_test.go`.

A small generic helper (no twillingate domain in it) is our own code in its
own leaf package under `internal/shared/`, rank 0, rather than a third-party
Go dependency; only a very reputable vendor justifies one. Domain leaves
(`enrich`, `identity`, `geo`, `config`) stay at the top of `internal/`.

Refusals are typed (`manage.ErrNotFound`, `manage.ErrConflict`,
`manage.ErrInvalid`; the first two are the store's own values) and matched
with `errors.Is`; message text is for humans, never for matching.

## Commits and releases

[Conventional Commits](https://www.conventionalcommits.org/):
`<type>(<scope>): <subject>`, imperative, lower case, no trailing period.
The release notes are generated from the log, so the subject is the changelog
entry. Only `feat`, `fix` and `perf` appear in the notes; `docs`, `refactor`,
`test`, `build`, `ci`, `chore` and `style` are valid and expected but never
appear in the notes, so anything a user should read about needs one of the
three published types. `!` before the colon (or a `BREAKING CHANGE:` footer)
marks a breaking change. Scopes match the tree: `store`, `server`, `jobs`,
`config`, `api`, `manage`, `pipeline`, `geo`, `dashboards`, `reporting`,
`shared`, `sdk`, `web`, `cmd`, `deploy`, `ci`; omit for repo-wide changes.

Releases are cut by hand with `gh workflow run release.yml` (optional `version`
input; blank means next patch). Pushing to `main` publishes nothing. Pre-1.0:
breaking changes bump the minor. The workflow runs `make check` and the race
suite (`make test`), tags, builds the tarballs and hands off to `npx changelogithub`; container images publish
from a separate job; several commits can ship under one version; notes are
not hand-edited.

## Checks

`make check` (vet, the coverage gate without `-race`, the SQLite-free packages
with it, restore test) is what pull request CI runs; run it before pushing.
`make build` compiles; `make test` runs the whole suite under `-race`, about
ten times slower because modernc SQLite is instrumented too, so CI runs it only
in the release workflow. CI runs on pull requests only (nothing on push to
`main`) and skips jobs whose inputs a pull request does not touch: the path
rules are in the `changes` job of `.github/workflows/ci.yml`. The
SDK bundle `internal/server/twillingate.js` is committed: after any change in
`sdk/`, run `npm run build` there and commit the result, or CI's drift check
fails. The `web/` build is not committed: `make ui` (a prerequisite of
`build`, `test` and `check`, so they need Node 22) builds it into
`internal/reporting/ui/`, which the binary embeds; a bare `go build` without
it serves a 503 at `/app/`. Only `internal/reporting/ui/components.json` is
committed, since the Go server validates widgets against it: after changing a
widget contract, commit the regenerated file, or CI's `web` job (typecheck,
tests, build, then the drift check on that file) fails. Its `e2e` job runs
the Playwright suite against a built binary (`cd web && npm run e2e`).

## Documentation

Three pages, all served over MCP, all the contract rather than a summary:
`docs/twillingate.md` (`docs://twillingate`) for using twillingate,
`docs/reporting.md` (`docs://reporting`) for building dashboards, and
`docs/deployment.md` (`docs://deployment`) for running it. `docs/` holds those
three plus `docs/plausible/README.md`, which stays separate because it
documents bytes the collector serves at `/js/plausible-shim.js` and a test
binds it to them; do not add files there (dashboard material belongs in
`docs/reporting.md`). Per-migration upgrade runbooks live in
`deploy/UPGRADES.md`.

Update in the **same commit** as the change:

| Change to | Update |
| --- | --- |
| reserved keys or event names (`internal/server/ingest.go`), the wire format (`handlers.go`) | `docs/twillingate.md` |
| the SDK's public API, `data-` attributes or defaults (`sdk/src/`) | `docs/twillingate.md` |
| project fields or the CLI/MCP surface that edits them (`internal/manage/`) | `docs/twillingate.md` |
| MCP tools, REST routes, resources (`internal/api/ops_*.go`, `expose.go`, `rest.go`, `resources.go`) | `docs/twillingate.md` |
| reporting tools, routes and resources (`internal/api/ops_reporting.go`) | `docs/reporting.md` |
| components (`web/src/components/widgets/`) | the component table and examples in `docs/reporting.md` |
| queryable views (`internal/store/sqlite/migrations/`) | `docs/twillingate.md` and `schemaViews` in `internal/api/resources.go` |
| views the system dashboards read (`internal/store/sqlite/migrations/`) | the system widgets, which `TestSystemDashboards` in `internal/reporting/system_test.go` runs |
| a migration with pre-checks or a visible change on upgrade day | `deploy/UPGRADES.md` |
| environment variables (`internal/config/`) | `docs/deployment.md` |
| install, upgrade, replication or restore procedure (`deploy/`, `Makefile`) | `docs/deployment.md` |
| API auth modes or client setup (`internal/api/auth.go`, `oauth*.go`) | `docs/deployment.md` |
| the Evidence dashboards (`internal/dashboards/`, `evidence/`) | `docs/deployment.md` |

`internal/api/docs_sync_test.go` binds part of this in both directions
(reserved keys, tool names, routes, views, SDK symbols, environment variables,
the closed vocabularies, components and their worked examples), reading the
specific table that claims each fact.
The rest is on you.
