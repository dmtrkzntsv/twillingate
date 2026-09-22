# Groups in the product attribute breakdown — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Record how many distinct groups sat behind each product attribute value — a nullable `unique_groups` on `agg_product_attrs`, carried through `v_product_attrs`, the `product_attributes` tool and both Evidence tables on the product page.

**Architecture:** Migration `016_product_attr_groups.sql` adds the column and recreates the view with `group_id` threaded through every arm; `rollupAttrValue` writes the same `COUNT(DISTINCT NULLIF(group_id,''))` in both of its statements, so the live half and the rollup agree across the aggregation boundary. NULL is reserved for days rolled up before 016 (their raw rows are gone, so the figure cannot be recovered); the live half always produces an integer, `0` included. The API and Evidence only add a column each. No SDK change, no data step, no backfill.

**Tech Stack:** Go 1.26 (`/usr/local/go/bin/go`), SQLite via modernc, Evidence markdown pages under `evidence/`, the two contract docs.

**Spec:** `docs/superpowers/specs/2026-09-21-product-attr-groups-design.md` (copied onto this branch; this plan argues from it).

## Global Constraints

- `agg_product_attrs.unique_groups INTEGER` is **nullable, and NULL means "not measured"**: exactly the rows aggregated before 016. Every other column in these aggregates is NOT NULL; the exception is deliberate and documented in the migration next to the `ALTER`.
- The live half always produces an integer: `COUNT(DISTINCT NULLIF(group_id,''))` returns `0`, not NULL, when no row carries a group. `0` is a real measurement meaning "measured, none".
- No backfill. Aggregation deletes the day's raw rows in the same transaction that writes the rollup, so history cannot be recounted.
- Ranking is untouched: `ORDER BY c DESC, attr_value` in `ranked` stays as it is, so the set of rows the view returns is identical to 015's, plus one column.
- `(other)` gets its own group count from raw (`COUNT(DISTINCT NULLIF(group_id,''))` over the whole tail), never a sum of the tail's per-value counts.
- `rollupAttrValue` stays one function serving declared keys and system dimensions alike; neither `expr` nor `present` changes.
- **No validation in the database** (user rule): the migration is structure only — one `ALTER`, one `DROP VIEW`, one `CREATE VIEW`. No entry in `dataSteps`.
- **No new snippet `data-` attributes** (user rule): nothing in this change touches `sdk/`.
- `schemaViews` in `internal/api/resources.go`, `docs/twillingate.md` and `docs/deployment.md` change in the same commits as the code they describe (CLAUDE.md).
- `unique_users` is not renamed (it counts actors; out of scope). `v_product_daily`, `v_product_totals`, `v_retention` and `v_identity_daily` are untouched.
- Implementers do not commit; the controller commits per task with the message given in the task.
- Ruling on the squash type: nothing here breaks a reader — the migration is additive, the view and the tool gain a trailing column, an older binary's 7-column `INSERT` still succeeds against the nullable column. The PR is `feat:` without `!`. The migration is still irreversible in the ordinary sense (no down migration), which the deployment page says.

---

## File structure

| File | Responsibility |
| --- | --- |
| `internal/store/sqlite/migrations/016_product_attr_groups.sql` (new) | The column and the recreated view. |
| `internal/store/sqlite/aggregate_product.go` | `rollupAttrValue` writes `unique_groups` in both statements. |
| `internal/store/sqlite/migration016_test.go` (new) | `groupsOf` helper; NULL for pre-016 rows, integer for a day measured after. |
| `internal/store/sqlite/views_test.go` | `attrRow` and `readAttrs` gain the column; `seedAttrDay` carries a mix of set and empty groups; the invariant tests cover it; the `(other)` test asserts distinct groups. |
| `internal/store/sqlite/aggregate_product_test.go` | Zero is a measurement; distinct per event; the tail. |
| `internal/api/ops_product.go` | `productAttributes` selects the column. |
| `internal/api/ops_read.go` | `product_attributes` description names the three measures. |
| `internal/api/resources.go` | `schemaViews` row for `v_product_attrs`. |
| `internal/api/seed_test.go`, `ops_product_test.go` | A measured and an unmeasured seed row; the tool output carries the column. |
| `docs/twillingate.md` | The breakdown sentence, the `(other)` sentence, the tool table row, the views paragraph. |
| `evidence/sources/twillingate/v_product_attrs.sql` | Source query and its empty-database sentinel gain the column. |
| `evidence/pages/product/[project].md` | `attr_breakdowns` and `app_version_summary` gain `min_groups` and a "Groups (at least)" column; prose names both floors. |
| `docs/deployment.md` | "Upgrading to group counts (migration 016)" subsection. |
| `docs/superpowers/specs/2026-09-21-product-attr-groups-design.md` | Status → implemented. |

Task order: 1 (migration + rollup) → 2 (boundary and rollup tests) → 3 (API + user docs) → 4 (Evidence + deployment page + spec status). Tasks 1 and 2 both edit `internal/store/sqlite` and run in sequence.

Go: `/usr/local/go/bin/go test ./internal/store/sqlite/ -run <Name> -count=1`. The full store package takes ~1 min; `make check` ~5 min warm. Evidence has no local build in the test suite; the page and source edits are reviewed by reading.

---

### Task 1: Migration 016 and the rollup

