# Performance measures and Web Vitals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One retention pair for every family, a clustered `events` table, and a third event family `measures` (time, size, number) with bucketed percentiles, a `measures` tool, two system dashboards, `measure()` in the SDK and opt-in Web Vitals.

**Architecture:** Three stacked PRs. PR 1 collapses retention config. PR 2 rebuilds `events` as `WITHOUT ROWID` on `(family, project_id, day, id)` (migration 023). PR 3 adds `value`, `measure`, `sample_rate` and a virtual `bucket` column, two product-style aggregate tables and their views (migration 024), ingest rules, a read op, system dashboards, and SDK support with a lazily loaded `web-vitals` bundle.

**Tech Stack:** Go (modernc SQLite 3.53), TypeScript SDK (esbuild, vitest, `web-vitals` 6.2.2), React dashboard (Playwright e2e).

**Spec:** `docs/superpowers/specs/2026-09-30-performance-measures-design.md`. Read it before any task; decision numbers below refer to it.

## Global Constraints

- Go is at `/usr/local/go/bin/go` (not on PATH). Use `export PATH=/usr/local/go/bin:$PATH` in every shell.
- The machine is shared: run at most one `make check` (or full `go test ./...`) at a time. Prefer targeted `go test ./internal/<pkg>/ -run <Name>` while iterating.
- `make check` = vet + race tests with coverage (total and each core package ≥ 90%) + restore test. It needs Node 22 for `make ui`.
- Commits: Conventional Commits, `<type>(<scope>): <subject>`, imperative, lower case, no trailing period. End every commit message with the line `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never put a model name anywhere else.
- Docs change in the **same commit** as the code they describe (CLAUDE.md table). `internal/api/docs_sync_test.go` binds much of it.
- After any change under `sdk/`, run `cd sdk && npm run build` and commit the regenerated bundle(s) in `internal/server/`.
- Refusals are typed (`manage.ErrInvalid`, `manage.ErrNotFound`); never match on message text.
- No `CHECK` constraints in the database; ingest validates.
- Do not push, open PRs, or touch other branches. The controller does that.
- Migration numbers: clustering is `023`, measures is `024`. `022` belongs to #91.
- Measure kinds are exactly `time` (milliseconds), `size` (bytes), `number` (no unit). Families are exactly `views`, `product`, `measures`.
- Reserved metrics: `$lcp`, `$inp`, `$fcp`, `$ttfb` are `time`; `$cls` is `number`.
- Zero bucket is `-1000`; bucket base is `1.04`; `approx_value = CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END`.
- Widget SQL parameters are `:project`, `:from`, `:to`. Show `time` values in milliseconds with the `number` format.

## Review Focus

1. A backend batch sets `$sample_rate` once in batch `attributes` and mixes families: measures store the rate, views and product events get a warning and store 1. (Task 6 test `TestSampleRateOnlyOnMeasures`.)
2. `value` sent as a numeric string (`"340"`), a boolean, or `null` on a measure: the event is rejected with a reason naming `value`, never stored as 0. (Task 6 table test.)
3. An old client with no `family` sends `name: "$lcp"`: it is stored as a product event with the existing unknown-name warning, never as a measure. (Task 6 test `TestMissingFamilyNeverInfersMeasures`.)
4. A metric whose every sample is 0 (a CLS of 0 on every page): p50/p75/p95 are `0`, not `NULL` or empty. (Task 4 percentile test, Task 7 op test.)
5. A page closed before LCP/CLS/INP finalize: the vitals reported while `visibilityState === "hidden"` are sent immediately by `sendBeacon`, not left in the queue. (Task 10 vitest.)

---

# PR 1 — `feat(config)!: keep one retention pair for all events`

Branch: `feat/retention-pair`, created from `spec/performance-measures`.

### Task 1: One retention pair

**Files:**
- Modify: `internal/config/config.go` (types l.28–40, parse l.205–215, `renamed` l.265–272, `validate` l.296–304, `MaxEventAge` l.318–324)
- Modify: `internal/jobs/jobs.go` (interface l.28–45, `RunDailyPass` l.81–176)
- Modify: `internal/store/store.go:182`, `internal/store/sqlite/prune.go` (l.24–40)
- Modify: `internal/server/handlers.go:84-87` (comment), `internal/server/ingest.go:287-293` (comment)
- Modify tests: `internal/config/config_test.go`, `internal/config/example_config_test.go:47`, `internal/jobs/jobs_test.go`, `internal/jobs/errors_test.go:118-122`, `internal/server/server_test.go:705-718`, `internal/store/sqlite/prune_test.go`, `internal/store/sqlite/coverage_test.go:129-130,207,286`
- Modify docs: `.env.example:25-28`, `docs/deployment.md:103-107,125-129,135`, `docs/twillingate.md:715`, `deploy/UPGRADES.md` (append)

**Interfaces:**
- Produces: `config.Retention{Events RetentionClass; ArchivedDays int}`; `(*Config).MaxEventAge()` reads `Retention.Events.RawDays`; `PruneAggregates(ctx, projectID int64, before civil.Date) error` (one cutoff) on `store.Store`, `jobs.Store` and `*sqlite.DB`.

- [ ] **Step 1: Write the failing config tests.** In `config_test.go` replace `TestViewsRetentionDefaultsAndMaxEventAge`, `TestViewsRetentionFromEnv`, `TestRejectsNegativeViewsRetention` with:

```go
func TestEventsRetentionDefaultsAndMaxEventAge(t *testing.T) {
	c := configtest.Load(t, nil)
	if c.Retention.Events.RawDays != 30 || c.Retention.Events.AggregateDays != 365 {
		t.Fatalf("events retention = %+v, want 30/365", c.Retention.Events)
	}
	if got := c.MaxEventAge(); got != 30*24*time.Hour {
		t.Fatalf("MaxEventAge = %v", got)
	}
}

func TestEventsRetentionFromEnv(t *testing.T) {
	c := configtest.Load(t, map[string]string{
		"RETENTION_EVENTS_RAW_DAYS": "14", "RETENTION_EVENTS_AGGREGATE_DAYS": "90"})
	if c.Retention.Events.RawDays != 14 || c.Retention.Events.AggregateDays != 90 {
		t.Fatalf("events retention = %+v", c.Retention.Events)
	}
	if got := c.MaxEventAge(); got != 14*24*time.Hour {
		t.Fatalf("MaxEventAge = %v, want 14 days", got)
	}
}

func TestRejectsNegativeEventsRetention(t *testing.T) {
	for _, k := range []string{"RETENTION_EVENTS_RAW_DAYS", "RETENTION_EVENTS_AGGREGATE_DAYS"} {
		if _, err := configtest.LoadErr(t, map[string]string{k: "-1"}); err == nil {
			t.Errorf("%s=-1 accepted", k)
		}
	}
}

// The per-family names are gone, with no refusal: a leftover one is ignored.
func TestOldRetentionNamesHaveNoEffect(t *testing.T) {
	c := configtest.Load(t, map[string]string{
		"RETENTION_VIEWS_RAW_DAYS": "3", "RETENTION_VIEWS_AGGREGATE_DAYS": "4",
		"RETENTION_PRODUCT_RAW_DAYS": "5", "RETENTION_PRODUCT_AGGREGATE_DAYS": "6",
		"RETENTION_WEB_RAW_DAYS": "7", "RETENTION_APP_AGGREGATE_DAYS": "8"})
	if c.Retention.Events.RawDays != 30 || c.Retention.Events.AggregateDays != 365 {
		t.Fatalf("old names changed retention: %+v", c.Retention.Events)
	}
}
```

If `configtest` has no error-returning loader, use the existing pattern in `TestValidationErrors` (call `config.FromEnv` with a lookup map) instead of `LoadErr`. Update `TestDefaultsApplied` (l.38–39), `TestEnvOverrides` (l.70–73, 94–99: use the two new names with `"3"`/`"60"`), `TestValidationErrors` (l.121: `RETENTION_EVENTS_RAW_DAYS`), `TestRenamedVariablesRefuse` (keep only `LISTEN_ADDR`), and `example_config_test.go:47` (`cfg.Retention.Events.RawDays != 30`).

- [ ] **Step 2: Run to see them fail.** `go test ./internal/config/` → compile errors on `Retention.Events`.

- [ ] **Step 3: Implement config.**

```go
type Retention struct {
	// Events is every family's window: raw rows are rolled up and deleted
	// after RawDays; aggregates, actors, cohorts and identities are kept
	// AggregateDays.
	Events       RetentionClass `json:"events"`
	ArchivedDays int            `json:"archived_days"` // keep the existing comment
}
```
Parse: `Events: RetentionClass{RawDays: e.num("RETENTION_EVENTS_RAW_DAYS", 30), AggregateDays: e.num("RETENTION_EVENTS_AGGREGATE_DAYS", 365)}`. Remove the four `RETENTION_WEB_*`/`RETENTION_APP_*` rows from `renamed`. `validate`: check `c.Retention.Events` only. `MaxEventAge`: `time.Duration(c.Retention.Events.RawDays) * 24 * time.Hour`; comment: "the raw window: a clamped timestamp can never land in a day already rolled up."

- [ ] **Step 4: Collapse `PruneAggregates` to one cutoff.** `prune.go`: `func (d *DB) PruneAggregates(ctx context.Context, projectID int64, before civil.Date) error` deletes from `viewsAggTables`, `productAggTables` and `identityAggTables` (if it was pruned there before, keep it) with the same `before`; update its comment (no "per-family cutoffs"). Same signature in `store.go:182`, `jobs.go` interface, and the fake in `errors_test.go`. Update `prune_test.go` (`TestPruneAggregatesIndependentCutoffs` becomes `TestPruneAggregatesOneCutoff`: every agg table loses rows before the cutoff and keeps rows on or after it) and the calls in `coverage_test.go`.

- [ ] **Step 5: Jobs.** In `RunDailyPass` use `ret.Events.RawDays` for both `ViewDaysBefore` and `ProductDaysBefore`, and `today.AddDays(-ret.Events.AggregateDays)` for `PruneAggregates`, `PruneActors`, `PruneIdentities`. `jobs_test.go`: `jobsVars` becomes `{"RETENTION_EVENTS_RAW_DAYS": "7", "RETENTION_EVENTS_AGGREGATE_DAYS": "365"}`. Replace `TestRunDailyPassAggregatesEachFamilyByItsOwnWindow` with `TestRunDailyPassAggregatesEveryFamilyByOneWindow`: raw window 7, seed a view and a product event 10 days old and a view and a product event 3 days old; after the pass the old ones are rolled up (aggregate rows exist, raw rows gone) and the recent ones are still raw.

- [ ] **Step 6: Server.** `server_test.go:717` `TestEventAgeClampUsesGlobalRawWindow` sets `RETENTION_EVENTS_RAW_DAYS: "3"`; fix comments at 705–706, `handlers.go:84-86`, `ingest.go:290` to say "the raw window (`RETENTION_EVENTS_RAW_DAYS`)".

- [ ] **Step 7: Docs, same commit.**
  - `.env.example`: replace the four lines with `#RETENTION_EVENTS_RAW_DAYS=30` and `#RETENTION_EVENTS_AGGREGATE_DAYS=365`.
  - `docs/deployment.md` table rows 103–106 become:
    ```
    | `RETENTION_EVENTS_RAW_DAYS` | Days raw events of every family are kept before rollup. Also the oldest client timestamp accepted: older events are clamped to this edge. Default 30. |
    | `RETENTION_EVENTS_AGGREGATE_DAYS` | Days aggregates of every family (and actors, cohorts, identities) are kept. Default 365. |
    ```
    Lines 125–129: keep the `LISTEN_ADDR` → `INGEST_ADDR` sentence and the `MCP_*` sentence; drop every mention of `RETENTION_WEB_*`/`RETENTION_APP_*`/`RETENTION_VIEWS_*`. Line 135: "Lower `RETENTION_EVENTS_RAW_DAYS` (say `7`): raw events are the largest table in the file, and the live halves of the `v_*` views scan them on every query."
  - `docs/twillingate.md:715`: `max_event_age` equals `RETENTION_EVENTS_RAW_DAYS`.
  - `deploy/UPGRADES.md`, append (newest last):
    ```
    ### Upgrading to one retention pair (no migration)

    Retention is one pair for every family: `RETENTION_EVENTS_RAW_DAYS`
    (default 30) and `RETENTION_EVENTS_AGGREGATE_DAYS` (default 365). The old
    per-family variables are no longer read and no longer refuse the boot, so a
    leftover one is silently ignored and its family falls back to the defaults.
    Before upgrading, look for them in `twillingate.env`:

    ```sh
    grep -E '^RETENTION_(VIEWS|PRODUCT|WEB|APP)_' /etc/twillingate/twillingate.env
    ```

    Replace them with the two new names, choosing one value where views and
    product differed. A lower aggregate window than before deletes the older
    aggregates on the first daily pass.
    ```
    (Check the real env-file path in `docs/deployment.md` and use it.)

