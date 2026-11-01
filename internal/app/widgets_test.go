package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/api"
	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// widgetsFixture is a migrated store with projects 1 and 2, raw/ beside
// it, and the release's components but none of its system dashboards, so
// a test of the users' widgets does not depend on the system widgets.
type widgetsFixture struct {
	cfg *config.Config
	st  store.Store
	reg *manage.Registry
	ops *manage.Ops
}

func newWidgetsFixture(t *testing.T) *widgetsFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "twillingate.db")
	st := storetest.Open(t, path)
	ctx := context.Background()
	comps, err := reporting.ParseManifest(reporting.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]store.Component, 0, len(comps))
	for _, c := range comps {
		accepts, _ := json.Marshal(c.Accepts)
		inputs, _ := json.Marshal(c.Inputs)
		rows = append(rows, store.Component{Name: c.Name, Description: c.Description, Accepts: string(accepts),
			Inputs: string(inputs), Props: string(c.Props), DefaultWidth: c.DefaultWidth, DefaultHeight: c.DefaultHeight})
	}
	if err := st.SyncReporting(ctx, store.ReportingSync{Hash: "test", Components: rows}); err != nil {
		t.Fatal(err)
	}
	reg := manage.New(st, slog.New(slog.DiscardHandler))
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := manage.NewOps(reg, st)
	for _, name := range []string{"blog", "docs"} {
		if _, err := ops.CreateProject(ctx, "test", manage.ProjectSpec{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	return &widgetsFixture{cfg: configtest.Load(t, map[string]string{"DATABASE_DSN": "sqlite://" + path}), st: st, reg: reg, ops: ops}
}

// write appends one event of project on day to the event files: a page
// view, or a product event named signup.
func (f *widgetsFixture) write(t *testing.T, id string, project int64, family store.Family, day string) {
	t.Helper()
	log, err := archive.OpenLog(f.cfg.EventsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	ts, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatal(err)
	}
	name := "$page_view"
	if family == store.FamilyProduct {
		name = "signup"
	}
	if err := log.WriteEvents(context.Background(), []store.Event{{ID: id, ProjectID: project, Family: family,
		EventName: name, TS: ts.Add(12 * time.Hour), ReceivedAt: ts.Add(12 * time.Hour), Kind: "web",
		ActorID: "a", ActorKind: store.ActorInstall, SampleRate: 1}}); err != nil {
		t.Fatal(err)
	}
}

// sql is a client of a scripted SQL engine (archivetest.FakeSQL) for the
// fixture's raw/, answering from the test's file under
// testdata/sqlengine, recorded from a real engine over the events the
// test writes; closed with t.
func (f *widgetsFixture) sql(t *testing.T) *sqlengine.Client {
	t.Helper()
	return archivetest.NewFakeSQL(t).LoadTest(t, "testdata/sqlengine").Client(t)
}

// options is what serve gives the widgets and their check: the
// registry's rules and live projects, raw/ and the guards.
func (f *widgetsFixture) options() reporting.Options {
	return api.WidgetOptions(f.cfg, f.reg, reporting.Options{})
}

// TestWidgetSQLAppliesTheRules: a widget's query reads the files through
// the registry's rules, as the query tool does, scoped to its project,
// and samples the live project with the newest file.
func TestWidgetSQLAppliesTheRules(t *testing.T) {
	f := newWidgetsFixture(t)
	ctx := context.Background()
	f.write(t, "e1", 1, store.FamilyProduct, "2026-08-20")
	f.write(t, "e2", 2, store.FamilyProduct, "2026-08-20")
	svc := reporting.New(f.st, f.sql(t), f.options())
	count := func() string {
		t.Helper()
		got, err := svc.Preview(ctx, reporting.PreviewIn{Component: "stat", ProjectID: 1,
			Source: reporting.Source{Type: "sql", Content: "SELECT count(*) AS value FROM product WHERE event_name = 'signup' AND :project > 0"}})
		if err != nil {
			t.Fatal(err)
		}
		return got.Rows[0][0]
	}
	if n := count(); n != "1" {
		t.Fatalf("project 1 counts %s signups; want 1 (its own)", n)
	}
	if _, err := f.ops.BlockEvent(ctx, "test", 1, "product", "signup"); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != "0" {
		t.Fatalf("after blocking signup, project 1 counts %s; want 0", n)
	}
}

// TestCheckUserWidgetsLogsWhatFailsOnTheFiles: a user's widget written for
// SQLite (v_views_daily) is logged with its id, dashboard, title and
// error, the hint included; one in DuckDB SQL over the files, an archived
// one and an md widget are not.
func TestCheckUserWidgetsLogsWhatFailsOnTheFiles(t *testing.T) {
	f := newWidgetsFixture(t)
	ctx := context.Background()
	f.write(t, "e1", 1, store.FamilyViews, "2026-08-20")
	dash, err := f.st.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "Mine", SortKey: "z", Sidebar: true},
		[]store.Widget{
			{Component: "stat", SortKey: "a", Width: 3, Height: 4, Name: "old", Title: "Old visitors", Props: "{}", SourceType: "sql",
				Source: "SELECT SUM(visitors) AS value FROM v_views_daily WHERE project_id = :project AND day BETWEEN :from AND :to"},
			{Component: "stat", SortKey: "b", Width: 3, Height: 4, Name: "new", Title: "Visitors", Props: "{}", SourceType: "sql",
				Source: "SELECT count(DISTINCT actor_id) AS value FROM views WHERE day BETWEEN :from AND :to"},
			{Component: "stat", SortKey: "c", Width: 3, Height: 4, Name: "gone", Title: "Archived", Props: "{}", SourceType: "sql",
				Source: "SELECT 1 AS value FROM raw_views"},
			{Component: "markdown", SortKey: "d", Width: 3, Height: 4, Name: "note", Title: "Note", Props: "{}", SourceType: "md",
				Source: "hello"},
		}, store.AuditEntry{Actor: "test", Action: "dashboard.create"})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := f.st.ListWidgets(ctx, dash)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetWidgetArchived(ctx, ws[2].ID, true, store.AuditEntry{Actor: "test", Action: "widget.archive"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	checkUserWidgets(ctx, f.st, f.sql(t), f.options(), slog.New(slog.NewJSONHandler(&out, nil)))
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line %q: %v", l, err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want 1:\n%s", len(lines), out.String())
	}
	got := lines[0]
	if got["msg"] != "widget SQL fails on the event files" || got["widget_id"] != float64(ws[0].ID) ||
		got["dashboard_id"] != float64(dash) || got["title"] != "Old visitors" {
		t.Errorf("logged %v", got)
	}
	if e, _ := got["error"].(string); !strings.Contains(e, "v_views_daily") || !strings.Contains(e, "widget SQL is DuckDB SQL") {
		t.Errorf("error = %q, want DuckDB's message naming v_views_daily, with the hint", e)
	}
}

