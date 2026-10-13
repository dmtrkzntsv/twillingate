# Raw first: raw events as the only source of truth

Status: draft. D10 is settled by measurement (C, over Parquet files);
the storage and ingest design (D2–D3, D7–D8) was rewritten 2026-10-12 after
it. Numbers marked *estimate* are unmeasured.
Date: 2026-10-12

## Requirements

twillingate is positioned as a simple analytics solution for builders.

- **Product analytics, filtering and grouping by several attributes at
  once.**
- **Performant:** most dashboard queries are fast; some (a complex
  aggregation, a 3-year question) may be slower.
- **Easy to install and maintain**, as SQLite is: one binary, a file or
  two, nothing else to run.
- **Boundary:** up to about 1M events a month per server.

## Envelope

One server takes up to **1M events a month** and keeps raw events for up to
**3 years**: 36M raw events at the cap. Within that:

- reports are fast at any range;
- any question can be asked of the raw events: a user's or group's timeline,
  when an event was first seen, when a user unsubscribed, any combination of
  attributes, exact distinct users over any range;
- nobody has to declare anything up front to get a breakdown;
- ingest stays fast;
- the SQLite file stays small and flat, so backups, restores and vacuums
  stay cheap.

## Problem

- **Reports scan the raw window on every load.** Every `v_*` view is
  `agg_* UNION ALL` a live half computed from raw rows for every day still in
  the raw window. Prod measured ~65 µs of CPU per raw event per Views
  dashboard load: at 1M events a month, today's 30-day window is ~65 s of
  CPU per load before the cache; a 1-year window is ~13 min.
- **A rollup throws the questions away.** When a day leaves the raw window
  (`RETENTION_RAW_DAYS`, default 30) it is rolled up and its raw rows are
  deleted. What survives is one row per (day, event, key, value) with a
  count, unique users and unique groups:
  - only **declared** keys are rolled up; an undeclared key's values are
    gone, and once no raw day carries it the key leaves the catalog too;
  - each key is counted on its own, so **combinations** (`status=true AND
    reason=price`, or a key × `$country`) cannot be answered;
  - **distinct users don't add up** across days or values;
  - each (day, event, key) keeps its top 100 values; the rest fold into
    `(other)`;
  - **who** sent an event is gone: no timeline, no "when did user X opt
    out" (the motivating case of #173).
  Declaring a key later recovers only the days still raw.
- **Getting more out of aggregates means more declarations.** Declared
  breakdowns, `ATTRIBUTE_BREAKDOWNS_MAX`, `ATTRIBUTE_VALUES_TOP_N` and
  `IDENTITIES_TOP_N` exist only to keep aggregates small. Declaring
  combinations up front was considered and rejected: it complicates the user
  journey.
- **The only lever is the raw window, and it grows SQLite.** A raw row is
  444 B on prod and 615 B on the attribute-heavy synthetic set: 16–22 GB for
  36M rows in one file.
- **The nightly pass recomputes the whole raw window every night**: actors,
  retention cohorts, identities, `usage_attributes` and `usage_history`'s
  counts and attribute stats.
- **Raising `RETENTION_RAW_DAYS` is probably unsafe today** (inferred from
  the code, not tested; Next steps, step 0). `clampTS`
  (`internal/server/ingest.go`) bounds a timestamp to `received −
  RETENTION_RAW_DAYS`, which is what keeps a late event out of an
  already-rolled-up day. Raising the window moves that bound back over days
  whose raw rows are already gone. A late event lands there as the day's only
  raw row; when the day ages out again, the rollup recomputes it from that
  row alone and `INSERT OR REPLACE` overwrites the day's full aggregates.
  Prod's raw window was raised recently.

## How it works today

1. **Ingest.** The pipeline buffers events and writes a batch about every
   10 s into one `events` table (views, product, measures), keyed
   `(family, project_id, day, id) WITHOUT ROWID`, with no secondary index.
   Attributes are one JSON text column.
2. **Nightly pass** (`internal/jobs/jobs.go:RunDailyPass`, on boot and at
   03:00 UTC), in this order, which is load-bearing:
   1. For every raw day of every project, today included: `UpsertActors`,
      `AggregateRetentionDay`, then `AggregateIdentityDay` (today skipped).
   2. `DropBlockedAggregates`.
   3. Every day older than `today − RETENTION_RAW_DAYS` is rolled up into
      `agg_*` (`aggregate_views.go`, `aggregate_product.go`,
      `aggregate_measures.go`) and its raw rows deleted, in one transaction
      per project-day-family.
   4. Aggregates, actors and identities older than `RETENTION_AGGREGATE_DAYS`
      (default 400) are pruned.
   5. `RebuildFlatView`, `IncrementalVacuum`, `RecordUsageHistory`; the
      nightly `countReceived` drops `usage_attributes` rows of rolled-up days
      and `pruneCatalog` drops undeclared keys no raw day carries.
3. **A day is aggregated exactly when its raw rows are gone.** Nothing
   records that a day was rolled up. The `v_*` views (except
   `v_identity_daily`), the rollups (`n == 0` means "already done"),
   `usage_history`, `usage_attributes`, the `usage` tool and
   `forms.go:deleteEvent` all rely on it.
