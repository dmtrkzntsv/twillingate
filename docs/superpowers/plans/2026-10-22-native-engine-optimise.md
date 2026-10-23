# Native engine, step 1b: optimise and re-measure — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Find out what an optimised native scan achieves at 100k and 1M events a month on a quiet host. Then settle the row-group size, confirm the library at 1M, and decide the cache.

**Architecture:** This work extends the scratch module `scripts/native-bench` (step 1, PR #206). It makes four changes:
- Sources gain a row-group API, so a query can scan row groups in parallel with one partial aggregate per worker.
- The queries switch from per-row Go maps to sorted `uint64` keys and dense arrays.
- Attribute values gains an approximate variant: distinct users from HyperLogLog, which the spec allows for whole-range distinct counts.
- The matrix gains day-pruning ranges, a smaller row group, and a quiet-host gate.

**Tech Stack:** Go 1.26; `github.com/parquet-go/parquet-go` (the chosen library); `github.com/apache/arrow-go/v18` (kept only for the 1M library re-check); `github.com/duckdb/duckdb-go/v2` as the oracle.

**Spec:** `docs/superpowers/specs/2026-10-21-native-engine-design.md` (order of work, step 1b; "Measurements (step 1)"; "The engine"; Targets).

## Global Constraints

- Nothing is added to the main module's `go.mod`; all work stays in `scripts/native-bench` (its own `go.mod`).
- Workload envelope: **100,000 and 1,000,000 events per month**, 12 months ending 2026-09, one project. The data is the same as step 1's: `~/.cache/twbench-native/{100k,1m}/raw/1/YYYY-MM.parquet` plus the rewritten layouts under `~/.cache/twbench-native/<volume>/<layout>/`.
- Targets (from the spec):
  - Peak during a dashboard load: under ~150 MB.
  - Built-in dashboard, 30-day range, no cache: under 0.5 s total at 1M events/month; tens of ms at 100k.
  - 1-year range: under 1 s with a warm cache, under ~3 s cold, at 1M events/month.
- Every native run uses `GOMAXPROCS=2`, and parallel scans use 2 workers.
- Exact queries must equal DuckDB's answers exactly. The approximate attribute-values query must keep each `Users` and `Groups` value within **2%** of the exact one, or within 1 where the exact value is under 50. `Count` stays exact.
- No cache in this step. The owner's rule is no cache unless it shows great results.
- Outputs go under `~/.cache/twbench-native/`, never `/tmp`. Use `GOTMPDIR=~/.cache/gotmp`. Go is at `/usr/local/go/bin/go`.

## Review Focus

1. **Data races between workers.** A shared interner, shared counters, or one batch reused across workers corrupts results nondeterministically. Pinned by Task 1's `TestScanParallelMatchesSerial` and by running the module's tests with `-race` in every task.
2. **Merge order changes output.** Results must not depend on which worker saw which row group, including ties in ORDER BY. Pinned by Task 2's `TestQueriesDeterministicAcrossWorkers`.
3. **Null actors after the key change.** The step 1 semantics hold: in Countries, a null-actor-only group has value 0; in AttributeValues, a null actor adds one user. Pinned by Task 2, which keeps Task 2/step 1's null-actor tests and runs them through the parallel path.
4. **A sketch that is too coarse at low cardinality.** Most attribute values have few users, and HLL error is worst there unless the sketch is sparse or exact below a threshold. Pinned by Task 3's `TestApproxSmallSetsExact`.
5. **A quiet-host gate that waits forever.** The matrix must give up after a bounded wait, record the load it ran at, and carry on. Pinned by Task 4's `TestWaitQuietGivesUp`.

---

### Task 1: Row groups as the unit of work

**Files:**
- Modify: `scripts/native-bench/batch.go` (Source interface), `scripts/native-bench/scan.go`, `scripts/native-bench/src_pqgo.go`, `scripts/native-bench/src_arrow.go`
- Test: `scripts/native-bench/parallel_test.go`

**Interfaces:**
- Produces:
  ```go
  // Group is one row group of one file that survived pruning by statistics.
  type Group struct {
      File  string
      Index int
      Rows  int64
      All   bool // statistics prove every row matches family and range
  }
  // Added to Source:
  //   Groups(files []string, family string, from, to int64) ([]Group, error)
  //   ReadGroup(r *GroupReader, g Group, cols []string, family string, from, to int64) (*Batch, error)
  // GroupReader is a per-worker handle that keeps the last opened file open,
  // so consecutive groups of one file reuse it. Close releases it.
  type GroupReader struct{ /* source-specific */ }
  func (s ...) NewGroupReader() *GroupReader
  func (r *GroupReader) Close() error
  // ScanParallel hands each worker its own GroupReader and calls fn from
  // that worker's goroutine; fn never sees a batch another worker holds.
  func ScanParallel(src Source, files, cols []string, family string, from, to int64, workers int,
      fn func(worker int, b *Batch) error) error
  ```
- `Scan` stays and becomes `ScanParallel` with 1 worker.

- [ ] **Step 1: Write the failing tests**

`TestScanParallelMatchesSerial`, for each source on the step 1 fixtures (reuse `source_test.go`'s helpers): `ScanParallel` with workers 1 and 4 yields the same multiset of `(day, family, actor_id, country)` rows, and the same `OpenedRowGroups` count. `TestGroupsPrunes`: on the 30-group pruning fixture, `Groups` for a one-day range returns no more groups than overlap that day, and `Groups` for the full range returns all 30.

- [ ] **Step 2: Run them and verify they fail**

Run: `cd scripts/native-bench && GOTMPDIR=~/.cache/gotmp /usr/local/go/bin/go test -race -run 'TestScanParallel|TestGroupsPrunes' .`
Expected: FAIL (undefined `ScanParallel`, `Groups`).

- [ ] **Step 3: Implement**

Workers pull `Group`s from a channel in file order, so neighbouring groups of one file tend to land on one worker. Each source caches its open file and footer per `GroupReader`. `OpenedRowGroups` keeps counting groups actually read; it is atomic.

- [ ] **Step 4: Run the whole module's tests with -race**

Run: `cd scripts/native-bench && GOTMPDIR=~/.cache/gotmp /usr/local/go/bin/go test -race ./...`
Expected: PASS, with no race reports.

- [ ] **Step 5: Commit**

`git commit -m "test: native-bench scans row groups in parallel"`

### Task 2: Queries on sorted keys and arrays

**Files:**
- Modify: `scripts/native-bench/queries.go`
- Test: `scripts/native-bench/queries_test.go`, `scripts/native-bench/parallel_test.go`

**Interfaces:**
- Consumes: `ScanParallel` (Task 1).
- Produces: unchanged signatures `Countries`, `DAU`, `AttributeValues(src Source, files []string, from, to int64) ([]Row, error)`, plus a package variable `Workers = 2` (set by the `query` command from a `-workers` flag, default 2).

- [ ] **Step 1: Write the failing test**

`TestQueriesDeterministicAcrossWorkers`: on the step 1 fixtures, each query run with `Workers` = 1, 2 and 4 returns identical rows. Keep every step 1 query test as is; they must still pass through the new path. For `memSource`, implement `Groups`/`ReadGroup` with one group per hand-built batch.

- [ ] **Step 2: Run it and verify it fails**

Expected: FAIL (`Workers` undefined, or the queries are still serial).

- [ ] **Step 3: Implement**

A shared interner, guarded by a mutex that is held only while remapping one batch's dictionary (once per dictionary entry, never per row). It gives global `uint32` ids, with `0xFFFFFFFF` for null.

Each worker appends packed `uint64` keys to its own slice. At the end the slices are concatenated, sorted (`slices.Sort`), and runs are counted:
- **Countries:** key `(day-from)<<48 | country<<32 | actor`. Per (day, country), count distinct non-null actors. A run made only of null actors still creates the country with 0.
- **DAU:** key `(day-from)<<32 | actor`.
- **AttributeValues:**
  - Events per (attribute, value) pair go into a per-worker `[]int64` indexed by pair id, summed at the end. The pair id comes from a second shared map, `(attrID<<32 | valueID) → pairID`, also filled once per dictionary entry.
  - Users: key `pairID<<32 | actor`, deduplicated by sort. A null actor adds 1 per pair, as in step 1.
  - Groups: the same, over non-empty `group_id`.

Keys are deduplicated within each row group before they are appended (sort the group's keys, drop repeats), so worker slices hold each (key) once per group rather than once per row.

- [ ] **Step 4: Run the module's tests with -race**

Expected: PASS, with no race reports.

- [ ] **Step 5: Commit**

`git commit -m "perf: native-bench queries count on sorted keys in parallel"`

### Task 3: Approximate distinct users for attribute values

**Files:**
- Create: `scripts/native-bench/hll.go`, `scripts/native-bench/hll_test.go`
- Modify: `scripts/native-bench/queries.go` (add `AttributeValuesApprox`), `scripts/native-bench/run.go` (accept `-q attrvalues-approx`)

**Interfaces:**
- Produces:
  ```go
  // Sketch counts distinct uint32 ids: exact while it holds at most
  // ExactLimit ids, then a HyperLogLog with 2^14 registers (~0.8% error).
  type Sketch struct{ /* ... */ }
  const ExactLimit = 1024
  func (s *Sketch) Add(id uint32)
  func (s *Sketch) Merge(o *Sketch)
  func (s *Sketch) Count() uint64
  func AttributeValuesApprox(src Source, files []string, from, to int64) ([]Row, error)
  ```
  The output has the same columns and order as `AttributeValues`; only `Users` may differ. `Groups` uses a Sketch too.

- [ ] **Step 1: Write the failing tests**

- `TestApproxSmallSetsExact`: adding 1,000 distinct ids, with repeats, gives a count of exactly 1,000.
- `TestApproxErrorBound`: for 10k, 100k and 1M distinct ids (generated with a fixed seed), the count is within 2%.
- `TestSketchMergeEqualsUnion`: merging two sketches equals one sketch fed both sets.
- `TestAttributeValuesApproxMatchesExact`: on the step 1 fixtures, every row equals `AttributeValues` except `Users` and `Groups`, which are within the Global Constraints' tolerance.

- [ ] **Step 2: Run them and verify they fail**

Expected: FAIL (undefined `Sketch`).

- [ ] **Step 3: Implement**

Write our own code; no HLL dependency. Hash ids with a 64-bit mixer (e.g. splitmix64). Use the standard HyperLogLog estimator with small-range linear counting. Keep one Sketch per pair per worker, and merge them at the end.

- [ ] **Step 4: Run the module's tests with -race**

Expected: PASS.

- [ ] **Step 5: Commit**

`git commit -m "test: native-bench approximate distinct users for attribute values"`

### Task 4: The step 1b matrix on a quiet host

**Files:**
- Modify: `scripts/native-bench/run.go` (a `matrix1b` subcommand, or flags on `matrix`; the implementer chooses, keeping step 1's matrix runnable), `scripts/native-bench/main.go`
- Test: `scripts/native-bench/run_test.go`
- Modify: `scripts/native-bench/RESULTS.md` (append a "Step 1b" section)

**Interfaces:**
- Consumes: the sources, the queries, `AttributeValuesApprox`, `Rewrite`.
- Produces: `func waitQuiet(maxLoad float64, poll, limit time.Duration, load func() (float64, error)) (float64, bool)`. It returns the 1-minute load it ended at, and whether that load was at most `maxLoad` before `limit` elapsed.

- [ ] **Step 1: Write the failing test**

`TestWaitQuietGivesUp`: given a fake `load` that always returns 3.0, `waitQuiet(1.0, 10*time.Millisecond, 50*time.Millisecond, load)` returns `(3.0, false)` within 200 ms. Given a fake that returns 3.0 and then 0.5, it returns `(0.5, true)`.

- [ ] **Step 2: Run it and verify it fails**

Expected: FAIL.

- [ ] **Step 3: Implement and run the matrix**

Each cell, and each rewrite, first calls `waitQuiet(1.0, 10s, 10m, …)` and records the load it ran at. Cells are interleaved: for each range, query and run index, loop over layout and source. That way load drift spreads across layouts instead of following them.

| Dimension | Values |
|---|---|
| Volumes | `100k`, `1m` |
| Layouts | `duckdb`, `pqgo-1d`, `pqgo-4d`, `pqgo-8h` (PerMonth/90 rows), `arrow-1d` |
| Sources | `pqgo` on every layout; `arrow` on `arrow-1d` and `pqgo-1d` (the library re-check) |
| Queries | `countries`, `dau`, `attrvalues`, `attrvalues-approx` |
| Ranges | `1d` 2026-09-30; `7d` 2026-09-08..14; `30d-mid` 2026-08-16..09-14; `30d` 2026-09-01..30; `365d` 2025-10-01..2026-09-30 |

- Runs per cell: 3, reporting the first and the best of runs 2–3, as in step 1.
- **Rewrites:** `pqgo-8h` is new at both volumes. At 1M, re-time the rewrites of `pqgo-1d` and `arrow-1d` behind the gate for the library re-check. Write them to a fresh directory, so step 1's files stay as they are.
- **Correctness:** exact results must hash-equal across layouts and sources. `attrvalues-approx` must be within tolerance of `attrvalues`.
- **Output:** append to `RESULTS.md` per volume:
  - a range × query table for each layout, `pqgo` source: best wall, CPU and peak RSS;
  - the library table (`arrow` vs `pqgo` reads on `arrow-1d` and `pqgo-1d`, and the 1M write walls);
  - per range, a "dashboard trio" line: the sum of countries + dau + attrvalues-approx;
  - the load range.

Run: `cd scripts/native-bench && GOTMPDIR=~/.cache/gotmp /usr/local/go/bin/go test -race ./... && /usr/local/go/bin/go build -o ~/.cache/twbench-native/bin/native-bench . && ~/.cache/twbench-native/bin/native-bench matrix1b ~/.cache/twbench-native`. Run it in the background, logging to `~/.cache/twbench-native/matrix1b.log`, and poll it in bounded waits.
Expected: tests PASS, no failed cells, and every cell's recorded load ≤ 1.0, or the cells over 1.0 listed.

- [ ] **Step 4: Commit**

`git commit -m "test: measure the optimised native scans on a quiet host"`

### Task 5: Decide and record

**Files:**
- Modify: `docs/superpowers/specs/2026-10-21-native-engine-design.md` (add a "Measurements (step 1b)" section after "Measurements (step 1)"; update "Left to the builder" and the order of work)

- [ ] **Step 1: Write the section**

From `RESULTS.md` step 1b:
- the per-range tables at both volumes, for the chosen layout;
- the library re-check;
- what the optimisations bought against step 1's numbers, on the same range and layout.

- [ ] **Step 2: Record the decisions, one line each with the deciding numbers**

- **Row-group size.** Which of 8h/1d/4d, judged on 1d/7d/30d-mid/30d/365d together.
- **The library at 1M.** Confirmed or reversed.
- **The 30-day targets.** Met or missed at each volume, using the trio with `attrvalues-approx`.
- **The 1-year target at 1M.** Met or missed, warm.
- **The cache, per the owner's rule** (no cache unless great results). A per-day cache can at best reduce a 1-year load to about the cost of the uncached days plus merging, so its upper-bound speedup is roughly `T(365d) / T(1d)`.
  - **Cache: no** if the 1-year target is met without it.
  - Otherwise, state that bound, and say **cache: yes (step 5)** only if the 1-year target is missed and the bound is at least 5×.
  - Otherwise **no**, and say what else would close the gap.
- **Approximate distinct users.** Whether `attrvalues-approx` is what the engine ships for whole-range distinct counts, with the speed and memory it saves over exact.

- [ ] **Step 3: Commit**

`git commit -m "docs: record the native engine's step 1b measurements and decisions"`
