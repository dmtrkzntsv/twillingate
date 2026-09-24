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
		`INSERT INTO views (id, project_id, ts, received_at, kind, actor_id, actor_kind, path)
		 VALUES ('v1', 1, '2026-09-20T10:00:00Z', '2026-09-20T10:00:00Z', 'web', 'a1', 'connection', '/')`,
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
