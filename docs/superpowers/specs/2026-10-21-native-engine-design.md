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
   groups, early family/day pruning by page index) and re-measure 1M on a
   quiet host. Add a mid-month 30-day range and a 7-day range, so day
   pruning inside a file is exercised, before choosing a row-group size.
   Re-measure the library at 1M too (write wall; reads of files each
   library wrote). Decide the cache and the row-group size from those
   numbers.

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
   several times faster, enough to justify its moving parts.
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

- **Parquet library: `parquet-go/parquet-go`, on 100k evidence only.**
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
- **Row-group size: provisional.** At 100k, groups of 4 days (13,333
  rows) beat groups of 1 day (3,333 rows) by about 13% on the 365-day
  trio (1,045 against 1,197 ms with parquet-go), make files 6% smaller
  and cost a little more writer memory (77 against 63 MB); at 30 days
  the two are level (143 against 150 ms). The comparison is biased
  towards 4 days: ranges are month-aligned, so no day pruning happens,
  and smaller groups only pay overhead. Files sort by family first, so a
  "1d" group holds about 2–3 days of one family and a "4d" group about
  8–10. The 1M layout comparison is unreliable (load, below), so the
  size is chosen in step 1b, with mid-month ranges.
- **30-day targets, uncached: missed at both ends.** The target is "tens
  of ms" at 100k and under 0.5 s at 1M, for a whole dashboard. At 100k
  the trio alone (3 of ~20 widgets) takes 143–218 ms, so "tens of ms" is
  missed and "under 0.5 s" for a full dashboard is not established;
  attribute values dominates at ~100 ms. At 1M the best trio is 1.09 s
  against 0.5 s (attribute values 0.76 s of it): missed. That best came
  from the quietest cells (load 2.4–2.6), so the miss is robust to the
  load.
- **1-year target: missed by far, and slower than today.** At 1M the
  trio takes 12–26 s (first runs; best 13–26 s) with a peak up to 541
  MB, against 10.9 s for the whole Views dashboard and 5.3 s for Product
  in DuckDB. The queries measured are a deliberately naive first cut
  (exact sets in Go maps, one row group per batch, one thread per query),
  so these numbers bound nothing about an optimised engine.
- **Writer memory (step 4).** At 1M the parquet-go writer peaks at 208 MB
  (1-day groups) and 322 MB (4-day groups), above the ~150 MB budget; the
  daily pass in Go must bound it (smaller batches, or a smaller group).
- **Cache: not decided.** The rule is no cache unless it shows great
  results; that waits for step 1b.

**Host load at 1M** (1-minute load average at cell start, from
`matrix-1m.json`): the DuckDB layout, measured first, saw the highest
(5.8–8.7); arrow-go-written 1-day files read with parquet-go the lowest
(2.4–2.6); the later layouts 4–7. Load also differs within a layout,
between the two sources.

## Left to the builder

- Field names and JSON schema of the language; the exact metric and
  dimension lists beyond what the built-ins need.
- Row-group size (step 1b), HLL precision and the exact/approximate
  threshold.
- Cache directory default, size cap, eviction details, key format.
- Whether the fixed analytics tools survive as presets.
- Names of new environment variables.
- How the `approximate` flag is drawn.
