package sqlite

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// dropped042 is every object migration 042 drops; migration 041 left
// each of them (TestMigration042DropsAfterTheImport checks both).
var dropped042 = []string{
	"v_views_daily", "v_views_paths", "v_views_hosts", "v_views_referrers", "v_views_utm",
	"v_views_countries", "v_views_platforms", "v_views_os", "v_views_browsers", "v_views_app_versions",
	"v_views_devices", "v_views_displays", "v_views_consent", "v_views_locales",
	"v_product_daily", "v_product_totals", "v_product_attrs", "v_measures_daily", "v_measures_attrs",
	"v_identity_daily", "v_retention",
	"raw_views", "raw_product", "raw_measures",
	"agg_views_daily", "agg_views_paths", "agg_views_hosts", "agg_views_referrers", "agg_views_utm",
	"agg_views_countries", "agg_views_platforms", "agg_views_os", "agg_views_browsers", "agg_views_app_versions",
	"agg_views_devices", "agg_views_displays", "agg_views_consent", "agg_views_locales",
	"agg_product_daily", "agg_product_totals", "agg_product_attrs", "agg_measures_daily", "agg_measures_attrs",
	"agg_retention", "agg_identity_daily",
	"actors", "identities", "events", "usage_attributes", "project_attributes",
}

// schemaObjects lists the tables and views sqlite_master holds, sorted.
func schemaObjects(t *testing.T, db *DB) []string {
	t.Helper()
	return queryStrings(t, db, `SELECT name FROM sqlite_master WHERE type IN ('table', 'view') ORDER BY name`)
}

func schemaVersion(t *testing.T, db *DB) string {
	t.Helper()
	return queryStrings(t, db, `SELECT MAX(version) FROM schema_migrations`)[0]
}

// legacyRow is one raw row as ingest wrote it through 041.
func legacyRow() store.Event {
	return store.Event{ID: "e1", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup",
		TS: rawAt("2026-10-11T10:00:00Z"), ReceivedAt: rawAt("2026-10-11T10:00:00Z"),
		ActorID: "a", ActorKind: store.ActorUser, Platform: "web", SampleRate: 1}
}

// Migration 042 refuses while events holds a row the import has not
// copied: the error names the command, and nothing is dropped or recorded.
func TestMigration042RefusesUnimportedEvents(t *testing.T) {
	db := newTestDBAt(t, 41)
	execAll(t, db, `INSERT INTO projects (id, name, sort_key) VALUES (1, 'p1', 'a1')`)
	insertLegacy(t, db, legacyRow())

	err := db.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), eventsNotImported) {
		t.Fatalf("Migrate = %v, want the refusal %q", err, eventsNotImported)
	}
	if !strings.Contains(err.Error(), "twillingate events import") {
		t.Fatalf("the refusal %q does not name the command", err)
	}
	if got := queryStrings(t, db, `SELECT id FROM events`); !slices.Equal(got, []string{"e1"}) {
		t.Fatalf("events after the refusal = %v, want the row kept", got)
	}
	if got := schemaVersion(t, db); got != "41" {
		t.Fatalf("schema version after the refusal = %s, want 41", got)
	}
}

// With events_imported set, 042 drops every listed object, keeps the
// metadata, forms and dashboards, and prunes the caps from meta and the
// dropped tables' stats from usage_history.
func TestMigration042DropsAfterTheImport(t *testing.T) {
	db := newTestDBAt(t, 41)
	objects := schemaObjects(t, db)
	for _, name := range dropped042 {
		if !slices.Contains(objects, name) {
			t.Fatalf("fixture: %s is missing at 41", name)
		}
	}
	execAll(t, db,
		`INSERT INTO projects (id, name, sort_key) VALUES (1, 'p1', 'a1')`,
		`INSERT INTO rules (project_id, kind, family, name, action, to_name) VALUES (1, 'event', 'product', 'old', 'rename', 'new')`,
		`INSERT INTO forms (project_id, name, created_at) VALUES (1, 'contact', '2026-10-11T10:00:00Z')`,
		`INSERT INTO submissions (project_id, form, id, received_at, fields) VALUES (1, 'contact', 's1', '2026-10-11T10:00:00Z', '{}')`,
		`INSERT INTO dashboards (id, owner, title, sort_key) VALUES (1000, 'user', 'Mine', 'zz')`,
		`INSERT INTO identities (project_id, kind, id, name) VALUES (1, 'user', 'u1', 'Ada')`,
		`INSERT INTO usage_attributes (project_id, day, attr_key, events) VALUES (1, '2026-10-11', 'plan', 1)`,
		`INSERT INTO project_attributes (project_id, key) VALUES (1, 'plan')`,
		`INSERT INTO meta (key, value) VALUES ('attributes_top_n', '100'), ('attribute_breakdowns_max', '10'),
			('identities_top_n', '1000'), ('events_imported', '2026-10-12T00:00:00Z'), ('salt', 'kept')`,
		`INSERT INTO usage_history (key, project_id, measured_at, value) VALUES
			('database_bytes', 0, '2026-10-11', 1), ('file_bytes', 1, '2026-10-11', 2), ('raw_bytes', 1, '2026-10-11', 3)`,
	)
	insertLegacy(t, db, legacyRow())

	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	objects = schemaObjects(t, db)
	for _, name := range dropped042 {
		if slices.Contains(objects, name) {
			t.Errorf("%s survived migration 042", name)
		}
	}
	for _, name := range objects {
		if strings.HasPrefix(name, "agg_") || strings.HasPrefix(name, "v_") || strings.HasPrefix(name, "raw_") {
			t.Errorf("%s survived migration 042", name)
		}
	}
	for q, want := range map[string][]string{
		`SELECT name || '>' || to_name FROM rules`:   {"old>new"},
		`SELECT name FROM forms`:                     {"contact"},
		`SELECT id FROM submissions`:                 {"s1"},
		`SELECT title FROM dashboards WHERE id=1000`: {"Mine"},
		`SELECT key FROM meta ORDER BY key`:          {"config_version", "events_imported", "salt"},
		`SELECT key FROM usage_history ORDER BY key`: {"database_bytes", "file_bytes"},
	} {
		if got := queryStrings(t, db, q); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", q, got, want)
		}
	}
}