- [ ] **Step 8: Verify.** `grep -rnE 'RETENTION_(VIEWS|PRODUCT|WEB|APP)_' --exclude-dir=node_modules --exclude-dir=superpowers .` must print only the new `UPGRADES.md` entry's lines and the historical UPGRADES lines 17/37/139 (history stays). Run `go test ./internal/config/ ./internal/jobs/ ./internal/server/ ./internal/store/... ./internal/api/ -run 'Retention|Prune|Clamp|DailyPass|EnvVar|Example|Renamed|Defaults|Overrides|Validation'` then `go vet ./...`, then `make check`.

- [ ] **Step 9: Commit.**
```bash
git add -A && git commit -m "feat(config)!: keep one retention pair for all events" -m "RETENTION_EVENTS_RAW_DAYS and RETENTION_EVENTS_AGGREGATE_DAYS replace the per-family variables, which are no longer read.

BREAKING CHANGE: RETENTION_VIEWS_*, RETENTION_PRODUCT_*, RETENTION_WEB_* and RETENTION_APP_* are ignored; set the two new variables instead.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

# PR 2 — `perf(store): cluster events by family, project and day`

Branch: `perf/cluster-events`, created from `feat/retention-pair`.

### Task 2: Migration 023 and the write path

**Files:**
- Create: `internal/store/sqlite/migrations/023_cluster_events.sql`
- Create: `internal/store/sqlite/migration023_test.go`
- Modify: `internal/store/sqlite/write.go:16-17,33-60` (bind `day`, comment)
- Modify: `internal/server/handlers.go:297-300` (comment only)
- Modify tests inserting raw rows without `day`: `internal/store/sqlite/coverage_test.go:78`, `internal/store/sqlite/registry_test.go:445`, `internal/api/seed_test.go:103`, and any other `INSERT INTO events` in tests (`grep -rn "INTO events" --include=*_test.go internal`)
- Modify: `internal/store/sqlite/views_test.go:18-108` (plan assertions), `internal/store/sqlite/flatview_test.go` (add a 023 parity test)
- Modify: `internal/store/sqlite/bench_test.go` (file-size report)
- Modify: `deploy/UPGRADES.md` (append)

**Interfaces:**
- Produces: `events` is `WITHOUT ROWID`, `PRIMARY KEY (family, project_id, day, id)`, `day TEXT NOT NULL` (plain). No `idx_events_family`. `WriteEvents` binds `day = e.TS.UTC().Format("2006-01-02")`.

- [ ] **Step 1: Write the failing migration test** `migration023_test.go`, mirroring `migration020_test.go` (reuse `newTestDBAt`, `snapshotViews`, `hasTable`):

```go
func TestMigration023KeepsEveryViewsAnswer(t *testing.T) {
	ctx := context.Background()
	db := newTestDBAt(t, 22) // 022 does not exist on this branch; migrateThrough skips missing versions
	// Seed exactly as TestMigration020KeepsEveryViewsAnswer does after its
	// migrate step: raw views and product events on 3 days for 2 projects,
	// plus one agg_views_paths and one agg_product_daily row. Insert through
	// db.WriteEvents so the rows match what ingest writes.
	seed023(t, db)
	before := snapshotViews(t, db)
	for _, v := range []string{"v_views_daily", "v_views_paths", "v_product_daily", "v_product_attrs", "v_identity_daily"} {
		if len(before[v]) == 0 {
			t.Fatalf("%s empty before migrating", v)
		}
	}
	if err := db.migrateThrough(ctx, 23); err != nil {
		t.Fatal(err)
	}
	after := snapshotViews(t, db)
	for v, rows := range before {
		if strings.Join(rows, "\n") != strings.Join(after[v], "\n") {
			t.Errorf("%s changed across 023", v)
		}
	}
	var sql string
	db.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE name='events'`).Scan(&sql)
	if !strings.Contains(sql, "WITHOUT ROWID") || !strings.Contains(sql, "PRIMARY KEY (family, project_id, day, id)") {
		t.Errorf("events not clustered: %s", sql)
	}
	var n int
	db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND tbl_name='events' AND sql IS NOT NULL`).Scan(&n)
	if n != 0 {
		t.Errorf("events still has %d explicit indexes", n)
	}
	db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE day <> substr(ts,1,10)`).Scan(&n)
	if n != 0 {
		t.Errorf("%d rows with day != date(ts)", n)
	}
}

func TestMigration023ReplayIsIgnored(t *testing.T) {
	db := newTestDB(t)
	ev := store.Event{ID: "0190aaaa-0000-7000-8000-000000000001", ProjectID: 1, Family: store.FamilyProduct,
		EventName: "signup", TS: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC), ActorID: "a"}
	for i := 0; i < 2; i++ {
		if err := db.WriteEvents(context.Background(), []store.Event{ev}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM events WHERE id=?`, ev.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("replay stored %d rows", n)
	}
}
```
Write `seed023` in the test file by copying the seed block of `TestMigration020KeepsEveryViewsAnswer` (adapt it to write through `WriteEvents`, since the schema at 22 is the 020 shape). If `migrateThrough(ctx, 22)` fails because version 22 is absent, use `newTestDBAt(t, 21)` instead — the point is "before 023".

- [ ] **Step 2: Run to see it fail.** `go test ./internal/store/sqlite/ -run Migration023` → FAIL (no 023).

- [ ] **Step 3: Write `023_cluster_events.sql`.** Model it on `020_one_events_table.sql`:
  1. Header comment: what and why (spec decisions 10–13), the dedup gap.
  2. `DROP VIEW` every view that reads `events`, `raw_views` or `raw_product`: `v_events_flat`, `v_identity_daily`, `v_product_attrs`, `v_product_daily`, `v_product_totals`, the 14 `v_views_*` views listed in 020 l.14–32, then `raw_views`, `raw_product`.
  3. `CREATE TABLE events_new (...)` with the 020 column list, except: `id TEXT NOT NULL`, `day TEXT NOT NULL` (no `GENERATED`), and a trailing `PRIMARY KEY (family, project_id, day, id)`, then `) WITHOUT ROWID;`. Put `family, project_id, day, id` first in the column list.
  4. `INSERT INTO events_new (<every column>) SELECT <every column> FROM events;` (copy `day` as is; it is `substr(ts,1,10)` already).
  5. `DROP TABLE events; ALTER TABLE events_new RENAME TO events;` (dropping the table drops `idx_events_family`).
  6. Recreate `raw_views`, `raw_product` and every dropped view **byte-for-byte** from `020_one_events_table.sql` (copy l.112–520 exactly; `TestViewsReferenceNoRefusedName` string-matches the `v_product_attrs` meta fragment and `TestMigration020FlatViewMatchesTheBaseRebuild` compares `v_events_flat` to `RebuildFlatView`). Do not reformat.

- [ ] **Step 4: Bind `day` in `write.go`.** Add `day` after `ts` in the column list, one more `?`, and bind `e.TS.UTC().Format("2006-01-02")`. Update the comment at l.16–17: duplicates are ignored on `(family, project_id, day, id)`; a retry repeats all four. Same fix to the comment in `handlers.go:297-300`.

- [ ] **Step 5: Fix raw inserts in tests.** Every test `INSERT INTO events (...)` that omits `day` must add `day` = the first 10 characters of its `ts`. For `coverage_test.go:78` (`ts '2026-02-30...'`) set `day '2026-02-30'` so the corrupt-timestamp test still exercises what it did.

- [ ] **Step 6: Query-plan test.** In `views_test.go` `TestViewsLiveHalvesUseTheDayIndex`: the "never scan" check stays (`SCAN events`); the day-bound search check becomes `strings.Contains(d, "USING PRIMARY KEY (family=? AND project_id=? AND day")` with the same `day>`/`day<`/`day=` condition; update the doc comment (l.18–52) to name the primary key instead of `idx_events_family`. Add `TestMigration023FlatViewMatchesTheBaseRebuild` in `flatview_test.go`, identical to the 020 one but at 23.

- [ ] **Step 7: Run the store and dependent suites.** `go test ./internal/store/... ./internal/api/ ./internal/jobs/ ./internal/reporting/ ./internal/server/`. Expected PASS. If `TestRawTableIsReadOnlyThroughFamilyViews` flags nothing new, good; the migration's SQL file is not scanned.

- [ ] **Step 8: Bench.** In `bench_test.go` add `BenchmarkEventsFileSize` (or extend `setupBenchDB` usage) that seeds with `seedBenchViews` + `seedBenchEvents`, checkpoints, and reports `b.ReportMetric(float64(sizeBytes)/1e6, "MB")` from `PRAGMA page_count * page_size`. Run `go test ./internal/store/sqlite/ -run x -bench 'LiveHalf|FileSize' -benchtime 3x` on this branch and on `feat/retention-pair` (use `git stash`-free approach: `git worktree add /tmp/base feat/retention-pair` and run there, then `git worktree remove /tmp/base`). Record both results in the commit body.

- [ ] **Step 9: UPGRADES.md.** Append:
```
### Upgrading to a clustered events table (migration 023)