// TestCheckUserWidgetsWithoutEventFilesRunsNoQuery: with no event file
// under DATA_DIR every widget would run on nothing, and with no engine
// there is nothing to run them on, so the boot's check of the users'
// widgets sends no query and logs nothing.
func TestCheckUserWidgetsWithoutEventFilesRunsNoQuery(t *testing.T) {
	f := newWidgetsFixture(t)
	ctx := context.Background()
	if _, err := f.st.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "Mine", SortKey: "z", Sidebar: true},
		[]store.Widget{{Component: "stat", SortKey: "a", Width: 3, Height: 4, Name: "old", Title: "Old visitors", Props: "{}",
			SourceType: "sql", Source: "SELECT SUM(visitors) AS value FROM v_views_daily"}},
		store.AuditEntry{Actor: "test", Action: "dashboard.create"}); err != nil {
		t.Fatal(err)
	}
	files := &failingFiles{SQL: f.sql(t), err: errors.New("no query expected")}
	var out bytes.Buffer
	checkUserWidgets(ctx, f.st, files, f.options(), slog.New(slog.NewJSONHandler(&out, nil)))
	if files.queries != 0 || out.Len() != 0 {
		t.Fatalf("%d queries, logged %q; want none without event files", files.queries, out.String())
	}

	f.write(t, "e1", 1, store.FamilyViews, "2026-08-20")
	checkUserWidgets(ctx, f.st, nil, f.options(), slog.New(slog.NewJSONHandler(&out, nil)))
	if out.Len() != 0 {
		t.Fatalf("logged %q; want nothing without an engine", out.String())
	}
}

// failingFiles is an engine whose every query fails with err, or with
// only set, the first only queries, the rest running on SQL.
type failingFiles struct {
	reporting.SQL
	err     error
	only    int
	queries int
}

