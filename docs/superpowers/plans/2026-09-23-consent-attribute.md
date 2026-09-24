# Consent Flag Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Record, on every view and product event, whether the client had consent to keep anything on the device (`$consent` 1/0, NULL for unknown), and expose it as a `consent` views dimension with a rate on the dashboard.

**Architecture:** One reserved batch attribute parsed in Go at ingest into a nullable `consent INTEGER` column on `views` and `events` (migration 018). Views roll it up daily into `agg_views_consent` through the existing `viewDimensions` mechanism, stitched with the live half in `v_views_consent`; the API, Evidence and the SDK each gain one small piece.

**Tech Stack:** Go (net/http, database/sql, SQLite), TypeScript SDK (vitest, esbuild), Evidence (SQL + markdown pages).

**Spec:** `docs/superpowers/specs/2026-09-23-consent-attribute-design.md`

## Global Constraints

- Migration number is `018`, file `internal/store/sqlite/migrations/018_consent.sql`. Every migration test pins its ceiling with `migrateThrough(ctx, N)`; never an unbounded `Migrate`.
- Stored values: `1` (given), `0` (none), `NULL` (unknown). Breakdown values: `given`, `none`, `unknown` — exactly these strings.
- Accepted spellings, trimmed and case-folded: `1`, `"1"`, `true`, `"true"` → 1; `0`, `"0"`, `false`, `"false"` → 0; absent, JSON `null` or `""` → NULL with no warning; anything else → NULL with a warning. Never a rejection.
- No validation in the database (no CHECK constraints, no triggers). Validation lives in Go only.
- Docs change in the same commit as the code they describe (CLAUDE.md table). No model identifiers in any repo file.
- Commits: Conventional Commits, lower case, imperative, no trailing period. The controller commits; implementers do not run `git commit`.
- SDK: after any `sdk/` change run `npm run build` in `sdk/` and include `internal/server/twillingate.js`.
- Go is at `/usr/local/go/bin` (not on PATH): use `export PATH=/usr/local/go/bin:$PATH`.

### Deviations from the spec (deliberate; the PR body names them)

1. **`store.Consent` instead of `Consent int8` with `-1` for unknown.** With `int8`, the zero value `0` means "none", so every `store.View{…}` literal that does not set the field (tests, the Evidence seed, any future path) would record a refusal that never happened. `store.Consent`'s zero value is `ConsentUnknown`, and it implements `driver.Valuer`, so `write.go` passes it straight through and unknown lands as `NULL`.
2. **`agg_views_consent` is `WITHOUT ROWID`**, like `agg_views_platforms` in 015. The spec's SQL omitted it.
3. **`schemaViews` has no `v_events_flat` row today**, so the spec's "`v_events_flat`'s row names `consent`" becomes a mention in the docs' `v_events_flat` sentence instead.

## Review Focus

1. **A row built without `Consent` stores NULL, never 0.** Any `store.View{}`/`store.ProductEvent{}` literal that does not set it must read back as NULL (Task 1, `TestWriteLeavesConsentUnknownByDefault`).
2. **Older-ceiling migration tests writing through today's write path.** `migration016_test.go` calls `WriteProductEvents` on a database at 16; once the INSERT names `consent` it fails with "no such column". It must switch to a raw INSERT that names only 016's columns (Task 1, Step 1).
3. **Explicit `null` in a per-event attribute over a batch `$consent: 1`** overrides key by key and yields unknown, not given; `1.0` as a JSON number is `1`; `" TRUE "` is given (Task 2 test table).
4. **A retry batch replayed later keeps the `$consent` it was built with**: a batch that failed while consent was false and is replayed after `consent(true)` still says `0`; a fresh batch says `1` (Task 4 test).
5. **The live half of `v_views_consent` stays day-bounded**: `TestViewsLiveHalvesUseTheDayIndex` gains a `consent` query so a range read does not scan every raw row (Task 1).

---

### Task 1: Store — migration 018, the Consent type, the rollup and the views doc

**Files:**
- Create: `internal/store/sqlite/migrations/018_consent.sql`
- Create: `internal/store/sqlite/migration018_test.go`
- Modify: `internal/store/store.go` (type `Consent`; fields on `View`, `ProductEvent`)
- Modify: `internal/store/sqlite/write.go` (both INSERTs)
- Modify: `internal/store/sqlite/flatview.go:17` (`flatViewBaseColumns`)
- Modify: `internal/store/sqlite/aggregate_views.go` (`consentSQL`, `viewDimensions`)
- Modify: `internal/store/sqlite/prune.go:15-18` (`viewsAggTables`)
- Modify: `internal/store/sqlite/registry.go:227-235` (`projectTables`)
- Modify: `internal/store/sqlite/migration016_test.go:53-61` (raw INSERT)
- Modify: `internal/store/sqlite/views_test.go` (dims list, day-index test, new boundary test)
- Modify: `internal/store/sqlite/write_test.go` (round trip)
- Modify: `internal/store/sqlite/flatview_test.go` (column present)
- Modify: `internal/api/resources.go` (`schemaViews` row)
- Modify: `docs/twillingate.md` ("Writing SQL against the views" paragraph)
- Modify: `deploy/UPGRADES.md` (new section)

**Interfaces:**
- Produces (in `internal/store/store.go`):

