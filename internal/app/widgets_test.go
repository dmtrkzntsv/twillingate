package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
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

// files is the console's Files over the fixture's raw/, its engine closed
// with t.
func (f *widgetsFixture) files(t *testing.T) *widgetFiles {
	t.Helper()
	eng, reader, err := openFiles(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	return newWidgetFiles(f.cfg, reader, f.reg)
}

// TestSampleProjectIsTheLiveProjectWithTheNewestFile: the newest day wins
// among live projects; an archived project's files do not count, and
// with no files there is none.
func TestSampleProjectIsTheLiveProjectWithTheNewestFile(t *testing.T) {
	f := newWidgetsFixture(t)
	ctx := context.Background()
	files := f.files(t)
	if id, err := files.SampleProject(ctx); err != nil || id != 0 {
		t.Fatalf("no files: %d, %v; want 0", id, err)
	}
	f.write(t, "e1", 1, store.FamilyViews, "2026-08-20")
	f.write(t, "e2", 2, store.FamilyViews, "2026-08-21")
	if id, err := files.SampleProject(ctx); err != nil || id != 2 {
		t.Fatalf("newest: %d, %v; want 2", id, err)
	}
	if err := f.ops.ArchiveProject(ctx, "test", 2); err != nil {
		t.Fatal(err)
	}
	if id, err := files.SampleProject(ctx); err != nil || id != 1 {
		t.Fatalf("2 archived: %d, %v; want 1", id, err)
	}
}

// TestWidgetFilesApplyTheRules: a widget's query reads the files through
// the registry's rules, as the query tool does, scoped to its project.
func TestWidgetFilesApplyTheRules(t *testing.T) {
	f := newWidgetsFixture(t)
	ctx := context.Background()
	f.write(t, "e1", 1, store.FamilyProduct, "2026-08-20")
	f.write(t, "e2", 2, store.FamilyProduct, "2026-08-20")
	files := f.files(t)
	count := func() string {
		t.Helper()
		res, err := files.Query(ctx, archive.Scoped{SQL: "SELECT count(*) AS n FROM product WHERE event_name = 'signup'", Scope: true, Project: 1})
		if err != nil {
			t.Fatal(err)
		}
		return res.Rows[0][0].(json.Number).String()
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
	checkUserWidgets(ctx, f.st, f.files(t), slog.New(slog.NewJSONHandler(&out, nil)))
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

// failingFiles is the console's Files whose every query fails with err.
type failingFiles struct {
	reporting.Files
	err     error
	queries int
}

func (f *failingFiles) Query(context.Context, archive.Scoped) (engine.Result, error) {
	f.queries++
	return engine.Result{}, f.err
}

// TestCheckUserWidgetsTellsResourceFailures: a check that runs out of time
// or memory, or whose child dies, says nothing of the widget's SQL. It is
// logged as a widget that could not be checked, never as one that fails,
// and the check stops there rather than load the engine further.
func TestCheckUserWidgetsTellsResourceFailures(t *testing.T) {
	f := newWidgetsFixture(t)
	ctx := context.Background()
	f.write(t, "e1", 1, store.FamilyViews, "2026-08-20")
	if _, err := f.st.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "Mine", SortKey: "z", Sidebar: true},
		[]store.Widget{
			{Component: "stat", SortKey: "a", Width: 3, Height: 4, Name: "one", Title: "One", Props: "{}", SourceType: "sql",
				Source: "SELECT count(*) AS value FROM views WHERE project_id = :project"},
			{Component: "stat", SortKey: "b", Width: 3, Height: 4, Name: "two", Title: "Two", Props: "{}", SourceType: "sql",
				Source: "SELECT count(*) AS value FROM views"},
		}, store.AuditEntry{Actor: "test", Action: "dashboard.create"}); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"timeout":            context.DeadlineExceeded,
		"out of memory":      &engine.QueryError{Msg: "Out of Memory Error: failed to allocate data of size 64.0 MiB (476.8 MiB/476.8 MiB used)"},
		"logs out of memory": fmt.Errorf("%w: %w", archive.ErrLoadingLogs, &engine.QueryError{Msg: "Out of Memory Error: x"}),
		"child exited":       fmt.Errorf("engine: query: %w", engine.ErrChildExited),
	} {
		t.Run(name, func(t *testing.T) {
			files := &failingFiles{Files: f.files(t), err: err}
			var out bytes.Buffer
			checkUserWidgets(ctx, f.st, files, slog.New(slog.NewJSONHandler(&out, nil)))
			if strings.Contains(out.String(), "widget SQL fails") {
				t.Errorf("a resource failure logged as a failing widget:\n%s", out.String())
			}
			var got map[string]any
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatalf("want one log line: %v\n%s", err, out.String())
			}
			if msg, _ := got["msg"].(string); !strings.HasPrefix(msg, "could not check widget on the event files") ||
				got["title"] != "One" || got["reason"] == "" {
				t.Errorf("logged %v", got)
			}
			if files.queries != 1 {
				t.Errorf("%d queries, want the check to stop after the first", files.queries)
			}
		})
	}
}

