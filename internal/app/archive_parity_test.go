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
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

const appEngineChild = "APP_TEST_ENGINE_CHILD"

// TestMain lets the test binary act as the engine child the tests spawn:
// the production child is `twillingate engine`. The variable is set for
// the whole run, so the engines EngineOptions lays out (Serve's and
// Migrate's: this binary, with engine as its first argument, which the
// child skips) spawn a child that serves too, not another test run.
func TestMain(m *testing.M) {
	if os.Getenv(appEngineChild) == "1" {
		args := os.Args[1:]
		if len(args) > 0 && args[0] == "engine" {
			args = args[1:]
		}
		if err := engine.ServeChild(args, os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Setenv(appEngineChild, "1")
	os.Exit(m.Run())
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
	if err := (bothSinks{st, log}).WriteEvents(ctx, evs); err != nil {
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