func (f *failingFiles) Query(ctx context.Context, req sqlengine.Request) (sqlengine.Result, error) {
	f.queries++
	if f.only > 0 && f.queries > f.only {
		return f.SQL.Query(ctx, req)
	}
	return sqlengine.Result{}, f.err
}

// engineErr is the engine's answer of kind k.
func engineErr(k sqlengine.Kind, msg string) error {
	return &sqlengine.Error{Status: k.Status(), Kind: k, Message: msg}
}

// TestCheckUserWidgetsTellsResourceFailures: a check that runs out of time
// or memory, whose worker dies, or that the engine is busy or down for,
// says nothing of the widget's SQL. It is logged as a widget that could
// not be checked, never as one that fails. A timeout is that widget's
// own, and the check goes on: the next widget's SQL, written for SQLite,
// is still logged as failing. Anything else says the engine is short or
// down, and the check stops there rather than load it further.
func TestCheckUserWidgetsTellsResourceFailures(t *testing.T) {
	f := newWidgetsFixture(t)
	ctx := context.Background()
	f.write(t, "e1", 1, store.FamilyViews, "2026-08-20")
	if _, err := f.st.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "Mine", SortKey: "z", Sidebar: true},
		[]store.Widget{
			{Component: "stat", SortKey: "a", Width: 3, Height: 4, Name: "one", Title: "One", Props: "{}", SourceType: "sql",
				Source: "SELECT count(*) AS value FROM views WHERE project_id = :project"},
			{Component: "stat", SortKey: "b", Width: 3, Height: 4, Name: "two", Title: "Two", Props: "{}", SourceType: "sql",
				Source: "SELECT SUM(visitors) AS value FROM v_views_daily WHERE project_id = :project"},
		}, store.AuditEntry{Actor: "test", Action: "dashboard.create"}); err != nil {
		t.Fatal(err)
	}
	sql := f.sql(t)
	const (
		skipped = "could not check widget on the event files"
		stopped = skipped + "; the widgets after it go unchecked until the next start"
	)
	for name, tc := range map[string]struct {
		err  error
		stop bool
	}{
		"timeout":            {err: context.DeadlineExceeded},
		"engine timeout":     {err: engineErr(sqlengine.KindTimeout, "the query exceeded X-Timeout-Ms")},
		"out of memory":      {err: engineErr(sqlengine.KindOutOfMemory, "Out of Memory Error: failed to allocate data of size 64.0 MiB (476.8 MiB/476.8 MiB used)"), stop: true},
		"logs out of memory": {err: engineErr(sqlengine.KindOutOfMemoryLoadingLog, "Out of Memory Error: x"), stop: true},
		"child exited":       {err: engineErr(sqlengine.KindExited, "engine: query: child exited"), stop: true},
		"busy":               {err: engineErr(sqlengine.KindBusy, "no free slot; retry later"), stop: true},
		"internal":           {err: engineErr(sqlengine.KindInternal, "boom"), stop: true},
		"down":               {err: fmt.Errorf("%w: dial unix: connection refused", sqlengine.ErrUnavailable), stop: true},
	} {
		t.Run(name, func(t *testing.T) {
			files := &failingFiles{SQL: sql, err: tc.err, only: 1}
			var out bytes.Buffer
			checkUserWidgets(ctx, f.st, files, f.options(), slog.New(slog.NewJSONHandler(&out, nil)))
			var lines []map[string]any
			for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
				var m map[string]any
				if err := json.Unmarshal([]byte(l), &m); err != nil {
					t.Fatalf("log line %q: %v", l, err)
				}
				lines = append(lines, m)
			}
			want, queries := 2, 2
			if tc.stop {
				want, queries = 1, 1
			}
			if len(lines) != want || files.queries != queries {
				t.Fatalf("%d queries, logged %d lines; want %d and %d:\n%s", files.queries, len(lines), queries, want, out.String())
			}
			msg := skipped
			if tc.stop {
				msg = stopped
			}
			if got := lines[0]; got["msg"] != msg || got["title"] != "One" || got["reason"] == "" || got["error"] != nil {
				t.Errorf("logged %v, want %q for One with its reason", got, msg)
			}
			if !tc.stop {
				if got := lines[1]; got["msg"] != "widget SQL fails on the event files" || got["title"] != "Two" {
					t.Errorf("logged %v, want Two failing after the timeout", got)
				}
			}
		})
	}
}