The raw `events` table is rebuilt so each family's rows for a project and
day are stored together, keyed by `(family, project_id, day, id)`. Its two
indexes go away. Every view, tool, dashboard and saved query answers exactly
as before. The migration copies the raw window (30 days by default): while it
runs the file briefly holds that window twice, so keep that much free disk.

What changes on the day:

- Duplicates are detected on `(family, project_id, day, id)`. A retried
  batch is still ignored; the one gap is a retry of an event whose timestamp
  was clamped (more than 5 minutes ahead, or older than the raw window) that
  arrives on the other side of midnight: it is stored twice.
- SQL reading `events` directly (the CLI's database, not the `query` tool)
  sees `day` as an ordinary column; its values are unchanged.

There is no down migration. Rolling back means restoring the pre-upgrade copy
or Litestream snapshot, so take one before upgrading.
```

- [ ] **Step 10: Full check and commit.** `make check`, then:
```bash
git add -A && git commit -m "perf(store): cluster events by family, project and day" -m "<benchmark before/after from step 8>

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

# PR 3 — `feat: record performance measures and Web Vitals`

Branch: `feat/measures`, created from `perf/cluster-events`. Tasks 3–11 each commit on this branch (subjects below); the PR is squashed under the title above.

### Task 3: Migration 024, store types and the write path

**Files:**
- Create: `internal/store/sqlite/migrations/024_measures.sql`, `internal/store/sqlite/migration024_test.go`
- Modify: `internal/store/store.go` (`Family` l.14–23, `Event` l.37–55)
- Modify: `internal/store/sqlite/write.go`, `flatview.go:22-29`, `registry.go:226-234` (`projectTables`), `prune.go` (table list), `aggregate_views.go:29-44` (`rawMeasures`, `MeasureDaysBefore`)
- Modify: `internal/store/sqlite/rawreads_test.go` (allow `raw_measures`)

**Interfaces:**
- Produces:
  - `store.FamilyMeasures Family = "measures"`
  - `store.Event` gains `Value *float64`, `Measure string`, `SampleRate float64` (0 is written as 1)
  - `store.MeasureTime = "time"`, `store.MeasureSize = "size"`, `store.MeasureNumber = "number"` and `store.MeasureKinds = []string{"time","size","number"}`
  - `store.ReservedMetrics = map[string]string{"$lcp":"time","$inp":"time","$cls":"number","$fcp":"time","$ttfb":"time"}`
  - `(*DB).MeasureDaysBefore(ctx, projectID int64, before civil.Date) ([]civil.Date, error)`, also on the `store.Store` interface
  - Views `raw_measures`, `v_measures_daily(project_id, day, event_name, measure, bucket, approx_value, samples, weight, sum)`, `v_measures_attrs(project_id, day, event_name, measure, attr_key, attr_value, bucket, approx_value, samples, weight, sum)`; tables `agg_measures_daily`, `agg_measures_attrs`; `v_events_flat` gains `value, measure, sample_rate`

- [ ] **Step 1: Failing tests** in `migration024_test.go`:

```go
func TestMigration024AddsMeasures(t *testing.T) {
	db := newTestDB(t)
	for _, c := range []string{"value", "measure", "sample_rate"} {
		if !hasColumn(t, db, "events", c) {
			t.Errorf("events.%s missing", c)
		}
	}
	for _, tbl := range []string{"agg_measures_daily", "agg_measures_attrs"} {
		if !hasTable(t, db, tbl) {
			t.Errorf("%s missing", tbl)
		}
	}
}

func TestBucketColumn(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, c := range []struct {
		value float64
		want  int
	}{{340, 149}, {0.08, -64}, {1, 0}, {0, -1000}, {1e-18, -1000}} {
		v := c.value
		ev := measureEvent("m", v, "time")
		if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil {
			t.Fatal(err)
		}
		var got int
		db.db.QueryRow(`SELECT bucket FROM raw_measures WHERE id=?`, ev.ID).Scan(&got)
		if got != c.want {
			t.Errorf("bucket(%v) = %d, want %d", c.value, got, c.want)
		}
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM events WHERE family<>'measures' AND bucket IS NOT NULL`).Scan(&n)
	if n != 0 {
		t.Errorf("non-measure rows have a bucket")
	}
}

func TestWriteMeasureRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ev := measureEvent("checkout_api", 340, "time")
	ev.SampleRate = 0.25
	if err := db.WriteEvents(context.Background(), []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	var value, rate float64
	var measure string
	db.db.QueryRow(`SELECT value, measure, sample_rate FROM raw_measures WHERE id=?`, ev.ID).Scan(&value, &measure, &rate)
	if value != 340 || measure != "time" || rate != 0.25 {
		t.Fatalf("got %v %q %v", value, measure, rate)
	}
	// A product event stores NULL value and sample_rate 1.
	p := store.Event{ID: uuidFor(t), ProjectID: 1, Family: store.FamilyProduct, EventName: "signup",
		TS: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC), ActorID: "a"}
	db.WriteEvents(context.Background(), []store.Event{p})
	var isNull bool
	db.db.QueryRow(`SELECT value IS NULL AND sample_rate = 1 FROM raw_product WHERE id=?`, p.ID).Scan(&isNull)
	if !isNull {
		t.Fatal("product row carries a value or a rate")
	}
}
```
Add helpers in the test file:
```go
var measureSeq int
func measureEvent(name string, v float64, kind string) store.Event {
	measureSeq++
	return store.Event{ID: fmt.Sprintf("0190bbbb-0000-7000-8000-%012d", measureSeq), ProjectID: 1,
		Family: store.FamilyMeasures, EventName: name, TS: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
		ActorID: "a", Value: &v, Measure: kind, SampleRate: 1}
}
```
(Use an existing uuid helper for `uuidFor` if the package has one; otherwise a counter like above.)

- [ ] **Step 2: Run to see them fail.**

- [ ] **Step 3: `024_measures.sql`.**

```sql
-- 024: a third family, measures: a name, a number and a time.
-- Spec: docs/superpowers/specs/2026-09-30-performance-measures-design.md
ALTER TABLE events ADD COLUMN value REAL;
ALTER TABLE events ADD COLUMN measure TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN sample_rate REAL NOT NULL DEFAULT 1;
-- Log-scale bucket, base 1.04 (about ±2% relative precision). -1000 is the
-- zero bucket: below every real bucket, and not NULL because the aggregate
-- tables key on it.
ALTER TABLE events ADD COLUMN bucket INTEGER GENERATED ALWAYS AS
  (CASE WHEN value IS NULL THEN NULL
        WHEN value > 1e-17 THEN CAST(ceil(log(value) / log(1.04)) AS INTEGER)
        ELSE -1000 END) VIRTUAL;

CREATE VIEW raw_measures AS SELECT * FROM events WHERE family = 'measures';

CREATE TABLE agg_measures_daily (
    project_id INTEGER NOT NULL, day TEXT NOT NULL,
    event_name TEXT NOT NULL, measure TEXT NOT NULL,
    bucket INTEGER NOT NULL,
    samples INTEGER NOT NULL, weight REAL NOT NULL, sum REAL NOT NULL,
    PRIMARY KEY (project_id, day, event_name, measure, bucket)
) WITHOUT ROWID;

CREATE TABLE agg_measures_attrs (
    project_id INTEGER NOT NULL, day TEXT NOT NULL,
    event_name TEXT NOT NULL, measure TEXT NOT NULL,
    attr_key TEXT NOT NULL, attr_value TEXT NOT NULL,
    bucket INTEGER NOT NULL,
    samples INTEGER NOT NULL, weight REAL NOT NULL, sum REAL NOT NULL,
    PRIMARY KEY (project_id, day, event_name, measure, attr_key, attr_value, bucket)
) WITHOUT ROWID;

CREATE VIEW v_measures_daily AS
SELECT project_id, day, event_name, measure, bucket,
       CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
       samples, weight, sum
FROM agg_measures_daily
UNION ALL
SELECT project_id, day, event_name, measure, bucket,
       CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END,
       COUNT(*), SUM(1.0 / sample_rate), SUM(value / sample_rate)
