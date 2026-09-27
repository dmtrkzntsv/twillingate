package sqlite

import (
	"context"
	"testing"
)

// TestMigration021Tables checks the four tables and three unique indexes
// migration 021 creates exist at that version.
func TestMigration021Tables(t *testing.T) {
	db := newTestDBAt(t, 21)
	for _, table := range []string{"components", "dashboards", "widgets", "reporting_migrations"} {
		if !hasTable(t, db, table) {
			t.Errorf("table %s missing", table)
		}
	}
	for _, idx := range []string{"dashboards_order", "widgets_order", "widgets_name"} {
		var n int
		if err := db.db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("index %s missing", idx)
		}
	}
}

// TestMigration021DashboardIdsStartAt1001 checks the reserved system range
// (spec D8): the sqlite_sequence seed means the first ordinary insert gets
// id 1001, and an explicit low id can still be inserted for a system
// dashboard without disturbing the counter.
func TestMigration021DashboardIdsStartAt1001(t *testing.T) {
	db := newTestDBAt(t, 21)
	ctx := context.Background()
	res, err := db.db.ExecContext(ctx,
		`INSERT INTO dashboards (owner, title, sort_key) VALUES ('user', 'Mine', 'a')`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if id != 1001 {
		t.Fatalf("first inserted dashboard id = %d, want 1001", id)
	}

	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO dashboards (id, owner, title, sort_key) VALUES (3, 'system', 'Overview', 'b')`); err != nil {
		t.Fatal(err)
	}

	res2, err := db.db.ExecContext(ctx,
		`INSERT INTO dashboards (owner, title, sort_key) VALUES ('user', 'Another', 'c')`)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := res2.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if id2 != 1002 {
		t.Fatalf("next auto id after an explicit id=3 insert = %d, want 1002", id2)
	}
}

// TestMigration021DeleteDashboardCascadesWidgets checks the FK from
// widgets to dashboards: ON DELETE CASCADE, with foreign keys enforced
// the way the writer connection opens (openAt's _pragma=foreign_keys(1)).
func TestMigration021DeleteDashboardCascadesWidgets(t *testing.T) {
	db := newTestDB(t) // opened via openAt: foreign_keys=ON
	ctx := context.Background()
	res, err := db.db.ExecContext(ctx,
		`INSERT INTO dashboards (owner, title, sort_key) VALUES ('user', 'Mine', 'a')`)
	if err != nil {
		t.Fatal(err)
	}
	dashID, _ := res.LastInsertId()
	if _, err := db.db.ExecContext(ctx, `INSERT INTO widgets
		(dashboard_id, sort_key, width, height, name, source_type, source)
		VALUES (?, 'a', 4, 4, 'w1', 'events', 'signup')`, dashID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `DELETE FROM dashboards WHERE id=?`, dashID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM widgets WHERE dashboard_id=?`, dashID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("widgets after deleting their dashboard = %d, want 0 (cascade)", n)
	}
}

// TestMigration021DeleteComponentNullsWidgets checks the FK from widgets
// to components: ON DELETE SET NULL (spec D14, "component removed").
func TestMigration021DeleteComponentNullsWidgets(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `INSERT INTO components
		(name, description, accepts, inputs, props, default_width, default_height)
		VALUES ('chart', 'a chart', '[]', '{}', '{}', 4, 4)`); err != nil {
		t.Fatal(err)
	}
	res, err := db.db.ExecContext(ctx,
		`INSERT INTO dashboards (owner, title, sort_key) VALUES ('user', 'Mine', 'a')`)
	if err != nil {
		t.Fatal(err)
	}
	dashID, _ := res.LastInsertId()
	wres, err := db.db.ExecContext(ctx, `INSERT INTO widgets
		(dashboard_id, component, sort_key, width, height, name, source_type, source)
		VALUES (?, 'chart', 'a', 4, 4, 'w1', 'events', 'signup')`, dashID)
	if err != nil {
		t.Fatal(err)
	}
	widgetID, _ := wres.LastInsertId()
	if _, err := db.db.ExecContext(ctx, `DELETE FROM components WHERE name='chart'`); err != nil {
		t.Fatal(err)
	}
	var component *string
	if err := db.db.QueryRowContext(ctx,
		`SELECT component FROM widgets WHERE id=?`, widgetID).Scan(&component); err != nil {
		t.Fatal(err)
	}
	if component != nil {
		t.Errorf("widget component after deleting it = %v, want NULL", *component)
	}
}

// TestMigration021FromSeededDatabasePassesCheck seeds a database at schema
// 20 (projects, keys, the way fk_test.go does) then migrates it through
// 021, checking foreign_key_check still comes back clean.
func TestMigration021FromSeededDatabasePassesCheck(t *testing.T) {
	db := newTestDBAt(t, 20)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO projects (id, name, attributes) VALUES (1, 'Site', '[]')`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrateThrough(ctx, 21); err != nil {
		t.Fatalf("migration 021: %v", err)
	}
}
