package reporting

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// seedViewsDaily inserts one agg_views_daily row per day given, for
// projectID, so a widget's sql has something real to read.
func seedViewsDaily(t *testing.T, st store.Store, projectID int64, days ...string) {
	t.Helper()
	for i, day := range days {
		rawExec(t, st, `INSERT INTO agg_views_daily (project_id, day, kind, visitors, views, sessions, bounces, duration_sec)
			VALUES (?, ?, 'web', ?, ?, ?, 0, 0)`, projectID, day, 10+i, 20+i, 5+i)
	}
}

// statWidget is a "stat" widget reading a single number for :project.
func statWidget(name string) WidgetSpec {
	return WidgetSpec{Name: name, Component: "stat", Title: name, Source: Source{Type: "sql",
		Content: "SELECT SUM(views) AS value FROM agg_views_daily WHERE project_id=:project"}}
}

// lineWidget is a "line" widget reading a day/value series for :from/:to.
func lineWidget(name string) WidgetSpec {
	return WidgetSpec{Name: name, Component: "line", Title: name, Source: Source{Type: "sql",
		Content: "SELECT day AS x, views AS y FROM agg_views_daily WHERE day BETWEEN :from AND :to"}}
}

// tableWidget is a "table" widget with fixed content and open columns:
// no switcher, no project/range required.
func tableWidget(name, content string) WidgetSpec {
	return WidgetSpec{Name: name, Component: "table", Title: name, Source: Source{Type: "sql", Content: content}}
}

// rawExecOn reaches through svc's own store, for a test that only has
// the Service (not the store.Store it wraps) in hand. svc.st's dynamic
// type is always the concrete *sqlite.DB, which implements store.Store
// in full, so the assertion holds for every Service a test builds.
func rawExecOn(t *testing.T, svc *Service, q string, args ...any) {
	t.Helper()
	rawExec(t, svc.st.(store.Store), q, args...)
}

func TestWidgetDataUnknownWidgetRefused(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: 999})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestWidgetDataArchivedRefused(t *testing.T) {
	svc := newTestService(t)
	d := mustCreate(t, svc, "D", note("A"))
	id := d.Widgets[0].ID
	if err := svc.ArchiveWidget(context.Background(), "test", id); err != nil {
		t.Fatal(err)
	}
	_, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: id})
	wantRefusal(t, err, store.ErrNotFound, fmt.Sprintf("widget %d is archived", id))
}

func TestWidgetDataRemovedComponent(t *testing.T) {
	svc := newTestService(t)
	d := mustCreate(t, svc, "D", tableWidget("t", "SELECT 1 AS n"))
	id := d.Widgets[0].ID
	syncReporting(t, svc, []string{"table"}) // drops the component; widget survives, component NULL

	got, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: id})
	if err != nil {
		t.Fatalf("removed component: %v", err)
	}
	if !got.Removed || got.Data != nil {
		t.Errorf("got = %+v, want removed with nil data (no query run)", got)
	}
}

func TestWidgetDataFollowsProjectRequired(t *testing.T) {
	svc, st := newTestServiceOpts(t, Options{}, 1000)
	projectID := mustCreateProject(t, st, "p1")
	seedViewsDaily(t, st, projectID, "2026-08-20")
	d := mustCreate(t, svc, "D", statWidget("s"))
	id := d.Widgets[0].ID

	_, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: id})
	wantRefusal(t, err, store.ErrInvalid, fmt.Sprintf("widget %d follows the project switcher; pass project_id", id))

	got, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: id, ProjectID: projectID})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID == nil || *got.ProjectID != projectID {
		t.Errorf("ProjectID = %v, want %d", got.ProjectID, projectID)
	}
	res, ok := got.Data.(readsql.Result)
	if !ok || len(res.Rows) != 1 {
		t.Fatalf("Data = %+v", got.Data)
	}
}

func TestWidgetDataFollowsRangeRules(t *testing.T) {
	svc, st := newTestServiceOpts(t, Options{}, 1000)
	projectID := mustCreateProject(t, st, "p1")
	seedViewsDaily(t, st, projectID, "2026-08-20", "2026-08-21")
	d := mustCreate(t, svc, "D", lineWidget("l"))
	id := d.Widgets[0].ID
	ctx := context.Background()

	_, err := svc.WidgetData(ctx, DataRequest{WidgetID: id})
	wantRefusal(t, err, store.ErrInvalid, fmt.Sprintf("widget %d follows the date range; pass from and to", id))

	for _, c := range []struct{ from, to, msg string }{
		{"2026-08-01", "31/08/2026", "from and to are days, YYYY-MM-DD"},
		{"2026-08-02", "2026-08-01", "from 2026-08-02 is after to 2026-08-01"},
		{"2025-01-01", "2026-01-02", "from 2025-01-01 to 2026-01-02 spans more than 365 days"},
	} {
		_, err := svc.WidgetData(ctx, DataRequest{WidgetID: id, From: c.from, To: c.to})
		wantRefusal(t, err, store.ErrInvalid, c.msg)
	}

	got, err := svc.WidgetData(ctx, DataRequest{WidgetID: id, From: "2026-08-20", To: "2026-08-21"})
	if err != nil {
		t.Fatal(err)
	}
	if got.From != "2026-08-20" || got.To != "2026-08-21" {
		t.Errorf("echoed range = %s..%s", got.From, got.To)
	}
}