**Files:**
- Create: `internal/store/sqlite/migrations/016_product_attr_groups.sql`
- Modify: `internal/store/sqlite/aggregate_product.go` (`rollupAttrValue`, lines 158–194)
- Create: `internal/store/sqlite/migration016_test.go`

**Interfaces:**
- Consumes: `newTestDBAt(t, 15)` (migration012_test.go), `hasColumn(t, db, table, column)` (sqlite_test.go), `ts(string) time.Time` and `day(string) civil.Date` test helpers, `store.ProductEvent.GroupID`, `db.WriteProductEvents`, `db.AggregateProductDay(ctx, projectID, day, attrs, topN)`.
- Produces: `agg_product_attrs.unique_groups INTEGER NULL`; `v_product_attrs` with an eighth column `unique_groups`; `groupsOf(t, db, q, args...) sql.NullInt64` test helper (package `sqlite`, used again in Task 2).

- [ ] **Step 1: Write the failing migration test**

Create `internal/store/sqlite/migration016_test.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// groupsOf reads one row's unique_groups. Valid=false is a NULL, which
// after 016 means exactly one thing: the day was rolled up before the
// column existed.
func groupsOf(t *testing.T, db *DB, q string, args ...any) sql.NullInt64 {
	t.Helper()
	var g sql.NullInt64
	if err := db.db.QueryRowContext(context.Background(), q, args...).Scan(&g); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return g
}

// A day rolled up before 016 has no raw rows left to count groups from, so
// its unique_groups is NULL in the table and NULL through the view. A day
// still raw when the migration runs is measured by the live half and, once
// rolled up, keeps that measurement as an integer.
func TestMigration016LeavesHistoryUnmeasured(t *testing.T) {
	db := newTestDBAt(t, 15)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO projects (id, name, attributes) VALUES (1, 'App', '["plan"]')`,
		`INSERT INTO agg_product_attrs VALUES (1,'2026-09-01','signup','plan','pro',3,2)`,
	} {
		if _, err := db.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migration 016: %v", err)
	}
	if !hasColumn(t, db, "agg_product_attrs", "unique_groups") {
		t.Fatal("agg_product_attrs has no unique_groups column")
	}
	if g := groupsOf(t, db, `SELECT unique_groups FROM agg_product_attrs WHERE day='2026-09-01'`); g.Valid {
		t.Fatalf("pre-016 row unique_groups = %d, want NULL (unmeasured)", g.Int64)
	}
	if g := groupsOf(t, db, `SELECT unique_groups FROM v_product_attrs
		WHERE project_id=1 AND day='2026-09-01' AND attr_key='plan'`); g.Valid {
		t.Fatalf("view shows %d for a pre-016 day, want NULL", g.Int64)
	}

	// A raw day: one event with a group, one without.
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "e1", ProjectID: 1, EventName: "signup", ActorID: "u1", GroupID: "org1",
			TS: ts("2026-09-10T10:00:00Z"), Attributes: map[string]string{"plan": "pro"}},
		{ID: "e2", ProjectID: 1, EventName: "signup", ActorID: "u2",
			TS: ts("2026-09-10T11:00:00Z"), Attributes: map[string]string{"plan": "pro"}},
	}); err != nil {
		t.Fatal(err)
	}
	live := `SELECT unique_groups FROM v_product_attrs
		WHERE project_id=1 AND day='2026-09-10' AND attr_key='plan' AND attr_value='pro'`
	if g := groupsOf(t, db, live); !g.Valid || g.Int64 != 1 {
		t.Fatalf("live unique_groups = %+v, want 1", g)
	}
	if err := db.AggregateProductDay(ctx, 1, day("2026-09-10"), []string{"plan"}, 50); err != nil {
		t.Fatal(err)
	}
	if g := groupsOf(t, db, `SELECT unique_groups FROM agg_product_attrs
		WHERE day='2026-09-10' AND attr_key='plan' AND attr_value='pro'`); !g.Valid || g.Int64 != 1 {
		t.Fatalf("rolled-up unique_groups = %+v, want 1", g)
	}
	if g := groupsOf(t, db, live); !g.Valid || g.Int64 != 1 {
		t.Fatalf("view after rollup unique_groups = %+v, want 1", g)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `/usr/local/go/bin/go test ./internal/store/sqlite/ -run TestMigration016 -count=1`
Expected: FAIL — `agg_product_attrs has no unique_groups column` (or the `groupsOf` query erroring on `no such column: unique_groups`).

- [ ] **Step 3: Write the migration**

Create `internal/store/sqlite/migrations/016_product_attr_groups.sql`. The view is 015's definition with `group_id` carried from `vals` through `counted` and `ranked` into all three arms of the union; nothing else in it moves.

```sql
-- 016: groups in the product attribute breakdown.
-- Spec: docs/superpowers/specs/2026-09-21-product-attr-groups-design.md
--
-- agg_product_attrs gains unique_groups -- distinct non-empty group_id
-- among the rows carrying an attribute value -- and v_product_attrs is
-- recreated with group_id threaded through every arm. Structure only:
-- there is no value to fold and no data step.
--
-- Nullable on purpose, against the NOT NULL convention of every other
-- column here. Aggregation deletes the day's raw rows in the same
-- transaction that writes this table, so days rolled up before this
-- migration cannot be backfilled -- their group count is unknown, not
-- zero. The live half always writes an integer (COUNT DISTINCT returns 0,
-- not NULL, when no row carries a group), so NULL means exactly one thing:
-- aggregated before 016.
ALTER TABLE agg_product_attrs ADD COLUMN unique_groups INTEGER;

-- 015's definition with group_id carried from vals through counted and
-- ranked into all three arms of the union. Ranking stays ORDER BY c DESC,
-- attr_value: a value's place in the top N is decided by how often it
-- appeared, so the rows this view returns are 015's rows plus one column.
-- (other) counts its groups from raw, not as a sum of the tail's own
-- counts, for the same reason its unique_users is computed there.
DROP VIEW IF EXISTS v_product_attrs;
CREATE VIEW v_product_attrs AS
WITH cap AS (
  SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta
                   WHERE key='product_attributes_top_n'
                     AND CAST(value AS INTEGER) > 0), 50) AS n
),
declared AS (
  SELECT DISTINCT p.id AS project_id, j.value AS attr_key
  FROM projects p,
       json_each(CASE WHEN json_valid(p.attributes) THEN p.attributes ELSE '[]' END) j
  WHERE j.type = 'text'
),
vals AS (
  SELECT e.project_id AS project_id, substr(e.ts,1,10) AS day,
         e.event_name AS event_name, d.attr_key AS attr_key,
         json_extract(e.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') AS attr_value,
         e.actor_id AS actor_id, e.group_id AS group_id
  FROM events e
  JOIN declared d ON d.project_id = e.project_id
  WHERE json_extract(e.attributes, '$."' || replace(d.attr_key,'"','\"') || '"') IS NOT NULL
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$os', os, actor_id, group_id
  FROM events WHERE os <> ''
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$platform', platform, actor_id, group_id
  FROM events WHERE platform <> ''
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$app_version', app_version, actor_id, group_id
  FROM events WHERE app_version <> ''
),
counted AS (
  SELECT project_id, day, event_name, attr_key, attr_value,
         COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
         COUNT(DISTINCT NULLIF(group_id,'')) AS g
  FROM vals
  GROUP BY project_id, day, event_name, attr_key, attr_value
),
ranked AS (
  SELECT project_id, day, event_name, attr_key, attr_value, c, u, g,
         ROW_NUMBER() OVER (PARTITION BY project_id, day, event_name, attr_key
                            ORDER BY c DESC, attr_value) AS rn
  FROM counted
)
SELECT project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups
FROM agg_product_attrs
UNION ALL
SELECT project_id, day, event_name, attr_key, attr_value, c, u, g
FROM ranked
WHERE rn <= (SELECT n FROM cap)
UNION ALL
SELECT v.project_id, v.day, v.event_name, v.attr_key, '(other)',
       COUNT(*), COUNT(DISTINCT v.actor_id), COUNT(DISTINCT NULLIF(v.group_id,''))
FROM vals v
WHERE NOT EXISTS (
  SELECT 1 FROM ranked r
  WHERE r.project_id = v.project_id AND r.day = v.day
    AND r.event_name = v.event_name AND r.attr_key = v.attr_key
    AND r.attr_value = v.attr_value
    AND r.rn <= (SELECT n FROM cap))
GROUP BY v.project_id, v.day, v.event_name, v.attr_key;
```

Do not add anything to `dataSteps` in `migrate.go`: there is no value work.

- [ ] **Step 4: Write the rollup**

In `internal/store/sqlite/aggregate_product.go`, replace the body of `rollupAttrValue` (keep its doc comment; add one sentence to it: "Both statements also write `unique_groups`, the distinct non-empty `group_id` among the same rows, so a day rolled up after 016 carries an integer — `0` when no row had a group — and only pre-016 history is NULL."):

```go
func (d *DB) rollupAttrValue(ctx context.Context, tx *sql.Tx, expr, present string, named []any) error {
	// Top-N values by count. Ranking is by count alone; groups ride along.
	if _, err := tx.ExecContext(ctx, `
		WITH counted AS (
		  SELECT `+expr+` AS v, COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
		         COUNT(DISTINCT NULLIF(group_id,'')) AS g
		  FROM events
		  WHERE project_id=:p AND ts>=:from AND ts<:to AND event_name=:event
		    AND `+present+`
		  GROUP BY v
		),
		ranked AS (SELECT v, c, u, g, ROW_NUMBER() OVER (ORDER BY c DESC, v) AS rn FROM counted)
		INSERT OR REPLACE INTO agg_product_attrs
		  (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
		SELECT :p, :day, :event, :key, v, c, u, g FROM ranked WHERE rn <= :n`, named...); err != nil {
		return err
	}
	// Tail -> "(other)" with correct distinct users and groups, computed
	// from raw rather than summed across the tail's values.
	_, err := tx.ExecContext(ctx, `
		WITH counted AS (
		  SELECT `+expr+` AS v, COUNT(*) AS c
		  FROM events
		  WHERE project_id=:p AND ts>=:from AND ts<:to AND event_name=:event
		    AND `+present+`
		  GROUP BY v
		),
		ranked AS (SELECT v, ROW_NUMBER() OVER (ORDER BY c DESC, v) AS rn FROM counted),
		keep AS (SELECT v FROM ranked WHERE rn <= :n)
		INSERT OR REPLACE INTO agg_product_attrs
		  (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
		SELECT :p, :day, :event, :key, '(other)', COUNT(*), COUNT(DISTINCT actor_id),
		       COUNT(DISTINCT NULLIF(group_id,''))
		FROM events
		WHERE project_id=:p AND ts>=:from AND ts<:to AND event_name=:event
		  AND `+present+`
		  AND `+expr+` NOT IN (SELECT v FROM keep)
		HAVING COUNT(*) > 0`, named...)
	return err
}
```

- [ ] **Step 5: Run the test and the whole store package**

Run: `/usr/local/go/bin/go test ./internal/store/sqlite/ -run TestMigration016 -count=1`
Expected: PASS.

Run: `/usr/local/go/bin/go test ./internal/store/sqlite/ -count=1`
Expected: PASS. `TestMigrationsAreIdempotent`, `TestMigrationViews` and the 015 test all run through 016 now. `TestProductAttrsViewInvariant` still passes because `readAttrs` does not read the new column yet (Task 2 adds it).

Also run: `/usr/local/go/bin/go vet ./internal/store/... && /usr/local/go/bin/go test ./internal/api/ -count=1`
Expected: PASS — the API seeds `agg_product_attrs` by column name, so the new nullable column does not disturb it.

- [ ] **Step 6: Controller commits**

```bash
git add internal/store/sqlite/migrations/016_product_attr_groups.sql internal/store/sqlite/aggregate_product.go internal/store/sqlite/migration016_test.go docs/superpowers/specs/2026-09-21-product-attr-groups-design.md docs/superpowers/plans/2026-09-22-product-attr-groups.md
git commit -m "feat(store): count the distinct groups behind each product attribute value"
```

---

### Task 2: The boundary and the rollup tests

**Files:**
- Modify: `internal/store/sqlite/views_test.go` (`attrRow` ~line 504, `readAttrs` ~513, `seedAttrDay` ~536, `TestProductAttrsViewInvariant` ~574, `TestProductAttrsViewOtherRecomputesUniques` ~615)
- Modify: `internal/store/sqlite/aggregate_product_test.go` (append three tests)

**Interfaces:**
- Consumes: `groupsOf` from Task 1 (`migration016_test.go`, same package), `v_product_attrs.unique_groups`, `seedProductDay`, `seedDeclaredProject`, `readAttrs`, `store.ProductEvent.GroupID`.
- Produces: nothing later tasks use. These tests are the spec's tests 1, 3, 4 and 5. They are expected to pass against Task 1's code; a failure here is a defect in Task 1's SQL, fix it there (same package, same branch) and say so in the report.

- [ ] **Step 1: Carry the column through the boundary fixture**

In `views_test.go`, add `"database/sql"` to the imports and change `attrRow`, `readAttrs` and `seedAttrDay`:

```go
// attrRow mirrors one v_product_attrs row for before/after comparison.
// Groups is NULL only for a day rolled up before migration 016; the live
// half always measures, so every row read here must be Valid.
type attrRow struct {
	Event, Key, Value string
	Count, Uniques    int
	Groups            sql.NullInt64
}
```

In `readAttrs`, select and scan the column:

```go
	rows, err := db.db.Query(`SELECT event_name, attr_key, attr_value, count, unique_users, unique_groups
		FROM v_product_attrs WHERE project_id=? AND day=?
		ORDER BY event_name, attr_key, attr_value`, projectID, day)
	...
		if err := rows.Scan(&r.Event, &r.Key, &r.Value, &r.Count, &r.Uniques, &r.Groups); err != nil {
```

In `seedAttrDay`, give every event a group from a three-way cycle and extend the doc comment. Replace the comment and the inner loop body:

```go
// seedAttrDay writes 60 distinct "plan" values for one event on one day --
// more than the 50 cap, so the top-N cutoff and the "(other)" tail are both
// exercised. Counts vary (1..3) so the ranking is not a pure alphabetical
// tiebreak, and the four actors repeat across values so the tail's
// unique_users is strictly less than the sum of its per-value uniques --
// the exact case a summed "(other)" row would get wrong. Groups cycle
// through "", g1 and g2 by (i/3+n)%3, so the tail (the count-1 values
// p30..p57, i.e. i/3 in 10..19) holds empties as well as both groups:
// its distinct non-empty groups are 2 while its per-value sum is 7.
func seedAttrDay(t *testing.T, db *DB, projectID int64) {
	t.Helper()
	groups := []string{"", "g1", "g2"}
	var evs []store.ProductEvent
	id := 0
	for i := 0; i < 60; i++ {
		for n := 0; n <= i%3; n++ {
			id++
			evs = append(evs, store.ProductEvent{
				ID: fmt.Sprintf("e%04d", id), ProjectID: projectID, EventName: "signup",
				ActorID: fmt.Sprintf("a%d", (i+n)%4), GroupID: groups[(i/3+n)%3],
				TS:         ts("2026-08-01T10:00:00Z"),
				Attributes: map[string]string{"plan": fmt.Sprintf("p%02d", i)},
				OS:         []string{"ios", "android"}[i%2],
				AppVersion: []string{"1.0", "2.0", "3.0"}[i%3],
			})
		}
	}
	// A second event name, so the per-event partitioning is exercised too.
	evs = append(evs, store.ProductEvent{
		ID: "ping1", ProjectID: projectID, EventName: "ping", ActorID: "a9", GroupID: "g1",
		TS: ts("2026-08-01T11:00:00Z"), Attributes: map[string]string{"plan": "pro"},
		OS: "web", AppVersion: "1.0",
	})
	if err := db.WriteProductEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Assert the live half always measures**

In `TestProductAttrsViewInvariant`, right after the `if len(before) == 0` guard, add:

```go
	// The live half must always measure: NULL is reserved for days rolled
	// up before 016, and a NULL here would make the before/after
	// comparison agree for the wrong reason once the rollup writes NULL too.
	for _, r := range before {
		if !r.Groups.Valid {
			t.Fatalf("live row %s/%s=%s has NULL unique_groups; the live half must always measure", r.Event, r.Key, r.Value)
		}
	}
```

The existing `reflect.DeepEqual(before, after)` now compares `Groups` as well, in this test and in `TestProductAttrsViewSystemDimensionsWithoutDeclaredKeys`, `TestProductAttrsViewHonoursMetaCap` and `TestProductAttrsViewClampsBadMetaCap`. Nothing to change there.

- [ ] **Step 3: The tail's groups are distinct**

Replace `TestProductAttrsViewOtherRecomputesUniques` with:

```go
// The "(other)" row's unique_users and unique_groups must be fresh
// COUNT(DISTINCT ...) over the tail, not a sum of the per-value figures: an
// actor or a group appearing under several tail values would otherwise be
// counted once per value.
func TestProductAttrsViewOtherRecomputesUniques(t *testing.T) {
	db := newTestDB(t)
	id := seedDeclaredProject(t, db, []string{"plan"})
	seedAttrDay(t, db, id)
	var count, uniques int
	var groups sql.NullInt64
	if err := db.db.QueryRow(`SELECT count, unique_users, unique_groups FROM v_product_attrs
		WHERE project_id=? AND day='2026-08-01' AND event_name='signup'
		  AND attr_key='plan' AND attr_value='(other)'`, id).Scan(&count, &uniques, &groups); err != nil {
		t.Fatal(err)
	}
	if uniques >= count {
		t.Fatalf("(other) = count %d uniques %d; the fixture repeats actors across "+
			"tail values, so uniques must be strictly smaller than a summed count",
			count, uniques)
	}
	// The tail is the ten count-1 values p30..p57 (i/3 in 10..19): groups
	// g1, g2 and "" in rotation, so the distinct non-empty count is 2
	// while a per-value sum would be 7.
	if !groups.Valid || groups.Int64 != 2 {
		t.Fatalf("(other) unique_groups = %+v, want 2 (distinct across the tail, not summed)", groups)
	}
}
```

- [ ] **Step 4: Run the view tests**

Run: `/usr/local/go/bin/go test ./internal/store/sqlite/ -run 'TestProductAttrsView' -count=1 -v`
Expected: PASS, all five. If the invariant test reports a `Groups` mismatch, the live half and the rollup disagree — fix Task 1's SQL, not the fixture.

- [ ] **Step 5: The rollup tests**

Append to `aggregate_product_test.go`:

```go
// Zero is a measurement: a day whose events carry no group at all rolls up
// to unique_groups = 0, never NULL. NULL is reserved for days rolled up
// before migration 016, and the whole nullable decision rests on the two
// never being confused.
func TestAggregateProductGroupsZeroIsMeasured(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db) // no event carries a group_id
	if err := db.AggregateProductDay(ctx, 1, day("2026-08-10"), []string{"plan"}, 50); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"free", "pro"} {
		g := groupsOf(t, db, `SELECT unique_groups FROM agg_product_attrs
			WHERE event_name='subscribed' AND attr_key='plan' AND attr_value=?`, value)
		if !g.Valid || g.Int64 != 0 {
			t.Fatalf("plan=%s: unique_groups = %+v, want 0 (measured, none)", value, g)
		}
	}
}

// Distinctness: many events from one group count as one group, and the
// same group under two event names counts once per event row -- which is
// why the Evidence page takes max(unique_groups) rather than a sum.
func TestAggregateProductGroupsAreDistinctPerEvent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := func(m int) string { return fmt.Sprintf("2026-08-10T10:%02d:00Z", m) }
	pro := map[string]string{"plan": "pro"}
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "g1", ProjectID: 1, EventName: "signup", ActorID: "u1", GroupID: "acme", TS: ts(at(0)), Attributes: pro},
		{ID: "g2", ProjectID: 1, EventName: "signup", ActorID: "u2", GroupID: "acme", TS: ts(at(1)), Attributes: pro},
		{ID: "g3", ProjectID: 1, EventName: "signup", ActorID: "u3", GroupID: "acme", TS: ts(at(2)), Attributes: pro},
		{ID: "g4", ProjectID: 1, EventName: "signup", ActorID: "u4", GroupID: "globex", TS: ts(at(3)), Attributes: pro},
		{ID: "g5", ProjectID: 1, EventName: "renew", ActorID: "u1", GroupID: "acme", TS: ts(at(4)), Attributes: pro},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateProductDay(ctx, 1, day("2026-08-10"), []string{"plan"}, 50); err != nil {
		t.Fatal(err)
	}
	for event, want := range map[string]int64{"signup": 2, "renew": 1} {
		g := groupsOf(t, db, `SELECT unique_groups FROM agg_product_attrs
			WHERE event_name=? AND attr_key='plan' AND attr_value='pro'`, event)
		if !g.Valid || g.Int64 != want {
			t.Fatalf("%s/plan=pro: unique_groups = %+v, want %d", event, g, want)
		}
	}
	// acme appears under both events: the per-event figures sum to 3
	// while only two groups exist, so a reader must not add them up.
}

