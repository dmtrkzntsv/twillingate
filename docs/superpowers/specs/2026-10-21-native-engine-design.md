# Native engine: structured queries in Go, SQL as an external engine

Status: approved, 2026-10-21. Supersedes D8 and D10 of
`2026-10-12-raw-first-design.md` for the open-source server; the rest of
raw-first (event files are the only truth, per-project month Parquet plus
day files plus the open NDJSON log, retention by file) stands.

## Intent

Twillingate's long-term shape is an analytics server that is cheap to run:
fast answers, as little memory as possible, low CPU. Almost every request
is someone opening a built-in dashboard; very few people ever write SQL.
Today every one of those requests goes through DuckDB in a child process,
which costs a cgo build, an 81 MB binary, memory peaks of 600–800 MB per
child, a slot semaphore, and a SQL dialect that users and agents have to
learn.

The owner's decisions (2026-10-20/21):

- **The server has no SQL and no DuckDB.** It is pure Go. Every
  dashboard, built-in or custom, and every agent question runs on a native
  engine that answers a small structured query language.
- **SQL is an external engine**, a separate program reached through a
  narrow, generic hook. Its code is not in this repository. Without one
  configured, SQL sources are refused.
- **Agents compose their own questions** from the same structured language
  widgets use.
- **Approximate distinct counts (about 1%) are acceptable** where exactness
  would cost memory (tentative yes: "I think so"). This reverses raw-first
  D9's "no estimates" for those cases.
- **No precomputed partials as data.** No extra files the daily pass
  writes and someone must maintain, back up and clean. Per-day partial
  results may exist only as a **disposable cache**, generated on demand,
  in a cache directory with a size cap, safe to delete at any time.
- **No backward compatibility for widgets.** No converter, no upgrade
  path for SQL widgets. (Builder's call: saved SQL widgets and shares of them are kept,
  not deleted; they fail with the typed "needs an external SQL engine"
  error, and run as before where the hook is configured. Nothing
  irreversible happens on upgrade.)
- **Shipped as stacked PRs, released one by one.** DuckDB stays in this
  repository while the native engine takes over (the parity check needs
  it, and prod keeps working); the final `feat!` release removes it and
  adds the SQL hook together.
- **Go.** A Rust rewrite of the server or a Rust engine sidecar were
  considered and rejected: Rust's DuckDB crate wraps the same C++ library;
  its pure-Rust option (DataFusion) only matters for SQL, which leaves the
  server; the native engine's speed comes from what it reads, not the
  language, and Go can hold a ~20 MB idle budget (`GOMEMLIMIT`); the
  server is ~28.5k lines plus ~49.5k of tests.

## Targets

Many instances should fit on one modest host, so idle and peak memory per
process are the budget that matters. The workload envelope is **100,000 to
1,000,000 events per month per instance**; every target holds across it,
so the upper end is what the engine is sized for. Targets per process:

