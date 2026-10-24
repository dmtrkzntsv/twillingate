# Native engine, step 1: measure — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Settle, by measurement, the Parquet library, the row-group size, and whether native uncached scans meet the spec's targets at 100k and 1M events/month, before any engine code enters the server.

**Architecture:** A scratch nested Go module `scripts/native-bench` (like `scripts/archive-bench`: its own `go.mod`, so the candidates never touch the main module or CI). It rewrites generated month files with each candidate library, runs three representative built-in queries natively over a library-neutral batch interface, checks every answer against DuckDB on the same files, and prints wall time, CPU and peak RSS per run, each run in a fresh process. Today's DuckDB numbers come from the existing `TestMeasureDashboards`.

**Tech Stack:** Go 1.26; candidates `github.com/apache/arrow-go/v18/parquet` (with `pqarrow`) and `github.com/parquet-go/parquet-go`; `github.com/duckdb/duckdb-go/v2` as the oracle only.

**Spec:** `docs/superpowers/specs/2026-10-21-native-engine-design.md` (order of work, step 1; "The engine"; "The daily pass without DuckDB"; Targets).

## Global Constraints

- Nothing is added to the main module's `go.mod`; the bench is module `github.com/dmtrkzntsv/twillingate/scripts/native-bench` with its own `go.mod`/`go.sum`.
- Workload envelope: **100,000 and 1,000,000 events per month**, 12 months, one project. Both ends are measured for every row.
- Targets to judge against (copied from the spec): idle RSS ~20 MB; peak during a dashboard load under ~150 MB; built-in dashboard, 30-day range, no cache: under 0.5 s total at 1M events/month, tens of ms at 100k; 1-year range: under 1 s with a warm cache, under ~3 s cold, at 1M events/month.
- Every native run uses `GOMAXPROCS=2` (the DuckDB baselines ran at 2 threads).
- Generated data and outputs live under `~/.cache/twbench-native/` — never `/tmp` (its quota fills). Use `GOTMPDIR=~/.cache/gotmp`. Go is at `/usr/local/go/bin/go`.
- Writing our own Parquet codec is out of scope. Approximate distinct counts are not measured here: every native answer must equal DuckDB's exactly.
- The bench is scratch: its doc comment says to delete it once the spec's Measurements section is filled in.

## Review Focus