// TestMigrateChecksTheSystemWidgetsPastTheConsoleTimeout: the boot's
// check of the system widgets is no viewer's query. A CONSOLE_QUERY_TIMEOUT
// far shorter than starting the engine's child leaves it whole: the
// recorded hash is the release's, not one marked unchecked.
func TestMigrateChecksTheSystemWidgetsPastTheConsoleTimeout(t *testing.T) {
	f := newWidgetsFixture(t)
	ctx := context.Background()
	f.cfg.Console.QueryTimeout = time.Nanosecond
	if err := Migrate(ctx, f.cfg, f.st); err != nil {
		t.Fatal(err)
	}
	hash, err := f.st.ReportingHash(ctx)
	if err != nil || hash == "test" || strings.HasPrefix(hash, "unchecked") {
		t.Errorf("ReportingHash = %q, %v; want the release's, every widget checked", hash, err)
	}
}

// TestMigrateStartsWhenTheSystemCheckCannotRun: a check of the system
// widgets that runs out of time or memory, or whose child dies, is logged
// as an error and the start goes on, so the daily pass that frees the
// engine runs; a refusal of their SQL still stops it.
func TestMigrateStartsWhenTheSystemCheckCannotRun(t *testing.T) {
	f := newWidgetsFixture(t)
	ctx := context.Background()
	for name, cause := range map[string]error{
		"timeout":       context.DeadlineExceeded,
		"out of memory": fmt.Errorf("%w: %w", archive.ErrLoadingLogs, &engine.QueryError{Msg: "Out of Memory Error: x"}),
		"child exited":  engine.ErrChildExited,
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			files := &failingFiles{Files: f.files(t), err: cause}
			if err := migrateReporting(ctx, f.st, files, slog.New(slog.NewTextHandler(&out, nil))); err != nil {
				t.Fatalf("err = %v, want the start to go on", err)
			}
			if !strings.Contains(out.String(), "level=ERROR") || !strings.Contains(out.String(), "could not check the system dashboards") {
				t.Errorf("logged %q, want the error", out.String())
			}
		})
	}
	files := &failingFiles{Files: f.files(t), err: &engine.QueryError{Msg: "Binder Error: Referenced column \"nope\" not found"}}
	if err := migrateReporting(ctx, f.st, files, slog.New(slog.DiscardHandler)); err == nil || errors.Is(err, reporting.ErrUnchecked) {
		t.Errorf("DuckDB's refusal: err = %v, want the start stopped", err)
	}
}

// TestWarnImport: the console warns, naming the projects, when SQLite
// holds raw days older than the event files (or raw rows of a project with
// no files); a fresh install, whose files began with its first event,
// does not, and the import's row silences it.
func TestWarnImport(t *testing.T) {
	f := newWidgetsFixture(t)
	ctx := context.Background()
	warned := func() string {
		t.Helper()
		var out bytes.Buffer
		warnImport(ctx, f.st, f.cfg.EventsDir, slog.New(slog.NewTextHandler(&out, nil)))
		if strings.Contains(out.String(), "level=ERROR") {
			t.Fatal(out.String())
		}
		if !strings.Contains(out.String(), "twillingate events import") {
			return ""
		}
		return out.String()
	}
	held := func(id string, project int64, day string) {
		t.Helper()
		ts, err := time.Parse("2006-01-02", day)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.st.WriteEvents(ctx, []store.Event{{ID: id, ProjectID: project, Family: store.FamilyViews, EventName: "$page_view",
			TS: ts.Add(12 * time.Hour), Kind: "web", ActorID: "a", ActorKind: store.ActorInstall, SampleRate: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := warned(); got != "" {
		t.Errorf("an empty install warns: %s", got)
	}
	// A fresh install: every event both in SQLite and in the files.
	held("e1", 1, "2026-08-20")
	f.write(t, "e1", 1, store.FamilyViews, "2026-08-20")
	if got := warned(); got != "" {
		t.Errorf("a fresh install warns: %s", got)
	}
	// An upgraded one: SQLite reaches back before the files' first day.
	held("e0", 1, "2026-08-15")
	if got := warned(); !strings.Contains(got, "projects=[1]") {
		t.Errorf("rows before the files' first day: %q, want a warning naming project 1", got)
	}
	held("d0", 2, "2026-08-21")
	if got := warned(); !strings.Contains(got, "projects=\"[1 2]\"") && !strings.Contains(got, "projects=[1 2]") {
		t.Errorf("a project with rows and no files: %q, want a warning naming projects 1 and 2", got)
	}
	if err := f.st.SetMeta(ctx, eventsImportedKey, "2026-10-14T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if got := warned(); got != "" {
		t.Errorf("an import's row does not silence the warning: %s", got)
	}
}

// failingMarks is importMarks whose reads fail.
type failingMarks struct{}

func (failingMarks) GetMeta(context.Context, string) (string, error) { return "", nil }
func (failingMarks) OldestRawDays(context.Context) (map[int64]string, error) {
	return nil, errors.New("disk I/O error")
}

// TestWarnImportLogsAFailure: the check is advice, so a store that cannot
// answer is logged at error and the console starts all the same.
func TestWarnImportLogsAFailure(t *testing.T) {
	var out bytes.Buffer
	warnImport(context.Background(), failingMarks{}, t.TempDir(), slog.New(slog.NewTextHandler(&out, nil)))
	if !strings.Contains(out.String(), "level=ERROR") || !strings.Contains(out.String(), "disk I/O error") {
		t.Errorf("log = %q, want the error at level ERROR", out.String())
	}
}
