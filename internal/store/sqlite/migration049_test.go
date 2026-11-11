package sqlite

import (
	"context"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Migration 049 folds a monitor's contains and redirect_to (048) into
// expect: each moves in, and a monitor with neither expects nothing more.
func TestMigration049MovesExpectationsIntoExpect(t *testing.T) {
	db := newTestDBAt(t, 48)
	ctx := context.Background()
	const a, b, c, d = "https://a.example", "https://b.example", "https://c.example", "https://d.example"
	execAll(t, db,
		`INSERT INTO projects (id, name, allowed_origins) VALUES (7, 'm', '["`+a+`","`+b+`","`+c+`","`+d+`"]')`,
		`INSERT INTO monitors (project_id, origin, enabled, contains) VALUES (7, '`+a+`', 1, 'Say "hi"')`,
		`INSERT INTO monitors (project_id, origin, enabled, redirect_to) VALUES (7, '`+b+`', 1, '`+a+`')`,
		`INSERT INTO monitors (project_id, origin, enabled, contains, redirect_to) VALUES (7, '`+c+`', 1, 'x', '`+a+`')`,
		`INSERT INTO monitors (project_id, origin, enabled) VALUES (7, '`+d+`', 0)`)
	if err := db.migrateThrough(ctx, 49); err != nil {
		t.Fatal(err)
	}
	ms, err := db.ListMonitors(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.MonitorExpect{{Contains: `Say "hi"`}, {RedirectTo: a}, {Contains: "x", RedirectTo: a}, {}}
	if len(ms) != len(want) {
		t.Fatalf("after 049: %+v", ms)
	}
	for i, m := range ms {
		if m.Expect != want[i] {
			t.Errorf("%s: expect %+v, want %+v", m.Origin, m.Expect, want[i])
		}
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM monitors WHERE expect = '{}'`); n != 1 {
		t.Errorf("monitors with an empty expect = %d, want 1", n)
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
