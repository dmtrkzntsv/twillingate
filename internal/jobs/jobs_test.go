package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
	_ "modernc.org/sqlite"
)

var jobsProjectSpecs = []manage.ProjectSpec{
	{Name: "App", AllowedOrigins: []string{"https://a.com"}},
}

// jobsVars is the environment the tests start from: the defaults.
var jobsVars = map[string]string{}

// countingStore counts daily passes; IncrementalVacuum runs exactly once per
// pass, which makes it a reliable proxy. It also counts and records the
// days argument PurgeArchived was called with, for the tests that check
// the pass calls it (or skips it) as configured.
type countingStore struct {
	store.Store
	vacuums   atomic.Int64
	purges    atomic.Int64
	purgeDays atomic.Int64

	// purgeOverride, when set, replaces the delegate call entirely: lets a
	// test force PurgeArchived to return a partial result alongside an
	// error (as it does when one of several items failed), without needing
	// a genuine failure condition in the underlying database.
	purgeOverride func(ctx context.Context, days int) (store.PurgeResult, error)
}

func (c *countingStore) IncrementalVacuum(ctx context.Context) error {
	c.vacuums.Add(1)
	return c.Store.IncrementalVacuum(ctx)
}

func (c *countingStore) PurgeArchived(ctx context.Context, days int) (store.PurgeResult, error) {
	c.purges.Add(1)
	c.purgeDays.Store(int64(days))
	if c.purgeOverride != nil {
		return c.purgeOverride(ctx, days)
	}
	return c.Store.PurgeArchived(ctx, days)
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
	return storetest.Open(t, path), path
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

// TestPassWithoutArchiveRecordsTheDatabaseOnly: the pass ends by
// measuring the database at its own clock. With no archive (a process
// that keeps no event files, such as a console-only one) it records the
// database's size alone and leaves the file bytes the ingest process
// wrote.
func TestPassWithoutArchiveRecordsTheDatabaseOnly(t *testing.T) {
	_, _, r := setup(t, jobsVars, jobsProjectSpecs)
	r.RunDailyPass(context.Background())
	got := queryDays(t, `SELECT key || ' ' || project_id || ' ' || measured_at FROM usage_history ORDER BY key`)
	if want := []string{"database_bytes 0 2026-08-22"}; !slices.Equal(got, want) {
		t.Errorf("usage_history = %v, want %v", got, want)
	}
}

// TestPassRecordsTheArchiveSizes: with an archive the pass records each
// project's file bytes as the archive measures them once its maintenance
// is done; a failed measurement is logged and the database's size is
// still recorded.
func TestPassRecordsTheArchiveSizes(t *testing.T) {
	_, _, r := setup(t, jobsVars, jobsProjectSpecs)
	a := &fakeArchive{sizes: map[int64]int64{1: 4096, 2: 10}}
	r.WithArchive(a)
	r.RunDailyPass(context.Background())
	if !slices.Equal(a.calls, []string{"2026-08-22 late=1 keep=400", "sizes"}) {
		t.Errorf("archive calls = %v, want the maintenance, then the sizes", a.calls)
	}
	got := queryDays(t, `SELECT key || ' ' || project_id || ' ' || measured_at || ' ' || value FROM usage_history WHERE key = 'file_bytes' ORDER BY project_id`)
	if want := []string{"file_bytes 1 2026-08-22 4096", "file_bytes 2 2026-08-22 10"}; !slices.Equal(got, want) {
		t.Errorf("file bytes = %v, want %v", got, want)
	}

	var buf bytes.Buffer
	r.logger = slog.New(slog.NewTextHandler(&buf, nil))
	a.sizesErr = errors.New("boom")
	r.now = func() time.Time { return time.Date(2026, 8, 23, 4, 0, 0, 0, time.UTC) }
	r.RunDailyPass(context.Background())
	if !strings.Contains(buf.String(), "archive sizes") || !strings.Contains(buf.String(), "boom") {
		t.Errorf("log = %q, want the failed measurement", buf.String())
	}
	got = queryDays(t, `SELECT key || ' ' || project_id FROM usage_history WHERE measured_at = '2026-08-23' ORDER BY key`)
	if want := []string{"database_bytes 0"}; !slices.Equal(got, want) {
		t.Errorf("after a failed measurement = %v, want the database's size alone", got)
	}
}

// The pass purges archived projects, dashboards and widgets past
// RETENTION_ARCHIVED_DAYS before doing anything else, calling
// PurgeArchived with cfg.Retention.ArchivedDays, and reloads the registry
// once a project was purged: the registry must not keep handing out a
// project id that no longer exists in the store.
func TestRunDailyPassPurgesArchivedAndReloadsRegistryOnProjectPurge(t *testing.T) {
	vars := map[string]string{}
	for k, v := range jobsVars {
		vars[k] = v
	}
	vars["RETENTION_ARCHIVED_DAYS"] = "30"
	cfg := configtest.Load(t, vars)
	raw, path := openStoreAt(t)
	t.Setenv("JOBS_TEST_DB", path)
	st := &countingStore{Store: raw}
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	ctx := context.Background()
	ops := manage.NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "test", manage.ProjectSpec{Name: "Gone"}); err != nil {
		t.Fatal(err)
	}
	if err := ops.ArchiveProject(ctx, "test", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := rawExec(t, `UPDATE projects SET archived_at = datetime('now', '-31 days') WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	r := New(st, cfg, reg, identity.NewSalter(st, now), slog.Default(), now)

	r.RunDailyPass(ctx)
	if got := st.purges.Load(); got != 1 {
		t.Errorf("PurgeArchived calls = %d, want 1", got)
	}
	if got := st.purgeDays.Load(); got != 30 {
		t.Errorf("PurgeArchived days = %d, want cfg.Retention.ArchivedDays (30)", got)
	}
	if got := queryDays(t, `SELECT id FROM projects WHERE id=2`); len(got) != 0 {
		t.Fatalf("archived project not purged: %v", got)
	}
	if p := reg.Snapshot(ctx).Project(2); p != nil {
		t.Error("registry not reloaded after a project purge: project 2 still present")
	}
}

// A purged project's archive directory goes with it.
func TestRunDailyPassRemovesPurgedProjectsFromTheArchive(t *testing.T) {
	vars := map[string]string{}
	for k, v := range jobsVars {
		vars[k] = v
	}
	vars["RETENTION_ARCHIVED_DAYS"] = "30"
	cfg := configtest.Load(t, vars)
	raw, path := openStoreAt(t)
	t.Setenv("JOBS_TEST_DB", path)
	reg := newRegistry(t, raw, cfg, jobsProjectSpecs)
	ctx := context.Background()
	ops := manage.NewOps(reg, raw)
	if _, err := ops.CreateProject(ctx, "test", manage.ProjectSpec{Name: "Gone"}); err != nil {
		t.Fatal(err)
	}
	if err := ops.ArchiveProject(ctx, "test", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := rawExec(t, `UPDATE projects SET archived_at = datetime('now', '-31 days') WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	a := &fakeArchive{}
	r := New(raw, cfg, reg, identity.NewSalter(raw, now), slog.Default(), now).WithArchive(a)
	r.RunDailyPass(ctx)
	if !slices.Equal(a.removed, []int64{2}) {
		t.Fatalf("RemoveProject calls = %v, want [2]", a.removed)
	}
}

// RETENTION_ARCHIVED_DAYS=0 must keep archived items forever: the pass
// must not even call PurgeArchived.
func TestRunDailyPassSkipsPurgeWhenArchivedDaysIsZero(t *testing.T) {
	vars := map[string]string{}
	for k, v := range jobsVars {
		vars[k] = v
	}
	vars["RETENTION_ARCHIVED_DAYS"] = "0"
	cfg := configtest.Load(t, vars)
	raw, path := openStoreAt(t)
	t.Setenv("JOBS_TEST_DB", path)
	st := &countingStore{Store: raw}
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	r := New(st, cfg, reg, identity.NewSalter(st, now), slog.Default(), now)

	r.RunDailyPass(context.Background())
	if got := st.purges.Load(); got != 0 {
		t.Errorf("PurgeArchived calls = %d, want 0 when RETENTION_ARCHIVED_DAYS=0", got)
	}
}

// seedShare inserts a widget share of project 1 with the given dates (nil
// leaves the column NULL); the image bytes are placeholders, since the
// daily pass never reads them.
func seedShare(t *testing.T, id string, archiveAt, archivedAt any) {
	t.Helper()
	if _, err := rawExec(t, `INSERT INTO widget_shares
		(id, project_id, range_from, range_to, title, project_name, image, image_2x, archive_at, archived_at)
		VALUES (?, 1, '2026-08-01', '2026-08-21', 'Visitors', 'App', x'00', x'00', ?, ?)`,
		id, archiveAt, archivedAt); err != nil {
		t.Fatal(err)
	}
}

// The pass archives every share whose archive_at has come, whatever
// RETENTION_ARCHIVED_DAYS is, and leaves dated-later and
// project-lifetime shares alone. archived_at takes the share's own date,
// not the pass's clock.
func TestRunDailyPassArchivesDueWidgetShares(t *testing.T) {
	// The purge ages archived rows by the database's clock, which is years
	// past the pass's fixed one; keep it from taking the share just archived.
	vars := map[string]string{}
	for k, v := range jobsVars {
		vars[k] = v
	}
	vars["RETENTION_ARCHIVED_DAYS"] = "36500"
	_, _, r := setup(t, vars, jobsProjectSpecs)
	seedShare(t, "due", "2026-08-21T00:00:00Z", nil)
	seedShare(t, "future", "2026-09-01T00:00:00Z", nil)
	seedShare(t, "project", nil, nil)

	r.RunDailyPass(context.Background())
	got := queryDays(t, `SELECT id || ' ' || COALESCE(archived_at, '-') FROM widget_shares ORDER BY id`)
	want := []string{"due 2026-08-21T00:00:00Z", "future -", "project -"}
	if !slices.Equal(got, want) {
		t.Errorf("shares after the pass = %v, want %v", got, want)
	}
}

// Archived shares past RETENTION_ARCHIVED_DAYS go with the rest of the
// archive; younger ones stay.
func TestRunDailyPassPurgesOldArchivedWidgetShares(t *testing.T) {
	vars := map[string]string{}
	for k, v := range jobsVars {
		vars[k] = v
	}
	vars["RETENTION_ARCHIVED_DAYS"] = "30"
	_, _, r := setup(t, vars, jobsProjectSpecs)
	seedShare(t, "old", "2026-07-01T00:00:00Z", "2026-07-01T00:00:00Z")
	// The purge measures age against the database's clock, not the pass's.
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02T15:04:05Z")
	seedShare(t, "recent", yesterday, yesterday)

	r.RunDailyPass(context.Background())
	if got := queryDays(t, `SELECT id FROM widget_shares ORDER BY id`); !slices.Equal(got, []string{"recent"}) {
		t.Errorf("shares after the pass = %v, want only recent", got)
	}
}

// RETENTION_ARCHIVED_DAYS=0 keeps archived shares forever, but archiving
// due ones does not depend on the purge.
func TestRunDailyPassKeepsArchivedWidgetSharesWhenArchivedDaysIsZero(t *testing.T) {
	vars := map[string]string{}
	for k, v := range jobsVars {
		vars[k] = v
	}
	vars["RETENTION_ARCHIVED_DAYS"] = "0"
	_, _, r := setup(t, vars, jobsProjectSpecs)
	seedShare(t, "old", "2026-07-01T00:00:00Z", "2026-07-01T00:00:00Z")
	seedShare(t, "due", "2026-08-21T00:00:00Z", nil)

	r.RunDailyPass(context.Background())
	got := queryDays(t, `SELECT id || ' ' || COALESCE(archived_at, '-') FROM widget_shares ORDER BY id`)
	want := []string{"due 2026-08-21T00:00:00Z", "old 2026-07-01T00:00:00Z"}
	if !slices.Equal(got, want) {
		t.Errorf("shares after the pass = %v, want %v", got, want)
	}
}

// PurgeArchived can return a partial result alongside an error (one item
// among several failed, but it kept going for the rest — see
// internal/store/sqlite/purge.go). The pass must still reload the registry
// whenever purged.Projects is non-empty, whether or not err is nil: a
// project already deleted must not be left in the registry just because a
// sibling item's purge failed.
func TestRunDailyPassReloadsRegistryDespiteAPartialPurgeError(t *testing.T) {
	cfg := configtest.Load(t, jobsVars) // ArchivedDays defaults to 30 (>0): purge still runs, just overridden below.
	raw, path := openStoreAt(t)
	t.Setenv("JOBS_TEST_DB", path)
	st := &countingStore{Store: raw}
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	ctx := context.Background()

	// Inserted directly (bypassing the registry-aware Ops path) after the
	// registry's initial load: only a reload picks it up, so it is the
	// probe for whether RunDailyPass reloaded despite the error below.
	if _, err := rawExec(t,
		`INSERT INTO projects (id, name, allowed_origins) VALUES (7, 'Late', '[]')`); err != nil {
		t.Fatal(err)
	}
	if p := reg.Snapshot(ctx).Project(7); p != nil {
		t.Fatal("test setup: project 7 must not be visible before any reload")
	}

	st.purgeOverride = func(context.Context, int) (store.PurgeResult, error) {
		return store.PurgeResult{Projects: []int64{7}}, errors.New("dashboard 9: boom")
	}
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	r := New(st, cfg, reg, identity.NewSalter(st, now), slog.Default(), now)

	r.RunDailyPass(ctx)
	if p := reg.Snapshot(ctx).Project(7); p == nil {
		t.Error("registry not reloaded after a purge error carrying a non-empty partial result")
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

// orderStore records the order of the calls the expiry test cares about and
// the time ExpireDrafts was given.
type orderStore struct {
	store.Store
	mu       sync.Mutex
	calls    []string
	expireAt time.Time
}

func (o *orderStore) ExpireDrafts(ctx context.Context, now time.Time) (int, error) {
	o.mu.Lock()
	o.calls = append(o.calls, "expire")
	o.expireAt = now
	o.mu.Unlock()
	return o.Store.ExpireDrafts(ctx, now)
}

func (o *orderStore) PurgeArchived(ctx context.Context, days int) (store.PurgeResult, error) {
	o.mu.Lock()
	o.calls = append(o.calls, "purge")
	o.mu.Unlock()
	return o.Store.PurgeArchived(ctx, days)
}

// Draft forms past their date are archived on every pass, before the purge
// that could then take them, and whether or not RETENTION_ARCHIVED_DAYS
// enables the purge.
func TestRunDailyPassExpiresDraftsBeforePurge(t *testing.T) {
	for _, days := range []string{"30", "0"} {
		vars := map[string]string{}
		for k, v := range jobsVars {
			vars[k] = v
		}
		vars["RETENTION_ARCHIVED_DAYS"] = days
		cfg := configtest.Load(t, vars)
		raw, path := openStoreAt(t)
		t.Setenv("JOBS_TEST_DB", path)
		st := &orderStore{Store: raw}
		reg := newRegistry(t, st, cfg, jobsProjectSpecs)
		now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
		r := New(st, cfg, reg, identity.NewSalter(st, now), slog.Default(), now)

		r.RunDailyPass(context.Background())
		want := []string{"expire", "purge"}
		if days == "0" {
			want = []string{"expire"}
		}
		if !slices.Equal(st.calls, want) {
			t.Errorf("RETENTION_ARCHIVED_DAYS=%s: calls = %v, want %v", days, st.calls, want)
		}
		if !st.expireAt.Equal(now()) {
			t.Errorf("RETENTION_ARCHIVED_DAYS=%s: ExpireDrafts now = %v, want %v", days, st.expireAt, now())
		}
	}
}

type fakeArchive struct {
	calls    []string
	removed  []int64
	dirs     []int64 // the project directories Projects reports
	sizes    map[int64]int64
	sizesErr error
}

func (f *fakeArchive) Projects() ([]int64, error) { return f.dirs, nil }

func (f *fakeArchive) Maintain(ctx context.Context, today civil.Date, lateDays, retentionDays int) error {
	f.calls = append(f.calls, fmt.Sprintf("%s late=%d keep=%d", today, lateDays, retentionDays))
	return nil
}

func (f *fakeArchive) Sizes() (map[int64]int64, error) {
	f.calls = append(f.calls, "sizes")
	return f.sizes, f.sizesErr
}

func (f *fakeArchive) RemoveProject(id int64) error {
	f.removed = append(f.removed, id)
	return nil
}

func TestDailyPassMaintainsTheArchive(t *testing.T) {
	vars := map[string]string{"INGEST_LATE_DAYS": "2", "RETENTION_DAYS": "500"}
	_, _, r := setup(t, vars, jobsProjectSpecs)
	a := &fakeArchive{}
	r.WithArchive(a)
	r.RunDailyPass(context.Background())
	if len(a.calls) != 2 || a.calls[0] != "2026-08-22 late=2 keep=500" {
		t.Fatalf("archive calls = %v", a.calls)
	}
}

func TestScheduledPassWaitsItsJitterBeforeTheArchive(t *testing.T) {
	_, _, r := setup(t, jobsVars, jobsProjectSpecs)
	a := &fakeArchive{}
	r.WithArchive(a)
	var waited time.Duration
	r.jitter = func() time.Duration { return 7 * time.Minute }
	r.sleep = func(ctx context.Context, d time.Duration) { waited = d }
	r.now = func() time.Time { return time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC) }
	r.runScheduled(context.Background())
	if waited != 7*time.Minute || len(a.calls) != 2 {
		t.Fatalf("waited %v, archive calls %d; want 7m, then the maintenance and the sizes", waited, len(a.calls))
	}
}

// The boot catch-up pass never waits: the jitter is for the 03:00 pass.
func TestBootPassDoesNotWaitTheJitter(t *testing.T) {
	_, _, r := setup(t, jobsVars, jobsProjectSpecs)
	r.WithArchive(&fakeArchive{})
	waited := false
	r.sleep = func(ctx context.Context, d time.Duration) { waited = true }
	r.RunDailyPass(context.Background())
	if waited {
		t.Fatal("RunDailyPass slept; only the scheduled pass is jittered")
	}
}

// A shutdown during the jitter wait ends the scheduled pass before it starts:
// nothing runs on a dead context, and the day stays open for the next boot.
func TestScheduledPassCutShortByShutdownDoesNotRun(t *testing.T) {
	st, _, r := setup(t, jobsVars, jobsProjectSpecs)
	a := &fakeArchive{}
	r.WithArchive(a)
	ctx, cancel := context.WithCancel(context.Background())
	r.jitter = func() time.Duration { return time.Minute }
	r.sleep = func(context.Context, time.Duration) { cancel() }
	r.now = func() time.Time { return time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC) }
	r.runScheduled(ctx)
	if len(a.calls) != 0 {
		t.Fatalf("Maintain ran after the shutdown: %v", a.calls)
	}
	if got := queryDays(t, `SELECT key FROM usage_history`); len(got) != 0 {
		t.Fatalf("the pass ran after the shutdown: usage_history %v", got)
	}
	if r.lastPassDay != "" {
		t.Fatalf("lastPassDay = %q, want it left open", r.lastPassDay)
	}
	_ = st
}

// A directory under raw/ whose project the registry does not know (purged by
// another process) is removed; live and archived-but-not-purged projects keep
// theirs.
func TestRunDailyPassRemovesDirectoriesOfUnknownProjects(t *testing.T) {
	cfg := configtest.Load(t, jobsVars)
	st, path := openStoreAt(t)
	t.Setenv("JOBS_TEST_DB", path)
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	ctx := context.Background()
	ops := manage.NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "test", manage.ProjectSpec{Name: "Archived"}); err != nil {
		t.Fatal(err)
	}
	if err := ops.ArchiveProject(ctx, "test", 2); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	a := &fakeArchive{dirs: []int64{1, 2, 99}}
	r := New(st, cfg, reg, identity.NewSalter(st, now), slog.Default(), now).WithArchive(a)
	r.RunDailyPass(ctx)
	if !slices.Equal(a.removed, []int64{99}) {
		t.Fatalf("RemoveProject calls = %v, want [99]", a.removed)
	}
}

// A project created by another process after this registry loaded is not
// unknown: the sweep reloads before it compares.
func TestRunDailyPassKeepsDirectoriesOfProjectsCreatedElsewhere(t *testing.T) {
	cfg := configtest.Load(t, jobsVars)
	st, path := openStoreAt(t)
	t.Setenv("JOBS_TEST_DB", path)
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	ctx := context.Background()
	other := manage.NewOps(manage.New(st, slog.Default()), st) // another process's registry
	if err := other.Reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := other.CreateProject(ctx, "test", manage.ProjectSpec{Name: "Elsewhere"}); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	a := &fakeArchive{dirs: []int64{2}}
	r := New(st, cfg, reg, identity.NewSalter(st, now), slog.Default(), now).WithArchive(a)
	r.RunDailyPass(ctx)
	if len(a.removed) != 0 {
		t.Fatalf("RemoveProject calls = %v, want none", a.removed)
	}
}

// passOnlyStore has jobs.Store's methods and nothing else, so the pass
// cannot reach any of the event work the store used to do; it records the
// calls made.
type passOnlyStore struct{ calls []string }

var _ Store = (*passOnlyStore)(nil)

func (s *passOnlyStore) call(name string) { s.calls = append(s.calls, name) }

func (s *passOnlyStore) IncrementalVacuum(context.Context) error {
	s.call("IncrementalVacuum")
	return nil
}

func (s *passOnlyStore) RecordUsageHistory(_ context.Context, _ time.Time, fileBytes map[int64]int64) error {
	if fileBytes == nil {
		s.call("RecordUsageHistory nil")
	} else {
		s.call(fmt.Sprintf("RecordUsageHistory %v", fileBytes))
	}
	return nil
}

func (s *passOnlyStore) PurgeArchived(context.Context, int) (store.PurgeResult, error) {
	s.call("PurgeArchived")
	return store.PurgeResult{}, nil
}

func (s *passOnlyStore) ArchiveDueWidgetShares(context.Context, string) (int, error) {
	s.call("ArchiveDueWidgetShares")
	return 0, nil
}

func (s *passOnlyStore) ExpireDrafts(context.Context, time.Time) (int, error) {
	s.call("ExpireDrafts")
	return 0, nil
}

func (s *passOnlyStore) PendingSubmissions(context.Context, time.Time, int) ([]store.PendingSubmission, error) {
	return nil, nil
}

func (s *passOnlyStore) AttributeSubmission(context.Context, store.PendingSubmission, store.Event) (bool, error) {
	return false, nil
}

func (s *passOnlyStore) UnloggedEvents(context.Context, int) ([]store.UnloggedEvent, error) {
	return nil, nil
}

func (s *passOnlyStore) UnloggedCount(context.Context) (int, error) { return 0, nil }

func (s *passOnlyStore) MarkLogged(context.Context, []store.UnloggedEvent) error { return nil }

// TestPassRunsNoEventWork: the pass needs nothing of the store but the
// shares, drafts, purge, vacuum and usage history, and it maintains the
// archive before measuring.
func TestPassRunsNoEventWork(t *testing.T) {
	_, reg, r := setup(t, jobsVars, jobsProjectSpecs)
	st := &passOnlyStore{}
	r = New(st, r.cfg, reg, nil, slog.Default(), r.now)
	a := &fakeArchive{sizes: map[int64]int64{1: 7}}
	r.WithArchive(a)
	r.RunDailyPass(context.Background())
	want := []string{"ArchiveDueWidgetShares", "ExpireDrafts", "PurgeArchived", "IncrementalVacuum", "RecordUsageHistory map[1:7]"}
	if !slices.Equal(st.calls, want) {
		t.Errorf("store calls = %v, want %v", st.calls, want)
	}
	if want := []string{"2026-08-22 late=1 keep=400", "sizes"}; !slices.Equal(a.calls, want) {
		t.Errorf("archive calls = %v, want %v", a.calls, want)
	}
}
