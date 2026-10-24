package reporting

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/reporting/sqlbind"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// TestNoEngineRefusesSQL is Review Focus 1: a stored sql widget on a
// dashboard, served by a process with no SQL engine, answers
// ErrSQLEngine on its own card, through GET and the stream, while the
// dashboard's other widgets load; saving, previewing and duplicating sql
// are refused the same way, sql is no source type, and the start-up check of the
// users' widgets checks none.
func TestNoEngineRefusesSQL(t *testing.T) {
	ctx := context.Background()
	with := newTestService(t)
	d := mustCreate(t, with, "DD", statWidget("visits"), note("About"))
	none := New(with.st, nil, Options{QueryMaxRows: 1000})
	var sqlID, mdID int64
	for _, w := range d.Widgets {
		if w.Source.Type == "sql" {
			sqlID = w.ID
		} else {
			mdID = w.ID
		}
	}
	isRefusal := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrSQLEngine) || !errors.Is(err, store.ErrInvalid) ||
			!strings.HasSuffix(err.Error(), "SQL needs an external SQL engine (SQL_ENGINE_SOCKET)") {
			t.Errorf("%s: err = %v, want ErrSQLEngine", what, err)
		}
	}

	_, err := none.WidgetData(ctx, DataRequest{WidgetID: sqlID, ProjectID: 1, From: "2026-09-01", To: "2026-09-02"})
	isRefusal("GET", err)
	_, err = none.WidgetData(ctx, DataRequest{WidgetID: sqlID}) // no project: the refusal still comes first
	isRefusal("GET without a project", err)
	if got, err := none.WidgetData(ctx, DataRequest{WidgetID: mdID}); err != nil || got.Data == nil {
		t.Errorf("markdown by GET = %+v, %v; want it loaded", got, err)
	}
	reqs := []WidgetRequest{{WidgetID: sqlID, ProjectID: 1}, {WidgetID: mdID}}
	for _, r := range streamAll(t, ctx, none, reqs) {
		switch r.WidgetID {
		case sqlID:
			isRefusal("stream", r.Err)
		case mdID:
			if r.Err != nil || r.Data.Data == nil {
				t.Errorf("markdown by the stream = %+v, %v; want it loaded", r.Data, r.Err)
			}
		}
	}
	if got, err := none.Dashboard(ctx, d.ID); err != nil || !got.FollowsProject || len(got.Widgets) != 2 || !got.Widgets[0].FollowsProject {
		t.Errorf("the dashboard = %+v, %v; want it read, its sql widget following the project its SQL names", got, err)
	}

	_, err = none.AddWidget(ctx, "test", AddWidget{DashboardID: d.ID, WidgetSpec: statWidget("more")})
	isRefusal("add", err)
	_, err = none.CreateDashboard(ctx, "test", CreateDashboard{Title: "EE", Widgets: []WidgetSpec{statWidget("v")}})
	isRefusal("create", err)
	_, err = none.Preview(ctx, PreviewIn{Component: "stat", Source: Source{Type: "sql", Content: "SELECT 1 AS value"}})
	isRefusal("preview", err)
	_, err = none.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: d.ID})
	isRefusal("duplicate", err)
	_, err = none.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: d.ID, WholeGroup: true})
	isRefusal("duplicate the group", err)
	md := mustCreate(t, with, "MD", note("About"))
	if got, err := none.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: md.ID}); err != nil || len(got.Widgets) != 1 {
		t.Errorf("duplicating a dashboard with no sql widget = %+v, %v; want it copied", got, err)
	}
	if got := strings.Join(none.SourceTypes(), ","); got != "md,query" {
		t.Errorf("source types = %s, want md,query", got)
	}
	listed, err := none.ListedComponents(ctx)
	if err != nil || len(listed) == 0 {
		t.Fatalf("listed components = %v, %v", listed, err)
	}
	for _, c := range listed {
		if slices.Contains(c.Accepts, "sql") {
			t.Errorf("%s still accepts sql without an engine: %v", c.Name, c.Accepts)
		}
	}
	if all, _ := with.ListedComponents(ctx); !slices.ContainsFunc(all, func(c Component) bool { return slices.Contains(c.Accepts, "sql") }) {
		t.Error("with an engine, no component accepts sql")
	}

	failed, err := CheckUserWidgets(ctx, with.st, nil, Options{})
	if err != nil || len(failed) != 0 {
		t.Errorf("CheckUserWidgets without an engine = %v, %v; want none checked", failed, err)
	}
}

