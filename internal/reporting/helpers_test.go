package reporting

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
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
// on the same file. The file is a copy of storetest's.
func newTestStore(t *testing.T) (store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reporting.db")
	return storetest.Open(t, path), path
}

// newTestReadDB opens a migrated store and a read-only readsql.DB on the
// same file, for tests that exercise sqlSource against a real database
// but never write to it.
func newTestReadDB(t *testing.T) *readsql.DB {
	t.Helper()
	_, db := newTestStoreAndReadDB(t)
	return db
}

// newTestStoreAndReadDB opens a migrated store and a read-only
// readsql.DB on the same file, both left open for the test's duration —
// for a test that writes rows through the store (a project, some
// events) and then reads them back through sqlSource.
func newTestStoreAndReadDB(t *testing.T) (store.Store, *readsql.DB) {
	t.Helper()
	return newTestStoreAndReadDBMaxRows(t, 1000)
}

// newTestStoreAndReadDBMaxRows is newTestStoreAndReadDB with a caller-
// chosen row cap, for a test that wants readsql.Result.Truncated to
// actually trip.
func newTestStoreAndReadDBMaxRows(t *testing.T, maxRows int) (store.Store, *readsql.DB) {
	t.Helper()
	st, path := newTestStore(t)
	db, err := readsql.Open(path, 2*time.Second, maxRows)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return st, db
}

// rawExec reaches the underlying *sql.DB of the sqlite store, for
// seeding rows (agg_views_daily) or breaking them (dropping a table,
// writing a row that no longer fits a component) that no Store method
// exists to do directly.
func rawExec(t *testing.T, st store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.(interface {
		ExecForTest(string, ...any) (sql.Result, error)
	}).ExecForTest(q, args...); err != nil {
		t.Fatal(err)
	}
}

// mustCreateProject inserts a project and returns its id.
func mustCreateProject(t *testing.T, st store.Store, name string) int64 {
	t.Helper()
	id, err := st.CreateProject(context.Background(),
		store.RegistryProject{Name: name, AllowedOrigins: "[]", Attributes: "[]"},
		store.AuditEntry{Actor: "test", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// mustWriteEvent inserts one minimal event of family on day (a
// "2006-01-02" date) for projectID.
func mustWriteEvent(t *testing.T, st store.Store, id string, projectID int64, family store.Family, day string) {
	t.Helper()
	ts, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatal(err)
	}
	err = st.WriteEvents(context.Background(), []store.Event{{
		ID: id, ProjectID: projectID, Family: family, EventName: "$page_view",
		TS: ts, ActorID: "a",
	}})
	if err != nil {
		t.Fatal(err)
	}
}
