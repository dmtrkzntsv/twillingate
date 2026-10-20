package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

// fakeImporter records each day handed over, and fails a day once when
// told to.
type fakeImporter struct {
	days []string
	fail map[string]error
}

func (f *fakeImporter) ImportDay(ctx context.Context, project int64, day string, evs []store.Event) (int, error) {
	if err := f.fail[day]; err != nil {
		delete(f.fail, day)
		return 0, err
	}
	ids := make([]string, len(evs))
	for i, e := range evs {
		ids[i] = e.ID
	}
	f.days = append(f.days, fmt.Sprintf("%d %s %v", project, day, ids))
	return len(evs), nil
}

// importDB is a store with project 1 holding rows.
func importDB(t *testing.T, rows ...store.Event) store.Store {
	t.Helper()
	ctx := context.Background()
	st := storetest.Open(t, filepath.Join(t.TempDir(), "twillingate.db"))
	reg := manage.New(st, slog.Default())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manage.NewOps(reg, st).CreateProject(ctx, "test", manage.ProjectSpec{Name: "App"}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteEvents(ctx, rows); err != nil {
		t.Fatal(err)
	}
	return st
}

func importRow(id string, family store.Family, at string) store.Event {
	ts, _ := time.Parse(time.RFC3339, at)
	e := store.Event{ID: id, ProjectID: 1, Family: family, EventName: "x", TS: ts, ReceivedAt: ts,
		ActorID: "h", ActorKind: store.ActorConnection, Platform: "web"}
	if family == store.FamilyMeasures {
		v := 1.0
		e.Measure, e.Value = store.MeasureTime, &v
	}
	return e
}

var importNow = time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)

// TestImportEventsHandsEveryDayOver: every day of raw rows, every family,
// goes to the importer in order, and meta records the import.
func TestImportEventsHandsEveryDayOver(t *testing.T) {
	st := importDB(t,
		importRow("v1", store.FamilyViews, "2026-08-20T10:00:00Z"),
		importRow("p1", store.FamilyProduct, "2026-08-21T09:00:00Z"),
		importRow("m1", store.FamilyMeasures, "2026-08-21T09:30:00Z"))
	imp := &fakeImporter{}
	var out bytes.Buffer
	if err := importEvents(context.Background(), st, imp, 400, importNow, &out); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1 2026-08-20 [v1]", "1 2026-08-21 [p1 m1]"}; !slices.Equal(imp.days, want) {
		t.Fatalf("days = %v, want %v", imp.days, want)
	}
	if !strings.Contains(out.String(), "project 1: 2 days, 3 rows, 3 written") {
		t.Fatalf("output %q lacks the project's line", out.String())
	}
	if got, _ := st.GetMeta(context.Background(), "events_imported"); got != "2026-08-22T04:00:00Z" {
		t.Fatalf("meta = %q, want the time it finished", got)
	}
}

// TestImportEventsSkipsDaysRetentionDeletes: a day RETENTION_DAYS would
// delete is not handed over.
func TestImportEventsSkipsDaysRetentionDeletes(t *testing.T) {
	st := importDB(t,
		importRow("old", store.FamilyProduct, "2026-08-17T10:00:00Z"), // today - 5
		importRow("kept", store.FamilyProduct, "2026-08-18T10:00:00Z"))
	imp := &fakeImporter{}
	if err := importEvents(context.Background(), st, imp, 5, importNow, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1 2026-08-18 [kept]"}; !slices.Equal(imp.days, want) {
		t.Fatalf("days = %v, want %v", imp.days, want)
	}
}

// TestImportEventsRetriesAFailedDay: a failed day is reported, the others
// go on, the call fails and meta stays unset; run again, it succeeds and
// records the import.
func TestImportEventsRetriesAFailedDay(t *testing.T) {
	st := importDB(t,
		importRow("a", store.FamilyProduct, "2026-08-20T10:00:00Z"),
		importRow("b", store.FamilyProduct, "2026-08-21T10:00:00Z"))
	imp := &fakeImporter{fail: map[string]error{"2026-08-20": errors.New("disk full")}}
	var out bytes.Buffer
	if err := importEvents(context.Background(), st, imp, 400, importNow, &out); err == nil {
		t.Fatal("an import with a failed day succeeded")
	}
	if !strings.Contains(out.String(), "disk full") {
		t.Fatalf("output %q does not name the failure", out.String())
	}
	if got, _ := st.GetMeta(context.Background(), "events_imported"); got != "" {
		t.Fatalf("meta after a failed day = %q, want unset", got)
	}
	if err := importEvents(context.Background(), st, imp, 400, importNow, &out); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1 2026-08-21 [b]", "1 2026-08-20 [a]", "1 2026-08-21 [b]"}; !slices.Equal(imp.days, want) {
		t.Fatalf("days = %v, want %v", imp.days, want)
	}
	if got, _ := st.GetMeta(context.Background(), "events_imported"); got == "" {
		t.Fatal("meta unset after a run where every day succeeded")
	}
}