```go
// Consent is the client's answer, when it sent the row, to "may anything
// be kept on this device" ($consent). The zero value is ConsentUnknown,
// so a row built without it never claims an answer nobody recorded.
type Consent int8

const (
	ConsentUnknown Consent = iota // stored as NULL
	ConsentGiven                  // stored as 1
	ConsentNone                   // stored as 0
)

// Value implements driver.Valuer: unknown is NULL, the answers 1 and 0.
func (c Consent) Value() (driver.Value, error) {
	switch c {
	case ConsentGiven:
		return int64(1), nil
	case ConsentNone:
		return int64(0), nil
	}
	return nil, nil
}
```

  `store.View` and `store.ProductEvent` each gain a field `Consent Consent`.
- Produces SQL objects: columns `views.consent`, `events.consent` (INTEGER, nullable); table `agg_views_consent(project_id, day, consent, visitors, views)`; view `v_views_consent(project_id, day, consent, visitors, views)` with `consent` ∈ `given|none|unknown`; `v_events_flat` gains a `consent` column after `actor_id`.

- [ ] **Step 1: Keep the 016 test on 016's columns**

In `internal/store/sqlite/migration016_test.go`, replace the `db.WriteProductEvents(ctx, []store.ProductEvent{…})` call (the "A raw day" block) with raw INSERTs naming only columns that exist at 16, so the test keeps its ceiling:

```go
	// A raw day: one event with a group, one without. Raw INSERTs, not
	// WriteProductEvents: this database is at 16, and the write path names
	// columns later migrations add.
	for _, q := range []string{
		`INSERT INTO events (id, project_id, event_name, ts, received_at, actor_id, actor_kind, user_id, group_id, platform, os, app_version, attributes)
		 VALUES ('e1', 1, 'signup', '2026-09-10T10:00:00Z', '2026-09-10T10:00:00Z', 'u1', '', '', 'org1', 'unknown', '', '', '{"plan":"pro"}')`,
		`INSERT INTO events (id, project_id, event_name, ts, received_at, actor_id, actor_kind, user_id, group_id, platform, os, app_version, attributes)
		 VALUES ('e2', 1, 'signup', '2026-09-10T11:00:00Z', '2026-09-10T11:00:00Z', 'u2', '', '', '', 'unknown', '', '', '{"plan":"pro"}')`,
	} {
		if _, err := db.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
```

Check the events columns at 16 with `grep -n "events" internal/store/sqlite/migrations/0*.sql` before relying on this list; if `day` or another NOT NULL column without a default exists on `events`, include it. Drop the now-unused `store` import if nothing else uses it. Run `go test ./internal/store/sqlite -run TestMigration016 -count=1` — expected PASS (still green before any other change).

- [ ] **Step 2: Write the failing migration test**