| | Target | Today |
|---|---|---|
| Idle RSS | ~20 MB | 30 MB (after #200), no engine child |
| Peak during a dashboard load | under ~150 MB, released after | 600–800 MB in the DuckDB child |
| A built-in dashboard, 30-day range, no cache | under 0.5 s total at 1M events/month; tens of ms at 100k | 0.35 s+ plus a 23–100 ms spawn |
| A built-in dashboard, 1-year range | under 1 s total with a warm cache, under ~3 s cold, at 1M events/month | ~3 s at 2 threads |
| Binary | no cgo; target ≤ 30 MB | 81 MB, cgo |

These are targets to measure against, not promises; step 1 of the build
measures them.

## Goals

- One native Go engine answers every built-in dashboard, every custom
  widget, and the agent's `query` tool, within the budget above.
- Widgets are standardized: a widget's source is a structured query,
  validated structurally when it is saved, so a saved widget cannot fail
  with a syntax error or change meaning on an engine upgrade.
- The daily pass (dump, month merge, derived sessions, identities,
  aliases) runs without DuckDB.
- Pure-Go release build: no cgo, plain cross-compiling. 32-bit ARM may
  come back (a free side effect, not a goal to test for in v1).
- A narrow, published hook lets an external SQL engine answer SQL
  sources. With the hook unset, SQL sources are refused with a
  typed error.

## Non-goals

- A SQL engine of our own, or any subset of SQL.
- Funnels, paths/sankey, one-actor timelines in v1 (no built-in uses them;
  the survey found them only as documentation examples). They are the
  first candidates for later kinds, for agents.
- Chaining queries (passing one query's actor set into another's filter)
  in v1.
- An external SQL engine itself; this design only fixes the hook one
  plugs into.
- Migrating existing SQL widgets.

## Anti-goals (a failure even if it works)

- Per-day or per-month partial files that the daily pass writes and that
  have to be kept correct, backed up or cleaned up by hand.
- Redis or any other server to install beside the binary.
- A query an agent can send that makes memory grow with traffic: every
  kind has a bounded memory model, and past the bound the engine refuses
  or trims with a clear message.
- Approximate numbers presented as exact.
- A per-dashboard special case in Go: built-in dashboards are ordinary
  widgets over the same structured language.

## What the built-ins need (survey, 2026-10-20)

All 63 system widgets and the analytics tools were read
(`internal/reporting/system/`, `internal/api/ops_*.go`):

- Every query is one project and `day BETWEEN from AND to`. The only time
  bucket is `day`.
- Most figures are per-day then combined: "visitors" is the per-day
  distinct actors **summed** over days; the "users" columns in product
  and table widgets are the **max** of per-day distincts. So the engine
  needs a two-stage aggregate: inner per day (and dimension), outer sum,
  max or avg.
- Whole-range distinct counts appear in: active users/groups,
  actions-per-user/group, new users/groups, top users/groups "active
  days", attribute values users/groups (up to a year), list_attributes,
  product_attributes (capped at 92 days today because it runs out of
  memory), and retention (per cohort × offset).
- Previous-period comparison is used by three stat widgets.
- Shapes outside a plain trend/breakdown: retention cohorts with a
  clock-based denominator and D7/D30/D45 milestones; the attribute
  explorer (every attribute key × value with counts); new vs returning
  (`day = first_seen`); top-4-plus-Other and top-5-then-trend; vitals
  ratings against fixed thresholds; per-page vitals pivoted to columns;
  weighted percentiles from log-scale measure buckets.
- Form attribution (`internal/archive/attribution.go`) is internal code,
  not a user query: an actor/install/alias lookup in a time window.

## The structured query language

A widget source becomes `{"type": "query", "query": {…}}` (or `md`). The
agent's `query` tool takes the same object. The kinds for v1:

| Kind | Answers | Notes |
|---|---|---|
| `stat` | one number | optional `compare: "previous"` (the preceding range of equal length); optional per-day sparkline |
| `trend` | a metric per day, optionally split into series by one dimension | `series_limit` with top-N over the whole range (top-5-then-trend) |
| `breakdown` | a ranked table by 1–3 dimensions, one or more metrics | `limit`, `other: true` (fold the rest into "Other"); remote table paging/sort/filter on top |
| `retention` | cohort grid and pooled curves | cohort = first active day; activity filter; offsets 0..N; user vs install split as today; milestones |
| `percentiles` | p50/p75/p95 and weighted mean of measures and vitals | by day, by event, by one or two dimensions; ratings against named thresholds |
| `attributes` | every attribute key and value with counts | the attribute explorer, `list_attributes`, `product_attributes` |

Common fields: `table` (`views`, `product`, `measures`, `sessions`,
`events`, `identities`), `where` (a closed operator set — `eq`, `ne`,
`in`, `not_in`, `contains`, `prefix`, `gt`, `gte`, `lt`, `lte`, `is_set`
— on columns and `attr_*`), `metrics`, `dimensions`, `limit`, `order`.

Metrics (closed set): `count`, `actors` (distinct actors), `users`,
`groups`, `sum`, `avg`, `min`, `max` of a numeric column, `share` (of the
total), `ratio` of two metrics, and the per-day combinators the built-ins
use (`daily_sum`, `daily_max`, `daily_avg` of a per-day distinct).
Sessions add `sessions`, `bounces`, `bounce_rate`, `duration`.

Dimensions: any column of the table, any `attr_*`, `first_seen_day`
(new vs returning), and a few computed ones the built-ins need
(`resolution` = width×height, `device_label` = model or device,
`platform_version` = platform + app version, `consent_state`).

The exact field names, the JSON schema, and which combinations each kind
accepts are the builder's to settle, with three constraints: every
built-in widget is expressible; the MCP tool schema carries the whole
language inline (as widget props are today, no `$defs`), so an agent
learns it from the tool definition; and an invalid query is refused at
save time (widgets) or call time (`query`) with a typed `manage.ErrInvalid`
naming the field.

Every result carries an `approximate` flag per column when any value in
it came from a sketch; components show it (a "≈" marker is enough).
Builder's call (from the review): approximation applies to **distinct
counts only**. Plain counts, sums and rankings are exact, from the cache
too: a cache entry holds the day's full group table (under the kind's
caps), never only a top-K, so a merged range ranks exactly.

**Visitors stay "once per day, summed".** A cookieless visitor, the default, is a hash of the
connection under a key that rotates at midnight (`docs/twillingate.md`),
so the same person has a new id every day and a range-distinct count is
impossible for most visitors; it would dedupe only consenting visitors
and mix two meanings in one figure. This is the Plausible convention too.
The docs say it plainly. Product `users`/`groups` come from ids that do
not rotate (user, install, group), so their whole-range figures are
distinct over the range, approximate where the engine needs it.

## The engine

**Reading.** Parquet is read column by column: a query reads only the
columns it needs, reads each file's footer first, and skips every row
group whose min/max statistics (project, day) fall outside the query.
Dictionary-encoded string columns are grouped and counted by dictionary
index, and decoded only for the rows that are returned. The open NDJSON
log is parsed with the same decoders the ingest path uses. This
"scan smart every time" path is the engine; everything else is an
optimization.

