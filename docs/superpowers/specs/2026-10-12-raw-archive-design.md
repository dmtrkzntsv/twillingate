# Daily rollups and a monthly raw archive

Status: draft. Decisions are proposed; numbers marked *estimate* are to be
measured (Next steps, step 1).
Date: 2026-10-12

## Envelope

One server takes up to **1M events a month** and keeps raw events for up to
**3 years**: 36M raw events at the cap. Within that:

- reports are fast at any range;
- any raw event can be looked up: a user's or group's timeline, when an
  event was first seen, when a user unsubscribed;
- ingest stays fast;
- the SQLite file stays small and flat over time, so backups, restores and
  vacuums stay cheap.

## Problem

- **Reports scan the raw window on every load.** Every `v_*` view is
  `agg_* UNION ALL` a live half computed from raw rows for every day still in
  the raw window. Prod measured ~65 µs of CPU per raw event per Views
  dashboard load. At 1M events a month, today's 30-day window is ~65 s of
  CPU per load (before the cache); a 1-year window is ~13 min.
- **Raw history ends at the raw window.** After `RETENTION_RAW_DAYS`
  (default 30) a day is rolled up and its raw rows are deleted: only counts
  remain. A user's timeline, the first time an event was seen or the day a
  user opted out cannot be answered past 30 days (the motivating case of
  #173).
- **The only lever is the raw window, and it grows SQLite.** A raw row is
  444 B on prod and 615 B on the attribute-heavy synthetic set: 16–22 GB for
  36M rows in one file.
- **The nightly pass recomputes the whole raw window every night.** Actors,
  retention cohorts, identities, `usage_attributes` and `usage_history`'s
  counts and attribute stats are rebuilt from every raw day each night, so
  their cost grows with the window too.
- **Raising `RETENTION_RAW_DAYS` is probably unsafe today** (inferred from
  the code, not tested; Next steps, step 0). `clampTS`
  (`internal/server/ingest.go`) bounds a timestamp to `received −
  RETENTION_RAW_DAYS`, which is what keeps a late event out of an
  already-rolled-up day. Raising the window moves that bound back over days
  whose raw rows are already gone. A late event lands there as the day's only
  raw row; when the day ages out again, the rollup recomputes it from that
  row alone and `INSERT OR REPLACE` overwrites the day's full aggregates.
  Until then the live half adds the late row on top of the aggregates. Prod's
  raw window was raised recently.

## How it works today

1. **Ingest.** The pipeline buffers events and writes a batch about every
   10 s into one `events` table (views, product, measures), keyed
   `(family, project_id, day, id) WITHOUT ROWID`, with no secondary index.
2. **Nightly pass** (`internal/jobs/jobs.go:RunDailyPass`, on boot and at
   03:00 UTC), in this order, which is load-bearing:
   1. For every raw day of every project, today included: `UpsertActors`,
      `AggregateRetentionDay`, then `AggregateIdentityDay` (today skipped).
   2. `DropBlockedAggregates`.
   3. Every day older than `today − RETENTION_RAW_DAYS` is rolled up into
      `agg_*` and its raw rows deleted, in one transaction per project-day-family.
   4. Aggregates, actors and identities older than `RETENTION_AGGREGATE_DAYS`
      (default 400) are pruned.
   5. `RebuildFlatView`, `IncrementalVacuum`, `RecordUsageHistory`.
3. **A day is aggregated exactly when its raw rows are gone.** Nothing
   records that a day was rolled up. The `v_*` views (except
   `v_identity_daily`, which checks for an `agg_identity_daily` row), the
   rollups (`n == 0` means "already done"), `usage_history`,
   `usage_attributes`, the `usage` tool and `forms.go:deleteEvent` all rely
   on it.

## Use cases

**Where it lives:** Hot = raw rows in SQLite (D2). Agg = `agg_*` in SQLite.
Archive = monthly Parquet files (D6).

### Reports

| Use case | Reads today | Needs raw rows? | Where it lives |
|---|---|---|---|
| Views charts and KPIs (visitors, bounce, session length) | agg + raw days in range, re-sessionized per load; visitors/views read 2× the range | today only | Agg + today from Hot |
| Views breakdowns (13 `v_views_*`) | same | today only | Agg + Hot |
| Product events, DAU, totals | same | today only | Agg + Hot |
| Product attribute breakdowns | same | today only | Agg + Hot |
| Measures and Web Vitals histograms | same | today only | Agg + Hot |
| Vitals by page (`vitals/by-page.sql`) | `raw_measures` only, no aggregate | yes | new aggregate (path × vital), then Agg + Hot |
| Users and groups dashboards | `v_identity_daily`; the `firsts` CTEs have no day filter and read the whole raw window | no | Agg (add the day filter) |
| Retention cohorts (read) | `agg_retention` only | no | Agg |
| Cap usage, event names for rules | `v_*` | today only | Agg + Hot |

### Lookups

| Use case | Reads today | Needs raw rows? | Where it lives |
|---|---|---|---|
| A user's or group's timeline | `query` over the raw window, full scan (no actor index) | yes | Hot + Archive (D10) |
| An actor's first event | the day is in `actors`; the event is a full scan | the event: yes | day from `actors`; event from Hot + Archive |
| First occurrence of an event name | `MIN(day)` over `v_product_daily` | day: no | Agg; the event itself from Archive |
| "When did user X unsubscribe" and other agent questions | `query` over the raw window | yes | Hot via `query`; Archive via `query_archive` (D10) |
| Ad hoc SQL (`query`, widget SQL) | anything, `events` included | sometimes | Hot only; Archive through D10's tools |
| Form submission → visit linking (`forms.go:SessionAt`) | raw views of today and yesterday | yes | Hot |

### Writes and maintenance

| Use case | Today | Where it lives |
|---|---|---|
| Ingest, `$form_submit` | `INSERT OR IGNORE` into `events` | Hot |
| Rename/merge an event or attribute | rewrites raw rows over the window | Hot rewrite; Archive applies rules when read (D9) |
| Block an event | rows stay raw, dropped at rollup | Hot as today; Archive filters when read (D9) |
| Delete a submission, a project, a user's data | point delete; deleting a project is a full table scan | Hot delete; Archive rewrites the affected files (D9) |
| Views/product/measures rollups | once, when a day leaves the raw window | once per closed day, re-rolled when dirty (D3) |
| Actors, retention, identities, attribute stats | whole raw window every night | once per closed day, re-rolled when dirty (D5) |
| Day lists, sizes, raw counts (`allRawDays`, `usage` raw_days, `measureSizes`, `limits` raw count) | full scans of `events` | stored per-day counts (D5) |

## Decisions (proposed)

### Rollups

- **D1. Three tiers.** SQLite keeps metadata, every aggregate, and raw rows
  for the current month plus the late window (Hot). Monthly Parquet files
  keep every raw event for the raw retention (Archive). Reports read Agg +
  Hot; lookups read Hot + Archive.
- **D2. A late window, separate from retention.** `clampTS`'s lower bound
  becomes a late window (proposed 7 days; name to settle, e.g.
  `INGEST_LATE_DAYS`), no longer `RETENTION_RAW_DAYS`. An event older than
  that is clamped into the window's first day, as today. No event can reach a
  day whose raw rows have left SQLite.
- **D3. Each closed day is rolled up once, and re-rolled when dirty.** A day
  is closed once it is before today (UTC). A new table records the rollup
  state per `(project_id, family, day)`: rolled, and dirty. When a flush
  inserts into a day before today, it marks that day dirty in the same
  transaction. The nightly pass rolls up every closed day not yet rolled, and
  re-rolls every dirty day **by recomputing it from its raw rows** (raw rows
  are still in Hot: D2 guarantees it), replacing its aggregates. Late rows
  are never added to an aggregate: distinct visitors, DAU and sessions don't
  add up.
- **D4. Live halves read only days that are not rolled.** Each `v_*` live
  half reads raw rows only for days with no rollup record: today, and
  yesterday until the pass runs. A rolled day's late rows show up after the
  next pass's re-roll (D3), not live. The raw rows stay in Hot after the
  rollup, so the "aggregated exactly when raw is gone" rule is replaced
  everywhere it is relied on (How it works today, item 3).
- **D5. Every per-day derivation follows D3.** Actors, retention,
  identities, `usage_attributes` counts and `usage_history`'s counts and
  attribute stats are computed once per closed day and re-computed for dirty
  days, never over the whole window. Per-day raw counts are stored at rollup,
  so `usage` raw_days, `measureSizes` and the `limits` raw count stop
  scanning `events`.

### Archive

- **D6. One Parquet file per project and month.**
  `<data dir>/archive/<project_id>/<YYYY-MM>.parquet`, every family in one
  file, today's `events` columns unchanged (Parquet's dictionary and zstd do
  the compression). Rows sorted by `(actor_id, ts)` so a timeline reads one
  row range per file; settle the sort and the row-group size after
  measuring.
