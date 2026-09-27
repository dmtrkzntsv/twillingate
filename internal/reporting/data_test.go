package reporting

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
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

// TestWidgetDataRefusesFromAfterToday: a wholly future range (both from
// and to past today) must be refused outright, not silently reordered by
// clamping "to" back to today while leaving a still-future "from" ahead
// of it.
func TestWidgetDataRefusesFromAfterToday(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc, st := newTestServiceOpts(t, Options{Now: func() time.Time { return now }}, 1000)
	projectID := mustCreateProject(t, st, "p1")
	seedViewsDaily(t, st, projectID, "2026-09-20")
	d := mustCreate(t, svc, "D", lineWidget("l"))
	id := d.Widgets[0].ID

	_, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: id, From: "2026-10-01", To: "2026-10-05"})
	wantRefusal(t, err, store.ErrInvalid, "from 2026-10-01 is after today")
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

// TestWidgetDataChecksRowsPerWidgetDespiteASharedCacheAge is the
// reported repro: a "table" widget (open columns, no checks) and a
// "stat" widget (declares "value" must be a number) reading the exact
// same sql, with a non-zero CacheAge. Before keying the cache by widget
// id and re-running checkRows after every cache read (not only inside
// the loader), the table widget's cached raw rows would be handed
// straight to the stat widget's request on a hit, skipping stat's own
// column check entirely — "stat served unchecked rows". Here the row is
// mutated to something stat cannot accept between the two reads, so a
// correct implementation must still refuse the second one.
func TestWidgetDataChecksRowsPerWidgetDespiteASharedCacheAge(t *testing.T) {
	now := time.Now()
	svc, _ := newTestServiceOpts(t,
		Options{CacheAge: time.Minute, RefreshAge: time.Minute, Now: func() time.Time { return now }}, 1000)
	rawExecOn(t, svc, "CREATE TABLE probe(value TEXT)")
	rawExecOn(t, svc, "INSERT INTO probe(value) VALUES ('1')") // numeric: fits stat's Validate-time sample
	content := "SELECT value FROM probe"

	dTable := mustCreate(t, svc, "D1", tableWidget("t", content))
	dStat := mustCreate(t, svc, "D2", WidgetSpec{Name: "s", Component: "stat", Title: "s",
		Source: Source{Type: "sql", Content: content}})
	tableID, statID := dTable.Widgets[0].ID, dStat.Widgets[0].ID

	rawExecOn(t, svc, "UPDATE probe SET value='not-a-number'")

	ctx := context.Background()
	if _, err := svc.WidgetData(ctx, DataRequest{WidgetID: tableID}); err != nil {
		t.Fatalf("table widget (loads and caches the mutated row; open columns, nothing to check): %v", err)
	}
	_, err := svc.WidgetData(ctx, DataRequest{WidgetID: statID})
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("stat widget after the table widget's identical-sql load populated the cache: err = %v, want ErrInvalid", err)
	}
}

// stubSQLSource is a SourceType stub standing in for "sql" so a test can
// control exactly when Load starts and returns, and observe whether the
// context it received was cancelled.
type stubSQLSource struct {
	started chan struct{}
	unblock chan struct{}
	once    sync.Once
}

func (s *stubSQLSource) Name() string                                      { return "sql" }
func (s *stubSQLSource) Cacheable() bool                                   { return true }
func (s *stubSQLSource) Follows(string) (project, rng bool)                { return false, false }
func (s *stubSQLSource) Validate(context.Context, string, Component) error { return nil }
func (s *stubSQLSource) Load(ctx context.Context, content string, p Params) (any, error) {
	s.once.Do(func() { close(s.started) })
	<-s.unblock
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return readsql.Result{Columns: []string{"n"}, Rows: [][]string{{"1"}}}, nil
}

