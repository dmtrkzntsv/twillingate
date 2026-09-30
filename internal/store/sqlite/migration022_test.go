package sqlite

import (
	"context"
	"testing"
)

// TestMigration022GroupsEveryDashboardAlone checks every existing
// dashboard becomes a group of one: group_id = its own id.
func TestMigration022GroupsEveryDashboardAlone(t *testing.T) {
	db := newTestDBAt(t, 21)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `INSERT INTO dashboards (id, owner, title, sort_key) VALUES
		(1, 'system', 'Views', 'a0'), (1001, 'user', 'Mine', 'a0')`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrateThrough(ctx, 22); err != nil {
		t.Fatalf("migration 022: %v", err)
	}
	rows, err := db.db.QueryContext(ctx, `SELECT id, group_id FROM dashboards ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[int64]int64{}
	for rows.Next() {
		var id, g int64
		if err := rows.Scan(&id, &g); err != nil {
			t.Fatal(err)
		}
		got[id] = g
	}
	if got[1] != 1 || got[1001] != 1001 {
		t.Fatalf("group ids = %v, want 1→1 and 1001→1001", got)
	}
}
