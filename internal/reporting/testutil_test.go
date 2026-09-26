package reporting

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// newTestService opens a fresh, migrated store with testdata's components
// synced in and no system dashboards, and returns a Service over it (and
// a read-only readsql.DB on the same file, so sql widgets validate).
func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, _ := newTestServiceOpts(t, Options{}, 1000)
	return svc
}

// newTestServiceOpts is newTestService with caller-chosen Options (an
// injected clock, configured cache ages) and read-only row cap, and also
// returns the store directly — for a data/cache test that seeds rows
// (agg_views_daily) or a project no reporting.Store method exposes.
func newTestServiceOpts(t *testing.T, opt Options, maxRows int) (*Service, store.Store) {
	t.Helper()
	st, db := newTestStoreAndReadDBMaxRows(t, maxRows)
	svc := New(st, db, opt)
	syncReporting(t, svc, nil)
	return svc, st
}

// syncReporting makes the store's components testdata's, minus the names
// in drop (their widgets lose their component, as a release dropping one
// does), and its system dashboards exactly sys.
func syncReporting(t *testing.T, svc *Service, drop []string, sys ...store.SystemDashboard) {
	t.Helper()
	b, err := os.ReadFile("testdata/components.json")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	dropped := map[string]bool{}
	for _, n := range drop {
		dropped[n] = true
	}
	var rows []store.Component
	for _, c := range cs {
		if !dropped[c.Name] {
			rows = append(rows, c.row())
		}
	}
	if err := svc.st.SyncReporting(context.Background(), store.ReportingSync{
		Hash: "test", Version: "test", Components: rows, Dashboards: sys,
	}); err != nil {
		t.Fatalf("SyncReporting: %v", err)
	}
}

// systemDashboard is system dashboard 3 with one markdown widget, "note".
func systemDashboard() store.SystemDashboard {
	return store.SystemDashboard{
		ID: 3, Title: "Users", SortKey: "a0", Range: "7d",
		Widgets: []store.Widget{{
			Name: "note", Component: "markdown", SortKey: "a0", Width: 12, Height: 2,
			Props: "{}", SourceType: "md", Source: "system text",
		}},
	}
}

func ptr[T any](v T) *T { return &v }

func md(text string) Source { return Source{Type: "md", Content: text} }

// note is a markdown widget spec titled title.
func note(title string) WidgetSpec {
	return WidgetSpec{Component: "markdown", Title: title, Source: md("text of " + title)}
}

// mustCreate creates a user dashboard titled title holding specs.
func mustCreate(t *testing.T, svc *Service, title string, specs ...WidgetSpec) DashboardDetail {
	t.Helper()
	d, err := svc.CreateDashboard(context.Background(), "test", CreateDashboard{Title: title, Widgets: specs})
	if err != nil {
		t.Fatalf("CreateDashboard(%q): %v", title, err)
	}
	return d
}

// mustAdd adds spec to dashboardID after after.
func mustAdd(t *testing.T, svc *Service, dashboardID int64, after *int64, spec WidgetSpec) WidgetInfo {
	t.Helper()
	w, err := svc.AddWidget(context.Background(), "test", AddWidget{DashboardID: dashboardID, After: after, WidgetSpec: spec})
	if err != nil {
		t.Fatalf("AddWidget: %v", err)
	}
	return w
}

// widgetRows lists dashboardID's widget rows by sort key, archived ones
// included, straight from the store.
func widgetRows(t *testing.T, svc *Service, dashboardID int64) []store.Widget {
	t.Helper()
	ws, err := svc.st.ListWidgets(context.Background(), dashboardID)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

// liveNames is the names of dashboardID's live widgets, in order.
func liveNames(t *testing.T, svc *Service, dashboardID int64) []string {
	t.Helper()
	d, err := svc.Dashboard(context.Background(), dashboardID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, w := range d.Widgets {
		out = append(out, w.Name)
	}
	return out
}

// auditRows is every dashboard.* and widget.* audit row, oldest first, as
// "actor action".
func auditRows(t *testing.T, svc *Service) []string {
	t.Helper()
	res, err := svc.db.Run(context.Background(),
		`SELECT actor, action FROM audit_log
		 WHERE action LIKE 'dashboard.%' OR action LIKE 'widget.%' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range res.Rows {
		out = append(out, r[0]+" "+r[1])
	}
	return out
}

// wantRefusal fails unless err is kind with exactly msg.
func wantRefusal(t *testing.T, err, kind error, msg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want %v %q", kind, msg)
	}
	if !errors.Is(err, kind) {
		t.Errorf("err = %v, want kind %v", err, kind)
	}
	if err.Error() != msg {
		t.Errorf("err = %q, want %q", err.Error(), msg)
	}
}