// The tail: with topN forced low, "(other)" carries the distinct group
// count across the whole tail, computed from raw. Overlapping groups make
// that strictly less than the sum of the tail's own per-value counts.
func TestAggregateProductGroupsInTail(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var evs []store.ProductEvent
	id := 0
	add := func(user, group, val string) {
		id++
		evs = append(evs, store.ProductEvent{ID: fmt.Sprintf("t%d", id), ProjectID: 1,
			EventName: "clicked", ActorID: user, GroupID: group, TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"button": val}})
	}
	add("u1", "acme", "v0") // v0 x3 is the one kept value
	add("u2", "globex", "v0")
	add("u3", "", "v0")
	add("u1", "acme", "v1") // the tail: acme twice, globex once, one with no group
	add("u2", "acme", "v2")
	add("u3", "globex", "v3")
	add("u4", "", "v4")
	if err := db.WriteProductEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateProductDay(ctx, 1, day("2026-08-10"), []string{"button"}, 1); err != nil {
		t.Fatal(err)
	}
	if g := groupsOf(t, db, `SELECT unique_groups FROM agg_product_attrs
		WHERE attr_key='button' AND attr_value='v0'`); !g.Valid || g.Int64 != 2 {
		t.Fatalf("v0: unique_groups = %+v, want 2 (the empty group does not count)", g)
	}
	var count int
	var groups sql.NullInt64
	if err := db.db.QueryRow(`SELECT count, unique_groups FROM agg_product_attrs
		WHERE attr_key='button' AND attr_value='(other)'`).Scan(&count, &groups); err != nil {
		t.Fatal(err)
	}
	if count != 4 || !groups.Valid || groups.Int64 != 2 {
		t.Fatalf("(other): count=%d unique_groups=%+v, want 4 and 2 (acme, globex; a summed tail would say 3)", count, groups)
	}
}
```

Add `"database/sql"` to the file's imports.

- [ ] **Step 6: Run the package**

Run: `/usr/local/go/bin/go test ./internal/store/sqlite/ -count=1`
Expected: PASS.

Run: `/usr/local/go/bin/gofmt -l internal/store/sqlite/`
Expected: no output from the files this task touched (`retention.go` is a known pre-existing gofmt straggler; leave it alone).

- [ ] **Step 7: Controller commits**

```bash
git add internal/store/sqlite/views_test.go internal/store/sqlite/aggregate_product_test.go
git commit -m "test(store): pin the group count across the aggregation boundary and in the tail"
```

---

### Task 3: The private API and the user-facing document

**Files:**
- Modify: `internal/api/ops_product.go:58` (`productAttributes` query)
- Modify: `internal/api/ops_read.go:234-235` (`product_attributes` description)
- Modify: `internal/api/resources.go:50` (`schemaViews` row)
- Modify: `internal/api/seed_test.go:90-91`
- Modify: `internal/api/ops_product_test.go` (`TestProductAttributesReturnsRows`)
- Modify: `docs/twillingate.md:128-129`, `:143-144`, `:993`, `:1093-1094`

**Interfaces:**
- Consumes: `v_product_attrs.unique_groups` (Task 1). `h.table` returns `tableOut{Columns, Rows}` where a NULL cell scans through `sql.NullString` in `readdb.go` and renders as the empty string.
- Produces: `product_attributes` (MCP and `GET /api/projects/{project_id}/product/attributes`) return a seventh column `unique_groups`, positionally last.

- [ ] **Step 1: Write the failing API test**

In `internal/api/seed_test.go`, replace the `agg_product_attrs` seed with one measured and one unmeasured row:

```go
	// One row rolled up before migration 016 (unique_groups NULL, "not
	// measured") and one after, so the tool is seen to carry both.
	seed(`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
	      VALUES (1,'2026-08-20','signup','plan','pro',3,3,NULL),
	             (1,'2026-08-21','signup','plan','team',2,2,2)`)
