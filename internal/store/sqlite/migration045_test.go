package sqlite

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// Migration 045 lets a widget share have no project: an existing share
// keeps every column, its widget's ON DELETE SET NULL and both indexes;
// a share with no project names none, and a project with its name.
func TestMigration045ProjectlessShares(t *testing.T) {
	db := newTestDBAt(t, 44)
	ctx := context.Background()
	execAll(t, db,
		`INSERT INTO projects (id, name, allowed_origins) VALUES (1001, 'blog', '[]')`,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id) VALUES (1001, 'user', 'D', 'a0', 1001)`,
		`INSERT INTO widgets (id, dashboard_id, sort_key, width, height, name, source_type, source)
			VALUES (2001, 1001, 'a', 1, 1, 'w', 'md', 'a')`,
		`INSERT INTO widget_shares (id, widget_id, project_id, range_from, range_to, title, project_name,
			caption_project, caption_range, image, image_2x, created_at, archive_at, archived_at)
			VALUES ('s1', 2001, 1001, '2026-09-01', '2026-09-30', 'T', 'blog', 1, 0, x'01', x'02',
			'2026-10-01T00:00:00Z', '2026-11-01T00:00:00Z', NULL)`)
	const all = `SELECT id, widget_id, project_id, range_from, range_to, title, project_name, caption_project,
		caption_range, hex(image), hex(image_2x), created_at, COALESCE(archive_at, '-'), COALESCE(archived_at, '-')
		FROM widget_shares`
	before := queryStrings(t, db, all)
	if err := db.migrateThrough(ctx, 45); err != nil {
		t.Fatal(err)
	}
	if got := queryStrings(t, db, all); !reflect.DeepEqual(got, before) {
		t.Fatalf("rows = %q, want %q", got, before)
	}
	if got, want := queryStrings(t, db, `SELECT name FROM sqlite_schema
		WHERE type = 'index' AND tbl_name = 'widget_shares' AND sql IS NOT NULL ORDER BY name`),
		[]string{"idx_widget_shares_archive", "idx_widget_shares_widget"}; !reflect.DeepEqual(got, want) {
		t.Errorf("indexes = %q, want %q", got, want)
	}

	execAll(t, db, `PRAGMA foreign_keys = ON`,
		`INSERT INTO widget_shares (id, widget_id, project_id, range_from, range_to, title, project_name,
			caption_project, image, image_2x) VALUES ('s2', 2001, NULL, '2026-09-01', '2026-09-30', 'T', NULL, 0, x'01', x'02')`,
		`DELETE FROM widgets WHERE id = 2001`)
	if got, want := queryStrings(t, db, `SELECT id, COALESCE(widget_id, '-') FROM widget_shares ORDER BY id`),
		[]string{"s1|-", "s2|-"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after deleting the widget: %q, want %q", got, want)
	}
	for _, bad := range []string{
		`INSERT INTO widget_shares (id, project_id, range_from, range_to, title, project_name, caption_project, image, image_2x)
			VALUES ('b1', NULL, '2026-09-01', '2026-09-30', 'T', 'blog', 0, x'01', x'02')`,
		`INSERT INTO widget_shares (id, project_id, range_from, range_to, title, project_name, caption_project, image, image_2x)
			VALUES ('b2', 1001, '2026-09-01', '2026-09-30', 'T', NULL, 1, x'01', x'02')`,
		`INSERT INTO widget_shares (id, project_id, range_from, range_to, title, project_name, caption_project, image, image_2x)
			VALUES ('b3', NULL, '2026-09-01', '2026-09-30', 'T', NULL, 1, x'01', x'02')`,
	} {
		if _, err := db.db.ExecContext(ctx, bad); err == nil || !strings.Contains(err.Error(), "CHECK") {
			t.Errorf("%s: err = %v, want a CHECK failure", bad, err)
		}
	}
}