// TestWidgetDataSharedLoadIgnoresACallerCancellingMidFlight: two
// concurrent requests for the same widget share one load (singleflight).
// The first caller's context is cancelled while that load is still in
// flight; it must not turn into a false failure for the second caller,
// who is still waiting on the same shared call. WidgetData is expected
// to run the load against context.WithoutCancel(ctx), which readsql
// itself (real callers) times out on its own terms regardless.
func TestWidgetDataSharedLoadIgnoresACallerCancellingMidFlight(t *testing.T) {
	svc := newTestService(t)
	d := mustCreate(t, svc, "D", tableWidget("t", "SELECT 1 AS n"))
	id := d.Widgets[0].ID

	stub := &stubSQLSource{started: make(chan struct{}), unblock: make(chan struct{})}
	svc.sources["sql"] = stub

	ctx1, cancel1 := context.WithCancel(context.Background())
	var err1, err2 error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err1 = svc.WidgetData(ctx1, DataRequest{WidgetID: id})
	}()
	<-stub.started // goroutine 1 is the singleflight leader, now blocked inside Load

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err2 = svc.WidgetData(context.Background(), DataRequest{WidgetID: id})
	}()
	time.Sleep(50 * time.Millisecond) // let goroutine 2's own GetWidget/components reads land it on sf.Do
	cancel1()
	close(stub.unblock)
	wg.Wait()

	if err2 != nil {
		t.Errorf("second caller = %v, want success: the first caller's cancellation must not leak into a shared load", err2)
	}
	_ = err1 // what the cancelling caller itself sees back is not this test's concern
}

// TestWidgetDataCachedAtAndRefreshAfter asserts the actual relationship
// the envelope promises on a sql widget — CachedAt set, and RefreshAfter
// exactly CachedAt + Options.RefreshAge — rather than recomputing the
// same formula the implementation uses, and that both are UTC even when
// the process clock is not.
func TestWidgetDataCachedAtAndRefreshAfter(t *testing.T) {
	now := time.Now().In(time.FixedZone("UTC+3", 3*3600))
	refreshAge := 30 * time.Second
	svc, st := newTestServiceOpts(t,
		Options{CacheAge: time.Minute, RefreshAge: refreshAge, Now: func() time.Time { return now }}, 1000)
	projectID := mustCreateProject(t, st, "p1")
	seedViewsDaily(t, st, projectID, "2026-08-20")
	d := mustCreate(t, svc, "D", statWidget("s"))
	id := d.Widgets[0].ID

	got, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: id, ProjectID: projectID})
	if err != nil {
		t.Fatal(err)
	}
	if got.CachedAt == nil {
		t.Fatal("CachedAt = nil, want set for a sql envelope")
	}
	want := got.CachedAt.Add(refreshAge)
	if got.RefreshAfter == nil || !got.RefreshAfter.Equal(want) {
		t.Errorf("RefreshAfter = %v, want CachedAt + RefreshAge = %v", got.RefreshAfter, want)
	}
	if got.CachedAt.Location() != time.UTC || got.RefreshAfter.Location() != time.UTC {
		t.Errorf("CachedAt/RefreshAfter in %v/%v, want UTC", got.CachedAt.Location(), got.RefreshAfter.Location())
	}
}

// TestWidgetDataSourceUpdateMisses: updating a widget's source content
// must be visible on the very next read, even with a non-zero CacheAge —
// the new content's key differs from the old one's (Deviation 2), so
// there is nothing to invalidate; it is simply a different cache entry.
func TestWidgetDataSourceUpdateMisses(t *testing.T) {
	svc, _ := newTestServiceOpts(t, Options{CacheAge: time.Minute, RefreshAge: time.Minute}, 1000)
	d := mustCreate(t, svc, "D", tableWidget("t", "SELECT 1 AS n"))
	id := d.Widgets[0].ID
	ctx := context.Background()

	got1, err := svc.WidgetData(ctx, DataRequest{WidgetID: id})
	if err != nil {
		t.Fatal(err)
	}
	if res1 := got1.Data.(readsql.Result); res1.Rows[0][0] != "1" {
		t.Fatalf("before the update: rows = %v, want [[1]]", res1.Rows)
	}

	if _, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Source: &Source{Type: "sql", Content: "SELECT 2 AS n"}}); err != nil {
		t.Fatal(err)
	}

	got2, err := svc.WidgetData(ctx, DataRequest{WidgetID: id})
	if err != nil {
		t.Fatal(err)
	}
	if res2 := got2.Data.(readsql.Result); res2.Rows[0][0] != "2" {
		t.Errorf("after updating the source: rows = %v, want [[2]] (a miss, not the stale cached [[1]])", res2.Rows)
	}
}
