package sqlite

import (
	"context"
	"testing"
)

func execScan(t *testing.T, db *DB, q string, dest ...any) {
	t.Helper()
	if err := db.db.QueryRowContext(context.Background(), q).Scan(dest...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func projectTabRows(t *testing.T, db *DB) map[[2]int64]bool {
	t.Helper()
	rows, err := db.db.QueryContext(context.Background(), `SELECT project_id, dashboard_id FROM project_tabs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[[2]int64]bool{}
	for rows.Next() {
		var p, d int64
		if err := rows.Scan(&p, &d); err != nil {
			t.Fatal(err)
		}
		got[[2]int64{p, d}] = true
	}
	return got
}

// TestMigration032Backfills: every project gets every built-in as a tab,
// built-ins get project_tab 1, and a hidden (archived) built-in group
// becomes sidebar 0 and live.
func TestMigration032Backfills(t *testing.T) {
	db := newTestDBAt(t, 31)
	execAll(t, db,
		`INSERT INTO projects (name) VALUES ('One'), ('Two')`,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id, archived_at) VALUES
			(1, 'system', 'Views', 'a0', 1, NULL),
			(2, 'system', 'Product', 'a1', 1, NULL),
			(8, 'system', 'Reach', 'a2', 8, '2026-09-01T00:00:00Z'),
			(1001, 'user', 'Mine', 'a0', 1001, NULL)`)
	if err := db.migrateThrough(context.Background(), 32); err != nil {
		t.Fatal(err)
	}
	var one, two int64
	execScan(t, db, `SELECT id FROM projects WHERE name='One'`, &one)
	execScan(t, db, `SELECT id FROM projects WHERE name='Two'`, &two)
	got := projectTabRows(t, db)
	for _, p := range []int64{one, two} {
		for _, d := range []int64{1, 2, 8} {
			if !got[[2]int64{p, d}] {
				t.Errorf("project %d lacks built-in tab %d: %v", p, d, got)
			}
		}
		if got[[2]int64{p, 1001}] {
			t.Errorf("project %d got user dashboard 1001 as a tab", p)
		}
	}
	var sidebar, projectTab int
	var archived *string
	execScan(t, db, `SELECT sidebar, project_tab, archived_at FROM dashboards WHERE id=8`, &sidebar, &projectTab, &archived)
	if sidebar != 0 || projectTab != 1 || archived != nil {
		t.Errorf("hidden built-in 8 = sidebar %d project_tab %d archived %v, want 0 1 nil", sidebar, projectTab, archived)
	}
	execScan(t, db, `SELECT sidebar, project_tab FROM dashboards WHERE id=1001`, &sidebar, &projectTab)
	if sidebar != 1 || projectTab != 0 {
		t.Errorf("user 1001 = sidebar %d project_tab %d, want 1 0", sidebar, projectTab)
	}
}

// TestMigration032SeedsNewProject: the trigger gives a project inserted by
// plain SQL (as the CLI and MCP paths do) every live project_tab dashboard.
func TestMigration032SeedsNewProject(t *testing.T) {
	db := newTestDBAt(t, 32)
	execAll(t, db,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id, project_tab) VALUES
			(1, 'system', 'Views', 'a0', 1, 1),
			(1001, 'user', 'Mine', 'a0', 1001, 1),
			(1002, 'user', 'Gone', 'a1', 1002, 1),
			(1003, 'user', 'Off', 'a2', 1003, 0)`,
		`UPDATE dashboards SET archived_at='2026-09-01T00:00:00Z' WHERE id=1002`,
		`INSERT INTO projects (name) VALUES ('New')`)
	var p int64
	execScan(t, db, `SELECT id FROM projects WHERE name='New'`, &p)
	got := projectTabRows(t, db)
	want := map[[2]int64]bool{{p, 1}: true, {p, 1001}: true}
	if len(got) != len(want) || !got[[2]int64{p, 1}] || !got[[2]int64{p, 1001}] {
		t.Fatalf("tabs = %v, want %v", got, want)
	}
}

// TestMigration032SeedsNewBuiltinOnce: a system dashboard inserted with
// project_tab 1 lands on every existing project; an upsert's update path
// (a later release) inserts nothing, so a removed tab stays removed.
func TestMigration032SeedsNewBuiltinOnce(t *testing.T) {
	db := newTestDBAt(t, 32)
	execAll(t, db,
		`INSERT INTO projects (name) VALUES ('A'), ('B')`,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id, project_tab) VALUES (9, 'system', 'New', 'a9', 9, 1)`)
	if n := len(projectTabRows(t, db)); n != 2 {
		t.Fatalf("rows after insert = %d, want 2", n)
	}
	execAll(t, db,
		`DELETE FROM project_tabs WHERE dashboard_id=9 AND project_id=(SELECT id FROM projects WHERE name='A')`,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id, project_tab) VALUES (9, 'system', 'New!', 'a9', 9, 1)
		 ON CONFLICT(id) DO UPDATE SET title=excluded.title, project_tab=excluded.project_tab`)
	if n := len(projectTabRows(t, db)); n != 1 {
		t.Fatalf("rows after upsert = %d, want 1 (the removed tab stays removed)", n)
	}
}

// TestMigration032Cascades: rows go with their project or dashboard.
func TestMigration032Cascades(t *testing.T) {
	db := newTestDBAt(t, 32)
	execAll(t, db,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id, project_tab) VALUES (1, 'system', 'Views', 'a0', 1, 1),
			(1001, 'user', 'Mine', 'a0', 1001, 1)`,
		`INSERT INTO projects (name) VALUES ('A'), ('B')`,
		`DELETE FROM projects WHERE name='A'`,
		`DELETE FROM dashboards WHERE id=1001`)
	got := projectTabRows(t, db)
	if len(got) != 1 {
		t.Fatalf("rows = %v, want only B's tab of dashboard 1", got)
	}
}
