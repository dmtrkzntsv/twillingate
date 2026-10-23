# Native engine, step 2: the engine and stat/trend/breakdown — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a native Go engine to the server that answers structured `stat`, `trend` and `breakdown` queries over the event files, and expose it through the MCP/REST `query` tool. DuckDB stays in place for SQL.

**Architecture:** A new rank-0 package, `internal/native`, reads the project's event files directly:
- month, day and late-segment Parquet through `parquet-go`, plus the open NDJSON logs through a Go decoder;
- it applies the event and attribute rules in Go;
- it runs the three kinds over row groups in parallel, with per-worker partials on sorted `uint64` keys. This is the step 1b design, ported from `scripts/native-bench`.

A small leaf, `internal/shared/hll`, holds the distinct-count sketch. The `query` tool takes either `sql` (DuckDB, as today) or a structured `query` object (native). Every native answer is checked against DuckDB on the same files in tests.

**Tech Stack:** Go (main module, `go 1.25`); `github.com/parquet-go/parquet-go` v0.32.0 (its `go 1.24.9` directive fits); the existing `internal/archive`, `internal/store`, `internal/api`, `internal/app`.

**Spec:** `docs/superpowers/specs/2026-10-21-native-engine-design.md`. See "The structured query language", "The engine", "Measurements (step 1b)", "What changes in the repo" and the order of work, step 2.

## Global Constraints

- `internal/native` and `internal/shared/hll` are rank 0 in `internal/archtest/archtest_test.go`. Neither imports `internal/manage`; rules arrive as `archive.Rules` (`store.EventRule`, `store.AttributeRule`).
- `parquet-go` is the only new module dependency. The spec chose it on measurement, and its maintainers are reputable. Nothing else goes into `go.mod`.
- **Tables in step 2:** `events`, `views`, `product`, `measures`, with exactly the columns `archive.Reader` gives them (reader.go). `measures.bucket` = `ceil(ln(value)/ln(1.04))` for `value > 1e-17`, otherwise `-1000`. `sessions`, `identities` and `aliases` come in step 3; a query naming them is refused.
- **Files:** a query reads only its project's files whose day or month overlaps the range. That is the month files, the day files, the late segments (`<day>.<n>.parquet`) and the open logs (`<day>.log`, `<day>.<n>.closed`, and claimed `.dumping` logs whose target is missing), exactly the set `archive.ListFiles` returns.
- **Log rows:** keep the first line per `id` (as `logSelect` does). Lines that do not parse are skipped, as `ignore_errors=true` does.
- **Rules:** same semantics as `eventsView` (reader.go:447-554).
  - Event rules: rename, or drop when blocked or renamed onto a blocked name.
  - Attribute rules apply only to `product` and `measures`. A renamed or blocked key reads NULL. A rename target reads `coalesce(own value, sources in sortRules order)`. Keys match case-insensitively.
- **Exactness:** every exact answer equals DuckDB's answer, from `archive.Reader` over the same files, row for row, in the tests. Distinct counts over a whole range are exact while a set holds at most `hll.ExactLimit` (1024) ids; above that a sketch is used, and the column is marked approximate. Approximate values are within 2% of exact, or within 1 below 50.
- **Workers and memory:**
  - Each query uses 2 workers (`Options.Workers`, default 2).
  - At most 2 queries scan at once per process (`Options.MaxScans`, default 2). Further queries wait, or give up when their context is cancelled.
  - A breakdown tracks at most `Options.MaxGroups` (default 100,000) distinct keys. Past that it trims and sets `truncated`.
