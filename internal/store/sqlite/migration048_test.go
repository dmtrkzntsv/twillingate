package sqlite

import (
	"context"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Migration 048 folds a monitor's contains into expect: a text moves in,
// and a monitor with none expects nothing more.
func TestMigration048MovesContainsIntoExpect(t *testing.T) {
	db := newTestDBAt(t, 47)
	ctx := context.Background()
	execAll(t, db,
		`INSERT INTO projects (id, name, allowed_origins) VALUES (7, 'm', '["https://a.example","https://b.example"]')`,
		`INSERT INTO monitors (project_id, origin, enabled, contains) VALUES (7, 'https://a.example', 1, 'Say "hi"')`,
		`INSERT INTO monitors (project_id, origin, enabled) VALUES (7, 'https://b.example', 0)`)
	if err := db.migrateThrough(ctx, 48); err != nil {
		t.Fatal(err)
	}
	ms, err := db.ListMonitors(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Expect != (store.MonitorExpect{Contains: `Say "hi"`}) || !ms[0].Enabled || ms[1].Expect != (store.MonitorExpect{}) {
		t.Fatalf("after 048: %+v", ms)
	}
	if raw := countRows(t, db, `SELECT COUNT(*) FROM monitors WHERE expect = '{}'`); raw != 1 {
		t.Errorf("monitors with an empty expect = %d, want 1", raw)
	}
}

// A row whose expect is not a JSON object is an error, not a monitor that
// expects nothing: checking it as one would hide that it is broken.
func TestMonitorWithBrokenExpectIsAnError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db,
		`INSERT INTO projects (id, name, allowed_origins) VALUES (7, 'm', '["https://a.example"]')`,
		`INSERT INTO monitors (project_id, origin, enabled, expect) VALUES (7, 'https://a.example', 1, 'not json')`)
	if _, err := db.ListMonitors(ctx, 7); err == nil {
		t.Fatal("ListMonitors read a broken expect")
	}
	if _, err := db.ListEnabledMonitors(ctx); err == nil {
		t.Fatal("ListEnabledMonitors read a broken expect")
	}
}
