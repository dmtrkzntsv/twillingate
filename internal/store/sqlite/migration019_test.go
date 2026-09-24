package sqlite

import (
	"context"
	"testing"
)

// The browser's language survives the rename; the product's language has
// no history to recover and starts empty. The locales view reads the
// renamed column straight away.
func TestMigration019RenamesLocale(t *testing.T) {
	db := newTestDBAt(t, 18)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO projects (id, name) VALUES (1, 'Site')`,
		`INSERT INTO views (id, project_id, ts, received_at, kind, actor_id, actor_kind, path, locale)
		 VALUES ('v1', 1, '2026-09-20T10:00:00Z', '2026-09-20T10:00:00Z', 'web', 'a1', 'connection', '/', 'de-DE')`,
	} {
		if _, err := db.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.migrateThrough(ctx, 19); err != nil {
		t.Fatalf("migration 019: %v", err)
	}
	if hasColumn(t, db, "views", "locale") {
		t.Fatal("views still has a locale column")
	}
	for _, tbl := range []string{"views", "events"} {
		if !hasColumn(t, db, tbl, "app_locale") {
			t.Fatalf("%s has no app_locale column", tbl)
		}
	}
	var browser, app string
	var visitors, views int
	if err := db.db.QueryRowContext(ctx, `SELECT browser_locale, app_locale, visitors, views FROM v_views_locales
		WHERE project_id=1 AND day='2026-09-20'`).Scan(&browser, &app, &visitors, &views); err != nil {
		t.Fatal(err)
	}
	if browser != "de-DE" || app != "" || visitors != 1 || views != 1 {
		t.Fatalf("live row = (%q, %q, %d, %d), want (de-DE, \"\", 1, 1)", browser, app, visitors, views)
	}
}