FROM raw_measures
GROUP BY project_id, day, event_name, measure, bucket;
```

`v_measures_attrs`: copy `v_product_attrs` from `020_one_events_table.sql` l.161–242 **as the starting text** (its `cap` CTE fragment must stay byte-identical, including whitespace, for `TestViewsReferenceNoRefusedName` and the meta-guard test at `views_test.go:1025`), then change it so that:
  - every CTE reads `raw_measures` instead of `raw_product` and carries `measure`, `bucket`, `sample_rate`, `value` alongside `project_id, day, event_name`;
  - ranking is `ROW_NUMBER() OVER (PARTITION BY project_id, day, event_name, measure, attr_key ORDER BY COUNT(*) DESC, attr_value)` over `vals GROUP BY project_id, day, event_name, measure, attr_key, attr_value` (same tie-break as the Go rollup in Task 4);
  - the live half returns, per `(project_id, day, event_name, measure, attr_key, attr_value', bucket)` where `attr_value'` is `attr_value` when `rn <= (SELECT n FROM cap)` else `'(other)'`: `COUNT(*) AS samples, SUM(1.0/sample_rate) AS weight, SUM(value/sample_rate) AS sum`, plus `approx_value` computed as above;
  - the aggregate half is `SELECT project_id, day, event_name, measure, attr_key, attr_value, bucket, <approx_value>, samples, weight, sum FROM agg_measures_attrs`;
  - there are no unique-user or unique-group columns.

Recreate `v_events_flat` with `value, measure, sample_rate` appended after `attributes`: `DROP VIEW v_events_flat; CREATE VIEW v_events_flat AS SELECT <020 l.117 columns>, value, measure, sample_rate FROM events;` and append the same three names to `flatViewBaseColumns` in `flatview.go` so `RebuildFlatView` produces the identical text (add `TestMigration024FlatViewMatchesTheBaseRebuild` mirroring the 020/023 one).

- [ ] **Step 4: Store types and write path.** In `store.go` add the constants and fields listed under Interfaces, and extend the `Family` comment ("Ingest decides it from the event's `family`, or from its name when `family` is absent"). In `write.go` accept `FamilyMeasures` in the family check; add `value, measure, sample_rate` to the column list and bind `e.Value` (nil → NULL), `e.Measure`, and `rate` where `rate := e.SampleRate; if rate == 0 { rate = 1 }`.

- [ ] **Step 5: Registries of tables and raw sources.** Append `"agg_measures_daily", "agg_measures_attrs"` to `projectTables` (`registry.go`) and add `var measuresAggTables = []string{"agg_measures_daily", "agg_measures_attrs"}` to `prune.go`, pruned with the same single cutoff. In `aggregate_views.go` add `rawMeasures = "raw_measures"` and `func (d *DB) MeasureDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error) { return d.daysBefore(ctx, rawMeasures, projectID, before) }`; add it to the `store.Store` interface. In `rawreads_test.go` add `'raw_measures'` to the allowed view names.

- [ ] **Step 6: Run** `go test ./internal/store/...` (includes `TestProjectTablesMatchesSchema`, `TestPruneAggregatesCoversAllAggTables`, `TestMigrationViews` — add `raw_measures`, `v_measures_daily`, `v_measures_attrs` to its expected list). PASS.

- [ ] **Step 7: Commit** `feat(store): store measures with a value, a kind and a sample rate`.

### Task 4: Rolling up measures

**Files:**
- Create: `internal/store/sqlite/aggregate_measures.go`, `internal/store/sqlite/aggregate_measures_test.go`
- Modify: `internal/store/store.go` (interface)

**Interfaces:**
- Consumes: Task 3 tables/views, `store.SystemAttributes`, `store.DeclarableAttributes`, `attrPath` (aggregate_product.go).
- Produces: `func (d *DB) AggregateMeasureDay(ctx context.Context, projectID int64, day civil.Date, attrs []string, topN int) error` on `*DB` and `store.Store`.

- [ ] **Step 1: Failing tests** in `aggregate_measures_test.go`:
  1. `TestAggregateMeasureDayHistogram`: write 4 `time` samples of `checkout_api` on 2026-09-01 (values 100, 100, 340, 0; the last with `SampleRate: 0.5`) plus one `$cls` `number` sample; aggregate the day; assert `agg_measures_daily` has rows for `(checkout_api,time,bucket(100))` with `samples=2, weight=2, sum=200`, `(…,bucket(340))` `samples=1`, `(…,-1000)` `samples=1, weight=2, sum=0`, and `raw_measures` has no rows for that day.
  2. `TestMeasuresViewsSameAcrossRollup`: seed samples with `$browser`, `$device`, `$path` set and one declared custom key `endpoint`; with `attrs = []string{"endpoint", "$path"}` and `topN = 2`, snapshot `SELECT * FROM v_measures_daily ORDER BY 1,2,3,4,5` and `SELECT * FROM v_measures_attrs ORDER BY 1,2,3,4,5,6,7` (the live half needs the project's declared attributes: set them with the registry helper the product parity test `TestProductAttrsDeclaredSystemKeysAcrossBoundary` uses, and set meta `product_attributes_top_n` to `2`), roll the day up, snapshot again, require equality. Seed 3 distinct `$path` values with counts 3, 2, 1 so `(other)` appears.
  3. `TestMeasurePercentilesWithinTwoPercent`: for three seeded distributions (uniform 1–5000, log-normal via `math.Exp(rand.NormFloat64()*1+6)`, and 50% zeros + uniform), write 2000 samples each under distinct names, compute p50/p75/p95 with the spec's SQL pattern (decision 19, using `?` args), and compare to exact sorted-slice quantiles: `|got-exact| <= 0.02*exact + 1e-9`; for the zero-heavy set p50 must be exactly 0.
  4. `TestAllZeroMetricPercentileIsZero`: 10 samples of 0; p50/p75/p95 all `0` (Review Focus 4).

- [ ] **Step 2: Run to see them fail.**

- [ ] **Step 3: Implement** `aggregate_measures.go`, following `AggregateProductDay`/`rollupProduct`/`rollupAttrValue` (`aggregate_product.go`) closely:

```go
// AggregateMeasureDay rolls one raw day of measures into histograms, then
// deletes the day's raw rows, in one transaction. Dimensions and the top-N
// cap follow the product rules: the system attributes always, plus the
// project's declared keys.
func (d *DB) AggregateMeasureDay(ctx context.Context, projectID int64, day civil.Date, attrs []string, topN int) error {
	if topN <= 0 {
		topN = defaultAttrsTopN
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM raw_measures WHERE project_id=? AND day=?`,
			projectID, day.String()).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if err := d.rollupMeasures(ctx, tx, projectID, day, attrs, topN); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM events WHERE family='measures' AND project_id=? AND day=?`, projectID, day.String())
		return err
	})
}
```
`rollupMeasures`:
- `INSERT OR REPLACE INTO agg_measures_daily (project_id, day, event_name, measure, bucket, samples, weight, sum) SELECT project_id, ?, event_name, measure, bucket, COUNT(*), SUM(1.0/sample_rate), SUM(value/sample_rate) FROM raw_measures WHERE project_id=? AND day=? GROUP BY event_name, measure, bucket`.
- `SELECT DISTINCT event_name, measure FROM raw_measures WHERE project_id=? AND day=?`.
- For each (event, measure) and each declared key (same `expr`/`present` resolution as `rollupProduct`: `json_extract(attributes, :path)` / `IS NOT NULL` for custom keys, `store.DeclarableAttributes[key]` column / `<> ''` for `$` keys, skip unknown `$` keys) and then each `store.SystemAttributes` entry, call `rollupMeasureAttr(ctx, tx, expr, present, named)` with named params `:p, :day, :event, :measure, :key, :n` (+ `:path`).
- `rollupMeasureAttr` runs two statements:

```sql
-- kept values
WITH counted AS (
  SELECT {expr} AS v, COUNT(*) AS c FROM raw_measures
  WHERE project_id=:p AND day=:day AND event_name=:event AND measure=:measure AND {present}
  GROUP BY v),
ranked AS (SELECT v, ROW_NUMBER() OVER (ORDER BY c DESC, v) AS rn FROM counted)
INSERT OR REPLACE INTO agg_measures_attrs
  (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum)
SELECT :p, :day, :event, :measure, :key, {expr}, bucket, COUNT(*), SUM(1.0/sample_rate), SUM(value/sample_rate)
FROM raw_measures
WHERE project_id=:p AND day=:day AND event_name=:event AND measure=:measure AND {present}
  AND {expr} IN (SELECT v FROM ranked WHERE rn <= :n)
GROUP BY {expr}, bucket;
-- tail
WITH counted AS (...same...), ranked AS (...same...)
INSERT OR REPLACE INTO agg_measures_attrs (...)
SELECT :p, :day, :event, :measure, :key, '(other)', bucket, COUNT(*), SUM(1.0/sample_rate), SUM(value/sample_rate)
FROM raw_measures
WHERE project_id=:p AND day=:day AND event_name=:event AND measure=:measure AND {present}
  AND {expr} NOT IN (SELECT v FROM ranked WHERE rn <= :n)
