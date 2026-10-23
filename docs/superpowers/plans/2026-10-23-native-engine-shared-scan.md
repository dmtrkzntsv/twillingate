# Native engine, step 4c: the shared scan — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Loading a built-in dashboard runs one scan per table, not one per widget. Every answer stays identical to today's. The aim is that the Views and Product dashboards at 1M events/month reach DuckDB's times or better from 30 days on (today about 1.9 s against DuckDB's 1.09 s for Views at 30 days), and that their peak memory does not grow.

**Architecture:**
- **Engine.** `native.Engine` gains `AnswerMany(ctx, qs []Query, ps []Params) []Result`, with one result or error per query. It works in four stages:
  1. **Validate and compile.** Every query is validated and compiled first.
  2. **Plan.** Compatible queries go into one scan. The scan key is project, table class, and rule set. The table class is events (events, views, product and measures all read the events files) or sessions. Retention, attributes and identities keep their own paths, run once each.
  3. **Scan.** The scan reads the union of the queries' columns over the union of their ranges. For each work item (row group or log) it notes which consumers overlap it. Each batch goes to every overlapping consumer's existing `agg`, after that consumer's own family and day mask, so each `agg` sees exactly the rows its own scan would have produced.
  4. **Finish.** Each consumer then finishes as `Answer` does today.

  Per-batch memoisation means each batch builds a computed column once and hashes an id dictionary once for every consumer.
- **Reporting.** `reporting.Service.WidgetDataMany` checks each widget as `WidgetData` does and serves cache hits. The misses go into one `AnswerMany`.
- **Console (owner, 2026-10-23).** A dashboard load or refresh is one request, `POST /api/widgets/stream`, whose response streams one Server-Sent Event per widget as soon as that widget is ready. Cache hits come first, then each shared-scan group as it finishes. Each card keeps its own react-query entry, filled when its event arrives. Single-widget loads keep `GET /api/widgets/{id}/data`.
- **MCP tools.** The tools that run several queries per call use `AnswerMany`.

**Tech Stack:** Go (`internal/native`, `internal/reporting`, `internal/api`), React/TypeScript (`web/`).

**Spec:** `docs/superpowers/specs/2026-10-21-native-engine-design.md`, order of work 4c "The shared scan". Its anti-goal also binds: "a per-dashboard special case in Go". The survey of today's scan and request paths is the scratch file `.superpowers/shared-scan-survey.md` (a summary; the plan wins where they differ).

## Global Constraints

