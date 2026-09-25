package sqlite

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// snapshotViews reads every v_* view except v_events_flat, each row
// rendered as text and the rows sorted, so two schemas can be compared
// for identical answers.
func snapshotViews(t *testing.T, db *DB) map[string][]string {
	t.Helper()
	names := []string{}
	rows, err := db.db.Query(`SELECT name FROM sqlite_schema WHERE type='view'
		AND name LIKE 'v\_%' ESCAPE '\' AND name <> 'v_events_flat' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	out := map[string][]string{}
	for _, n := range names {
		r, err := db.db.Query("SELECT * FROM " + n)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			parts := make([]string, len(vals))
			for i, v := range vals {
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				parts[i] = fmt.Sprintf("%v", v)
			}
			out[n] = append(out[n], strings.Join(parts, "|"))
		}
		r.Close()
		sort.Strings(out[n])
	}
	return out
}

// Migration 020 moves both raw tables into one. Every v_* view must answer
// exactly as it did at 19, for raw days and rolled-up days, views of every
// kind (web, app, cli) and product events alike.
func TestMigration020KeepsEveryViewsAnswer(t *testing.T) {
	db := newTestDBAt(t, 19)
	ctx := context.Background()
	view := func(id, day, kind, actor, path, user, group string, consent string) string {
		return fmt.Sprintf(`INSERT INTO views (id, project_id, ts, received_at, kind, actor_id, actor_kind,
			user_id, group_id, host, path, referrer_source, utm_source, platform, os, os_version, browser,
			browser_version, device, browser_locale, app_locale, display_width, display_height, country, consent)
			VALUES ('%s', 1, '%sT10:00:00Z', '%sT10:00:00Z', '%s', '%s', 'user', '%s', '%s', 'shop.example.com',
			'%s', 'google', 'hn', 'web', 'macos', '14', 'safari', '17', 'desktop', 'de-DE', 'en', 1440, 900, 'DE', %s)`,
			id, day, day, kind, actor, path, user, group, consent)
	}
	event := func(id, day, name, actor, user, group, attrs string) string {
		return fmt.Sprintf(`INSERT INTO events (id, project_id, event_name, actor_id, ts, attributes, user_id,
			group_id, os, app_version, received_at, actor_kind, platform, consent, app_locale)
			VALUES ('%s', 1, '%s', '%s', '%sT11:00:00Z', '%s', '%s', '%s', 'ios', '2.4.1', '%sT11:00:00Z',
			'user', 'ios', 1, 'de')`, id, name, actor, day, attrs, user, group, day)
	}
	for _, q := range []string{
		`INSERT INTO projects (id, name, attributes) VALUES (1, 'Site', '["plan"]')`,
		view("v1", "2026-09-20", "web", "a1", "/", "u1", "g1", "1"),
		view("v2", "2026-09-20", "web", "a1", "/pricing", "u1", "g1", "1"),
		view("v3", "2026-09-20", "app", "a2", "/home", "", "", "0"),
		view("v4", "2026-09-20", "cli", "a3", "/run", "u3", "", "NULL"),
		view("v5", "2026-09-21", "web", "a4", "/", "", "g2", "NULL"),
		event("e1", "2026-09-20", "signup", "a1", "u1", "g1", `{"plan":"pro"}`),
		event("e2", "2026-09-20", "signup", "a2", "", "", `{"plan":"free"}`),
		event("e3", "2026-09-21", "export", "a1", "u1", "g1", `{}`),
		`INSERT INTO agg_views_paths (project_id, day, path, visitors, views) VALUES (1, '2026-09-01', '/', 4, 9)`,
		`INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users) VALUES (1, '2026-09-01', 'signup', 3, 2)`,
	} {
		if _, err := db.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	before := snapshotViews(t, db)
	for _, must := range []string{"v_views_daily", "v_views_paths", "v_views_locales", "v_views_consent",
		"v_product_daily", "v_product_attrs", "v_identity_daily"} {
		if len(before[must]) == 0 {
			t.Fatalf("%s is empty before the migration; the comparison would be vacuous", must)
		}
	}
	if err := db.migrateThrough(ctx, 20); err != nil {
		t.Fatalf("migration 020: %v", err)
	}
	after := snapshotViews(t, db)
	for name, rows := range before {
		if strings.Join(rows, "\n") != strings.Join(after[name], "\n") {
			t.Errorf("%s changed:\nbefore %v\nafter  %v", name, rows, after[name])
		}
	}
	if hasTable(t, db, "views") {
		t.Error("the views table survived migration 020")
	}
	got := map[string]string{}
	rows, err := db.db.Query(`SELECT id, family || ' ' || event_name FROM events`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, v string
		if err := rows.Scan(&id, &v); err != nil {
			t.Fatal(err)
		}
		got[id] = v
	}
	rows.Close()
	want := map[string]string{
		"v1": "views $page_view", "v2": "views $page_view", "v3": "views $screen_view",
		"v4": "views $screen_view", "v5": "views $page_view",
		"e1": "product signup", "e2": "product signup", "e3": "product export",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("row %s = %q, want %q", id, got[id], w)
		}
	}
}
