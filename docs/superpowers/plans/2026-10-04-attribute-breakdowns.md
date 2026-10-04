# Attribute Breakdowns Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Pick a project's attribute breakdowns from the keys it actually received, bounded by one database-wide limit, without slowing event writes.

**Architecture:** A `received_attributes` table beside `events` is counted at ingest (inside `WriteEvents`' transaction, inserted rows only) and recounted nightly with each key's busiest-partition distinct values; it follows the raw window. `manage` enforces `ATTRIBUTE_BREAKDOWNS_MAX` across active projects. A read tool `received_attributes` feeds a rebuilt project dialog (origins as rows, breakdowns as a checkbox list) and the Details section.

**Tech Stack:** Go 1.x, modernc SQLite (`internal/store/sqlite`), MCP + REST via `internal/api` `expose`, React + TanStack Query + shadcn/ui (`web/`), vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-10-04-attribute-breakdowns-design.md`

## Global Constraints

- Go is at `/usr/local/go/bin`, not on PATH: prefix commands with `export PATH=$PATH:/usr/local/go/bin &&`.
- `make check` must pass before the branch is pushed; vitest needs `--testTimeout=30000` on this machine.
- Conventional Commits, lower case, imperative, no trailing period; end every commit message with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Refusals are typed (`manage.ErrInvalid`) and matched with `errors.Is`, never by message.
- Every clickable element is a `button`, a link or `role="button"` (pointer cursor rule, `web/e2e/cursor.spec.ts`); no `cursor-pointer` classes.
- Docs update in the same commit as the change (CLAUDE.md table); `internal/api/docs_sync_test.go` binds env vars, tool names and routes to the docs.
- Setting name `ATTRIBUTE_BREAKDOWNS_MAX`, default 50, 0 = no limit. Values cap is `ATTRIBUTE_VALUES_TOP_N` (meta/stat key `attributes_top_n`).
- Ingest overhead budget: ≤ 10% on `BenchmarkWriteEvents/attrs=5` versus the commit before counting.
- No new third-party Go dependency.

## Review Focus

- **A retried batch (same event ids) must not double-count keys** — expect the counts unchanged after writing the same batch twice. (Task 2 test.)
- **An archived project gaining keys must not dodge the limit** — adding keys to an archived project counts it as active for the check, so archive → add → restore cannot exceed the limit. (Task 4 test.)
- **A server already over the limit can still save a project without adding keys** (rename, origins, removing keys) — expect success. (Task 4 test.)
- **A key with a `"` or `.` in its name** (allowed in JSON) is counted and listed verbatim, not split or dropped. (Task 2 test.)
- **The dialog's budget counts its own unsaved edits** — unchecking a selected key frees a slot before Save; Save is disabled with the reason when over. (Task 6 test.)

---

### Task 1: Benchmark event writes (baseline)

**Files:**
- Modify: `internal/store/sqlite/bench_test.go` (append)

**Interfaces:**
- Produces: `BenchmarkWriteEvents` with sub-benchmarks `attrs=0`, `attrs=5`, `attrs=20`; Task 2 reruns it.

- [ ] **Step 1: Add the benchmark**

Append to `internal/store/sqlite/bench_test.go`:

```go
// BenchmarkWriteEvents writes 500-event product batches, the most one
// request carries (wire.MaxBatchEvents), with 0, 5 and 20 custom
// attributes per event, into a fresh database per sub-benchmark. Event ids
// are unique across iterations so every row is inserted, never ignored.
// It guards the write path's cost: received_attributes counting (spec D3)
// may add at most 10% to attrs=5.
func BenchmarkWriteEvents(b *testing.B) {
	for _, n := range []int{0, 5, 20} {
		b.Run(fmt.Sprintf("attrs=%d", n), func(b *testing.B) {
			db := setupBenchDB(b) // the file's helper: a fresh migrated database
			ctx := context.Background()
			start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
			batch := make([]store.Event, 500)
			b.ReportAllocs()
			b.ResetTimer()
			for it := 0; it < b.N; it++ {
				for i := range batch {
					attrs := make(map[string]string, n)
					for k := 0; k < n; k++ {
						attrs[fmt.Sprintf("key%02d", k)] = fmt.Sprintf("v%d", (i+k)%7)
					}
					batch[i] = store.Event{Family: store.FamilyProduct,
						ID: fmt.Sprintf("w-%d-%03d", it, i), ProjectID: benchProject,
						EventName: fmt.Sprintf("event-%d", i%5), TS: start, ReceivedAt: start,
						ActorID: fmt.Sprintf("actor-%03d", i%200), ActorKind: store.ActorUser,
						Path: fmt.Sprintf("/p/%d", i%20), Attributes: attrs}
				}
				if err := db.WriteEvents(ctx, batch); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
```

`setupBenchDB` is defined in this file; read it to confirm it returns a fresh migrated `*DB` and closes it on cleanup.

- [ ] **Step 2: Run it and keep the numbers**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/store/sqlite -run '^$' -bench BenchmarkWriteEvents -benchmem -count 6 | tee /tmp/claude-1000/bench-before.txt`
Expected: three sub-benchmarks report ns/op and allocs/op.

- [ ] **Step 3: Commit**

```bash
git add internal/store/sqlite/bench_test.go
git commit -m "test(store): benchmark event batch writes" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: received_attributes table, counted at ingest

**Files:**
- Create: `internal/store/sqlite/migrations/028_received_attributes.sql`
- Create: `internal/store/sqlite/received_attributes.go`
- Create: `internal/store/sqlite/received_attributes_test.go`
- Modify: `internal/store/sqlite/write.go:31-82` (`WriteEvents`)
- Modify: `internal/store/sqlite/registry.go:228-238` (`projectTables`)
- Create: `internal/store/sqlite/migration028_test.go`

**Interfaces:**
- Consumes: `store.Event` fields `Family, ProjectID, TS, Attributes, Host, Path, ReferrerSource, UTMSource, UTMMedium, UTMCampaign, OSVersion, BrowserVersion, DeviceModel`; `store.DeclarableAttributes` (map of the nine `$` keys).
- Produces: table `received_attributes(project_id, day, attr_key, events, max_values)`; `func declarableValues(e store.Event) [9]struct{ key, value string }` in package `sqlite`; Task 3 recounts the same table.

- [ ] **Step 1: Write the migration**

`internal/store/sqlite/migrations/028_received_attributes.sql`:

```sql
-- 028: received_attributes, the attribute keys each project's product
-- events and measures carried per day, beside events: counted at ingest in
-- the same transaction as the rows (events), recounted by the daily pass
-- with each key's busiest (event, measure) partition's distinct values
-- (max_values, NULL until counted). It follows the raw window: the pass
-- keeps only days whose raw rows remain. The console lists it to pick a
-- project's attribute breakdowns. Empty until the first write or pass.
CREATE TABLE received_attributes (
    project_id INTEGER NOT NULL,
    day        TEXT    NOT NULL,
    attr_key   TEXT    NOT NULL,
    events     INTEGER NOT NULL,
    max_values INTEGER,
    PRIMARY KEY (project_id, day, attr_key)
) WITHOUT ROWID;
```

- [ ] **Step 2: Write the failing tests**

`internal/store/sqlite/received_attributes_test.go`:

```go
package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// receivedRows reads received_attributes as "project/day/key=events" lines, sorted.
func receivedRows(t *testing.T, db *DB) []string {
	t.Helper()
	rows, err := db.db.Query(`SELECT project_id, day, attr_key, events FROM received_attributes`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p, n int64
		var day, key string
		if err := rows.Scan(&p, &day, &key, &n); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d/%s/%s=%d", p, day, key, n))
	}
	sort.Strings(out)
	return out
}

// Ingest counts each key once per inserted product or measure row, per
// project and day; views are not counted; a retried batch (same ids) adds
// nothing; a key with a quote or a dot is kept verbatim; the declarable
// reserved keys count from their columns when set.
func TestWriteEventsCountsReceivedAttributes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	boot := 1.0
	evs := []store.Event{
		{ID: "p1", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup", TS: ts("2026-08-02T10:00:00Z"),
			ActorID: "a", Path: "/pricing", Attributes: map[string]string{"plan": "pro", `we"ird.key`: "x"}},
		{ID: "p2", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup", TS: ts("2026-08-02T11:00:00Z"),
			ActorID: "b", Attributes: map[string]string{"plan": "free"}},
		{ID: "p3", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup", TS: ts("2026-08-03T11:00:00Z"),
			ActorID: "b", Attributes: map[string]string{"plan": "free"}},
		{ID: "m1", ProjectID: 2, Family: store.FamilyMeasures, EventName: "boot", TS: ts("2026-08-02T11:00:00Z"),
			ActorID: "c", Measure: store.MeasureTime, Value: &boot, DeviceModel: "iPhone15,2",
			Attributes: map[string]string{"plan": "pro"}},
		{ID: "v1", ProjectID: 1, Family: store.FamilyViews, TS: ts("2026-08-02T10:00:00Z"), Kind: "web",
			ActorID: "a", Path: "/", Attributes: map[string]string{"ignored": "yes"}},
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"1/2026-08-02/$path=1",
		"1/2026-08-02/plan=2",
		`1/2026-08-02/we"ird.key=1`,
		"1/2026-08-03/plan=1",
		"2/2026-08-02/$device_model=1",
		"2/2026-08-02/plan=1",
	}
	if got := receivedRows(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("after first write\n got %v\nwant %v", got, want)
	}
	// The same batch again: every row is ignored as a duplicate id.
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if got := receivedRows(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("a retried batch changed the counts\n got %v\nwant %v", got, want)
	}
}