- **Identical answers.** For every query, `AnswerMany([...q...])` returns the same `Result` as `Answer(q)`: columns, rows, `approximate` and `truncated`. Float sums may differ only in their last bits, as they already do between runs; compare numbers as the parity harnesses do.
  - The truncation decision stays per query. Each consumer keeps its own `MaxGroups` and `MaxBytes` accounting.
  - A test asserts this property over every built-in widget on the system fixture (`internal/reporting/system_test.go`'s fixture), for every preset. It also covers the native parity fixtures' queries, batched in random orders and groupings.
- **Memory.** Each consumer keeps its own `MaxBytes` and accounting.
  - A shared scan also has a total budget of 2×`MaxBytes`, summed over its live consumers. When adding a batch would exceed it, the consumer with the largest held bytes leaves the scan and is answered alone afterwards with `Answer`, so its answer stays identical.
  - A dashboard load's peak must not exceed today's for the same dashboard (the spec's step 4b numbers).
  - At most 32 consumers per scan. More are split into several scans.
- **Concurrency.**
  - One `AnswerMany` scan takes one `turn`.
  - Cancellation is per caller. A consumer whose context is done leaves the scan, and the scan stops when none remain.
  - The `fs.ErrNotExist` retry reruns the whole scan with fresh aggregations.
  - `Answer` stays as it is, as the single-query path, and may itself become `AnswerMany` of one only if that is measured not slower.
- **Errors.**
  - A query that fails validation, or fails on a batch (for example "column is not text"), fails only its own result.
  - A scan-level error (I/O, a corrupt file) fails every consumer of that scan with that error, as each would have failed alone.
- **The stream route.**
  - It is `POST /api/widgets/stream`, REST-only, never an MCP tool.
  - The request body is `{"widgets": [{"widget_id", "project_id", "from", "to", "fresh"}]}`, at most 64 widgets.
  - The response is `text/event-stream`. Each widget gets one `event: widget` with `data: {"widget_id", "data"}` or `data: {"widget_id", "error": {"code", "message"}}`, where `data` has the same JSON as the GET route's body and `error` has the same codes. Events arrive in completion order. A final `event: done` closes the stream.
  - A `: keepalive` comment goes out every 15 s, and every event is flushed. Response headers: `Cache-Control: no-cache` and `X-Accel-Buffering: no`.
  - Closing the request cancels its widgets' queries.
  - The same auth and per-widget checks apply as for `GET /api/widgets/{id}/data`.
  - Remote-table views (paging, sort, filters, distinct) stay on the GET route.
  - The proxy used in production (Caddy, `deploy/`) must not buffer the stream. Check its config, and document any needed setting in `docs/deployment.md`.
  - `docs/twillingate.md` lists the route, and `internal/api/docs_sync_test.go` stays green.
- **The cache.** The reporting cache is keyed per widget, unchanged. Only misses are batched, and their results are stored per widget as today. Concurrent identical widget loads still share one load (singleflight).
- **Web.**
  - `web/src/lib/widget-stream.ts` opens the stream for a dashboard's widgets: all of them on load, all of them with `fresh` on refresh and auto-refresh, using `fetch` with the console's auth and a stream reader, not `EventSource`. It parses SSE and calls `queryClient.setQueryData` for each widget's existing query key.
  - Cards render as their data arrives. A card still pending when the stream ends or fails falls back to its own GET.
  - React-query's per-widget fetch stays the path for a single card, for example a newly added widget or a remote table.
  - A refresh with `fresh` goes through the same path.
  - A query widget can be refreshed like an SQL one. Today `canRefresh` and `WidgetCard` allow `'sql'` only, which is a bug.
  - The 360px phone rule and the pointer-cursor rule apply.
  - The Playwright e2e suite passes.
- **Checks.**
  - `make check` passes before each commit: `PATH=/usr/local/go/bin:$PATH GOTMPDIR=~/.cache/gotmp make check`.
  - Web tasks also run `cd web && npm test && npm run e2e`.
  - Never use `/tmp`.
- **Commits.** Conventional Commits with scopes `native`, `reporting`, `api` and `web`. User-visible speed changes use `perf`; the new route uses `feat(api)`.

## Review Focus

1. **A stat with `compare: previous` batched with trends over the same range.** Its previous period extends the scan's union range. The trends never see the earlier days, and the stat's previous value is identical to `Answer`'s. Pinned by Task 3's property test with a dedicated case.
2. **Two widgets with identical content on one dashboard.** Both get the right answer, whether the engine shares one consumer or keeps two. Two widgets cached under different ids each get their own entry. Pinned by Task 5.
3. **One widget's context cancelled mid-scan** (the card unmounts). The others still answer, and the scan stops if every consumer is cancelled. Pinned by Task 3.
4. **A consumer evicted by the memory budget.** It is answered alone afterwards, and its answer equals `Answer`'s, including `truncated`. Pinned by Task 3 with a tiny `MaxBytes`.
5. **A project's files changing mid-scan** (the daily pass merges a month). The retry reruns every consumer from scratch, and no answer mixes two file sets. Pinned by Task 3, reusing `TestRunRetriesVanishedFiles`' approach.

---

### Task 1: Profile a dashboard load

**Files:**
- `internal/app/measure_system_test.go` (tag `measure`): add a CPU and heap profile option (`-cpuprofile`-style output under `~/.cache`).
- `scripts/native-bench/RESULTS.md`.
- The spec: a short "Profile (step 4c)" note under "Measurements (step 4)".

Profile the Views and Product dashboards at 30 days, 1M events/month, on the step 4b datasets, behind the quiet-host gate. Repeat **with an open log of today's events in range** (about 33k lines), which the earlier benchmarks lacked. Report where time goes:
- footer decode;
- `readChunk` and the dictionaries;
- JSON log decode;
- `selectRows`, `groupsOf` and `addDistinct` (hashing);
- computed columns;
- merge;
- waiting on `turn`.

No code change beyond the profiling hook.

- [ ] Profile, record the result, and commit: `docs: profile a dashboard load on the native engine`.

### Task 2: The cheap fixes the profile found (replaces "memoise per batch")

Ruling after Task 1's profile (spec "Profile (step 4c)"):
- With today in range, decoding the open log is 69–75% of a load's CPU: about 575 ms a decode, done once per widget.
- Without the open log, about half of a load is per-query work: the per-day distinct sort (33% of Views), `map[string]int32` dictionary building (22–27%), id hashing and merge.
- Computed columns are 3%, so the per-batch memo is dropped.

Each part below is its own commit, measured with the Task 1 harness (Views and Product, 30 days, 1M, with and without the log) behind the quiet-host gate.

**2a. The harness.** Its /proc poll for child processes ran on the native cell's cores and slowed native by 14–16%. Poll only for DuckDB cells, or from outside the cell's cores. Re-run the step 4b native Views and Product 30-day cells so the before-numbers are fair. Commit: `test(app): keep the measurement's process poll off native cells`.

**2b. The decoded open log is kept.** The engine keeps each open log's decoded batch: the union of columns any query asked for, with columns added on demand. It is keyed by (path, inode, size). Logs only grow, so when a log has grown, only the bytes past the decoded size are decoded and appended, after the torn-line rule.
- The cache is bounded by bytes: LRU within `MaxBytes`, charged like the identities cache. A log that would not fit is decoded per query as today.
- The rules are applied per query after the cache, as today, so the cache holds raw rows.
- The id dedup (first line per id) spans appends: the cache keeps the seen-id set.
- Tests:
  - parity of every log test, cached against uncached;
  - growth between queries, including a torn tail completed later;
  - a rotation (a new inode) drops the entry;
  - concurrency;
  - the byte bound.

Commit: `perf(native): decode an open log once and keep it while it grows`.

**2c. Per-day distinct without a comparison sort on every batch.** Replace the per-batch key sort in the daily distinct path (`addDistinct` and the per-day sets) with per-day hash sets or a radix approach. Exact counts must stay exact, and the HLL past 1,024 must stay identical in value. The parity tests pass. Commit: `perf(native): count per-day distinct ids without sorting each batch`.

**2d. A chunk's first dictionary without a map.** When a column chunk's dictionary is the first for the batch's column, adopt it as the batch dictionary directly. Remap through a map only when a later chunk or page brings a second dictionary. Commit: `perf(native): take a chunk's dictionary as the batch's without a map`.

**After 2a–2d,** re-measure. Record per-part gains in RESULTS.md and the spec. Task 3 then shares what remains: footers, chunk reads, and the one decoded log, which 2b already shares.

### Task 3: `AnswerMany`

**Files:** `internal/native/engine.go`, `scan.go`, and a new `many.go`.

**Interfaces (produces):**

```go
type Request struct { Query Query; Params Params }
type Answered struct { Result Result; Err error }
func (e *Engine) AnswerMany(ctx context.Context, reqs []Request, ctxs []context.Context) []Answered
```

`ctxs` holds per-request contexts for cancellation; nil means `ctx`. Any shape is fine if it carries per-request cancellation.

**Algorithm** (follows the Global Constraints):
- Plan scans by (project, table class, rules).
- Take the union of columns and the union of ranges.
- Tag each work item with the consumers it overlaps.
- For each consumer, apply a family mask, and a day mask unless the item is `all` for that consumer.
- A stat with `compare: previous` becomes two consumers.
- Sessions consumers share one sessions scan, including gap-day derivation.
- Retention, attributes and identities run alone, after or between.
- The memory budget evicts and reruns as specified.

**Tests:**
- `TestAnswerManyEqualsAnswer`: over the native parity fixtures' queries, batched in random groupings and orders, including mixed tables, mixed ranges, `compare`, sessions, retention and attributes.
- The Review Focus cases 1, 3, 4 and 5.
- `TestAnswerManyOneTurn`: one scan per (project, table class), asserted by counting `turn` acquisitions or file opens.

- [ ] Write the tests first, then implement and run `go test -race ./internal/native/` and `make check`. Commit: `perf(native): answer several queries from one scan`.

### Task 4: Reporting and the stream route

**Files:**
- `internal/reporting/data.go`: `WidgetDataStream(ctx, []WidgetRequest, emit func(WidgetResult))`.
  - Each request goes through `WidgetData`'s checks. Cache hits are emitted at once.
  - The misses' query-source loads go into `AnswerMany` per project. Each shared-scan group's results are emitted, and cached per widget, as soon as the group finishes. That may need `AnswerMany` to call back per group; extend Task 3's API if so.
  - SQL-source widgets run their own loads concurrently and emit when done.
  - `emit` is serialised.
- `query_source.go` exposes a batched load.
- `internal/api`: the `POST /api/widgets/stream` route, with SSE writing, flush, keepalive, and cancellation on client close.
- `docs/twillingate.md` and the docs sync.
- `docs/deployment.md`: proxy buffering. Check the repo's Caddy config and the prod notes.

**Tests:**
- `TestWidgetDataStreamEqualsWidgetData` runs over every built-in dashboard and preset on the system fixture, so every widget's data equals its GET's.
- Cache hits are emitted before any scan finishes.
- A per-widget error (missing, archived, other project) fails only its event.
- Closing the client cancels the queries.
- Keepalive is sent.
- Review Focus 2.
- Route tests cover auth, the 64-widget cap, and that `done` arrives last.

- [ ] Commit: `feat(api): stream a dashboard's widgets from one request`.

### Task 5: The console loads a dashboard through the stream

**Files:**
- `web/src/lib/widget-stream.ts`, as described in the Global Constraints.
- The dashboard page and project dashboard tab (`Dashboard.tsx`, `ProjectDashboardTab.tsx`): open the stream on load, refresh and auto-refresh.
- `WidgetCard.tsx` and `widget-query.ts`: a query widget becomes refreshable, and a card shows its own loading state until its event arrives.

**Tests:**
- vitest: SSE parsing, including events split across chunks and comments; each widget's data lands in its own key; a widget missing from a failed stream falls back to GET; refresh sends `fresh`.
- e2e: a dashboard load makes one stream request and no per-widget GETs; the widgets render; refresh makes one stream request. Assert with Playwright's request log in an existing dashboard spec. The phone and cursor specs pass.

- [ ] Run `cd web && npm test && npm run e2e`, then `make check`. Commit: `perf(web): load a dashboard's widgets from one streamed request`.

### Task 6: MCP tools on `AnswerMany`

**Files:**
- `internal/api/ops_read.go` (`views_overview`: views plus sessions).
- `ops_product.go` (`product_events`: two product queries).
- `ops_usage.go` (one project's queries).

Each runs its queries as one `AnswerMany`. Outputs are unchanged, and the existing parity tests against the old SQL pass.

- [ ] Commit: `perf(api): answer a tool's queries from one scan`.

### Task 7: Measure and document

Re-run the dashboard matrix from step 4b: native, 100k and 1M; Views and Product at 7d/30d/90d/1y; the rest at 30d. Run it through `WidgetDataStream` (time to first widget and to the last), so as the console loads now, behind the quiet-host gate. Report:
- wall time, before (step 4b) and after;
- peak RSS against step 4b;
- one row with today's open log in range.

Add the result to the spec as "Measurements (step 4c)" and to RESULTS.md. State plainly what was met and missed against DuckDB and against the targets.

- [ ] Commit: `docs: measure the dashboards with one scan per table`.
