package sqlite

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// createPurgeableProject creates a live project and returns its id.
func createPurgeableProject(t *testing.T, db *DB, name string) int64 {
	t.Helper()
	id, err := db.CreateProject(context.Background(),
		store.RegistryProject{Name: name, AllowedOrigins: "[]"},
		store.AuditEntry{Actor: "test", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// archiveProjectDaysAgo sets a project's archived_at using the same
// format SetProjectArchived writes (datetime('now'), "YYYY-MM-DD
// HH:MM:SS"), n days in the past.
func archiveProjectDaysAgo(t *testing.T, db *DB, id int64, n int) {
	t.Helper()
	if _, err := db.ExecForTest(
		`UPDATE projects SET archived_at = datetime('now', ?) WHERE id=?`,
		daysAgoModifier(n), id); err != nil {
		t.Fatal(err)
	}
}

// createPurgeableDashboard creates a live dashboard (with any widgets)
// and returns its id.
func createPurgeableDashboard(t *testing.T, db *DB, owner, sortKey string, ws []store.Widget) int64 {
	t.Helper()
	id, err := db.InsertDashboard(context.Background(),
		store.Dashboard{Owner: owner, Title: "D " + sortKey, SortKey: sortKey}, ws,
		store.AuditEntry{Actor: "agent", Action: "dashboard.create"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// archiveDashboardDaysAgo sets a dashboard's archived_at using the RFC3339
// format the reporting writers use, n days in the past. ExecForTest
// bypasses SetDashboardArchived, which is how a system dashboard's
// archived_at gets forced for the system-row test: production code never
// archives one.
func archiveDashboardDaysAgo(t *testing.T, db *DB, id int64, n int) {
	t.Helper()
	if _, err := db.ExecForTest(
		`UPDATE dashboards SET archived_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now', ?) WHERE id=?`,
		daysAgoModifier(n), id); err != nil {
		t.Fatal(err)
	}
}

func archiveWidgetDaysAgo(t *testing.T, db *DB, id int64, n int) {
	t.Helper()
	if _, err := db.ExecForTest(
		`UPDATE widgets SET archived_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now', ?) WHERE id=?`,
		daysAgoModifier(n), id); err != nil {
		t.Fatal(err)
	}
}

func daysAgoModifier(n int) string {
	return "-" + strconv.Itoa(n) + " days"
}

func auditRows(t *testing.T, db *DB, action string) []struct{ Actor, Subject string } {
	t.Helper()
	rows, err := db.db.QueryContext(context.Background(),
		`SELECT actor, subject FROM audit_log WHERE action=?`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []struct{ Actor, Subject string }
	for rows.Next() {
		var a, s string
		if err := rows.Scan(&a, &s); err != nil {
			t.Fatal(err)
		}
		out = append(out, struct{ Actor, Subject string }{a, s})
	}
	return out
}

func contains(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestPurgeArchivedAgesOutWidgetDashboardAndProject checks the 30-day
// boundary independently for each kind: 31 days ago is purged, 29 days
// ago survives.
func TestPurgeArchivedAgesOutWidgetDashboardAndProject(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	oldProject := createPurgeableProject(t, db, "old")
	archiveProjectDaysAgo(t, db, oldProject, 31)
	recentProject := createPurgeableProject(t, db, "recent")
	archiveProjectDaysAgo(t, db, recentProject, 29)

	oldDash := createPurgeableDashboard(t, db, store.OwnerUser, "a", []store.Widget{
		{SortKey: "a", Width: 1, Height: 1, Name: "w1", SourceType: "events", Source: "x"},
	})
	archiveDashboardDaysAgo(t, db, oldDash, 31)
	recentDash := createPurgeableDashboard(t, db, store.OwnerUser, "b", nil)
	archiveDashboardDaysAgo(t, db, recentDash, 29)

	// A widget on its own (its dashboard stays live) also ages out on its
	// own timer.
	liveDash := createPurgeableDashboard(t, db, store.OwnerUser, "c", []store.Widget{
		{SortKey: "a", Width: 1, Height: 1, Name: "old-widget", SourceType: "events", Source: "x"},
		{SortKey: "b", Width: 1, Height: 1, Name: "recent-widget", SourceType: "events", Source: "y"},
	})
	widgets, err := db.ListWidgets(ctx, liveDash)
	if err != nil {
		t.Fatal(err)
	}
	var oldWidget, recentWidget int64
	for _, w := range widgets {
		if w.Name == "old-widget" {
			oldWidget = w.ID
		} else {
			recentWidget = w.ID
		}
	}
	archiveWidgetDaysAgo(t, db, oldWidget, 31)
	archiveWidgetDaysAgo(t, db, recentWidget, 29)

	res, err := db.PurgeArchived(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}

	if !contains(res.Projects, oldProject) || contains(res.Projects, recentProject) {
		t.Errorf("Projects purged = %v, want %d only", res.Projects, oldProject)
	}
	if !contains(res.Dashboards, oldDash) || contains(res.Dashboards, recentDash) {
		t.Errorf("Dashboards purged = %v, want %d only", res.Dashboards, oldDash)
	}
	if !contains(res.Widgets, oldWidget) || contains(res.Widgets, recentWidget) {
		t.Errorf("Widgets purged = %v, want %d only", res.Widgets, oldWidget)
	}

	if _, err := db.GetDashboard(ctx, oldDash); err == nil {
		t.Error("old dashboard still exists")
	}
	if _, err := db.GetDashboard(ctx, recentDash); err != nil {
		t.Errorf("recent dashboard gone: %v", err)
	}
	// Cascade: the old dashboard's widget goes with it.
	if ws, err := db.ListWidgets(ctx, oldDash); err != nil || len(ws) != 0 {
		t.Errorf("old dashboard's widgets = %v, %v, want none (dashboard gone)", ws, err)
	}

	if _, err := db.GetWidget(ctx, oldWidget); err == nil {
		t.Error("old standalone widget still exists")
	}
	if _, err := db.GetWidget(ctx, recentWidget); err != nil {
		t.Errorf("recent standalone widget gone: %v", err)
	}
	// The dashboard the standalone widgets live on must survive: it was
	// never archived itself.
	if _, err := db.GetDashboard(ctx, liveDash); err != nil {
		t.Errorf("live dashboard gone: %v", err)
	}
}

// TestPurgeArchivedDeletesProjectData checks a purged project's dependent
// rows go with it, reusing DeleteProjectData's own table list guarantee
// (fk_test.go / errors_test.go already cover that list's completeness).
func TestPurgeArchivedDeletesProjectData(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	id := createPurgeableProject(t, db, "app")

	if err := db.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyViews, ID: "e1", ProjectID: id,
			TS: time.Now(), ReceivedAt: time.Now(),
			Kind: "web", ActorID: "a", ActorKind: store.ActorConnection, Path: "/"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecForTest(
		`INSERT INTO agg_views_daily (project_id, day, kind, visitors, views, sessions, bounces, duration_sec)
		 VALUES (?, '2026-01-01', 'web', 1, 1, 1, 0, 0)`, id); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_x", ProjectID: id, Label: "web"},
		store.AuditEntry{Actor: "test", Action: "key.issue", Subject: "1/web"}); err != nil {
		t.Fatal(err)
	}
	archiveProjectDaysAgo(t, db, id, 31)

	if _, err := db.PurgeArchived(ctx, 30); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{
		`SELECT COUNT(*) FROM events WHERE project_id=?`,
		`SELECT COUNT(*) FROM agg_views_daily WHERE project_id=?`,
		`SELECT COUNT(*) FROM ingest_keys WHERE project_id=?`,
		`SELECT COUNT(*) FROM projects WHERE id=?`,
	} {
		var n int
		if err := db.db.QueryRowContext(ctx, q, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s = %d, want 0", q, n)
		}
	}
}

// TestPurgeArchivedZeroDaysIsNoOp checks days<=0 purges nothing, even a
// project archived long ago.
func TestPurgeArchivedZeroDaysIsNoOp(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	id := createPurgeableProject(t, db, "old")
	archiveProjectDaysAgo(t, db, id, 3650)

	res, err := db.PurgeArchived(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Projects) != 0 || len(res.Dashboards) != 0 || len(res.Widgets) != 0 {
		t.Errorf("PurgeArchived(0) = %+v, want an empty result", res)
	}
	var got int64
	if err := db.db.QueryRowContext(ctx, `SELECT id FROM projects WHERE id=?`, id).Scan(&got); err != nil {
		t.Errorf("project purged despite days=0: %v", err)
	}
}

// TestPurgeArchivedParsesLegacyProjectTimestampFormat checks that
// projects.archived_at, always written as datetime('now') ("YYYY-MM-DD
// HH:MM:SS", no 'T'/'Z' — unlike dashboards' and widgets' RFC3339), is
// still judged correctly: julianday() must parse this format too, not
// just the RFC3339 one the newer tables use.
func TestPurgeArchivedParsesLegacyProjectTimestampFormat(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	id := createPurgeableProject(t, db, "legacy")
	if _, err := db.ExecForTest(
		`UPDATE projects SET archived_at = '2000-01-01 00:00:00' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}

	res, err := db.PurgeArchived(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Projects, id) {
		t.Errorf("Projects purged = %v, want %d (legacy timestamp format)", res.Projects, id)
	}
}

// TestPurgeArchivedWritesOneAuditRowPerKind checks the actor and action
// PurgeArchived's own audit rows carry.
func TestPurgeArchivedWritesOneAuditRowPerKind(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	project := createPurgeableProject(t, db, "app")
	archiveProjectDaysAgo(t, db, project, 31)

	dash := createPurgeableDashboard(t, db, store.OwnerUser, "a", []store.Widget{
		{SortKey: "a", Width: 1, Height: 1, Name: "w1", SourceType: "events", Source: "x"},
	})
	archiveDashboardDaysAgo(t, db, dash, 31)

	standaloneDash := createPurgeableDashboard(t, db, store.OwnerUser, "b", []store.Widget{
		{SortKey: "a", Width: 1, Height: 1, Name: "w2", SourceType: "events", Source: "y"},
	})
	widgets, err := db.ListWidgets(ctx, standaloneDash)
	if err != nil {
		t.Fatal(err)
	}
	archiveWidgetDaysAgo(t, db, widgets[0].ID, 31)

	if _, err := db.PurgeArchived(ctx, 30); err != nil {
		t.Fatal(err)
	}

	for action, wantSubject := range map[string]string{
		"project.purge":   strconv.FormatInt(project, 10),
		"dashboard.purge": "dashboard/" + strconv.FormatInt(dash, 10),
		"widget.purge":    "widget/" + strconv.FormatInt(widgets[0].ID, 10),
	} {
		rows := auditRows(t, db, action)
		if len(rows) != 1 {
			t.Fatalf("%s: audit rows = %d, want 1", action, len(rows))
		}
		if rows[0].Actor != "retention" {
			t.Errorf("%s: actor = %q, want retention", action, rows[0].Actor)
		}
		if rows[0].Subject != wantSubject {
			t.Errorf("%s: subject = %q, want %q", action, rows[0].Subject, wantSubject)
		}
	}
}

// TestPurgeArchivedNeverSelectsSystemDashboards checks the owner='user'
// guard: even a system dashboard forced into an archived state (never
// happens through production code, which never archives one) must not be
// purged.
func TestPurgeArchivedNeverSelectsSystemDashboards(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sysDash := createPurgeableDashboard(t, db, store.OwnerSystem, "a", nil)
	archiveDashboardDaysAgo(t, db, sysDash, 3650)

	res, err := db.PurgeArchived(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if contains(res.Dashboards, sysDash) {
		t.Errorf("Dashboards purged = %v, must never include a system dashboard", res.Dashboards)
	}
	if _, err := db.GetDashboard(ctx, sysDash); err != nil {
		t.Errorf("system dashboard purged: %v", err)
	}
}

// TestPurgeArchivedNeverSelectsWidgetsOnSystemDashboards checks the
// defense-in-depth guard on the widget query itself: a widget on a system
// dashboard, forced into an archived state (never happens through
// production code), must not be purged even though its own archived_at
// alone would otherwise qualify it.
func TestPurgeArchivedNeverSelectsWidgetsOnSystemDashboards(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sysDash := createPurgeableDashboard(t, db, store.OwnerSystem, "a", []store.Widget{
		{SortKey: "a", Width: 1, Height: 1, Name: "w1", SourceType: "events", Source: "x"},
	})
	widgets, err := db.ListWidgets(ctx, sysDash)
	if err != nil {
		t.Fatal(err)
	}
	archiveWidgetDaysAgo(t, db, widgets[0].ID, 3650)

	res, err := db.PurgeArchived(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if contains(res.Widgets, widgets[0].ID) {
		t.Errorf("Widgets purged = %v, must never include one on a system dashboard", res.Widgets)
	}
	if _, err := db.GetWidget(ctx, widgets[0].ID); err != nil {
		t.Errorf("widget on a system dashboard purged: %v", err)
	}
}

// TestPurgeArchivedContinuesPastAFailedItem checks that one item's delete
// failing does not abort the rest of the pass: a trigger makes one
// project's events delete fail (rolling back its own transaction only),
// while a second, otherwise identical project must still be purged, and
// the error for the first must still be reported.
func TestPurgeArchivedContinuesPastAFailedItem(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	bad := createPurgeableProject(t, db, "bad")
	archiveProjectDaysAgo(t, db, bad, 31)
	good := createPurgeableProject(t, db, "good")
	archiveProjectDaysAgo(t, db, good, 31)

	if err := db.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyViews, ID: "e1", ProjectID: bad, TS: time.Now(), ReceivedAt: time.Now(),
			Kind: "web", ActorID: "a", ActorKind: store.ActorConnection, Path: "/"},
	}); err != nil {
		t.Fatal(err)
	}
	// Fails only bad's own events delete, so only its purge transaction
	// rolls back; good's must still commit.
	if _, err := db.ExecForTest(fmt.Sprintf(
		`CREATE TRIGGER fail_bad_events BEFORE DELETE ON events WHEN OLD.project_id = %d
		 BEGIN SELECT RAISE(FAIL, 'boom'); END`, bad)); err != nil {
		t.Fatal(err)
	}

	res, err := db.PurgeArchived(ctx, 30)
	if err == nil {
		t.Fatal("want an error reported for the failed item")
	}
	if !contains(res.Projects, good) {
		t.Errorf("Projects purged = %v, want %d to still be purged", res.Projects, good)
	}
	if contains(res.Projects, bad) {
		t.Errorf("Projects purged = %v, want %d absent (its delete failed)", res.Projects, bad)
	}
	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id=?`, bad).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("failed project's row count = %d, want 1 (its transaction rolled back)", n)
	}
	var gone int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id=?`, good).Scan(&gone); err != nil {
		t.Fatal(err)
	}
	if gone != 0 {
		t.Errorf("good project's row count = %d, want 0 (purged)", gone)
	}
}