- **D7. A month is archived once it is closed and past the late window**
  (the pass on day `late window + 1` of the next month). Write to a temp
  file, fsync, verify the row count against SQLite, rename, then delete the
  month's raw rows from `events` in one transaction. Hot therefore holds at
  most the current month plus the late window: ~38 days.
- **D8. Raw retention deletes whole files.** `RETENTION_RAW_DAYS` keeps its
  name and now means raw kept in Hot + Archive, up to 1095 days; the pass
  deletes month files entirely older than it. `RETENTION_AGGREGATE_DAYS`
  is unchanged. No `DELETE` over millions of rows, no free pages, no
  `VACUUM`.
- **D9. Files are immutable; rules apply when read.** Event and attribute
  renames and merges map old names on read (the `rules` table already says
  how); blocked names are filtered on read. Deleting data that must really
  go (a user's erasure, a submission, a project) rewrites the affected
  files: copy without the rows, fsync, rename. A purged project deletes its
  directory.
- **D10. Reading the archive.** Pure Go (`parquet-go`), no SQL engine:
  - `timeline`: one actor's, user's or group's events, newest first, paged,
    over Hot + Archive. Profile pages use it.
  - `query_archive` (MCP and REST): a month range plus filters pushed down to
    the files (project, family, event names, actor/user/group id). Matching
    rows are loaded into a temporary in-memory SQLite table shaped like
    `raw_*`, then the agent's SQL runs on it under `query`'s guard, row cap
    and timeout. A "first seen" question walks months oldest first and stops
    at the first hit.
  - `query` keeps reading Hot only; the docs point to the two tools above.
- **D11. Backups.** Litestream keeps covering the SQLite file. Archive files
  never change after they are written (except D9's rare rewrites), so each is
  uploaded once next to the Litestream replica; restore copies them back.
  `docs/litestream.md` and `docs/deployment.md` describe it.
- **D12. One new dependency.** `github.com/parquet-go/parquet-go` (pure Go,
  pulls `klauspost/compress`). CLAUDE.md allows a third-party dependency
  only from a very reputable vendor; confirm it qualifies, and that it builds
  with `CGO_ENABLED=0` for `linux/arm` (the 32-bit release target).

### Superseded

- **D13. The events size encoding (#175) is dropped**, apart from its
  widget-SQL allowlist (`raw_*`, `v_*`, `agg_*` only), which becomes its own
  change. Hot holds ~38 days (0.5–0.8 GB at 1M a month in today's format);
  saving ~0.4 GB doesn't justify an irreversible migration, and its
  benchmark was mixed (system widgets 1.3–2× faster, `json_extract` and text
  filters slower). Parquet does the long-term compression.
- **D14. Kept events (#173) are closed.** The archive keeps every event, so
  a keep rule has nothing left to do.

## Numbers

| What | Value | Source |
|---|---|---|
| Raw row, today's format | 444 B prod; 615 B synthetic (45% product events with ~15 attributes) | prod 2026-10-04; `scripts/archive-bench`, 985,500 rows → 606 MB |
| Raw row, #175 encoding | 211 B | #175 benchmark, 1.32M prod-shaped rows |
| Aggregates | ~27 B per event | seeded measurement, 2026-10-04 |
| Report CPU | ~65 µs per raw event in range, per Views dashboard load | prod, 2026-10-04 |
| Ingest | ~5,200 rows/s, one writer, 500-row batches (laptop, 4 cores) | `scripts/archive-bench`; the need at the cap is 0.4/s on average |
| Parquet row | 20–50 B | *estimate* |

At the cap (1M a month, 3 years):

| | Today, 3-year raw window | This design |
|---|---|---|
| SQLite | 16–22 GB raw + aggregates | Hot ~38 days (0.5–0.8 GB) + aggregates (~1 GB *estimate*), flat |
| Archive | — | 0.7–1.8 GB *estimate* |
| Views dashboard load, 1-year range | ~13 min CPU | ~2 s CPU (today's ~33k rows) |

## Open questions

- **Q1.** Late window length (7 days?) and its variable name.
- **Q2.** Archive sort order: `(actor_id, ts)` serves timelines; a
  first-seen search by event name scans each file's `event_name` column.
  Measure both.
- **Q3.** Does the "raw events held" allowance (2026-10-09 raw events held
  spec, hosted plans) count Hot only, or Hot + Archive? Archive rows cost
  ~10× less disk and nothing at report time.
- **Q4.** Should yesterday be rolled up shortly after midnight UTC instead
  of at 03:00, so live halves read only today?
- **Q5.** Upgrade path: on first boot, days already rolled up get rollup
  records (from `agg_*` presence); raw days older than the current month
  plus the late window are archived by the first pass. On a server whose raw
  window was raised, check step 0's damage first. `deploy/UPGRADES.md` gets
  the runbook.
- **Q6.** Should `query_archive` allow an unfiltered multi-month scan, or
  require a filter or a month cap?

## Next steps

0. **Verify the raw-window risk** (Problem, last item) with a test: raise
   `RETENTION_RAW_DAYS` after a rollup, ingest a late event into a rolled-up
   day, age it out, and compare the day's aggregates. If confirmed, check
   prod (`agg_*` days with suspiciously low counts since the raise) and
   decide whether to lower the window again until D2 ships.
1. **Measure** with `scripts/archive-bench` (generator done; write the
   rest) on 36M rows (1M a month × 36 months, 2023-10-13 → 2026-10-12):
   - Parquet bytes per row and write time per month, for each sort order (Q2);
   - a timeline for one actor across 36 files; for a heavy and a light actor;
   - "first seen" for a common and a rare event name;
   - loading one month into in-memory SQLite for `query_archive`, unfiltered
     and filtered;
   - rewriting one month without one actor's rows (D9 erasure);
   - Hot SQLite size at 38 days, and a nightly pass (rollups + D5) on one
     closed day vs today's whole-window pass;
   - `CGO_ENABLED=0 GOARCH=arm go build` with parquet-go (D12).
   Fill the *estimates* in Numbers and settle Q2.
2. **Review this spec** with the numbers, settle Q1–Q6, then write the
   implementation plan.
3. **Implement in this order**, each its own PR:
   1. Rollup state, dirty days, late window, live halves on unrolled days
      (D2–D4). Fixes step 0's risk and makes reports fast on its own.
   2. Per-day derivations and stored counts (D5).
   3. Archive writer, Hot cut at 38 days, retention by file, backups
      (D6–D8, D11).
   4. `timeline` and `query_archive` (D10), immutability rules (D9), docs.
   5. Close #173; reduce #175 to the widget-SQL allowlist (D13, D14).

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