Create `internal/store/sqlite/migration018_test.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"testing"
)

// History has no consent answer to recover: every pre-018 raw row reads
// NULL, and every rolled-up day becomes one 'unknown' row carrying the
// daily totals, so a range across the upgrade shows a full unknown bar
// rather than nothing.
func TestMigration018LeavesHistoryUnknown(t *testing.T) {
	db := newTestDBAt(t, 17)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO projects (id, name) VALUES (1, 'Site')`,
		`INSERT INTO views (id, project_id, ts, received_at, kind, actor_id, path)
		 VALUES ('v1', 1, '2026-09-20T10:00:00Z', '2026-09-20T10:00:00Z', 'web', 'a1', '/')`,
		`INSERT INTO agg_views_daily (project_id, day, kind, visitors, views, sessions, bounces, duration_sec)
		 VALUES (1, '2026-09-01', 'web', 4, 9, 5, 2, 300), (1, '2026-09-01', 'app', 2, 3, 2, 1, 60)`,
	} {
		if _, err := db.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.migrateThrough(ctx, 18); err != nil {
		t.Fatalf("migration 018: %v", err)
	}
	for _, tbl := range []string{"views", "events"} {
		if !hasColumn(t, db, tbl, "consent") {
			t.Fatalf("%s has no consent column", tbl)
		}
	}
	var c sql.NullInt64
	if err := db.db.QueryRowContext(ctx, `SELECT consent FROM views WHERE id='v1'`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c.Valid {
		t.Fatalf("pre-018 view consent = %d, want NULL", c.Int64)
	}
	var consent string
	var visitors, views int
	if err := db.db.QueryRowContext(ctx, `SELECT consent, visitors, views FROM agg_views_consent
		WHERE project_id=1 AND day='2026-09-01'`).Scan(&consent, &visitors, &views); err != nil {
		t.Fatal(err)
	}
	if consent != "unknown" || visitors != 6 || views != 12 {
		t.Fatalf("seeded row = (%s, %d, %d), want (unknown, 6, 12)", consent, visitors, views)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT consent, visitors, views FROM v_views_consent
		WHERE project_id=1 AND day='2026-09-20'`).Scan(&consent, &visitors, &views); err != nil {
		t.Fatal(err)
	}
	if consent != "unknown" || visitors != 1 || views != 1 {
		t.Fatalf("live row = (%s, %d, %d), want (unknown, 1, 1)", consent, visitors, views)
	}
}
```

If the `views` INSERT above misses a NOT NULL column without a default at 17 (check `012_views.sql`; `day` may be a generated or explicit column), add it — the test must build a real pre-018 row. `hasColumn` already exists in the package (used by the 016 test).

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/store/sqlite -run TestMigration018 -count=1`
Expected: FAIL — `migrateThrough` has no version 18 (or `consent` column missing).

- [ ] **Step 4: Write the migration**

Create `internal/store/sqlite/migrations/018_consent.sql`:

```sql
-- 018: whether the client had consent to keep anything on the device.
-- Spec: docs/superpowers/specs/2026-09-23-consent-attribute-design.md
--
-- 1, 0, or NULL for unknown. NULL is every row stored before this
-- migration and every row a client sends without $consent; there is
-- nothing to backfill from, and 0 would claim a refusal that was never
-- recorded. The accepted spellings are validated in Go at ingest; the
-- database carries no copy of them.
ALTER TABLE views  ADD COLUMN consent INTEGER;
ALTER TABLE events ADD COLUMN consent INTEGER;

-- Daily rollup, modelled on agg_views_platforms. consent here is the
-- breakdown value ('given', 'none', 'unknown'), not the raw flag.
CREATE TABLE agg_views_consent (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, consent TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, consent)
) WITHOUT ROWID;

-- Seeded from the daily totals as 'unknown', the way 015 seeded
-- platforms, so a range covering rolled-up days shows a full 'unknown'
-- bar rather than nothing. Summing visitors across kinds over-counts an
-- actor seen on two kinds in one day; 015 accepted the same.
INSERT INTO agg_views_consent (project_id, day, consent, visitors, views)
SELECT project_id, day, 'unknown', SUM(visitors), SUM(views)
FROM agg_views_daily GROUP BY project_id, day;

-- Three values need no top-500 cap, so this is the plain
-- aggregate-plus-live-half shape. The CASE is consentSQL in
-- aggregate_views.go; TestStitchViewConsentAcrossBoundary keeps them equal.
CREATE VIEW v_views_consent AS
SELECT project_id, day, consent, visitors, views FROM agg_views_consent
UNION ALL
SELECT project_id, day,
       CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM views
GROUP BY project_id, day, CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END;
```

Check how migrations are discovered (`internal/store/sqlite/migrate.go`): if files are embedded by glob, nothing else is needed; if there is a list or a max-version constant, extend it.

- [ ] **Step 5: Run the migration test**

Run: `go test ./internal/store/sqlite -run 'TestMigration01' -count=1`
Expected: PASS for 012–018.

- [ ] **Step 6: Add the Consent type and write it**

Add the `Consent` type from **Interfaces** to `internal/store/store.go` (import `database/sql/driver`), and the field `Consent Consent` as the last field of `View` and of `ProductEvent`, each with a one-line comment only if the neighbouring fields carry one.

In `internal/store/sqlite/write.go`, append `consent` to both column lists and one `?` to each VALUES, and pass `v.Consent` / `e.Consent` as the last argument (the `driver.Valuer` does the NULL mapping):

```go
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO views
			(id, project_id, ts, received_at, kind, actor_id, actor_kind, user_id, group_id, session_id,
			 host, path, referrer_source, utm_source, utm_medium, utm_campaign,
			 platform, os, os_version, os_name, browser, browser_version, app_version,
			 device, device_model, locale, display_width, display_height, country, consent)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
```

```go
				v.Device, v.DeviceModel, v.Locale, v.DisplayWidth, v.DisplayHeight, v.Country, v.Consent); err != nil {
```

and likewise for events (`platform, os, app_version, attributes, consent` / 14 placeholders / `string(blob), e.Consent`).

- [ ] **Step 7: Write the round-trip tests**

Add to `internal/store/sqlite/write_test.go`:

```go
// consentOf reads a row's consent: Valid=false is NULL (unknown).
func consentOf(t *testing.T, db *DB, table, id string) sql.NullInt64 {
	t.Helper()
	var c sql.NullInt64
	if err := db.db.QueryRow(`SELECT consent FROM `+table+` WHERE id=?`, id).Scan(&c); err != nil {
		t.Fatalf("%s %s: %v", table, id, err)
	}
	return c
}

func TestWriteStoresConsent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WriteViews(ctx, []store.View{
		{ID: "g", ProjectID: 1, TS: ts("2026-08-10T10:00:00Z"), Kind: "web", ActorID: "a", Path: "/", Consent: store.ConsentGiven},
		{ID: "n", ProjectID: 1, TS: ts("2026-08-10T10:00:00Z"), Kind: "web", ActorID: "a", Path: "/", Consent: store.ConsentNone},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "e", ProjectID: 1, EventName: "x", TS: ts("2026-08-10T10:00:00Z"), ActorID: "a", Consent: store.ConsentNone},
	}); err != nil {
		t.Fatal(err)
	}
	if c := consentOf(t, db, "views", "g"); !c.Valid || c.Int64 != 1 {
		t.Errorf("given view consent = %+v, want 1", c)
	}
	if c := consentOf(t, db, "views", "n"); !c.Valid || c.Int64 != 0 {
		t.Errorf("none view consent = %+v, want 0", c)
	}
	if c := consentOf(t, db, "events", "e"); !c.Valid || c.Int64 != 0 {
		t.Errorf("none event consent = %+v, want 0", c)
	}
}

