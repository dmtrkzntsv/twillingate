# Raw first: raw events as the only source of truth

Status: draft. Decisions are proposed; the query engine (D5) is chosen by
measurement (Next steps, step 1). Numbers marked *estimate* are unmeasured.
Date: 2026-10-12

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
- **D2. Two tiers: a SQLite buffer, then Parquet.**
  - **Hot:** ingest keeps writing `events` in SQLite. A day stays there
    until it is closed and past the late window (D3).
  - **Parquet:** each closed day past the late window is written to a
    Parquet file, then its rows are deleted from `events` in one
    transaction (write to a temp file, fsync, verify the row count, rename,
    delete). Files are merged into one file per project, family and month
    once the month is complete (Q2).
  - Layout: `<data dir>/raw/<project_id>/<family>/<YYYY-MM>.parquet`
    (plus daily files until the month is merged).
  - Hot holds about the late window plus today: ~8 days, ~0.2 GB at the cap
    in today's row format. Raw storage effort goes into Parquet (D4's
    columns, dictionary encoding, zstd); #175's SQLite encoding stays dropped.
- **D3. A late window, separate from retention.** `clampTS`'s lower bound
  becomes a late window (proposed 7 days; name to settle, e.g.
  `INGEST_LATE_DAYS`), no longer `RETENTION_RAW_DAYS`. An event older than
  that is clamped into the window's first day, as today. Late events always
  land in Hot, so a Parquet file never changes because of one, and a day in
  Parquet is final.
- **D4. One wide table per project and family, with every attribute.** Each
  project exposes `views`, `product` and `measures` (names to settle, Q1):
  today's typed columns plus **one column per attribute key the project's
  catalog has seen**, declared or not. Parquet stores each key as a real,
  dictionary-encoded column, so a wide, sparse table costs little; a file
  written before a key existed reads it as null. Hot exposes the same
  columns over its JSON (as `v_events_flat` does today, but for every key).
  Key sanitizing and collisions follow `flatview.go`'s rules.
- **D5. Retention deletes whole files.** `RETENTION_RAW_DAYS` keeps its
  name, means raw kept (Hot + Parquet), and goes up to 1095 days. The pass
  deletes month files entirely older than it. No `DELETE` over millions of
  rows, no free pages, no `VACUUM`.
- **D6. Files are immutable; rules apply when read.** Event and attribute
  renames and merges map old names on read (`rules` already says how);
  blocked names are filtered on read and dropped when a file is next
  rewritten. Data that must really go (a user's erasure, a submission, a
  project) is removed by rewriting the affected files: copy without the
  rows, fsync, rename. A purged project deletes its directory.
