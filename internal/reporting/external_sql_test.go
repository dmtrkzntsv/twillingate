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
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// TestNoEngineRefusesSQL is Review Focus 1: a stored sql widget on a
// dashboard, served by a process with no SQL engine, answers
// ErrSQLEngine on its own card, through GET and the stream, while the
// dashboard's other widgets load; saving and previewing sql are refused
// the same way, sql is no source type, and the start-up check of the
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
	if got := strings.Join(none.SourceTypes(), ","); got != "md,query" {
		t.Errorf("source types = %s, want md,query", got)
	}

	failed, err := CheckUserWidgets(ctx, with.st, nil, Options{})
	if err != nil || len(failed) != 0 {
		t.Errorf("CheckUserWidgets without an engine = %v, %v; want none checked", failed, err)
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
			wantRefusal(t, err, store.ErrInvalid, enginesql.EngineFailed)
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