// A row built without Consent must not claim a refusal: the zero value is
// unknown, stored as NULL.
func TestWriteLeavesConsentUnknownByDefault(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WriteViews(ctx, []store.View{
		{ID: "v", ProjectID: 1, TS: ts("2026-08-10T10:00:00Z"), Kind: "web", ActorID: "a", Path: "/"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "e", ProjectID: 1, EventName: "x", TS: ts("2026-08-10T10:00:00Z"), ActorID: "a"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][2]string{{"views", "v"}, {"events", "e"}} {
		if c := consentOf(t, db, r[0], r[1]); c.Valid {
			t.Errorf("%s consent = %d, want NULL", r[0], c.Int64)
		}
	}
}
```

Use the package's existing helpers (`newTestDB`, `ts`; check how other write tests create project 1 and copy that if a project row is required). Add `database/sql` to the imports if missing.

- [ ] **Step 8: Roll consent up and prune it**

In `internal/store/sqlite/aggregate_views.go`, beside `displaySQL`:

```go
// consentSQL maps the raw consent flag onto the breakdown value, one
// expression shared by the aggregator and the live half of v_views_consent
// (018_consent.sql) so the two cannot drift.
const consentSQL = `CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END`
```

and as the last entry of `viewDimensions`:

```go
	{table: "agg_views_consent", keys: []string{"consent"}, exprs: []string{consentSQL}},
```

Append `"agg_views_consent"` to `viewsAggTables` in `prune.go` and to the views line of `projectTables` in `registry.go` (after `"agg_views_displays"` in both).

Append `"consent"` to `flatViewBaseColumns` in `flatview.go` right after `"actor_id"`:

```go
var flatViewBaseColumns = []string{"id", "project_id", "event_name", "actor_id", "consent", "ts", "attributes"}
```

- [ ] **Step 9: Write the boundary and plan tests**

In `internal/store/sqlite/views_test.go`:

(a) add `{"v_views_consent", "consent"}` to the `dims` list of `TestStitchViewsInvariantAllViewsDimensions` (the fixture's rows are all unknown, which still exercises the unknown arm on both sides);

(b) add `"consent"` to the `queries` map of `TestViewsLiveHalvesUseTheDayIndex`:

```go
		"consent": `SELECT consent, SUM(visitors), SUM(views) FROM v_views_consent
			WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY consent`,
```

If the plan shows the live half is not day-bounded, do not weaken the assertion: report it back (status DONE_WITH_CONCERNS) with the plan lines;

(c) add the load-bearing boundary test:

```go
// The consent live half and the rollup must agree on every value,
// including unknown, or the rate jumps the night a day is aggregated.
func TestStitchViewConsentAcrossBoundary(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	v := func(id, actor string, c store.Consent, h int) store.View {
		return store.View{ID: id, TS: at(h, 0), ActorID: actor, Kind: "web", Platform: "web", Path: "/", Consent: c}
	}
	seedViews(t, db,
		v("1", "a", store.ConsentGiven, 10), v("2", "a", store.ConsentGiven, 11), v("3", "b", store.ConsentGiven, 10),
		v("4", "c", store.ConsentNone, 10),
		v("5", "d", store.ConsentUnknown, 10), v("6", "d", store.ConsentUnknown, 12),
	)
	read := func() map[string][2]int {
		t.Helper()
		rows, err := db.db.Query(`SELECT consent, visitors, views FROM v_views_consent
			WHERE project_id=1 AND day='2026-08-10'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string][2]int{}
		for rows.Next() {
			var k string
			var vis, vws int
			if err := rows.Scan(&k, &vis, &vws); err != nil {
				t.Fatal(err)
			}
			out[k] = [2]int{vis, vws}
		}
		return out
	}
	want := map[string][2]int{"given": {2, 3}, "none": {1, 1}, "unknown": {1, 2}}
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Fatalf("live v_views_consent = %v, want %v", got, want)
	}
	if err := db.AggregateViewDay(ctx, 1, day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Errorf("after rollup v_views_consent = %v, want %v", got, want)
	}
}
```

`seedViews`, `at` and `day` are existing helpers (`aggregate_views_test.go`); check `seedViews` sets `ProjectID: 1` and the day `2026-08-10` as `seedViewDay` relies on.

(d) in `flatview_test.go`, add one assertion to an existing rebuild test (e.g. `TestRebuildFlatView`) or a small new test that `SELECT consent FROM v_events_flat LIMIT 0` succeeds after `RebuildFlatView(ctx, nil)`.

- [ ] **Step 10: Run the store package**

Run: `go test ./internal/store/... -count=1`
Expected: PASS, including `TestPruneAggregatesCoversAllAggTables` and `TestProjectTablesMatchesSchema`.

- [ ] **Step 11: Document the view and the upgrade**

`internal/api/resources.go`, in `schemaViews` after the `v_views_displays` line:

```
  v_views_consent(project_id, day, consent, visitors, views)  -- consent: 'given'|'none'|'unknown'; unknown is every row stored before migration 018 or sent without $consent
```

`docs/twillingate.md`, "Writing SQL against the views" paragraph: in the views-family list change "`v_views_devices` and `v_views_displays`;" to "`v_views_devices`, `v_views_displays` and `v_views_consent` (`given`, `none` or `unknown`, where `unknown` is every view stored before migration 018 or sent without `$consent`);", and change "plus `v_events_flat`, the `events` table with one column per declared attribute" to "plus `v_events_flat`, the `events` table (with its `consent` column, 1, 0 or NULL) and one column per declared attribute".

`deploy/UPGRADES.md`, a new section after the 017 one, in the house style of the neighbouring sections:

```markdown
### Upgrading to the consent flag (migration 018)

Views and product events gain a `consent` column: `1` when the client said
it had consent to keep anything on the device, `0` when it said it had
not, NULL when it said nothing. A `consent` views breakdown
(`v_views_consent`, `views_breakdown` with `dimension: "consent"`) reads it
as `given`, `none` and `unknown`.

Nothing to check first; the migration adds columns and a table and
rewrites no rows.

What changes on the day:

- Every existing view and event reads `unknown`: there is no record to
  backfill from, and `none` would claim a refusal nobody recorded. Rolled-up
  days get one `unknown` row carrying the day's totals.
- The `unknown` share shrinks as pages pick up the new SDK, which sends
  `$consent` on every batch (the served SDK is cached for a day).
- A hand-built client sends `$consent` itself or stays `unknown`.
```

- [ ] **Step 12: Run the docs binding tests**

Run: `go test ./internal/api -run 'TestDocument' -count=1`
Expected: PASS.

- [ ] **Step 13: Commit (controller)**

```bash
git add internal/store docs/twillingate.md deploy/UPGRADES.md internal/api/resources.go
git commit -m "feat(store): store a consent flag on views and events and roll it up daily"
```

---

### Task 2: Ingest — the `$consent` reserved key

**Files:**
- Modify: `internal/server/ingest.go` (`resolved`, `reservedKeys`, `parseConsent`)
- Modify: `internal/server/handlers.go:~124-190` (compute once, set on both row types)
- Test: `internal/server/server_test.go`
- Modify: `docs/twillingate.md` ("Reserved attribute keys", "Envelope")

**Interfaces:**
- Consumes: `store.Consent`, `store.ConsentUnknown`, `store.ConsentGiven`, `store.ConsentNone`; fields `store.View.Consent`, `store.ProductEvent.Consent` (Task 1).
- Produces: `func parseConsent(raw string) (c store.Consent, bad bool)` in package `server`.

- [ ] **Step 1: Write the failing test**

Add to `internal/server/server_test.go` (uses the existing `testServer`, `post`, `envelopeOf`, `decodeResult`, `testKey` helpers):

```go
// $consent is parsed in Go: the accepted spellings map to given/none,
// absence and null are unknown without a warning, anything else is
// unknown with one. Never a rejection.
func TestConsentParsing(t *testing.T) {
	cases := []struct {
		raw  string // JSON value, or "" for absent
		want store.Consent
		warn bool
	}{
		{`1`, store.ConsentGiven, false},
		{`1.0`, store.ConsentGiven, false},
		{`"1"`, store.ConsentGiven, false},
		{`true`, store.ConsentGiven, false},
		{`" TRUE "`, store.ConsentGiven, false},
		{`0`, store.ConsentNone, false},
		{`"0"`, store.ConsentNone, false},
		{`false`, store.ConsentNone, false},
		{`"False"`, store.ConsentNone, false},
		{``, store.ConsentUnknown, false},
		{`null`, store.ConsentUnknown, false},
		{`""`, store.ConsentUnknown, false},
		{`"maybe"`, store.ConsentUnknown, true},
		{`2`, store.ConsentUnknown, true},
	}
	for _, c := range cases {
		attr := ""
		if c.raw != "" {
			attr = `,"$consent":` + c.raw
		}
		q, h := testServer(t)
		res := decodeResult(t, post(h, envelopeOf(
			`{"name":"$page_view","attributes":{"$path":"/"`+attr+`}},
			 {"name":"signup","attributes":{"x":"y"`+attr+`}}`), nil))
		if res.Accepted != 2 || res.Rejected != 0 {
			t.Errorf("%s: result = %+v, want both accepted", c.raw, res)
			continue
		}
		if len(q.views) != 1 || len(q.events) != 1 {
			t.Fatalf("%s: views=%d events=%d", c.raw, len(q.views), len(q.events))
		}
		if q.views[0].Consent != c.want || q.events[0].Consent != c.want {
			t.Errorf("%s: view %v event %v, want %v", c.raw, q.views[0].Consent, q.events[0].Consent, c.want)
		}
		warned := 0
		for _, w := range res.Warnings {
			if strings.Contains(w.Reason, "$consent") {
				warned++
			}
		}
		if want := map[bool]int{true: 2, false: 0}[c.warn]; warned != want {
			t.Errorf("%s: %d $consent warnings, want %d (%+v)", c.raw, warned, want, res.Warnings)
		}
	}
}

// Batch-level $consent applies to every event; a per-event value
// overrides it key by key, including an explicit null, which is unknown.
func TestConsentPerEventOverridesBatch(t *testing.T) {
	q, h := testServer(t)
	body := `{"key":"` + testKey + `","attributes":{"$consent":1},"events":[
	  {"name":"a"},
	  {"name":"b","attributes":{"$consent":0}},
	  {"name":"c","attributes":{"$consent":null}}]}`
	post(h, body, nil)
	if len(q.events) != 3 {
		t.Fatalf("events = %+v", q.events)
	}
	want := []store.Consent{store.ConsentGiven, store.ConsentNone, store.ConsentUnknown}
	for i, e := range q.events {
		if e.Consent != want[i] {
			t.Errorf("event %s consent = %v, want %v", e.EventName, e.Consent, want[i])
		}
	}
}
```

Check `envelopeOf` wraps the events with the test key and no batch attributes; if its signature differs, adapt the call, not the assertions. If the fake queue collects events in a different field than `q.events`, use that.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/server -run 'TestConsent' -count=1`
Expected: FAIL — `$consent` is an unknown reserved key (warning "unknown reserved key $consent") and `Consent` stays unknown for the given/none cases.

- [ ] **Step 3: Implement**

In `internal/server/ingest.go`: add `consentRaw string` to `resolved` beside `displayWidthRaw`; add to `reservedKeys` directly after `"$session_id"` (so it sits with Identity):

```go
	"$consent":         func(r *resolved, v string) { r.consentRaw = v },
```

and beside `parseDisplay`:

```go
// parseConsent reads a declared $consent, trimmed and case-folded. Absent
// (or null, or "") is unknown and not a mistake; a value outside the
// accepted spellings is unknown too, and bad reports it so the handler
// warns. Never a rejection: a client sending a value this server does not
// know must not be handed a 4xx, which the retry rules treat as poison.
func parseConsent(raw string) (c store.Consent, bad bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return store.ConsentUnknown, false
	case "1", "true":
		return store.ConsentGiven, false
	case "0", "false":
		return store.ConsentNone, false
	}
	return store.ConsentUnknown, true
}
```

(import `github.com/dmtrkzntsv/twillingate/internal/store` in ingest.go if it is not already imported; `stringify` already renders `true`, `1` and `1.0` as `"true"`, `"1"`, `"1"`.)

In `internal/server/handlers.go`, directly after the `platform := res.declared(…)` line:

```go
		consent, badConsent := parseConsent(rv.consentRaw)
		if badConsent {
			res.warn(i, "unknown $consent value %q, ignored", rv.consentRaw)
		}
```

and set `Consent: consent` in the `store.ProductEvent{…}` literal and in the `store.View{…}` literal.

- [ ] **Step 4: Run the server package**

Run: `go test ./internal/server -count=1`
Expected: PASS.

- [ ] **Step 5: Document the key**

`docs/twillingate.md`:
- "Reserved attribute keys" table, Identity row: append `` `$consent` `` after `` `$session_id` ``.
- Directly under that table (before "An **unrecognized `$` key is dropped**"), one paragraph:

```markdown
`$consent` is whether the client had consent to keep anything on the device
when it sent the event: `1` (or `true`) given, `0` (or `false`) not given, as a
number, boolean or string in any case. Absent means unknown. Any other value is
stored as unknown with a warning, never rejected.
```

- "Envelope" sample: add `"$consent": 1` to the batch attributes, e.g. after `"$session_id": "018f1e5b-…",` so the line stays readable (re-wrap that line if it grows past the others).

- [ ] **Step 6: Run the docs binding tests**

Run: `go test ./internal/api -run 'TestDocument' -count=1`
Expected: PASS (`TestDocumentMatchesReservedKeys` finds `$consent` in both places).

- [ ] **Step 7: Commit (controller)**

```bash
git add internal/server/ingest.go internal/server/handlers.go internal/server/server_test.go docs/twillingate.md
git commit -m "feat(server): accept \$consent and store it on every view and event"
```

---

### Task 3: API — the `consent` breakdown dimension

**Files:**
- Modify: `internal/api/ops_read.go:159-188, ~228` (map, enum tag, tool description)
- Test: `internal/api/ops_read_test.go` (`TestViewsBreakdownEveryDimension`), `internal/api/seed_test.go` (fixture)
- Modify: `docs/twillingate.md` ("Answer questions with the data" table)

**Interfaces:**
- Consumes: view `v_views_consent(project_id, day, consent, visitors, views)` (Task 1); `store.View.Consent` (Task 1) for the fixture.
- Produces: `viewsDimensions["consent"] = {"v_views_consent", []string{"consent"}}`.

- [ ] **Step 1: Write the failing test**

In `internal/api/seed_test.go`, set `Consent: store.ConsentGiven` on at least one of the seeded views in August 2026 (the fixture `newTestHost` writes). In `internal/api/ops_read_test.go`, add `"consent": "given"` to the `want` map of `TestViewsBreakdownEveryDimension`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/api -run 'TestViewsBreakdownEveryDimension|TestBreakdownEnumMatchesDimensions' -count=1`
Expected: FAIL — `unknown dimension "consent"`.

- [ ] **Step 3: Implement**

In `internal/api/ops_read.go`:

```go
	"displays":     {"v_views_displays", []string{"display"}},
	"consent":      {"v_views_consent", []string{"consent"}},
```

The `Dimension` jsonschema tag must equal `"one of: " + dimensionNames()` (sorted), so it becomes:

```go
	Dimension string `json:"dimension" jsonschema:"one of: app_versions, browsers, consent, countries, devices, displays, hosts, kinds, os, paths, platforms, referrers, utm"`
```

In the `views_breakdown` tool Description: change "…, app_versions, devices or displays." to "…, app_versions, devices, displays or consent." and append " consent is given, none or unknown (sent without $consent, or stored before it existed)." at the end of the description.

- [ ] **Step 4: Run the API package**

Run: `go test ./internal/api -count=1`
Expected: PASS, including `TestDocumentCoversEveryViewsDimension` (Task 1 documented `v_views_consent`).

- [ ] **Step 5: Document the dimension**

`docs/twillingate.md`, the `views_breakdown` row of the tools table: change "`devices`, `displays`." to "`devices`, `displays`, `consent`." and append " `consent` is `given`, `none` or `unknown`." to that cell.

- [ ] **Step 6: Commit (controller)**

```bash
git add internal/api docs/twillingate.md
git commit -m "feat(api): break views down by consent"
```

---

### Task 4: SDK — send `$consent` on every batch

**Files:**
- Modify: `sdk/src/twillingate.ts:685-707` (`batchAttributes`)
- Test: `sdk/src/consent.test.ts` (inside the `"storage under consent"` describe block)
- Modify: `internal/server/twillingate.js` (rebuilt bundle)
- Modify: `internal/api/docs_sync_test.go` (`TestDocumentMatchesSDK` symbol list)
- Modify: `docs/twillingate.md` ("Consent and storage")

**Interfaces:**
- Consumes: the server accepts `$consent` as `1`/`0` (Task 2).
- Produces: every batch's `attributes.$consent` is `1` or `0`.

- [ ] **Step 1: Write the failing tests**

Add inside `describe("storage under consent", …)` in `sdk/src/consent.test.ts` (reuses `tg`, `lastAttributes`, `drain`, `sent`, `fetchImpl`, `okFetch`, `failFetch`):

```ts
  it("sends $consent on every batch, in both identity modes", async () => {
    for (const identity of ["anonymous", "identified"] as const) {
      expect((await lastAttributes(tg({ identity }))).$consent, identity).toBe(0);
      expect((await lastAttributes(tg({ identity, consent: true }))).$consent, identity).toBe(1);
    }
  });

  it("reflects consent(true|false) and a flipping consent manager at the next flush", async () => {
    const t = tg();
    expect((await lastAttributes(t)).$consent).toBe(0);
    t.consent(true);
    expect((await lastAttributes(t)).$consent).toBe(1);
    t.consent(false);
    expect((await lastAttributes(t)).$consent).toBe(0);

    let granted = false;
    const m = tg({ instance: "cmp", consent: () => granted });
    expect((await lastAttributes(m)).$consent).toBe(0);
    granted = true;
    expect((await lastAttributes(m)).$consent).toBe(1);
  });

  it("replays a failed batch with the $consent it was built with", async () => {
    fetchImpl = failFetch;
    const t = tg();
    t.track("while-refused");
    t.flush();
    await drain();
    fetchImpl = okFetch;
    t.consent(true);
    window.dispatchEvent(new Event("online"));
    await drain();
    const replayed = sent.find((s) => s.body.events.some((e) => e.name === "while-refused"));
    expect(replayed?.body.attributes.$consent).toBe(0);
    expect((await lastAttributes(t)).$consent).toBe(1);
  });
```

Check `InitOptions` for the exact option names (`instance`, `consent`) and that a second `Twillingate` with a distinct instance name is allowed in one test; if `instance` is not an `InitOptions` field, drop it and create the second instance the way `factory.test.ts` does.

- [ ] **Step 2: Run to verify they fail**

Run: `cd sdk && npx vitest run src/consent.test.ts`
Expected: FAIL — `$consent` is `undefined`.

- [ ] **Step 3: Implement**

In `batchAttributes()` in `sdk/src/twillingate.ts`, after `a.$kind = this.kind;`:

```ts
    // The answer the storage code acts on, read at flush time, so the
    // server can tell consented rows from the rest. Sent in both identity
    // modes: an anonymous instance's retry queue is still gated on it.
    a.$consent = this.mayStore() ? 1 : 0;
```

- [ ] **Step 4: Run the SDK suite, typecheck, build**

Run: `cd sdk && npm test && npm run typecheck && npm run build`
Expected: all tests PASS, no type errors, `internal/server/twillingate.js` rewritten. Existing tests that assert an exact attributes object (`toEqual` on the whole batch) may now fail on the extra key; update those expectations to include `$consent`, never loosen them to `toMatchObject` unless the test's intent is a subset.

- [ ] **Step 5: Bind and document**

`internal/api/docs_sync_test.go`, `TestDocumentMatchesSDK`: add `"$consent"` to the symbol list after `"$device",`.

`docs/twillingate.md`, "Consent and storage": after the sentence ending "…flipping to false deletes every key the instance owns.", add:

```markdown
Every batch carries `$consent` — `1` or `0`, the answer in force when it was
sent — in both identity modes, so the `consent` breakdown (`given`, `none`,
`unknown`) shows how many visitors consented.
```

- [ ] **Step 6: Run the Go docs tests and the server tests**

Run: `go test ./internal/api -run TestDocument -count=1 && go test ./internal/server -count=1`
Expected: PASS (the server embeds the rebuilt bundle).

- [ ] **Step 7: Commit (controller)**

```bash
git add sdk/src internal/server/twillingate.js internal/api/docs_sync_test.go docs/twillingate.md
git commit -m "feat(sdk): send \$consent on every batch"
```

---

### Task 5: Reporting — Evidence consent block and demo data

**Files:**
- Create: `evidence/sources/twillingate/v_views_consent.sql`
- Modify: `evidence/pages/views/[project].md` (queries + a "Consent" section)
- Modify: `scripts/seed-demo.py` (views and events carry consent)
- Modify: `internal/store/sqlite/zz_seed_test.go` (Evidence fixture carries consent)

**Interfaces:**
- Consumes: `v_views_consent` (Task 1), `store.Consent*` (Task 1).
- Produces: nothing downstream.

- [ ] **Step 1: The source, with the empty-database sentinel**

Create `evidence/sources/twillingate/v_views_consent.sql`:

```sql
-- Empty-database guard: the sqlite connector infers column types from the
-- first row, so a zero-row result throws "Cannot convert undefined or null to
-- object" and fails the whole source build -- taking every other query on the
-- page down with it. A fresh install has no traffic yet, so emit a sentinel
-- row when the view is empty; pages filter it out via their project_id clause.
select project_id, day, consent, visitors, views
from v_views_consent
union all
select 0, '1970-01-01', '', 0, 0
where not exists (select 1 from v_views_consent)
```

- [ ] **Step 2: The page block**

In `evidence/pages/views/[project].md`, after the Audience section's last chart (`<BarChart data={displays} …/>`) and before the `app_versions` query, add:

````markdown
```sql consent
select consent, sum(visitors) as visitors, sum(views) as views
from twillingate.v_views_consent
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by consent order by visitors desc
```

```sql consent_rate
select case when sum(case when consent in ('given', 'none') then visitors else 0 end) > 0
            then sum(case when consent = 'given' then visitors else 0 end) * 1.0
                 / sum(case when consent in ('given', 'none') then visitors else 0 end)
       end as rate
from twillingate.v_views_consent
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
```

## Consent

Whether the client could keep anything on the device when it sent the view.
`unknown` is every view sent without `$consent`, including all history before
the flag existed; the rate leaves it out.

<Grid cols=2>
    <BigValue data={consent_rate} value=rate fmt=pct1 title="Consent rate (given / (given + none))" />
    <DataTable data={consent} rows=3>
        <Column id=consent />
        <Column id=visitors fmt=num0 />
        <Column id=views fmt=num0 />
    </DataTable>
</Grid>
````

Match the page's existing component usage: grep the pages for `BigValue` and `DataTable` and copy their prop style (e.g. `fmt=` names) — adjust only the syntax, not the numbers shown.

- [ ] **Step 3: Demo and fixture data carry consent**

`scripts/seed-demo.py`, the web views INSERT: add `consent` to the column list, one `?`, and the value `1 if sends_ids else random.choice([0, 0, 1])` (an identified tag only sends an install id with consent; an anonymous tag's visitors split). Same for the product events INSERT (`1 if sends_ids else random.choice([0, 0, 1])`) and for the app views INSERT in `seed_app` (`1 if sends_ids else random.choice([0, 1])`). Run `python3 -m py_compile scripts/seed-demo.py`.

`internal/store/sqlite/zz_seed_test.go`: set `Consent: []store.Consent{store.ConsentGiven, store.ConsentNone, store.ConsentUnknown}[i%3]` on the seeded views so the Evidence fixture has all three values.

- [ ] **Step 4: Build the sources against an empty and a seeded database**

```bash
export PATH=/usr/local/go/bin:$PATH
S=$(mktemp -d)
DATABASE_DSN="sqlite://$S/empty.db" go run ./cmd/twillingate migrate
SEED_DB=$S/seeded.db go test ./internal/store/sqlite -run TestSeedEvidenceFixture -count=1
cd evidence && npm install --no-audit --no-fund >/dev/null
EVIDENCE_SOURCE__twillingate__filename=$S/empty.db npm run sources
EVIDENCE_SOURCE__twillingate__filename=$S/seeded.db npm run sources
```

Expected: both `npm run sources` runs finish without "could not read a source" / "Cannot convert undefined or null". Check the `DATABASE_DSN` / `migrate` invocation against the Makefile's `seed-demo` target and the filename form against its `dashboards` target (relative vs absolute). If `npm install` cannot reach the registry in this environment, report that instead of skipping silently (status DONE_WITH_CONCERNS). If there is time, `npm run build` with the seeded database renders the page.

- [ ] **Step 5: Commit (controller)**

```bash
git add evidence scripts/seed-demo.py internal/store/sqlite/zz_seed_test.go
git commit -m "feat(dashboards): show the consent split and rate on the views page"
```

---

### Finish

- [ ] `make check` (vet, coverage, restore test) passes.
- [ ] Whole-branch review (superpowers:requesting-code-review), findings fixed.
- [ ] Push `feat/consent-attribute`, open one PR titled `feat: record whether consent was given on every view and event`, problem-first body naming the three deviations above; close spec PR #51 pointing at it.
