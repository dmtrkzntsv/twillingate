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
- **Console.** It calls `POST /api/widgets/data`, a REST-only route, through a dataloader that collects the widget-data calls made in the same tick. Each card keeps its own react-query entry.
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
- **The batch route.**
  - It is `POST /api/widgets/data`, REST-only, never an MCP tool.
  - The request body is `{"widgets": [{"widget_id", "project_id", "from", "to", "fresh"}]}`, at most 64 widgets.
  - The response is `{"widgets": [{"widget_id", "data" | "error": {"code", "message"}}]}`, in request order.
  - It uses the same auth and per-widget checks as `GET /api/widgets/{id}/data`.
  - Remote-table views (paging, sort, filters, distinct) stay on the GET route.
  - `docs/twillingate.md` lists the route, and `internal/api/docs_sync_test.go` stays green.
- **The cache.** The reporting cache is keyed per widget, unchanged. Only misses are batched, and their results are stored per widget as today. Concurrent identical widget loads still share one load (singleflight).
- **Web.**
  - The dataloader lives in `web/src/lib/api.ts` (or `widget-query.ts`). It collects the `widgetData` calls made in one macrotask (`setTimeout 0`), sends one POST per (project, from, to), at most 64 widgets each, and resolves each call with its own result or error.
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

### Task 2: Memoise per batch

**Files:** `internal/native/batch.go`, `filter.go` (`batchColumn`, computed columns), `aggregate.go` (`addDistinct`'s per-dictionary hashes). Add a per-batch memo, created per batch, so each computed column and each id-dictionary hash vector is built once per batch whatever reads it. The memo's bytes are charged to memory.

**Tests:**
- Every native parity test passes.
- A unit test shows a computed column is built once for a filter plus a dimension.
- The benchmarks in `internal/native/bench_test.go` show no regression, and an improvement for a query that uses `device_label` in both its filter and its dimension.

- [ ] Commit: `perf(native): build a batch's computed columns and id hashes once`.

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

### Task 4: Reporting and the batch route

**Files:**
- `internal/reporting/data.go`: `WidgetDataMany(ctx, []WidgetRequest) []WidgetResult`. Each request goes through `WidgetData`'s checks; cache hits and misses are handled per widget. The misses' query-source loads go into one `AnswerMany` per project. SQL-source widgets fall back to their own loads.
- `query_source.go` exposes a batched load.
- `internal/api/ops_reporting.go` and `rest.go`: the `POST /api/widgets/data` route.
- `docs/twillingate.md` and the docs sync.

**Tests:**
- `TestWidgetDataManyEqualsWidgetData` runs over every built-in dashboard and preset on the system fixture.
- Per-widget errors (a widget id that is missing, archived, or in another project) fail only their own entry.
- Cache hits are served without a scan.
- Review Focus 2.
- REST route tests: auth, the 64-widget cap, ordering.

- [ ] Commit: `feat(api): load a dashboard's widgets in one request`.

### Task 5: The console's dataloader

**Files:**
- `web/src/lib/api.ts` (or `widget-query.ts`): the dataloader described in the Global Constraints.
- `WidgetCard.tsx` and `widget-query.ts`: query widgets become refreshable.
- Tests:
  - vitest: the calls of one tick become one POST per (project, from, to), each call gets its own result or error, and a refresh goes through it;
  - e2e: a dashboard load makes one data request. Assert this with Playwright's request log in an existing dashboard spec.

- [ ] Run `cd web && npm test && npm run e2e`, then `make check`. Commit: `perf(web): ask for a dashboard's widgets in one request`.

### Task 6: MCP tools on `AnswerMany`

**Files:**
- `internal/api/ops_read.go` (`views_overview`: views plus sessions).
- `ops_product.go` (`product_events`: two product queries).
- `ops_usage.go` (one project's queries).

Each runs its queries as one `AnswerMany`. Outputs are unchanged, and the existing parity tests against the old SQL pass.

- [ ] Commit: `perf(api): answer a tool's queries from one scan`.

### Task 7: Measure and document

Re-run the dashboard matrix from step 4b: native, 100k and 1M; Views and Product at 7d/30d/90d/1y; the rest at 30d. Run it through `WidgetDataMany`, so as the console loads now, behind the quiet-host gate. Report:
- wall time, before (step 4b) and after;
- peak RSS against step 4b;
- one row with today's open log in range.

Add the result to the spec as "Measurements (step 4c)" and to RESULTS.md. State plainly what was met and missed against DuckDB and against the targets.

- [ ] Commit: `docs: measure the dashboards with one scan per table`.
