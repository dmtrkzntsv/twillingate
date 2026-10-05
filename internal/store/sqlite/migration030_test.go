package sqlite

import "testing"

// Migration 030 renames server_stats to usage_history and keeps its rows.
func TestMigration030RenamesServerStats(t *testing.T) {
	db := newTestDBAt(t, 29)
	if _, err := db.db.Exec(`INSERT INTO server_stats (key, project_id, measured_at, value) VALUES ('views', 1, '2026-08-01', 7)`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrateThrough(t.Context(), 30); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.db.QueryRow(`SELECT value FROM usage_history WHERE key = 'views' AND project_id = 1 AND measured_at = '2026-08-01'`).Scan(&n); err != nil || n != 7 {
		t.Fatalf("usage_history row = %d, %v; want 7 carried over", n, err)
	}
	var left int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'server_stats'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("server_stats still exists (%d, %v)", left, err)
	}
}
