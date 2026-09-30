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

// TestMigration022TriggerGivesInsertsTheirOwnGroup checks the
// dashboards_own_group trigger: a row inserted without naming group_id,
// as the binary before 022 does after a rollback, reads back as a group
// of its own, while a row naming a group keeps it.
func TestMigration022TriggerGivesInsertsTheirOwnGroup(t *testing.T) {
	db := newTestDBAt(t, 22)
	ctx := context.Background()
	for _, title := range []string{"First", "Second"} {
		if _, err := db.db.ExecContext(ctx,
			`INSERT INTO dashboards (owner, title, sort_key) VALUES ('user', ?, ?)`,
			title, "k"+title); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO dashboards (owner, title, sort_key, group_id)
		 SELECT 'user', 'Tab', 'kTab', id FROM dashboards WHERE title = 'First'`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.QueryContext(ctx, `SELECT title, id, group_id FROM dashboards WHERE owner = 'user'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct{ id, group int64 }
	got := map[string]row{}
	for rows.Next() {
		var title string
		var r row
		if err := rows.Scan(&title, &r.id, &r.group); err != nil {
			t.Fatal(err)
		}
		got[title] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got["First"].group != got["First"].id || got["Second"].group != got["Second"].id {
		t.Fatalf("rows without group_id: %v, want group_id = id each", got)
	}
	if got["First"].id == got["Second"].id || got["Tab"].group != got["First"].id {
		t.Fatalf("rows: %v, want Tab in First's group and First, Second apart", got)
	}
}