**Aggregation.** Per kind, one pass over the scanned rows into a hash
aggregate keyed by (day, dimensions). Distinct actors per day are exact
(a per-day set); a whole-range distinct is exact while the set is under a
threshold and switches to HyperLogLog above it (the threshold and the
sketch precision are the builder's, chosen against the memory target).
Retention needs per-actor first day and active days: exact per actor id
within the range, with the actor id mapped to a dense integer per query.

**Memory bounds.** Each kind has a written memory model (rows × width,
groups × state size). Breakdowns cap group count (top-N with Other, and
a hard cap on distinct keys tracked); retention caps cohort days and
offsets; `attributes` caps keys and values per key (today's
`ATTRIBUTE_VALUES_TOP_N` and friends). Past a cap the engine trims and
says so in the result (`truncated`), or refuses with a typed error; it
never grows unbounded.

**Concurrency.** Queries run in the server process. A per-process
semaphore (default: a small number, e.g. 2) bounds concurrent scans so a
dashboard of many widgets cannot multiply peak memory. Widgets of one
dashboard load run one or two at a time, as the measurements in
raw-first found best on small boxes.

**The cache.** A disposable per-day partial cache:

- Lives under a cache directory (default under the data directory, e.g.
  `cache/`, configurable), with a size cap and least-recently-used
  eviction; a small in-memory layer above it within the memory budget.
- Keyed by (project, day, a hash of the query's scan shape: table, where,
  dimensions, metrics) and by the file's identity (path + size + mtime or
  a generation number), so a rewritten day file or month file simply
  misses.
- Entries hold per-day partial state: counts, sums, the full
  per-dimension group table (under the kind's caps; not a top-K), HLL
  sketches, measure bucket weights.
- Built on demand the first time a query needs that day; never by the
  daily pass. Today and the open log are never cached.
- Rule changes (rename, merge, block), late events landing on a closed
  day, and retention deletions drop the affected project's entries (or
  bump a per-project generation). No repair logic.
- Not backed up, not migrated; a missing or corrupt directory is rebuilt.
  Deleting it is always safe.
- Ships behind a setting that can turn it off; the engine must meet the
  30-day targets across the whole envelope without it.

## The daily pass without DuckDB

`internal/archive` today runs DuckDB `COPY … TO` for the day dump
(NDJSON → Parquet), the month merge, and the derived `sessions`,
`identities` and `aliases` files. In Go:

- A Parquet writer and reader. Per CLAUDE.md, a dependency needs a very
  reputable vendor: Apache Arrow's Go Parquet package qualifies;
  `parquet-go/parquet-go` is the other candidate. Builder chooses by
  measurement (write speed, memory while merging a month, read speed for
  the engine's column/row-group access) and records why. Writing our own
  Parquet codec is out of scope.
- File layout stays as raw-first D2/D4 defined it, with two requirements
  the engine relies on: rows ordered by family then time within a file,
  and row groups small enough to make day pruning useful (builder
  measures; ~one day per row group in a month file is a starting point).
- Sessionization, identities and aliases become Go over the day's rows;
  the semantics stay exactly as `internal/archive/derive.go` defines them.
- Merging a month streams row groups; it never loads a month into memory.

## SQL: the external engine hook

- The widget source type `sql` stays in the contract but is only
  accepted when an external SQL engine is configured: one environment
  variable with the engine's address (a Unix socket path; name is the
  builder's, e.g. `SQL_ENGINE_SOCKET`, documented in
  `docs/deployment.md`). Unset: saving a SQL widget or calling `query`
  with SQL is refused with a typed error that says SQL needs an external
  engine.
- **What the engine is sent: the SQL plus a table spec.** Today SQL never
  reads raw files directly: `internal/archive/reader.go` builds the seven
  tables as DuckDB views that apply the event and attribute rules
  (rename, merge, block) on read, load the open NDJSON logs, and derive
  sessions, identities and aliases for days without a derived file. The
  hook sends the SQL, its args, `max_rows`, the project, and a **table
  spec**: the file lists per table, the open log paths, and the project's
  rules. Turning a table spec into tables is the engine's job. Storage
  does not change: the server and an engine read the same event files;
  the table spec is a per-request message, never stored.
- An engine must reproduce the rule semantics, open-log parsing and
  sessionization the native engine applies; the table spec carries
  everything it needs. Baking rules into the files was rejected: rules
  apply to the past by design.
- An engine reads the event files directly and only needs read access to
  `raw/`; paths in a table spec always resolve inside it.
- `internal/engine`, the `twillingate engine` subcommand,
  `internal/shared/enginesql`, the reader's SQL, the DuckDB dependency and
  cgo leave this repository in the final release.

**The protocol (v1): HTTP with JSON over a Unix socket.** Standard library,
`curl --unix-socket` to debug, cancellation through the request context.
Message shapes follow today's pipe protocol
(`internal/engine/protocol.go`).

| Call | Purpose |
|---|---|
| `GET /v1/health` | protocol and engine version; twillingate checks it at startup and refuses a mismatch with a clear error |
| `POST /v1/query` | `{sql, args, max_rows, project, tables}` → `{columns, rows, truncated}` |

Errors by status: 400 = the engine refused the SQL (message shown to the
user, as `QueryError` today); 503 with `Retry-After` = the engine is
busy; 500 = the engine failed. The protocol and table-spec types live in
a small package in this repository so an engine can import them; the
file layout and table names are part of the versioned contract.

## What changes in the repo

