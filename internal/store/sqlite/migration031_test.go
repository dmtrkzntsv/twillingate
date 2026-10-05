package sqlite

import (
	"context"
	"testing"
)

func groupNames(t *testing.T, db *DB) map[int64]string {
	t.Helper()
	rows, err := db.db.QueryContext(context.Background(), `SELECT group_id, title FROM dashboard_groups`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[int64]string{}
	for rows.Next() {
		var g int64
		var title string
		if err := rows.Scan(&g, &title); err != nil {
			t.Fatal(err)
		}
		got[g] = title
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func execAll(t *testing.T, db *DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := db.db.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// TestMigration031DeletesNameWithLastDashboard checks the name row stays
// while any dashboard of the group remains and goes with the last one.
func TestMigration031DeletesNameWithLastDashboard(t *testing.T) {
	db := newTestDBAt(t, 31)
	execAll(t, db,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id) VALUES
			(1001, 'user', 'A', 'a0', 1001), (1002, 'user', 'B', 'a1', 1001)`,
		`INSERT INTO dashboard_groups (group_id, title) VALUES (1001, 'Alpha')`,
		`DELETE FROM dashboards WHERE id=1002`)
	if got := groupNames(t, db); got[1001] != "Alpha" {
		t.Fatalf("names after deleting one of two = %v, want Alpha kept", got)
	}
	execAll(t, db, `DELETE FROM dashboards WHERE id=1001`)
	if got := groupNames(t, db); len(got) != 0 {
		t.Fatalf("names after deleting the last dashboard = %v, want none", got)
	}
}

// TestMigration031DeletesNameWhenGroupOfOneJoinsAnother checks a group
// whose only member changes group loses its name and the target keeps its.
func TestMigration031DeletesNameWhenGroupOfOneJoinsAnother(t *testing.T) {
	db := newTestDBAt(t, 31)
	execAll(t, db,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id) VALUES
			(1001, 'user', 'A', 'a0', 1001), (1002, 'user', 'B', 'a1', 1002)`,
		`INSERT INTO dashboard_groups (group_id, title) VALUES (1001, 'Alpha'), (1002, 'Beta')`,
		`UPDATE dashboards SET group_id=1002 WHERE id=1001`)
	got := groupNames(t, db)
	if _, ok := got[1001]; ok || got[1002] != "Beta" || len(got) != 1 {
		t.Fatalf("names after the join = %v, want only 1002=Beta", got)
	}
}

// TestMigration031KeepsNameWhileMembersRemain checks an update that does
// not change group_id, or leaves members behind, keeps the row.
func TestMigration031KeepsNameWhileMembersRemain(t *testing.T) {
	db := newTestDBAt(t, 31)
	execAll(t, db,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id) VALUES (1001, 'user', 'A', 'a0', 1001)`,
		`INSERT INTO dashboard_groups (group_id, title) VALUES (1001, 'Alpha')`,
		`UPDATE dashboards SET sort_key='a5' WHERE id=1001`,
		`UPDATE dashboards SET group_id=1001 WHERE id=1001`)
	if got := groupNames(t, db); got[1001] != "Alpha" {
		t.Fatalf("names after no-op updates = %v, want Alpha kept", got)
	}
}