- **Refusals:** `native.ErrInvalid`, matched with `errors.Is`. `internal/api` maps it to `invalidf` (400 over REST). Messages name the field.
- **Docs:** the `query` row and the `POST /api/query` row in `docs/twillingate.md` change in the same commit as the tool (CLAUDE.md's table). The structured language gets a section in `docs/twillingate.md` next to "Writing SQL against the event files".
- Run `make check` before each commit that touches Go outside `scripts/` (it builds the web UI, so it needs Node 22). Never use `/tmp`; use `GOTMPDIR=~/.cache/gotmp`. Go is at `/usr/local/go/bin/go`.

## Review Focus

1. **A log line and a Parquet row for the same `id`** (an event dumped while its log was still being read). The reader's DuckDB views do not dedupe across file kinds, so native must not either: both count, as in DuckDB. Pinned by Task 3's `TestLogAndParquetMatchDuckDB`.
2. **A rule renaming onto a key that exists only in some files.** The target column is missing in some files and present in others; coalesce must still work. Pinned by Task 4's `TestAttributeRenameAcrossFiles`.
3. **A range that starts or ends mid-month.** Month-file row groups straddle the edges; rows outside the range must not count. Pinned by Task 6's `TestKindsMatchDuckDB` (mid-month ranges in its table).
4. **Ties in ORDER BY.** A breakdown or top-N series with equal values must order deterministically by label, exactly like the SQL it is checked against. Pinned by Task 6's tie case.
5. **An agent sending a huge breakdown** (e.g. `actor_id` × `path` over a year). Memory must stay bounded and the result must say `truncated`. Pinned by Task 6's `TestBreakdownCapTruncates`.

---

### Task 1: The distinct-count sketch as a shared leaf

**Files:**
- Create: `internal/shared/hll/hll.go`, `internal/shared/hll/hll_test.go` (port `scripts/native-bench/hll.go` and its tests)
- Modify: `internal/archtest/archtest_test.go` (rank 0 entry)

**Interfaces:**
- Produces: `type Sketch struct{…}`; `const ExactLimit = 1024`; `func (s *Sketch) Add(fp uint32)`; `func (s *Sketch) Merge(o *Sketch)`; `func (s *Sketch) Count() uint64`; `func (s *Sketch) Exact() bool` (true while it still holds the exact set); `func Fingerprint(s string) uint32` (FNV-1a, never `0xFFFFFFFF`).

- [ ] **Step 1: Port the step 1b tests and add `TestExactReportsPhase`**

The step 1b tests are `TestApproxSmallSetsExact`, `TestApproxErrorBound`, `TestSketchMergeEqualsUnion` and the determinism test. Add `TestExactReportsPhase`: `Exact()` is true at 1,024 distinct ids and false at 1,025.

- [ ] **Step 2: Run them and verify they fail**

Run: `GOTMPDIR=~/.cache/gotmp /usr/local/go/bin/go test ./internal/shared/hll/`. Expected: FAIL (package missing).

- [ ] **Step 3: Port the implementation**

Use Ertl's estimator and 2^14 registers. The doc comment says the 2% bound is statistical (~2.5σ), and that FNV-1a collisions are about n²/2^33 pairs.

- [ ] **Step 4: Run the tests and `go test ./internal/archtest/`**

Expected: PASS.

- [ ] **Step 5: Commit**

`git commit -m "feat(shared): add a distinct-count sketch"`

### Task 2: Parquet row groups for one project and range

**Files:**
- Create: `internal/native/files.go` (which files a query reads), `internal/native/parquet.go` (row-group source), `internal/native/batch.go` (`Column`, `Batch`), `internal/native/scan.go` (pruning, `ScanParallel`)
- Test: `internal/native/parquet_test.go`, `internal/native/main_test.go` (`archivetest.Child()` in TestMain)
- Modify: `go.mod`/`go.sum` (`github.com/parquet-go/parquet-go v0.32.0`), `internal/archtest/archtest_test.go` (`"internal/native": 0`)

**Interfaces:**
- Produces:
  ```go
  // Batch, Column: as scripts/native-bench/batch.go (Dict/Idx for strings,
  // Ints+Null for integers and dates), plus Floats+Null for DOUBLE and
  // Times (unix microseconds)+Null for TIMESTAMP.
  type Range struct{ From, To civil.Date }
  // projectFiles returns the Parquet files and the logs of project p that may
  // hold rows in r, from archive.ListFiles(dir)[p] and the files' day/month names.
  func projectFiles(dir string, p int64, r Range) (parquet, logs []string, err error)
  type group struct{ file string; index int; rows int64; all bool }
  func parquetGroups(files []string, family string, r Range) ([]group, error)
  type groupReader struct{…} // per worker, keeps the last file open
  func (g *groupReader) read(gr group, cols []string, family string, r Range) (*Batch, error)
  func scanParallel(ctx context.Context, groups []group, workers int, read func(w int, g group) error) error
  ```
  `cols` may hold the pattern `attr_*`, which expands per file.

- [ ] **Step 1: Write the failing tests**

Use `archivetest.New` with events written via `Files.Write` across three days in two months, then `Files.Maintain(today)` so that past days become day or month Parquet:
- `TestProjectFilesByRange`: picks only overlapping files for a mid-month range.
- `TestParquetRowsMatchDuckDB`: for every column of `events` (including `ts`, `day`, `value`, `display_width`, `consent` and attributes), the rows read for project 1 and the full range equal `SELECT * FROM events WHERE project_id=1 ORDER BY id` through `Reader.Query`, compared as formatted strings.
- `TestParquetPrunesByDay`: a one-day range on a month file with many groups reads fewer groups than exist.

- [ ] **Step 2: Run them and verify they fail**

Run: `GOTMPDIR=~/.cache/gotmp /usr/local/go/bin/go test ./internal/native/`. Expected: FAIL.

- [ ] **Step 3: Implement**

Port from `scripts/native-bench/src_pqgo.go`, `scan.go` and `batch.go`. Keep pruning on row-group statistics; page-index pruning is out of scope here. Add DOUBLE and TIMESTAMP columns. `day` is DATE.

- [ ] **Step 4: Run the tests with `-race`, then `make check`**

Expected: PASS.

- [ ] **Step 5: Commit**

`git commit -m "feat(native): read event Parquet files by row group"`

### Task 3: The open logs in Go

**Files:**
- Create: `internal/native/logs.go`
- Test: `internal/native/logs_test.go`

**Interfaces:**
- Consumes: `Batch`, `Column` (Task 2).
- Produces: `func readLog(path string, cols []string, family string, r Range) (*Batch, error)`. It decodes each line's fields into the same columns and types the Parquet path yields, keeps the first line per `id`, skips lines that do not parse, and filters by family and range.

- [ ] **Step 1: Write the failing tests**

- `TestLogRowsMatchDuckDB`: events written with `Files.Write` and never maintained; native log rows equal the reader's `events` rows.
- `TestLogKeepsFirstLinePerID`: two lines with the same `id` give one row, the first.
- `TestLogSkipsBadLines`: a garbage line between valid ones is skipped.
- `TestLogAndParquetMatchDuckDB`: a project with a maintained past day plus today's log; the union equals DuckDB's `events`.

- [ ] **Step 2: Run and verify they fail.**

- [ ] **Step 3: Implement**

Decode with `encoding/json` into a `map[string]json.RawMessage` per line. Parse only the requested keys. `ts`/`received_at` use the layout `"2006-01-02T15:04:05Z"`; `day` is `YYYY-MM-DD`. `consent` is 0/1 or absent (null). `sample_rate` defaults per `line()` in archive/log.go. Missing attribute keys are null. Build a dictionary per batch for string columns.

- [ ] **Step 4: Run the tests with `-race`; `make check`**

Expected: PASS.

- [ ] **Step 5: Commit**

`git commit -m "feat(native): read the open event logs"`

### Task 4: Event and attribute rules in Go

**Files:**
- Create: `internal/native/rules.go`
- Test: `internal/native/rules_test.go`

**Interfaces:**
- Consumes: `archive.Rules` (`internal/archive/reader.go:40`), `Batch`.
- Produces: `type ruleSet struct{…}`; `func compileRules(p int64, rules archive.Rules) ruleSet`. `func (rs ruleSet) apply(b *Batch, family string) *Batch` renames `event_name` via the dictionary, drops blocked rows, and folds attribute columns: NULL for renamed or blocked sources, coalesce into targets, case-insensitive key matching, and the target column added when it is missing. `func (rs ruleSet) columns(cols []string) []string` gives the source columns a scan must also read to build the requested targets.

- [ ] **Step 1: Write the failing tests**

Each test checks native `events` rows with rules against DuckDB's `events` view built with the same `archive.Rules`:
- `TestEventRenameAndBlockMatchDuckDB`
- `TestAttributeRenameAndBlockMatchDuckDB`
- `TestAttributeRenameAcrossFiles`: the target key exists in one file only.
- `TestRulesIgnoreViewsForAttributes`

- [ ] **Step 2: Run and verify they fail.**

- [ ] **Step 3: Implement**

Port the semantics from `eventsView`, including `sortRules` order. The rename map is resolved once per batch dictionary, not per row.

- [ ] **Step 4: Run the tests with `-race`; `make check`**

Expected: PASS.

- [ ] **Step 5: Commit**

`git commit -m "feat(native): apply event and attribute rules"`

### Task 5: The query language

**Files:**
- Create: `internal/native/query.go` (types and validation)
- Test: `internal/native/query_test.go`

**Interfaces:**
- Produces:
  ```go
  var ErrInvalid = errors.New("native: invalid query")
  type Query struct {
      Kind       string    `json:"kind"`        // "stat" | "trend" | "breakdown"
      Table      string    `json:"table"`       // "events" | "views" | "product" | "measures"
      Metrics    []Metric  `json:"metrics"`     // stat, breakdown: ≥1; trend: exactly 1
      Where      []Filter  `json:"where,omitempty"`
      Dimensions []string  `json:"dimensions,omitempty"` // breakdown: 1–3
      Series     string    `json:"series,omitempty"`     // trend: optional split
      SeriesLimit int      `json:"series_limit,omitempty"` // trend: top N series over the range (default 5)
      Compare    string    `json:"compare,omitempty"`    // stat: "" | "previous"
      Limit      int       `json:"limit,omitempty"`      // breakdown: default 20, max 1000
      Other      bool      `json:"other,omitempty"`      // breakdown: fold the rest into "Other"
      Order      string    `json:"order,omitempty"`      // breakdown: metric name to sort by, desc (default: first metric)
  }
  type Metric struct {
      Op     string `json:"op"`     // count | actors | users | groups | sum | avg | min | max | daily_sum | daily_max | daily_avg
      Column string `json:"column,omitempty"` // sum/avg/min/max: value | sample_rate | display_width | display_height
      Of     string `json:"of,omitempty"`     // daily_*: actors | users | groups
      As     string `json:"as,omitempty"`     // output column name; default derived from op
  }
  type Filter struct {
      Column string   `json:"column"` // a table column or attr_<key>
      Op     string   `json:"op"`     // eq | ne | in | not_in | contains | prefix | gt | gte | lt | lte | is_set
      Values []string `json:"values,omitempty"`
  }
  func (q Query) Validate() error // errors wrap ErrInvalid and name the field
  ```
  Dimensions and filter columns: any column of the table except `id`, `ts`, `received_at`, `value`, `sample_rate`; any `attr_<key>`; `day`. Computed dimensions (`resolution`, `device_label`, `platform_version`, `consent_state`) and `first_seen_day` come in step 3.

- [ ] **Step 1: Write the failing tests**

`TestValidate` is a table of valid and invalid queries, each invalid one asserting `errors.Is(err, ErrInvalid)` and a message naming the field. Cases:
- an unknown kind;
- a breakdown with 0 or 4 dimensions;
- a trend with 2 metrics;
- `sum` with no column;
- `daily_max` with no `of`;
- an unknown operator;
- `in` with no values;
- `limit` 1001;
- table `sessions`, refused for now;
- a dimension `id`.

- [ ] **Step 2: Run and verify they fail.**

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run; commit**

`git commit -m "feat(native): define and validate structured queries"`

### Task 6: Running stat, trend and breakdown

**Files:**
- Create: `internal/native/engine.go` (`Engine`, `Options`, `Run`), `internal/native/aggregate.go` (per-worker partials and merges, ported from `scripts/native-bench/queries.go`)
- Test: `internal/native/engine_test.go`

**Interfaces:**
- Consumes: Tasks 1–5.
- Produces:
  ```go
  type Options struct{ Dir string; Workers, MaxScans, MaxGroups int }
  type Engine struct{…}
  func New(o Options) *Engine
  type Params struct{ Project int64; From, To civil.Date }
  type Result struct {
      Columns     []string   `json:"columns"`
      Rows        [][]string `json:"rows"`
      Truncated   bool       `json:"truncated"`
      Approximate []string   `json:"approximate,omitempty"` // names of columns holding any sketch value
  }
  func (e *Engine) Run(ctx context.Context, q Query, p Params, rules archive.Rules) (Result, error)
  ```

**Output shapes:**

| Kind | Columns |
|---|---|
| `stat` | one row: one column per metric; with `compare: "previous"`, a `previous_<name>` column per metric over the preceding range of equal length |
| `trend` | `day`, optionally `series`, then the metric; rows by `day`, then `series`; series beyond `series_limit` (ranked by the metric over the whole range, ties by label) are left out |
| `breakdown` | one column per dimension, then one per metric; sorted by the order metric desc, then dimensions asc; `limit` rows, plus an `Other` row (dimensions `Other`) holding the rest when `other` |

Values are formatted as DuckDB prints them: integers in decimal, floats via `strconv.FormatFloat(v, 'f', -1, 64)`, days as `YYYY-MM-DD`. A NULL dimension value is an empty string.

- [ ] **Step 1: Write the failing tests**

`TestKindsMatchDuckDB` builds a fixture with `archivetest`: 2 projects; 40 days across 2 months (some maintained into day and month files, today in the log); views, product events with attributes, and measures; null actors and empty countries; and rules for project 1. For each case it runs `Engine.Run` and the equivalent hand-written SQL through `Reader.Query` (with the same rules), and the rows must be equal. Cases:

- **Stat:** `count`, `actors`, `users`, `groups`, `sum(value)`, `avg(value)`, `daily_avg(actors)` on views; with `compare: previous`.
- **Trend:** `count` by day; `actors` by day with series `country` and `series_limit` 2, including a tie.
- **Breakdown:**
  - `path` by `actors` + `count`, limit 3 with `other`;
  - `event_name` × `attr_plan` on product;
  - a filter `in` and `prefix`;
  - a mid-month range.
- **Measures:** `breakdown` of `event_name` by `avg(value)`.
- **Rules:** a rule-renamed event name.

Also:
- `TestDistinctTurnsApproximate`: a stat `actors` over more than 1,024 distinct actors (generated) is within 2% of exact and lists the column in `Approximate`. Up to 1,024 it is exact and not listed.
- `TestBreakdownCapTruncates`: with `MaxGroups` 10 and 50 distinct paths, the result is truncated and memory stays bounded (only that `truncated` is set and the rows are the top by metric among the tracked keys).
- `TestMaxScansWaits`: with `MaxScans` 1, a second Run blocks until the first finishes, or returns the context's error when cancelled.

- [ ] **Step 2: Run and verify they fail.**

- [ ] **Step 3: Implement**

Per-worker partials keyed by sorted `uint64` keys and dense arrays, as in step 1b. Whole-range distinct counts use per-group `hll.Sketch`es fed `hll.Fingerprint` of the id string. Per-day distinct counts stay exact (sorted keys). `daily_*` metrics are the per-day exact distinct counts, then sum, max or avg over the range's days that have rows, matching `SUM`/`MAX`/`AVG` over SQL's per-day groups.

- [ ] **Step 4: Run with `-race`; `make check`**

Expected: PASS.

- [ ] **Step 5: Commit**

`git commit -m "feat(native): run stat, trend and breakdown queries"`

### Task 7: Structured queries over the `query` tool

**Files:**
- Modify: `internal/api/ops_query.go` (input gains `project_id`, `from`, `to`, `query`; exactly one of `sql` and `query`), `internal/api/server.go` (`Build`/`NewHandler` take a `Native` interface), `internal/app/app.go` (construct `native.New` with `Dir: cfg.EventsDir`), `docs/twillingate.md` (the `query` tool row, the `POST /api/query` row, a new section "Structured queries" after "Writing SQL against the event files")
- Test: `internal/api/ops_query_test.go`

**Interfaces:**
- Consumes: `native.Engine.Run`, `native.ErrInvalid`.
- Produces:
  ```go
  // in internal/api
  type Native interface {
      Run(ctx context.Context, q native.Query, p native.Params, rules archive.Rules) (native.Result, error)
  }
  type queryIn struct {
      SQL       string        `json:"sql,omitempty"`
      ProjectID int64         `json:"project_id,omitempty"`
      From      string        `json:"from,omitempty"`  // YYYY-MM-DD; default: 30 days ending today
      To        string        `json:"to,omitempty"`
      Query     *native.Query `json:"query,omitempty"`
  }
  ```
  The output stays `tableOut` plus `Approximate []string` (`json:"approximate,omitempty"`). With `query`, `project_id` is required; the rules come from `reg.Snapshot(ctx).RuleLists()`, as `runFilesQuery` does. The same `queryTimeout` and `queryRows` apply: `Rows` is capped at `queryRows` and `Truncated` set.

- [ ] **Step 1: Write the failing tests**

- `TestQueryStructuredRuns`: a breakdown through the handler returns the native rows.
- `TestQueryRefusesBoth`: `sql` and `query` together give a 400 naming them.
- `TestQueryStructuredInvalid`: an invalid query gives a 400 whose message names the field.
- `TestQueryStructuredNeedsProject`.

Then run `go test ./internal/api/ -run 'TestDocument'` to see which docs-sync tests fail, before you update the docs.

- [ ] **Step 2: Run and verify they fail.**

- [ ] **Step 3: Implement and document**

The tool description says that `query` is the native structured language and lists the kinds, and that `sql` is DuckDB SQL until it moves out. The docs section gives the three kinds, the fields, the metrics, the operators and one example of each kind, plus the approximation rule. Keep the tool count unchanged.

- [ ] **Step 4: `make check`**

Expected: PASS, including `docs_sync_test.go` and `internal/sqlcheck`.

- [ ] **Step 5: Commit**

`git commit -m "feat(api): answer structured queries natively"`