4. **Attributes in SQL.** `v_events_flat` has an `attr_*` column only for
   declared keys; anything else needs `json_extract(attributes, …)`.

## Decisions (proposed)

### Storage

- **D1. Raw events are the only source of truth.** Everything else
  (aggregates, caches, counters, actors, cohorts) is derived from them and
  can be dropped and rebuilt. No step deletes information that a later
  question might need, except retention and erasure.
- **D2. Events never enter SQLite. An append-only log per project and
  day, then Parquet.** SQLite keeps everything else (projects, dashboards,
  forms, submissions) and nothing about events. Everything on the event
  path is an append or a new file; nothing is rewritten in place until the
  two merges, and both merge data that is already final. The whole event
  side is four scheduled actions: fsync the log at each flush, dump at day
  close, merge at month close, delete at retention.
  - **Log:** ingest appends each event as one NDJSON line to
    `<data dir>/raw/<project_id>/<YYYY-MM-DD>.log` and fsyncs at every
    pipeline flush (~10 s, the window the in-memory buffer already has
    today). **The line has the shape of the Parquet row** (settled
    2026-10-12, Q10): the column names as fields, attributes flat as
    `attr_<key>` with string values, fields omitted when absent, a `v`
    field for the format. Rules (rename, merge, block) and the key
    sanitizing of `flatview.go` are applied in Go from the registry
    snapshot before the write, the only place names are decided. Appends
    go through one `O_APPEND` descriptor; at boot a log is truncated to its
    last newline, so a line torn by a crash never reaches a reader. About
    1.3 KB a line as DuckDB exports it, less with absent fields omitted,
    ~40 MB a day at the cap, deleted once dumped.
  - **Dedup** belongs to the merges, not to ingest: the dump writes
    `DISTINCT ON (id)` over the day's log and the month merge over the day
    file and its late segments, so every duplicate is gone once a file is
    final, with no state. Ingest keeps a small set of the ids of the last
    ~10 minutes to keep SDK retries out of the log; what slips through is a
    duplicate visible in a dashboard until the next dump.
  - **Dump:** at day close the engine (D8) writes the day's log as
    `<YYYY-MM-DD>.parquet` with one statement and no schema of its own:
    `COPY (SELECT DISTINCT ON (id) * FROM read_ndjson(log, sample_size=-1)
    ORDER BY family, ts)`; DuckDB infers the columns from the whole file
    (measured: no slower than sampling) and types the built-ins from JSON
    (`ts` TIMESTAMP, `day` DATE, `id` UUID); every `attr_*` is VARCHAR
    because the log writes them as strings, so no two files can disagree
    on a type. 0.2 s for a cap day; then the log is deleted. Until then queries read the log directly
    (`read_ndjson`): 150 ms per query for a full cap day of one project
    (25 MB), proportionally less below the cap. If that shows in a
    project's dashboards, the same dump can run earlier, on a size or time
    threshold, writing the lines since the last dump as a new segment
    `<YYYY-MM-DD>.<n>.parquet` and rotating the log; segments merge into the
    day file at day close. Per project, if ever needed; not in the first
    version.
  - **Merge:** once a month is complete and past the late window (D3), its
    day files merge into `<YYYY-MM>.parquet`, rows ordered by family then
    time (~1 s per 1M rows). One file per project and month, no family
    split: measured the same speed as per-family files with a quarter of
    the files and a smaller archive, provided the sort order is kept.
  - **No index.** The directory is the list of files (about 100 per
    project at 3 years), a file's name is its day or month, and its Parquet
    footer holds its row count, which is what `usage`, `limits` and the
    hosted allowance (Q7) read. A manifest table in SQLite would be a cache
    of this; add it only if directory scans or footer reads show up.
  - Each event is written four times in its life (log, segment or day
    file, day file, month file), about 4 × 1.5 KB. Rewriting the day file at
    every flush, considered first, costs up to 14 GB a day at the cap and
    is rejected.
- **D3. A late window, separate from retention.** `clampTS`'s lower bound
  becomes a late window, `INGEST_LATE_DAYS`, default 1 day, configurable
  (settled 2026-10-12), no longer `RETENTION_RAW_DAYS`. An event older than
  that is clamped into the window's first day, as today. A late event for a
  closed day is appended to that day's log (reopened, or a new one) and
  dumped as a new segment of that day; nothing is rewritten. A month merges
  only after its last day's late window has passed, so a month file is
  final.
- **D4. One wide table per project, with every attribute.** Each project
  exposes `events` (and `views`, `product`, `measures` as views on it):
  today's typed columns, `project_id` included so a file says where it
  belongs wherever it is copied, plus **one column per attribute key
  the project's catalog has seen**, declared or not. Parquet stores each key
  as a real, dictionary-encoded column, so a wide, sparse table costs
  little; a column that is null for a whole family costs bytes. A file
  written before a key existed reads it as null (`union_by_name`). The
  mapping from the log's JSON to the columns is one SQL statement in the
  dump (D2), the only place the schema is spelled out. Key sanitizing and
  collisions follow `flatview.go`'s rules.
