package jobs

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	_ "github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
	_ "modernc.org/sqlite"
)

var jobsProjectSpecs = []manage.ProjectSpec{
	{Name: "App", AllowedOrigins: []string{"https://a.com"},
		Attributes: []string{"plan"}},
}

// jobsVars pins the retention windows the assertions below rely on: a
// 7-day raw window puts the fixed 2026-08-10 fixture day outside it relative
// to the fake now of 2026-08-22.
var jobsVars = map[string]string{
	"RETENTION_VIEWS_RAW_DAYS": "7", "RETENTION_VIEWS_AGGREGATE_DAYS": "365",
	"RETENTION_PRODUCT_RAW_DAYS": "7", "RETENTION_PRODUCT_AGGREGATE_DAYS": "365",
}

// countingStore counts daily passes; IncrementalVacuum runs exactly once per
// pass, which makes it a reliable proxy.
type countingStore struct {
	store.Store
	vacuums atomic.Int64
}

func (c *countingStore) IncrementalVacuum(ctx context.Context) error {
	c.vacuums.Add(1)
	return c.Store.IncrementalVacuum(ctx)
}

type countingRotator struct {
	*identity.Salter
	rotations atomic.Int64
}

func (c *countingRotator) Rotate(ctx context.Context) error {
	c.rotations.Add(1)
	return c.Salter.Rotate(ctx)
}

func openStoreAt(t *testing.T) (store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jobs.db")
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st, path
}

func openStore(t *testing.T) store.Store {
	t.Helper()
	st, _ := openStoreAt(t)
	return st
}