1. **A row group that straddles the range edge**: pruning by statistics may keep it, and rows outside `[from, to]` inside it must still be filtered. Pinned by Task 3's `TestRangeEdgeRowsFiltered`.
2. **Different dictionaries per row group and per file**: index 3 in one batch is not index 3 in the next; aggregating by raw index silently merges different countries. Pinned by Task 2's `TestDictRemapAcrossBatches`.
3. **Empty and null values**: `country = ''` is excluded from the countries query; a null `attr_*` is not a value in attribute-values. Pinned by Task 2's `TestCountriesExcludesEmpty` and `TestAttributeValuesSkipsNull`.
4. **Schemas that differ between files**: an `attr_*` column present in one month file and absent in another (DuckDB's `union_by_name`); the missing column reads as null, not as an error. Pinned by Task 3's `TestMissingColumnReadsNull`.
5. **High-cardinality attributes** (`attr_item_id` has 20,000 values): attribute-values must report its peak RSS at 1M/month over a year rather than abort; the run records the number either way. Pinned by Task 5's matrix, which includes `attrvalues` at 365 days for both volumes.

---

### Task 1: Today's baseline and the generated data

**Files:**
- Create: `scripts/native-bench/RESULTS.md` (scratch; the numbers move to the spec in Task 6)

**Interfaces:**
- Produces: generated month files at `~/.cache/twbench-native/100k/raw/1/YYYY-MM.parquet` and `~/.cache/twbench-native/1m/raw/1/YYYY-MM.parquet` (12 months ending 2026-09), plus their derived files, which later tasks read.

- [ ] **Step 1: Generate and measure at 1M/month**

Run:
```bash
MEASURE_DIR=~/.cache/twbench-native/1m MEASURE_MONTH_EVENTS=1000000 MEASURE_RUNS=1:30,1:365,2:30,2:365 \
  GOTMPDIR=~/.cache/gotmp /usr/local/go/bin/go test -tags measure -run TestMeasureDashboards -v -timeout 2h ./internal/app
```
Expected: PASS, with per-widget timings and the child's peak RSS logged for Views (1) and Product (2) over 30 and 365 days.

- [ ] **Step 2: The same at 100k/month**

Run step 1's command with `MEASURE_DIR=~/.cache/twbench-native/100k MEASURE_MONTH_EVENTS=100000`.
Expected: PASS.

- [ ] **Step 3: Record the baseline**

In `RESULTS.md`, write one table: rows = dashboard × range × volume; columns = total wall, slowest widget, child peak RSS. Also record the month files' row-group count and rows per group (`SELECT row_group_id, max(row_group_num_rows) … FROM parquet_metadata('…')` in DuckDB) for one month at each volume.

- [ ] **Step 4: Commit**

```bash
git add scripts/native-bench/RESULTS.md
git commit -m "test: record the DuckDB dashboard baseline at 100k and 1M events a month"
```

### Task 2: The batch interface and the three queries

**Files:**
- Create: `scripts/native-bench/go.mod`, `scripts/native-bench/main.go` (doc comment with usage, subcommand switch; scratch notice)
- Create: `scripts/native-bench/batch.go`, `scripts/native-bench/queries.go`
- Test: `scripts/native-bench/queries_test.go`

**Interfaces:**
- Produces:
  ```go
  // Column is one column of a batch. Strings arrive dictionary-encoded:
  // Dict holds this batch's distinct values, Idx one entry per row (-1 = null).
  // Ints hold DATE (days since 1970-01-01) and integer columns.
  type Column struct {
      Dict []string
      Idx  []int32
      Ints []int64
      Null []bool // for Ints; nil when no nulls
  }
  type Batch struct {
      N    int
      Cols map[string]*Column
  }
  // Source streams batches holding the named columns of rows with
  // family == family and day in [from, to] (days since epoch, inclusive).
  type Source interface {
      Name() string
      Scan(files []string, cols []string, family string, from, to int64, fn func(*Batch) error) error
  }
  type Row = []string // query results, values formatted as DuckDB prints them
  func Countries(src Source, files []string, from, to int64) ([]Row, error)     // label, value; top 20
  func DAU(src Source, files []string, from, to int64) ([]Row, error)           // x (YYYY-MM-DD), y
  func AttributeValues(src Source, files []string, from, to int64) ([]Row, error) // Attribute, Value, Count, Users, Groups
  ```
- The SQL each query must equal (oracle text in the tests): `internal/reporting/system/views/countries.sql`, `internal/reporting/system/product/daily-active-users.sql`, `internal/reporting/system/product/attribute-values.sql`, with `views`/`product` as `read_parquet(files, union_by_name=true) WHERE family = …`.

- [ ] **Step 1: Write the failing tests**

In `queries_test.go`, a `memSource` (implements `Source` over hand-built batches) feeds:
- `TestCountriesSumsDailyDistinct`: actor `a` in `DE` on two days, `b` in `DE` on one day, `c` in `US` twice on one day → rows `{"DE","3"}, {"US","1"}`.
- `TestCountriesExcludesEmpty`: a row with country `''` is not a label.
- `TestCountriesTop20Order`: 25 countries → 20 rows, ordered by value desc then label asc.
- `TestDictRemapAcrossBatches`: batch 1 `Dict{"DE","US"}`, batch 2 `Dict{"US","DE"}`, one actor each → `DE` and `US` each 1, not 2 and 0.
- `TestDAUPerDay`: two days → `{"2026-09-01","2"}, {"2026-09-02","1"}`, ordered by day.
- `TestAttributeValuesSkipsNull`: `attr_plan` null on one row → no row with an empty Value for it; Count counts events, Users distinct actors, Groups distinct non-empty `group_id`.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd scripts/native-bench && /usr/local/go/bin/go test ./...`
Expected: FAIL (undefined `Countries`, `DAU`, `AttributeValues`).

- [ ] **Step 3: Implement the queries in `queries.go`**

Approach, the part the tests do not dictate: per batch, map each `Dict` entry to a global id once (`remap[i] = intern(Dict[i])`), then aggregate on global ids. Countries: a set of `(day, countryID, actorID)`, then sums per country. DAU: a set of `(day, actorID)`. AttributeValues: per `(key, valueID)` an event count plus sets of actor and group ids. Exact sets (Go maps), no sketches.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd scripts/native-bench && /usr/local/go/bin/go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add scripts/native-bench
git commit -m "test: native-bench queries over a library-neutral batch"
```

### Task 3: The two Parquet sources

**Files:**
- Create: `scripts/native-bench/src_arrow.go` (arrow-go `parquet/file` + `pqarrow`, dictionary reads on), `scripts/native-bench/src_pqgo.go` (parquet-go)
- Test: `scripts/native-bench/source_test.go`

**Interfaces:**
- Consumes: `Source`, `Batch`, `Column`, the three queries (Task 2).
- Produces: `func NewArrowSource() Source`, `func NewPQGoSource() Source`, and `func OpenedRowGroups(src Source) int` (a counter each source increments when it reads a row group, for the pruning test).

- [ ] **Step 1: Write the failing tests**

The test writes fixtures with DuckDB (`COPY (…) TO '…' (FORMAT parquet, ROW_GROUP_SIZE 1000)`) into `t.TempDir()` — two small month files of hand-chosen rows, the second without `attr_variant` — and, for each source in `[]Source{NewArrowSource(), NewPQGoSource()}`:
- `TestQueriesMatchDuckDB`: each of the three queries equals the oracle SQL run by DuckDB over the same files, row for row.
- `TestRangeEdgeRowsFiltered`: a row group holding days 1–3; range day 2..2 → only day 2's rows count.
- `TestMissingColumnReadsNull`: `attr_variant` absent in the second file → AttributeValues still matches DuckDB.
- `TestPrunesRowGroupsByDay`: a file of 30 row groups, one day each; a one-day range → `OpenedRowGroups` ≤ 2.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd scripts/native-bench && /usr/local/go/bin/go test -run 'TestQueriesMatchDuckDB|TestRangeEdge|TestMissingColumn|TestPrunes' ./...`
Expected: FAIL (undefined sources).

- [ ] **Step 3: Implement both sources**

Each prunes row groups by the `day` and `family` column statistics before reading any page, reads only `cols`, and fills `Column.Dict`/`Idx` from dictionary pages where the page is dictionary-encoded (decoding plain pages into a per-batch dictionary otherwise). A column missing from a file is all-null in that file's batches.

- [ ] **Step 4: Run the tests to verify they pass**

Expected: PASS for both sources.

- [ ] **Step 5: Commit**

```bash
git add scripts/native-bench
git commit -m "test: native-bench Parquet sources for arrow-go and parquet-go"
```

### Task 4: Rewrite month files with each library

**Files:**
- Create: `scripts/native-bench/rewrite.go`
- Test: `scripts/native-bench/rewrite_test.go`

**Interfaces:**
- Produces: `func Rewrite(lib string, in, out string, rowsPerGroup int) (WriteStats, error)` with `lib` ∈ `"arrow"`, `"pqgo"`; `type WriteStats struct{ Wall time.Duration; Bytes int64; RowGroups int }`. Subcommand: `native-bench rewrite -lib arrow|pqgo -rows N <in> <out>`.

- [ ] **Step 1: Write the failing test**

`TestRewriteKeepsRows`: for each lib, rewrite a DuckDB-written fixture at `rowsPerGroup = 1000`; DuckDB reads `out` back and `SELECT count(*), sum(hash(id)), min(ts), max(ts) …` equals the same over `in`; `parquet_metadata(out)` shows every group ≤ 1000 rows, zstd compression, and `country` dictionary-encoded; row order (family, ts) is preserved.

- [ ] **Step 2: Run it to verify it fails**

Expected: FAIL (undefined `Rewrite`).

- [ ] **Step 3: Implement `Rewrite`**

Stream row group by row group from `in` to `out` (written aside as `out.tmp`, then renamed); never hold the whole file. zstd, dictionary encoding for string columns, the input's schema unchanged.

- [ ] **Step 4: Run it to verify it passes**

Expected: PASS for both libraries.

- [ ] **Step 5: Commit**

```bash
git add scripts/native-bench
git commit -m "test: native-bench rewrites month files with each library"
```

### Task 5: The measurement matrix

**Files:**
- Create: `scripts/native-bench/run.go`, `scripts/native-bench/rss.go`
- Modify: `scripts/native-bench/main.go` (subcommands `rewrite`, `query`, `matrix`)

**Interfaces:**
- Consumes: the sources (Task 3), `Rewrite` (Task 4), the queries (Task 2).
- Produces: `native-bench query -src arrow|pqgo -q countries|dau|attrvalues -from YYYY-MM-DD -to YYYY-MM-DD <files…>`: runs one query once and prints one JSON line `{"wall_ms","user_ms","sys_ms","peak_rss_kb","rows"}` (peak from `/proc/self/status` `VmHWM`, CPU from `syscall.Getrusage`). `native-bench matrix <root>`: re-execs itself per cell, so each cell starts with a fresh process.

- [ ] **Step 1: Write the failing test**

`TestQueryCommandPrintsOneJSONLine`: runs the `query` subcommand on Task 3's fixture; the output parses as the JSON object above with `rows > 0` and `peak_rss_kb > 0`.

- [ ] **Step 2: Run it to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Implement `query` and `matrix`**

The matrix, for volume ∈ {100k, 1m}:
1. Rewrites the 12 month files with each lib at three group sizes: rows of ~1 day (`PerMonth/30`), ~4 days (`PerMonth*4/30`), and DuckDB's own files as they are (`-lib none`). Records `WriteStats` plus the writer process's peak RSS.
2. Runs each query × source × layout × range ∈ {30 days, 365 days, both ending 2026-09-30} three times, keeping the first run and the best of the other two (cold-ish, warm).
3. Appends one Markdown table per volume to `RESULTS.md`.
4. Builds two minimal binaries that import only one library's reader each and records their size.

- [ ] **Step 4: Run it to verify it passes, then run the matrix**

Run: `cd scripts/native-bench && /usr/local/go/bin/go test ./... && GOMAXPROCS=2 /usr/local/go/bin/go run . matrix ~/.cache/twbench-native`
Expected: tests PASS; `RESULTS.md` holds both volumes' tables with no failed cells. A failed cell (out of memory, error) is recorded as such, not dropped.

- [ ] **Step 5: Commit**

```bash
git add scripts/native-bench
git commit -m "test: measure native scans and writes at 100k and 1M events a month"
```

### Task 6: Decide and record

**Files:**
- Modify: `docs/superpowers/specs/2026-10-21-native-engine-design.md` (add a "Measurements (step 1)" section before "Left to the builder"; edit "Left to the builder")

- [ ] **Step 1: Write the Measurements section**

From `RESULTS.md`: the DuckDB baseline table; per volume, the native table (query × range × source × layout: wall, CPU, peak RSS); write speed, writer peak RSS and file size per library; binary size per library.

- [ ] **Step 2: Record the decisions**

In the same section, one line each, with the number that decided it:
- The Parquet library.
- The row-group size.
- Whether the 30-day targets hold uncached at both ends of the envelope.
- Whether the 1-year target is met without a cache. The default is no cache: step 5 of the order of work happens only if the 1-year target is missed and the numbers suggest a cache would make that dashboard several times faster.

Remove the settled items from "Left to the builder".

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/specs/2026-10-21-native-engine-design.md
git commit -m "docs: record the native engine's step 1 measurements and decisions"
```
