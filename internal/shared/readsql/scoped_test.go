package readsql

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func scopedFixture(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.db")
	w, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE events (project_id INTEGER, day TEXT, path TEXT, PRIMARY KEY (project_id, day, path)) WITHOUT ROWID`,
		`CREATE TABLE identities (id TEXT, name TEXT)`,
		`CREATE TABLE projects (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE agg_views_paths (project_id INTEGER, day TEXT, path TEXT, views INTEGER)`,
		`CREATE VIEW raw_views AS SELECT * FROM events`,
		`CREATE VIEW v_identity_daily AS SELECT 1 AS project_id, '2026-10-11' AS day, 3 AS events, 2 AS actors`,
		`INSERT INTO events VALUES (1, '2026-10-11', '/a')`,
	} {
		if _, err := w.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	db, err := OpenScoped(path, 5*time.Second, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestScopedAllowsOnlyViewsAndAggregates(t *testing.T) {
	db := scopedFixture(t)
	ctx := context.Background()
	for _, q := range []string{
		`SELECT path FROM raw_views WHERE project_id = 1`,
		`SELECT events, actors FROM v_identity_daily`, // columns named like tables
		`SELECT * FROM agg_views_paths`,
		`WITH events AS (SELECT 1 AS x) SELECT x FROM events`, // a CTE may take any name
		`SELECT tw_cell(path), tw_num('2') FROM raw_views`,    // QueryPage's functions are registered
	} {
		if _, err := db.Query(ctx, q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
	for _, q := range []string{
		`SELECT * FROM events`,
		`SELECT * FROM identities`,
		`SELECT * FROM projects`,
		`SELECT * FROM main.events`,
		`SELECT * FROM temp.events`,
		`SELECT 1 WHERE 1 IN events`,
		`SELECT * FROM raw_views JOIN "events" USING (path)`,
	} {
		if _, err := db.Query(ctx, q); err == nil || !strings.Contains(err.Error(), "no such table") {
			t.Errorf("%s: err = %v, want no such table", q, err)
		}
	}
	// The temporary views are listed in sqlite_temp_master: a SQLite
	// internal name, which Check refuses on every handle.
	if _, err := db.Query(ctx, `SELECT * FROM sqlite_temp_master`); !errors.Is(err, ErrRefused) {
		t.Errorf("sqlite_temp_master: err = %v, want ErrRefused", err)
	}
	for _, q := range []string{
		`SELECT * FROM tw_store.events`,
		`SELECT * FROM "tw_store".events`,
		`SELECT * FROM [tw_store].events`,
		`SELECT * FROM TW_STORE.events`,
		"SELECT * FROM `tw_store`.events",
		`SELECT * FROM 'tw_store'.events`,
		`SELECT * FROM tw_store . events`,
	} {
		if _, err := db.Query(ctx, q); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: err = %v, want ErrRefused", q, err)
		}
	}
}

// TestScopedRefusesNamesItWasGiven: the names OpenScoped is given join
// the private schema's, as Open's do.
func TestScopedRefusesNamesItWasGiven(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	w, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`CREATE VIEW v_secret AS SELECT 1 AS x`); err != nil {
		t.Fatal(err)
	}
	w.Close()
	db, err := OpenScoped(path, 5*time.Second, 100, "v_secret")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Query(context.Background(), `SELECT * FROM v_secret`); !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
}

// TestScopedIsReadOnly: the connection is query_only once its views are
// made, so even trusted text run on it cannot write, temp schema included.
func TestScopedIsReadOnly(t *testing.T) {
	db := scopedFixture(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TEMP VIEW x AS SELECT 1`,
		`DROP VIEW temp.raw_views`,
		`INSERT INTO tw_store.events VALUES (2, '2026-10-11', '/b')`,
	} {
		if _, err := db.Run(ctx, q); err == nil {
			t.Errorf("%s: want an error", q)
		}
	}
}

func TestScopedFiltersReachTheTable(t *testing.T) {
	db := scopedFixture(t)
	res, err := db.Run(context.Background(), `EXPLAIN QUERY PLAN SELECT path FROM raw_views WHERE project_id = 1 AND day = '2026-10-11'`)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for _, r := range res.Rows {
		plan.WriteString(strings.Join(r, " ") + "\n")
	}
	if !strings.Contains(plan.String(), "SEARCH") {
		t.Errorf("plan scans instead of seeking:\n%s", plan.String())
	}
}

// TestScopedOnTheMigratedSchema runs the real views through the scoped
// handle: every raw_*, v_* and agg_* object of a migrated database reads,
// every other table and view is "no such table" even to trusted text (Run
// skips Check), and a project/day filter on a view still seeks events'
// key through the temporary view in front of it.
func TestScopedOnTheMigratedSchema(t *testing.T) {
	_, path := newTestDB(t, 5*time.Second, 100)
	db, err := OpenScoped(path, 30*time.Second, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	full, err := Open(path, 5*time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	objs, err := full.Run(ctx, `SELECT name FROM sqlite_schema WHERE type IN ('table', 'view') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	allowed := 0
	for _, r := range objs.Rows {
		name := r[0]
		if strings.HasPrefix(name, "sqlite_") {
			continue // refused by Check on every handle; no view may shadow one
		}
		_, err := db.Run(ctx, `SELECT * FROM "`+name+`" LIMIT 0`)
		ok := strings.HasPrefix(name, "raw_") || strings.HasPrefix(name, "v_") || strings.HasPrefix(name, "agg_")
		if ok {
			allowed++
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "no such table") {
			t.Errorf("%s: err = %v, want no such table", name, err)
		}
	}
	if allowed == 0 {
		t.Fatal("no raw_, v_ or agg_ object in the migrated schema")
	}

	res, err := db.Run(ctx, `EXPLAIN QUERY PLAN SELECT * FROM v_views_paths
		WHERE project_id = 1 AND day BETWEEN '2026-10-01' AND '2026-10-02'`)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for _, r := range res.Rows {
		plan.WriteString(strings.Join(r, " ") + "\n")
	}
	if !strings.Contains(plan.String(), "SEARCH events USING PRIMARY KEY (family=? AND project_id=? AND day>? AND day<?)") {
		t.Errorf("v_views_paths does not seek events:\n%s", plan.String())
	}
}
