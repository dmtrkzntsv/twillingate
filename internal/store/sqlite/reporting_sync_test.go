package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func chartComponent() store.Component {
	return store.Component{
		Name: "chart", Description: "a chart", Accepts: `["events"]`,
		Inputs: "{}", Props: "{}", DefaultWidth: 4, DefaultHeight: 3,
	}
}

func tableComponent() store.Component {
	return store.Component{
		Name: "table", Description: "a table", Accepts: `["events"]`,
		Inputs: "{}", Props: "{}", DefaultWidth: 6, DefaultHeight: 4,
	}
}

// baseSync is a two-dashboard, two-widget-each manifest used as the
// "before" state by several tests.
func baseSync(hash string) store.ReportingSync {
	return store.ReportingSync{
		Hash: hash, Version: "0.12.0",
		Components: []store.Component{chartComponent(), tableComponent()},
		Dashboards: []store.SystemDashboard{
			{
				ID: 1, Title: "Overview", SortKey: "a", Range: "30d",
				Widgets: []store.Widget{
					{Component: "chart", SortKey: "a", Width: 4, Height: 3,
						Name: "w1", Title: "Views", SourceType: "events", Source: "views"},
					{Component: "table", SortKey: "b", Width: 6, Height: 4,
						Name: "w2", Title: "Table", SourceType: "events", Source: "raw"},
				},
			},
			{
				ID: 2, Title: "Retention", SortKey: "b", Range: "7d",
				Widgets: []store.Widget{
					{Component: "table", SortKey: "a", Width: 6, Height: 4,
						Name: "w1", Title: "Cohorts", SourceType: "events", Source: "retention"},
				},
			},
		},
	}
}

func TestSyncReportingFirstSyncInserts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	s := baseSync("hash-one-0123456789abcdef")

	if err := db.SyncReporting(ctx, s); err != nil {
		t.Fatal(err)
	}

	comps, err := db.ListComponents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(comps) != 2 {
		t.Fatalf("ListComponents = %d, want 2", len(comps))
	}

	dashes, err := db.ListDashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var system []store.Dashboard
	for _, d := range dashes {
		if d.Owner == store.OwnerSystem {
			system = append(system, d)
		}
	}
	if len(system) != 2 {
		t.Fatalf("system dashboards = %d, want 2", len(system))
	}
	for _, d := range system {
		want := map[int64]string{1: "30d", 2: "7d"}[d.ID]
		if d.LastRange != want {
			t.Errorf("dashboard %d LastRange = %q, want %q", d.ID, d.LastRange, want)
		}
	}

	ws1, err := db.ListWidgets(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws1) != 2 {
		t.Fatalf("dashboard 1 widgets = %d, want 2", len(ws1))
	}

	ws2, err := db.ListWidgets(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws2) != 1 {
		t.Fatalf("dashboard 2 widgets = %d, want 1", len(ws2))
	}

	hash, err := db.ReportingHash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hash != s.Hash {
		t.Errorf("ReportingHash = %q, want %q", hash, s.Hash)
	}

	var migrations int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reporting_migrations`).Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	if migrations != 1 {
		t.Errorf("reporting_migrations rows = %d, want 1", migrations)
	}

	var auditRows int
	var actor, action, detail string
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_log WHERE action='reporting.migrate'`).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if auditRows != 1 {
		t.Fatalf("audit rows for reporting.migrate = %d, want 1", auditRows)
	}
	if err := db.db.QueryRowContext(ctx,
		`SELECT actor, action, detail FROM audit_log WHERE action='reporting.migrate'`).
		Scan(&actor, &action, &detail); err != nil {
		t.Fatal(err)
	}
	if actor != "release" {
		t.Errorf("audit actor = %q, want release", actor)
	}
	for _, want := range []string{"2 components", "0 removed", "2 system dashboards", "3 widgets"} {
		if !strings.Contains(detail, want) {
			t.Errorf("audit detail = %q, want it to contain %q", detail, want)
		}
	}
}

