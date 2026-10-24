# Native engine, step 3f-1: close the gaps the built-in widgets need — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the few generic features that a survey of all 59 data widgets found missing, so step 3f-2 can move every built-in dashboard onto structured queries without a visible regression.

**Architecture:** The additions are small and generic, mostly in `internal/native`, plus one in the widget source and one in a component:
- a `ratio` metric;
- `min`/`max` over `day`, `ts` and `received_at`;
- identity name and first-seen computed columns;
- trend zero-fill, several metrics as one trend's series, and an `Other` row that never holds a single group;
- the query source's `labels` value map;
- `stat` rendering an empty value as blank.

The survey is `.superpowers/native-3f-mapping.md` (git-ignored scratch); this plan copies what it needs.

**Tech Stack:** Go (`internal/native`, `internal/reporting`, `internal/api`), React/TypeScript (`web/`), `docs/twillingate.md`, `docs/reporting.md`.

**Spec:** `docs/superpowers/specs/2026-10-21-native-engine-design.md` (the order of work, step 3: "rewrite the system widgets"). The anti-goal "a per-dashboard special case in Go" means every addition here is generic.

## Global Constraints

- **`ratio` metric:** `{"op": "ratio", "num": <metric>, "den": <metric>}`.
  - Each side is a metric the table accepts, with its own optional `where`, and without `as`, `round`, `share` or nested `ratio`.
  - The value is `num / den` as a DOUBLE; it is empty when `den` is 0 or empty, or when `num` is empty.
  - `round` applies to the result. Default name: `ratio`.
  - An `Other` row computes both sides over the folded groups and then divides.
  - It is `approximate` when either side is.
  - It is valid in stat, trend and breakdown.
- **`min`/`max` column types:** they now also take `column: "day"` (the answer is a YYYY-MM-DD day, typed day) and `"ts"` / `"received_at"` (an RFC 3339 UTC timestamp, typed text). Empty over no rows. The output must equal DuckDB's `MIN(day)`/`MAX(received_at)` printed the way the engine prints days and timestamps elsewhere (check the existing formatting helpers).
- **Computed identity columns:** on `events`, `views` and `product`, joined from identities over all history, as `user_status` is today.
  - `user_label`: the identity's `name`, else the `user_id`; NULL when `user_id` is empty.
  - `group_label`: the same over `group_id`.
  - `user_first_seen` / `group_first_seen`: the identity's first-seen day, typed day; NULL when the id is empty or not in identities.

  Each is a function of the id, so a breakdown by (`user_id`, `user_label`) never splits a group. These are dimensions, series and filter columns; the `first_seen` ones also allow ordered ops like `day`.
- **Trend `fill: "zero"`:** every day of the range, times every kept series (or the single unsplit line), appears.
  - Count-like metrics (`count`, `actors`, `users`, `groups`, `bounces`, `weight`) read `0`; the others read empty.
  - The row cap still applies.
  - It is valid only on trend.
