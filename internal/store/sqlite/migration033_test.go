package sqlite

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

func TestMigration033WidgetShares(t *testing.T) {
	db := newTestDBAt(t, 33)
	rows, err := db.db.QueryContext(context.Background(), `SELECT name FROM pragma_table_info('widget_shares')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"id", "widget_id", "project_id", "range_from", "range_to", "title",
		"project_name", "caption_project", "caption_range", "image", "image_2x", "created_at", "archive_at", "archived_at"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("columns = %v, want %v", got, want)
	}
}

// TestMigration033DeletingWidgetKeepsShare checks widget_id goes NULL, not
// the row, when its widget is deleted (foreign keys are on, as at runtime).
func TestMigration033DeletingWidgetKeepsShare(t *testing.T) {
	db := newTestDBAt(t, 33)
	execAll(t, db,
		`PRAGMA foreign_keys = ON`,
		`INSERT INTO projects (id, name, allowed_origins) VALUES (1001, 'blog', '[]')`,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id) VALUES (1001, 'user', 'D', 'a0', 1001)`,
		`INSERT INTO widgets (id, dashboard_id, sort_key, width, height, name, source_type, source)
			VALUES (2001, 1001, 'a', 1, 1, 'w', 'events', 'a')`,
		`INSERT INTO widget_shares (id, widget_id, project_id, range_from, range_to, title, project_name, image, image_2x)
			VALUES ('s1', 2001, 1001, '2026-09-01', '2026-09-30', 'T', 'blog', x'01', x'02')`,
		`DELETE FROM widgets WHERE id = 2001`)
	var wid *int64
	var created string
	if err := db.db.QueryRow(`SELECT widget_id, created_at FROM widget_shares WHERE id='s1'`).Scan(&wid, &created); err != nil {
		t.Fatal(err)
	}
	if wid != nil || created == "" {
		t.Fatalf("widget_id = %v, created_at = %q; want NULL and a default stamp", wid, created)
	}
}