GROUP BY bucket;
```
Note: `json_extract` returns numbers for numeric JSON; the live view must group on the same expression so parity holds (the parity test covers it). Read only `raw_measures` in SELECTs (`rawreads_test.go` rejects `FROM events` outside a DELETE).

- [ ] **Step 4: Run** `go test ./internal/store/sqlite/ -run 'Measure|RawTable|Prune'`. PASS.

- [ ] **Step 5: Commit** `feat(store): roll measures up into daily histograms`.

### Task 5: The daily pass

**Files:** Modify `internal/jobs/jobs.go` (interface l.28–45, loop after the product block), `internal/jobs/jobs_test.go`, `internal/jobs/errors_test.go` (fake gains the two methods).

**Interfaces:** Consumes `MeasureDaysBefore`, `AggregateMeasureDay`. Measures are **not** added to `allRawDays` (spec decision 17).

- [ ] **Step 1: Failing test** `TestRunDailyPassRollsUpMeasures` in `jobs_test.go`: with `jobsVars` (raw window 7), write a measure 10 days old and one 3 days old via the store; run the pass; assert `agg_measures_daily` has the old day and `raw_measures` still has the recent row. Add `TestMeasuresDoNotCreateActors`: a measure alone (no view or product event) from actor `srv` on a day past the window; after the pass `SELECT COUNT(*) FROM actors WHERE actor_id='srv'` is 0.

- [ ] **Step 2: Run to see it fail.**

- [ ] **Step 3: Implement.** Add to `jobs.Store`:
```go
MeasureDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
AggregateMeasureDay(ctx context.Context, projectID int64, day civil.Date, attrs []string, topN int) error
```
After the product loop:
```go
measureDays, err := r.store.MeasureDaysBefore(ctx, id, today.AddDays(-ret.Events.RawDays))
if err != nil {
	return err
}
for _, day := range measureDays {
	if err := r.store.AggregateMeasureDay(ctx, id, day, attrs, r.topN); err != nil {
		r.logger.Error("aggregate measures failed", "project", id, "day", day.String(), "error", err)
	}
}
```
Add the two methods to the `errors_test.go` fake and a `TestRunDailyPassLogsAggregateMeasuresFailure` mirroring the product failure test there.

- [ ] **Step 4: Run** `go test ./internal/jobs/`. PASS. **Commit** `feat(jobs): roll up measures in the daily pass`.

### Task 6: Ingest

**Files:**
- Modify: `internal/server/ingest.go` (`rawEvent` l.67–72, `resolved`, `reservedKeys`, new helpers), `internal/server/handlers.go` (l.106–219)
- Modify: `internal/server/server_test.go` (`fakeQueue` l.24–39 keeps measures separately), `internal/server/ingest_test.go`
- Modify docs: `docs/twillingate.md` (l.483–520 event model table, l.629–707 envelope, "An event is…" line 656, reserved event names l.668–679, reserved attribute keys l.681–707)

**Interfaces:**
- Produces: wire fields `family`, `value`, `measure`; reserved key `$sample_rate`; `func resolveFamily(ev rawEvent) (family store.Family, name string, warn, reject string)`; `func parseValue(raw json.RawMessage) (float64, bool)`; `func parseSampleRate(raw string) (rate float64, bad bool)`.

- [ ] **Step 1: Failing tests** (`server_test.go`, table-driven like `TestConsentParsing`):

```go
func TestFamilyRules(t *testing.T) {
	cases := []struct {
		name, event string
		wantFamily  store.Family // "" = rejected
		reject      string       // substring of the rejection reason
		warn        string       // substring of a warning
	}{
		{"view without family", `{"name":"$page_view","attributes":{"$path":"/"}}`, store.FamilyViews, "", ""},
		{"product without family", `{"name":"signup"}`, store.FamilyProduct, "", ""},
		{"explicit product", `{"family":"product","name":"signup"}`, store.FamilyProduct, "", ""},
		{"explicit views", `{"family":"views","name":"$screen_view","attributes":{"$screen":"/s"}}`, store.FamilyViews, "", ""},
		{"views with a product name", `{"family":"views","name":"signup"}`, "", "family views", ""},
		{"product with a view name", `{"family":"product","name":"$page_view","attributes":{"$path":"/"}}`, "", "is a view", ""},
		{"unknown family", `{"family":"logs","name":"x"}`, "", "unknown family", ""},
		{"measure", `{"family":"measures","name":"checkout_api","value":340,"measure":"time"}`, store.FamilyMeasures, "", ""},
		{"measure zero", `{"family":"measures","name":"q","value":0,"measure":"number"}`, store.FamilyMeasures, "", ""},
		{"measure without value", `{"family":"measures","name":"q","measure":"number"}`, "", "value", ""},
		{"measure null value", `{"family":"measures","name":"q","value":null,"measure":"number"}`, "", "value", ""},
		{"measure string value", `{"family":"measures","name":"q","value":"340","measure":"time"}`, "", "value", ""},
		{"measure bool value", `{"family":"measures","name":"q","value":true,"measure":"time"}`, "", "value", ""},
		{"measure negative", `{"family":"measures","name":"q","value":-1,"measure":"time"}`, "", "value", ""},
		{"measure without kind", `{"family":"measures","name":"q","value":1}`, "", "measure", ""},
		{"measure unknown kind", `{"family":"measures","name":"q","value":1,"measure":"seconds"}`, "", "measure", ""},
		{"lcp as time", `{"family":"measures","name":"$lcp","value":1200,"measure":"time"}`, store.FamilyMeasures, "", ""},
		{"cls as time", `{"family":"measures","name":"$cls","value":0.1,"measure":"time"}`, "", "$cls", ""},
		{"future metric", `{"family":"measures","name":"$tbt","value":10,"measure":"time"}`, store.FamilyMeasures, "", "unknown reserved metric"},
		{"value on product", `{"name":"signup","value":3,"measure":"number"}`, store.FamilyProduct, "", "value"},
	}
	// for each: fresh testServer, post envelopeOf(c.event), decodeResult;
	// assert accepted/rejected, the stored family via the fake queue, and
	// that a reason/warning containing the substring exists when expected.
}

func TestMissingFamilyNeverInfersMeasures(t *testing.T) {
	// {"name":"$lcp","attributes":{"$path":"/"}} with no family: stored as
	// product, with the "unknown reserved name" warning. Also with
	// "value":1200,"measure":"time" present: still product, plus the
	// value-ignored warning.
}

func TestSampleRateOnlyOnMeasures(t *testing.T) {
	// Batch attributes {"$sample_rate":0.1}; events: one measure, one product
	// event, one view. The measure stores SampleRate 0.1; the product event and
	// the view store 1 (SampleRate 0 or 1 in the fake — assert the written row
	// via the fake's captured store.Event) and each gets a warning containing
	// "$sample_rate".
}

func TestSampleRateParsing(t *testing.T) {
	// "0.2" -> 0.2; "1" -> 1; absent -> 1 no warning; "0", "-0.5", "1.5",
	// "abc", true -> 1 with a warning containing "$sample_rate".
}
```
Write out the loops fully in the test file (use `testServer`, `post`, `envelopeOf`, `decodeResult`).

- [ ] **Step 2: Run to see them fail.**

- [ ] **Step 3: Implement.**

`ingest.go`:
```go
type rawEvent struct {
	ID         string          `json:"id"`
	TS         string          `json:"ts"`
	Family     string          `json:"family"`
	Name       string          `json:"name"`
	Value      json.RawMessage `json:"value"`
	Measure    string          `json:"measure"`
	Attributes map[string]any  `json:"attributes"`
}
```
Add `sampleRateRaw string` to `resolved` and `"$sample_rate": func(r *resolved, v string) { r.sampleRateRaw = v },` to `reservedKeys` (keep that exact form: `docs_sync_test` extracts keys with a regexp).

```go
// parseValue reads a measure's value: a JSON number, finite and >= 0.
// Absent, null, a string or a boolean is not a value.
func parseValue(raw json.RawMessage) (float64, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s[0] == '"' || s == "true" || s == "false" {
		return 0, false
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, false
	}
	return v, true
}