- **A trend over several metrics:** a trend without `series` may take 1 to 5 metrics. With more than one, the answer is long: `day`, `series` (each metric's name, so `as` gives the label), `value`. Rows are ordered by day, then by the metrics' order. Each metric gets a row for a day when that day has rows, or every day with `fill`. With one metric the shape is unchanged.
- **`Other` never folds exactly one group:** when exactly one group lies past `limit`, it is listed under its own name instead of `Other`. Then `limit: 4, other: true` gives at most 5 rows.
- **Widget source `labels`:** the query source content gains `"labels": {"<output column after rename>": {"<value>": "<label>"}}`.
  - It is applied after `rename` and before the component.
  - Values not listed pass through.
  - Validated at save: every column named must exist after rename.
  - Labels never change a column's type check, except that a day or number column cannot be labelled (refuse it).
- **`stat` component:**
  - An empty `value` renders as a blank figure (an en dash, "–"), never as 0.
  - An empty `previous` renders as no change line.
  - `0` still reads `0`.
  - The vitest suite and the widget gallery fixtures are updated.
- **Parity:** every native addition gets DuckDB parity cases in the existing harness, comparing against the SQL the system widgets use where one exists (`users/actions-per-user.sql`, `views/consent-share.sql`, `users/top-users.sql`, `groups/top-groups.sql`, `users/daily-active-users.sql`).
- **Validate, `Columns()`/`ColumnTypes()`, the MCP schema tags, `docs/twillingate.md` "Structured queries" (bound by `TestStructuredQueryDocument`) and `docs/reporting.md` (the source content and the `stat` behaviour) change in the same commit as the behaviour.** The web TS mirror in `web/src/lib/widget-draft.ts` follows `Columns()`/`ColumnTypes()` where it mirrors them.
- **Checks before each commit:** `make check` (`PATH=/usr/local/go/bin:$PATH GOTMPDIR=~/.cache/gotmp make check`) must pass; web tasks also run `cd web && npm test` and `npm run e2e`. Never use `/tmp`.

## Review Focus

1. **`ratio` with a filtered numerator in `Other` and with compare previous.** Both sides are folded before dividing. Pinned by Task 1.
2. **`user_label` for a user whose name changed.** It takes identities' combined name (the latest by `last_seen`), the same for every row of that user. Pinned by Task 3's parity with top-users.sql.
3. **`fill` over a series that has no rows on most days.** Its zeros appear for every day, but series are still chosen by their totals. Pinned by Task 4.
4. **A multi-metric trend whose metrics have different empty days.** Each metric's rows follow its own day rows, unless `fill` is set. Pinned by Task 4.
5. **`labels` on a column that `rename` produced.** It keys on the renamed name. Pinned by Task 5.

---

### Task 1: The `ratio` metric

**Files:** `internal/native/query.go` (Metric `Num`/`Den *Metric`; Validate paths `metrics[i].num.*`), `aggregate.go` and `engine.go` (compute after merge and fold), the docs and schema.
**Tests:** parity against `users/actions-per-user.sql` (count ÷ users, round 1), `groups/actions-per-group.sql`, and `views/consent-share.sql` (filtered daily_sum ÷ daily_sum). Also a breakdown with `Other`, compare previous, and Validate refusals (nested ratio, `as` inside a side).

- [ ] Tests first, run them and see them fail, implement, run `go test -race ./internal/native/ ./internal/api/`, then `make check`. Commit: `git commit -m "feat(native): divide one metric by another"`

### Task 2: `min`/`max` over days and timestamps

**Files:** `internal/native/query.go` (value columns per table now include `day`, `ts` and `received_at` for `min`/`max` only), `aggregate.go` (min and max over ints and times), `ColumnTypes()` (day, or text for timestamps), the docs and schema.
**Tests:** parity: a breakdown by `user_id` with `max day` and `min day`, a stat `max received_at`, and empty ranges.

- [ ] Tests first, then implement and run `make check`. Commit: `git commit -m "feat(native): take the first and last day or time"`

### Task 3: Identity labels and first-seen days

**Files:** `internal/native/identities.go` (labels and first days from the cached identity parts), `filter.go`/`aggregate.go` (computed columns), `query.go`, the docs and schema.
**Tests:** parity against `users/top-users.sql` and `groups/top-groups.sql`, expressed with Task 1 and Task 2 plus these columns:
- dims `user_id`, `user_label`, `user_first_seen`;
- metrics actions = `count`, events = `count` with where `family eq product`, views = `count` with where `family eq views`, active days = `daily` count of days, last seen = `max day`.

Use `distinct days`, if expressible, as `daily_sum` of a per-day constant; if not, add a `days` metric (the number of distinct days with rows) here and document it. Also cover a user without a name, a renamed user, and a filter `user_first_seen gte`.

- [ ] Tests first, then implement and run `make check`. Commit: `git commit -m "feat(native): name users and groups and tell when they were first seen"`

### Task 4: Trend fill, several metrics, and a one-group `Other`

**Files:** `internal/native/query.go` (`Fill string`, json `fill`, "" or "zero"; a trend's metrics count 1..5 without series; Validate), `engine.go` (trend output; breakdown `Other` rule), `Columns()`/`ColumnTypes()` (long form: day, series, value), the docs, the schema and the TS mirror.
**Tests:** parity against `users/daily-active-users.sql` (new and returning with fill zero) and `views/visitors-and-views.sql` (two metrics as series), plus `browsers.sql`-style top 4 + Other with exactly 5 and exactly 6 groups.

- [ ] Tests first, then implement and run `make check`. Commit: `git commit -m "feat(native): fill a trend's empty days and draw several metrics as series"`

### Task 5: Source labels and a blank empty stat

**Files:** `internal/reporting/query_source.go` (`Labels map[string]map[string]string`; validate; apply after rename), `docs/reporting.md`, `web/src/components/widgets/stat.tsx` (empty `value` renders "–", empty `previous` gives no change line) and its tests, plus gallery or fixtures if they cover stat.
**Tests:**
- Go: `TestQuerySourceLabels` (relabels `new` → `New users`, leaves other values alone, refuses an unknown column, refuses a day column).
- vitest: a stat with `""` shows "–"; a stat with `"0"` shows 0; a stat with an empty previous shows no change line.
- e2e: unchanged, but must pass.

- [ ] Tests first, then implement. Run `cd web && npm test && npm run e2e`, then `make check`. Commit: `git commit -m "feat(reporting): relabel query values and leave an empty stat blank"`