```

In `internal/api/ops_product_test.go`, extend `TestProductAttributesReturnsRows` after the first `IsError` check:

```go
	out := textOf(res)
	if !strings.Contains(out, "plan") || !strings.Contains(out, "pro") {
		t.Errorf("missing attribute row: %s", out)
	}
	// unique_groups flows through as the last column; a NULL (a day rolled
	// up before 016) is an empty cell, not an error.
	if !strings.Contains(out, "unique_groups") || !strings.Contains(out, "team") {
		t.Errorf("missing unique_groups column or the measured row: %s", out)
	}
```

(Replace the existing `if out := textOf(res); ...` line for that first check so `out` is a variable; leave the event-filter branch as it is.)

- [ ] **Step 2: Run it to verify it fails**

Run: `/usr/local/go/bin/go test ./internal/api/ -run TestProductAttributesReturnsRows -count=1`
Expected: FAIL — `missing unique_groups column`.

- [ ] **Step 3: The query, the description, the schema resource**

`internal/api/ops_product.go`:

```go
	q := `SELECT day, event_name, attr_key, attr_value, count, unique_users, unique_groups
		FROM v_product_attrs WHERE project_id=? AND day BETWEEN ? AND ?`
```

`internal/api/ops_read.go`, the `product_attributes` spec:

```go
		Description: "Attribute breakdowns for product events: count, unique users and unique groups per value, per event, per day. unique_groups is empty for days rolled up before it was measured and 0 when it was measured and no group was involved. The system dimensions $platform, $os and $app_version are always included; a custom key only appears once the project declares it in attributes (see update_project)."},
