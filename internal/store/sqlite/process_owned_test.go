package sqlite

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// The migrations that wrote v_events_flat before the process owned it.
// Applied migrations never change, so they keep their DROP/CREATE pairs;
// the migrator drops the view before each later one anyway.
var processOwnedGrandfathered = map[int]bool{3: true, 12: true, 14: true, 20: true, 23: true, 24: true}

// No migration past those names a process-owned object: the process
// builds them from the data, and a migration's fixed text would replace
// that with a stale shape (v_events_flat's attr_ columns, which widgets
// and BI queries read).
func TestMigrationsNeverNameProcessOwnedObjects(t *testing.T) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		version, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			t.Fatal(err)
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range processOwned {
			named := strings.Contains(strings.ToLower(string(body)), name)
			if named && !processOwnedGrandfathered[version] {
				t.Errorf("%s names %s, which the process owns (processOwned in migrate.go); drop the reference", e.Name(), name)
			}
			if !named && processOwnedGrandfathered[version] {
				t.Errorf("%s is grandfathered for %s but does not name it; remove it from processOwnedGrandfathered", e.Name(), name)
			}
		}
	}
}

// A pending migration finds no v_events_flat and leaves none, whatever the
// view held: the caller's rebuild after migrating is what brings the
// declared attribute columns back.
func TestMigrateDropsProcessOwnedViews(t *testing.T) {
	db := newTestDBAt(t, 36)
	ctx := context.Background()
	if err := db.RebuildFlatView(ctx, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	if !viewColumns(t, db, "v_events_flat")["attr_plan"] {
		t.Fatal("fixture: no attr_plan before migrating")
	}
	if err := db.migrateThrough(ctx, 37); err != nil {
		t.Fatal(err)
	}
	if def, err := db.flatViewDefinition(ctx); err != nil || def != "" {
		t.Fatalf("v_events_flat after a migration = %q (%v), want it dropped", def, err)
	}
	if err := db.RebuildFlatView(ctx, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	if !viewColumns(t, db, "v_events_flat")["attr_plan"] {
		t.Fatal("the rebuild did not bring attr_plan back")
	}

	// With nothing pending the migrator leaves the view alone.
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if !viewColumns(t, db, "v_events_flat")["attr_plan"] {
		t.Fatal("Migrate with nothing pending dropped the view")
	}
}