// A fresh install never has the event schema: 042 runs on an empty events
// table, which needs no import.
func TestFreshInstallHasNoEventsTable(t *testing.T) {
	db := newTestDB(t)
	got := queryStrings(t, db, `SELECT name FROM sqlite_master
		WHERE name = 'events' OR name LIKE 'agg\_%' ESCAPE '\' OR name LIKE 'v\_%' ESCAPE '\' OR name LIKE 'raw\_%' ESCAPE '\'`)
	if len(got) != 0 {
		t.Fatalf("a fresh install holds %v", got)
	}
}

// An install from before 020 still has the flat view the process built
// then over its product events: the migrator drops it before each old
// migration (which rebuild events under it), and nothing builds it again.
func TestMigrateFromSnapshot19ThroughHead(t *testing.T) {
	db := newTestDBAt(t, 19)
	execAll(t, db,
		`INSERT INTO projects (id, name) VALUES (1, 'p1')`,
		`INSERT INTO events (id, project_id, event_name, actor_id, ts, attributes) VALUES
			('e1', 1, 'signup', 'a', '2026-09-20T10:00:00Z', '{"plan":"pro"}')`,
		`CREATE VIEW v_events_flat AS SELECT id, project_id, event_name, actor_id, consent, ts, attributes,
			json_extract(attributes, '$."plan"') AS attr_plan FROM events`,
		`INSERT INTO meta (key, value) VALUES ('events_imported', '2026-09-21T00:00:00Z')`,
	)
	if got := queryStrings(t, db, `SELECT attr_plan FROM v_events_flat`); !slices.Equal(got, []string{"pro"}) {
		t.Fatalf("fixture: v_events_flat reads %v", got)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := queryStrings(t, db, `SELECT name FROM sqlite_master WHERE name IN ('v_events_flat', 'events')`); len(got) != 0 {
		t.Fatalf("after migrating to the head: %v remain", got)
	}
}

// The import migrates through LastEventsVersion and no further: 042 is
// left pending, and EventsDropped tells the two sides apart.
func TestMigrateThroughStopsBefore042(t *testing.T) {
	db := newTestDBAt(t, 30)
	ctx := context.Background()
	if dropped, err := db.EventsDropped(ctx); err != nil || dropped {
		t.Fatalf("EventsDropped at 30 = %v, %v; want false", dropped, err)
	}
	if err := db.MigrateThrough(ctx, LastEventsVersion); err != nil {
		t.Fatal(err)
	}
	if got := schemaVersion(t, db); got != "41" {
		t.Fatalf("schema version = %s, want 41", got)
	}
	if dropped, err := db.EventsDropped(ctx); err != nil || dropped {
		t.Fatalf("EventsDropped at 41 = %v, %v; want false", dropped, err)
	}
	if dropped, err := newTestDB(t).EventsDropped(ctx); err != nil || !dropped {
		t.Fatalf("EventsDropped at the head = %v, %v; want true", dropped, err)
	}
}

// A database file nothing has migrated yet has no schema_migrations: its
// events are not dropped, the import migrates it first.
func TestEventsDroppedOnAnEmptyFile(t *testing.T) {
	db, err := openAt(t.TempDir() + "/empty.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if dropped, err := db.EventsDropped(context.Background()); err != nil || dropped {
		t.Fatalf("EventsDropped on an empty file = %v, %v; want false", dropped, err)
	}
}