- **D7. Backups.** Litestream keeps covering the SQLite file. Parquet files
  are written once (D6's rewrites aside) and uploaded once next to the
  Litestream replica; restore copies them back. `docs/litestream.md` and
  `docs/deployment.md` describe it.

### Reports and queries

- **D8. Dashboards query raw.** Widgets, system dashboards and the `query`
  tool read D4's tables over Hot + Parquet through the query engine (D10).
  Exact distinct users over any range, combinations of any keys and keys
  nobody declared all come from the same rows.
- **D9. Aggregates become a cache, if the engine needs one.** If the engine
  is not fast enough at the cap (D10's targets), closed days' results are
  cached per widget query and per day:
  - the first load computes each day and stores it; later loads read the
    stored days and compute only Hot;
  - only days in Parquet are cached, and they are final (D3), so the cache
    is invalidated only by rules changes and erasure;
  - it works for anything that adds up across days (counts, sums,
    histograms); distinct users over a range are recomputed from raw, shown
    per day, or estimated with a per-day HyperLogLog sketch (Q5).
  Nothing is declared: the widget's own query defines what is cached.
- **D10. The query engine is chosen by measurement.** Go has no columnar
  SQL engine without cgo. The two candidates:

  | | Pure Go (`parquet-go` + our code) | DuckDB |
  |---|---|---|
  | How | read only the needed columns, skip row groups by min/max and dictionaries, load matching rows into in-memory SQLite, run the SQL there | SQL directly over Parquet and the SQLite buffer, vectorized, multi-core |
  | Broad scan of 36M rows | about minutes; loading into SQLite is the bottleneck *estimate* | under a second for a typical group-by on 4 cores *estimate* |
  | Dashboards | need D9's cache | likely query raw directly, no cache |
  | Build | keeps `CGO_ENABLED=0` and `linux/arm` | cgo; drops 32-bit ARM; binary +40–60 MB; needs a per-process memory limit |
  | Widget SQL | stays SQLite | becomes DuckDB SQL: breaking for user widgets, softened because real attribute columns replace `json_extract` |

  Targets at the cap, warm: a system dashboard over 1 year loads in under
  2 s; an agent's question over 3 years answers in under 30 s; a
  timeline over 3 years in under 1 s. Settle the targets with the numbers.

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
- **D13. New dependencies.** `github.com/parquet-go/parquet-go` (pure Go,
  pulls `klauspost/compress`), and DuckDB if D10 picks it. CLAUDE.md allows
  a third-party dependency only from a very reputable vendor; confirm both
  qualify, and that parquet-go builds with `CGO_ENABLED=0` for `linux/arm`.

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
| Ad hoc SQL (`query`, widget SQL) | anything, `events` included | D4's tables over Hot + Parquet |
| Form submission → visit linking (`forms.go:SessionAt`) | raw views of today and yesterday | Hot, unchanged |

### Writes and maintenance

| Use case | Today | Raw-first |
|---|---|---|
| Ingest, `$form_submit` | `INSERT OR IGNORE` into `events` | Hot, unchanged |
| Rename/merge an event or attribute | rewrites raw rows and moves aggregates | Hot rewrite; Parquet maps on read (D6) |
| Block an event | rows dropped at rollup | Hot delete; Parquet filters on read (D6) |
| Delete a submission, a project, a user's data | point delete; deleting a project is a full table scan | Hot delete; Parquet file rewrite (D6) |
| Rollups, actors, cohorts, identities, attribute stats | nightly over the raw window | gone, or D9's cache (D11) |
| Day lists, sizes, raw counts (`allRawDays`, `usage`, `measureSizes`, `limits`) | full scans of `events` | file metadata (Parquet footers hold row counts) |

## Numbers

| What | Value | Source |
|---|---|---|
| Raw row in SQLite, today's format | 444 B prod; 615 B synthetic (45% product events with ~15 attributes) | prod 2026-10-04; `scripts/archive-bench`, 985,500 rows → 606 MB |
| Raw row in SQLite, #175 encoding | 211 B | #175 benchmark, 1.32M prod-shaped rows |
| Raw row in Parquet | 20–50 B | *estimate* |
| Aggregates | ~27 B per event | seeded measurement, 2026-10-04 |
| Report CPU, today | ~65 µs per raw event in range, per Views dashboard load | prod, 2026-10-04 |
| Ingest | ~5,200 rows/s, one writer, 500-row batches (laptop, 4 cores) | `scripts/archive-bench`; the need at the cap is 0.4/s on average |

At the cap (1M a month, 3 years):

| | Today, 3-year raw window | Raw-first |
|---|---|---|
| SQLite | 16–22 GB | Hot ~8 days (~0.2 GB) + metadata, flat |
| Parquet | — | 0.7–1.8 GB *estimate* |
| Views dashboard, 1-year range | ~13 min CPU | D10's target: under 2 s |

## Options considered

Everything discussed on the way to this design (2026-10-12), with where it
stands.

### Storage engines

| Option | What it is | Status |
|---|---|---|
| SQLite as is, longer raw window | raise `RETENTION_RAW_DAYS` to 1–3 years | rejected: 16–22 GB at the cap, reports scan raw, step 0's risk |
| SQLite + #175 encoding | 491 → 211 B/row (benchmarked), pure Go, Litestream keeps working | rejected (D12): halves the size but keeps every other problem; mixed speed (system widgets 1.3–2× faster, `json_extract` and text filters slower); irreversible migration |
| DuckDB, whole store | embedded columnar SQL, one file, Postgres-like dialect | rejected as the whole store: no triggers (`project_tabs`, `dashboard_groups` rely on them), cgo, no Litestream |
| SQLite for metadata + DuckDB for events | DuckDB can `ATTACH` the SQLite file for joins | kept as D10's DuckDB candidate, reading Parquet rather than its own file |
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
| Per-day cache of each widget's own query, no declarations | kept as D9, if the engine needs it |
| HyperLogLog sketches for distinct users over ranges | open (Q5) |

## Open questions

- **Q1.** Names of D4's tables, and whether a widget sees only its own
  project's tables or every project's with a `project_id` column.
- **Q2.** Daily files merged into monthly ones, or monthly files only (Hot
  then holds the current month + the late window, ~38 days)?
- **Q3.** Parquet sort order and row-group size: `(actor_id, ts)` serves
  timelines; `(ts)` serves date ranges. Measure both.
- **Q4.** Late window length (7 days?) and its variable name.
- **Q5.** Distinct users over a range with D9's cache: recompute, per-day
  only, or HyperLogLog sketches (our own code in `internal/shared/`)?
- **Q6.** Upgrade path. Existing servers hold only aggregates for days
  older than their raw window: those days have no raw rows to move. Either
  dashboards keep reading `agg_*` for days before the switch (two code paths
  for as long as those aggregates are retained), or history before the
  switch is reduced to what a migration can express from aggregates. On a
  server whose raw window was raised, check step 0's damage first.
  `deploy/UPGRADES.md` gets the runbook.
- **Q7.** Does the "raw events held" allowance (2026-10-09 spec, hosted
  plans) count Hot only or Hot + Parquet?
- **Q8.** Should `query` allow an unfiltered 3-year scan, or require a range
  or a filter, under its timeout?

## Next steps

0. **Verify the raw-window risk** (Problem, last item) with a test: raise
   `RETENTION_RAW_DAYS` after a rollup, ingest a late event into a rolled-up
   day, age it out, and compare the day's aggregates. If confirmed, check
   prod (days whose aggregates dropped after the raise) and decide whether
   to lower the window until D3 ships. Independent of everything else.
1. **Measure both engines** with `scripts/archive-bench` (generator done;
   write the rest) on 36M rows (1M a month × 36 months, 2023-10-13 →
   2026-10-12):
   - Parquet bytes per row and write time per month, for each sort order
     (Q3), with D4's wide attribute columns;
   - a query set, each over 30 days, 1 year and 3 years: views and visitors
     per day; sessions and bounce rate; top pages; product events per name
     per day; DAU; exact distinct users over the range; a two-attribute
     combination (`status` × `reason`); a key × `$country`; a retention
     cohort; one actor's timeline; first occurrence of a common and a rare
     event name;
   - run each with the DuckDB command-line tool over the Parquet files (no
     cgo in twillingate needed to measure), with the pure-Go path (column
     pushdown + in-memory SQLite), and on today's SQLite as the baseline;
   - DuckDB's memory at a fixed limit; a file rewrite without one actor's
     rows (D6); `CGO_ENABLED=0 GOARCH=arm go build` with parquet-go (D13).
   Fill the *estimates* and compare against D10's targets.
2. **Choose the engine** (D10), settle Q1–Q8, then write the implementation
   plan.
3. **Implementation order** (each its own PR; detail after step 2):
   1. Late window (D3). Fixes step 0's risk on its own.
   2. Parquet writer, Hot cut, retention by file, backups (D2, D5, D7). With
      today's aggregates untouched, this alone gives 3 years of raw history.
   3. D4's tables over Hot + Parquet, the engine, `query` on them (D8, D10).
   4. Dashboards on raw, D9's cache if needed, the upgrade path (Q6).
   5. Remove what D11 lists; close #173; reduce #175 to the allowlist (D12).

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