// parseSampleRate reads $sample_rate: a number in (0, 1]. Absent means 1;
// anything else is stored as 1 and reported.
func parseSampleRate(raw string) (rate float64, bad bool) {
	if raw == "" {
		return 1, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || !(v > 0 && v <= 1) {
		return 1, true
	}
	return v, false
}

// resolveFamily decides an event's family and stored name. An explicit
// family is checked, never overridden; a missing one keeps the old rule
// (view names are views, everything else product), so a measure only
// exists when the event declares it.
func resolveFamily(ev rawEvent) (family store.Family, name, warn, reject string) {
	_, isView := viewName(ev.Name)
	switch ev.Family {
	case "":
		if isView {
			return store.FamilyViews, canonicalViewName(ev.Name), "", ""
		}
		if strings.HasPrefix(ev.Name, "$") {
			warn = fmt.Sprintf("unknown reserved name %s, stored as a custom event", ev.Name)
		}
		return store.FamilyProduct, ev.Name, warn, ""
	case string(store.FamilyViews):
		if !isView {
			return "", "", "", fmt.Sprintf("family views requires %s or %s", namePageView, nameScreenView)
		}
		return store.FamilyViews, canonicalViewName(ev.Name), "", ""
	case string(store.FamilyProduct):
		if isView {
			return "", "", "", fmt.Sprintf("%s is a view: send family views or omit family", ev.Name)
		}
		if strings.HasPrefix(ev.Name, "$") {
			warn = fmt.Sprintf("unknown reserved name %s, stored as a custom event", ev.Name)
		}
		return store.FamilyProduct, ev.Name, warn, ""
	case string(store.FamilyMeasures):
		if want, reserved := store.ReservedMetrics[ev.Name]; reserved && ev.Measure != want {
			return "", "", "", fmt.Sprintf("%s requires measure %s", ev.Name, want)
		} else if !reserved && strings.HasPrefix(ev.Name, "$") {
			warn = fmt.Sprintf("unknown reserved metric %s, stored", ev.Name)
		}
		return store.FamilyMeasures, ev.Name, warn, ""
	}
	return "", "", "", fmt.Sprintf("unknown family %q", ev.Family)
}
```
In `handlers.go` replace the routing block (l.141–147) with `resolveFamily`: on `reject != ""` → `res.reject(i, "%s", reject); continue`; on `warn != ""` → `res.warn(i, "%s", warn)`. Then:
```go
var value *float64
sampleRate := 1.0
measure := ""
if family == store.FamilyMeasures {
	v, ok := parseValue(ev.Value)
	if !ok {
		res.reject(i, "measures require a value: a number >= 0")
		continue
	}
	if !slices.Contains(store.MeasureKinds, ev.Measure) {
		res.reject(i, "measures require measure: time, size or number")
		continue
	}
	value, measure = &v, ev.Measure
	rate, bad := parseSampleRate(rv.sampleRateRaw)
	if bad {
		res.warn(i, "$sample_rate %q is not in (0, 1], stored as 1", rv.sampleRateRaw)
	}
	sampleRate = rate
} else {
	if len(ev.Value) > 0 {
		res.warn(i, "value is only read on the measures family, ignored")
	}
	if ev.Measure != "" {
		res.warn(i, "measure is only read on the measures family, ignored")
	}
	if rv.sampleRateRaw != "" {
		res.warn(i, "$sample_rate is only read on the measures family, ignored")
	}
}
```
(Keep the `ev.Name == ""` check before this; the view `$path` requirement stays for views only; `isView` for the path check becomes `family == store.FamilyViews`.) Set `Value: value, Measure: measure, SampleRate: sampleRate` on the `store.Event`. Extend `fakeQueue` so measures are captured (e.g. a `measures []store.Event` slice).

- [ ] **Step 4: Docs, same commit.** `docs/twillingate.md`:
  - Event-model table (l.488–492): add a `measures` row: `| any name, with `family: "measures"` | measures | — | `measures`, the Web Vitals and Measures dashboards |`, and one paragraph "### Measures" after "### Product (everything else)": a measure is a name, a number and a time; the three kinds and units; the reserved metrics table (name, metric, measure); `$sample_rate`; "a measure only exists when the event says `family: "measures"`".
  - Envelope example (l.629–656): add a measure event `{ "id": "018f1e5f-…", "ts": "2026-08-30T10:00:10Z", "family": "measures", "name": "checkout_api", "value": 340, "measure": "time", "attributes": { "$sample_rate": 0.1 } }` and change line 656 to "An event is `{id, ts, family, name, value, measure, attributes}`; `family` is optional for views and product events, and `value` and `measure` belong to measures only."
  - Replace "### Reserved event names" with "### Families and reserved names": a table `| family | name | Requires | When family is omitted |` covering views, product, measures, the omitted-family default, the per-event rejection rules (unknown family, contradictions, value/measure validity, reserved metric kind), and "an older server ignores `family`, `value` and `measure` and stores the event as a product event: upgrade the server before sending measures".
  - "### Reserved attribute keys": add a row `| Sampling | `$sample_rate` | on Web Vitals when `data-vitals` is below 1 |` and one sentence: `$sample_rate` is a number in (0, 1], read on measures only; each stored row counts as 1/rate.

- [ ] **Step 5: Run** `go test ./internal/server/ ./internal/api/ -run 'Family|Sample|Reserved|Document|Clamp|Consent|Rejection'`. PASS. **Commit** `feat(server): accept measures with a family, a value and a kind`.

### Task 7: The `measures` tool and route

**Files:**
- Create: `internal/api/ops_measures.go`, `internal/api/ops_measures_test.go`
- Modify: `internal/api/ops_read.go` (`register`), `internal/api/openapi.go` (`tagOf` l.185–196), `internal/api/resources.go` (`schemaViews`), `internal/api/expose_test.go` (33 → 34), `internal/api/rest_test.go` (`TestRESTMatchesMCP` row), `internal/api/seed_test.go` (seed measures)
- Modify docs: `docs/twillingate.md` (tool count sentence l.776 "thirty-four tools: the eighteen below…", tool table row, HTTP API route row, the views prose in "Writing SQL against the views" including `v_events_flat` now holding every family)

**Interfaces:**
- Produces: tool `measures`, route `GET /api/projects/{project_id}/measures`, input `measuresIn{rangeIn; Name string `json:"name,omitempty"`; AttrKey string `json:"attr_key,omitempty"`}`, output `tableOut` with columns `event_name, measure, [attr_value,] samples, est_count, mean, p50, p75, p95`.

- [ ] **Step 1: Failing tests** in `ops_measures_test.go` using `newTestHost` and `callTool`:
  - seeded metric `checkout_api` (time) and `$cls` (number, all zeros) exist in the host template (add rows to `seed_test.go`: raw measures through `WriteEvents` for project 1 on a day inside the seeded range, plus a few `agg_measures_daily` and `agg_measures_attrs` rows with `attr_key='$browser'`);
  - `measures` over the range returns one row per (event_name, measure); `samples` and `est_count` match the seeded totals; `p75` of `$cls` is `0`;
  - `name` filters to one metric; `attr_key: "$browser"` adds `attr_value` and returns one row per browser;
  - an unknown project is `ErrNotFound`; a bad date is `ErrInvalid`.

- [ ] **Step 2: Run to see them fail.**

- [ ] **Step 3: Implement** `ops_measures.go`:

```go
type measuresIn struct {
	rangeIn
	Name    string `json:"name,omitempty" jsonschema:"filter to one metric name"`
	AttrKey string `json:"attr_key,omitempty" jsonschema:"break each metric down by this attribute key: a system key such as $browser, or a key the project declares"`
}

// measures answers percentiles from the log-bucket histograms in
// v_measures_daily (or v_measures_attrs when attr_key is set): each bucket's
// weight is the estimated true count, so sampled rows count at full size.
func (h *host) measures(ctx context.Context, in measuresIn) (tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return tableOut{}, err
	}
	src, dim, by := "v_measures_daily", "", ""
	args := []any{in.ProjectID, in.From, in.To}
	if in.AttrKey != "" {
		src, dim, by = "v_measures_attrs", " AND attr_key = ?", "attr_value, "
		args = append(args, in.AttrKey)
	}
	if in.Name != "" {
		dim += " AND event_name = ?"
		args = append(args, in.Name)
	}
	q := `WITH h AS (
	  SELECT event_name, measure, ` + by + `bucket, approx_value,
	         SUM(samples) AS s, SUM(weight) AS w, SUM(sum) AS total
	  FROM ` + src + ` WHERE project_id = ? AND day BETWEEN ? AND ?` + dim + `
	  GROUP BY event_name, measure, ` + by + `bucket, approx_value),
	c AS (
	  SELECT *, SUM(w) OVER (PARTITION BY event_name, measure` + strings.TrimSuffix(", "+by, ", ") + ` ORDER BY bucket) AS run,
	            SUM(w) OVER (PARTITION BY event_name, measure` + strings.TrimSuffix(", "+by, ", ") + `) AS all_w
	  FROM h)
	SELECT event_name, measure, ` + by + `SUM(s) AS samples, ROUND(SUM(w), 1) AS est_count,
	       ROUND(SUM(total) / SUM(w), 3) AS mean,
	       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.50 * all_w), 3) AS p50,
	       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.75 * all_w), 3) AS p75,
	       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.95 * all_w), 3) AS p95
	FROM c GROUP BY event_name, measure` + strings.TrimSuffix(", "+by, ", ") + `
	ORDER BY event_name, measure` + strings.TrimSuffix(", "+by, ", ")
	return h.table(ctx, q, args...)
}
```
(Build the partition suffix once in a variable instead of repeating `TrimSuffix`; the query shape is what matters.) Check the project exists as `productAttributes` does (`h.reg.Snapshot(ctx).Project(in.ProjectID)`, `h.unknownProjectErr`). Register in `register` next to `product_attributes`:
```go
expose(r, spec{Name: "measures", Annotations: ro, Method: "GET", Path: p + "/measures",
	Description: "Performance measures and Web Vitals: per metric, samples, estimated count, mean and p50/p75/p95 from log-scale histograms (about ±2%). time is milliseconds, size bytes, number unitless. attr_key breaks each metric down by one attribute."},
	h.measures)
```
`openapi.go` `tagOf`: add `"measures"` to the analytics case. `resources.go` `schemaViews`, after the `v_product_attrs` line:
```
  v_measures_daily(project_id, day, event_name, measure, bucket, approx_value, samples, weight, sum)  -- measure: 'time' (ms)|'size' (bytes)|'number'; weight = estimated count; percentile: order by bucket, first bucket whose running weight reaches q × total
  v_measures_attrs(project_id, day, event_name, measure, attr_key, attr_value, bucket, approx_value, samples, weight, sum)  -- same keys and top-N as v_product_attrs
```
`expose_test.go`: 34 tools. `rest_test.go`: add `{"measures", "/api/projects/1/measures?" + rng, args}`.

- [ ] **Step 4: Docs, same commit** (`docs/twillingate.md`): count sentence; tool row `| `measures` | `name`, `attr_key` | per metric: samples, estimated count, mean, p50/p75/p95 (time in ms, size in bytes) |`; route row `| `GET` | `/api/projects/{project_id}/measures` | `measures` | query: `from`, `to`, `name`, `attr_key` |`; views prose: add `raw_measures`, `v_measures_daily`, `v_measures_attrs`, and change `v_events_flat` to "every raw row of every family".

- [ ] **Step 5: Run** `go test ./internal/api/`. PASS. **Commit** `feat(api): answer measures with percentiles`.

### Task 8: System dashboards

**Files:**
- Create: `internal/reporting/system/vitals/` (`dashboard.json` + widgets), `internal/reporting/system/measures/` (`dashboard.json` + widgets)
- Modify: `internal/reporting/system_test.go` (`want` string l.~80, `seedSystemData` measures rows), `internal/reporting/source.go:252-269` (`sampleProject` also considers `family='measures'`)
- Modify: `scripts/seed-demo.py` (measures for the `dev` profile), `web/e2e/app.spec.ts:7` (`SYSTEM_TITLES`)
- Modify docs: `docs/reporting.md` (a "### Percentiles from measures" subsection with the spec's decision-19 SQL using `:project`/`:from`/`:to`, and the two dashboards in the system dashboards list)

**Interfaces:** Dashboard ids 6 (`Web Vitals`, range `30d`) and 7 (`Measures`, range `7d`). `TestSystemDashboards` `want` becomes `"1 Views 7d, 2 Product 7d, 3 Users 7d, 4 Groups 7d, 5 Retention 90d, 6 Web Vitals 30d, 7 Measures 7d"`.

- [ ] **Step 1: Failing test.** Update `want` in `TestSystemDashboards` and seed measures in `seedSystemData`: raw rows (family `measures`, the five vitals with realistic values, `$cls` including zeros, `$browser`/`$device`/`$path` set, and one custom `checkout_api` time metric) on the three raw days, and `agg_measures_daily` + `agg_measures_attrs` (`$browser`, `$device`) rows for the 97 aggregate days via `valuesInsert` (add `cols` entries). Run → FAIL (dashboards missing).

- [ ] **Step 2: Web Vitals widgets** (`system/vitals/`). Shared p75 SQL for one vital, used by the five stats (`lcp`, `inp`, `cls`, `fcp`, `ttfb`) — each file differs only in the name literal:

```sql
WITH cur AS (
  SELECT bucket, approx_value, SUM(weight) AS w FROM v_measures_daily
  WHERE project_id = :project AND day BETWEEN :from AND :to AND event_name = '$lcp'
  GROUP BY bucket, approx_value),