func TestWidgetDataClampsFutureTo(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc, st := newTestServiceOpts(t, Options{Now: func() time.Time { return now }}, 1000)
	projectID := mustCreateProject(t, st, "p1")
	seedViewsDaily(t, st, projectID, "2026-09-20")
	d := mustCreate(t, svc, "D", lineWidget("l"))
	id := d.Widgets[0].ID

	got, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: id, From: "2026-09-01", To: "2026-12-31"})
	if err != nil {
		t.Fatal(err)
	}
	if got.To != "2026-09-26" {
		t.Errorf("To = %s, want clamped to today 2026-09-26", got.To)
	}
}

func TestWidgetDataFixedWidgetIgnoresParams(t *testing.T) {
	svc := newTestService(t)
	d := mustCreate(t, svc, "D", tableWidget("t", "SELECT 1 AS n"))
	id := d.Widgets[0].ID
	got, err := svc.WidgetData(context.Background(),
		DataRequest{WidgetID: id, ProjectID: 7, From: "2026-01-01", To: "2026-01-02"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != nil || got.From != "" || got.To != "" {
		t.Errorf("fixed widget echoed = %+v, want none", got)
	}
}

func TestWidgetDataMarkdown(t *testing.T) {
	svc := newTestService(t)
	d := mustCreate(t, svc, "D", note("A"))
	id := d.Widgets[0].ID
	got, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: id})
	if err != nil {
		t.Fatal(err)
	}
	md, ok := got.Data.(Markdown)
	if !ok || md.Markdown != "text of A" {
		t.Errorf("Data = %+v", got.Data)
	}
	if got.CachedAt != nil || got.RefreshAfter != nil {
		t.Errorf("md widget carries CachedAt/RefreshAfter = %v/%v, want neither", got.CachedAt, got.RefreshAfter)
	}
}

func TestWidgetDataLoadFailureNamesReleaseNotes(t *testing.T) {
	svc := newTestService(t)
	d := mustCreate(t, svc, "D", tableWidget("t", "SELECT * FROM agg_views_daily"))
	id := d.Widgets[0].ID
	// Break the table the widget's sql reads, the way a release changing
	// the schema underneath an already-saved widget would.
	rawExecOn(t, svc, "DROP TABLE agg_views_daily")

	_, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: id})
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if !strings.HasSuffix(err.Error(), "; if this started after an update, see the release notes at "+ReleasesURL) {
		t.Errorf("err = %q, want it to end with the release notes pointer", err.Error())
	}
}

func TestWidgetDataRowsNoLongerFitComponentNamesReleaseNotes(t *testing.T) {
	svc := newTestService(t)
	rawExecOn(t, svc, "CREATE TABLE probe(value TEXT)")
	rawExecOn(t, svc, "INSERT INTO probe(value) VALUES ('1'), ('2')") // fits number; Validate's own sample passes
	d := mustCreate(t, svc, "D", WidgetSpec{Name: "s", Component: "stat", Title: "s",
		Source: Source{Type: "sql", Content: "SELECT value FROM probe"}})
	id := d.Widgets[0].ID

	// A release (or, here, a direct mutation standing in for one) leaves
	// a row that no longer fits stat's declared "value" column, after the
	// widget already validated against the good rows above.
	rawExecOn(t, svc, "UPDATE probe SET value='not-a-number' WHERE value='1'")

	_, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: id})
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if !strings.HasSuffix(err.Error(), "; if this started after an update, see the release notes at "+ReleasesURL) {
		t.Errorf("err = %q, want it to end with the release notes pointer", err.Error())
	}
}

func TestWidgetDataTruncated(t *testing.T) {
	svc, st := newTestServiceOpts(t, Options{}, 5) // maxRows 5: easy to trip
	projectID := mustCreateProject(t, st, "p1")
	days := make([]string, 0, 10)
	for i := 1; i <= 10; i++ {
		days = append(days, "2026-08-"+twoDigit(i))
	}
	seedViewsDaily(t, st, projectID, days...)
	d := mustCreate(t, svc, "D", tableWidget("t", "SELECT day, views FROM agg_views_daily"))
	id := d.Widgets[0].ID

	got, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: id})
	if err != nil {
		t.Fatal(err)
	}
	res, ok := got.Data.(readsql.Result)
	if !ok || !res.Truncated {
		t.Fatalf("Data = %+v, want Truncated true", got.Data)
	}
}

func twoDigit(n int) string {
	s := strconv.Itoa(n)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}