// newRegistry builds a registry over st, seeded with specs via the audited
// Ops path (mirrors production: registry rows come from CreateProject, not
// a direct insert).
func newRegistry(t *testing.T, st store.Store, cfg *config.Config, specs []manage.ProjectSpec) *manage.Registry {
	t.Helper()
	ctx := context.Background()
	reg := manage.New(st, slog.Default())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := manage.NewOps(reg, st)
	for _, spec := range specs {
		if _, err := ops.CreateProject(ctx, "test", spec); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

// setup seeds a fresh store and registry from specs and returns a Runner
// wired to both, using a fixed clock (2026-08-22T04:00:00Z).
func setup(t *testing.T, vars map[string]string, specs []manage.ProjectSpec) (store.Store, *manage.Registry, *Runner) {
	t.Helper()
	cfg := configtest.Load(t, vars)
	st, path := openStoreAt(t)
	t.Setenv("JOBS_TEST_DB", path)
	reg := newRegistry(t, st, cfg, specs)
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	return st, reg, New(st, cfg, reg, identity.NewSalter(st, now), slog.Default(), now)
}

func mustTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func mustDay(s string) civil.Date { d, _ := civil.Parse(s); return d }

func TestRunDailyPassAggregatesOldDays(t *testing.T) {
	st, _, r := setup(t, jobsVars, jobsProjectSpecs)
	ctx := context.Background()
	// Old day (beyond the 7-day raw window relative to fake now 2026-08-22).
	if err := st.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyViews, ID: "1", ProjectID: 1, TS: mustTime("2026-08-10T10:00:00Z"), ReceivedAt: mustTime("2026-08-10T10:00:00Z"),
			Kind: "web", ActorID: "v", ActorKind: store.ActorConnection, Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyProduct, ID: "2", ProjectID: 1, EventName: "e", UserID: "u", TS: mustTime("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"plan": "pro"}}}); err != nil {
		t.Fatal(err)
	}
	// Recent day (inside the window) must survive as raw.
	if err := st.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyViews, ID: "3", ProjectID: 1, TS: mustTime("2026-08-21T10:00:00Z"), ReceivedAt: mustTime("2026-08-21T10:00:00Z"),
			Kind: "web", ActorID: "v", ActorKind: store.ActorConnection, Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	oldWeb, err := st.ViewDaysBefore(ctx, 1, mustDay("2026-08-20"))
	if err != nil {
		t.Fatal(err)
	}
	if len(oldWeb) != 0 {
		t.Fatalf("old web raw must be gone: %v", oldWeb)
	}
	recent, err := st.ViewDaysBefore(ctx, 1, mustDay("2026-08-23"))
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 {
		t.Fatalf("recent raw must survive: %v", recent)
	}
	oldProd, err := st.ProductDaysBefore(ctx, 1, mustDay("2026-08-20"))
	if err != nil {
		t.Fatal(err)
	}
	if len(oldProd) != 0 {
		t.Fatalf("old product raw must be gone: %v", oldProd)
	}
	// Both families share one raw table: each must have been rolled up, not
	// deleted by the other family's pass before its own ran.
	if got := queryDays(t, `SELECT kind || ':' || views FROM agg_views_daily
		WHERE project_id=1 AND day='2026-08-10'`); len(got) != 1 || got[0] != "web:1" {
		t.Errorf("agg_views_daily for 2026-08-10 = %v, want [web:1]", got)
	}
	if got := queryDays(t, `SELECT event_name || ':' || count FROM agg_product_daily
		WHERE project_id=1 AND day='2026-08-10'`); len(got) != 1 || got[0] != "e:1" {
		t.Errorf("agg_product_daily for 2026-08-10 = %v, want [e:1]", got)
	}
	// Second pass is a no-op (idempotency at the job level).
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 {
		t.Fatal("second pass must not disturb in-window raw data")
	}
}

// Each family ages out by its own raw window. With the windows apart, the
// family still inside its window must keep its raw rows for the shared day
// (the other family's pass must not delete them) and must not be rolled up.
func TestRunDailyPassAggregatesEachFamilyByItsOwnWindow(t *testing.T) {
	for _, tc := range []struct {
		name              string
		viewsRaw, prodRaw string
		wantViewsAgg      bool
		wantProductAgg    bool
	}{
		{"views aged out, product inside", "7", "30", true, false},
		{"product aged out, views inside", "30", "7", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := map[string]string{
				"RETENTION_VIEWS_RAW_DAYS": tc.viewsRaw, "RETENTION_VIEWS_AGGREGATE_DAYS": "365",
				"RETENTION_PRODUCT_RAW_DAYS": tc.prodRaw, "RETENTION_PRODUCT_AGGREGATE_DAYS": "365",
			}
			st, _, r := setup(t, vars, jobsProjectSpecs)
			ctx := context.Background()
			// 2026-08-10 is 12 days before the fake now: outside a 7-day
			// window, inside a 30-day one.
			old := mustTime("2026-08-10T10:00:00Z")
			if err := st.WriteEvents(ctx, []store.Event{
				{Family: store.FamilyViews, ID: "v", ProjectID: 1, TS: old, ReceivedAt: old,
					Kind: "web", ActorID: "v", ActorKind: store.ActorConnection, Path: "/"},
				{Family: store.FamilyProduct, ID: "p", ProjectID: 1, EventName: "e", UserID: "u", TS: old, ReceivedAt: old},
			}); err != nil {
				t.Fatal(err)
			}
			if err := r.RunDailyPass(ctx); err != nil {
				t.Fatal(err)
			}
			rawViews := queryDays(t, `SELECT id FROM raw_views WHERE project_id=1 AND day='2026-08-10'`)
			rawProduct := queryDays(t, `SELECT id FROM raw_product WHERE project_id=1 AND day='2026-08-10'`)
			aggViews := queryDays(t, `SELECT day FROM agg_views_daily WHERE project_id=1 AND day='2026-08-10'`)
			aggProduct := queryDays(t, `SELECT day FROM agg_product_daily WHERE project_id=1 AND day='2026-08-10'`)
			if tc.wantViewsAgg {
				if len(rawViews) != 0 || len(aggViews) != 1 {
					t.Errorf("views: raw %v agg %v, want rolled up", rawViews, aggViews)
				}
			} else if len(rawViews) != 1 || len(aggViews) != 0 {
				t.Errorf("views: raw %v agg %v, want raw kept and not rolled up", rawViews, aggViews)
			}
			if tc.wantProductAgg {
				if len(rawProduct) != 0 || len(aggProduct) != 1 {
					t.Errorf("product: raw %v agg %v, want rolled up", rawProduct, aggProduct)
				}
			} else if len(rawProduct) != 1 || len(aggProduct) != 0 {
				t.Errorf("product: raw %v agg %v, want raw kept and not rolled up", rawProduct, aggProduct)
			}
		})
	}
}

// The pass must rebuild v_events_flat from the keys actually present, so a
// newly seen attribute becomes queryable without a restart.
func TestRunDailyPassRebuildsFlatView(t *testing.T) {
	st, _, r := setup(t, jobsVars, jobsProjectSpecs)
	ctx := context.Background()
	// Inside the raw window, so it survives to be discovered.
	if err := st.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyProduct, ID: "1", ProjectID: 1, EventName: "e", UserID: "u", TS: mustTime("2026-08-21T10:00:00Z"),
			Attributes: map[string]string{"plan": "pro"}}}); err != nil {
		t.Fatal(err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	// Inspect the view through a separate read-only handle: Store deliberately
	// does not expose its *sql.DB.
	raw, err := sql.Open("sqlite", "file:"+os.Getenv("JOBS_TEST_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var plan string
	if err := raw.QueryRow(`SELECT attr_plan FROM v_events_flat WHERE id='1'`).Scan(&plan); err != nil {
		t.Fatalf("v_events_flat not rebuilt with the discovered key: %v", err)
	}
	if plan != "pro" {
		t.Errorf("attr_plan = %q, want pro", plan)
	}
}

// A project that has been archived in the registry must still be
// maintained: it is still a registry row, so store.ProjectIDs (all rows,
// including archived) still returns it.
func TestRunDailyPassCoversArchivedProjects(t *testing.T) {
	st, reg, r := setup(t, jobsVars, jobsProjectSpecs)
	ctx := context.Background()
	ops := manage.NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "test", manage.ProjectSpec{Name: "Gone"}); err != nil {
		t.Fatal(err)
	}
	if err := ops.ArchiveProject(ctx, "test", 2); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyViews, ID: "1", ProjectID: 2, TS: mustTime("2026-08-10T10:00:00Z"), ReceivedAt: mustTime("2026-08-10T10:00:00Z"),
			Kind: "web", ActorID: "v", ActorKind: store.ActorConnection, Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	left, err := st.ViewDaysBefore(ctx, 2, mustDay("2026-08-20"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("archived project not aggregated: %v", left)
	}
}

// TestPruneUsesGlobalWindowsForEveryProject: retention is global, so two
// projects with the same aged-out data lose it on the same pass.
func TestPruneUsesGlobalWindowsForEveryProject(t *testing.T) {
	_, _, r := setup(t, jobsVars, []manage.ProjectSpec{
		{Name: "App", AllowedOrigins: []string{"https://a.com"}},
		{Name: "Shop", AllowedOrigins: []string{"https://s.com"}},
	})
	ctx := context.Background()
	for _, id := range []int64{1, 2} {
		if _, err := rawExec(t, `INSERT INTO agg_views_daily (project_id, day, kind, visitors, views, sessions, bounces, duration_sec)
			VALUES (?, '2024-01-01', 'web', 1, 1, 1, 0, 0), (?, '2026-08-20', 'web', 1, 1, 1, 0, 0)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2} {
		days := queryDays(t, `SELECT day FROM agg_views_daily WHERE project_id=? ORDER BY day`, id)
		if len(days) != 1 || days[0] != "2026-08-20" {
			t.Errorf("project %d kept %v, want only 2026-08-20 (365-day aggregate window)", id, days)
		}
	}
}

// rawExec runs one statement against the store's file through a second
// connection, for seeding rows the Store interface has no writer for.
func rawExec(t *testing.T, q string, args ...any) (sql.Result, error) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+os.Getenv("JOBS_TEST_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	return db.Exec(q, args...)
}

func queryDays(t *testing.T, q string, args ...any) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+os.Getenv("JOBS_TEST_DB")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

// The scheduler must fire salt rotation at 00:00 and the daily pass at 03:00,
// each at most once per day, and neither at other hours.
func TestScheduleFiresOncePerDay(t *testing.T) {
	cfg := configtest.Load(t, jobsVars)
	clock := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	st := &countingStore{Store: openStore(t)}
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	rot := &countingRotator{Salter: identity.NewSalter(st, now)}
	r := New(st, cfg, reg, rot, slog.Default(), now)
	ctx := context.Background()

	at := func(h, m int) {
		t.Helper()
		clock = time.Date(2026, 8, 22, h, m, 0, 0, time.UTC)
		r.runScheduled(ctx)
	}
	at(0, 0)
	at(0, 30) // same hour, same day: must not rotate twice
	if got := rot.rotations.Load(); got != 1 {
		t.Errorf("rotations = %d, want 1", got)
	}
	at(1, 0)
	if got := st.vacuums.Load(); got != 0 {
		t.Errorf("daily pass ran at 01:00 (%d vacuums)", got)
	}
	at(3, 0)
	at(3, 45) // same hour, same day: must not run twice
	if got := st.vacuums.Load(); got != 1 {
		t.Errorf("daily passes = %d, want 1", got)
	}

	// Next day: both fire again.
	clock = time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	r.runScheduled(ctx)
	clock = time.Date(2026, 8, 23, 3, 0, 0, 0, time.UTC)
	r.runScheduled(ctx)
	if got := rot.rotations.Load(); got != 2 {
		t.Errorf("rotations = %d, want 2 after a day boundary", got)
	}
	if got := st.vacuums.Load(); got != 2 {
		t.Errorf("daily passes = %d, want 2 after a day boundary", got)
	}
}

// Boot must run a catch-up pass immediately so downtime never skips a day,
// and Run must return when the context is cancelled.
func TestRunBootCatchUpAndCancel(t *testing.T) {
	cfg := configtest.Load(t, jobsVars)
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	st := &countingStore{Store: openStore(t)}
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	rot := &countingRotator{Salter: identity.NewSalter(st, now)}
	r := New(st, cfg, reg, rot, slog.Default(), now)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()

	deadline := time.After(5 * time.Second)
	for st.vacuums.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("boot catch-up pass never ran")
		case <-time.After(time.Millisecond):
		}
	}
	// Boot must also ensure a salt exists.
	salt, err := rot.Current(context.Background())
	if err != nil || salt == "" {
		t.Fatalf("salt after boot = %q, err %v", salt, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// --- app class, cohorts and identities ---

var appProjectSpecs = []manage.ProjectSpec{
	{Name: "App", AllowedOrigins: []string{"https://a.com"}},
}

func setupApp(t *testing.T, specs []manage.ProjectSpec) (store.Store, *Runner, *sql.DB) {
	t.Helper()
	cfg := configtest.Load(t, jobsVars)
	st, path := openStoreAt(t)
	reg := newRegistry(t, st, cfg, specs)
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	return st, New(st, cfg, reg, identity.NewSalter(st, now), slog.Default(), now), raw
}

func seedAppDay(t *testing.T, st store.Store, actors ...string) {
	t.Helper()
	var views []store.Event
	for i, a := range actors {
		ts := mustTime("2026-08-10T10:00:00Z")
		views = append(views, store.Event{Family: store.FamilyViews,
			ID: "v" + a + string(rune('a'+i)), ProjectID: 1,
			TS: ts, ReceivedAt: ts,
			Kind: "app", ActorID: a, ActorKind: store.ActorUser,
			UserID: "u-" + a, GroupID: "org9",
			Path: "/home", OS: "iOS", AppVersion: "2.4.1",
		})
	}
	if err := st.WriteEvents(context.Background(), views); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestRunDailyPassAggregatesAppDays(t *testing.T) {
	st, r, db := setupApp(t, appProjectSpecs)
	ctx := context.Background()
	seedAppDay(t, st, "a", "b")

	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}

	if n := count(t, db, `SELECT COUNT(*) FROM agg_views_daily WHERE project_id=1 AND kind='app'`); n != 1 {
		t.Errorf("agg_views_daily rows = %d, want 1", n)
	}
	if n := count(t, db, `SELECT visitors FROM agg_views_daily WHERE kind='app'`); n != 2 {
		t.Errorf("visitors = %d, want 2", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM raw_views WHERE project_id=1`); n != 0 {
		t.Errorf("raw views left = %d, want 0", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM actors`); n != 2 {
		t.Errorf("actors = %d, want 2", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_retention WHERE day_offset=0`); n != 1 {
		t.Errorf("cohort rows = %d, want 1", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_identity_daily WHERE kind='group'`); n != 1 {
		t.Errorf("group aggregate rows = %d, want 1", n)
	}
	if n := count(t, db, `SELECT users FROM agg_identity_daily WHERE kind='group'`); n != 2 {
		t.Errorf("users in group = %d, want 2", n)
	}
}

// A project whose clients send no ids has connection-hash actors only.
// UpsertActors keeps user and install kinds, so it gets no actors and no
// cohorts, while rollups and identity aggregates still run.
func TestRunDailyPassBuildsNoActorsWithoutIds(t *testing.T) {
	st, r, db := setupApp(t, appProjectSpecs)
	ctx := context.Background()
	ts := mustTime("2026-08-10T10:00:00Z")
	if err := st.WriteEvents(ctx, []store.Event{{Family: store.FamilyViews,
		ID: "vconn", ProjectID: 1, TS: ts, ReceivedAt: ts,
		Kind: "app", ActorID: "hash1", ActorKind: store.ActorConnection,
		GroupID: "org9", Path: "/home", OS: "iOS", AppVersion: "2.4.1",
	}}); err != nil {
		t.Fatal(err)
	}

	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}

	if n := count(t, db, `SELECT COUNT(*) FROM actors`); n != 0 {
		t.Errorf("actors = %d; a connection-hash actor is never cohorted", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_retention`); n != 0 {
		t.Errorf("agg_retention rows = %d, want 0", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_views_daily WHERE kind='app'`); n != 1 {
		t.Errorf("agg_views_daily rows = %d, want 1", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_identity_daily WHERE kind='group'`); n != 1 {
		t.Errorf("group aggregate rows = %d, want 1", n)
	}
}

func TestRunDailyPassIsIdempotentAcrossAppSteps(t *testing.T) {
	st, r, db := setupApp(t, appProjectSpecs)
	ctx := context.Background()
	seedAppDay(t, st, "a", "b")

	for i := 0; i < 2; i++ {
		if err := r.RunDailyPass(ctx); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	if n := count(t, db, `SELECT views FROM agg_views_daily WHERE kind='app'`); n != 2 {
		t.Errorf("views = %d after two passes, want 2", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_retention`); n != 1 {
		t.Errorf("agg_retention rows = %d after two passes, want 1", n)
	}
}

func TestRunDailyPassPrunesActorsAndIdentities(t *testing.T) {
	st, r, db := setupApp(t, appProjectSpecs)
	ctx := context.Background()

	if err := st.UpsertIdentities(ctx, []store.Identity{
		{ProjectID: 1, Kind: store.KindUser, ID: "old", Name: "Gone"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE identities SET last_seen_day='2020-01-01'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO actors VALUES (1,'stale','install','2020-01-01','2020-01-01')`); err != nil {
		t.Fatal(err)
	}

	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM actors`); n != 0 {
		t.Errorf("stale actors left = %d, want 0", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM identities`); n != 0 {
		t.Errorf("stale identities left = %d, want 0", n)
	}
}

// A web-only project whose views carry a $user_id must still get cohorts and identity rollups:
// they used to be driven off app rows alone, which meant a project with no
// app never got either.
func TestRunDailyPassCoversWebOnlyProjectsForCohorts(t *testing.T) {
	st, r, db := setupApp(t, appProjectSpecs)
	ctx := context.Background()
	ts := mustTime("2026-08-10T10:00:00Z")

	if err := st.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyViews, ID: "w1", ProjectID: 1, TS: ts, ReceivedAt: ts, Kind: "web", ActorID: "a", ActorKind: store.ActorUser,
			UserID: "u1", GroupID: "org9", Path: "/"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM actors`); n != 1 {
		t.Errorf("actors = %d for a web-only project, want 1", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_retention`); n != 1 {
		t.Errorf("agg_retention rows = %d, want 1", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_identity_daily WHERE kind='user'`); n != 1 {
		t.Errorf("user aggregate rows = %d, want 1", n)
	}
}

// Cohorts must not lag the raw-retention window. They read raw rows without
// deleting them, so they cover days still inside the window too — otherwise
// the retention page would be a whole window stale.
func TestRunDailyPassComputesCohortsForRecentDays(t *testing.T) {
	st, r, db := setupApp(t, appProjectSpecs)
	ctx := context.Background()
	// Two days before the fake now of 2026-08-22, well inside the 7-day
	// app raw window used by jobsVars.
	recent := mustTime("2026-08-20T10:00:00Z")

	if err := st.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyViews, ID: "r1", ProjectID: 1, TS: recent, ReceivedAt: recent, Kind: "app", ActorID: "a", ActorKind: store.ActorUser,
			UserID: "u1", Path: "/home", OS: "iOS"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_retention WHERE cohort_day='2026-08-20'`); n != 1 {
		t.Errorf("cohort rows for an in-window day = %d, want 1", n)
	}
	// The raw row itself must survive: it is inside the retention window.
	if n := count(t, db, `SELECT COUNT(*) FROM raw_views WHERE project_id=1`); n != 1 {
		t.Errorf("raw views = %d; an in-window day must not be aggregated away", n)
	}
}

// The raw window must apply to every kind at once: a single ViewDaysBefore
// window drives one AggregateViewDay call per day, which rolls up whatever
// mix of kinds landed on that day.
func TestDailyPassRollsUpEveryKindPastTheWindow(t *testing.T) {
	st, _, r := setup(t, jobsVars, jobsProjectSpecs)
	ctx := context.Background()
	old := mustTime("2026-08-10T10:00:00Z") // 12 days before the fixed clock; window is 7
	if err := st.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyViews, ID: "w", ProjectID: 1, TS: old, ReceivedAt: old, Kind: "web", ActorID: "h", ActorKind: store.ActorConnection, Path: "/"},
		{Family: store.FamilyViews, ID: "a", ProjectID: 1, TS: old, ReceivedAt: old, Kind: "app", ActorID: "i", ActorKind: store.ActorInstall, Path: "/home"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+os.Getenv("JOBS_TEST_DB")) // the pattern the file's other tests use
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var kinds int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agg_views_daily WHERE project_id=1 AND day='2026-08-10'`).Scan(&kinds); err != nil {
		t.Fatal(err)
	}
	if kinds != 2 {
		t.Errorf("agg_views_daily rows = %d, want one per kind", kinds)
	}
	var raw int
	if err := db.QueryRow(`SELECT COUNT(*) FROM raw_views WHERE project_id=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != 0 {
		t.Errorf("raw views left = %d", raw)
	}
}

// Today is still arriving, so rolling it up at 03:00 would freeze a partial
// day into agg_identity_daily, which v_identity_daily prefers over raw. It
// is left to the view's live half; yesterday, complete by now, is rolled up.
func TestRunDailyPassLeavesTodaysIdentityActivityLive(t *testing.T) {
	st, r, db := setupApp(t, appProjectSpecs)
	ctx := context.Background()
	for i, ts := range []string{"2026-08-21T10:00:00Z", "2026-08-22T01:00:00Z"} {
		at := mustTime(ts)
		if err := st.WriteEvents(ctx, []store.Event{
			{Family: store.FamilyViews, ID: "t" + string(rune('a'+i)), ProjectID: 1, TS: at, ReceivedAt: at,
				Kind: "app", ActorID: "a", ActorKind: store.ActorUser, UserID: "u1",
				Path: "/home", OS: "iOS"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_identity_daily WHERE day='2026-08-22'`); n != 0 {
		t.Errorf("today's identity rows aggregated = %d, want 0", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_identity_daily WHERE day='2026-08-21'`); n != 1 {
		t.Errorf("yesterday's identity rows aggregated = %d, want 1", n)
	}
	// Retention still covers today: it has no live half to fall back on.
	if n := count(t, db, `SELECT COUNT(*) FROM agg_retention WHERE cohort_day='2026-08-21' AND day_offset=1`); n != 1 {
		t.Errorf("retention rows owned by today = %d, want 1", n)
	}
}