- `internal/reporting`: widget sources become structured queries;
  validation is structural; the 63 system widgets are rewritten as
  structured queries; `components.json` gains whatever the components
  need for the `approximate` flag.
- `internal/api`: `query` takes the structured language; the fixed
  analytics tools (`views_overview`, `views_breakdown`, `product_events`,
  `product_attributes`, `measures`, `retention`, `identities`) become thin
  presets over the engine or are dropped in favour of `query` (builder's
  call; lean towards dropping — one way in).
- `internal/archive`: Go Parquet writing, Go derivations, the reader
  serving the engine instead of building SQL.
- A new package for the engine (rank in `internal/archtest`).
- `internal/sqlcheck`: whatever still reaches DuckDB is gone; it keeps
  checking SQLite.
- Docs: `docs/reporting.md` teaches the structured language (the
  component table and worked examples); `docs/twillingate.md` drops
  "writing SQL against the event files"; `docs/deployment.md` drops the
  engine variables (`ENGINE_MEMORY_LIMIT`, `ENGINE_MAX_CHILDREN`, …) and
  adds the cache and SQL hook settings; `deploy/UPGRADES.md` says SQL
  widgets are refused after the upgrade. `docs_sync_test.go` follows.
- Web: the widget editor builds structured queries (the SQL box shows
  only when the server reports an external engine).
- Release: `make dist` and `release.yml` drop cgo and the arm64 C
  toolchain.
- Commits: `feat!` (breaking for widgets and `query`).

## Order of work (suggested)

1. **Measure first.** With `scripts/archive-bench`'s generator at 100k and
   1M events/month over a year: the candidate Parquet libraries' read and
   write; a native scan of three representative built-ins (visitors by
   country, DAU, attribute values) with no cache. This settles the
   library, the row-group size, and whether both ends of the envelope meet
   the 30-day targets uncached.

   **Step 1b.** Optimise the native queries (count by dictionary index
   into arrays, sort-based distinct instead of hash sets, parallel row
   groups, early family/day pruning by row-group statistics) and re-measure 1M on a
   quiet host. Add a mid-month 30-day range and a 7-day range, so day
   pruning inside a file is exercised, before choosing a row-group size.
   Re-measure the library at 1M too (write wall; reads of files each
   library wrote). Decide the cache and the row-group size from those
   numbers. **Done 2026-10-22**: see "Measurements (step 1b)". Page-index
   pruning was not tried; it is left for step 2.

2. The engine and the language for `stat`, `trend`, `breakdown`;
   `query` over MCP.
3. `percentiles`, `retention`, `attributes`; rewrite the system widgets;
   parity check against today's DuckDB results on the same files (as the
   raw-first switch did).
4. The daily pass in Go (DuckDB still in the tree for SQL widgets and the
   parity check).
5. The cache, only if it shows great results (owner, 2026-10-21): the
   default is no cache. It is built only when the measurements show the
   1-year target missed without it and the cache bringing that dashboard
   several times faster, enough to justify its moving parts. Step 1b
   found the 1-year target missed and an upper bound of 37x, so this step
   proceeds; it measures the real gain first and drops the cache if the
   gain is not large.
6. The SQL hook, the final `feat!` release: DuckDB, cgo and
   `internal/engine` leave this repository in the same release.

Each step is a PR stacked on the previous one and released on its own;
until step 6, SQL widgets and the daily pass may still use DuckDB.

## Measurements (step 1)

Data and runs: `scripts/native-bench` (full tables in its `RESULTS.md`).
Host 4 CPUs, 30 GB; a year of months per volume, project 1; DuckDB at 2
threads, every native run at GOMAXPROCS=2. "Best" is warm (best of runs 2
and 3, OS cache warm). Caveats:

- The data is the synthetic generator's, with fixed vocabularies
  (attribute values return 20,367 rows at both ranges), so cardinality
  and skew differ from real data. It has no `group_id`, so the group sets
  of attribute values were never built natively (nor in the DuckDB
  baseline); real attribute values cost more.
- Some Product widgets return 0 rows in the baseline (users by app
  version, app versions; `RESULTS.md`, Task 1).
- The 1M native runs were disturbed by another process (load average up
  to 8.69 on 4 CPUs), and the load differed by cell (see the library
  choice below).
- Ranges are month-aligned (one month file for 30 d, twelve for 365 d),
  so day pruning inside a file is never exercised.
- Nothing was measured cold: the "first" runs follow earlier cells over
  the same files, so the OS cache was warm. The "under ~3 s cold" target
  was not measured; the verdict below holds regardless, since warm runs
  already miss.
- The "Write" rows below are a Parquet-to-Parquet rewrite of an existing
  month, with fsync in the wall, not the daily pass's NDJSON dump and
  month merge.

**Today's DuckDB, whole dashboards** (best of 3, one widget at a time;
child peak RSS):

| Dashboard | Range | 100k | 1M | Peak RSS at 1M |
|---|---|---|---|---|
| Views (20 widgets) | 30 d | 550 ms | 1.11 s | 171 MB |
| Views | 365 d | 1.48 s | 10.9 s | 853 MB |
| Product (10) | 30 d | 306 ms | 1.60 s | 729 MB |
| Product | 365 d | 608 ms | 5.28 s | 812 MB |