```

`internal/api/resources.go`, the `v_product_attrs` line in `schemaViews`:

```
  v_product_attrs(project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)  -- attr_key '$platform', '$os', '$app_version' always present; unique_groups is NULL for days rolled up before it was measured (0 = measured, none)
```

- [ ] **Step 4: Run the API package**

Run: `/usr/local/go/bin/go test ./internal/api/ -count=1`
Expected: PASS, including `docs_sync_test.go` (view names and tool names are unchanged).

- [ ] **Step 5: The user-facing document**

`docs/twillingate.md`. Four edits, each replacing the quoted text:

1. Lines 128–129, the declared-key sentence: replace `a value breakdown (counts and unique users per distinct value, per event, per day)` with `a value breakdown (counts, unique users and unique groups per distinct value, per event, per day)`.

2. Lines 143–144, the cap sentence: replace `whose unique-user count is recomputed from raw rather than summed` with `whose unique-user and unique-group counts are recomputed from raw rather than summed`.

3. Line 993, the tool table row for `product_attributes`, becomes:

```
| `product_attributes` | `event`, `key` | Count, unique users and unique groups per value of a declared attribute. `$platform`, `$os` and `$app_version` are always available; a custom key only appears once the project declares it. `unique_groups` is empty for days rolled up before it was measured and `0` when it was measured and no group was involved |
```

4. Lines 1093–1094, the views paragraph: replace `Product events have `v_product_daily`, `v_product_totals` and `v_product_attrs`, plus `v_events_flat`,` with:

```
Product events have `v_product_daily`, `v_product_totals` and
`v_product_attrs` (whose `unique_groups` is NULL, not zero, for days rolled
up before it was measured — `MAX()` skips it, `SUM()` would too, a `COALESCE`
to 0 would lie), plus `v_events_flat`,
```

Keep line wrapping near 78 columns as the surrounding text does.

- [ ] **Step 6: Run the docs binding tests**

Run: `/usr/local/go/bin/go test ./internal/api/ -run 'TestDocument' -count=1`
Expected: PASS.

- [ ] **Step 7: Controller commits**

```bash
git add internal/api/ops_product.go internal/api/ops_read.go internal/api/resources.go internal/api/seed_test.go internal/api/ops_product_test.go docs/twillingate.md
git commit -m "feat(api): return the group count behind each product attribute value"
```

---

### Task 4: Evidence, the deployment page and the spec status

**Files:**
- Modify: `evidence/sources/twillingate/v_product_attrs.sql`
- Modify: `evidence/pages/product/[project].md` (`app_version_summary` ~line 58, its `DataTable` ~line 81, `attr_breakdowns` ~line 118, its prose and `DataTable` ~line 133)
- Modify: `docs/deployment.md` (new subsection after "Upgrading to the declared environment (migration 015)", before "### Replication with litestream" at line 691)
- Modify: `docs/superpowers/specs/2026-09-21-product-attr-groups-design.md` (Status line)

**Interfaces:**
- Consumes: `v_product_attrs.unique_groups` (Task 1). Evidence's sqlite connector types a column from the first row whose value is not NULL (`inferColumnTypes` in `@evidence-dev/db-commons`), so aggregate rows that lead with NULL do not break inference; the empty-database sentinel still needs a value.
- Produces: two dashboard columns titled `Groups (at least)`.

- [ ] **Step 1: The source query**

`evidence/sources/twillingate/v_product_attrs.sql` — keep the comment, change both selects:

```sql
select project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups
from v_product_attrs
union all
select 0, '1970-01-01', '', '', '', 0, 0, 0
where not exists (select 1 from v_product_attrs)
```

- [ ] **Step 2: The page**

`evidence/pages/product/[project].md`. `app_version_summary` becomes:

```sql app_version_summary
-- unique_users and unique_groups are per event and cannot be summed -- one
-- person or one group firing two events would count twice -- so the largest
-- single-event figure is shown, a floor on the true number. unique_groups
-- is NULL for days rolled up before the collector measured it; max() skips
-- those, so a range with no measured day shows an empty cell, not 0.
select attr_value as app_version, sum(count) as total,
       max(unique_users) as min_users,
       max(unique_groups) as min_groups