// declarableValues covers exactly store.DeclarableAttributes' keys.
func TestDeclarableValuesMatchesTheDeclarableKeys(t *testing.T) {
	var got []string
	for _, kv := range declarableValues(store.Event{}) {
		got = append(got, kv.key)
	}
	sort.Strings(got)
	if want := store.DeclarableAttributeKeys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("declarableValues keys = %v, want %v", got, want)
	}
}
```

`internal/store/sqlite/migration028_test.go` (pins the ceiling, per the repo's migration test rule):

```go
package sqlite

import "testing"

// Migration 028 adds an empty received_attributes table keyed by project,
// day and key.
func TestMigration028AddsReceivedAttributes(t *testing.T) {
	db := newTestDBAt(t, 28)
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('received_attributes') WHERE pk > 0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("received_attributes primary key columns = %d, want 3", n)
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/store/sqlite -run 'ReceivedAttributes|DeclarableValues|Migration028|ProjectTables'`
Expected: FAIL — `undefined: declarableValues`, and `TestProjectTablesMatchesSchema` fails naming `received_attributes`.

- [ ] **Step 4: Implement**

`internal/store/sqlite/received_attributes.go`:

```go
package sqlite

import (
	"context"
	"database/sql"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// declarableValues pairs each reserved key a project may declare
// (store.DeclarableAttributes) with the event's column value; empty when
// the event did not carry it.
func declarableValues(e store.Event) [9]struct{ key, value string } {
	return [9]struct{ key, value string }{
		{"$host", e.Host}, {"$path", e.Path}, {"$referrer", e.ReferrerSource},
		{"$utm_source", e.UTMSource}, {"$utm_medium", e.UTMMedium}, {"$utm_campaign", e.UTMCampaign},
		{"$os_version", e.OSVersion}, {"$browser_version", e.BrowserVersion}, {"$device_model", e.DeviceModel},
	}
}

type receivedKey struct {
	project int64
	day     string
	key     string
}

// receivedCounts sums, over a batch's inserted product and measure rows,
// how many carried each key per project and day.
type receivedCounts map[receivedKey]int64

func (c receivedCounts) add(e store.Event, day string) {
	if e.Family == store.FamilyViews {
		return
	}
	for k := range e.Attributes {
		c[receivedKey{e.ProjectID, day, k}]++
	}
	for _, kv := range declarableValues(e) {
		if kv.value != "" {
			c[receivedKey{e.ProjectID, day, kv.key}]++
		}
	}
}

// write upserts the batch's counts: one statement per distinct
// (project, day, key), however many events carried it.
func (c receivedCounts) write(ctx context.Context, tx *sql.Tx) error {
	if len(c) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO received_attributes (project_id, day, attr_key, events)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (project_id, day, attr_key) DO UPDATE SET events = events + excluded.events`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for k, n := range c {
		if _, err := stmt.ExecContext(ctx, k.project, k.day, k.key, n); err != nil {
			return err
		}
	}
	return nil
}
```

In `write.go` `WriteEvents`, inside the `d.tx` closure: create `counts := receivedCounts{}` before the loop; replace the `stmt.ExecContext` call so its result is kept, and count only an inserted row:

```go
			day := e.TS.UTC().Format("2006-01-02")
			res, err := stmt.ExecContext(ctx, e.ID, e.ProjectID, string(e.Family), e.EventName,
				e.TS.UTC().Format(tsFormat), day,
				/* …the remaining arguments exactly as today… */)
			if err != nil {
				return fmt.Errorf("event %s: %w", e.ID, err)
			}
			// INSERT OR IGNORE: a duplicate id (a retried batch) inserts
			// nothing and so counts nothing.
			if n, err := res.RowsAffected(); err == nil && n == 1 {
				counts.add(e, day)
			}
```

and after the loop, before `return nil`: `return counts.write(ctx, tx)`. Update the function's doc comment with one sentence: "Each inserted product or measure row also counts its attribute keys into received_attributes, in the same transaction."

In `registry.go`, add `"received_attributes",` to `projectTables` after `"events",`.

- [ ] **Step 5: Run the tests**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/store/sqlite`
Expected: PASS (all, including `TestProjectTablesMatchesSchema` and every older migration test).

- [ ] **Step 6: Compare the benchmark**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/store/sqlite -run '^$' -bench BenchmarkWriteEvents -benchmem -count 6 | tee /tmp/claude-1000/bench-after.txt && go run golang.org/x/perf/cmd/benchstat@latest /tmp/claude-1000/bench-before.txt /tmp/claude-1000/bench-after.txt`
Expected: `attrs=5` sec/op delta ≤ +10%. If `benchstat` cannot be fetched, compare the medians by hand. **If attrs=5 exceeds +10%, stop and report the numbers instead of committing** — the spec's fallback (pass-only counting) is a decision for the human. Save the benchstat output for the PR.

- [ ] **Step 7: Commit**

```bash
git add internal/store/sqlite/
git commit -m "feat(store): count received attribute keys at ingest" -m "<paste the benchstat table here>" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Nightly recount with max_values, and the raw-window prune

**Files:**
- Modify: `internal/store/sqlite/received_attributes.go` (add `countReceived`)
- Modify: `internal/store/sqlite/server_stats.go:35-52` (`MeasureServerStats` calls it; doc bullet)
- Test: `internal/store/sqlite/received_attributes_test.go` (append)

**Interfaces:**
- Consumes: table from Task 2; `MeasureServerStats(ctx, now)` already runs in the daily pass and at start.
- Produces: rows for every raw day before today hold exact `events` and non-NULL `max_values`; rows for days without raw product/measure rows are gone.

- [ ] **Step 1: Write the failing test**

Append to `received_attributes_test.go`:

```go
// The pass recounts every raw day before today exactly, fills max_values
// with the busiest (event, measure) partition's distinct values, leaves
// today's ingest counts alone, and drops days whose raw rows are gone.
func TestMeasureServerStatsRecountsReceivedAttributes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var evs []store.Event
	// 2026-08-02: signup carries plan free/pro/team (3 values), upgrade
	// carries plan pro (1): max_values 3, events 4.
	for i, plan := range []string{"free", "pro", "team"} {
		evs = append(evs, store.Event{ID: fmt.Sprintf("s%d", i), ProjectID: 1, Family: store.FamilyProduct,
			EventName: "signup", TS: ts("2026-08-02T10:00:00Z"), ActorID: "a", Attributes: map[string]string{"plan": plan}})
	}
	evs = append(evs, store.Event{ID: "u1", ProjectID: 1, Family: store.FamilyProduct, EventName: "upgrade",
		TS: ts("2026-08-02T12:00:00Z"), ActorID: "a", Attributes: map[string]string{"plan": "pro"}})
	// 2026-08-04 is "today" for the pass: counted by ingest only.
	evs = append(evs, store.Event{ID: "t1", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup",
		TS: ts("2026-08-04T09:00:00Z"), ActorID: "a", Attributes: map[string]string{"plan": "pro"}})
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	// A stale row for a day with no raw rows left (rolled up): pruned.
	if _, err := db.db.Exec(`INSERT INTO received_attributes VALUES (1, '2026-07-01', 'plan', 9, 2)`); err != nil {
		t.Fatal(err)
	}
	// A wrong ingest count for a raw day: corrected.
	if _, err := db.db.Exec(`UPDATE received_attributes SET events = 99 WHERE day = '2026-08-02'`); err != nil {
		t.Fatal(err)
	}
	if err := db.MeasureServerStats(ctx, ts("2026-08-04T03:00:00Z")); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.Query(`SELECT day, attr_key, events, max_values FROM received_attributes ORDER BY day, attr_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var day, key string
		var n int64
		var mv sql.NullInt64
		if err := rows.Scan(&day, &key, &n, &mv); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s/%s=%d/%v", day, key, n, mv))
	}
	want := []string{"2026-08-02/plan=4/{3 true}", "2026-08-04/plan=1/{0 false}"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}
```

(Add `"database/sql"` to the test file's imports.)

- [ ] **Step 2: Run it to see it fail**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/store/sqlite -run TestMeasureServerStatsRecountsReceivedAttributes`
Expected: FAIL — the 2026-07-01 row survives and 2026-08-02 shows `99/{0 false}`.

- [ ] **Step 3: Implement**

Append to `received_attributes.go`:

```go
// receivedSQL lists every (project, day, family, event, measure, key,
// value) the raw product and measure rows before ?1 carry: their JSON
// attributes and the declarable reserved columns that are set.
var receivedSQL = func() string {
	cols := []struct{ key, col string }{
		{"$host", "host"}, {"$path", "path"}, {"$referrer", "referrer_source"},
		{"$utm_source", "utm_source"}, {"$utm_medium", "utm_medium"}, {"$utm_campaign", "utm_campaign"},
		{"$os_version", "os_version"}, {"$browser_version", "browser_version"}, {"$device_model", "device_model"},
	}
	q := `SELECT e.project_id, e.day, e.family, e.event_name, e.measure, j.key AS k, j.value AS v
		FROM events e, json_each(CASE WHEN json_valid(e.attributes) THEN e.attributes ELSE '{}' END) j
		WHERE e.family IN ('product', 'measures') AND e.day < ?1`
	for _, c := range cols {
		q += `
		UNION ALL SELECT project_id, day, family, event_name, measure, '` + c.key + `', ` + c.col + `
		FROM events WHERE family IN ('product', 'measures') AND day < ?1 AND ` + c.col + ` != ''`
	}
	return q
}()

// countReceived rewrites received_attributes for every day before today:
// the days whose raw product or measure rows remain get exact counts and
// max_values (the most distinct values in one (family, event, measure)
// partition, the partition ATTRIBUTE_VALUES_TOP_N caps); the rest, days
// already rolled up, are dropped. Today stays as ingest counts it.
func countReceived(ctx context.Context, tx *sql.Tx, today string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM received_attributes WHERE day < ?1`, today); err != nil {
		return fmt.Errorf("received attributes: %w", err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO received_attributes (project_id, day, attr_key, events, max_values)
		SELECT project_id, day, k, SUM(n), MAX(vals) FROM (
		  SELECT project_id, day, k, COUNT(*) AS n, COUNT(DISTINCT v) AS vals
		  FROM (`+receivedSQL+`)
		  GROUP BY project_id, day, family, event_name, measure, k
		) GROUP BY project_id, day, k`, today)
	if err != nil {
		return fmt.Errorf("received attributes: %w", err)
	}
	return nil
}
```

(Add `"fmt"` to the file's imports.) In `MeasureServerStats`, replace the final `return countAttributes(ctx, tx, day)` with:

```go
		if err := countAttributes(ctx, tx, day); err != nil {
			return err
		}
		return countReceived(ctx, tx, day)
```

and add a bullet to its doc comment: "received attribute keys, for every day before now's still raw: each key's events and its busiest partition's distinct values (received_attributes); days already rolled up are dropped, today is left to ingest."

- [ ] **Step 4: Run the package tests**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/store/sqlite ./internal/jobs`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlite/
git commit -m "feat(store): recount received attribute keys nightly with their values" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: ATTRIBUTE_BREAKDOWNS_MAX, enforced in manage

**Files:**
- Modify: `internal/config/config.go` (constant, field, parse, validate)
- Modify: `internal/config/config_test.go`
- Modify: `internal/manage/registry.go` (add `BreakdownsInUse`)
- Modify: `internal/manage/ops.go` (`Ops.BreakdownsMax`, `checkBreakdowns`, call in `create` and `UpdateProject`)
- Create: `internal/manage/breakdowns_test.go`
- Modify: `internal/app/app.go:94-104` (meta row) and `:171`, `:182` (set `BreakdownsMax`)
- Modify: `cmd/twillingate/project.go:85`
- Modify: `internal/store/store.go:181-188` (`StatCapBreakdowns`)
- Modify: `internal/store/sqlite/server_stats.go:152-170` (`recordCaps`) and its test file `server_stats_test.go` if it lists the caps
- Modify: `.env.example`, `docs/deployment.md` (env table), `deploy/UPGRADES.md` (new section)

**Interfaces:**
- Produces: `config.DefaultAttributeBreakdownsMax = 50`, `config.Config.AttributeBreakdownsMax int`; `(*manage.Snapshot).BreakdownsInUse() int`; `manage.Ops.BreakdownsMax int`; `store.StatCapBreakdowns = "attribute_breakdowns_max"`; meta key `attribute_breakdowns_max`.

- [ ] **Step 1: Write the failing manage tests**

`internal/manage/breakdowns_test.go`:

```go
package manage

import (
	"context"
	"errors"
	"testing"
)

func keys(n int, prefix string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + string(rune('a'+i))
	}
	return out
}

// ATTRIBUTE_BREAKDOWNS_MAX counts the attributes declared across active
// projects: a save that adds past it is refused, one that keeps or
// removes keys passes even over the limit, archived projects don't count
// but adding to one is checked as if it were active, and restore is never
// refused.
func TestBreakdownsLimit(t *testing.T) {
	ops, _, reg := newOps(t)
	ctx := context.Background()
	ops.BreakdownsMax = 3

	a, err := ops.CreateProject(ctx, "t", ProjectSpec{Name: "a", Attributes: keys(2, "a")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CreateProject(ctx, "t", ProjectSpec{Name: "b", Attributes: keys(2, "b")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create past the limit: err = %v, want ErrInvalid", err)
	}
	b, err := ops.CreateProject(ctx, "t", ProjectSpec{Name: "b", Attributes: keys(1, "b")})
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Snapshot(ctx).BreakdownsInUse(); got != 3 {
		t.Fatalf("in use = %d, want 3", got)
	}
	// Swapping one key for another adds one and removes one: still 3.
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: a.ID, Attributes: []string{"aa", "zz"}}); err != nil {
		t.Fatalf("swap within the limit: %v", err)
	}
	// Over the limit (lowered setting): saves that add nothing still pass.
	ops.BreakdownsMax = 1
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: a.ID, Name: "renamed"}); err != nil {
		t.Fatalf("rename over the limit: %v", err)
	}
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: a.ID, Attributes: []string{"aa"}}); err != nil {
		t.Fatalf("remove over the limit: %v", err)
	}
	// Archived projects don't count, and restore is never refused…
	if err := ops.ArchiveProject(ctx, "t", b.ID); err != nil {
		t.Fatal(err)
	}
	if got := reg.Snapshot(ctx).BreakdownsInUse(); got != 1 {
		t.Fatalf("in use after archive = %d, want 1", got)
	}
	// …but adding to an archived project is checked as if it were active.
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: b.ID, Attributes: []string{"ba", "bb"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("add to archived past the limit: err = %v, want ErrInvalid", err)
	}
	if err := ops.RestoreProject(ctx, "t", b.ID); err != nil {
		t.Fatalf("restore over the limit: %v", err)
	}
	// 0 is no limit.
	ops.BreakdownsMax = 0
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: a.ID, Attributes: keys(10, "a")}); err != nil {
		t.Fatalf("no limit: %v", err)
	}
}
```

Add to `config_test.go` a case asserting the default (`c.AttributeBreakdownsMax == 50` beside the caps' defaults at line 44), the override (`"ATTRIBUTE_BREAKDOWNS_MAX": "7"` → 7 next to line 68-75), and a negative-value refusal (`"negative breakdowns max": base(map[string]string{"ATTRIBUTE_BREAKDOWNS_MAX": "-1"})` next to line 111).

- [ ] **Step 2: Run them to see them fail**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/manage ./internal/config`
Expected: FAIL to compile — `ops.BreakdownsMax undefined`, `AttributeBreakdownsMax undefined`.

- [ ] **Step 3: Implement config**

In `config.go`: add to the caps' defaults block `DefaultAttributeBreakdownsMax = 50` with the comment line "ATTRIBUTE_BREAKDOWNS_MAX: the attributes all active projects may declare together; each is a breakdown with its own aggregate rows." Add field `AttributeBreakdownsMax int` beside `AttributeValuesTopN`, parse `e.num("ATTRIBUTE_BREAKDOWNS_MAX", DefaultAttributeBreakdownsMax)`, and add `{"ATTRIBUTE_BREAKDOWNS_MAX", c.AttributeBreakdownsMax}` to the non-negative loop in `validate` (its message says "0 keeps every value"; give this entry its own check instead if the shared message reads wrong: `"config: ATTRIBUTE_BREAKDOWNS_MAX must not be negative (0 is no limit): %d"`).

- [ ] **Step 4: Implement manage**

`registry.go`, after `DeclaredAttributeKeys`:

```go
// BreakdownsInUse is how many attributes the active projects declare
// together: each is an attribute breakdown, held against
// ATTRIBUTE_BREAKDOWNS_MAX. Archived projects receive nothing, so theirs
// don't count.
func (s *Snapshot) BreakdownsInUse() int {
	n := 0
	for _, p := range s.ordered {
		if !p.Archived {
			n += len(p.Attributes)
		}
	}
	return n
}
```

`ops.go`: add the field and doc to `Ops`:

```go
type Ops struct {
	Reg *Registry
	St  Store
	// BreakdownsMax is ATTRIBUTE_BREAKDOWNS_MAX: the attributes all active
	// projects may declare together; 0 is no limit. Set by the caller
	// after NewOps.
	BreakdownsMax int
}
```

and the check:

```go
// checkBreakdowns refuses a save that adds attributes when the active
// projects would then declare more than BreakdownsMax together. A save
// that adds none always passes, so a server over the limit can still be
// edited down. The project counts as active whatever its state, so
// adding to an archived project cannot dodge the limit.
func (o *Ops) checkBreakdowns(ctx context.Context, cur *Project, next []string) error {
	if o.BreakdownsMax <= 0 {
		return nil
	}
	had := map[string]bool{}
	var curKeys []string
	if cur != nil {
		curKeys = cur.Attributes
	}
	for _, k := range curKeys {
		had[k] = true
	}
	distinct := map[string]bool{}
	added := 0
	for _, k := range next {
		if distinct[k] {
			continue
		}
		distinct[k] = true
		if !had[k] {
			added++
		}
	}
	if added == 0 {
		return nil
	}
	used := o.Reg.Snapshot(ctx).BreakdownsInUse()
	others := used
	if cur != nil && !cur.Archived {
		others -= len(curKeys)
	}
	if others+len(distinct) > o.BreakdownsMax {
		return fmt.Errorf("%w: %d of %d attribute breakdowns are in use; this adds %d (ATTRIBUTE_BREAKDOWNS_MAX)",
			ErrInvalid, used, o.BreakdownsMax, added)
	}
	return nil
}
```

Call it in `create` after `spec.validate()`: `if err := o.checkBreakdowns(ctx, nil, spec.Attributes); err != nil { return nil, err }`, and in `UpdateProject` after `spec.validate()`: `if err := o.checkBreakdowns(ctx, cur, spec.Attributes); err != nil { return nil, err }`.

- [ ] **Step 5: Wire it and record it**

- `internal/app/app.go`: at both `manage.NewOps(reg, st)` sites, build it first: `ops := manage.NewOps(reg, st); ops.BreakdownsMax = cfg.AttributeBreakdownsMax` and pass `ops`. Add `{"attribute_breakdowns_max", cfg.AttributeBreakdownsMax},` to the meta loop at line ~98.
- `cmd/twillingate/project.go:85`: same two lines before returning.
- `internal/store/store.go`: add `StatCapBreakdowns = "attribute_breakdowns_max"` to the caps block.
- `internal/store/sqlite/server_stats.go` `recordCaps`: add `{store.StatCapBreakdowns, 50},` with a comment `// ATTRIBUTE_BREAKDOWNS_MAX's default`. Update any test in `server_stats_test.go` that asserts the exact set of cap rows.

- [ ] **Step 6: Docs**

- `.env.example`: after `#ATTRIBUTE_VALUES_TOP_N=50` add `#ATTRIBUTE_BREAKDOWNS_MAX=50`.
- `docs/deployment.md` env table, after the `ATTRIBUTE_VALUES_TOP_N` row:
  `| ATTRIBUTE_BREAKDOWNS_MAX | Attributes all active projects may declare together; each is a breakdown with its own aggregate rows, kept RETENTION_EVENTS_AGGREGATE_DAYS. A create or update that adds attributes past it is refused; saves that add none always pass. 0 is no limit. Default 50. |` (wrap names in backticks as the neighbouring rows do).
- `deploy/UPGRADES.md`: append a section in the style of the 026 section:

~~~markdown
### Upgrading to attribute breakdowns (migration 028)

Migration 028 adds `received_attributes`, the attribute keys each
project's product events and measures carried per day. Ingest counts
them as it writes; the first daily pass, which also runs at start,
counts the raw window's days, so the project dialog lists the keys
received in the last 30 days at once.

`ATTRIBUTE_BREAKDOWNS_MAX` (default 50) now bounds the attributes all
active projects declare together. A server already past it keeps every
declared attribute, and saving a project still works, but nothing can
add an attribute until the total is under the limit or the setting is
raised. Check the total before upgrading:

```sh
twillingate project list   # sum the attributes of the active projects
```
~~~

- [ ] **Step 7: Run the tests**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/config ./internal/manage ./internal/app ./internal/store/... ./internal/api -run 'Docs|Breakdowns|Config|Caps|ServerStats|Limits' && go build ./...`
Expected: PASS. (`internal/api` docs sync passes because the variable is in `docs/deployment.md`.)

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "feat(manage): bound the attribute breakdowns of all projects with ATTRIBUTE_BREAKDOWNS_MAX" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: The received_attributes tool, and the limit in limits

**Files:**
- Create: `internal/api/ops_received.go`
- Create: `internal/api/ops_received_test.go`
- Modify: `internal/api/ops_read.go` (register after `cap_usage`)
- Modify: `internal/api/ops_limits.go` (`settingBreakdowns`, caps entry)
- Modify: `internal/api/ops_limits_test.go` (settings list grows by one)
- Modify: `docs/twillingate.md` (tool row near `cap_usage` line ~906, route row near `/api/limits` line ~953, the `limits` row's caps list, and the project fields section about declaring attributes)

**Interfaces:**
- Consumes: table `received_attributes` (Task 2/3); `(*manage.Snapshot).BreakdownsInUse()` (Task 4); `config.Config.AttributeBreakdownsMax`, `config.DefaultAttributeBreakdownsMax` (Task 4); `h.run`, `usageRange`, `usageRangeIn`, `h.capOf`, `h.unknownProjectErr` (existing, `ops_limits.go`).
- Produces: MCP tool `received_attributes`, REST `GET /api/received-attributes?project_id=&from=&to=`, answer:

```go
type receivedKeyOut struct {
	Key       string `json:"key"`
	Events    int64  `json:"events"`
	MaxValues *int64 `json:"max_values"`
	Declared  bool   `json:"declared"`
}
type receivedOut struct {
	ProjectID      int64            `json:"project_id,omitempty"`
	From           string           `json:"from"`
	To             string           `json:"to"`
	Keys           []receivedKeyOut `json:"keys"`
	ValuesCap      int              `json:"values_cap"`
	BreakdownsUsed int              `json:"breakdowns_used"`
	BreakdownsMax  int              `json:"breakdowns_max"`
}
```

- [ ] **Step 1: Write the failing test**

`internal/api/ops_received_test.go` — model the setup on the nearest test that seeds rows through the host's store (look at `ops_limits_test.go`'s cap-usage test and `seed_test.go` for how a test writes events and declares attributes; reuse those helpers rather than inventing new ones). The test must assert:

```go
// received_attributes lists the range's received keys merged with the
// project's declared ones (events 0, max_values null when not received),
// sorted by events then key, with the values cap and the breakdown
// budget; without project_id it answers only the budget.
func TestReceivedAttributes(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	// Seed: project 1 declares "plan" and "never_sent"; received_attributes
	// holds plan (2026-10-01: 5 events, max 3; 2026-10-02: 2 events, max 4)
	// and order_id (2026-10-02: 9 events, max NULL) — insert the rows
	// directly with the test host's writable store handle.
	// …seed via the helpers the neighbouring tests use…
	out, err := h.receivedAttributes(ctx, receivedIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-10-01", To: "2026-10-02"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []receivedKeyOut{
		{Key: "order_id", Events: 9, MaxValues: nil, Declared: false},
		{Key: "plan", Events: 7, MaxValues: ptr(int64(4)), Declared: true},
		{Key: "never_sent", Events: 0, MaxValues: nil, Declared: true},
	}
	if !reflect.DeepEqual(out.Keys, want) {
		t.Errorf("keys = %+v, want %+v", out.Keys, want)
	}
	if out.ValuesCap != h.capOf(settingAttrs) || out.BreakdownsMax != h.capOf(settingBreakdowns) || out.BreakdownsUsed != 2 {
		t.Errorf("budget = %+v", out)
	}
	// Without a project: no keys, the budget only.
	none, err := h.receivedAttributes(ctx, receivedIn{})
	if err != nil || len(none.Keys) != 0 || none.BreakdownsUsed != 2 {
		t.Errorf("no project = %+v, %v", none, err)
	}
	// An unknown project is refused like every project tool.
	if _, err := h.receivedAttributes(ctx, receivedIn{ProjectID: 999}); err == nil {
		t.Error("unknown project accepted")
	}
	// The MCP tool answers the same keys.
	var mcpOut receivedOut
	if err := json.Unmarshal([]byte(textOf(callTool(t, cs, "received_attributes",
		map[string]any{"project_id": 1, "from": "2026-10-01", "to": "2026-10-02"}))), &mcpOut); err != nil {
		t.Fatal(err)
	}
	if len(mcpOut.Keys) != 3 {
		t.Errorf("MCP keys = %+v", mcpOut.Keys)
	}
}
```

Define `func ptr[T any](v T) *T { return &v }` in the test file if no such helper exists in the package. Extend `TestLimitsReportsTheLimitsInForce`'s `settings` list with `{groupCaps, "ATTRIBUTE_BREAKDOWNS_MAX", <value set in cfg>, config.DefaultAttributeBreakdownsMax}` placed after `ATTRIBUTE_VALUES_TOP_N` (set `cfg.AttributeBreakdownsMax = 12` in that test).

- [ ] **Step 2: Run to see it fail**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/api -run 'ReceivedAttributes|Limits'`
Expected: FAIL to compile — `h.receivedAttributes undefined`, `settingBreakdowns undefined`.

- [ ] **Step 3: Implement**

`ops_limits.go`: add `settingBreakdowns = "ATTRIBUTE_BREAKDOWNS_MAX"` to the settings consts, and in `limitsFrom` after the "Attribute values" entry:

```go
		setting(groupCaps, "Attribute breakdowns", settingBreakdowns, cfg.AttributeBreakdownsMax, config.DefaultAttributeBreakdownsMax, "", "no cap",
			"attributes declared across every active project, each a breakdown with its own aggregate rows; a save that adds more is refused"),
```

`ops_received.go`:

```go
package api

import (
	"context"
	"sort"
	"strconv"
	"time"
)

// ---- received_attributes ----

type receivedIn struct {
	ProjectID int64 `json:"project_id,omitempty" jsonschema:"one project; absent answers only the breakdown budget"`
	usageRangeIn
}

// (receivedKeyOut and receivedOut as in the Interfaces block)

func (h *host) receivedAttributes(ctx context.Context, in receivedIn) (receivedOut, error) {
	snap := h.reg.Snapshot(ctx)
	out := receivedOut{ProjectID: in.ProjectID, Keys: []receivedKeyOut{},
		ValuesCap: h.capOf(settingAttrs), BreakdownsUsed: snap.BreakdownsInUse(), BreakdownsMax: h.capOf(settingBreakdowns)}
	fromD, toD, err := usageRange(in.usageRangeIn, time.Now())
	if err != nil {
		return receivedOut{}, err
	}
	out.From, out.To = fromD.String(), toD.String()
	if in.ProjectID == 0 {
		return out, nil
	}
	p := snap.Project(in.ProjectID)
	if p == nil {
		return receivedOut{}, h.unknownProjectErr(ctx, in.ProjectID)
	}
	res, err := h.run(ctx, `SELECT attr_key, SUM(events), MAX(max_values) FROM received_attributes
		WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY attr_key`, in.ProjectID, out.From, out.To)
	if err != nil {
		return receivedOut{}, err
	}
	declared := map[string]bool{}
	for _, k := range p.Attributes {
		declared[k] = true
	}
	seen := map[string]bool{}
	for _, r := range res.Rows {
		n, _ := strconv.ParseInt(r[1], 10, 64)
		k := receivedKeyOut{Key: r[0], Events: n, Declared: declared[r[0]]}
		if mv, err := strconv.ParseInt(r[2], 10, 64); err == nil {
			k.MaxValues = &mv
		}
		out.Keys = append(out.Keys, k)
		seen[r[0]] = true
	}
	for _, k := range p.Attributes {
		if !seen[k] {
			out.Keys = append(out.Keys, receivedKeyOut{Key: k, Declared: true})
		}
	}
	sort.SliceStable(out.Keys, func(i, j int) bool {
		a, b := out.Keys[i], out.Keys[j]
		if a.Events != b.Events {
			return a.Events > b.Events
		}
		return a.Key < b.Key
	})
	return out, nil
}
```

Check how `readsql` renders a NULL cell (empty string vs `"NULL"`); `ParseInt` fails on either, which is the intent — but confirm with the test. If `h.run` cannot see `received_attributes` because `readsql` restricts tables to views, read it the way `usage` reads `server_stats` instead (find that in `ops_usage.go`) and mirror it.

`ops_read.go`, after the `cap_usage` expose:

```go
	expose(r, spec{Name: "received_attributes", Annotations: ro, Method: "GET", Path: "/api/received-attributes",
		Description: "The attribute keys a project's product events and measures carried over a range (default the last 30 days, at most 400; only the raw window is kept, RETENTION_EVENTS_RAW_DAYS): per key the events that carried it, max_values (the most distinct values one event (or measure) had on one day, which ATTRIBUTE_VALUES_TOP_N caps; null until the daily pass counts the day, so today's), and whether the project declares it as a breakdown. Declared keys not received are listed with events 0. Also values_cap (ATTRIBUTE_VALUES_TOP_N), breakdowns_used (attributes declared across active projects) and breakdowns_max (ATTRIBUTE_BREAKDOWNS_MAX, 0 = no limit). Without project_id, only the budget. Declare a breakdown with update_project's attributes."},
		h.receivedAttributes)
```

- [ ] **Step 4: Docs**

In `docs/twillingate.md`:
- Tools table (near `cap_usage`): `| received_attributes | project_id (optional), from, to (optional: the last 30 days) | The keys the project's product events and measures carried — each key's events, max_values (the busiest event's distinct values on one day, against ATTRIBUTE_VALUES_TOP_N; null for today, until the daily pass counts it) and declared — plus declared keys not received (events 0), values_cap, breakdowns_used and breakdowns_max (ATTRIBUTE_BREAKDOWNS_MAX). Kept for the raw window only. Without project_id, the budget only |` (backticks as neighbours).
- Routes table: `| GET | /api/received-attributes | received_attributes | query: project_id, from, to |`.
- The `limits` row: add `ATTRIBUTE_BREAKDOWNS_MAX` to the caps list.
- Where the doc explains declaring attributes (search for "declare" near line 106 and in the project fields section): add one paragraph — "Declaring an attribute makes it a breakdown: the daily rollup keeps per-value rows for it, so `product_attributes` can break events down by it. `received_attributes` lists the keys events actually carry to choose from. `ATTRIBUTE_BREAKDOWNS_MAX` (default 50) bounds the attributes all active projects declare together; a create or update that adds attributes past it is refused (`invalid`), while a save that adds none always passes."

- [ ] **Step 5: Run the tests**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/api`
Expected: PASS, including the docs sync and OpenAPI tests.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(api): list the attribute keys a project received, with the breakdown budget" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: The project dialog and Details

**Files:**
- Modify: `web/src/lib/api.ts` (types + endpoint), `web/src/lib/queries.ts` (query), `web/src/hooks/use-project-actions.ts` (`PROJECT_WRITE` adds `'received-attributes'`)
- Create: `web/src/components/projects/OriginsField.tsx` + `OriginsField.test.tsx`
- Create: `web/src/components/projects/BreakdownsField.tsx` + `BreakdownsField.test.tsx`
- Modify: `web/src/components/projects/ProjectFormDialog.tsx` (use both; `projectId?` prop; Save disabled with a reason)
- Delete: `web/src/components/projects/ChipsInput.tsx`, `ChipsInput.test.tsx` (no other user — confirm with `grep -rn ChipsInput web/src`)
- Modify: `web/src/components/projects/DetailsSection.tsx` (origins one per line; Breakdowns with counts; `range` prop)
- Modify: `web/src/pages/Project.tsx` (pass `projectId` to the edit dialog via DetailsSection, `range` to DetailsSection)
- Modify: `web/src/components/projects/UsageSection.tsx` + test (drop the "Declared but not sent" line)
- Test: `web/src/components/projects/DetailsSection.test.tsx` (create if absent)

**Interfaces:**
- Consumes: `GET /api/received-attributes` answer (Task 5).
- Produces:

```ts
// api.ts
export interface ReceivedKey { key: string; events: number; max_values: number | null; declared: boolean }
export interface ReceivedAttributes {
  project_id?: number; from: string; to: string; keys: ReceivedKey[]
  values_cap: number; breakdowns_used: number; breakdowns_max: number
}
// endpoints
receivedAttributes: (q: RangeQuery & { project_id?: number }) =>
  api<ReceivedAttributes>(`/api/received-attributes${toQuery(q)}`),
// queries.ts
export const receivedAttributesQuery = (q: RangeQuery & { project_id?: number }) => ({
  queryKey: ['received-attributes', q.project_id ?? 'none', q.from ?? '', q.to ?? ''],
  queryFn: () => endpoints.receivedAttributes(q),
})
// OriginsField
export default function OriginsField(props: { value: string[]; onChange: (next: string[]) => void }): JSX.Element
// BreakdownsField
export default function BreakdownsField(props: {
  projectId?: number
  /** The keys the project declares now (what Save would replace). */
  initial: string[]
  value: string[]
  onChange: (next: string[]) => void
  /** Null when the selection fits the budget, else why Save is disabled. */
  onProblem: (problem: string | null) => void
}): JSX.Element
// pure helper, exported for tests
export function budget(used: number, max: number, initial: string[], value: string[]): { used: number; over: boolean }
```

- [ ] **Step 1: Write the failing tests**

`OriginsField.test.tsx`:

```tsx
import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import OriginsField from './OriginsField'

describe('OriginsField', () => {
  it('edits origins as one row each, adds and removes rows', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<OriginsField value={['https://a.example', '*']} onChange={onChange} />)
    expect(screen.getAllByRole('textbox', { name: /Origin \d/ })).toHaveLength(2)
    await user.click(screen.getByRole('button', { name: 'Remove origin 1' }))
    expect(onChange).toHaveBeenLastCalledWith(['*'])
    await user.click(screen.getByRole('button', { name: 'Add origin' }))
    expect(onChange).toHaveBeenLastCalledWith(['https://a.example', '*', ''])
    expect(screen.getByText(/With none, browsers can't send/)).toBeInTheDocument()
  })
})
```

`BreakdownsField.test.tsx`:

```tsx
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { endpoints } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import BreakdownsField, { budget } from './BreakdownsField'

const answer = {
  project_id: 1, from: '2026-09-05', to: '2026-10-04', values_cap: 50, breakdowns_used: 3, breakdowns_max: 3,
  keys: [
    { key: 'order_id', events: 980, max_values: 412, declared: false },
    { key: 'plan', events: 900, max_values: 3, declared: true },
    { key: '$path', events: 900, max_values: 38, declared: false },
  ],
}

function Harness({ initial, onProblem }: { initial: string[]; onProblem: (p: string | null) => void }) {
  const [value, setValue] = useState(initial)
  return <BreakdownsField projectId={1} initial={initial} value={value} onChange={setValue} onProblem={onProblem} />
}

describe('budget', () => {
  it('counts the dialog edits against the server total', () => {
    expect(budget(3, 3, ['plan'], ['plan'])).toEqual({ used: 3, over: false })
    expect(budget(3, 3, ['plan'], ['plan', 'order_id'])).toEqual({ used: 4, over: true })
    expect(budget(3, 3, ['plan'], ['order_id'])).toEqual({ used: 3, over: false })
    expect(budget(3, 0, [], ['a', 'b'])).toEqual({ used: 5, over: false })
  })
})

describe('BreakdownsField', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.spyOn(endpoints, 'receivedAttributes').mockResolvedValue(answer)
  })

  it('lists received keys with events and values, flags a key that folds, and counts the budget', async () => {
    const user = userEvent.setup()
    const onProblem = vi.fn()
    renderWithProviders(<Harness initial={['plan']} onProblem={onProblem} />)
    expect(await screen.findByRole('checkbox', { name: /order_id/ })).not.toBeChecked()
    expect(screen.getByRole('checkbox', { name: /plan/ })).toBeChecked()
    expect(screen.getByText(/412 values · folds past 50/)).toBeInTheDocument()
    expect(screen.getByText(/3 of 3 in use/)).toBeInTheDocument()
    await user.click(screen.getByRole('checkbox', { name: /order_id/ }))
    expect(screen.getByText(/4 of 3 in use/)).toBeInTheDocument()
    expect(onProblem).toHaveBeenLastCalledWith(expect.stringMatching(/over the limit/))
    // Unchecking a selected key frees its slot before saving.
    await user.click(screen.getByRole('checkbox', { name: /plan/ }))
    expect(onProblem).toHaveBeenLastCalledWith(null)
  })

  it('adds a key not received yet, and refuses a $ key there', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Harness initial={[]} onProblem={vi.fn()} />)
    const input = await screen.findByRole('textbox', { name: 'Key not received yet' })
    await user.type(input, '$host{Enter}')
    expect(screen.getByText(/\$ keys appear in the list once received/)).toBeInTheDocument()
    await user.clear(input)
    await user.type(input, 'tier{Enter}')
    expect(screen.getByRole('checkbox', { name: /tier/ })).toBeChecked()
  })
})
```

`DetailsSection.test.tsx`: render `DetailsSection` with a project `{ project_id: 1, name: 'dev', allowed_origins: ['https://a.example', '*'], attributes: ['plan', 'never_sent'] }`, `range={{ from: '2026-09-05', to: '2026-10-04' }}`, the `receivedAttributes` spy answering `plan` (events 900, max 3, declared) and `never_sent` (events 0, null, declared); assert both origins render as separate list items (`getAllByRole('listitem')` within the origins list), `plan` shows "900 events · 3 values", and `never_sent` shows "not received".

In `UsageSection.test.tsx`, change `'shows totals, freshness, size and unused attributes'` to stop asserting the "Declared but not sent" text, and delete the `'treats null unused attributes as not computed'` case (the line no longer exists).

- [ ] **Step 2: Run them to see them fail**

Run: `cd web && npx vitest run --testTimeout=30000 src/components/projects`
Expected: FAIL — modules `./OriginsField`, `./BreakdownsField` not found.

- [ ] **Step 3: Implement the API layer**

Add the `ReceivedKey`/`ReceivedAttributes` types and `endpoints.receivedAttributes` to `api.ts`, `receivedAttributesQuery` to `queries.ts`, and `'received-attributes'` to `PROJECT_WRITE` in `use-project-actions.ts` (a save changes `declared` and the budget).

- [ ] **Step 4: Implement OriginsField**

```tsx
import { PlusIcon, XIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

/** Allowed origins, one row each: edit in place, × removes, Add origin appends an empty row. */
export default function OriginsField({ value, onChange }: { value: string[]; onChange: (next: string[]) => void }) {
  const set = (i: number, v: string) => onChange(value.map((o, j) => (j === i ? v : o)))
  return (
    <fieldset className="flex flex-col gap-1.5">
      <legend className="text-sm font-medium">Allowed origins</legend>
      {value.map((o, i) => (
        <div key={i} className="flex items-center gap-1.5">
          <Input aria-label={`Origin ${i + 1}`} value={o} placeholder="https://example.com or *" onChange={(e) => set(i, e.target.value)} />
          <Button type="button" variant="ghost" size="icon" aria-label={`Remove origin ${i + 1}`} onClick={() => onChange(value.filter((_, j) => j !== i))}>
            <XIcon />
          </Button>
        </div>
      ))}
      <Button type="button" variant="outline" size="sm" className="self-start" onClick={() => onChange([...value, ''])}>
        <PlusIcon /> Add origin
      </Button>
      <p className="text-xs text-muted-foreground">`*` allows any origin. With none, browsers can't send; native apps still can.</p>
    </fieldset>
  )
}
```

Render the `*` in the hint as `<code>*</code>` rather than backticks. `ProjectFormDialog`'s submit trims each origin and drops empty rows before calling `onSubmit`.

- [ ] **Step 5: Implement BreakdownsField**

```tsx
import { useEffect, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import type { ReceivedKey } from '@/lib/api'
import { receivedAttributesQuery } from '@/lib/queries'

const DAYS = [7, 14, 30] as const

/** The total in use if the dialog saved now: the server's, minus what it drops, plus what it adds. 0 max is no limit. */
export function budget(used: number, max: number, initial: string[], value: string[]) {
  const added = value.filter((k) => !initial.includes(k)).length
  const removed = initial.filter((k) => !value.includes(k)).length
  const next = used + added - removed
  return { used: next, over: max > 0 && added > 0 && next > max }
}

function dayString(d: Date) {
  return d.toISOString().slice(0, 10)
}

/** Breakdowns: pick from the keys the project received (7/14/30 days), plus a key not received yet. */
export default function BreakdownsField({ projectId, initial, value, onChange, onProblem }: {
  projectId?: number; initial: string[]; value: string[]; onChange: (next: string[]) => void; onProblem: (p: string | null) => void
}) {
  const [days, setDays] = useState<(typeof DAYS)[number]>(30)
  const [text, setText] = useState('')
  const [refused, setRefused] = useState<string | null>(null)
  const to = new Date()
  const from = new Date(to.getTime() - (days - 1) * 86_400_000)
  const { data } = useQuery({
    ...receivedAttributesQuery({ project_id: projectId, from: dayString(from), to: dayString(to) }),
    placeholderData: keepPreviousData,
  })
  const listed: ReceivedKey[] = [...(data?.keys ?? [])]
  for (const k of value) if (!listed.some((r) => r.key === k)) listed.push({ key: k, events: 0, max_values: null, declared: false })
  const b = data ? budget(data.breakdowns_used, data.breakdowns_max, initial, value) : null
  const problem = b?.over ? `${b.used} of ${data!.breakdowns_max} breakdowns: over the limit (ATTRIBUTE_BREAKDOWNS_MAX)` : null
  useEffect(() => onProblem(problem), [problem])
  const toggle = (k: string, on: boolean) => onChange(on ? [...value, k] : value.filter((x) => x !== k))
  const add = () => {
    const k = text.trim()
    if (!k) return
    if (k.startsWith('$')) return setRefused('$ keys appear in the list once received')
    setRefused(null)
    setText('')
    if (!value.includes(k)) onChange([...value, k])
  }
  const describe = (r: ReceivedKey) => {
    if (r.events === 0) return 'not received'
    const values = r.max_values === null ? 'values counted tonight'
      : r.max_values > (data?.values_cap ?? 0) && (data?.values_cap ?? 0) > 0
        ? `${r.max_values.toLocaleString()} values · folds past ${data!.values_cap}`
        : `${r.max_values.toLocaleString()} ${r.max_values === 1 ? 'value' : 'values'}`
    return `${r.events.toLocaleString()} events · ${values}`
  }
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="flex w-full items-center justify-between gap-2 text-sm font-medium">
        <span>
          Breakdowns
          {b && <span className="font-normal text-muted-foreground"> · {data!.breakdowns_max > 0 ? `${b.used} of ${data!.breakdowns_max} in use` : `${b.used} in use`}</span>}
        </span>
        <ToggleGroup type="single" size="sm" value={String(days)} onValueChange={(v) => v && setDays(Number(v) as (typeof DAYS)[number])}>
          {DAYS.map((d) => <ToggleGroupItem key={d} value={String(d)}>{d} days</ToggleGroupItem>)}
        </ToggleGroup>
      </legend>
      {listed.length > 0 ? (
        <ul className="flex max-h-64 flex-col gap-1 overflow-y-auto rounded-md border p-2">
          {listed.map((r) => {
            const id = `breakdown-${r.key}`
            return (
              <li key={r.key} className="flex items-center gap-2">
                <Checkbox id={id} checked={value.includes(r.key)} onCheckedChange={(c) => toggle(r.key, c === true)} />
                <Label htmlFor={id} className="flex flex-1 items-baseline justify-between gap-2 font-normal">
                  <code className="text-sm">{r.key}</code>
                  <span className="text-xs text-muted-foreground">{describe(r)}</span>
                </Label>
              </li>
            )
          })}
        </ul>
      ) : (
        <p className="text-sm text-muted-foreground">{projectId ? 'No attributes received in this range.' : 'Nothing received yet: add keys by name, or pick them once events arrive.'}</p>
      )}
      <Input
        aria-label="Key not received yet"
        placeholder="Add a key not received yet"
        value={text}
        onChange={(e) => { setText(e.target.value); setRefused(null) }}
        onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); add() } }}
      />
      {refused && <p className="text-xs text-destructive">{refused}</p>}
      {problem && <p className="text-xs text-destructive">{problem}</p>}
    </fieldset>
  )
}
```

The checkbox's accessible name must contain the key (the test finds `/order_id/`); the `Label htmlFor` gives it. Verify `ToggleGroup`'s props in `web/src/components/ui/toggle-group.tsx` (it is the shadcn Radix one: `type="single"`, `value`, `onValueChange`) and adjust if its `size` prop differs.

- [ ] **Step 6: Wire the dialog, Details, the page and Usage**

`ProjectFormDialog`: add `projectId?: number`; replace both `ChipsInput`s with `<OriginsField value={origins} onChange={setOrigins} />` and `<BreakdownsField projectId={projectId} initial={initial?.attributes ?? []} value={attributes} onChange={setAttributes} onProblem={setProblem} />`; keep `const [problem, setProblem] = useState<string | null>(null)`; submit sends `allowed_origins: origins.map((o) => o.trim()).filter(Boolean)`; the Save button is `disabled={pending || name.trim() === '' || problem !== null}` with `title={problem ?? undefined}`. Widen the dialog: `<DialogContent className="sm:max-w-xl">`. Delete `ChipsInput.tsx` and its test.

`DetailsSection`: take `range: { from: string; to: string }`; pass `projectId={project.project_id}` to its `ProjectFormDialog`; render origins as `<ul>` with one `<li>` each (or the existing "None: browsers cannot send" text); replace "Declared attributes" with "Breakdowns": a `<ul>` of the declared keys, each `<code>{key}</code>` plus the `describe`-style text from `receivedAttributesQuery({ project_id, ...range })` ("900 events · 3 values", "not received"); while loading, the keys alone. Export the `describe` logic from `BreakdownsField.tsx` as `describeKey(r: ReceivedKey, valuesCap: number): string` and use it in both places instead of duplicating it.

`Project.tsx`: pass `range={range}` to `DetailsSection`.

`UsageSection.tsx`: remove the `s.unused_attributes && …` paragraph (the API field stays).

- [ ] **Step 7: Run the web checks**

Run: `cd web && npm run typecheck && npx vitest run --testTimeout=30000`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add -A web/
git commit -m "feat(web): pick breakdowns from received attributes, and edit origins as rows" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: End to end, and the whole branch green

**Files:**
- Modify: `web/e2e/projects.spec.ts`

**Interfaces:**
- Consumes: everything above; the e2e server (`web/e2e/serve.sh`) seeds project `dev` whose product events carry `plan` (`scripts/seed-demo.py`), counted into `received_attributes` by the pass that runs at start.

- [ ] **Step 1: Update the e2e**

In `'creates a project, edits it, …'`, replace the chip interactions:

```ts
  await page.getByRole('button', { name: 'Add origin' }).click()
  await page.getByRole('textbox', { name: 'Origin 1' }).fill('https://e2e.example')