**Native, three queries one after another** (visitors by country, DAU,
attribute values; no cache; 3 of the ~20 widgets of a Views dashboard).
The spread is over the layouts and the two libraries as source; peak RSS
is the largest of the three queries, per range, with the range over
parquet-go as source in brackets.

| Volume | Range | Sum of best walls | Peak RSS |
|---|---|---|---|
| 100k | 30 d | 143–218 ms (attribute values ~100 ms of it) | 46–97 MB (parquet-go 46–48) |
| 100k | 365 d | 1.05–1.65 s | 66–137 MB (66–78) |
| 1M | 30 d | 1.09–2.79 s (best: attribute values 0.76 s) | 128–207 MB (128–142) |
| 1M | 365 d | 12–26 s first run (best 13–26 s) | 401–541 MB (401–431) |

Against the ~150 MB peak target: met at 100k at both ranges, met at 1M
and 30 d with parquet-go as source (128–142 MB; arrow-go 171–207 MB
misses), missed at 1M and 365 d with either library (401 MB and up).

**Parquet libraries.**

| | arrow-go | parquet-go |
|---|---|---|
| Read, 100k, 30 d, same layout (sum of best walls) | 174–200 ms | 143–152 ms |
| Read, 100k, 365 d | 1.40–1.61 s | 1.05–1.21 s |
| Read peak RSS, 100k (365 d) | 73–75 MB | 66–68 MB |
| Write a 100k month, max wall / writer peak RSS | 629–780 ms / 147–167 MB | 451–491 ms / 63–77 MB |
| Write a 1M month at 133,333-row groups, writer peak RSS | 597 MB | 322 MB |
| Write a 1M month at 33,333-row groups, writer peak RSS | 271 MB | 208 MB |
| Stripped binary over an empty main | +20.6 MB | +5.7 MB |

Output size varies about 8% across the four layouts (100k year: 52.7 to
57.1 MB; 55–57 MB arrow-go, 53–56 MB parquet-go); every one is 11–21%
larger than DuckDB's own 47 MB.

**Decisions** (the numbers that decided them):

- **Parquet library: `parquet-go/parquet-go`, on 100k evidence only (confirmed at 1M in step 1b).**
  Faster and leaner reads than arrow-go (the 100k comparison is clean:
  143–152 ms against 174–200 ms for the 30-day trio, 66–68 against 73–75
  MB), a lower writer peak on a real 1M month at 133,333-row groups (322
  MB against 597 MB), and +5.7 MB of binary against +20.6 MB. At 1M
  parquet-go looks worse, but the load confounds it:

  | 1M | arrow-go | parquet-go |
  |---|---|---|
  | Max write wall, 1-day groups | 5.2 s | 8.2 s |
  | Max write wall, 4-day groups | 5.9 s | 10.7 s |
  | 1-day groups, first six months of writes | 5.0–5.2 s | 4.0–4.2 s |
  | 30 d trio, parquet-go reading 1-day files written by | 1.09 s (load 2.4–2.6) | 2.08 s (load 5.0–6.1) |

  Step 1b re-measures the library at 1M (write wall; reads of
  parquet-go-written files) on a quiet host before this is final.
- **Row-group size: provisional (settled in step 1b: 1 day).** At 100k, groups of 4 days (13,333
  rows) beat groups of 1 day (3,333 rows) by about 13% on the 365-day
  trio (1,045 against 1,197 ms with parquet-go), make files 6% smaller
  and cost a little more writer memory (77 against 63 MB); at 30 days
  the two are level (143 against 150 ms). The comparison is biased
  towards 4 days: ranges are month-aligned, so no day pruning happens,
  and smaller groups only pay overhead. Files sort by family first, so a
  "1d" group holds about 2–3 days of one family and a "4d" group about
  8–10. The 1M layout comparison is unreliable (load, below), so the
  size is chosen in step 1b, with mid-month ranges.
- **30-day targets, uncached: missed at both ends (step 1b: trio met at 1M, full dashboard not established, see below).** The target is "tens
  of ms" at 100k and under 0.5 s at 1M, for a whole dashboard. At 100k
  the trio alone (3 of ~20 widgets) takes 143–218 ms, so "tens of ms" is
  missed and "under 0.5 s" for a full dashboard is not established;
  attribute values dominates at ~100 ms. At 1M the best trio is 1.09 s
  against 0.5 s (attribute values 0.76 s of it): missed. That best came
  from the quietest cells (load 2.4–2.6), so the miss is robust to the
  load.
- **1-year target: missed by far, and slower than today (step 1b: still missed at 3.27 s uncached with a warm OS cache, see below).** At 1M the
  trio takes 12–26 s (first runs; best 13–26 s) with a peak up to 541
  MB, against 10.9 s for the whole Views dashboard and 5.3 s for Product
  in DuckDB. The queries measured are a deliberately naive first cut
  (exact sets in Go maps, one row group per batch, one thread per query),
  so these numbers bound nothing about an optimised engine.
- **Writer memory (step 4).** At 1M the parquet-go writer peaks at 208 MB
  (1-day groups) and 322 MB (4-day groups), above the ~150 MB budget; the
  daily pass in Go must bound it (smaller batches, or a smaller group).
- **Cache: not decided in step 1** (decided in step 1b).