// TestImportEventsRunsOnce: with events_imported set, ImportEvents says
// when it ran and does nothing else, not even take the events directory.
func TestImportEventsRunsOnce(t *testing.T) {
	st := importDB(t)
	if err := st.SetMeta(context.Background(), "events_imported", "2026-08-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "raw")
	held, err := archive.OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	cfg := configtest.Load(t, map[string]string{"EVENTS_DIR": dir})
	var out bytes.Buffer
	if err := ImportEvents(context.Background(), cfg, st, false, &out); err != nil {
		t.Fatalf("a second run: %v, want nil", err)
	}
	if !strings.Contains(out.String(), "2026-08-01T00:00:00Z") || !strings.Contains(out.String(), "-force") {
		t.Fatalf("output %q: want when it ran and how to run it again", out.String())
	}
}

// TestImportEventsRefusesWhileTheServerRuns: while a Log holds the events
// directory (the ingest server), the import fails with ErrLocked and says
// to stop the service, before it writes anything: not meta, and no
// migration either (app.Migrate would rebuild the dropped flat view).
func TestImportEventsRefusesWhileTheServerRuns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "twillingate.db")
	st := storetest.Open(t, dbPath)
	if err := st.WriteEvents(context.Background(), []store.Event{importRow("a", store.FamilyProduct, "2026-08-20T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`DROP VIEW v_events_flat`); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "raw")
	held, err := archive.OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	cfg := configtest.Load(t, map[string]string{"EVENTS_DIR": dir, "DATABASE_DSN": "sqlite://" + dbPath})
	err = ImportEvents(context.Background(), cfg, st, true, &bytes.Buffer{})
	if !errors.Is(err, archive.ErrLocked) || !strings.Contains(err.Error(), "stop the ingest service") {
		t.Fatalf("err = %v, want ErrLocked telling to stop the ingest service", err)
	}
	if got, _ := st.GetMeta(context.Background(), "events_imported"); got != "" {
		t.Fatalf("meta = %q, want unset", got)
	}
	var views int
	if err := raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'v_events_flat'`).Scan(&views); err != nil {
		t.Fatal(err)
	}
	if views != 0 {
		t.Fatal("a refused import migrated the database (v_events_flat rebuilt)")
	}
}

// TestImportEventsRefusesAnotherUserBeforeTouchingRaw: run as anyone but
// the database file's owner (sudo), the import is refused before it creates
// raw/ or raw/.lock, which a root-owned copy would lock the service user
// out of, and before it migrates or marks the database.
func TestImportEventsRefusesAnotherUserBeforeTouchingRaw(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "twillingate.db")
	st := storetest.Open(t, dbPath)
	if err := st.WriteEvents(context.Background(), []store.Event{importRow("a", store.FamilyProduct, "2026-08-20T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`DROP VIEW v_events_flat`); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "raw")
	cfg := configtest.Load(t, map[string]string{"EVENTS_DIR": dir, "DATABASE_DSN": "sqlite://" + dbPath})
	prev := geteuid
	geteuid = func() int { return os.Geteuid() + 1 }
	t.Cleanup(func() { geteuid = prev })

	err = ImportEvents(context.Background(), cfg, st, true, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "service user") {
		t.Fatalf("err = %v, want a refusal naming the service user", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v, want it not to exist: a refused run created raw/", dir, err)
	}
	if got, _ := st.GetMeta(context.Background(), "events_imported"); got != "" {
		t.Fatalf("meta = %q, want unset", got)
	}
	var views int
	if err := raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'v_events_flat'`).Scan(&views); err != nil {
		t.Fatal(err)
	}
	if views != 0 {
		t.Fatal("a refused import migrated the database (v_events_flat rebuilt)")
	}
}

// TestCheckOwner: the database file's owner may run the import; anyone
// else is refused with the owner's uid and the service user named.
func TestCheckOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twillingate.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := int(info.Sys().(*syscall.Stat_t).Uid)
	if err := checkOwner(path, owner, "events import"); err != nil {
		t.Fatalf("the owner: %v, want nil", err)
	}
	err = checkOwner(path, owner+1, "events import")
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("uid %d", owner)) || !strings.Contains(err.Error(), "service user") {
		t.Fatalf("another uid: %v, want a refusal naming uid %d and the service user", err, owner)
	}
	if err := checkOwner(filepath.Join(t.TempDir(), "missing.db"), owner, "events import"); err == nil {
		t.Fatal("a missing database file passed")
	}
}