// TestMigrateStartsWhenTheSystemCheckCannotRun: a check of the system
// widgets that runs out of time or memory, or whose child dies, comes
// back from the reporting migrator as ErrUnchecked (reporting's own tests
// pin which causes do); it is logged as an error and the start goes on,
// so the daily pass that frees the engine runs. A refusal of their SQL
// still stops it.
func TestMigrateStartsWhenTheSystemCheckCannotRun(t *testing.T) {
	ctx := context.Background()
	f := newWidgetsFixture(t)
	was := reportingMigrate
	t.Cleanup(func() { reportingMigrate = was })
	var out bytes.Buffer
	reportingMigrate = func(context.Context, reporting.Store, reporting.SQL, reporting.Options) error {
		return fmt.Errorf("%w: system dashboard 6 widget lcp: Out of Memory Error: x; the next start checks them again", reporting.ErrUnchecked)
	}
	if err := migrateReporting(ctx, f.st, nil, reporting.Options{}, slog.New(slog.NewTextHandler(&out, nil))); err != nil {
		t.Fatalf("err = %v, want the start to go on", err)
	}
	if !strings.Contains(out.String(), "level=ERROR") || !strings.Contains(out.String(), "could not check the system dashboards") || !strings.Contains(out.String(), "Out of Memory") {
		t.Errorf("logged %q, want the error", out.String())
	}
	refused := errors.New(`reporting: system dashboard 6 widget lcp: Binder Error: Referenced column "nope" not found`)
	reportingMigrate = func(context.Context, reporting.Store, reporting.SQL, reporting.Options) error { return refused }
	if err := migrateReporting(ctx, f.st, nil, reporting.Options{}, slog.New(slog.DiscardHandler)); !errors.Is(err, refused) {
		t.Errorf("DuckDB's refusal: err = %v, want the start stopped", err)
	}
}

// TestCheckUserWidgetsStopsAfterThreeSkips: a timeout, or files that went
// stale on every attempt, skips only that widget, but the third widget in
// a row to end that way stops the check, logged once as such: an engine
// that is struggling is not sent the rest.
func TestCheckUserWidgetsStopsAfterThreeSkips(t *testing.T) {
	f := newWidgetsFixture(t)
	ctx := context.Background()
	f.write(t, "e1", 1, store.FamilyViews, "2026-08-20")
	var ws []store.Widget
	for i, n := range []string{"one", "two", "three", "four", "five"} {
		ws = append(ws, store.Widget{Component: "stat", SortKey: string(rune('a' + i)), Width: 3, Height: 4, Name: n, Title: n,
			Props: "{}", SourceType: "sql", Source: "SELECT count(*) AS value FROM views WHERE project_id = :project"})
	}
	if _, err := f.st.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "Mine", SortKey: "z", Sidebar: true},
		ws, store.AuditEntry{Actor: "test", Action: "dashboard.create"}); err != nil {
		t.Fatal(err)
	}
	sql := f.sql(t)
	for name, tc := range map[string]struct {
		err     error
		queries int
	}{
		"timeouts": {err: engineErr(sqlengine.KindTimeout, "the query exceeded X-Timeout-Ms"), queries: 3},
		"stale":    {err: engineErr(sqlengine.KindStale, "a file in the table spec is gone"), queries: 3 * sqlengine.StaleAttempts},
	} {
		t.Run(name, func(t *testing.T) {
			files := &failingFiles{SQL: sql, err: tc.err}
			var out bytes.Buffer
			checkUserWidgets(ctx, f.st, files, f.options(), slog.New(slog.NewJSONHandler(&out, nil)))
			var msgs, titles []string
			for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
				var m map[string]any
				if err := json.Unmarshal([]byte(l), &m); err != nil {
					t.Fatalf("log line %q: %v", l, err)
				}
				msgs = append(msgs, fmt.Sprint(m["msg"]))
				titles = append(titles, fmt.Sprint(m["title"]))
			}
			const skipped = "could not check widget on the event files"
			want := []string{skipped, skipped, skipped + "; the widgets after it go unchecked until the next start"}
			if !slices.Equal(msgs, want) || !slices.Equal(titles, []string{"one", "two", "three"}) || files.queries != tc.queries {
				t.Errorf("%d queries, logged %q for %q; want %d, %q for one, two, three", files.queries, msgs, titles, tc.queries, want)
			}
		})
	}
}

// scopeIs takes the engine requests scoped to project.
func scopeIs(project int64) archivetest.Match {
	return func(r sqlengine.Request) bool { return r.Scope != nil && *r.Scope == project }
}