**Host load at 1M** (1-minute load average at cell start, from
`matrix-1m.json`): the DuckDB layout, measured first, saw the highest
(5.8–8.7); arrow-go-written 1-day files read with parquet-go the lowest
(2.4–2.6); the later layouts 4–7. Load also differs within a layout,
between the two sources.

## Measurements (step 1b)

Data and runs: `scripts/native-bench` (full tables in its `RESULTS.md`,
"Step 1b"). Same host and data as step 1, with the queries optimised
(counts by dictionary index into arrays, sort-based distinct, parallel row
groups; pruning by row-group statistics only, the page index is not used) and the matrix gated: every run waited for a
1-minute load average of at most 1.0 (actual 0.81 to 1.00, no run flagged).
Native runs use GOMAXPROCS=2 and 2 workers; 3 runs per cell, "best" is the
minimum of runs 2 and 3. Ranges: 1 d, 7 d, 30 d mid-month (spans two
month files, so day pruning inside a file is exercised), 30 d (a whole
month) and 365 d. Layouts: `pqgo-8h` (volume/90 rows per group), `pqgo-1d`
(volume/30), `pqgo-4d` (volume*4/30), `arrow-1d`, DuckDB's own files.
Caveats: the data is still the generator's (no `group_id`, same users
every month, fixed vocabularies); nothing was measured cold (the OS cache
was warm), so the "cold" 1-year target is judged from the warm figure,
which already misses; a trio is 3 of the ~20 widgets of a Views dashboard,
so no full dashboard is established.

**The chosen layout, 1-day groups, written and read by parquet-go.** Trio =
visitors by country + DAU + attribute values (approximate); sum of best
walls, first run in brackets, peak RSS of the largest query.

| Range | 100k | 1M |
|---|---|---|
| 1 d | 32 ms (33), 32 MB | 88 ms (94), 52 MB |
| 7 d | 54 ms (55), 41 MB | 191 ms (207), 65 MB |
| 30 d mid-month | 112 ms (118), 49 MB | 431 ms (451), 83 MB |
| 30 d | 94 ms (98), 50 MB | 402 ms (415), 76 MB |
| 365 d | 570 ms (576), 65 MB | 3,274 ms (3,280), 186 MB |

Per query at 1M, 30 d / 365 d, best wall: countries 93 ms / 776 ms, DAU
122 ms / 1,112 ms, attribute values exact 539 ms / 5,243 ms, attribute
values approximate 187 ms / 1,385 ms. DuckDB's own files read natively:
trio 531 ms / 4,591 ms at 1M.

**Row-group size** (trio, best wall, 1M; 100k in brackets):

| Range | 8 h | 1 d | 4 d |
|---|---|---|---|
| 1 d | 97 (63) | 88 (32) | 164 (34) |
| 7 d | 182 (81) | 191 (54) | 294 (62) |
| 30 d mid-month | 492 (166) | 431 (112) | 539 (109) |
| 30 d | 470 (131) | 402 (94) | 416 (81) |
| 365 d | 3,906 (944) | 3,274 (570) | 3,258 (459) |

**Parquet library at 1M.** Files of 1-day groups, written by each library
and read by each (best wall; the smallest ratio is exact attribute values
at 30 d, 595 against 539 ms, 1.10x):

| | parquet-go reads | arrow-go reads |
|---|---|---|
| 30 d trio, pqgo-1d files | 402 ms | 545 ms |
| 365 d trio, pqgo-1d files | 3,274 ms | 4,772 ms |
| 365 d approx attribute values, pqgo-1d files | 1,385 ms, 139 MB | 2,212 ms, 201 MB |

| Write a 1M year (12 files, 1-day groups), gated | parquet-go | arrow-go |
|---|---|---|
| Total wall | 48.1 s | 60.3 s |
| Slowest file | 4.1 s | 5.1 s |
| Writer peak RSS | 203 MB | 268 MB |
| Size | 508.7 MB | 533.6 MB |

**What the optimisation bought** (pqgo-1d, parquet-go, best wall against
step 1's naive scans; step 1's exact attribute values stand in for the
approximate column). Upper bounds: step 1 ran on a noisier host (100k at
load 1.3 to 2.3; 1M at 2.4 to 8.7, and 5.0 to 6.1 for this layout and
source).

| Query | Range | 100k step 1 -> 1b | 1M step 1 -> 1b |
|---|---|---|---|
| countries | 30 d | 18 -> 16 ms (1.1x) | 234 -> 93 ms (2.5x) |
| DAU | 30 d | 25 -> 21 ms (1.2x) | 340 -> 122 ms (2.8x) |
| attribute values, exact | 30 d | 107 -> 70 ms (1.5x) | 1,510 -> 539 ms (2.8x) |
| attribute values, approximate | 30 d | 107 -> 57 ms (1.9x) | 1,510 -> 187 ms (8.1x) |
| countries | 365 d | 157 -> 122 ms (1.3x) | 2,303 -> 776 ms (3.0x) |
| DAU | 365 d | 228 -> 145 ms (1.6x) | 3,321 -> 1,112 ms (3.0x) |
| attribute values, exact | 365 d | 811 -> 523 ms (1.6x) | 14,475 -> 5,243 ms (2.8x) |
| attribute values, approximate | 365 d | 811 -> 303 ms (2.7x) | 14,475 -> 1,385 ms (10.5x) |