- **D5. Retention deletes whole files.** `RETENTION_RAW_DAYS` keeps its
  name, means raw kept, and goes up to 1095 days. The pass
  deletes month files entirely older than it. No `DELETE` over millions of
  rows, no free pages, no `VACUUM`.
- **D6. Files are immutable; rules apply when read.** Event and attribute
  renames and merges map old names on read (`rules` already says how);
  blocked names are filtered on read and dropped when a file is next
  rewritten. Data that must really go (a user's erasure, a submission, a
  project) is removed by rewriting the affected files: copy without the
  rows, fsync, rename. A purged project deletes its directory.
- **D7. Backups are the operator's, by hand.** twillingate ships no backup
  code and the design no longer counts on Litestream (settled 2026-10-12).
  The data directory is the backup unit: the SQLite file (copied with
  `sqlite3 .backup` or with the service stopped), the Parquet files (final
  once written, so a plain copy at any time is correct) and the open logs
  (append-only; a torn last line is dropped on replay). Restore copies the
  directory back; nothing else to restore. `docs/deployment.md` says what
  to copy and in which order. `docs/litestream.md` and `docs/deployment.md` describe
  it.

### Reports and queries

- **D8. Dashboards query raw, through an engine that is a child process.**
  Widgets, system dashboards and the `query` tool read D4's tables through
  DuckDB (D10), which the server runs as a child process of its own binary
  (`twillingate engine`), started on the first query or dump and exited
  after a few idle minutes. One query sees month files, day files, today's
  segments and the open log as one table: `read_parquet(…) UNION ALL
  read_ndjson(…)`, the engine layer building the file list from the
  directory for the project and range. Exact distinct users over any range,
  combinations of any keys and keys nobody declared all come from the same
  rows. The child, not the driver in-process, because the measurements
  showed: DuckDB open and idle holds 37 MB and keeps what it used up to its
  limit until the limit is lowered (never returned on macOS), so a process
  that exits gives everything back, which matters to a hosting operator
  with many idle customers; a crash or out-of-memory error in a user's
  widget SQL cannot take ingest down; and the child can read the files and
  nothing else, so widget SQL never has a path to the SQLite file. The
  ingest process never initializes DuckDB and stays at today's footprint.
  Settings: `memory_limit=512MB` (at 256 MB a `COUNT(DISTINCT)` over 3
  years fails, at 128 MB from 1 year), `threads` equal to the CPUs, a
  spill directory. Unmeasured: the child's time from spawn to first result.
  - **A machine-wide cap on engine children.** Each child can reach ~700 MB
    for a few seconds, and nothing else bounds how many run at once: ten
    projects' dashboards opening together on one install, or fifty
    customers' on one host. A semaphore of lock files in a shared directory
    (`flock`, a handful of lines) holds the count under a configured
    maximum across processes; a `systemd` slice with `MemoryMax` is the
    backstop that turns the worst case into one killed child.
  - **Jitter in the daily pass.** The dumps and merges of every process on
    a host spread over a window rather than all starting at 03:00.
- **D9. No cache, no estimates.** Removed 2026-10-12: measured, every
  question answers from raw within the targets except sessionization
  (D16), so there is no per-day cache of widget results and no HyperLogLog;
  distinct users over any range are exact. (The cache design is kept in
  "Options considered".)