from twillingate.v_product_attrs
where project_id = '${params.project}'
  and attr_key = '$app_version'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by attr_value
order by total desc
```

Its table gains a column after `min_users`:

```
    <Column id=min_groups title="Groups (at least)" fmt=num0 />
```

`attr_breakdowns` becomes:

```sql attr_breakdowns
-- Summed across events: a value's count is how often it appeared on any
-- event that day. unique_users and unique_groups are per event and cannot
-- be summed -- one person or one group firing two events would count twice
-- -- so the largest single-event figure is shown, a floor on the true
-- number. unique_groups is NULL for days rolled up before the collector
-- measured it; max() skips those, so such a day shows an empty cell, not 0.
select attr_key, day, attr_value, sum(count) as count,
       max(unique_users) as min_users,
       max(unique_groups) as min_groups
from twillingate.v_product_attrs
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by attr_key, day, attr_value
order by attr_key, day desc, count desc
```

The prose under `## Attribute breakdowns` becomes:

```
Each attribute's values per day, across all events. Users and Groups are the
largest counts from any single event, so the true numbers are at least
those. Groups is empty for days the collector rolled up before it measured
groups — unknown, not zero.
```

Its table gains, after `min_users`:

```
    <Column id=min_groups title="Groups (at least)" fmt=num0 />
```

