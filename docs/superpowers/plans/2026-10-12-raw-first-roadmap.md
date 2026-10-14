# Raw First Roadmap

**Spec:** `docs/superpowers/specs/2026-10-12-raw-first-design.md` (23 decisions, all
questions settled, measurements done).

The spec is six releases, each a working server on its own and each
changing the code the next one touches, so each gets its own detailed plan
when its predecessor lands. This file fixes the order, the scope of each,
and the interfaces between them. The first plan is written:
`2026-10-12-late-window.md`.

## Global constraints (from the spec)

- Envelope: 1M events a month per server, 3 years of events (`RETENTION_DAYS`
  400 by default, up to 1095), a 1 GB / 2 CPU server runs it.
- One binary; from PR 2 on it needs cgo and a glibc image (D13): release
  targets become linux/amd64 and linux/arm64 built natively; `linux/arm`
  goes; the Docker runtime image leaves Alpine.
- Events never enter SQLite once PR 6 lands; until then they are written
  to both (PR 3 → PR 5), so every release is reversible by config.
- Files are the truth (D1): anything derived can be deleted and rebuilt.
- The engine is a child process (D8): `ENGINE_MEMORY_LIMIT` 512 MB, threads
  = CPUs, `ENGINE_MAX_CHILDREN` 2, the log loaded once per child, widgets one
  or two at a time per child.
- Conventional Commits; `make check` before every push; a change to a
  contract page lands in the same commit as the code.

## PR 1. The late window (D3): fixes the raised-window bug

Plan: `2026-10-12-late-window.md`. `INGEST_LATE_DAYS` (default 1, at least 1,
at most `RETENTION_RAW_DAYS`) replaces the raw window as `clampTS`'s lower
bound. No migration. Un-skips nothing: the skipped store test is deleted,
since the scenario can no longer be produced through ingest; the server test
`TestEventAgeClampUsesLateWindow` and a 400-day raw window test take its
place. Runbook in `deploy/UPGRADES.md` with the prod check for damaged days.

## PR 2. cgo, the engine child, the build

Plan: `2026-10-12-engine-child.md`.

- New package `internal/engine` (rank: a leaf above `shared`; `app` and
  `jobs` import it): spawns `twillingate engine` as a child of the same
  binary, speaks one JSON object per line each way, `Exec(sql) error` and
  `Query(sql) (rows, error)`, `memory_limit`, `threads`, `temp_directory`,
  a machine-wide semaphore of `ENGINE_MAX_CHILDREN` lock files in the data
  directory, exit after idle. The child confines itself to the data
  directory (`enable_external_access=false`, `allowed_directories`).
- `cmd/twillingate/engine.go`: the `engine` subcommand (hidden).
- Dependency: `github.com/duckdb/duckdb-go/v2` (D13). Build: `CGO_ENABLED=1`,
  `Makefile` `build-all`/`dist` without `linux/arm`, `vet` without
  `GOARCH=arm`; `release.yml` builds arm64 on an arm64 runner; Dockerfile
  runtime image `debian:bookworm-slim` (or distroless cc) built per
  platform, no cross-compile; `scripts/archive-bench/engine` is the model.
- Measured on landing: `engine` spawn-to-ready on CI's runner; binary size.
- Docs: `docs/deployment.md` (platforms, image, memory: 1 GB, the child's
  limit, `GOMEMLIMIT` not covering it), `deploy/UPGRADES.md` (32-bit ARM
  dropped, Alpine dropped).
- Done when: the binary runs on linux/amd64 and arm64 with the child
  answering `SELECT 1`, and nothing else changed for users.

## PR 3. The log, the dump, the merge, retention by file (D2, D5, D6, D7)

Plan: `2026-10-12-raw-log.md`.

Events are written to SQLite as today **and** to the log; nothing reads the
files yet.

- New package `internal/archive` (same rank as `engine`; imports `engine`):
  - `Layout`: `<data dir>/raw/<project_id>/<YYYY-MM-DD>.log`,
    `<YYYY-MM-DD>.parquet`, `<YYYY-MM-DD>.<n>.parquet` (late segments),
    `<YYYY-MM>.parquet`; `sessions/`, `identities/`, `aliases/` reserved.
  - `Log`: `Append(project, day, []store.Event)` writes flat NDJSON lines
    (Q10: Parquet column names, `attr_<key>` string values, absent fields
    omitted, `v: 1`), one `O_APPEND` descriptor per open log, `Sync()` at
    each pipeline flush; `Truncate` to the last newline at boot.
  - `Dump(project, day)`: `COPY (SELECT DISTINCT ON (id) * FROM
    read_ndjson(log, sample_size=-1) ORDER BY family, ts) TO tmp` via the
    engine, fsync, rename, delete the log. Day close for yesterday's logs;
    a late day's log becomes a `<day>.<n>.parquet` segment.
  - `Merge(project, month)`: once the month's last day is past
    `INGEST_LATE_DAYS`: all its day files and segments into
    `<YYYY-MM>.parquet` ordered by `family, ts`, `DISTINCT ON (id)`; delete
    the inputs.
  - `Retain(project, before)`: delete month files older than
    `RETENTION_DAYS`.
  - `Files(project, from, to) []string`: the file list for a range, from
    the directory (no index).
- `pipeline`: the flush also calls `Log.Append` + `Sync` (the interface
  `server.Enqueuer` is unchanged; `app` wires both).