// TestCallerGoneBeforeTheEngineTimeout: a request whose caller went away
// keeps the caller's error even when the engine answered timeout.
func TestCallerGoneBeforeTheEngineTimeout(t *testing.T) {
	gone := failing(&sqlengine.Error{Status: 504, Kind: sqlengine.KindTimeout, Message: "the query exceeded X-Timeout-Ms"})
	s := &sqlSource{eng: gone.eng(t)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Load(ctx, "SELECT 1", Params{})
	if errors.Is(err, store.ErrInvalid) || resourceCause(err) != nil {
		t.Errorf("err = %v, want the engine's answer passed through, no timeout advice", err)
	}
}

// TestEngineDownMidFlight is Review Focus 2: an engine that is not there,
// or that drops the connection with the query in flight, reads as the
// engine having failed, a resourceErr (no verdict on the SQL), and leaves
// no goroutine behind.
func TestEngineDownMidFlight(t *testing.T) {
	dir, err := os.MkdirTemp(filepath.Join(os.Getenv("HOME"), ".cache"), "sqle-down-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	// Drops every connection once the request is read.
	sock := filepath.Join(dir, "drop.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	before := runtime.NumGoroutine()
	for name, socket := range map[string]string{"missing": filepath.Join(dir, "missing.sock"), "dropped": sock} {
		c := sqlengine.Dial(socket)
		s := &sqlSource{eng: newSQLEngine(c, Options{EventsDir: t.TempDir(), QueryTimeout: 5 * time.Second, QueryMaxRows: 10})}
		for range 5 {
			_, err := s.Load(context.Background(), "SELECT 1", Params{})
			wantRefusal(t, err, store.ErrInvalid, sqlbind.EngineFailed)
			if cause := resourceCause(err); !errors.Is(cause, sqlengine.ErrUnavailable) {
				t.Errorf("%s: cause = %v, want ErrUnavailable", name, cause)
			}
		}
		c.Close()
	}
	srv.Close()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Errorf("%d goroutines after, %d before: the failed queries left some behind", n, before)
	}
}

// TestStreamCancelReachesTheEngine is Review Focus 4: a stream whose
// client goes away cancels its sql widget's query, and the engine sees
// the request end before its answer.
func TestStreamCancelReachesTheEngine(t *testing.T) {
	svc := newTestService(t)
	// Fast on the sample project (none: 0), slow on project 1.
	slow := WidgetSpec{Name: "slow", Component: "stat", Title: "Slow", Source: Source{Type: "sql",
		Content: "SELECT sum(hash(i)) AS value FROM range(:project * 100000000000) t(i)"}}
	d := mustCreate(t, svc, "DD", slow)
	eng := filesOf(t, svc).sqle

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []WidgetResult, 1)
	go func() {
		var got []WidgetResult
		svc.WidgetDataStream(ctx, []WidgetRequest{{WidgetID: d.Widgets[0].ID, ProjectID: 1}}, func(r WidgetResult) { got = append(got, r) })
		done <- got
	}()
	time.Sleep(500 * time.Millisecond) // the query is running
	cancel()
	select {
	case got := <-done:
		if len(got) != 1 || got[0].Err == nil {
			t.Errorf("results = %+v, want the cancelled load's error", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not end after its context was cancelled")
	}
	deadline := time.Now().Add(10 * time.Second)
	for eng.Cancelled() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if eng.Cancelled() == 0 {
		t.Error("the engine never saw the query cancelled")
	}
}

// vanishing deletes the first log or Parquet file of the spec it is sent,
// before it passes the first query on (the daily pass removing a file
// between the spec and the engine's read), and with always, answers
// stale every time without passing any on.
type vanishing struct {
	SQL
	dir     string
	always  bool
	specs   []sqlengine.Tables
	deleted string
}

func (v *vanishing) Query(ctx context.Context, req sqlengine.Request) (sqlengine.Result, error) {
	v.specs = append(v.specs, req.Tables)
	if v.always {
		return sqlengine.Result{}, &sqlengine.Error{Status: 409, Kind: sqlengine.KindStale, Message: "a file in the table spec is gone"}
	}
	if v.deleted == "" {
		for _, p := range req.Tables.Projects {
			if files := append(slices.Clone(p.Logs), p.Parquet...); len(files) > 0 {
				v.deleted = files[0]
				if err := os.Remove(filepath.Join(v.dir, filepath.FromSlash(v.deleted))); err != nil {
					return sqlengine.Result{}, err
				}
				break
			}
		}
	}
	return v.SQL.Query(ctx, req)
}

// TestStaleSpecRetried: a file removed between the table spec and the
// engine's read answers stale, and the widget's query goes again with a
// fresh spec, without that file; a spec stale on every attempt is given
// up after sqlengine.StaleAttempts, with advice and no verdict on the SQL.
func TestStaleSpecRetried(t *testing.T) {
	f := newTestFiles(t)
	f.Write(t, event(t, "e1", 1, store.FamilyViews, "pageview", "2026-10-01"), event(t, "e2", 1, store.FamilyViews, "pageview", "2026-10-02"))
	v := &vanishing{SQL: f, dir: f.Dir}
	s := &sqlSource{eng: newSQLEngine(v, f.options(Options{}))}
	res, err := s.Load(context.Background(), "SELECT count(*) AS n FROM views WHERE project_id = :project", Params{ProjectID: 1})
	if rows, ok := res.(readsql.Result); err != nil || !ok || len(rows.Rows) != 1 {
		t.Fatalf("after a stale spec: %+v, %v; want the retry's answer", res, err)
	}
	if v.deleted == "" || len(v.specs) != 2 || slices.ContainsFunc(v.specs[1].Projects, func(p sqlengine.Project) bool {
		return slices.Contains(p.Logs, v.deleted) || slices.Contains(p.Parquet, v.deleted)
	}) {
		t.Errorf("deleted %q, sent %d specs; want a second without it", v.deleted, len(v.specs))
	}

	always := &vanishing{SQL: f, dir: f.Dir, always: true}
	s = &sqlSource{eng: newSQLEngine(always, f.options(Options{}))}
	_, err = s.Load(context.Background(), "SELECT 1 AS n", Params{})
	wantRefusal(t, err, store.ErrInvalid, sqlbind.Stale)
	if resourceCause(err) == nil || len(always.specs) != sqlengine.StaleAttempts {
		t.Errorf("always stale: %d attempts, cause %v; want %d and a resource failure", len(always.specs), resourceCause(err), sqlengine.StaleAttempts)
	}
}
