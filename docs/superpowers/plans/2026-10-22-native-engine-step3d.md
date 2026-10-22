# Native engine, step 3d: retention — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `retention` query kind that answers exactly what the built-in retention dashboard asks: the cohort grid, the pooled curve per identity kind, and its milestones. It equals the system widgets' DuckDB SQL.

**Architecture:** `internal/native` gains:
- **A retention scan.** It reads the events of actors a cohort can hold (`actor_kind` user or install, non-empty `actor_id`, families views and product, no rules beyond the event rules the `events` table applies) over **all history** up to `to + offsets` and before today. It keeps one record per actor: the cohort day, the first day seen as a user, and the set of active days within the window.
- **A new kind, `retention`,** with three outputs: `grid`, `curve`, `milestones`.

"Today" comes from the engine's clock (`Options.Now`, default `time.Now` in UTC), so tests are deterministic.

**Tech Stack:** Go (`internal/native`, `internal/api`), `docs/twillingate.md`.

**Spec:** `docs/superpowers/specs/2026-10-21-native-engine-design.md` (kinds table: `retention` — cohort = first active day; activity filter; offsets 0..N; user vs install split as today; milestones). The SQL to match is `internal/reporting/system/retention/cohorts.sql`, `install-retention.sql`, `install-milestones.sql`, `signed-in-retention.sql` and `signed-in-milestones.sql`.

## Global Constraints

- **Exact semantics.** For each actor with `actor_kind IN (user, install)` and `actor_id <> ''`, over rows of families views and product with `day < today` and `day <= to + offsets`:
  - **Cohort and user days:** `cohort_day` = MIN(day). `user_day` = MIN(day) where `actor_kind = 'user'`. The `acts` set is distinct (actor_id, actor_kind, day).
  - **Return days:** a return on day `d ≥ cohort_day` counts for kind `user` when `user_day ≤ d`, else for `install`. Distinct actors are counted per (kind, cohort_day, offset = d − cohort_day), for cohorts with `cohort_day ∈ [from, to]`.
  - **Cohort size and retention:** the cohort size for (kind, cohort_day) is its offset-0 count. Retention = returned / size, or 0 when size is 0.
  - **Curve for one kind:** for offset o in 0..offsets:
    - the denominator is the sum of the cohort sizes (offset 0) of that kind's cohorts with `cohort_day + o < today`;
    - the numerator is the sum of returned at offset o over that kind's cohorts with `cohort_day + o < today`;
    - retention is the numerator over the denominator, 0 when the denominator is 0;
    - every offset with a denominator row appears.
  - **Milestones:** the curve's retention at the requested offsets, one column each, empty when the curve lacks that offset.
- **The query:**

  ```json
  {"kind": "retention",
   "output": "grid" | "curve" | "milestones",
   "identified_by": "install" | "user",
   "offsets": 45,
   "milestones": [7, 30, 45],
   "where": [...]}
  ```

  - `identified_by` is required for `curve` and `milestones`, and refused for `grid`.
  - `offsets`: 1..90, default 45.
  - `milestones`: each in 0..offsets, distinct, at most 6.
  - `where`: optional extra filters on `events` columns, applied with the rest.
  - `table` must be absent or `events`. Metrics, dimensions and other fields are refused.
- **Output columns:**
  - `grid`: `cohort` (day), `identified_by` (text), `day` (number, the offset), `size` (number), `returned` (number), `retention` (number). Rows are ordered by cohort desc, identified_by asc, day asc, for offsets 0..offsets.
  - `curve`: `day` (number, the offset) and `retention` (number), ordered by day.
  - `milestones`: one column per milestone, named `d<n>` (number).

  Values are formatted as DuckDB prints them. Retention is a DOUBLE, compared with tolerance in parity.
- **Memory:** the per-actor state over all history is charged to `MaxBytes`. Past the budget the query is refused with `ErrTooLarge`. Retention is a single-group answer, like a stat.
- `ColumnTypes()`, Validate, the MCP schema, `docs/twillingate.md` "Structured queries" (bound by `TestStructuredQueryDocument`) and the 3a query widget source (`rename` maps these columns onto components; the editor templates mirror `ColumnTypes`) change in the same commits.
- `make check` must pass before each commit: `PATH=/usr/local/go/bin:$PATH GOTMPDIR=~/.cache/gotmp make check`. Never use `/tmp`. Never mention prices, tiers, plans, margins, customers, cloud or private services.

## Review Focus

1. **An actor first seen before `from`.** They are not in any cohort of the range, even when active in it: `firsts` is over all history. Pinned by Task 2's fixture.
2. **An install that signs in on day 3.** Days 0–2 count for `install` and day 3 onward for `user`. Its install cohort size is offset 0's, so it never appears in the user cohort's size unless `user_day == cohort_day`. Pinned by Task 2.
3. **Today's boundary.** Days ≥ today are excluded. The curve's `cohort_day + o < today` clip decides which cohorts count per offset. Pinned by tests with a fixed `Now`.
4. **`to + offsets` beyond today.** It is clipped by `day < today`. Pinned by Task 2.
5. **Empty range.** The grid has no rows; the curve has no rows; milestones return one row of empties. This matches the SQL. Pinned by Task 2.

---

### Task 1: The retention scan

**Files:**
- Create: `internal/native/retention.go` (the scan and the per-actor state)
- Modify: `internal/native/engine.go` (`Options.Now func() time.Time`, default `time.Now`; the file selection for a retention query is all of the project's Parquet and logs up to min(today−1, to+offsets), not just the range)
- Test: `internal/native/retention_test.go`. `TestRetentionStateMatchesDuckDB` compares the per-actor (cohort_day, user_day, active days) against DuckDB's `acts`/`firsts` CTEs on a fixture spanning three months, with installs that sign in later, users, anonymous actors (ignored), and actors first seen before the range.

- [ ] Tests first, run and see them fail, implement, run `go test -race ./internal/native/`, then `make check`. Commit: `git commit -m "feat(native): gather retention cohorts from every day of history"`

### Task 2: The `retention` kind

**Files:**
- Modify: `internal/native/query.go` (kind `retention` with the fields above; Validate; `Columns()`; `ColumnTypes()`), `internal/native/engine.go` (outputs `grid`, `curve`, `milestones`), the MCP schema tags, `docs/twillingate.md`, and `web/src/lib/widget-draft.ts` if its TS mirror enumerates kinds (keep its `Columns()`/types mirror in step; add a template only if a component maps naturally — no new templates required).
- Test: parity cases that run the five retention SQL files as written (with `current_date` replaced by the fixture's fixed today in the SQL text, and `:from`/`:to`/`:project` bound) against the matching native queries. Cover the Review Focus cases and an empty range.

- [ ] Tests first, then implement and run `make check`. Commit: `git commit -m "feat(native): answer retention grids, curves and milestones"`
