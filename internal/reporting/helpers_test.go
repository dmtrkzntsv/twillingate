package reporting

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
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

// TestMain lets the test binary serve as the engine child archivetest's
// files spawn (os.Executable), before it runs any test.
func TestMain(m *testing.M) {
	if archivetest.Child() {
		return
	}
	os.Exit(m.Run())
}

// storeDBs keeps each test store's read-only handle (newTestStore), by
// store, for a test that reads a row no Store method returns.
var storeDBs sync.Map // store.Store → *readsql.DB

// newTestStore opens a fresh, migrated store at a temp path and returns
// both it and the path. The file is a copy of storetest's.
func newTestStore(t *testing.T) (store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reporting.db")
	st := storetest.Open(t, path)
	db, err := readsql.Open(path, 2*time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	storeDBs.Store(st, db)
	t.Cleanup(func() { storeDBs.Delete(st); db.Close() })
	return st, path
}

// storeDB is st's read-only handle: metadata (audit_log, groups, tabs),
// never widget SQL, which runs on the event files.
func storeDB(t *testing.T, st Store) *readsql.DB {
	t.Helper()
	db, ok := storeDBs.Load(st)
	if !ok {
		t.Fatal("storeDB: the store was not opened by newTestStore")
	}
	return db.(*readsql.DB)
}

// svcDB is storeDB of the store svc was built on, which a test may have
// wrapped in svc.st since.
func svcDB(t *testing.T, svc *Service) *readsql.DB {
	t.Helper()
	return storeDB(t, filesOf(t, svc).st)
}

// testFiles is Files over an archivetest directory, as app's adapter is
// over the console's raw/: each query under the timeout, scoped to its
// project, with no rules (no test here makes one). The sample project is
// the store's live project with the newest file.
type testFiles struct {
	*archivetest.Files
	st      store.Store
	timeout time.Duration
	maxRows int
}

func (f *testFiles) Query(ctx context.Context, sql string, args []engine.Arg, maxRows int, project int64) (engine.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	return f.Reader.QueryScoped(ctx, archive.Scoped{SQL: sql, Args: args, MaxRows: maxRows, Project: project}, archive.Rules{})
}

func (f *testFiles) SampleProject(ctx context.Context) (int64, error) {
	ps, _, err := f.st.LoadRegistry(ctx)
	if err != nil {
		return 0, err
	}
	days, err := archive.LatestDays(f.Dir)
	if err != nil {
		return 0, err
	}
	var id int64
	var newest string
	for _, p := range ps { // a tie goes to the lowest id
		if day, ok := days[p.ID]; ok && p.ArchivedAt == "" && (day > newest || day == newest && p.ID < id) {
			id, newest = p.ID, day
		}
	}
	return id, nil
}

func (f *testFiles) Timeout() time.Duration { return f.timeout }
func (f *testFiles) MaxRows() int           { return f.maxRows }

// newTestStoreAndFiles opens a migrated store and an empty raw/ directory
// with an engine over it, both closed with the test; maxRows is
// CONSOLE_QUERY_ROWS_MAX, for a test that wants Truncated to trip.
func newTestStoreAndFiles(t *testing.T, maxRows int) (store.Store, *testFiles) {
	t.Helper()
	st, _ := newTestStore(t)
	return st, &testFiles{Files: archivetest.New(t), st: st, timeout: 10 * time.Second, maxRows: maxRows}
}

// newTestFiles is newTestStoreAndFiles for a test that needs no store of
// its own.
func newTestFiles(t *testing.T) *testFiles {
	t.Helper()
	_, f := newTestStoreAndFiles(t, 1000)
	return f
}

// filesOf is svc's test files, to write events a widget then reads.
func filesOf(t *testing.T, svc *Service) *testFiles {
	t.Helper()
	f, ok := svc.files.(*testFiles)
	if !ok {
		t.Fatalf("svc.files is %T, not the test's", svc.files)
	}
	return f
}

// event is one minimal event of family on day (YYYY-MM-DD) at noon UTC,
// for project, named name.
func event(t *testing.T, id string, project int64, family store.Family, name, day string) store.Event {
	t.Helper()
	ts, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatal(err)
	}
	ts = ts.Add(12 * time.Hour)
	return store.Event{ID: id, ProjectID: project, Family: family, EventName: name, TS: ts, ReceivedAt: ts,
		Kind: "web", ActorID: "a", ActorKind: "install"}
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

// mustWriteEvent writes one minimal event of family on day (YYYY-MM-DD)
// for projectID to f.
func mustWriteEvent(t *testing.T, f *testFiles, id string, projectID int64, family store.Family, day string) {
	t.Helper()
	f.Write(t, event(t, id, projectID, family, "$page_view", day))
}
