package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// seed023 seeds a pre-023 database (the one-events-table shape, where day is
// still a generated column) with raw views and product events on three days
// across two projects, plus one rolled-up row of each kind. It inserts with
// raw SQL rather than through WriteEvents: at this schema, WriteEvents would
// try to bind day, which does not exist as a bindable column yet (it is
// GENERATED ALWAYS AS (substr(ts,1,10)) STORED until 023 makes it plain).
func seed023(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	view := func(id string, project int64, day, kind, actor, path, user, group, consent string) string {
		name := "$screen_view"
		if kind == "web" {
			name = "$page_view"
		}
		return fmt.Sprintf(`INSERT INTO events (id, project_id, family, event_name, ts, received_at, kind,
			actor_id, actor_kind, user_id, group_id, host, path, referrer_source, utm_source, platform, os,
			os_version, browser, browser_version, device, browser_locale, app_locale, display_width, display_height,
			country, consent)
			VALUES ('%s', %d, 'views', '%s', '%sT10:00:00Z', '%sT10:00:00Z', '%s', '%s', 'connection', '%s', '%s',
			'shop.example.com', '%s', 'google', 'hn', 'web', 'macos', '14', 'safari', '17', 'desktop', 'de-DE',
			'en', 1440, 900, 'DE', %s)`,
			id, project, name, day, day, kind, actor, user, group, path, consent)
	}
	event := func(id string, project int64, day, name, actor, user, group, attrs string) string {
		return fmt.Sprintf(`INSERT INTO events (id, project_id, family, event_name, actor_id, ts, attributes,
			user_id, group_id, os, app_version, received_at, actor_kind, platform, consent, app_locale)
			VALUES ('%s', %d, 'product', '%s', '%s', '%sT11:00:00Z', '%s', '%s', '%s', 'ios', '2.4.1',
			'%sT11:00:00Z', 'user', 'ios', 1, 'de')`,
			id, project, name, actor, day, attrs, user, group, day)
	}
	for _, q := range []string{
		`INSERT INTO projects (id, name, attributes) VALUES (1, 'Site', '["plan"]')`,
		`INSERT INTO projects (id, name, attributes) VALUES (2, 'App', '["plan"]')`,
		view("v1", 1, "2026-09-20", "web", "a1", "/", "u1", "g1", "1"),
		view("v2", 1, "2026-09-20", "web", "a1", "/pricing", "u1", "g1", "1"),
		view("v3", 1, "2026-09-21", "app", "a2", "/home", "", "", "0"),
		view("v4", 2, "2026-09-22", "cli", "a3", "/run", "u3", "", "NULL"),
		view("v5", 2, "2026-09-20", "web", "a4", "/", "", "g2", "NULL"),
		event("e1", 1, "2026-09-20", "signup", "a1", "u1", "g1", `{"plan":"pro"}`),
		event("e2", 1, "2026-09-21", "signup", "a2", "", "", `{"plan":"free"}`),
		event("e3", 2, "2026-09-22", "export", "a1", "u1", "g1", `{}`),
		`INSERT INTO agg_views_paths (project_id, day, path, visitors, views) VALUES (1, '2026-09-01', '/', 4, 9)`,
		`INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users) VALUES (1, '2026-09-01', 'signup', 3, 2)`,
	} {
		if _, err := db.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// Migration 023 rebuilds events as a WITHOUT ROWID table clustered on
// (family, project_id, day, id). Every v_* view must answer exactly as it
// did before: nothing that reads events changes (spec decision 13).
func TestMigration023KeepsEveryViewsAnswer(t *testing.T) {
	ctx := context.Background()
	db := newTestDBAt(t, 22) // 022 does not exist on this branch; migrateThrough skips missing versions
	seed023(t, db)
	before := snapshotViews(t, db)
	for _, v := range []string{"v_views_daily", "v_views_paths", "v_product_daily", "v_product_attrs", "v_identity_daily"} {
		if len(before[v]) == 0 {
			t.Fatalf("%s empty before migrating", v)
		}
	}
	if err := db.migrateThrough(ctx, 23); err != nil {
		t.Fatal(err)
	}
	after := snapshotViews(t, db)
	for v, rows := range before {
		if strings.Join(rows, "\n") != strings.Join(after[v], "\n") {
			t.Errorf("%s changed across 023", v)
		}
	}
	var sql string
	db.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE name='events'`).Scan(&sql)
	if !strings.Contains(sql, "WITHOUT ROWID") || !strings.Contains(sql, "PRIMARY KEY (family, project_id, day, id)") {
		t.Errorf("events not clustered: %s", sql)
	}
	var n int
	db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND tbl_name='events' AND sql IS NOT NULL`).Scan(&n)
	if n != 0 {
		t.Errorf("events still has %d explicit indexes", n)
	}
	db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE day <> substr(ts,1,10)`).Scan(&n)
	if n != 0 {
		t.Errorf("%d rows with day != date(ts)", n)
	}
}

// A replayed batch is still ignored after 023: duplicates are detected on
// the whole primary key, and a retry repeats id, ts (hence day), family and
// project unchanged.
func TestMigration023ReplayIsIgnored(t *testing.T) {
	db := newTestDB(t)
	ev := store.Event{ID: "0190aaaa-0000-7000-8000-000000000001", ProjectID: 1, Family: store.FamilyProduct,
		EventName: "signup", TS: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC), ActorID: "a"}
	for i := 0; i < 2; i++ {
		if err := db.WriteEvents(context.Background(), []store.Event{ev}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM events WHERE id=?`, ev.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("replay stored %d rows", n)
	}
}