- **D10. Three designs, chosen by measurement: C, over Parquet files.** Go
  has no columnar SQL engine without cgo. Against the Requirements:

  | | A. SQLite only, raw kept | B. SQLite + Parquet, pure Go | C. SQLite for metadata + DuckDB for events |
  |---|---|---|---|
  | Multi-attribute queries | yes, every filter a `json_extract` per row | yes, through our own Parquet reader + in-memory SQLite | yes, real columns, any combination |
  | Dashboard over 30 days (~1M rows) | 0.1–1.8 s per widget (measured) | needs D9's cache | 3–120 ms per widget (measured) |
  | Dashboard over 1 year (~12M rows) | 1.7–23 s per widget (measured); needs D9's cache | needs D9's cache | 0.02–1.8 s per widget, 2 threads (measured) |
  | Complex query over 3 years | 29–166 s (measured) | minutes; loading into SQLite is the bottleneck *estimate* | 0.06–7.9 s, 2 threads (measured) |
  | Size at 36M events | 22.1 GB (measured; 7.6 GB with #175) | ~1.5 GB *estimate* | 2.55 GB as a DuckDB file, 1.45 GB as monthly Parquet (measured) |
  | Install | one binary, one file | one binary, a file + a directory of Parquet | one binary, two files (or a file + Parquet, Q9) |
  | Backups | Litestream | Litestream + uploading files | Litestream for metadata; events need their own (Q9) |
  | Code we maintain | D9's cache | the most: Parquet writer, compaction, pushdown, cache | the least: SQL straight on the data, likely no aggregates |
  | Cost | file size, slow long ranges | a small analytics engine of our own | cgo; no 32-bit ARM; binary +40–60 MB; a per-process memory limit; widget SQL becomes DuckDB SQL (close to Postgres; breaking for user widgets, softened because real attribute columns replace `json_extract`) |

  - **A** fails "SQLite stays small" and still needs a cache for long
    ranges.
  - **B** keeps a pure-Go build, but moves the maintenance onto us: a
    columnar query layer, compaction and caching to own, for a product
    positioned as simple.
  - **C** fits the Requirements most directly. DuckDB is to analytics what
    SQLite is to transactions: embedded, one file, no server. At 36M rows it
    answers dashboards and combinations straight from raw, with no
    aggregates, caps or declarations (Measurements). Users still download
    one binary.
  - **Decided 2026-10-12: 32-bit ARM (`linux/arm`) can be dropped.** Its
    users are Raspberry Pi Zero/1/2, Pis on the 32-bit OS (the 64-bit OS
    is recommended), older ARMv7 NAS boxes, cheap boards and OpenWrt
    routers. Still to accept for C: cgo in the release build (DuckDB ships
    static libraries for linux amd64/arm64 and macOS; arm64 builds on an
    arm64 runner or with a cross C toolchain), the widget SQL dialect, and
    backups of the events data.

  Targets at the cap, warm: a system dashboard over 1 year loads in under
  2 s; an agent's question over 3 years answers in under 30 s; a
  timeline over 3 years in under 1 s. Measured (Measurements): C meets
  the last two at 2 threads and 256 MB; a 1-year Views dashboard is ~3 s
  of widgets run one after another (sessions alone 1.75 s), under 1 s at 10
  threads. A meets none.

  Within C, the Parquet files win over a DuckDB file (Q9, measured): a
  smaller archive, the same speed, memory that falls back after queries,
  retention by deleting files, backups by copying files that never change,
  and no process lock (a DuckDB file admits one process, which breaks the
  CLI against a live server and the split install). D2–D8 describe it.

- **D16. One derived table: sessions.** Sessionization (a visitor's views
  ordered in time, a new session after a 30-minute gap) is the only query
  that misses the targets at 2 threads: 1.75 s over a year, 7 s over 3
  years, because every view in the range is sorted per visitor. At day
  close the dump also writes `sessions/<YYYY-MM-DD>.parquet`, one row per
  session (actor, kind, start, end, views, entry and exit path), ~50 ms a
  day and a third the rows of the views; today's sessions come live from
  the log. Sessions cut at midnight as today's live half does. The Views
  dashboard's sessions, bounce, duration and entry/exit widgets read it
  instead of sorting views. Derived, rebuildable from raw at any time,
  deleted and merged with the day's files.

### Hosting

- **D14. One process per customer stays.** The event side (D2) makes a
  tenant a directory, so one process could serve many, but it would need a
  router from hostname or ingest key to tenant, a tenant lifecycle and
  multi-tenant tests, and what it saves is idle RAM: a process idles at
  ~17–35 MB (Go runtime, SQLite's page cache as it fills, buffers; the
  binary's pages are shared by the OS), so 100 customers on a host idle at
  2–3.5 GB, 1,000 at 20–35 GB. Everything else costs the same either way:
  a dashboard load is one engine child, the OS caches each Parquet file
  once, the daily pass is seconds. The per-customer process keeps the
  isolation the OS gives (a crash, a leak, `MemoryMax`, `CPUQuota` and
  metrics per unit, no path from one customer's data to another's),
  independent upgrades, and the `twillingate@<name>` unit that exists.
  The two things it lacks, D8's cap on engine children and the daily-pass
  jitter, are small and wanted by a single install too. Revisit at hundreds
  of customers per host; nothing in the layout prevents it then.
- **D15. No second database for metadata.** Postgres for all tenants'
  settings would mean porting or duplicating 40 migrations, 8 triggers and
  every metadata query, and a server for self-hosters to run, in exchange
  for concurrent writers and several app servers on one database, which
  the envelope does not need. One SQLite file per customer already
  isolates tenants.

### What goes away

- **D11. Candidates to remove** once dashboards run on raw (each needs its
  own check against D10's numbers): the `agg_*` tables and rollups; the
  `v_*` live halves; declared breakdowns (the catalog's `breakdown` flag)
  and `ATTRIBUTE_BREAKDOWNS_MAX`; `ATTRIBUTE_VALUES_TOP_N` and `(other)`
  rows; `IDENTITIES_TOP_N`; the nightly whole-window recomputes (actors,
  cohorts, identities, `usage_attributes`, attribute stats), replaced by
  queries or by D9's cache; `RETENTION_AGGREGATE_DAYS` (Q6).
- **D12. Superseded work.** #175's encoding is dropped; its widget-SQL
  allowlist becomes its own change, adjusted to D4's tables. #173 (kept
  events) is closed: every event is kept.
- **D13. New dependencies.** DuckDB's Go driver
  (`github.com/duckdb/duckdb-go/v2`, cgo; prebuilt static libraries for
  linux amd64/arm64, macOS and Windows, glibc only, so the image leaves
  Alpine; `parquet`, `json` and `icu` compiled in, `sqlite_scanner` not).
  No Parquet writer of our own: DuckDB writes the files. CLAUDE.md allows a
  third-party dependency only from a very reputable vendor; the DuckDB
  Foundation qualifies. With 32-bit ARM dropped, the release targets become
  linux/amd64 and linux/arm64 (`Makefile` `dist`, `vet`'s `GOARCH=arm`
  check, `release.yml`, the installer and `docs/deployment.md`).

## Use cases

How each use case found in the code (2026-10-12 inventory) is served
raw-first.

### Reports

| Use case | Today | Raw-first |
|---|---|---|
| Views charts and KPIs (visitors, bounce, session length) | agg + raw days in range, re-sessionized per load; visitors/views read 2× the range | query `views` over the range (D8), cached per day if needed (D9) |
| Views breakdowns (13 `v_views_*`) | agg (top 100 + `(other)`) + raw | same, no cap |
| Product events, DAU, totals | agg + raw | same |
| Product attribute breakdowns | declared keys only, one key at a time | any key, any combination |
| Measures and Web Vitals histograms | agg + raw | same |
| Vitals by page | raw only (no aggregate), so 30 days at most | same as the others |
| Users and groups dashboards | top 1000 ids per day; `firsts` CTEs scan the whole raw window | query, exact |
| Retention cohorts | `agg_retention`, rebuilt nightly over the raw window | query, or D9's cache |
| Cap usage, event names for rules | `v_*` | query; cap usage loses its purpose with the caps (D11) |

### Lookups and questions

| Use case | Today | Raw-first |
|---|---|---|
| A user's or group's timeline | `query` over the raw window, full scan | query over 3 years; Parquet sorted to suit (Q3) |
| First event of an actor or an event name | `actors` day; `MIN(day)` from aggregates; the event itself only in the window | query |
| "When did user X unsubscribe", combinations, undeclared keys | raw window only | query |
| Ad hoc SQL (`query`, widget SQL) | anything, `events` included | D4's tables over the files and the open log (D8) |
| Form submission → visit linking (`forms.go:SessionAt`) | raw views of today and yesterday | today's and yesterday's day files or log, or linked lazily (Q9) |

### Writes and maintenance

| Use case | Today | Raw-first |
|---|---|---|
| Ingest, `$form_submit` | `INSERT OR IGNORE` into `events` | append to the project's day log, dedup in memory (D2) |
| Rename/merge an event or attribute | rewrites raw rows and moves aggregates | applied to the log at write; files map on read (D6) |
| Block an event | rows dropped at rollup | dropped at write; files filter on read (D6) |
| Delete a submission, a project, a user's data | point delete; deleting a project is a full table scan | rewrite the affected files (D6) |
| Rollups, actors, cohorts, identities, attribute stats | nightly over the raw window | gone, or D9's cache (D11) |
| Day lists, sizes, raw counts (`allRawDays`, `usage`, `measureSizes`, `limits`) | full scans of `events` | file metadata (Parquet footers hold row counts) |

## Numbers

| What | Value | Source |
|---|---|---|
| Raw row in SQLite, today's format | 444 B prod; 615 B synthetic (45% product events with ~15 attributes) | prod 2026-10-04; `scripts/archive-bench`, 985,500 rows → 606 MB |
| Raw row in SQLite, #175 encoding | 211 B | #175 benchmark, 1.32M prod-shaped rows |
| Raw row in Parquet | 40 B monthly files, 44 B one file (zstd) | `scripts/archive-bench`, 36M rows |
| Raw row in a DuckDB file | 71 B | `scripts/archive-bench`, 36M rows |
| Aggregates | ~27 B per event | seeded measurement, 2026-10-04 |
| Report CPU, today | ~65 µs per raw event in range, per Views dashboard load | prod, 2026-10-04 |
| Ingest | 27,000 rows/s into SQLite, one writer, 500-row batches (M4); a 500-row DuckDB append takes 15 ms (max 37 ms) while queries run | `scripts/archive-bench`; the need at the cap is 0.4/s on average |

At the cap (1M a month, 3 years):

| | Today, 3-year raw window | Raw-first |
|---|---|---|
| SQLite | 22.1 GB (measured) | metadata only, flat |
| Logs | — | today's, ~40 MB at the cap, deleted when dumped |
| Parquet | — | 1.36 GB, one file per project and month (measured) |
| Views dashboard, 1-year range | ~13 min CPU | ~3 s at 2 threads, measured (C) |

## Measurements (2026-10-12)

`scripts/archive-bench` on 36,003,600 rows (1M a month, 2023-10-13 →
2026-10-12; 45% product events with ~19 attributes), Apple M4, 32 GB RAM.
A: modernc SQLite (twillingate's driver), one thread, file warm in the OS
cache, one run. C: DuckDB 1.5.6, `threads=2`, `memory_limit=256MB`, best of
3 warm runs. Row counts checked equal across engines.

| | A. SQLite | C. DuckDB file | C. monthly Parquet | C. one Parquet file |
|---|---|---|---|---|
| Size | 22.1 GB | 2.55 GB | 1.45 GB (268 files) | 1.60 GB |
| Built in | 22 min (generator) | 412 s from SQLite | 39 s from DuckDB | 31 s from DuckDB |

Seconds per query, 1 year (12M rows) / 3 years (36M rows):

| Query | A | C file | C monthly | C one file |
|---|---|---|---|---|
| views and visitors per day | 3.9 / 30 | 0.20 / 0.73 | 0.21 / 0.68 | 0.34 / 1.10 |
| sessions and bounce | 22.8 / 68 | 1.75 / 7.3 | 1.81 / 7.5 | 1.97 / 7.9 |
| top pages, distinct visitors | 8.5 / 35 | 0.73 / OOM | 0.80 / OOM | 0.84 / OOM |
| product events per name per day | 6.0 / 46 | 0.07 / 0.21 | 0.06 / 0.15 | 0.12 / 0.33 |
| DAU | 4.1 / 29 | 0.12 / 0.38 | 0.11 / 0.34 | 0.19 / 0.54 |
| exact distinct users, range | 3.9 / 29 | 0.04 / 0.13 | 0.03 / 0.06 | 0.10 / 0.27 |
| two attributes | 20.7 / 166 | 0.19 / 0.62 | 0.10 / 0.28 | 0.19 / 0.52 |
| two attributes as a filter | 8.9 / 108 | 0.14 / 0.44 | 0.06 / 0.16 | 0.15 / 0.42 |
| attribute × country | 19.2 / 138 | 0.16 / 0.53 | 0.10 / 0.29 | 0.18 / 0.51 |
| weekly retention cohorts | 6.0 / 94 | 0.10 / 0.33 | 0.09 / 0.29 | 0.17 / 0.49 |
| one actor's timeline | 1.9 / 81 | 0.12 / 0.29 | 0.09 / 0.09 | 0.30 / 0.64 |
| first common / rare event | 1.7 / 69 | 0.03 / 0.10 | 0.05 / 0.13 | 0.14 / 0.37 |

Over 30 days A takes 0.14–1.8 s per query, C 3–120 ms. C's DuckDB file at
10 threads and no memory limit: every 3-year query under 1.5 s, RSS 4.4 GB.

- **OOM:** `COUNT(DISTINCT actor_id) … GROUP BY path` over 3 years (~300k
  paths) fails at 256 MB in every C layout; DuckDB does not spill distinct
  aggregates, even with `temp_directory`. Written as `GROUP BY path,
  actor_id` then `GROUP BY path` it runs in 2.2 s at 256 MB; at 512 MB the
  original runs in 2.5 s. Widget SQL needs a limit above 256 MB or this
  rewrite.
- **Memory:** RSS peaks ~420 MB at `memory_limit=256MB`; the limit covers
  DuckDB's buffer manager only.
- **Writes (DuckDB file):** a 500-row append every 0.2 s while 1-year
  queries run: 15 ms median, 37 ms max; queries not slowed. Deleting the
  oldest month (1M rows) takes 0.05 s and one actor's rows 0.32 s, but the
  file does not shrink, `VACUUM` included; space comes back only by
  rewriting the file (37 s at 2.5 GB). Retention by deleting monthly
  Parquet files has no such cost (Q9).
- **Layouts (3 years, 2 threads, 256 MB):** one file per project and month
  with no family split, rows ordered by family then time, is 1.36 GB (74
  files) against 1.45 GB for per-family files (268 files) and the same
  speed on every query (two attributes 0.31 vs 0.28 s). Ordered by time
  alone it is 1.43 GB and ~1.3× slower on product queries (0.38 s), since
  a family filter cannot skip row groups. One file per month holding
  every project, ordered by project, family and time (37 files, 1.34 GB),
  is the same speed on every query at every range: row-group statistics
  prune by project as directories would. One file for everything is
  1.60 GB and 1.3–4× slower. Per project stays (D2) for operational
  reasons: a project is a directory to delete, back up or hand over, and
  the dump writes one log into one file.
- **Log and dump (one project's cap day, ~20k events):** the NDJSON log is
  25 MB and DuckDB aggregates it in 0.15 s (CSV: 7.5 MB, 0.25 s); `COPY`
  of the log to a 1.0 MB day file takes 0.20 s; a full day's rows rewrite
  as Parquet in 52 ms. A 30-day query over month files takes 9 ms alone,
  162 ms with the whole day's log unioned in.
- **Backups (DuckDB file):** from inside the process holding it, `COPY
  FROM DATABASE` to a new file takes 151 s at 2 threads; `EXPORT DATABASE`
  to Parquet runs out of memory at 256 MB. A second process cannot open the
  file at all while one holds it, not even read-only.

CPU and memory in one Go process (`scripts/archive-bench/probe`: duckdb-go
v2.10506.0 and modernc side by side), the 13 queries once warm, Linux arm64
in a container capped at 2 CPUs and 1 GB (glibc; macOS's allocator never
returns DuckDB's memory, even after closing):

| | wall | CPU | peak RSS | RSS 20 s after | after `SET memory_limit='16MB'` |
|---|---|---|---|---|---|
| SQLite (twillingate's pragmas: `temp_store` memory), 30 days | 10.1 s | 10.2 s | 401 MB | 55 MB | — |
| SQLite, 1 year (macOS) | 111 s | 110 s | 4,263 MB | 59 MB | — |
| DuckDB file, 30 days, 2 threads, 256 MB | 0.27 s | 0.53 s | 211 MB | 211 MB | 65 MB |
| DuckDB file, 1 year, 1 thread, 128 MB | 9.5 s | 7.3 s | 344 MB | 234 MB | 76 MB |
| DuckDB file, 1 year, 2 threads, 256 MB | 4.9 s | 8.2 s | 421 MB | 366 MB | 81 MB |
| DuckDB file, 3 years, 2 threads, 512 MB | 23 s | 29 s | 686 MB | 555 MB | 71 MB |
| Monthly Parquet, 3 years, 2 threads, 512 MB | 22 s | 25 s | 676 MB | 128 MB | 76 MB |

DuckDB loaded and open, before any query: 37 MB RSS. The DuckDB file's
buffer pool holds its pages until the limit is lowered or the database
closed; Parquet reads are not kept, so RSS falls back on its own. At 128 MB
`top_pages` fails from 1 year. Build: the Linux binary needs cgo and g++,
links glibc dynamically (no Alpine/musl), and adds ~50 MB (63 MB for an
empty program with the driver).

## Options considered

Everything discussed on the way to this design (2026-10-12), with where it
stands.

### Storage engines

| Option | What it is | Status |
|---|---|---|
| SQLite as is, longer raw window | raise `RETENTION_RAW_DAYS` to 1–3 years | rejected: 16–22 GB at the cap, reports scan raw, step 0's risk |
| SQLite + #175 encoding | 491 → 211 B/row (benchmarked), pure Go, Litestream keeps working | rejected (D12): halves the size but keeps every other problem; mixed speed (system widgets 1.3–2× faster, `json_extract` and text filters slower); irreversible migration |
| DuckDB, whole store | embedded columnar SQL, one file, Postgres-like dialect | rejected as the whole store: no triggers (`project_tabs`, `dashboard_groups` rely on them), cgo, no Litestream |
| SQLite for metadata + DuckDB for events | DuckDB can `ATTACH` the SQLite file for joins | favoured: design C (D10); its own file or Parquet is Q9 |
| chDB | ClickHouse engine in-process | rejected: Python-first, thin Go bindings, several hundred MB, cgo |
| FrostDB (Polar Signals) | pure-Go embeddable columnar store on Arrow/Parquet | rejected: niche, experimental, no SQL |
| DataFusion | Rust query engine library | rejected: a toolkit, not a database; Rust |
| ClickHouse server as a sidecar | best compression (~10–25 B/event *estimate*), fastest | rejected: a second process to install, upgrade and back up; no longer one binary |
| QuestDB; TimescaleDB / pg_duckdb | time-series server; Postgres extensions | rejected: a separate server |
| Turso | SQLite rewrite | rejected earlier (2026-10-04): bills rows scanned, lacks LAG and recursive CTEs, cgo |

### Archive formats

| Option | Status |
|---|---|
| Monthly SQLite files, attached for SQL | rejected: ~211 B/row at best (~7.6 GB at the cap), no columnar scans |
| Monthly Parquet files | kept (D2): ~7× smaller *estimate*, standard format that users' own tools read, can be read in parts from object storage |
| Daily Parquet files merged monthly | open (Q2) |

### Limits

- **Events per month** (1M) and **retention** (3 years) as two limits was
  reframed as one: **raw events held** (36M), which lets a server trade
  volume for retention (1M × 3 years, 3M × 1 year, 6M × 6 months) at the
  same cost. Ties into Q7.

### The first version of this spec: rollups kept, raw archived

The design before raw-first, kept here as the fallback if neither engine
meets D10's targets:

- each closed day rolled up once, and re-rolled from its raw rows when late
  events land (a dirty-day table marked by ingest); late events can't be
  added onto an aggregate because distinct users and sessions don't add up;
- `v_*` live halves read only days not yet rolled up;
- actors, cohorts, identities and attribute stats computed once per closed
  day instead of over the whole window every night;
- SQLite holds the current month + the late window (~38 days); every older
  raw event goes to monthly Parquet files;
- a `timeline` tool and a `query_archive` tool: filters pushed down to the
  files, matching rows loaded into in-memory SQLite, the agent's SQL run on
  them;
- aggregates unchanged, so combinations, undeclared keys and exact range
  uniques still need the archive. That gap led to raw-first.

### Combinations of attributes

| Option | Status |
|---|---|
| Aggregate every combination of every declared key | rejected: rows multiply per key; with per-user numbers (accounts, payees) nearly every user is a unique tuple, so the aggregate is as big as raw |
| Declared combinations (a project declares `[status, reason]`; top-N tuples + `(other)` per day) | rejected: predefining tuples complicates the user journey |
| Answer combinations from raw | kept (D8) |
| Per-day cache of each widget's own query, no declarations | rejected (D9): not needed by measurement; the first load would compute each closed day per widget and store it, later loads read the stored days and compute only today, invalidated by rules and erasure; distinct users would need recompute or sketches |
| HyperLogLog sketches for distinct users over ranges | rejected (Q5): distinct users are exact from raw in 0.06–0.27 s over 3 years |
| A derived sessions table written at day close | kept (D16) |

## Open questions

- **Q1.** Settled 2026-10-12: the table is `events`; every file carries a
  `project_id` column and files are stored per project (D2, D4). A widget
  sees its own project, scoped by the file list.
- **Q2.** Settled: day files, merged into one file per project and month
  (D2).
- **Q3.** Settled for the file order: `(family, ts)` (Measurements).
  Row-group size and `(actor_id, ts)` for timelines are unmeasured; the
  timeline already meets its target at `(family, ts)`.
- **Q4.** Settled 2026-10-12: `INGEST_LATE_DAYS`, default 1, configurable
  (D3). A day file is final a day after the day closes; a month merges a
  day after it ends.
- **Q5.** Settled 2026-10-12: moot without D9's cache; exact from raw. The
  sessions table (D16) takes the one slow query.
- **Q6.** Upgrade path. Existing servers hold only aggregates for days
  older than their raw window: those days have no raw rows to move. Either
  dashboards keep reading `agg_*` for days before the switch (two code paths
  for as long as those aggregates are retained), or history before the
  switch is reduced to what a migration can express from aggregates. On a
  server whose raw window was raised, check step 0's damage first.
  `deploy/UPGRADES.md` gets the runbook.
- **Q7.** Does the "raw events held" allowance (2026-10-09 spec, hosted
  plans) count the archive's footer row counts plus the open logs' lines?
  (Hot no longer exists.)
- **Q8.** Should `query` allow an unfiltered 3-year scan, or require a range
  or a filter, under its timeout?

- **Q9.** Settled: Parquet files, not a DuckDB file (D10, measured);
  backups by hand, no Litestream (D7, 2026-10-12). Still open from it: the
  `forms.go:SessionAt` link from a submission to a visit, which read
  today's and yesterday's views in SQLite: a query over two day files, or
  linking lazily when the submission is viewed.
- **Q10.** Settled 2026-10-12: flat NDJSON in the Parquet row's shape,
  `attr_<key>` string values, dedup at dump and merge, a time-bounded
  recent-ids set at ingest (D2).
- **Q11.** Whether widget SQL sees any SQLite table at all. Metadata lives
  in SQLite and events in the engine, so a widget cannot join them unless a
  few small tables (projects) are handed to the child.

## Next steps

0. **Verify the raw-window risk** (Problem, last item) with a test: raise
   `RETENTION_RAW_DAYS` after a rollup, ingest a late event into a rolled-up
   day, age it out, and compare the day's aggregates. If confirmed, check
   prod (days whose aggregates dropped after the raise) and decide whether
   to lower the window until D3 ships. Independent of everything else.
1. **Measure A against C first.** Done 2026-10-12 (Measurements):
   `scripts/archive-bench` on 36M rows
   (1M a month × 36 months, 2023-10-13 → 2026-10-12):
   - generate the rows into SQLite (A's data);
   - load the same rows into DuckDB with D4's wide attribute columns, both
     as a DuckDB file and as monthly Parquet files (Q9); record sizes and
     load times. The DuckDB command-line tool is enough: no cgo in
     twillingate is needed to measure;
   - a query set, each over 30 days, 1 year and 3 years: views and visitors
     per day; sessions and bounce rate; top pages; product events per name
     per day; DAU; exact distinct users over the range; a two-attribute
     combination (`status` × `reason`); a key × `$country`; a retention
     cohort; one actor's timeline; first occurrence of a common and a rare
     event name;
   - DuckDB's memory at a fixed limit (e.g. 256 MB, one process per
     customer); appending a 10-second batch while queries run; deleting a
     month (retention) and one actor's rows (erasure) from the DuckDB file,
     and the file size after.
   Fill the *estimates* and compare against D10's targets. Measure B (the
   pure-Go path) only if C is ruled out.
2. **Measure the child engine** (D8): spawn to first result, and a
   dashboard of 8 widgets through it at 2 threads. Then settle Q1, Q4–Q8,
   Q10–Q11 and write the implementation plan.
3. **Implementation order** (each its own PR; detail after step 2):
   1. Late window (D3). Fixes step 0's risk on its own.
   2. The log and the dump beside today's ingest: events written to both
      SQLite and the log, day files produced by the engine, retention by
      file, backups (D2, D5, D7). With today's aggregates
      untouched, this alone gives 3 years of raw history.
   3. D4's tables, the engine child, `query` on them (D8, D10).
   4. Dashboards on raw, the sessions table or D9's cache if needed, the
      upgrade path (Q6).
   5. Stop writing events to SQLite; remove what D11 lists; close #173;
      reduce #175 to the allowlist (D12).

## Found along the way

- `reporting/source.go:sampleProject` says widget SQL may not read
  `events`; only `submissions` is refused.
- The `views/*.sql` widget headers and the "Cost note" in
  `api/resources.go` say the live half sessionizes every raw view of every
  project before any filter applies; true before 025, not since.
- `IDENTITIES_TOP_N` has two defaults: 500 in config (and `api/resources.go`),
  1000 in the store's fallback (`identities.go:defaultIdentitiesTopN`, also
  `v_identity_daily`'s) when `meta` has no `identities_top_n` row.
- `DELETE FROM events WHERE project_id=?` (`registry.go:deleteProject`) is a
  full table scan: the key starts with `family`.