- `jobs.RunDailyPass`: dump, merge, retain, with jitter.
- `RETENTION_DAYS` (new, default 400) applies to files; the SQLite pair
  keeps working until PR 6.
- Docs: `docs/deployment.md` (the directory, what to back up and in which
  order, D7), `docs/twillingate.md` (the line format is internal; the
  files' schema is the contract from PR 4 on).
- Done when: a day of ingest produces a day file DuckDB reads back with the
  same row count as SQLite's; a month merges; retention deletes; the
  reproduction tests cover a torn last line and a duplicate id.

### Entry conditions from PR 2's review

Before the engine has a caller:

- Query and widget requests run only read-only statements. DuckDB's allowed
  directory is read-write, so `COPY ... TO` could overwrite or delete files
  in `raw/`. Only the dump writes, ideally from its own child; set
  `max_temp_directory_size` too.
- A request cut short by `Close` gets `ErrClosed`, not a `QueryError` (the
  child is fine).
- An idle child holds its slot for `IdleExit` (3 minutes). On a split
  install with `ENGINE_MAX_CHILDREN=1` the other process waits for it, so
  release or retire the child early.
- Spawn `/proc/self/exe`, or check a protocol version in the ready line:
  `os.Executable` names the new binary after an in-place upgrade, before
  the restart.
- The installer refusal test runs only by hand (`make test-install`);
  consider running it in CI.

## PR 4. The tables, the engine's view, `query` on the files (D4, D8, D21)

Plan: `2026-10-13-raw-query.md`. It keeps `query` on SQLite by default in
this release (`source: "files"` opt-in) and keys unsanitized; see its
"Decisions".

- `engine.View(project, from, to)`: `events` = `read_parquet(files,
  union_by_name=true) UNION ALL BY NAME hot`, `hot` loaded once at spawn
  from the open logs; `views`, `product`, `measures` as views on it;
  `project_id` present. Rules (renames, merges, blocks: D6) handed as
  mapping rows and applied in the views.
- The `query` MCP tool and `/api/.../query` run on the engine; timeout
  default 30 s; OOM gets its message (D21). `readsql` guards replaced by the
  child's confinement.
- `docs/twillingate.md`: the `events` table's columns (the contract), the
  DuckDB dialect for `query`, with the SQLite `events`/`v_*` section kept
  until PR 6 and marked.
- Done when: an agent's 3-year question over the files answers through
  MCP within the measured times; widgets still run on SQLite.

## PR 5. Dashboards on raw, the derived tables, the migration (D16–D19, D22)

- `archive.Derive(project, day)`: `sessions/<day>.parquet` (D16),
  `identities/<project>.parquet` and `aliases/<project>.parquet` (D17,
  D18) at day close.
- The 59 system widget `.sql` files ported to DuckDB SQL over `events`,
  `sessions`, `identities`, `aliases`; `internal/reporting` validation and
  loading on the engine (`components.json` unchanged); widget SQL from
  users validated against the engine with the same `LIMIT 0` / `LIMIT 5`
  pass; the `identities`, `views_*`, `product_*`, `measures`, `retention`
  tools on the engine.
- Ingest keeps `user_name`, `group_name` and every identifier on the row
  (D17, D18); `UpsertIdentities` stops.
- `migrate`: raw days streamed from `events` into logs and dumped (D19).
- SDK: visit context on `$form_submit` (D22); `SessionAt` removed.
- `docs/reporting.md`: the component table's examples in the new dialect,
  the tables, the breaking change; `deploy/UPGRADES.md`: the upgrade day
  (raw history migrates, aggregate history does not), `VACUUM`.
- Measured on landing: the Views dashboard over 1 year on a 2-CPU server
  with the sessions table (target under 2 s).
- Done when: every system dashboard renders from the files with the
  numbers the SQLite path gave for the same range.

## PR 6. Removal (D11, D12, D20, D23)

- Stop writing events to SQLite; drop `events`, every `agg_*`, `actors`,
  `identities`, `agg_retention`, the `v_*` views, `v_events_flat`, the
  rollups, the nightly recomputes, `usage_attributes`, declared breakdowns
  and the caps; `limits` counts the last 30 days from footers and logs
  (D20).
- Config: `RETENTION_RAW_DAYS`, `RETENTION_AGGREGATE_DAYS` and aliases,
  `ATTRIBUTE_VALUES_TOP_N`, `ATTRIBUTE_BREAKDOWNS_MAX`, `IDENTITIES_TOP_N`
  reported and ignored for one release (D23).
- `sqlcheck`'s SQL set shrinks to the metadata queries; `storetest`
  snapshots regenerate; `docs/twillingate.md` loses the SQLite events
  section; #173 closed; #175 reduced to its allowlist, adjusted to D4.
- Done when: a fresh install never creates an events table in SQLite, and
  `make check` is green with the smaller store.

## Order of measurements still to take

| When | What | Target |
|---|---|---|
| PR 2 | spawn-to-ready on the release runners | under 100 ms |
| PR 3 | a cap day's log appended and fsynced per flush, on a 2-CPU server | no visible ingest cost |
| PR 5 | Views dashboard, 1 year, 2 CPUs, with `sessions` | under 2 s |
| PR 6 | idle RSS of a fresh install | ~17 MB, as today |
