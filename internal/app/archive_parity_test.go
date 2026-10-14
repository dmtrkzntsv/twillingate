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

// TestFilesHoldWhatSQLiteHolds: the files hold every event SQLite holds,
// a retried batch and an approved form's submission included (PR 3's
// Review Focus 4), and the reader's events table answers the same counts
// per day before the daily pass (every day from its log) and after it
// (a day file and today's log). A query that tries to write through the
// reader is refused and writes nothing.
func TestFilesHoldWhatSQLiteHolds(t *testing.T) {
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
	// event), approval, then a second submission whose event the
	// attribution job writes.
	sub := func(id string, at time.Time) store.NewSubmission {
		return store.NewSubmission{
			Submission: store.Submission{ProjectID: 1, ID: id, Form: "contact", ReceivedAt: at, Fields: map[string]string{"email": "a@x.io"}},
			DraftUntil: at.Add(7 * 24 * time.Hour),
			Event:      store.Event{ID: id, ProjectID: 1, Family: store.FamilyProduct, EventName: store.FormSubmitEvent, TS: at, ReceivedAt: at},
		}
	}
	if _, _, err := st.WriteSubmission(ctx, sub("00000000-0000-4000-a000-000000000001", day.Add(2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := st.ApproveForm(ctx, 1, "contact", []string{"email"}, day.Add(3*time.Hour), store.AuditEntry{Actor: "test", Action: "form.approve", Subject: "form/1/contact"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.WriteSubmission(ctx, sub("00000000-0000-4000-a000-000000000002", day.Add(4*time.Hour))); err != nil {
		t.Fatal(err)
	}
	// The jobs runner writes the event to SQLite when the submission is
	// attributed, and to the log after it.
	pending, err := st.PendingSubmissions(ctx, day.AddDate(0, 0, 1), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pending {
		wrote, err := st.AttributeSubmission(ctx, p, p.Event)
		if err != nil {
			t.Fatal(err)
		}
		if wrote {
			if err := log.WriteEvents(ctx, []store.Event{p.Event}); err != nil {
				t.Fatal(err)
			}
		}
	}

	self, _ := os.Executable()
	eng, err := engine.New(engine.Options{Path: self, Env: []string{appEngineChild + "=1"},
		AllowedDirs: []string{log.Dir()}, SpillRoot: filepath.Join(data, "engine", "spill"), SlotDir: filepath.Join(data, "engine", "slots"),
		MaxChildren: 1, MemoryLimit: "256MB", Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	// A second, read-only handle on the same file (the modernc driver is
	// registered through internal/store/sqlite): no accessor on store.Store
	// for a test.
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(data, "twillingate.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var want []string
	rows, err := raw.Query(`SELECT day, count(*) FROM events WHERE project_id = 1 GROUP BY day ORDER BY day`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var day string
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			t.Fatal(err)
		}
		want = append(want, fmt.Sprintf("[%s %d]", day, n))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(want) != "[[2026-10-11 51] [2026-10-12 3]]" {
		t.Fatalf("SQLite per day = %v; want 51 (50 events once, plus the approved submission) and 3", want)
	}
	files, err := archive.NewReader(log.Dir(), eng)
	if err != nil {
		t.Fatal(err)
	}
	perDay := func() string {
		t.Helper()
		res, err := files.Query(ctx, "SELECT CAST(day AS VARCHAR), count(*) FROM events WHERE project_id = 1 GROUP BY day ORDER BY day", 0, archive.Rules{})
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(res.Rows)
	}
	if got := perDay(); got != fmt.Sprint(want) {
		t.Fatalf("files per day before the pass = %s, SQLite %v", got, want)
	}

	pass, _ := civil.Parse("2026-10-12")
	if err := archive.New(log, eng, slog.Default()).Maintain(ctx, pass, 1, 400); err != nil {
		t.Fatal(err)
	}
	res, err := eng.Query(ctx, "SELECT count(*) FROM read_parquet('"+filepath.Join(log.Dir(), "1", "2026-10-11.parquet")+"')", 0)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(res.Rows) != "[[51]]" {
		t.Fatalf("day file = %v rows, want 51", res.Rows)
	}
	if b, err := os.ReadFile(filepath.Join(log.Dir(), "1", "2026-10-12.log")); err != nil || len(splitLines(b)) != 3 {
		t.Fatalf("today's log: %v, %d lines; want it open with 3", err, len(splitLines(b)))
	}
	if got := perDay(); got != fmt.Sprint(want) {
		t.Fatalf("files per day after the pass = %s, SQLite %v", got, want)
	}
	out := filepath.Join(log.Dir(), "x.csv")
	if _, err := files.Query(ctx, "COPY (SELECT 1) TO '"+out+"' (FORMAT csv)", 0, archive.Rules{}); err == nil {
		t.Fatal("a COPY through the reader was accepted")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("%s exists: a refused query wrote it", out)
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

// TestDerivedTablesMatchSQLite: the sessions the files give a day are the
// ones agg_views_daily counts (sessions, bounces, seconds per kind), and
// identities name and count each user and group as identities and
// v_identity_daily do; before the pass (from the log) and after it (from
// the derived files).
func TestDerivedTablesMatchSQLite(t *testing.T) {
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

	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	n := 0
	ev := func(fam store.Family, kind, actor, user, session string, at time.Duration) store.Event {
		n++
		e := store.Event{ID: fmt.Sprintf("00000000-0000-4000-b000-%012d", n), ProjectID: 1, Family: fam, Kind: kind,
			ActorID: actor, ActorKind: store.ActorConnection, SessionID: session, TS: day.Add(at), ReceivedAt: day.Add(at)}
		if user != "" {
			e.ActorKind, e.UserID, e.UserName = store.ActorUser, user, "Name of "+user
		}
		if fam == store.FamilyViews {
			e.EventName, e.Path = "$page_view", "/"
		} else {
			e.EventName = "signup"
		}
		return e
	}
	h, m := time.Hour, time.Minute
	grouped := ev(store.FamilyProduct, "ios", "u1", "u1", "", 9*h+m)
	grouped.GroupID, grouped.GroupName = "g1", "Acme"
	evs := []store.Event{
		ev(store.FamilyViews, "web", "a", "", "", 10*h), ev(store.FamilyViews, "web", "a", "", "", 10*h+10*m),
		ev(store.FamilyViews, "web", "a", "", "", 10*h+50*m), // a new session after 40 minutes
		ev(store.FamilyViews, "web", "b", "", "", 12*h),      // a bounce
		ev(store.FamilyViews, "ios", "u1", "u1", "s1", 9*h), ev(store.FamilyViews, "ios", "u1", "u1", "s1", 11*h),
		grouped,
		ev(store.FamilyProduct, "web", "u2", "u2", "", 14*h),
	}
	if err := (pipeline.Sinks{st, log}).WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	// As ingest does beside the events (handlers.go, identityNames).
	if err := st.UpsertIdentities(ctx, []store.Identity{
		{ProjectID: 1, Kind: store.KindUser, ID: "u1", Name: "Name of u1"},
		{ProjectID: 1, Kind: store.KindUser, ID: "u2", Name: "Name of u2"},
		{ProjectID: 1, Kind: store.KindGroup, ID: "g1", Name: "Acme"},
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", "file:"+filepath.Join(data, "twillingate.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	sqliteRows := func(q string) string {
		t.Helper()
		rows, err := raw.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var out [][]any
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				if b, ok := v.([]byte); ok {
					vals[i] = string(b)
				}
			}
			out = append(out, vals)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(out)
	}
	// v_identity_daily's live half reads the raw rows, which the views
	// rollup below deletes: read it first.
	wantIdentities := sqliteRows(`SELECT d.kind, d.id, i.name, SUM(d.views), SUM(d.events)
		FROM v_identity_daily d LEFT JOIN identities i ON i.project_id = d.project_id AND i.kind = d.kind AND i.id = d.id
		WHERE d.project_id = 1 GROUP BY d.kind, d.id, i.name ORDER BY d.kind, d.id`)
	if wantIdentities != "[[group g1 Acme 0 1] [user u1 Name of u1 2 1] [user u2 Name of u2 0 1]]" {
		t.Fatalf("SQLite identities = %s", wantIdentities)
	}
	if err := st.AggregateViewDay(ctx, 1, civil.DateOf(day), 0); err != nil {
		t.Fatal(err)
	}
	wantSessions := sqliteRows(`SELECT kind, sessions, bounces, duration_sec FROM agg_views_daily
		WHERE project_id = 1 AND day = '2026-10-11' ORDER BY kind`)
	if wantSessions != "[[ios 1 0 7200] [web 3 2 600]]" {
		t.Fatalf("SQLite sessions = %s", wantSessions)
	}

	self, _ := os.Executable()
	eng, err := engine.New(engine.Options{Path: self, Env: []string{appEngineChild + "=1"},
		AllowedDirs: []string{log.Dir()}, SpillRoot: filepath.Join(data, "engine", "spill"), SlotDir: filepath.Join(data, "engine", "slots"),
		MaxChildren: 1, MemoryLimit: "256MB", Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	files, err := archive.NewReader(log.Dir(), eng)
	if err != nil {
		t.Fatal(err)
	}
	ask := func(q string) string {
		t.Helper()
		res, err := files.Query(ctx, q, 0, archive.Rules{})
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(res.Rows)
	}
	check := func(when string) {
		t.Helper()
		got := ask(`SELECT kind, count(*), count(*) FILTER (WHERE views = 1), CAST(sum(duration_sec) AS BIGINT)
			FROM sessions WHERE project_id = 1 AND day = DATE '2026-10-11' GROUP BY kind ORDER BY kind`)
		if got != wantSessions {
			t.Errorf("%s: files sessions = %s, SQLite %s", when, got, wantSessions)
		}
		got = ask(`SELECT kind, id, name, views, events FROM identities WHERE project_id = 1 ORDER BY kind, id`)
		if got != wantIdentities {
			t.Errorf("%s: files identities = %s, SQLite %s", when, got, wantIdentities)
		}
	}
	check("from the log")
	pass, _ := civil.Parse("2026-10-12")
	if err := archive.New(log, eng, slog.Default()).Maintain(ctx, pass, 1, 400); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(log.Dir(), "1", "sessions", "2026-10-11.parquet")); err != nil {
		t.Fatalf("the pass derived no session file: %v", err)
	}
	check("from the derived files")
}