**Decisions** (the numbers that decided them):

- **Row-group size: about one day of rows per group (volume/30).** At 1M
  it is best or within 5% on every range, and ties 4 d at 365 d
  (3,274 against 3,258 ms); 8 h is worse on 30 d (470 against 402 ms) and
  365 d (3,906 against 3,274 ms) and not better on 1 d (97 against 88
  ms); 4 d is slower on short ranges (1 d: 164 against 88 ms; 7 d: 294
  against 191 ms; 30 d mid-month: 539 against 431 ms). At 100k 4 d wins
  the long ranges (365 d: 459 against 570 ms; 30 d: 81 against 94 ms; 30 d
  mid-month: 109 against 112 ms) and
  loses the short ones (1 d: 34 against 32 ms; 7 d: 62 against 54 ms),
  by less in absolute terms than at 1M. The engine is sized for 1M,
  where 1 d is best or within 5% on every range, so it overrides the 100k result.
  This is the best size without page-index pruning, which was not tried
  (the reader skips the page index) and is left for step 2; page pruning
  could favour larger groups on short ranges, so the size is open to
  revisit then.
  Writer peak at 1M is 203 MB for
  1 d against 322 MB for 4 d. Files sort by family first, so a group
  holds about 2-3 days of one family.
- **Library at 1M: parquet-go confirmed.** It reads 1.2 to 1.6x faster
  than arrow-go on the trio queries at 1M (1.17 to 1.60x across ranges; 1.3 to
  1.6x at 30 d and 365 d: trio 402 against 545 ms and 3,274 against 4,772 ms), down to 1.1x on exact attribute values;
  it writes the year in 48.1 s against 60.3 s with a 203 MB writer peak
  against 268 MB, and adds 5.7 MB to the binary against 20.6 MB (step 1).
  The step 1 doubt about 1M writes was load: behind the gate the slowest
  parquet-go file takes 4.1 s against 8.2 s in step 1, arrow-go's 5.1 s
  against 5.2 s.
- **30-day targets, uncached.** At 1M the trio takes 402 ms uncached,
  OS cache warm (415 ms first run) against under 0.5 s: met for the trio. At 100k it takes 94 ms (98 ms
  first, 81 ms with 4-day groups) against "tens of ms": not met, though
  borderline. The trio is 3 of the ~20 widgets of a Views dashboard, so
  neither verdict establishes a full dashboard.
- **1-year target at 1M: missed.** The trio takes 3,274 ms uncached,
  OS cache warm (peak 186 MB, over the ~150 MB budget), against under 1 s
  with a warm engine cache. The cold target (under ~3 s) is missed too,
  since even this figure is above it. A cache cannot rescue it: a cold
  load runs the uncached path and builds the cache, and the trio alone is
  3 of ~20 widgets, so the cold target needs a faster scan or a revised
  target. Nothing was measured cold. At 100k the trio takes 570 ms.
- **Cache: yes, step 5 proceeds** (owner's rule: none unless the results
  are great; plan rule: yes only if the 1-year target is missed and the
  upper-bound speedup T(365 d) / T(1 d) is at least 5x). T(365 d trio) =
  3,274 ms and T(1 d trio) = 88 ms, so the bound is about 37x. It is a
  theoretical upper bound: it ignores merge cost and reading today's open
  log (never cached). Step 5 builds the disposable per-day cache exactly
  as "The cache" above describes, measures the real 1-year gain first
  and drops the cache if the gain is not large.
- **Approximate distinct users ship** for whole-range distinct counts.
  Attribute values at 1M and 365 d: 1,385 ms and 139 MB approximate
  against 5,243 ms and 395 MB exact (3.8x faster, 2.8x less memory);
  exact is over the ~150 MB budget by itself, which is a second reason.
  Against exact, the largest error on counts of 50 or more is 1.73% (1M,
  30 d mid-month; 1.31% at 100k) and no value fell outside the 2%
  tolerance in the 35 comparisons at each volume. That is less evidence
  than the cell count suggests: the fingerprints are deterministic, so the
  7 layout and source combinations are one sample per range, and 30 d and
  365 d hold the same users. The HLL path (values above 1,024 users) was
  exercised by at most 367 values per range at 1M and 27 at 100k. The 2% bound is statistical
  (about 2.5 sigma at 2^14 registers), not guaranteed. The check is
  weaker than it looks: the generator has no group ids (the groups half
  compares 0 with 0) and repeats the same users every month (30 d and
  365 d have the same distinct users per value).

## Measurements (step 3f)

Every built-in dashboard loaded as the console loads it (all widgets at
once), native against the old SQL on DuckDB, on fresh files at 100k and
1M events a month (a year, with group ids and web vitals), GOMAXPROCS=2,
behind the quiet-host gate (every cell started at a 1-minute load of
0.87 to 1.00). "Best" is the fastest of 3 warm runs in the same process;
no result cache on either side. Peak is VmHWM: the Go process for
native, the Go process plus the engine child for DuckDB. Method, the
100k table and the tools: `scripts/native-bench/RESULTS.md`, "Step 3f".