The `app_versions` area chart is unchanged.

- [ ] **Step 3: The deployment page**

Insert before `### Replication with litestream` in `docs/deployment.md`:

```markdown
### Upgrading to group counts (migration 016)

`agg_product_attrs` gains a nullable `unique_groups` — the distinct groups
behind each attribute value — and `v_product_attrs`, the
`product_attributes` tool and the product dashboard's two breakdown tables
carry it. There is nothing to check first: the migration is one `ALTER` and
one view, no value is rewritten, and it runs in well under a second.

What changes on the day:

- **Days rolled up before the upgrade stay unmeasured.** Aggregation
  deletes a day's raw rows in the same transaction that writes its rollup,
  so there is nothing left to count groups from. Those rows hold NULL, not
  0, and stay that way: `product_attributes` returns an empty cell and the
  dashboard's "Groups (at least)" column is blank for any range that has no
  measured day. `0` only ever means measured, none.
- Days still within `RETENTION_PRODUCT_RAW_DAYS` at upgrade time are
  measured by the live half at once and keep the figure when they roll up.
- Saved SQL that reads `v_product_attrs` by position gets the new column
  last; by name, nothing changes. A `SUM()` or `MAX()` over it skips the
  NULLs; do not `COALESCE` them to 0.

There is no down migration. An older binary still runs against the upgraded
file — it writes the seven columns it knows and leaves `unique_groups`
NULL — but a day it rolls up is then unmeasured for good.
```