func TestSyncReportingSecondSyncEditsPreservesIDsAndView(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.SyncReporting(ctx, baseSync("hash-one")); err != nil {
		t.Fatal(err)
	}

	ws1Before, err := db.ListWidgets(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var keptID int64
	for _, w := range ws1Before {
		if w.Name == "w1" {
			keptID = w.ID
		}
	}
	if keptID == 0 {
		t.Fatal("widget w1 not found after first sync")
	}

	// A viewer picks a project/range in between syncs.
	if err := db.SetDashboardView(ctx, store.Dashboard{
		ID: 1, LastProjectID: 5, LastRange: "7d", LastFrom: "2026-09-01", LastTo: "2026-09-07",
	}); err != nil {
		t.Fatal(err)
	}

	second := store.ReportingSync{
		Hash: "hash-two", Version: "0.13.0",
		Components: []store.Component{chartComponent(), tableComponent()},
		Dashboards: []store.SystemDashboard{
			{
				ID: 1, Title: "Overview v2", SortKey: "a", Range: "90d", // Range ignored: not first insert
				Widgets: []store.Widget{
					// w1 kept (re-ordered to sort_key "b"); w2 removed; w3 added.
					{Component: "chart", SortKey: "b", Width: 4, Height: 3,
						Name: "w1", Title: "Views v2", SourceType: "events", Source: "views"},
					{Component: "table", SortKey: "a", Width: 6, Height: 4,
						Name: "w3", Title: "New", SourceType: "events", Source: "new"},
				},
			},
			{
				ID: 2, Title: "Retention", SortKey: "b", Range: "7d",
				Widgets: []store.Widget{
					{Component: "table", SortKey: "a", Width: 6, Height: 4,
						Name: "w1", Title: "Cohorts", SourceType: "events", Source: "retention"},
				},
			},
		},
	}
	if err := db.SyncReporting(ctx, second); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetDashboard(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Overview v2" {
		t.Errorf("Title = %q, want %q", got.Title, "Overview v2")
	}
	if got.LastRange != "7d" || got.LastProjectID != 5 || got.LastFrom != "2026-09-01" || got.LastTo != "2026-09-07" {
		t.Errorf("SetDashboardView fields did not survive resync: %+v", got)
	}

	ws1After, err := db.ListWidgets(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]store.Widget{}
	for _, w := range ws1After {
		byName[w.Name] = w
	}
	if _, gone := byName["w2"]; gone {
		t.Error("widget w2 should have been removed")
	}
	w1, ok := byName["w1"]
	if !ok {
		t.Fatal("widget w1 should have survived")
	}
	if w1.ID != keptID {
		t.Errorf("widget w1 id changed: %d -> %d, want kept", keptID, w1.ID)
	}
	if w1.SortKey != "b" || w1.Title != "Views v2" {
		t.Errorf("widget w1 not updated: %+v", w1)
	}
	if _, added := byName["w3"]; !added {
		t.Error("widget w3 should have been added")
	}
}

// TestSyncReportingGroupIDDefaultsToOwnID checks a manifest dashboard
// with GroupID 0 (the ordinary case: no dashboard in this release is
// grouped with another) stores group_id = its own id on first sync,
// mirroring insertDashboardRow's rule for an ordinary InsertDashboard.
func TestSyncReportingGroupIDDefaultsToOwnID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.SyncReporting(ctx, baseSync("hash-group-default")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2} {
		got, err := db.GetDashboard(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.GroupID != id {
			t.Errorf("dashboard %d GroupID = %d, want %d (own id)", id, got.GroupID, id)
		}
	}
}

// TestSyncReportingGroupIDNamesAnotherDashboard checks a manifest
// dashboard whose GroupID names another dashboard in the same manifest
// (id 2 naming group 1) stores that group_id, not its own id.
func TestSyncReportingGroupIDNamesAnotherDashboard(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	s := store.ReportingSync{
		Hash: "hash-group-named", Version: "0.1.0",
		Dashboards: []store.SystemDashboard{
			{ID: 1, Title: "Overview", SortKey: "a"},
			{ID: 2, Title: "Overview tab 2", SortKey: "b", GroupID: 1},
		},
	}
	if err := db.SyncReporting(ctx, s); err != nil {
		t.Fatal(err)
	}
	got1, err := db.GetDashboard(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got1.GroupID != 1 {
		t.Errorf("dashboard 1 GroupID = %d, want 1 (own id)", got1.GroupID)
	}
	got2, err := db.GetDashboard(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.GroupID != 1 {
		t.Errorf("dashboard 2 GroupID = %d, want 1 (named group)", got2.GroupID)
	}
}

// TestSyncReportingResyncUpdatesGroupID checks a later sync that changes
// a dashboard's GroupID overwrites the stored group_id, the same as it
// overwrites title and sort_key.
func TestSyncReportingResyncUpdatesGroupID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	first := store.ReportingSync{
		Hash: "hash-regroup-1", Version: "0.1.0",
		Dashboards: []store.SystemDashboard{
			{ID: 1, Title: "Overview", SortKey: "a"},
			{ID: 2, Title: "Retention", SortKey: "b"},
		},
	}
	if err := db.SyncReporting(ctx, first); err != nil {
		t.Fatal(err)
	}
	got2Before, err := db.GetDashboard(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got2Before.GroupID != 2 {
		t.Fatalf("dashboard 2 GroupID before regroup = %d, want 2 (own id)", got2Before.GroupID)
	}

	second := first
	second.Hash = "hash-regroup-2"
	second.Dashboards = []store.SystemDashboard{
		{ID: 1, Title: "Overview", SortKey: "a"},
		{ID: 2, Title: "Retention", SortKey: "b", GroupID: 1},
	}
	if err := db.SyncReporting(ctx, second); err != nil {
		t.Fatal(err)
	}
	got2After, err := db.GetDashboard(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got2After.GroupID != 1 {
		t.Errorf("dashboard 2 GroupID after regroup = %d, want 1 (updated on resync)", got2After.GroupID)
	}
}

func TestSyncReportingSwapsSortKeysWithoutConflict(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	first := store.ReportingSync{
		Hash: "hash-swap-1", Version: "0.1.0",
		Dashboards: []store.SystemDashboard{
			{
				ID: 1, Title: "D", SortKey: "a",
				Widgets: []store.Widget{
					{SortKey: "a", Width: 1, Height: 1, Name: "w1", SourceType: "events", Source: "a"},
					{SortKey: "b", Width: 1, Height: 1, Name: "w2", SourceType: "events", Source: "b"},
				},
			},
		},
	}
	if err := db.SyncReporting(ctx, first); err != nil {
		t.Fatal(err)
	}

	swapped := first
	swapped.Hash = "hash-swap-2"
	swapped.Dashboards = []store.SystemDashboard{
		{
			ID: 1, Title: "D", SortKey: "a",
			Widgets: []store.Widget{
				{SortKey: "b", Width: 1, Height: 1, Name: "w1", SourceType: "events", Source: "a"},
				{SortKey: "a", Width: 1, Height: 1, Name: "w2", SourceType: "events", Source: "b"},
			},
		},
	}
	if err := db.SyncReporting(ctx, swapped); err != nil {
		t.Fatalf("sync with swapped sort keys should succeed: %v", err)
	}
	ws, err := db.ListWidgets(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, w := range ws {
		got[w.Name] = w.SortKey
	}
	if got["w1"] != "b" || got["w2"] != "a" {
		t.Errorf("sort keys after swap = %+v, want w1=b w2=a", got)
	}
}

func TestSyncReportingDroppedComponentNullsUserWidget(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.SyncReporting(ctx, store.ReportingSync{
		Hash: "h1", Components: []store.Component{chartComponent()},
	}); err != nil {
		t.Fatal(err)
	}

	// An agent builds a user dashboard/widget referencing the component.
	userDashID, err := db.InsertDashboard(ctx,
		store.Dashboard{Owner: store.OwnerUser, Title: "Mine", SortKey: "a"},
		[]store.Widget{{Component: "chart", SortKey: "a", Width: 1, Height: 1,
			Name: "w1", SourceType: "events", Source: "a"}},
		store.AuditEntry{Actor: "agent", Action: "dashboard.create"})
	if err != nil {
		t.Fatal(err)
	}

	// A later release drops the component.
	if err := db.SyncReporting(ctx, store.ReportingSync{Hash: "h2"}); err != nil {
		t.Fatal(err)
	}

	comps, err := db.ListComponents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(comps) != 0 {
		t.Fatalf("components after drop = %d, want 0", len(comps))
	}

	ws, err := db.ListWidgets(ctx, userDashID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].Component != "" {
		t.Fatalf("user widget after component drop = %+v, want Component empty", ws)
	}
}

func TestSyncReportingComponentReappearingDoesNotRestoreWidgetLink(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.SyncReporting(ctx, store.ReportingSync{
		Hash: "h1", Components: []store.Component{chartComponent()},
	}); err != nil {
		t.Fatal(err)
	}
	userDashID, err := db.InsertDashboard(ctx,
		store.Dashboard{Owner: store.OwnerUser, Title: "Mine", SortKey: "a"},
		[]store.Widget{{Component: "chart", SortKey: "a", Width: 1, Height: 1,
			Name: "w1", SourceType: "events", Source: "a"}},
		store.AuditEntry{Actor: "agent", Action: "dashboard.create"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SyncReporting(ctx, store.ReportingSync{Hash: "h2"}); err != nil { // drops chart
		t.Fatal(err)
	}
	if err := db.SyncReporting(ctx, store.ReportingSync{
		Hash: "h3", Components: []store.Component{chartComponent()}, // chart comes back
	}); err != nil {
		t.Fatal(err)
	}
	ws, err := db.ListWidgets(ctx, userDashID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].Component != "" {
		t.Fatalf("user widget after component returned = %+v, want Component still empty", ws)
	}
}

func TestSyncReportingDroppedSystemDashboardDeletedWithWidgets(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.SyncReporting(ctx, baseSync("hash-one")); err != nil {
		t.Fatal(err)
	}
	// Second sync only lists dashboard 1: dashboard 2 is dropped.
	second := baseSync("hash-two")
	second.Dashboards = second.Dashboards[:1]
	if err := db.SyncReporting(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetDashboard(ctx, 2); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetDashboard(2) after drop = %v, want ErrNotFound", err)
	}
	ws, err := db.ListWidgets(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 0 {
		t.Errorf("widgets on dropped dashboard 2 = %d, want 0 (cascade)", len(ws))
	}

	// The audit detail must count dashboard 2's one widget as removed even
	// though it never went through syncWidgets: it was deleted by
	// ON DELETE CASCADE inside syncDashboards.
	var detail string
	if err := db.db.QueryRowContext(ctx,
		`SELECT detail FROM audit_log WHERE action='reporting.migrate' ORDER BY rowid DESC LIMIT 1`).
		Scan(&detail); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 system dashboards (1 removed)", "2 widgets (1 removed)"} {
		if !strings.Contains(detail, want) {
			t.Errorf("audit detail = %q, want it to contain %q (cascaded widget counted)", detail, want)
		}
	}
}

func TestSyncReportingRefusesSystemIDOwnedByUserDashboard(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	// A user dashboard holding an id inside the range a release manifest
	// might use (nothing at the DB level stops an explicit low id; the
	// 1-999 reservation is convention, not a constraint).
	userID, err := db.InsertDashboard(ctx,
		store.Dashboard{ID: 5, Owner: store.OwnerUser, Title: "Mine", SortKey: "a"},
		nil, store.AuditEntry{Actor: "agent", Action: "dashboard.create"})
	if err != nil {
		t.Fatal(err)
	}
	if userID != 5 {
		t.Fatalf("InsertDashboard with explicit id = %d, want 5", userID)
	}

	s := store.ReportingSync{
		Hash: "hash-collide", Version: "0.1.0",
		Dashboards: []store.SystemDashboard{
			{
				ID: 5, Title: "Overview", SortKey: "a",
				Widgets: []store.Widget{
					{SortKey: "a", Width: 1, Height: 1, Name: "w1", SourceType: "events", Source: "a"},
				},
			},
		},
	}
	err = db.SyncReporting(ctx, s)
	if err == nil {
		t.Fatal("SyncReporting with a system dashboard id owned by a user dashboard should fail")
	}
	if !strings.Contains(err.Error(), "5") {
		t.Errorf("error = %v, want it to name the colliding id 5", err)
	}

	// Rolled back entirely: the user's dashboard is untouched and got no
	// widgets attached, and no sync was recorded.
	got, err := db.GetDashboard(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != store.OwnerUser || got.Title != "Mine" {
		t.Errorf("user dashboard changed by failed sync: %+v", got)
	}
	ws, err := db.ListWidgets(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 0 {
		t.Errorf("widgets attached to user dashboard by failed sync = %d, want 0", len(ws))
	}
	hash, err := db.ReportingHash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "" {
		t.Errorf("ReportingHash after failed sync = %q, want empty (no prior sync)", hash)
	}
}

func TestSyncReportingUserDashboardsNeverTouched(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	userID, err := db.InsertDashboard(ctx,
		store.Dashboard{Owner: store.OwnerUser, Title: "Mine", SortKey: "a"},
		[]store.Widget{{SortKey: "a", Width: 1, Height: 1, Name: "w1", SourceType: "events", Source: "a"}},
		store.AuditEntry{Actor: "agent", Action: "dashboard.create"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SyncReporting(ctx, baseSync("hash-one")); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetDashboard(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Mine" || got.SortKey != "a" || got.Owner != store.OwnerUser {
		t.Errorf("user dashboard changed by sync: %+v", got)
	}
	ws, err := db.ListWidgets(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].Name != "w1" {
		t.Errorf("user dashboard widgets changed by sync: %+v", ws)
	}
}

func TestSyncReportingFailingSyncRollsBackEverything(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	first := baseSync("hash-good")
	if err := db.SyncReporting(ctx, first); err != nil {
		t.Fatal(err)
	}

	bad := store.ReportingSync{
		Hash: "hash-bad", Version: "0.99.0",
		Dashboards: []store.SystemDashboard{
			{
				ID: 1, Title: "Broken", SortKey: "a",
				Widgets: []store.Widget{
					{Component: "does-not-exist", SortKey: "a", Width: 1, Height: 1,
						Name: "w1", SourceType: "events", Source: "a"},
				},
			},
		},
	}
	if err := db.SyncReporting(ctx, bad); err == nil {
		t.Fatal("sync with a widget naming a missing component should fail")
	}

	hash, err := db.ReportingHash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hash != first.Hash {
		t.Errorf("ReportingHash after failed sync = %q, want unchanged %q", hash, first.Hash)
	}
	got, err := db.GetDashboard(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Overview" {
		t.Errorf("dashboard 1 title after failed sync = %q, want unchanged %q", got.Title, "Overview")
	}
	comps, err := db.ListComponents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(comps) != 2 {
		t.Errorf("components after failed sync = %d, want unchanged 2", len(comps))
	}
}

// hideDashboards is a user's Hide of built-in dashboards: sidebar 0.
func hideDashboards(t *testing.T, db *DB, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		execAll(t, db, fmt.Sprintf(`UPDATE dashboards SET sidebar=0 WHERE id=%d`, id))
	}
}

// D3: hiding a system group survives a sync, and a dashboard the release
// adds to that group arrives hidden too (sidebar 0), never archived.
func TestSyncKeepsHiddenSystemGroup(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	group := func(n int) []store.SystemDashboard {
		out := make([]store.SystemDashboard, n)
		for i := range out {
			var g int64
			if i > 0 {
				g = 10
			}
			out[i] = store.SystemDashboard{ID: int64(10 + i), Title: fmt.Sprintf("T%d", i),
				SortKey: fmt.Sprintf("a%d", i), GroupID: g, Range: "7d", Sidebar: true}
		}
		return out
	}
	sync := func(hash string, ds []store.SystemDashboard) {
		t.Helper()
		if err := db.SyncReporting(ctx, store.ReportingSync{Hash: hash, Version: "test", Dashboards: ds}); err != nil {
			t.Fatal(err)
		}
	}
	sync("h1", group(2))
	// A fresh install: nothing has been hidden yet, so both arrive as the fixture says.
	for _, id := range []int64{10, 11} {
		d, err := db.GetDashboard(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !d.Sidebar || d.ArchivedAt != "" {
			t.Errorf("dashboard %d after first sync = sidebar %v archived %q, want sidebar, live", id, d.Sidebar, d.ArchivedAt)
		}
	}
	hideDashboards(t, db, 10, 11)
	sync("h2", append(group(3), store.SystemDashboard{ID: 20, Title: "Alone", SortKey: "b0", Range: "7d", Sidebar: true}))
	for _, id := range []int64{10, 11, 12} {
		d, err := db.GetDashboard(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Sidebar || d.ArchivedAt != "" {
			t.Errorf("dashboard %d after resync = sidebar %v archived %q, want hidden (its group is hidden) and live", id, d.Sidebar, d.ArchivedAt)
		}
	}
	if d, _ := db.GetDashboard(ctx, 20); !d.Sidebar {
		t.Errorf("new dashboard 20 in its own group hidden, want the fixture's sidebar")
	}
}

// D3: a dashboard regrouped onto a hidden group by the same sync that
// introduces its new leader arrives hidden too. Manifest rows arrive
// sorted by id (internal/reporting/files.go), so the new leader L (lower
// id) is upserted before the pre-existing, hidden M (higher id) that is
// joining it: a per-row check couldn't see M's state yet, which is
// exactly the bug this whole-sync pass fixes.
func TestSyncHidesDashboardRegroupedOntoHiddenGroup(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sync := func(hash string, ds []store.SystemDashboard) {
		t.Helper()
		if err := db.SyncReporting(ctx, store.ReportingSync{Hash: hash, Version: "test", Dashboards: ds}); err != nil {
			t.Fatal(err)
		}
	}
	sync("h1", []store.SystemDashboard{
		{ID: 15, Title: "M", SortKey: "a0", Range: "7d", Sidebar: true},
	})
	hideDashboards(t, db, 15)
	sync("h2", []store.SystemDashboard{
		{ID: 10, Title: "L", SortKey: "a0", Range: "7d", Sidebar: true},
		{ID: 15, Title: "M", SortKey: "a1", GroupID: 10, Range: "7d", Sidebar: true},
	})
	for _, id := range []int64{10, 15} {
		d, err := db.GetDashboard(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Sidebar {
			t.Errorf("dashboard %d in the sidebar after regroup sync, want hidden (joined a hidden group)", id)
		}
	}
}

// D3: a group that is entirely new in this sync (leader and tab both
// unseen before) arrives as its fixture says, even though its tab is
// upserted after its leader; new siblings never count as "pre-existing
// members" either way.
func TestSyncEntirelyNewGroupKeepsFixtureSidebar(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.SyncReporting(ctx, store.ReportingSync{Hash: "h1", Version: "test", Dashboards: []store.SystemDashboard{
		{ID: 30, Title: "Leader", SortKey: "a0", Range: "7d", Sidebar: true},
		{ID: 31, Title: "Tab", SortKey: "a1", GroupID: 30, Range: "7d", Sidebar: true},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{30, 31} {
		d, err := db.GetDashboard(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !d.Sidebar || d.ArchivedAt != "" {
			t.Errorf("dashboard %d in an entirely new group = sidebar %v archived %q, want sidebar, live", id, d.Sidebar, d.ArchivedAt)
		}
	}
}

// D6: the fixture's sidebar is written on insert only; a later resync
// leaves the install's value alone.
func TestSyncReportingSidebarOnInsertOnly(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sync := func(hash string, sidebar bool) {
		t.Helper()
		if err := db.SyncReporting(ctx, store.ReportingSync{Hash: hash, Version: "test", Dashboards: []store.SystemDashboard{
			{ID: 40, Title: "D", SortKey: "a0", Range: "7d", Sidebar: sidebar, ProjectTab: true},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	sync("h1", false)
	if d, err := db.GetDashboard(ctx, 40); err != nil || d.Sidebar {
		t.Fatalf("after insert: sidebar %v err %v, want false", d.Sidebar, err)
	}
	execAll(t, db, `UPDATE dashboards SET sidebar=1 WHERE id=40`)
	sync("h2", false)
	if d, err := db.GetDashboard(ctx, 40); err != nil || !d.Sidebar {
		t.Fatalf("after resync: sidebar %v err %v, want the install's true", d.Sidebar, err)
	}
}

// D6: the fixture's project_tab is the release's, re-synced every time.
func TestSyncReportingProjectTabResyncs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sync := func(hash string, tab bool) {
		t.Helper()
		if err := db.SyncReporting(ctx, store.ReportingSync{Hash: hash, Version: "test", Dashboards: []store.SystemDashboard{
			{ID: 41, Title: "D", SortKey: "a0", Range: "7d", Sidebar: true, ProjectTab: tab},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	sync("h1", true)
	if d, err := db.GetDashboard(ctx, 41); err != nil || !d.ProjectTab {
		t.Fatalf("after insert: project_tab %v err %v, want true", d.ProjectTab, err)
	}
	sync("h2", false)
	if d, err := db.GetDashboard(ctx, 41); err != nil || d.ProjectTab {
		t.Fatalf("after resync: project_tab %v err %v, want false", d.ProjectTab, err)
	}
}

// D4: a built-in the release adds with project_tab lands on every existing
// project once; a tab a user removed stays removed over later syncs.
func TestSyncReportingNewBuiltinSeedsProjectsOnce(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (name) VALUES ('A'), ('B')`)
	var a, b int64
	execScan(t, db, `SELECT id FROM projects WHERE name='A'`, &a)
	execScan(t, db, `SELECT id FROM projects WHERE name='B'`, &b)
	sync := func(hash string) {
		t.Helper()
		if err := db.SyncReporting(ctx, store.ReportingSync{Hash: hash, Version: "test", Dashboards: []store.SystemDashboard{
			{ID: 42, Title: "D", SortKey: "a0", Range: "7d", Sidebar: true, ProjectTab: true},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	sync("h1")
	got := projectTabRows(t, db)
	if len(got) != 2 || !got[[2]int64{a, 42}] || !got[[2]int64{b, 42}] {
		t.Fatalf("tabs after first sync = %v, want both projects", got)
	}
	execAll(t, db, fmt.Sprintf(`DELETE FROM project_tabs WHERE project_id=%d AND dashboard_id=42`, a))
	sync("h2")
	got = projectTabRows(t, db)
	if len(got) != 1 || got[[2]int64{a, 42}] || !got[[2]int64{b, 42}] {
		t.Fatalf("tabs after resync = %v, want only B's", got)
	}
}

func TestReportingHashEmptyWhenNeverSynced(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	hash, err := db.ReportingHash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "" {
		t.Errorf("ReportingHash before any sync = %q, want empty", hash)
	}
}

// namedSystemGroup is baseSync with dashboards 1 and 2 as one group named
// by the fixture of its founder, 1.
func namedSystemGroup(hash string) store.ReportingSync {
	s := baseSync(hash)
	s.Dashboards[0].GroupTitle = "Reports"
	s.Dashboards[1].GroupID = 1
	return s
}

// TestSyncReportingWritesAndClearsSystemGroupNames checks D6: the founder's
// fixture names the group for every member, and a sync without it
// removes the name.
func TestSyncReportingWritesAndClearsSystemGroupNames(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.SyncReporting(ctx, namedSystemGroup("hash-named-1")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2} {
		if got := groupTitleOf(t, db, id); got != "Reports" {
			t.Errorf("dashboard %d GroupTitle = %q, want Reports", id, got)
		}
	}
	unnamed := namedSystemGroup("hash-named-2")
	unnamed.Dashboards[0].GroupTitle = ""
	if err := db.SyncReporting(ctx, unnamed); err != nil {
		t.Fatal(err)
	}
	if got := groupNames(t, db); len(got) != 0 {
		t.Errorf("names after a sync without group_title = %v, want none", got)
	}
}

// TestSyncReportingDroppedSystemGroupLosesName checks a release that drops
// every dashboard of a named system group leaves no name row behind.
func TestSyncReportingDroppedSystemGroupLosesName(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.SyncReporting(ctx, namedSystemGroup("hash-drop-1")); err != nil {
		t.Fatal(err)
	}
	dropped := baseSync("hash-drop-2")
	dropped.Dashboards = nil
	if err := db.SyncReporting(ctx, dropped); err != nil {
		t.Fatal(err)
	}
	if got := groupNames(t, db); len(got) != 0 {
		t.Errorf("names after dropping the group = %v, want none", got)
	}
}
