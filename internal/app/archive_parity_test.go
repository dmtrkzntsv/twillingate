package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/pipeline"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

const appEngineChild = "APP_TEST_ENGINE_CHILD"

// TestMain lets the test binary act as the engine child the parity test
// spawns: the production child is `twillingate engine`.
func TestMain(m *testing.M) {
	if os.Getenv(appEngineChild) == "1" {
		if err := engine.ServeChild(os.Args[1:], os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestDailyPassDumpsWhatSQLiteHolds: the day file holds every event SQLite
// holds for the day, a retried batch and an approved form's submission
// included (Review Focus 4).
func TestDailyPassDumpsWhatSQLiteHolds(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	st := storetest.Open(t, filepath.Join(data, "twillingate.db"))
	reg := manage.New(st, slog.Default())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manage.NewOps(reg, st).CreateProject(ctx, "test", manage.ProjectSpec{Name: "App"}); err != nil {
		t.Fatal(err)
	}
	log, err := archive.OpenLog(filepath.Join(data, "raw"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	sinks := pipeline.Sinks{st, log}

	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	var batch []store.Event
	for i := 0; i < 50; i++ {
		at := day.Add(time.Duration(i) * time.Minute)
		e := store.Event{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), ProjectID: 1, TS: at, ReceivedAt: at}
		switch i % 3 {
		case 0:
			e.Family, e.EventName, e.Kind, e.ActorID, e.ActorKind, e.Path = store.FamilyViews, "$page_view", "web", "v", store.ActorConnection, "/"
		case 1:
			e.Family, e.EventName, e.UserID, e.Attributes = store.FamilyProduct, "signup", "u", map[string]string{"plan": "pro"}
		default:
			v := float64(i)
			e.Family, e.EventName, e.Measure, e.Value = store.FamilyMeasures, "load", store.MeasureTime, &v
		}
		batch = append(batch, e)
	}
	for i := 0; i < 2; i++ { // the second is a retry: neither SQLite nor the log may count it twice
		if err := sinks.WriteEvents(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}
	var today []store.Event
	for i := 0; i < 3; i++ {
		at := day.AddDate(0, 0, 1).Add(time.Duration(i) * time.Hour)
		today = append(today, store.Event{ID: fmt.Sprintf("00000000-0000-4000-9000-%012d", i), ProjectID: 1,
			Family: store.FamilyProduct, EventName: "x", TS: at, ReceivedAt: at})
	}
	if err := sinks.WriteEvents(ctx, today); err != nil {
		t.Fatal(err)
	}

	// An approved form: its first submission creates it as a draft (no
	// event), approval, then a second submission writes the event.
	forms := archivedForms{FormStore: st, log: log, logger: slog.Default()}
	sub := func(id string, at time.Time) store.NewSubmission {
		return store.NewSubmission{
			Submission: store.Submission{ProjectID: 1, ID: id, Form: "contact", ReceivedAt: at, Fields: map[string]string{"email": "a@x.io"}},
			DraftUntil: at.Add(7 * 24 * time.Hour),
			Event:      store.Event{ID: id, ProjectID: 1, Family: store.FamilyProduct, EventName: store.FormSubmitEvent, TS: at, ReceivedAt: at},
		}
	}
	if _, _, err := forms.WriteSubmission(ctx, sub("00000000-0000-4000-a000-000000000001", day.Add(2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := st.ApproveForm(ctx, 1, "contact", []string{"email"}, day.Add(3*time.Hour), store.AuditEntry{Actor: "test", Action: "form.approve", Subject: "form/1/contact"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := forms.WriteSubmission(ctx, sub("00000000-0000-4000-a000-000000000002", day.Add(4*time.Hour))); err != nil {
		t.Fatal(err)
	}

	self, _ := os.Executable()
	eng, err := engine.New(engine.Options{Path: self, Env: []string{appEngineChild + "=1"},
		AllowedDirs: []string{log.Dir()}, SpillRoot: filepath.Join(data, "engine", "spill"), SlotDir: filepath.Join(data, "engine", "slots"),
		MaxChildren: 1, MemoryLimit: "256MB", Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	pass, _ := civil.Parse("2026-10-12")
	if err := archive.New(log, eng, slog.Default()).Maintain(ctx, pass, 1, 400); err != nil {
		t.Fatal(err)
	}

	res, err := eng.Query(ctx, "SELECT count(*) FROM read_parquet('"+filepath.Join(log.Dir(), "1", "2026-10-11.parquet")+"')", 0)
	if err != nil {
		t.Fatal(err)
	}
	var files, sqlite int
	fmt.Sscan(fmt.Sprint(res.Rows[0][0]), &files)
	// A second, read-only handle on the same file (the modernc driver is
	// registered through internal/store/sqlite): no accessor on store.Store
	// for a test.
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(data, "twillingate.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := raw.QueryRow(`SELECT count(*) FROM events WHERE project_id=1 AND day='2026-10-11'`).Scan(&sqlite); err != nil {
		t.Fatal(err)
	}
	if files != sqlite || sqlite != 51 {
		t.Fatalf("day file %d rows, SQLite %d; want both 51 (50 events once, plus the approved submission)", files, sqlite)
	}
	if b, err := os.ReadFile(filepath.Join(log.Dir(), "1", "2026-10-12.log")); err != nil || len(splitLines(b)) != 3 {
		t.Fatalf("today's log: %v, %d lines; want it open with 3", err, len(splitLines(b)))
	}
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		i := 0
		for i < len(b) && b[i] != '\n' {
			i++
		}
		out = append(out, b[:i])
		if i == len(b) {
			break
		}
		b = b[i+1:]
	}
	return out
}