prev AS (
  SELECT bucket, approx_value, SUM(weight) AS w FROM v_measures_daily
  WHERE project_id = :project AND event_name = '$lcp'
    AND day BETWEEN date(:from, '-' || (julianday(:to) - julianday(:from) + 1) || ' days') AND date(:from, '-1 day')
  GROUP BY bucket, approx_value),
c AS (SELECT approx_value, SUM(w) OVER (ORDER BY bucket) AS run, SUM(w) OVER () AS total FROM cur),
p AS (SELECT approx_value, SUM(w) OVER (ORDER BY bucket) AS run, SUM(w) OVER () AS total FROM prev)
SELECT (SELECT ROUND(MIN(approx_value) FILTER (WHERE run >= 0.75 * total), 3) FROM c) AS value,
       (SELECT ROUND(MIN(approx_value) FILTER (WHERE run >= 0.75 * total), 3) FROM p) AS previous
```
Configs: `{"component":"stat","title":"LCP p75 (ms)","props":{"format":"number"}}`; titles `INP p75 (ms)`, `CLS p75`, `FCP p75 (ms)`, `TTFB p75 (ms)`.

`ratings` (`{"component":"bar","title":"Good, needs improvement, poor","props":{"format":"percent","stacked":true,"horizontal":true}}`) — check `web/src/lib/format.ts` for the percent convention (0–1 or 0–100) and match `views/bounce-rate.sql`:
```sql
WITH t(name, good, poor) AS (VALUES ('$lcp', 2500, 4000), ('$inp', 200, 500), ('$cls', 0.1, 0.25), ('$fcp', 1800, 3000), ('$ttfb', 800, 1800)),
r AS (
  SELECT upper(substr(d.event_name, 2)) AS x,
         CASE WHEN d.approx_value <= t.good THEN 'good' WHEN d.approx_value <= t.poor THEN 'needs improvement' ELSE 'poor' END AS series,
         SUM(d.weight) AS w
  FROM v_measures_daily d JOIN t ON t.name = d.event_name
  WHERE d.project_id = :project AND d.day BETWEEN :from AND :to
  GROUP BY 1, 2)
SELECT x, series, w / SUM(w) OVER (PARTITION BY x) AS y FROM r ORDER BY x, series
```
`p75-by-day` (`line`, `format: number`, title `p75 by day (ms)`): per `day` and time vital, the first bucket whose running weight reaches 0.75 of the day's total — `x` = day, `series` = `upper(substr(event_name,2))`, `y` = p75; window `PARTITION BY day, event_name ORDER BY bucket`; names `IN ('$lcp','$inp','$fcp','$ttfb')`.
`cls-by-day` (`line`, `format: number`, title `CLS p75 by day`): the same for `$cls` only, no series.
`by-page` (`table`, title `By page (raw window)`, formats number): from `raw_measures` (typed `path` column) in range, per `path`: samples and p75 of LCP, INP, CLS as three columns (one CTE per vital joined on path, or conditional window partitions `PARTITION BY path, event_name`), ordered by LCP samples desc, `LIMIT 50`.
`by-device-browser` (`table`, title `By device and browser`): from `v_measures_attrs` where `attr_key IN ('$device','$browser')`, per `attr_key`/`attr_value`: p75 LCP, INP, CLS and samples.
`thresholds` (`markdown`, title `Reading this dashboard`): Google's thresholds table for the five vitals (spec decision 3), "p75 over the range; values from few samples are noisy; LCP, CLS and INP are reported when the page is hidden; enable collection with `data-vitals`."
`dashboard.json`: `{"id": 6, "title": "Web Vitals", "range": "30d", "layout": [5 stats 12/5 wide… ]}` — use widths that sum to 12 per row as the existing dashboards do (e.g. five stats at width 2–3, ratings 12×6, the two lines 6×8, the two tables 12×10, markdown 12×4).

- [ ] **Step 3: Measures widgets** (`system/measures/`): `metrics` (`table`, title `Metrics`): every `event_name NOT LIKE '$%'` from `v_measures_daily` in range with columns `Metric`, `Kind`, `Samples`, `Mean`, `p50`, `p75`, `p95` (same percentile pattern partitioned by event_name, measure); `p75-by-day` (`line`, series = metric). `dashboard.json`: `{"id": 7, "title": "Measures", "range": "7d", "layout": [{"widget":"metrics","width":12,"height":10},{"widget":"p75-by-day","width":12,"height":8}]}`.

- [ ] **Step 4:** `sampleProject` (`source.go`): include `family='measures'` in its candidate projects so validation samples a project with measures. `scripts/seed-demo.py`: for the `dev` profile, write measures rows (five vitals per ~30% of page views with realistic log-normal values, CLS mostly under 0.1, plus a `checkout_api` time metric) with `family='measures'`, `value`, `measure`, `sample_rate=1`, `day`. `web/e2e/app.spec.ts:7`: add `'Web Vitals', 'Measures'`.

- [ ] **Step 5: Docs** (`docs/reporting.md`): the percentile subsection (decision-19 SQL with `:project`/`:from`/`:to`, one sentence on `approx_value` and `weight`, one on `time` being milliseconds, shown with `number`), and the two dashboards wherever the system dashboards are listed.

- [ ] **Step 6: Run** `go test ./internal/reporting/ ./internal/api/`. PASS. **Commit** `feat(reporting): add Web Vitals and Measures system dashboards`.

### Task 9: SDK `measure()` and explicit families

**Files:** Modify `sdk/src/twillingate.ts` (`Event` l.117–128, `track` l.519–523, `emit` l.770–807, `page`/`screen`/tagged callers), `sdk/src/api.test.ts` (or a new `sdk/src/measure.test.ts`), `internal/api/docs_sync_test.go` (SDK symbols: add `"measure("`), `docs/twillingate.md` (Runtime API table, "### From code"), then rebuild `internal/server/twillingate.js`.

**Interfaces:**
- Produces: `type Family = "views" | "product" | "measures"`; `type MeasureKind = "time" | "size" | "number"`; `Event` gains `family: Family; value?: number; measure?: MeasureKind`; `emit(name, merged, family, m?: { value: number; measure: MeasureKind })`; public `measure(name: string, value: number, measure: MeasureKind, attrs?: Record<string, unknown>): void`.

- [ ] **Step 1: Failing vitest** (`measure.test.ts`, setup copied from `api.test.ts` l.1–50):
  - `measure("checkout_api", 340, "time", {endpoint: "/x"})` sends `{family:"measures", name:"checkout_api", value:340, measure:"time"}` with `endpoint` and `$path` in `storedEvents`;
  - `measure("q", -1, "time")`, `measure("q", NaN, "time")`, `measure("q", Infinity, "time")`, `measure("q", "3" as any, "time")`, `measure("q", 1, "seconds" as any)`, `measure("", 1, "time")` send nothing;
  - `page()` sends `family: "views"`, `track("signup")` sends `family: "product"`, a tagged element click sends `family: "product"`;
  - `measure()` before `init` is held and replayed like `track`;
  - `optOut(true)` then `measure(...)` sends nothing.

- [ ] **Step 2: Run** `cd sdk && npx vitest run src/measure.test.ts` → FAIL.

- [ ] **Step 3: Implement.** Extend `Event`; make `emit` take `family` and optional `m` and push `{ id, ts, family, name, ...(m ?? {}), attributes }`; pass `"views"` from `page`/`screen`, `"product"` from `track` and tagged events. Add:
```ts
measure(name: string, value: number, measure: MeasureKind, attrs?: Record<string, unknown>): void {
  if (!this.ready) return this.hold(() => this.measure(name, value, measure, attrs));
  if (!this.live()) return;
  if (!name || typeof value !== "number" || !isFinite(value) || value < 0 || !MEASURE_KINDS.includes(measure)) {
    this.log("measure ignored: needs a name, a number >= 0 and time, size or number", { name, value, measure });
    return;
  }
  this.emit(String(name), { ...this.eventContext(), ...expandNulls(this.defaultAttrs), ...expandNulls(attrs) }, "measures", { value, measure });
}
```
with `const MEASURE_KINDS: MeasureKind[] = ["time", "size", "number"];`. Check `this.log`'s actual signature and adapt.

- [ ] **Step 4: Docs** (`docs/twillingate.md`): Runtime API row `| `measure(name, value, measure, attrs?)` | records a measure: `measure` is `"time"` (ms), `"size"` (bytes) or `"number"`; invalid calls are dropped (logged with `debug`) |`, a `twillingate.measure(...)` line in "From code", and a sentence in "Transport" that every event now carries its `family`. Add `"measure("` to the SDK symbol list in `docs_sync_test.go`.

- [ ] **Step 5:** `cd sdk && npm run typecheck && npm test && npm run build`; `go test ./internal/server/ ./internal/api/ -run 'SDK|Document'`. **Commit** `feat(sdk): add measure() and send every event's family` (include `internal/server/twillingate.js`).

### Task 10: Web Vitals bundle and collection

**Files:**
- Create: `sdk/src/vitals-bundle.ts` (entry of the second bundle), `sdk/src/vitals-loader.ts` (in the main bundle), `sdk/src/vitals.test.ts`
- Modify: `sdk/package.json` (`web-vitals` `6.2.2` in `devDependencies`, lockfile), `sdk/build.mjs` (second build), `sdk/src/twillingate.ts` (`InitOptions.vitals`, init hook, `vital()`), `sdk/src/factory.ts` (`tagOptions`: `data-vitals`), `sdk/src/snippet.test.ts` (`optionFor` gains `vitals: "vitals"`)
- Modify: `internal/server/script.go` (embed + route), `internal/server/twillingate_script_test.go` (test for the vitals route), `.github/workflows/ci.yml` (drift check for `internal/server/twillingate-vitals.js`)
- Create (generated): `internal/server/twillingate-vitals.js`
- Modify docs: `docs/twillingate.md` ("Snippet mode" `data-vitals` row, `vitals` option in "From code", a "### Web Vitals" subsection), `internal/api/docs_sync_test.go` (SDK symbols `"data-vitals"`, `"vitals"`, `"$sample_rate"`, `"$lcp"`)

