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

Split on 2026-10-13 into four releases, each a working server, each with
its own plan written when its predecessor lands. 5d is the one breaking day.

Two corrections to the spec, found while planning:

- **D18: the server cannot tell a visitor id from an install id.** The SDK
  sends its persisted visitor id and a declared install id both as
  `$install_id`, and ingest drops `$install_id` from the row whenever a
  `$user_id` outranks it as the actor. So the row gains `install_id`, and
  `aliases` holds `(user_id, install_id)` pairs; there is no `visitor_id`
  column.
- **D22: the SDK does not know the visit today.** It sends each view's
  referrer and UTMs but keeps none of them, tracks no entry page, and
  never makes a session id; `SessionAt` finds all of it on the server. 5b
  makes the SDK keep the visit.

### PR 5a. Everything on the event; sessions, identities, aliases (D16–D18)

Plan: `2026-10-13-raw-derived.md`.

- `store.Event` gains `InstallID`, `UserName`, `GroupName`; ingest and the
  form handler set them; the log line and the tables gain `install_id`,
  `user_name`, `group_name`. SQLite does not store them, and
  `UpsertIdentities` stays: the SQLite widgets read `identities` until 5d.
- The daily pass derives, per project, `sessions/<day>.parquet` (then
  `sessions/<month>.parquet` once the month merges), `identities.parquet`
  and `aliases.parquet`, each rewritten whenever the events files under
  it are newer.
- The reader exposes `sessions`, `identities` and `aliases`: the derived
  files, plus what they do not cover yet (the open logs, a day dumped
  since) computed on read.
- Docs: the files SQL section, Identity, the directory in
  `docs/deployment.md`.
- Done when: sessions from the files give the numbers `agg_views_daily`
  gives for the same views, and identities the names and counts SQLite
  gives.

### PR 5b. Form attribution from the files (D22, revised)

Plan: `2026-10-14-raw-visit.md`. D22 as written (the SDK sends the visit,
the server looks nothing up) would lose attribution in the default setup:
an anonymous instance without consent keeps nothing across page loads,
and a plain HTML form has no SDK. The user chose, on 2026-10-14:

- A job in the ingest process attributes each submission about a minute
  after it arrives: the submitter's last non-direct session within
  `ATTRIBUTION_DAYS` (default 7), read from `sessions`, `views` and
  `aliases` (5a) in one engine query; the event takes the newest view's
  kind, group, session, environment and consent. The `$form_submit`
  event is written by the job. Migration 041 queues submissions in
  SQLite; `SessionAt` and its store method go.
- Forms accept `$referrer` and `$utm_*`; when sent, they are the source.
- SDK (`feat!`): with consent an anonymous instance keeps a visitor id
  and sends it as `$install_id`, so its visits connect across days; the
  connection hash of an anonymous visitor without consent changes at
  midnight UTC, which bounds their lookback.
- Correction: the environment is declared by the client and only
  validated, never read from the User-Agent.
- Docs: `docs/twillingate.md` (forms, Identity, consent),
  `docs/deployment.md`, `deploy/UPGRADES.md`.

### PR 5c. Raw rows migrate into the files (D19)

Plan: `2026-10-14-raw-migrate.md`. Stacked on 5a, not 5b, which it does
not touch. Not run at boot: a one-time command, `twillingate events
import`, run with the ingest service stopped (decided 2026-10-14); the
ingest process locks `raw/` so the two never write at once.

- Once per install, recorded in `meta`: every raw day SQLite holds that
  the files lack is streamed out of `events` as log lines (rows whose id
  the files hold are skipped) and dumped like any other day. SQLite keeps
  writing `events` until PR 6, so nothing is dropped.
- History older than the raw window exists only as aggregates and does
  not migrate; `deploy/UPGRADES.md` says so before 5d makes it matter.
- Done when: the files hold, per day and project, every row SQLite holds.
- Measured on 2026-10-14, on the bench set (`scripts/archive-bench`: 985,500
  rows over 30 days, 591,157 in project 1 and 394,343 in project 2, 240k
  view actors; a 606 MB database), on the N150 at load average 3 to 10
  from other sessions: `twillingate migrate` 1.2 s; `events import`
  **100 s** (271 MB peak RSS), leaving 734 MB of closed logs under `raw/`;
  the first `serve`, whose boot pass dumps, merges and derives them, took
  **390 s** to settle (child peak RSS about 950 MB), leaving 57 MiB of
  Parquet, a tenth of the database. Until the pass ends every query over
  the files is slow (`deploy/UPGRADES.md` says so). One run, no repeat.

### PR 5d. The switch: dashboards and tools on the files (D8)

