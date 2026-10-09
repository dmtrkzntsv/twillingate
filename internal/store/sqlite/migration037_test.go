package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/shared/sortkey"
)

// tabOrder lists a project's tab dashboard ids by row key alone.
func tabOrder(t *testing.T, db *DB, project int64) []int64 {
	t.Helper()
	rows, err := db.db.Query(`SELECT dashboard_id FROM project_tabs WHERE project_id=? ORDER BY sort_key, dashboard_id`, project)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// TestMigration037KeepsShownOrder checks the keys are rewritten into the
// order the page showed before: built-ins by dashboards.sort_key, then the
// user's by row key, an archived user row kept at its place.
func TestMigration037KeepsShownOrder(t *testing.T) {
	db := newTestDBAt(t, 36)
	// Two built-ins whose ids run against their sort_key, seeded onto the new
	// project by the 032 triggers; the order the page showed at 036 is read
	// back below, then user dashboards are added around them.
	execAll(t, db,
		`INSERT INTO dashboards (id, title, owner, sort_key, group_id, project_tab)
		 VALUES (9010, 'built-in b', 'system', 'a5', 9010, 1), (9011, 'built-in a', 'system', 'a4', 9011, 1)`,
		`INSERT INTO projects (id, name, sort_key) VALUES (901, 'p901', 'zz')`,
		`INSERT INTO dashboards (id, title, owner, sort_key, group_id, archived_at)
		 VALUES (9001, 'mine a', 'user', 'a0', 9001, NULL),
		        (9002, 'mine b', 'user', 'a1', 9002, '2026-10-01T00:00:00Z'),
		        (9003, 'mine c', 'user', 'a2', 9003, NULL)`,
		`INSERT INTO project_tabs (project_id, dashboard_id, sort_key)
		 VALUES (901, 9003, 'a3'), (901, 9002, 'a2'), (901, 9001, 'a1')`)
	var builtins []int64
	rows, err := db.db.Query(`SELECT d.id FROM project_tabs pt JOIN dashboards d ON d.id = pt.dashboard_id
		WHERE pt.project_id = 901 AND d.owner = 'system' ORDER BY d.sort_key, d.id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		builtins = append(builtins, id)
	}
	rows.Close()
	if len(builtins) == 0 {
		t.Fatal("no built-in tabs seeded on project 901")
	}
	want := append(builtins, 9001, 9002, 9003)

	if err := db.migrateThrough(context.Background(), 37); err != nil {
		t.Fatal(err)
	}
	if got := tabOrder(t, db, 901); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	keys, err := db.db.Query(`SELECT sort_key FROM project_tabs`)
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close()
	for keys.Next() {
		var k string
		_ = keys.Scan(&k)
		if _, err := sortkey.Between(k, ""); err != nil {
			t.Errorf("key %q is not a valid sort key: %v", k, err)
		}
	}
}

// TestMigration037NewBuiltinGoesLast checks a built-in a release adds
// lands after every tab of each project, whatever its dashboards.sort_key.
func TestMigration037NewBuiltinGoesLast(t *testing.T) {
	db := newTestDBAt(t, 37)
	execAll(t, db,
		`INSERT INTO dashboards (id, title, owner, sort_key, group_id, project_tab)
		 VALUES (9099, 'old built-in', 'system', 'a0', 9099, 1)`,
		`INSERT INTO projects (id, name, sort_key) VALUES (902, 'p902', 'zz')`,
		`INSERT INTO dashboards (id, title, owner, sort_key, group_id) VALUES (9101, 'mine', 'user', 'a0', 9101)`,
		`INSERT INTO project_tabs (project_id, dashboard_id, sort_key) VALUES (902, 9101, 'b05')`,
		`INSERT INTO dashboards (id, title, owner, sort_key, group_id, project_tab)
		 VALUES (9100, 'new built-in', 'system', '!', 9100, 1)`)
	if got, want := tabOrder(t, db, 902), []int64{9099, 9101, 9100}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// TestMigration037FormsSeen checks existing forms are seen at the upgrade
// and a form created later starts unseen.
func TestMigration037FormsSeen(t *testing.T) {
	db := newTestDBAt(t, 36)
	execAll(t, db, `INSERT INTO forms (project_id, name) VALUES (1, 'old')`)
	if err := db.migrateThrough(context.Background(), 37); err != nil {
		t.Fatal(err)
	}
	execAll(t, db, `INSERT INTO forms (project_id, name) VALUES (1, 'new')`)
	var old, fresh *string
	_ = db.db.QueryRow(`SELECT seen_at FROM forms WHERE name='old'`).Scan(&old)
	_ = db.db.QueryRow(`SELECT seen_at FROM forms WHERE name='new'`).Scan(&fresh)
	if old == nil || fresh != nil {
		t.Errorf("seen_at old=%v new=%v, want set and NULL", old, fresh)
	}
}