At 1M (wall first / best; peak):

| Dashboard | Range | Native | DuckDB | Native peak | DuckDB peak |
| --- | --- | --- | --- | --- | --- |
| Views (20) | 7 d | 911 / 874 ms | 732 / 604 ms | 135 MB | 162 MB |
| Views (20) | 30 d | 2.20 / 2.15 s | 1.24 / 1.09 s | 171 MB | 208 MB |
| Views (20) | 90 d | 6.70 / 6.11 s | 2.88 / 2.78 s | 238 MB | 387 MB |
| Views (20) | 365 d | 24.5 / 23.9 s | 14.6 / 11.5 s | 651 MB | 810 MB |
| Product (10) | 7 d | 712 / 698 ms | 619 / 522 ms | 169 MB | 328 MB |
| Product (10) | 30 d | 1.64 / 1.51 s | 1.89 / 1.71 s | 236 MB | 790 MB |
| Product (10) | 90 d | 4.33 / 4.38 s | 2.55 / 2.39 s | 315 MB | 657 MB |
| Product (10) | 365 d | 17.7 / 16.9 s | 5.72 / 5.64 s | 693 MB | 886 MB |
| Users (6) | 30 d | 645 / 511 ms | 521 / 396 ms | 216 MB | 218 MB |
| Groups (6) | 30 d | 349 / 246 ms | 326 / 243 ms | 143 MB | 164 MB |
| Retention (5) | 90 d (default) | 1.96 s / 2 ms | 1.39 / 1.21 s | 116 MB | 186 MB |
| Web Vitals (11 / 10) | 30 d (default) | 153 / 111 ms | 406 / 334 ms | 81 MB | 155 MB |
| Measures (2) | 30 d | 29 / 29 ms | 162 / 71 ms | 83 MB | 141 MB |

Against the targets:

- **Peak under ~150 MB: missed at 1M for the large dashboards, met for
  the small ones.** Met at 1M by Retention, Web Vitals, Measures at every
  range, Groups to 30 d and Views at 7 d; missed by Views from 30 d
  (171 MB, up to 651 MB over the year), Product at every range (169 to
  693 MB), Users at every range (152 to 281 MB) and Groups at 90 d. At 100k only Views over the
  year (160 MB) and Product from 90 d (155 and 209 MB) miss it. Native
  is below DuckDB's Go-plus-child peak in every cell but one (Users, 90 d,
  1M: 281 against 265 MB), often by half.
- **30-day dashboard under 0.5 s at 1M, uncached: missed for Views
  (2.15 s), Product (1.51 s), Users (511 ms) and Retention (2.06 s on the
  first load); met for Groups (246 ms), Web Vitals (111 ms) and Measures
  (29 ms).** Views at 30 d is twice as slow as DuckDB's 1.09 s.
- **Tens of ms at 100k, 30 days: met for Users (65 ms), Groups (31 ms),
  Web Vitals (44 ms) and Measures (9 ms); missed for Views (377 ms),
  Product (228 ms) and Retention's first load (178 ms).**
- **1-year dashboard at 1M (under ~3 s cold, under 1 s with a warm
  cache): missed by far, and slower than DuckDB.** Views takes 23.9 s
  (DuckDB 11.5 s) and Product 16.9 s (DuckDB 5.6 s), uncached with the OS
  cache warm; no engine cache exists yet (step 5).
- **Against DuckDB:** native is faster for Web Vitals and Measures at
  every range (1.1 to 3.2x at 1M, the gap closing at 90 d), for Groups and Users at 7 d, for Product at
  30 d (1.51 against 1.71 s at 1M) and for Views up to 30 d at 100k; it is
  slower for Views from 7 d at 1M, for Users from 30 d at 1M, and for
  Views and Product from 90 d at both volumes (2.1 to 3.3x over the
  year).

Why, and what the numbers do not cover:

- The files are DuckDB-written month files (one row group a month at
  100k, 122,880 rows a group at 1M), as this branch's daily pass writes
  them. Step 1b's 1-day groups come with the Go writer (step 4), so no
  scan here prunes days inside a month. Step 1b's trio gains came on
  those smaller groups; this has to be measured again after step 4.
- Each widget is its own scan, two at a time (MaxScans 2): a dashboard
  costs about the sum of its scans over two, and Views has 20. One native
  views breakdown at 1M over 30 d takes 110 to 170 ms against 60 to 115
  ms for the old SQL (the tools table), so the per-scan cost, not only
  the count, is behind the miss. Sharing one pass over the files between
  the widgets of a load, or a cheaper scan, is what would close it; the
  per-day cache of step 5 is the lever for the 1-year target.
- Retention's warm loads take 2 ms because the engine caches identities
  in the process; its first load (about 2 s at 1M) is the uncached figure.
- Nothing was measured cold (the OS cache was warm), and "released
  after" (memory returned after a load) was not measured.

## Left to the builder

- Field names and JSON schema of the language; the exact metric and
  dimension lists beyond what the built-ins need.
- HLL precision and the exact/approximate threshold.
- Cache directory default, size cap, eviction details, key format.
- Whether the fixed analytics tools survive as presets.
- Names of new environment variables.
- How the `approximate` flag is drawn.