Plan: `2026-10-14-raw-switch.md`. Stacked on 5c (#183); 5b (#184) is a sibling.

Breaking (`feat!`): widget SQL becomes DuckDB SQL over `events`,
`sessions`, `identities` and `aliases`.

- The engine binds `:project`, `:from`, `:to`; reporting's sources take an
  interface instead of `*readsql.DB`; a widget reads its project's files
  for its range only; table filters (`tw_cell`, `tw_num`) as DuckDB macros.
- The 59 system widgets ported; `identities`, `views_*`, `product_*`,
  `measures` and `retention` tools on the engine; `query` defaults to
  `files`; `UpsertIdentities` stops.
- Users' widgets that fail on the engine stay and show their error with a
  hint to rewrite them; the upgrade logs each one, and
  `deploy/UPGRADES.md` says how to find them.
- Docs: `docs/reporting.md` (the component examples in the new dialect,
  the tables), `docs/twillingate.md`, `deploy/UPGRADES.md`.
- Measured on landing: the Views dashboard over 1 year on a 2-CPU server
  (target under 2 s). **Missed, 5 times over** (below).
- Done when: every system dashboard renders from the files with the
  numbers the SQLite path gave for the same range.

**Measured on 2026-10-14 (the 5d plan's Task 8):** `TestMeasureDashboards`
(`go test -tags measure -run TestMeasureDashboards -v -timeout 2h
./internal/app`) generates 12 months of one project, 1M rows a month (12M
rows, 478 MB of zstd Parquet in 12 month files; 50% views in sessions of 1
to 7 views, 40% product events with 8 attributes, 10% measures), derives
its `sessions/`, `identities` and `aliases` files with the daily pass
(11.5 s), and runs every SQL widget of a system dashboard through
`reporting.Service.WidgetData`, cache off, over the last 365 days (Views,
20 widgets, 5.9M views, 2.5M visitor-days: the sum of per-day distinct
counts, from 300k distinct actors) or 90 (Retention, 5 widgets). The
engine child runs at 2 threads and `ENGINE_MEMORY_LIMIT=512MB`, on an Intel
N150 (4 CPUs, shared with other sessions: load average 3.6 to 8.4 during
the run, which makes the times upper bounds; the OS page cache was warm,
the files having just been written, which pushes the other way). The
generator is a model, not real traffic: 300k actors, no bots, arrivals
uniform over the month. The first query builds the
tables in 76 ms; `reporting.Migrate`'s check of all 59 widgets at `LIMIT 0`
takes 0.8 s.

| Dashboard, mode (threads 2) | Wall, best of 3 passes | Child peak RSS |
|---|---|---|
| Views, one at a time | **10.8 s** (passes: 10.8, 10.8, 11.5) | 772 MB |
| Views, two at a time (engine `parallel` 2) | 10.9 s | 815 MB |
| Views, all 20 at once, as a browser sends them (`parallel` 1) | 18.6 s (load rose to 8) | 776 MB |
| Retention, 90 days, one at a time | 10.2 s (passes: 12.8, 10.2, 11.0) | 1,211 MB |
| Retention, two at a time | 9.3 s | 1,275 MB |
| Retention, all 5 at once | 10.2 s | 1,420 MB |

Four threads, same data and load (2 passes per mode, not 3; the
all-at-once mode ran at engine `parallel` 1, not the default of 2 for 4
threads): Views 7.2 s one at a time, 7.0 s all at once; Retention 7.3 s and
9.1 s. More threads on a loaded 4-CPU box helped by about a third; the
target is still missed by about 3.5 times.

The child exceeded the 512 MB limit: Views peaked at about 0.8 GB,
Retention at 1.2 to 1.4 GB (the limit covers DuckDB's buffers, not
everything the process holds).

Views, one at a time (first pass), slowest first: `top-pages` 1,631 ms,
`display-resolutions` 878, `browsers` 819, `devices` 804, `operating-systems`
713, `hosts` 690, `visitors-and-views` 674, `visitors-by-kind` 664,
`consent-states` 646, `visitors` 596, `countries` 584, `visitors-by-country`
581, `consent-share` 551, `referrers` 514, `campaigns` 192,
`visitors-by-app-version` 167, `views` 48, `avg-session-length` 33,
`bounce-rate` 30, `avg-session` 27. The four that read `sessions` or only
count rows take 27 to 48 ms; 14 of the others scan a year of
views for `count(DISTINCT actor_id)` per day and dimension, 0.5 to 1.6 s
each (`campaigns` 192 ms and `visitors-by-app-version` 167 ms scan less),
and add up. Retention's five widgets take 2.1 to 3.4 s each: their `acts` CTE
has no lower bound on the day (`day <= :to + 45` only), so each scans the
project's whole history up to the range's end to find a visitor's first
day, whatever the preset. The fix is a follow-up, not 5d's: the sum of the
widgets, not one outlier, is the cost.

Retention fixed in 5d itself (2026-10-19): most of its cost was every
anonymous actor in that scan, though only users and installs join a
cohort. With `acts` restricted to `actor_kind IN ('user', 'install')`,
Retention at 90 days takes 2.1 s one at a time and 1.3 s all at once, and
the child peaks at 113 to 181 MB (load 5.5). Parity is unchanged. Views and
Product (whose `attribute-values` runs out of memory at a year) are
`docs/superpowers/specs/2026-10-19-raw-daily-design.md`'s.

**Second data point, the bench set (30 days at the cap, realistic
cardinality):** the same test on the files the import and the boot pass
left (985,500 rows; project 2 `site` has the views, 394k of them from 202k
anonymous actors; project 1 `app` has 98k views, 444k product events and
49k measures from users), `MEASURE_DB=… MEASURE_PROJECTS=2,1
MEASURE_RUNS=1:7,1:30,5:30 MEASURE_TO=2026-10-11`, threads 2, 512 MB, cache
off, load average 1.8 to 2.4 at the start of each mode (lighter than the
first run). **The Views dashboard meets the target at this size.** Best of
3 passes, wall time:

| Dashboard, range | One at a time | Two at a time | All at once | Child peak RSS |
|---|---|---|---|---|
| Views, project 2, 7 days | 0.59 s | 0.37 s | 0.57 s | 136–152 MB |
| Views, project 2, 30 days | **1.21 s** | 0.85 s | 1.17 s | 201–244 MB |
| Views, project 1, 7 days | 0.49 s | 0.29 s | 0.47 s | 117–124 MB |
| Views, project 1, 30 days | 0.65 s | 0.38 s | 0.61 s | 133–140 MB |
| Retention, project 2, 30 days | 0.39 s | 0.30 s | 0.38 s | 145–207 MB |
| Retention, project 1, 30 days | 0.73 s | 0.64 s | 0.72 s | 200–282 MB |

The slowest Views widgets on project 2 at 30 days, one at a time:
`top-pages` 170 ms, `display-resolutions` 91, `browsers` 87,
`operating-systems` 88, `countries` 82; the rest 16 to 75 ms. First query
(build) 75 to 117 ms. Two at a time is faster than one at a time here (the
child has 2 threads and a widget this size uses fewer). Empty
results, not worked around: in Retention, `signed-in-retention`,
`install-retention` and `cohorts` of project 2 return no rows, and
`install-retention` of project 1 too; project 2's actors are all
anonymous and retention counts users and installs only, and project 1's
views and product events come from users, with no installs. The cause is the data's actor kinds, not the clock (the data ends
on 2026-10-11 and `current_date` was 2026-10-14; I did not isolate its
effect). In Views, `visitors-by-app-version` returns no rows on both
projects (no app versions sent), `referrers` and `campaigns` none on
project 1, and `consent-share` reads 0 (no consent sent). Read with the first run: the cost grows with the rows
scanned, about 1.2 s for 400k views in 30 days against 10.8 s for 6M in a
year.

How far the first run represents a real install: the stat widgets (`visitors`,
`views`) also read the previous period, so a second-year install scans
about twice as much for those; Retention scans the whole history, so a
3-year install costs about 3 times as much.

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
- Plan: `2026-10-19-raw-removal.md`. It changed this section in three
  places: `limits` counts the last 30 days with one engine query (cached
  5 minutes) instead of summing footers; `cap_usage` is removed outright;
  migration 042 refuses while un-imported raw rows remain, and
  `events import` migrates only through 041.
- Idle RSS taken 2026-10-20, fresh install, `serve` with only
  `DATABASE_DSN` and `INGEST_ADDR`: 38.4 MB at 60 s, with the engine child
  (62 MB) still alive from the start-up pass; 37.6 MB at 230 s, after the
  child's 3-minute idle exit. Target ~17 MB not met.

## Order of measurements still to take

| When | What | Target |
|---|---|---|
| PR 2 | spawn-to-ready on the release runners | under 100 ms |
| PR 3 | a cap day's log appended and fsynced per flush, on a 2-CPU server | no visible ingest cost |
| PR 5d | Views dashboard, 1 year, 2 CPUs, with `sessions` | under 2 s. **Taken 2026-10-14: 10.8 s** (Retention, 90 days: 10.2 s). Follow-up needed. Bench set, 30 days: 1.2 s, met |
| PR 6 | idle RSS of a fresh install | ~17 MB, as today. **Taken 2026-10-20: 38.4 MB at 60 s (engine child still up, 62 MB), 37.6 MB once the child exited.** Not met |
