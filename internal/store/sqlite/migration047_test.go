package sqlite

import (
	"context"
	"testing"
)

// Migration 047 adds the notification tables and forms.notify: a form
// present before it, approved or not, reads notify off.
func TestMigration047ExistingFormsNotifyOff(t *testing.T) {
	db := newTestDBAt(t, 46)
	ctx := context.Background()
	execAll(t, db,
		`INSERT INTO forms (project_id, name, status, expected_fields, approved_at)
			VALUES (1, 'contact', 'approved', '["email"]', '2026-10-06T11:00:00Z')`,
		`INSERT INTO forms (project_id, name, draft_until) VALUES (1, 'draft', '2026-12-01T00:00:00Z')`)
	if err := db.migrateThrough(ctx, 47); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"contact", "draft"} {
		f, err := db.GetForm(ctx, 1, name)
		if err != nil {
			t.Fatal(err)
		}
		if f.Notify {
			t.Errorf("%s: notify on after 047", name)
		}
	}
	for _, table := range []string{"notifications", "notify_states", "form_bursts"} {
		if c := countRows(t, db, `SELECT COUNT(*) FROM `+table); c != 0 {
			t.Errorf("%s has %d rows", table, c)
		}
	}
}