- [ ] **Step 4: The spec status**

In `docs/superpowers/specs/2026-09-21-product-attr-groups-design.md`, change `Status: proposed` to `Status: implemented (2026-09-22)` and add one line under it: `Implementation: docs/superpowers/plans/2026-09-22-product-attr-groups.md; the PR is feat: without ! (additive migration, trailing column, an older binary's insert still succeeds).`

- [ ] **Step 5: Check**

Run: `grep -n "unique_groups\|min_groups\|Groups (at least)" evidence/sources/twillingate/v_product_attrs.sql "evidence/pages/product/[project].md" docs/deployment.md`
Expected: the source select and sentinel, two `min_groups` selects, two `Column` lines, the deployment subsection.

Run: `/usr/local/go/bin/go test ./internal/api/ ./internal/dashboards/ -count=1`
Expected: PASS (the docs binding tests read `docs/deployment.md` for environment variables; the new prose adds none).

- [ ] **Step 6: Controller commits**

```bash
git add evidence/sources/twillingate/v_product_attrs.sql "evidence/pages/product/[project].md" docs/deployment.md docs/superpowers/specs/2026-09-21-product-attr-groups-design.md
git commit -m "feat(dashboards): show the groups behind each attribute value on the product page"
```

---

## After the tasks (controller)

1. `make check` on the branch (vet + coverage + restore test).
2. Final whole-branch review, one fix wave.
3. Push `feat/product-attr-groups`, open a draft PR titled `feat: count the groups behind each product attribute value`, body leading with the problem (seats vs accounts) and the two rulings (nullable column; no `!`), then close spec PR #42 pointing at it.

## Self-review

- **Spec coverage.** Decisions 1–5 → Tasks 1 (column, view, rollup), 3 (API), 4 (Evidence). "Why history stays NULL" → the migration comment (T1), the deployment page (T4), the schema resource and the docs (T3). Storage SQL → T1 Step 3, verbatim in shape. Rollup → T1 Step 4. Private API + `schemaViews` → T3. Reporting → T4. Documentation → T3 (`docs/twillingate.md`) and T4 (`docs/deployment.md`, added because CLAUDE.md binds it to `evidence/`). Testing 1 → T2 Steps 1–2 (the invariant compares `Groups`); 2 → T1 Step 1; 3, 4, 5 → T2 Step 5. Out of scope: nothing here renames `unique_users` or touches the other views.
- **Placeholders.** None: every SQL statement, Go function and prose edit is spelled out.
- **Type consistency.** `groupsOf(t, db, q, args...) sql.NullInt64` is defined in T1 and used in T2 with the same signature; `attrRow.Groups sql.NullInt64` matches `readAttrs`'s scan; `min_groups` is the alias in both page queries and both `Column id=` values; the eighth view column is `unique_groups` in the migration, the rollup's insert list, `schemaViews`, the API select, the Evidence source and the seed.
