package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// testManifest reads testdata/components.json, the same five-component
// manifest the rest of the package's tests use.
func testManifest(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/components.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// systemFSCopy copies testdata/system into a fresh temp directory and
// returns both an fs.FS over it and the directory itself, so a test can
// mutate a widget's file between two migrateFrom runs (testdata itself
// stays read-only, shared by every test).
func systemFSCopy(t *testing.T) (fs.FS, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/system")); err != nil {
		t.Fatal(err)
	}
	return os.DirFS(dir), dir
}

// reportingMigrationCount reads reporting_migrations' row count straight
// off db — store.Store has no such accessor, only ReportingHash (the
// latest row), and this needs the count.
func reportingMigrationCount(t *testing.T, db *readsql.DB) int {
	t.Helper()
	res, err := db.Run(context.Background(), `SELECT COUNT(*) FROM reporting_migrations`)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(res.Rows[0][0])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// manifestWithout returns testManifest with the named component removed,
// for a test that resyncs with a component dropped.
func manifestWithout(t *testing.T, drop string) []byte {
	t.Helper()
	var m manifest
	if err := json.Unmarshal(testManifest(t), &m); err != nil {
		t.Fatal(err)
	}
	kept := m.Components[:0]
	for _, c := range m.Components {
		if c.Name != drop {
			kept = append(kept, c)
		}
	}
	m.Components = kept
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// systemDashboardsOf filters ds to the system-owned rows.
func systemDashboardsOf(ds []store.Dashboard) []store.Dashboard {
	var out []store.Dashboard
	for _, d := range ds {
		if d.Owner == store.OwnerSystem {
			out = append(out, d)
		}
	}
	return out
}

func TestMigrateFromFirstRunInsertsDashboards(t *testing.T) {
	st, files := newTestStoreAndFiles(t, 1000)
	db := storeDB(t, st)
	ctx := context.Background()
	system, _ := systemFSCopy(t)

	if err := migrateFrom(ctx, st, files, system, testManifest(t)); err != nil {
		t.Fatal(err)
	}

	ds, err := st.ListDashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sys := systemDashboardsOf(ds)
	if len(sys) != 2 {
		t.Fatalf("system dashboards = %d, want 2", len(sys))
	}

	ws1, err := st.ListWidgets(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws1) != 1 || ws1[0].Name != "users" {
		t.Errorf("dashboard 1 widgets = %+v", ws1)
	}
	ws2, err := st.ListWidgets(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws2) != 1 || ws2[0].Name != "note" {
		t.Errorf("dashboard 2 widgets = %+v", ws2)
	}

	// Each file's placement flags land on its row (testdata's overview
	// ships out of the sidebar and off new projects).
	for _, d := range sys {
		if want := d.ID == 1; d.Sidebar != want || d.ProjectTab != want {
			t.Errorf("dashboard %d sidebar %v project_tab %v, want %v, %v", d.ID, d.Sidebar, d.ProjectTab, want, want)
		}
	}

	if reportingMigrationCount(t, db) != 1 {
		t.Errorf("reporting_migrations rows = %d, want 1", reportingMigrationCount(t, db))
	}
}

// The release's own system directory lands every built-in in the sidebar
// and on new projects' tabs (spec 2026-10-05 D6).
func TestMigrateReleaseBuiltinsInSidebarAndProjectTabs(t *testing.T) {
	st, files := newTestStoreAndFiles(t, 1000)
	ctx := context.Background()
	if err := Migrate(ctx, st, files); err != nil {
		t.Fatal(err)
	}
	ds, err := st.ListDashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sys := systemDashboardsOf(ds)
	if len(sys) == 0 {
		t.Fatal("no system dashboards")
	}
	for _, d := range sys {
		if !d.Sidebar || !d.ProjectTab {
			t.Errorf("built-in %d sidebar %v project_tab %v, want both true", d.ID, d.Sidebar, d.ProjectTab)
		}
	}
}

func TestMigrateFromSecondRunWithSameInputsWritesNothing(t *testing.T) {
	st, files := newTestStoreAndFiles(t, 1000)
	db := storeDB(t, st)
	ctx := context.Background()
	system, _ := systemFSCopy(t)
	manifest := testManifest(t)

	if err := migrateFrom(ctx, st, files, system, manifest); err != nil {
		t.Fatal(err)
	}
	before := reportingMigrationCount(t, db)

	if err := migrateFrom(ctx, st, files, system, manifest); err != nil {
		t.Fatal(err)
	}
	after := reportingMigrationCount(t, db)

	if before != after {
		t.Errorf("reporting_migrations rows changed on an identical rerun: %d -> %d", before, after)
	}
}

func TestMigrateFromChangedWidgetKeepsIDBumpsCountKeepsView(t *testing.T) {
	st, files := newTestStoreAndFiles(t, 1000)
	db := storeDB(t, st)
	ctx := context.Background()
	system, dir := systemFSCopy(t)
	manifest := testManifest(t)

	if err := migrateFrom(ctx, st, files, system, manifest); err != nil {
		t.Fatal(err)
	}
	before := reportingMigrationCount(t, db)

	ws, err := st.ListWidgets(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var widgetID int64
	for _, w := range ws {
		if w.Name == "users" {
			widgetID = w.ID
		}
	}
	if widgetID == 0 {
		t.Fatal("widget users not found after first run")
	}

	// A viewer picks a project/range in between runs.
	if err := st.SetDashboardView(ctx, store.Dashboard{
		ID: 1, LastProjectID: 7, LastRange: "custom", LastFrom: "2026-01-01", LastTo: "2026-01-02",
	}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "retention", "users.sql"), []byte("SELECT 2 AS value"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := migrateFrom(ctx, st, files, system, manifest); err != nil {
		t.Fatal(err)
	}
	after := reportingMigrationCount(t, db)
	if after != before+1 {
		t.Errorf("reporting_migrations rows = %d, want %d", after, before+1)
	}

	ws, err = st.ListWidgets(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var got store.Widget
	for _, w := range ws {
		if w.Name == "users" {
			got = w
		}
	}
	if got.ID != widgetID {
		t.Errorf("widget id changed: %d -> %d, want kept", widgetID, got.ID)
	}
	if got.Source != "SELECT 2 AS value" {
		t.Errorf("widget source = %q, want the changed content", got.Source)
	}

	dash, err := st.GetDashboard(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dash.LastProjectID != 7 || dash.LastRange != "custom" ||
		dash.LastFrom != "2026-01-01" || dash.LastTo != "2026-01-02" {
		t.Errorf("view not preserved across resync: %+v", dash)
	}
}

func TestMigrateFromRemovedComponentNullsUserWidget(t *testing.T) {
	st, files := newTestStoreAndFiles(t, 1000)
	ctx := context.Background()
	system, _ := systemFSCopy(t)

	if err := migrateFrom(ctx, st, files, system, testManifest(t)); err != nil {
		t.Fatal(err)
	}

	userDashID, err := st.InsertDashboard(ctx,
		store.Dashboard{Owner: store.OwnerUser, Title: "Mine", SortKey: "z", Sidebar: true},
		[]store.Widget{{
			Component: "pie", SortKey: "a", Width: 4, Height: 8,
			Name: "w1", SourceType: "sql", Source: "select 'a' as label, 1 as value",
		}},
		store.AuditEntry{Actor: "test", Action: "dashboard.create"})
	if err != nil {
		t.Fatal(err)
	}

	if err := migrateFrom(ctx, st, files, system, manifestWithout(t, "pie")); err != nil {
		t.Fatal(err)
	}

	ws, err := st.ListWidgets(ctx, userDashID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].Component != "" {
		t.Fatalf("user widget after component drop = %+v, want Component empty", ws)
	}
}

func TestMigrateFromInvalidSystemWidgetFailsAndWritesNothing(t *testing.T) {
	st, files := newTestStoreAndFiles(t, 1000)
	db := storeDB(t, st)
	ctx := context.Background()
	system, dir := systemFSCopy(t)
	manifest := testManifest(t)

	if err := migrateFrom(ctx, st, files, system, manifest); err != nil {
		t.Fatal(err)
	}
	goodHash, err := st.ReportingHash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := reportingMigrationCount(t, db)

	// stat needs a "value" column; this SQL doesn't have one.
	if err := os.WriteFile(filepath.Join(dir, "retention", "users.sql"), []byte("SELECT 1 AS wrong"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = migrateFrom(ctx, st, files, system, manifest)
	if err == nil {
		t.Fatal("want error: system widget missing a required column")
	}
	wantPrefix := "reporting: system dashboard 1 widget users:"
	if got := err.Error(); len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Errorf("error = %q, want prefix %q", got, wantPrefix)
	}

	hash, err := st.ReportingHash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hash != goodHash {
		t.Errorf("ReportingHash after failed migration = %q, want unchanged %q", hash, goodHash)
	}
	if got := reportingMigrationCount(t, db); got != before {
		t.Errorf("reporting_migrations rows = %d, want unchanged %d", got, before)
	}

	got, err := st.ListWidgets(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range got {
		if w.Name == "users" && w.Source != "SELECT 1 AS value\n" {
			t.Errorf("widget source changed by failed migration: %q", w.Source)
		}
	}
}

// countingFiles is Files that counts its queries and, with err set, fails
// each with it rather than run it.
type countingFiles struct {
	Files
	err     error
	queries int
}

func (f *countingFiles) Query(ctx context.Context, q archive.Scoped) (engine.Result, error) {
	f.queries++
	if f.err != nil {
		return engine.Result{}, f.err
	}
	return f.Files.Query(ctx, q)
}

// TestMigrateFromResourceFailureSyncsAndChecksAgain: a system widget's
// check that runs out of time or memory, or whose child dies, does not
// fail the start (the pass that would free the engine runs only after
// it). Migrate syncs the release, checks the widgets after it without a
// query, says so with ErrUnchecked, and the next run checks them all.
func TestMigrateFromResourceFailureSyncsAndChecksAgain(t *testing.T) {
	ctx := context.Background()
	manifest := testManifest(t)
	for name, cause := range map[string]error{
		"timeout":            context.DeadlineExceeded,
		"out of memory":      &engine.QueryError{Msg: "Out of Memory Error: failed to allocate data of size 64.0 MiB"},
		"logs out of memory": fmt.Errorf("%w: %w", archive.ErrLoadingLogs, &engine.QueryError{Msg: "Out of Memory Error: x"}),
		"child exited":       engine.ErrChildExited,
	} {
		t.Run(name, func(t *testing.T) {
			st, files := newTestStoreAndFiles(t, 1000)
			system, dir := systemFSCopy(t)
			addSystemWidget(t, dir, "visits", "SELECT 2 AS value")

			failing := &countingFiles{Files: files, err: cause}
			err := migrateFrom(ctx, st, failing, system, manifest)
			if !errors.Is(err, ErrUnchecked) || !strings.Contains(err.Error(), "widget users") {
				t.Fatalf("err = %v, want ErrUnchecked naming widget users", err)
			}
			if failing.queries != 1 {
				t.Errorf("%d queries, want none after the first failed", failing.queries)
			}
			ws, err := st.ListWidgets(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(ws) != 2 {
				t.Errorf("dashboard 1 widgets = %d, want the release's 2 synced", len(ws))
			}
			hash, err := st.ReportingHash(ctx)
			if err != nil || !strings.HasPrefix(hash, uncheckedPrefix) {
				t.Fatalf("ReportingHash = %q, %v; want it marked unchecked", hash, err)
			}

			// The next run checks every widget again, and records the hash.
			working := &countingFiles{Files: files}
			if err := migrateFrom(ctx, st, working, system, manifest); err != nil {
				t.Fatal(err)
			}
			if working.queries != 2 {
				t.Errorf("next run: %d queries, want both widgets checked", working.queries)
			}
			if hash, _ := st.ReportingHash(ctx); strings.HasPrefix(hash, uncheckedPrefix) {
				t.Errorf("ReportingHash after a full check = %q, still unchecked", hash)
			}
		})
	}
}

// TestMigrateFromWithoutFilesRunsNoQuery: with no event files (nil Files,
// a fresh install) there is nothing to check the system widgets on, so
// Migrate syncs the release without a query, which would spawn an engine
// child for nothing, and records the sync unchecked, quietly: the first
// start with files checks every widget. SQL the scanner refuses still
// fails it.
func TestMigrateFromWithoutFilesRunsNoQuery(t *testing.T) {
	ctx := context.Background()
	manifest := testManifest(t)
	st, files := newTestStoreAndFiles(t, 1000)
	system, dir := systemFSCopy(t)
	addSystemWidget(t, dir, "visits", "SELECT 2 AS value")

	if err := migrateFrom(ctx, st, nil, system, manifest); err != nil {
		t.Fatalf("no files: %v, want the release synced", err)
	}
	ws, err := st.ListWidgets(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 {
		t.Errorf("dashboard 1 widgets = %d, want the release's 2 synced", len(ws))
	}
	hash, err := st.ReportingHash(ctx)
	if err != nil || !strings.HasPrefix(hash, uncheckedPrefix) {
		t.Fatalf("ReportingHash = %q, %v; want it marked unchecked", hash, err)
	}

	working := &countingFiles{Files: files}
	if err := migrateFrom(ctx, st, working, system, manifest); err != nil {
		t.Fatal(err)
	}
	if working.queries != 2 {
		t.Errorf("first run with files: %d queries, want both widgets checked", working.queries)
	}
	if hash, _ := st.ReportingHash(ctx); strings.HasPrefix(hash, uncheckedPrefix) {
		t.Errorf("ReportingHash after a full check = %q, still unchecked", hash)
	}

	st2, _ := newTestStoreAndFiles(t, 1000)
	broken, dir2 := systemFSCopy(t)
	addSystemWidget(t, dir2, "broken", "SELECT ? AS value")
	err = migrateFrom(ctx, st2, nil, broken, manifest)
	if !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), "widget broken") {
		t.Errorf("no files, refused SQL: err = %v, want widget broken refused", err)
	}
}

// TestMigrateFromResourceFailureStillRefusesSQL: SQL refused for itself
// fails the start, before a resource failure or after one, where the
// scanner still reads it.
func TestMigrateFromResourceFailureStillRefusesSQL(t *testing.T) {
	ctx := context.Background()
	st, files := newTestStoreAndFiles(t, 1000)
	system, dir := systemFSCopy(t)
	addSystemWidget(t, dir, "visits", "SELECT ? AS value")
	err := migrateFrom(ctx, st, &countingFiles{Files: files, err: context.DeadlineExceeded}, system, testManifest(t))
	if errors.Is(err, ErrUnchecked) || !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), "widget visits") {
		t.Errorf("after a timeout: err = %v, want widget visits refused", err)
	}

	err = migrateFrom(ctx, st, &countingFiles{Files: files,
		err: &engine.QueryError{Msg: "Binder Error: Referenced column \"nope\" not found"}}, system, testManifest(t))
	if errors.Is(err, ErrUnchecked) || !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), "widget users") {
		t.Errorf("DuckDB's refusal: err = %v, want widget users refused", err)
	}
	if hash, _ := st.ReportingHash(ctx); hash != "" {
		t.Errorf("ReportingHash = %q, want nothing synced", hash)
	}
}

// addSystemWidget adds a stat widget name with sql to the copied
// retention dashboard under dir, after its users widget.
func addSystemWidget(t *testing.T, dir, name, sql string) {
	t.Helper()
	ret := filepath.Join(dir, "retention")
	for file, body := range map[string]string{
		name + ".json":   `{"component": "stat", "title": "Visits", "props": {"format": "number"}}`,
		name + ".sql":    sql,
		"dashboard.json": `{"id": 1, "title": "Retention", "range": "30d", "sidebar": true, "project_tab": true, "layout": [{"widget": "users"}, {"widget": "` + name + `"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(ret, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateFromSystemDashboardsSortByID(t *testing.T) {
	st, files := newTestStoreAndFiles(t, 1000)
	ctx := context.Background()
	system, _ := systemFSCopy(t)

	if err := migrateFrom(ctx, st, files, system, testManifest(t)); err != nil {
		t.Fatal(err)
	}

	ds, err := st.ListDashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sys := systemDashboardsOf(ds)
	if len(sys) != 2 {
		t.Fatalf("system dashboards = %d, want 2", len(sys))
	}
	// testdata/system's directories are named so alphabetical order
	// ("overview", "retention") is the reverse of id order (1, 2): this
	// only passes if dashboards are sorted by id, not directory name.
	if sys[0].ID != 1 || sys[1].ID != 2 {
		t.Errorf("dashboards not sorted by id: %+v", sys)
	}
	if sys[0].SortKey >= sys[1].SortKey {
		t.Errorf("sort keys not increasing with id: %q >= %q", sys[0].SortKey, sys[1].SortKey)
	}
}

func TestMigrateFromAcceptsUnregisteredTypeFails(t *testing.T) {
	st, files := newTestStoreAndFiles(t, 1000)
	ctx := context.Background()
	system, _ := systemFSCopy(t)

	var m manifest
	if err := json.Unmarshal(testManifest(t), &m); err != nil {
		t.Fatal(err)
	}
	m.Components = append(m.Components, manifestComponent{
		Name: "bogus", Accepts: []string{"csv"},
		Inputs: Inputs{Open: true}, Props: json.RawMessage(`{}`),
		DefaultWidth: 1, DefaultHeight: 1,
	})
	bad, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	if err := migrateFrom(ctx, st, files, system, bad); err == nil {
		t.Fatal("want error: accepts names an unregistered source type")
	}

	hash, err := st.ReportingHash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "" {
		t.Errorf("ReportingHash after a failed first migration = %q, want empty", hash)
	}
}

// TestHashSystemFramesFieldsAgainstCollision is the fix for a review
// finding: hashSystem used to concatenate path, content and manifest
// with no framing, so ("a","bx") and ("ab","x") hashed the same. Each
// field is now length-prefixed (writeFramed), so the same total bytes
// split differently must hash differently.
func TestHashSystemFramesFieldsAgainstCollision(t *testing.T) {
	a := fstest.MapFS{"a": &fstest.MapFile{Data: []byte("bx")}}
	b := fstest.MapFS{"ab": &fstest.MapFile{Data: []byte("x")}}

	hashA, err := hashSystem(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := hashSystem(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hashA == hashB {
		t.Fatal(`hashSystem: ("a","bx") and ("ab","x") still collide`)
	}
}

// TestMigrateFromGroupNamingMissingIDFails is D16: a file's "group" must
// name a dashboard in the same release.
func TestMigrateFromGroupNamingMissingIDFails(t *testing.T) {
	st, files := newTestStoreAndFiles(t, 1000)
	ctx := context.Background()
	system, dir := systemFSCopy(t)

	if err := os.WriteFile(filepath.Join(dir, "retention", "dashboard.json"),
		[]byte(`{"id":1,"title":"Retention","range":"30d","sidebar":true,"project_tab":true,"group":99,"layout":[{"widget":"users"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := migrateFrom(ctx, st, files, system, testManifest(t))
	if err == nil {
		t.Fatal("want error: group names a dashboard not in the release")
	}
	if !strings.Contains(err.Error(), "group 99 is not a dashboard in this release") {
		t.Errorf("error = %q, want it to name the missing group", err)
	}
}

// TestMigrateFromGroupNamingATabFails: a group must name a dashboard that
// is not itself in another group (D16: "a group names its first
// dashboard, which is not itself in another group").
func TestMigrateFromGroupNamingATabFails(t *testing.T) {
	st, files := newTestStoreAndFiles(t, 1000)
	ctx := context.Background()
	system, dir := systemFSCopy(t)

	// overview (id 2) joins retention's group (id 1)...
	if err := os.WriteFile(filepath.Join(dir, "overview", "dashboard.json"),
		[]byte(`{"id":2,"title":"Overview","range":"7d","sidebar":true,"project_tab":true,"group":1,"layout":[{"widget":"note"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// ...and retention (id 1) tries to join overview's group instead of
	// naming its own: retention is no longer a valid group name.
	if err := os.WriteFile(filepath.Join(dir, "retention", "dashboard.json"),
		[]byte(`{"id":1,"title":"Retention","range":"30d","sidebar":true,"project_tab":true,"group":2,"layout":[{"widget":"users"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := migrateFrom(ctx, st, files, system, testManifest(t))
	if err == nil {
		t.Fatal("want error: group names a dashboard that is itself in another group")
	}
	if !strings.Contains(err.Error(), "is itself in another group") {
		t.Errorf("error = %q, want it to say the group is itself in another group", err)
	}
}

// TestMigrateFromNonContiguousGroupFails is D16/the spec's "Keys stay
// sortkey.Spread in file order... check that order puts each group's
// members together": a third dashboard between two members of group 1
// breaks it up, and migrateFrom refuses rather than silently splitting
// the group's tabs apart in sort key order.
func TestMigrateFromNonContiguousGroupFails(t *testing.T) {
	st, files := newTestStoreAndFiles(t, 1000)
	ctx := context.Background()
	system, dir := systemFSCopy(t)

	// retention (id 1) is its own group (1); overview (id 2) is its own
	// group (2); a new dashboard (id 3) joins group 1, landing after
	// group 2 in id order: 1, 2, 1 — not contiguous.
	third := filepath.Join(dir, "third")
	if err := os.MkdirAll(third, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(third, "dashboard.json"),
		[]byte(`{"id":3,"title":"Third","range":"7d","sidebar":true,"project_tab":true,"group":1,"layout":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := migrateFrom(ctx, st, files, system, testManifest(t))
	if err == nil {
		t.Fatal("want error: group 1 is not contiguous")
	}
	if !strings.Contains(err.Error(), "group 1 is not contiguous") {
		t.Errorf("error = %q, want it to say group 1 is not contiguous", err)
	}
}

// TestMigrateFromRefusesEmptyRange is the fix for a review finding: a
// system dashboard.json must give range explicitly (no default, unlike
// create_dashboard's "7d"), and the refusal should name the problem
// plainly rather than reuse checkPreset's "range must be one of ...".
func TestMigrateFromRefusesEmptyRange(t *testing.T) {
	st, files := newTestStoreAndFiles(t, 1000)
	ctx := context.Background()
	system, dir := systemFSCopy(t)

	if err := os.WriteFile(filepath.Join(dir, "retention", "dashboard.json"),
		[]byte(`{"id":1,"title":"Retention","sidebar":true,"project_tab":true,"layout":[{"widget":"users"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := migrateFrom(ctx, st, files, system, testManifest(t))
	if err == nil {
		t.Fatal("want error: dashboard.json with no range")
	}
	if !strings.Contains(err.Error(), "dashboard.json needs range") {
		t.Errorf("error = %q, want it to say dashboard.json needs range", err)
	}
}

// TestCheckGroupsRefusesSidebarMismatch: the sidebar shows groups, so a
// group's members agree on sidebar (spec 2026-10-05 D3).
func TestCheckGroupsRefusesSidebarMismatch(t *testing.T) {
	founder := FileDashboard{ID: 1, Title: "Views", Sidebar: true}
	tab := FileDashboard{ID: 2, Title: "Product", Group: 1, Sidebar: true}
	if err := checkGroups([]FileDashboard{founder, tab}); err != nil {
		t.Errorf("agreeing members: err = %v, want nil", err)
	}
	tab.Sidebar = false
	err := checkGroups([]FileDashboard{founder, tab})
	if err == nil || err.Error() != "reporting: system dashboard 2: sidebar differs from its group's (1)" {
		t.Errorf("err = %v, want the sidebar mismatch", err)
	}
	// project_tab is each dashboard's own.
	tab.Sidebar, tab.ProjectTab = true, true
	if err := checkGroups([]FileDashboard{founder, tab}); err != nil {
		t.Errorf("differing project_tab: err = %v, want nil", err)
	}
}

// TestCheckGroupsRefusesGroupTitleOnATab: only a group's founding
// dashboard names it (D6).
func TestCheckGroupsRefusesGroupTitleOnATab(t *testing.T) {
	founder := FileDashboard{ID: 1, Title: "Views", GroupTitle: "Reports"}
	tab := FileDashboard{ID: 2, Title: "Product", Group: 1}
	if err := checkGroups([]FileDashboard{founder, tab}); err != nil {
		t.Errorf("group_title on the founder: err = %v, want nil", err)
	}
	tab.GroupTitle = "Reports"
	if err := checkGroups([]FileDashboard{founder, tab}); err == nil {
		t.Error("group_title on a tab: want an error")
	}
}

// manifestWithQueryStat returns testManifest with its stat replaced by
// queryStat, a stat that accepts a query source as well as sql.
func manifestWithQueryStat(t *testing.T) []byte {
	t.Helper()
	var m manifest
	if err := json.Unmarshal(testManifest(t), &m); err != nil {
		t.Fatal(err)
	}
	var stat manifestComponent
	if err := json.Unmarshal([]byte(queryStat), &stat); err != nil {
		t.Fatal(err)
	}
	for i, c := range m.Components {
		if c.Name == "stat" {
			m.Components[i] = stat
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMigrateSystemWidgetSourceType: a release that turns a system
// widget's users.sql into users.query changes that widget's source type
// and content in place, keeping its id, while a user's copy of the
// dashboard made before the upgrade keeps its SQL.
func TestMigrateSystemWidgetSourceType(t *testing.T) {
	st, files := newTestStoreAndFiles(t, 1000)
	ctx := context.Background()
	system, dir := systemFSCopy(t)
	manifest := manifestWithQueryStat(t)

	if err := migrateFrom(ctx, st, files, system, manifest); err != nil {
		t.Fatal(err)
	}
	users := func(dashboardID int64) store.Widget {
		t.Helper()
		ws, err := st.ListWidgets(ctx, dashboardID)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range ws {
			if w.Name == "users" {
				return w
			}
		}
		t.Fatalf("dashboard %d has no widget users", dashboardID)
		return store.Widget{}
	}
	before := users(1)
	if before.SourceType != "sql" {
		t.Fatalf("users source type = %s before the upgrade, want sql", before.SourceType)
	}
	svc := New(st, files, Options{})
	cp, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: 1})
	if err != nil {
		t.Fatal(err)
	}

	const query = `{"query":{"kind":"stat","table":"views","metrics":[{"op":"count","as":"value"}]}}`
	if err := os.Remove(filepath.Join(dir, "retention", "users.sql")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "retention", "users.query"), []byte(query), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := migrateFrom(ctx, st, files, system, manifest); err != nil {
		t.Fatal(err)
	}

	after := users(1)
	if after.ID != before.ID {
		t.Errorf("widget id changed: %d -> %d, want kept", before.ID, after.ID)
	}
	if after.SourceType != "query" || after.Source != query {
		t.Errorf("users = %s %q, want query %q", after.SourceType, after.Source, query)
	}
	copied := users(cp.ID)
	if copied.SourceType != "sql" || copied.Source != before.Source {
		t.Errorf("the user's copy = %s %q, want its SQL kept: sql %q", copied.SourceType, copied.Source, before.Source)
	}
}