```

and in the edit step:

```ts
  await details.getByRole('button', { name: 'Edit' }).click()
  await page.getByRole('textbox', { name: 'Origin 1' }).fill('*')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(details.getByText('*', { exact: true })).toBeVisible()
```

Add a test:

```ts
test('picks a breakdown from the attributes the seeded project received', async ({ page }) => {
  await login(page)
  await page.goto('/app/projects')
  await page.getByRole('article', { name: 'dev' }).getByRole('link', { name: 'dev' }).click()
  const details = page.getByRole('region', { name: 'Details' })
  await details.getByRole('button', { name: 'Edit' }).click()
  const plan = page.getByRole('checkbox', { name: /plan/ })
  await expect(plan).toBeVisible()
  await plan.check()
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(details.getByText('plan', { exact: true })).toBeVisible()
  await expect(details.getByText(/events · \d+ values?/)).toBeVisible()
  // Leave the seed as it was for the other tests.
  await details.getByRole('button', { name: 'Edit' }).click()
  await page.getByRole('checkbox', { name: /plan/ }).uncheck()
  await page.getByRole('button', { name: 'Save' }).click()
})
```

- [ ] **Step 2: Run the e2e**

Run: `cd web && npx playwright test e2e/projects.spec.ts e2e/cursor.spec.ts`
Expected: PASS. If a stale `/tmp/twillingate-e2e-*` directory fills the tmpfs quota, remove ones older than an hour first (`find /tmp -maxdepth 1 -name 'twillingate-e2e-*' -user "$(id -u)" -mmin +60 -exec rm -rf {} +`).

- [ ] **Step 3: Full check**

Run: `export PATH=$PATH:/usr/local/go/bin && make check && cd web && npx vitest run --testTimeout=30000`
Expected: PASS, coverage gate met.

- [ ] **Step 4: Commit**

```bash
git add web/e2e/projects.spec.ts
git commit -m "test(web): pick a breakdown end to end" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