**Interfaces:**
- Consumes: `measure` plumbing from Task 9 (`emit(..., "measures", {value, measure})`), `collectorOrigin()` from `origin.ts`.
- Produces: `window.twillingateVitals: (cb: (m: { name: "LCP" | "INP" | "CLS" | "FCP" | "TTFB"; value: number }) => void) => void` (defined by the vitals bundle); `requestVitals(cb)` in `vitals-loader.ts`; `InitOptions.vitals?: number`; `parseRate(raw: string | null): number | undefined` in `factory.ts`.

- [ ] **Step 1: Failing vitest** (`vitals.test.ts`, setup from `api.test.ts`; stub `window.twillingateVitals` so no script loads; stub `Math.random`):
  - `vitals: 1`: reported LCP 1234.5 and CLS 0.05 become `{family:"measures", name:"$lcp", value:1234.5, measure:"time"}` and `{…"$cls", measure:"number"}`, each with `$sample_rate: 1` and the page-load `$path` (navigate with `history.pushState` + `page()` before the report: `$path` stays the load path);
  - `vitals: 0.2` with `Math.random` returning 0.5: nothing is sent for any metric; returning 0.1: every metric is sent with `$sample_rate: 0.2` (all-or-none per page load);
  - no `vitals` option: `twillingateVitals` is never subscribed and no `<script>` for `twillingate-vitals.js` is added;
  - `vitals: 1` without `window.twillingateVitals`: exactly one `<script src="https://collector.example.com/js/twillingate-vitals.js">` is appended, even with two instances;
  - a metric reported while `document.visibilityState === "hidden"` is sent immediately through `navigator.sendBeacon` (stub it, as `consent.test.ts:173-220` does), not by the timer (Review Focus 5);
  - two instances with rates 1 and 0 (0 treated as off): only the first sends;
  - `optOut(true)`: nothing is sent;
  - `tagOptions`: `data-vitals="1"` → 1, `"0.25"` → 0.25, `"0"`, `"2"`, `"x"`, absent → `undefined`.

- [ ] **Step 2: Run to see it fail.**

- [ ] **Step 3: Implement.**

`vitals-bundle.ts`:
```ts
import { onCLS, onFCP, onINP, onLCP, onTTFB, type Metric } from "web-vitals";

type Report = (m: { name: Metric["name"]; value: number }) => void;
const subscribers: Report[] = [];
const seen: { name: Metric["name"]; value: number }[] = [];
const fan = (m: Metric) => {
  const r = { name: m.name, value: m.value };
  seen.push(r);
  subscribers.forEach((s) => s(r));
};
onLCP(fan); onINP(fan); onCLS(fan); onFCP(fan); onTTFB(fan);

declare global { interface Window { twillingateVitals?: (cb: Report) => void } }
window.twillingateVitals = (cb) => {
  subscribers.push(cb);
  seen.forEach((m) => cb(m)); // a late subscriber still gets early FCP/TTFB
};
window.dispatchEvent(new Event("twillingate-vitals"));
```
(`web-vitals` reports final LCP/CLS/INP on `visibilitychange` to hidden; with default options each metric reports once per page load, and again after a back/forward-cache restore.)

`vitals-loader.ts`:
```ts
import { collectorOrigin } from "./origin";
type Report = (m: { name: string; value: number }) => void;
let loading = false;
export function requestVitals(cb: Report): void {
  if (window.twillingateVitals) return window.twillingateVitals(cb);
  window.addEventListener("twillingate-vitals", () => window.twillingateVitals?.(cb), { once: true });
  if (loading) return;
  const origin = collectorOrigin();
  if (!origin) return;
  loading = true;
  const s = document.createElement("script");
  s.src = origin + "/js/twillingate-vitals.js";
  s.async = true;
  document.head.appendChild(s);
}
export function resetVitalsLoader(): void { loading = false; } // test hook
```
In `twillingate.ts`: `InitOptions.vitals?: number` ("sample rate for Web Vitals, (0, 1]; absent or 0 = off; decided once per page load"). At the end of `init` (web kind only, after the entry pageview so the location is known): if `0 < rate <= 1` and `Math.random() < rate`, capture `const at = this.eventContext()` (host/path of the load) and `requestVitals((m) => this.vital(m, rate, at))`. Add:
```ts
private vital(m: { name: string; value: number }, rate: number, at: Record<string, unknown>): void {
  if (!this.live()) return;
  const name = VITALS[m.name];
  if (!name || typeof m.value !== "number" || !isFinite(m.value) || m.value < 0) return;
  this.emit(name.metric, { ...at, ...expandNulls(this.defaultAttrs), $sample_rate: rate }, "measures", { value: m.value, measure: name.measure });
  if (typeof document !== "undefined" && document.visibilityState === "hidden") this.drain(true);
}
```
with `const VITALS: Record<string, { metric: string; measure: MeasureKind }> = { LCP: {metric: "$lcp", measure: "time"}, INP: {metric: "$inp", measure: "time"}, CLS: {metric: "$cls", measure: "number"}, FCP: {metric: "$fcp", measure: "time"}, TTFB: {metric: "$ttfb", measure: "time"} };`. Add `"$sample_rate"` to `RESERVED_KEYS` if that list must know every reserved key (check its comment). `factory.ts`:
```ts
export function parseRate(raw: string | null): number | undefined {
  if (raw === null || raw.trim() === "") return undefined;
  const v = Number(raw);
  return v > 0 && v <= 1 ? v : undefined;
}
```
and `vitals: parseRate(script.getAttribute("data-vitals")),` in `tagOptions`.

`build.mjs`: after the existing build, a second `await build({...})` with `entryPoints: ["src/vitals-bundle.ts"]`, the same `bundle/format/target/minify/legalComments`, `outfile: "../internal/server/twillingate-vitals.js"`, and banner:
```js
"/* twillingate-vitals.js __TWILLINGATE_VERSION__ — Web Vitals for the twillingate SDK.\n * MIT License. Source: sdk/ in https://github.com/dmtrkzntsv/twillingate\n * Bundles web-vitals (https://github.com/GoogleChrome/web-vitals), Copyright Google LLC,\n * licensed under the Apache License, Version 2.0: http://www.apache.org/licenses/LICENSE-2.0 */"
```
`script.go`: `//go:embed twillingate-vitals.js` → `var vitalsScript []byte`; in `registerScript` a `GET /js/twillingate-vitals.js` handler with the same `headers(w)` and version substitution (no origin substitution). Test in `twillingate_script_test.go`: status 200, content type, `max-age=86400`, the banner contains the version and `Apache License`, and the body contains `twillingateVitals`. `ci.yml` `sdk` job: add `git diff --exit-code internal/server/twillingate-vitals.js` next to the existing drift line.

- [ ] **Step 4: Docs** (`docs/twillingate.md`): `data-vitals` row in the snippet table ("sample rate for Web Vitals, (0, 1]; absent = off"); `vitals` in "From code"; a "### Web Vitals" subsection: opt-in, what is sent (`$lcp` … as measures with `$sample_rate` and the load's `$host`/`$path`), sampling once per page load, the second script `/js/twillingate-vitals.js` loaded only when enabled (≈3.3 KB gzip), and that LCP/CLS/INP arrive when the page is hidden. Add the four symbols to `docs_sync_test.go`'s SDK list; if `"$lcp"`/`"$sample_rate"` live outside the three concatenated SDK files, extend the file list with `vitals-loader.ts`/`twillingate.ts` as needed.

- [ ] **Step 5: Run** `cd sdk && npm ci && npm run typecheck && npm test && npm run build`, then `go test ./internal/server/ ./internal/api/`. **Commit** `feat(sdk): collect Web Vitals from an opt-in second script` (include both bundles, `package-lock.json`, `ci.yml`).

### Task 11: Browser test, upgrade note, full check

**Files:** Create `web/e2e/vitals.spec.ts`; modify `deploy/UPGRADES.md`.

- [ ] **Step 1: Playwright spec** `vitals.spec.ts`:
  - issue an ingest key for project 1 through the REST API with `Authorization: Bearer e2e-token` (find the route in `docs/twillingate.md` "HTTP API", e.g. `POST /api/projects/1/keys`), and if the project restricts origins, add `http://127.0.0.1:18080` through `PATCH /api/projects/1`;
  - `page.route('http://127.0.0.1:18080/vitals-test', r => r.fulfill({contentType: 'text/html', body: '<html><body><h1>Vitals</h1><button id="b">tap</button><script src="/js/twillingate.js" data-key="KEY" data-vitals="1"></script></body></html>'}))`, then `page.goto` it;
  - collect request bodies to `/ingest/events` (`page.on('request')`);
  - click `#b`, then hide the page: `await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', {value: 'hidden', configurable: true}); document.dispatchEvent(new Event('visibilitychange')); })`;
  - `expect.poll` until the collected events include `family: "measures"` with names `$lcp`, `$fcp`, `$ttfb`, `$cls`, and `$inp`; every one has `measure` matching the reserved kind and `attributes.$sample_rate === 1` (merge batch attributes).
  Run: `cd web && npm run e2e -- vitals.spec.ts` (it builds the binary through `e2e/serve.sh`). If Chromium does not report INP for a synthetic click, press a key on the button as well; do not weaken the assertion to skip INP without noting it in the commit body.

- [ ] **Step 2: UPGRADES.md** append:
```
### Upgrading to measures (migration 024)

A third family, `measures`, stores performance numbers: `events` gains
`value`, `measure` and `sample_rate` (no rebuild), and two aggregate tables
and two views appear. Nothing existing changes its answer.

What changes on the day:

- Two system dashboards appear, Web Vitals and Measures; they stay empty
  until a site sets `data-vitals` or a client sends `family: "measures"`.
- `v_events_flat` has three more columns: `value`, `measure`, `sample_rate`.
- The served SDK sends `family` on every event. Purge a CDN copy of
  `/js/twillingate.js` if one sits in front of the collector.
- Backends and self-hosted SDK copies must reach an upgraded server before
  sending measures: an older one stores them as product events.
```

- [ ] **Step 3: Full check.** `make check` (one at a time), `cd sdk && npm test`, `cd web && npm run typecheck && npm test && npm run e2e`. All green.

- [ ] **Step 4: Commit** `test(web): check Web Vitals reach the collector from a real browser` (spec + UPGRADES entry; if the UPGRADES entry reads better with Task 3, it may move there).
