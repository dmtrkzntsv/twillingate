package reporting

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	_ "github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
)

// testComponents parses testdata/components.json and returns its five
// components keyed by name, for tests that need a real, schema-resolved
// Component rather than one built by hand.
func testComponents(t *testing.T) map[string]Component {
	t.Helper()
	b, err := os.ReadFile("testdata/components.json")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := ParseManifest(b)
	if err != nil {
		t.Fatalf("ParseManifest(testdata): %v", err)
	}
	out := make(map[string]Component, len(cs))
	for _, c := range cs {
		out[c.Name] = c
	}
	return out
}

// oneComponent parses a single manifest entry (wrapped in the
// {"components":[...]} envelope ParseManifest expects) for a test that
// wants to hand-build one component's JSON.
func oneComponent(t *testing.T, entryJSON string) Component {
	t.Helper()
	cs, err := ParseManifest([]byte(`{"components":[` + entryJSON + `]}`))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(cs) != 1 {
		t.Fatalf("ParseManifest returned %d components, want 1", len(cs))
	}
	return cs[0]
}

// newTestStore opens a fresh, migrated store at a temp path and returns
// both it and the path, so a test can also open a read-only readsql.DB
// on the same file.
func newTestStore(t *testing.T) (store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reporting.db")
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, path
}

// newTestReadDB opens a migrated store and a read-only readsql.DB on the
// same file, for tests that exercise sqlSource against a real database.
func newTestReadDB(t *testing.T) *readsql.DB {
	t.Helper()
	_, path := newTestStore(t)
	db, err := readsql.Open(path, 2*time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