// TestCheckUserWidgetsTriesEveryProject: a widget written for one
// project's attribute fails on a project that never sent it (its scoped
// tables have no such column), so the check at start tries each live
// project with files, the sample first, and logs nothing when one runs
// it. A widget that fails on every project is still logged, with the
// sample project's error. A widget tried on several projects counts as
// one skip: three such widgets in a row timing out on the second project
// stop the check after six queries.
func TestCheckUserWidgetsTriesEveryProject(t *testing.T) {
	f := newWidgetsFixture(t)
	ctx := context.Background()
	f.write(t, "e1", 1, store.FamilyProduct, "2026-08-21") // the sample: the newest day
	f.write(t, "e2", 2, store.FamilyProduct, "2026-08-20")
	const notFound = `Binder Error: Referenced column "attr_position" not found`
	insert := func(key string, sources ...string) {
		t.Helper()
		var ws []store.Widget
		for i, src := range sources {
			n := fmt.Sprint("w", i)
			ws = append(ws, store.Widget{Component: "stat", SortKey: string(rune('a' + i)), Width: 3, Height: 4, Name: n, Title: n,
				Props: "{}", SourceType: "sql", Source: src})
		}
		if _, err := f.st.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "Mine " + key, SortKey: key, Sidebar: true},
			ws, store.AuditEntry{Actor: "test", Action: "dashboard.create"}); err != nil {
			t.Fatal(err)
		}
	}
	logged := func(fake *archivetest.FakeSQL) []map[string]any {
		t.Helper()
		var out bytes.Buffer
		checkUserWidgets(ctx, f.st, fake.Client(t), f.options(), slog.New(slog.NewJSONHandler(&out, nil)))
		var lines []map[string]any
		for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			if l == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(l), &m); err != nil {
				t.Fatalf("log line %q: %v", l, err)
			}
			lines = append(lines, m)
		}
		return lines
	}

	insert("m", "SELECT count(*) AS value FROM product WHERE project_id = :project AND COALESCE(attr_position, '') <> ''",
		"SELECT count(attr_nowhere) AS value FROM product WHERE project_id = :project")
	fake := archivetest.NewFakeSQL(t)
	fake.On(archivetest.Any, archivetest.Fail(sqlengine.KindSQL, `Binder Error: Referenced column "attr_nowhere" not found`))
	fake.On(scopeIs(1), archivetest.Fail(sqlengine.KindSQL, notFound))
	fake.On(archivetest.And(scopeIs(2), archivetest.SQLHas("attr_position")), archivetest.Shape("value"))
	lines := logged(fake)
	if len(lines) != 1 || lines[0]["msg"] != "widget SQL fails on the event files" || lines[0]["title"] != "w1" ||
		lines[0]["error"] != notFound {
		t.Fatalf("logged %v; want w1 alone failing, with project 1's error (w0 runs on project 2)", lines)
	}
	if n := len(fake.Requests()); n != 4 {
		t.Errorf("%d queries, want 4: w0 on 1 and 2, w1 on 1 and 2", n)
	}

	// Three widgets more, each refused on project 1 and timing out on 2.
	ds, err := f.st.ListDashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if err := f.st.SetDashboardsArchived(ctx, []int64{d.ID}, true, store.AuditEntry{Actor: "test", Action: "dashboard.archive"}); err != nil {
			t.Fatal(err)
		}
	}
	const slow = "SELECT count(attr_position) AS value FROM product WHERE project_id = :project"
	insert("n", slow, slow, slow, slow)
	fake = archivetest.NewFakeSQL(t)
	fake.On(scopeIs(1), archivetest.Fail(sqlengine.KindSQL, notFound))
	fake.On(scopeIs(2), archivetest.Fail(sqlengine.KindTimeout, "the query exceeded X-Timeout-Ms"))
	const skipped = "could not check widget on the event files"
	var msgs []string
	for _, l := range logged(fake) {
		msgs = append(msgs, fmt.Sprint(l["msg"]))
	}
	if want := []string{skipped, skipped, skipped + "; the widgets after it go unchecked until the next start"}; !slices.Equal(msgs, want) {
		t.Errorf("logged %q, want %q", msgs, want)
	}
	if n := len(fake.Requests()); n != 6 {
		t.Errorf("%d queries, want 6: three widgets on two projects each", n)
	}
}
