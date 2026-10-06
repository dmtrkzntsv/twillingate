package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestListComponentsByName(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, name := range []string{"table", "chart", "map"} {
		if _, err := db.db.ExecContext(ctx, `INSERT INTO components
			(name, description, accepts, inputs, props, default_width, default_height)
			VALUES (?, 'd', '[]', '{}', '{}', 4, 3)`, name); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ListComponents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("ListComponents = %d rows, want 3", len(got))
	}
	for i, want := range []string{"chart", "map", "table"} {
		if got[i].Name != want {
			t.Errorf("ListComponents[%d].Name = %q, want %q (alphabetical)", i, got[i].Name, want)
		}
	}
}

func TestInsertDashboardRoundTripsEveryField(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx, `INSERT INTO components
		(name, description, accepts, inputs, props, default_width, default_height)
		VALUES ('chart', 'a chart', '[]', '{}', '{}', 4, 3)`); err != nil {
		t.Fatal(err)
	}
	dash := store.Dashboard{
		Owner: store.OwnerUser, Title: "Marketing", SortKey: "a", Sidebar: true,
		LastProjectID: 7, LastRange: "30d", LastFrom: "2026-08-01", LastTo: "2026-08-31",
	}
	ws := []store.Widget{{
		Component: "chart", SortKey: "a", Width: 4, Height: 3,
		Name: "w1", Title: "Signups", Props: `{"metric":"count"}`,
		SourceType: "events", Source: "signup",
	}}
	id, err := db.InsertDashboard(ctx, dash, ws, store.AuditEntry{Actor: "agent", Action: "dashboard.create"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.GetDashboard(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != dash.Owner || got.Title != dash.Title || got.SortKey != dash.SortKey ||
		got.LastProjectID != dash.LastProjectID || got.LastRange != dash.LastRange ||
		got.LastFrom != dash.LastFrom || got.LastTo != dash.LastTo || got.Sidebar != dash.Sidebar {
		t.Fatalf("GetDashboard round-trip = %+v, want fields matching %+v", got, dash)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Errorf("timestamps not set: %+v", got)
	}
	if got.ArchivedAt != "" {
		t.Errorf("ArchivedAt = %q, want empty (live)", got.ArchivedAt)
	}
	if got.LiveWidgets != 1 {
		t.Errorf("LiveWidgets = %d, want 1", got.LiveWidgets)
	}

	widgets, err := db.ListWidgets(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(widgets) != 1 {
		t.Fatalf("ListWidgets = %d widgets, want 1", len(widgets))
	}
	w := widgets[0]
	if w.DashboardID != id || w.Component != "chart" || w.SortKey != "a" ||
		w.Width != 4 || w.Height != 3 || w.Name != "w1" || w.Title != "Signups" ||
		w.Props != `{"metric":"count"}` || w.SourceType != "events" || w.Source != "signup" {
		t.Fatalf("widget round-trip = %+v", w)
	}
	if w.CreatedAt == "" || w.UpdatedAt == "" {
		t.Errorf("widget timestamps not set: %+v", w)
	}

	got2, err := db.GetWidget(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2 != w {
		t.Errorf("GetWidget = %+v, want %+v", got2, w)
	}
}

func TestListDashboardsSystemGroupFirst(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "release", Action: "dashboard.create"}
	mk := func(owner, title, sortKey string) {
		t.Helper()
		if _, err := db.InsertDashboard(ctx,
			store.Dashboard{Owner: owner, Title: title, SortKey: sortKey, Sidebar: true}, nil, audit); err != nil {
			t.Fatal(err)
		}
	}
	mk(store.OwnerUser, "Mine B", "b")
	mk(store.OwnerSystem, "Overview", "a")
	mk(store.OwnerUser, "Mine A", "a")
	mk(store.OwnerSystem, "Retention", "b")

	dashes, err := db.ListDashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashes) != 4 {
		t.Fatalf("ListDashboards = %d rows, want 4", len(dashes))
	}
	var order []string
	for _, d := range dashes {
		order = append(order, d.Owner+"/"+d.Title)
	}
	want := []string{"system/Overview", "system/Retention", "user/Mine A", "user/Mine B"}
	for i, w := range want {
		if order[i] != w {
			t.Errorf("ListDashboards order = %v, want %v", order, want)
			break
		}
	}
}

func TestLiveWidgetsCountsOnlyUnarchived(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "agent", Action: "dashboard.create"}
	ws := []store.Widget{
		{SortKey: "a", Width: 1, Height: 1, Name: "w1", SourceType: "events", Source: "a"},
		{SortKey: "b", Width: 1, Height: 1, Name: "w2", SourceType: "events", Source: "b"},
	}
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a", Sidebar: true}, ws, audit)
	if err != nil {
		t.Fatal(err)
	}
	widgets, err := db.ListWidgets(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetWidgetArchived(ctx, widgets[0].ID, true,
		store.AuditEntry{Actor: "agent", Action: "widget.archive"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetDashboard(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.LiveWidgets != 1 {
		t.Errorf("LiveWidgets = %d, want 1 (one of two archived)", got.LiveWidgets)
	}
	// ListWidgets still returns the archived one.
	all, err := db.ListWidgets(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("ListWidgets = %d, want 2 (archived included)", len(all))
	}
}

func TestDuplicateWidgetNameIsConflict(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "agent", Action: "dashboard.create"}
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a", Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}
	w1 := store.Widget{DashboardID: id, SortKey: "a", Width: 1, Height: 1, Name: "dup", SourceType: "events", Source: "a"}
	if _, err := db.InsertWidget(ctx, w1, store.AuditEntry{Actor: "agent", Action: "widget.create"}); err != nil {
		t.Fatal(err)
	}
	w2 := store.Widget{DashboardID: id, SortKey: "b", Width: 1, Height: 1, Name: "dup", SourceType: "events", Source: "b"}
	_, err = db.InsertWidget(ctx, w2, store.AuditEntry{Actor: "agent", Action: "widget.create"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate widget name error = %v, want ErrConflict", err)
	}

	w3 := store.Widget{DashboardID: id, SortKey: "a", Width: 1, Height: 1, Name: "other", SourceType: "events", Source: "c"}
	_, err = db.InsertWidget(ctx, w3, store.AuditEntry{Actor: "agent", Action: "widget.create"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate widget sort_key error = %v, want ErrConflict", err)
	}
}

func TestDashboardArchiveRestoreRoundTripsAndIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a", Sidebar: true},
		nil, store.AuditEntry{Actor: "agent", Action: "dashboard.create"})
	if err != nil {
		t.Fatal(err)
	}
	archiveAudit := store.AuditEntry{Actor: "agent", Action: "dashboard.archive"}
	if err := db.SetDashboardArchived(ctx, id, true, archiveAudit); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetDashboard(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArchivedAt == "" {
		t.Fatal("dashboard not archived")
	}
	firstArchivedAt := got.ArchivedAt

	// idempotent: archiving again does not change archived_at.
	if err := db.SetDashboardArchived(ctx, id, true, archiveAudit); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetDashboard(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArchivedAt != firstArchivedAt {
		t.Errorf("archived_at changed on repeat archive: %q -> %q", firstArchivedAt, got.ArchivedAt)
	}

	// restore clears it.
	if err := db.SetDashboardArchived(ctx, id, false, store.AuditEntry{Actor: "agent", Action: "dashboard.restore"}); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetDashboard(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArchivedAt != "" {
		t.Errorf("ArchivedAt after restore = %q, want empty", got.ArchivedAt)
	}

	// idempotent restore.
	if err := db.SetDashboardArchived(ctx, id, false, store.AuditEntry{Actor: "agent", Action: "dashboard.restore"}); err != nil {
		t.Fatal(err)
	}
}

func TestSetDashboardViewWritesOnlyLastFieldsNoAudit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a", Sidebar: true},
		nil, store.AuditEntry{Actor: "agent", Action: "dashboard.create"})
	if err != nil {
		t.Fatal(err)
	}
	var auditCountBefore int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&auditCountBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.SetDashboardView(ctx, store.Dashboard{
		ID: id, LastProjectID: 3, LastRange: "7d", LastFrom: "2026-09-01", LastTo: "2026-09-07",
	}); err != nil {
		t.Fatal(err)
	}
	var auditCountAfter int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&auditCountAfter); err != nil {
		t.Fatal(err)
	}
	if auditCountAfter != auditCountBefore {
		t.Errorf("audit_log rows = %d, want %d (SetDashboardView must not audit)", auditCountAfter, auditCountBefore)
	}
	got, err := db.GetDashboard(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastProjectID != 3 || got.LastRange != "7d" || got.LastFrom != "2026-09-01" || got.LastTo != "2026-09-07" {
		t.Errorf("GetDashboard after SetDashboardView = %+v", got)
	}
	if got.Title != "D" || got.SortKey != "a" {
		t.Errorf("SetDashboardView touched non-last_* columns: %+v", got)
	}
}

func TestReportingUnknownIDIsNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.GetDashboard(ctx, 999999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetDashboard unknown id = %v, want ErrNotFound", err)
	}
	if _, err := db.GetWidget(ctx, 999999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetWidget unknown id = %v, want ErrNotFound", err)
	}
	err := db.UpdateDashboard(ctx, store.Dashboard{ID: 999999, Title: "x", SortKey: "y"},
		store.AuditEntry{Actor: "agent", Action: "dashboard.update"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UpdateDashboard unknown id = %v, want ErrNotFound", err)
	}
	err = db.UpdateWidget(ctx, store.Widget{ID: 999999, Name: "x", SortKey: "y", SourceType: "events", Source: "a"},
		store.AuditEntry{Actor: "agent", Action: "widget.update"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UpdateWidget unknown id = %v, want ErrNotFound", err)
	}
	err = db.SetDashboardArchived(ctx, 999999, true, store.AuditEntry{Actor: "agent", Action: "dashboard.archive"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetDashboardArchived unknown id = %v, want ErrNotFound", err)
	}
	err = db.SetWidgetArchived(ctx, 999999, true, store.AuditEntry{Actor: "agent", Action: "widget.archive"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetWidgetArchived unknown id = %v, want ErrNotFound", err)
	}
	err = db.SetDashboardView(ctx, store.Dashboard{ID: 999999})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetDashboardView unknown id = %v, want ErrNotFound", err)
	}
}

func TestWidgetComponentEmptyStoresNull(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a", Sidebar: true},
		nil, store.AuditEntry{Actor: "agent", Action: "dashboard.create"})
	if err != nil {
		t.Fatal(err)
	}
	wID, err := db.InsertWidget(ctx, store.Widget{
		DashboardID: id, SortKey: "a", Width: 1, Height: 1, Name: "w1",
		SourceType: "events", Source: "a", Component: "",
	}, store.AuditEntry{Actor: "agent", Action: "widget.create"})
	if err != nil {
		t.Fatal(err)
	}
	var raw *string
	if err := db.db.QueryRowContext(ctx, `SELECT component FROM widgets WHERE id=?`, wID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != nil {
		t.Errorf("component column = %q, want NULL", *raw)
	}
	got, err := db.GetWidget(ctx, wID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Component != "" {
		t.Errorf("GetWidget().Component = %q, want empty", got.Component)
	}
}

func TestReportingAuditSubjects(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a", Sidebar: true},
		nil, store.AuditEntry{Actor: "agent", Action: "dashboard.create"})
	if err != nil {
		t.Fatal(err)
	}
	var subject string
	if err := db.db.QueryRowContext(ctx,
		`SELECT subject FROM audit_log WHERE action='dashboard.create' ORDER BY rowid DESC LIMIT 1`).
		Scan(&subject); err != nil {
		t.Fatal(err)
	}
	wantDash := "dashboard/" + strconv.FormatInt(id, 10)
	if subject != wantDash {
		t.Errorf("dashboard create audit subject = %q, want %q", subject, wantDash)
	}

	wID, err := db.InsertWidget(ctx, store.Widget{
		DashboardID: id, SortKey: "a", Width: 1, Height: 1, Name: "w1", SourceType: "events", Source: "a",
	}, store.AuditEntry{Actor: "agent", Action: "widget.create"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRowContext(ctx,
		`SELECT subject FROM audit_log WHERE action='widget.create' ORDER BY rowid DESC LIMIT 1`).
		Scan(&subject); err != nil {
		t.Fatal(err)
	}
	wantWidget := "widget/" + strconv.FormatInt(wID, 10)
	if subject != wantWidget {
		t.Errorf("widget create audit subject = %q, want %q", subject, wantWidget)
	}
}

func TestInsertDashboardGroupIDDefaultsToOwnID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "agent", Action: "dashboard.create"}
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a", Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.GetDashboard(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.GroupID != id {
		t.Errorf("GroupID = %d, want %d (own id)", got.GroupID, id)
	}

	id2, err := db.InsertDashboard(ctx,
		store.Dashboard{Owner: store.OwnerUser, Title: "D2", SortKey: "b", GroupID: 1001, Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := db.GetDashboard(ctx, id2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.GroupID != 1001 {
		t.Errorf("GroupID = %d, want 1001 (given group)", got2.GroupID)
	}
}

func TestUpdateDashboardWritesGroupID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "agent", Action: "dashboard.create"}
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a", Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateDashboard(ctx, store.Dashboard{ID: id, Title: "D", SortKey: "a", GroupID: 4242},
		store.AuditEntry{Actor: "agent", Action: "dashboard.update"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetDashboard(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.GroupID != 4242 {
		t.Errorf("GroupID after UpdateDashboard = %d, want 4242", got.GroupID)
	}
}

// TestMoveDashboardsSwapsSortKeysWithoutConflict swaps two rows' sort
// keys in one call, which a naive single-pass UPDATE would fail on the
// (owner, sort_key) unique index mid-way.
func TestMoveDashboardsSwapsSortKeysWithoutConflict(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "agent", Action: "dashboard.create"}
	id1, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D1", SortKey: "a", Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D2", SortKey: "b", Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}
	var auditCountBefore int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&auditCountBefore); err != nil {
		t.Fatal(err)
	}
	err = db.MoveDashboards(ctx, []store.DashboardKey{
		{ID: id1, GroupID: id1, SortKey: "b"},
		{ID: id2, GroupID: id2, SortKey: "a"},
	}, store.GroupRekey{}, store.AuditEntry{Actor: "agent", Action: "dashboard.move"})
	if err != nil {
		t.Fatal(err)
	}
	got1, err := db.GetDashboard(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := db.GetDashboard(ctx, id2)
	if err != nil {
		t.Fatal(err)
	}
	if got1.SortKey != "b" || got2.SortKey != "a" {
		t.Fatalf("swapped sort keys = %q, %q, want b, a", got1.SortKey, got2.SortKey)
	}
	var auditCountAfter int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&auditCountAfter); err != nil {
		t.Fatal(err)
	}
	if auditCountAfter != auditCountBefore+1 {
		t.Errorf("audit_log rows after MoveDashboards = %d, want %d (one row)", auditCountAfter, auditCountBefore+1)
	}
	var subject string
	if err := db.db.QueryRowContext(ctx,
		`SELECT subject FROM audit_log WHERE action='dashboard.move' ORDER BY rowid DESC LIMIT 1`).
		Scan(&subject); err != nil {
		t.Fatal(err)
	}
	wantSubject := "dashboard/" + strconv.FormatInt(id1, 10)
	if subject != wantSubject {
		t.Errorf("MoveDashboards audit subject = %q, want %q", subject, wantSubject)
	}
}

func TestInsertDashboardGroupAttachesWidgetsAndSharesGroupID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ds := []store.Dashboard{
		{Owner: store.OwnerUser, Title: "D1", SortKey: "a", Sidebar: true},
		{Owner: store.OwnerUser, Title: "D2", SortKey: "b", Sidebar: true},
		{Owner: store.OwnerUser, Title: "D3", SortKey: "c", Sidebar: true},
	}
	ws := [][]store.Widget{
		{{SortKey: "a", Width: 1, Height: 1, Name: "w1", SourceType: "events", Source: "a"}},
		nil,
		{{SortKey: "a", Width: 1, Height: 1, Name: "w3", SourceType: "events", Source: "c"}},
	}
	ids, err := db.InsertDashboardGroup(ctx, ds, ws, store.AuditEntry{Actor: "agent", Action: "dashboard.group.create"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("InsertDashboardGroup returned %d ids, want 3", len(ids))
	}
	if !(ids[0] < ids[1] && ids[1] < ids[2]) {
		t.Fatalf("ids not ascending: %v", ids)
	}
	for i, id := range ids {
		got, err := db.GetDashboard(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.GroupID != ids[0] {
			t.Errorf("dashboard %d (index %d) GroupID = %d, want %d", id, i, got.GroupID, ids[0])
		}
	}
	w1, err := db.ListWidgets(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(w1) != 1 || w1[0].Name != "w1" {
		t.Errorf("widgets on dashboard 0 = %+v, want [w1]", w1)
	}
	w2, err := db.ListWidgets(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(w2) != 0 {
		t.Errorf("widgets on dashboard 1 = %+v, want none", w2)
	}
	w3, err := db.ListWidgets(ctx, ids[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(w3) != 1 || w3[0].Name != "w3" {
		t.Errorf("widgets on dashboard 2 = %+v, want [w3]", w3)
	}
}

func TestSetDashboardsArchivedUnknownIDArchivesNone(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "agent", Action: "dashboard.create"}
	id1, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D1", SortKey: "a", Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D2", SortKey: "b", Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}

	err = db.SetDashboardsArchived(ctx, []int64{id1, id2, 999999}, true,
		store.AuditEntry{Actor: "agent", Action: "dashboard.archive"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetDashboardsArchived with an unknown id = %v, want ErrNotFound", err)
	}
	got1, err := db.GetDashboard(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := db.GetDashboard(ctx, id2)
	if err != nil {
		t.Fatal(err)
	}
	if got1.ArchivedAt != "" || got2.ArchivedAt != "" {
		t.Fatalf("archived after a partly-unknown call: %q, %q, want both empty (nothing written)",
			got1.ArchivedAt, got2.ArchivedAt)
	}

	if err := db.SetDashboardsArchived(ctx, []int64{id1, id2}, true,
		store.AuditEntry{Actor: "agent", Action: "dashboard.archive"}); err != nil {
		t.Fatal(err)
	}
	got1, err = db.GetDashboard(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	got2, err = db.GetDashboard(ctx, id2)
	if err != nil {
		t.Fatal(err)
	}
	if got1.ArchivedAt == "" || got2.ArchivedAt == "" {
		t.Fatalf("archived after full call: %q, %q, want both set", got1.ArchivedAt, got2.ArchivedAt)
	}
}

func TestReportingWritesDoNotBumpConfigVersion(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	v0, err := db.ConfigVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a", Sidebar: true},
		[]store.Widget{{SortKey: "a", Width: 1, Height: 1, Name: "w1", SourceType: "events", Source: "a"}},
		store.AuditEntry{Actor: "agent", Action: "dashboard.create"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateDashboard(ctx, store.Dashboard{ID: id, Title: "D2", SortKey: "a"},
		store.AuditEntry{Actor: "agent", Action: "dashboard.update"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetDashboardArchived(ctx, id, true, store.AuditEntry{Actor: "agent", Action: "dashboard.archive"}); err != nil {
		t.Fatal(err)
	}
	v1, err := db.ConfigVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v1 != v0 {
		t.Errorf("config_version = %d after reporting writes, want unchanged %d", v1, v0)
	}
}

func TestInsertDashboardSortKeyConflictIsErrConflict(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "agent", Action: "dashboard.create"}
	if _, err := db.InsertDashboard(ctx,
		store.Dashboard{Owner: store.OwnerUser, Title: "D1", SortKey: "a", Sidebar: true}, nil, audit); err != nil {
		t.Fatal(err)
	}
	_, err := db.InsertDashboard(ctx,
		store.Dashboard{Owner: store.OwnerUser, Title: "D2", SortKey: "a", Sidebar: true}, nil, audit)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("InsertDashboard duplicate (owner, sort_key) = %v, want ErrConflict", err)
	}
	// A different owner may reuse the same sort key: the unique index is
	// (owner, sort_key), not sort_key alone.
	if _, err := db.InsertDashboard(ctx,
		store.Dashboard{Owner: store.OwnerSystem, Title: "S1", SortKey: "a"}, nil, audit); err != nil {
		t.Fatalf("InsertDashboard with same sort key, different owner: %v, want success", err)
	}
}

func TestInsertDashboardIDConflictNamesTheID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "agent", Action: "dashboard.create"}
	id, err := db.InsertDashboard(ctx,
		store.Dashboard{Owner: store.OwnerUser, Title: "D1", SortKey: "a", Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.InsertDashboard(ctx,
		store.Dashboard{ID: id, Owner: store.OwnerUser, Title: "D2", SortKey: "b", Sidebar: true}, nil, audit)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("InsertDashboard with a taken id = %v, want ErrConflict", err)
	}
	if want := fmt.Sprintf("id %d already exists", id); !strings.Contains(err.Error(), want) {
		t.Errorf("InsertDashboard with a taken id = %q, want it to say %q", err, want)
	}
}

func TestUpdateDashboardSortKeyConflictIsErrConflict(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "agent", Action: "dashboard.create"}
	id1, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D1", SortKey: "a", Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D2", SortKey: "b", Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}
	err = db.UpdateDashboard(ctx, store.Dashboard{ID: id2, Title: "D2", SortKey: "a"},
		store.AuditEntry{Actor: "agent", Action: "dashboard.update"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("UpdateDashboard onto another row's (owner, sort_key) = %v, want ErrConflict", err)
	}
	got, err := db.GetDashboard(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	if got.SortKey != "a" {
		t.Errorf("dashboard 1 sort key after refused update = %q, want unchanged %q", got.SortKey, "a")
	}
}

// TestMoveDashboardsConflictWritesNothing moves one dashboard onto a
// sort key another (untouched) row already holds: the park phase only
// parks the rows named in ks, so the real write for the moved row
// collides with the other row's still-live key. Checks the error is
// ErrConflict with its own message (not MoveDashboards reusing
// mapDashboardConflict's "insert dashboard" / blank-owner text) and
// that the transaction rolled back — including the moved row's own
// parked '~' sort key, which must not survive the failed call.
func TestMoveDashboardsConflictWritesNothing(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "agent", Action: "dashboard.create"}
	id1, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D1", SortKey: "a", Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D2", SortKey: "b", Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}
	var auditCountBefore int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&auditCountBefore); err != nil {
		t.Fatal(err)
	}

	err = db.MoveDashboards(ctx, []store.DashboardKey{
		{ID: id1, GroupID: id1, SortKey: "b"}, // id2 still holds "b": not named in this call
	}, store.GroupRekey{}, store.AuditEntry{Actor: "agent", Action: "dashboard.move"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("MoveDashboards onto another row's sort key = %v, want ErrConflict", err)
	}
	if strings.Contains(err.Error(), "insert dashboard") {
		t.Errorf("MoveDashboards conflict message reused insert's wording: %v", err)
	}

	got1, err := db.GetDashboard(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	if got1.SortKey != "a" {
		t.Errorf("dashboard 1 sort key after failed move = %q, want unchanged %q (park phase rolled back too)",
			got1.SortKey, "a")
	}
	got2, err := db.GetDashboard(ctx, id2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.SortKey != "b" {
		t.Errorf("dashboard 2 sort key after failed move = %q, want unchanged %q", got2.SortKey, "b")
	}
	var auditCountAfter int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&auditCountAfter); err != nil {
		t.Fatal(err)
	}
	if auditCountAfter != auditCountBefore {
		t.Errorf("audit_log rows after failed MoveDashboards = %d, want unchanged %d", auditCountAfter, auditCountBefore)
	}
}

// TestMoveDashboardsUnknownIDIsNotFound checks the RowsAffected==0
// branch: an id in ks that no row has (the park phase's WHERE id IN
// (...) matches nothing for it, and neither does the real per-key
// UPDATE) is refused ErrNotFound rather than silently accepted as a
// no-op move.
func TestMoveDashboardsUnknownIDIsNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	err := db.MoveDashboards(ctx, []store.DashboardKey{
		{ID: 999999, GroupID: 999999, SortKey: "a"},
	}, store.GroupRekey{}, store.AuditEntry{Actor: "agent", Action: "dashboard.move"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("MoveDashboards unknown id = %v, want ErrNotFound", err)
	}
}

func TestSetDashboardsArchivedEmptyIDsIsNoOp(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var auditCountBefore int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&auditCountBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.SetDashboardsArchived(ctx, nil, true, store.AuditEntry{Actor: "agent", Action: "dashboard.archive"}); err != nil {
		t.Fatalf("SetDashboardsArchived with no ids: %v, want nil", err)
	}
	var auditCountAfter int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&auditCountAfter); err != nil {
		t.Fatal(err)
	}
	if auditCountAfter != auditCountBefore {
		t.Errorf("audit_log rows after empty-ids SetDashboardsArchived = %d, want unchanged %d",
			auditCountAfter, auditCountBefore)
	}
}

// TestSetDashboardsArchivedRestoresMixedIDs restores two ids in one
// call where only one is actually archived (the other is already live,
// so its UPDATE is an idempotent no-op) — both branches of the restore
// statement's WHERE-less UPDATE in one call, each still auditing its own
// row.
func TestSetDashboardsArchivedRestoresMixedIDs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "agent", Action: "dashboard.create"}
	id1, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D1", SortKey: "a", Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D2", SortKey: "b", Sidebar: true}, nil, audit)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetDashboardArchived(ctx, id1, true, store.AuditEntry{Actor: "agent", Action: "dashboard.archive"}); err != nil {
		t.Fatal(err)
	}
	// id2 is left live.

	var auditCountBefore int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&auditCountBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.SetDashboardsArchived(ctx, []int64{id1, id2}, false,
		store.AuditEntry{Actor: "agent", Action: "dashboard.restore"}); err != nil {
		t.Fatal(err)
	}
	got1, err := db.GetDashboard(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := db.GetDashboard(ctx, id2)
	if err != nil {
		t.Fatal(err)
	}
	if got1.ArchivedAt != "" {
		t.Errorf("dashboard 1 (was archived) ArchivedAt after restore = %q, want empty", got1.ArchivedAt)
	}
	if got2.ArchivedAt != "" {
		t.Errorf("dashboard 2 (was already live) ArchivedAt after restore = %q, want empty", got2.ArchivedAt)
	}
	var auditCountAfter int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&auditCountAfter); err != nil {
		t.Fatal(err)
	}
	if auditCountAfter != auditCountBefore+2 {
		t.Errorf("audit_log rows after restoring 2 mixed ids = %d, want %d (one per id)",
			auditCountAfter, auditCountBefore+2)
	}
}

// TestInsertDashboardGroupEmptyIsNoOp checks an empty ds is accepted as
// a no-op (empty ids, no error, nothing audited) rather than writing a
// group with no members.
func TestInsertDashboardGroupEmptyIsNoOp(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var auditCountBefore int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&auditCountBefore); err != nil {
		t.Fatal(err)
	}
	ids, err := db.InsertDashboardGroup(ctx, nil, nil, store.AuditEntry{Actor: "agent", Action: "dashboard.group.create"})
	if err != nil {
		t.Fatalf("InsertDashboardGroup with no dashboards: %v, want nil", err)
	}
	if len(ids) != 0 {
		t.Errorf("InsertDashboardGroup with no dashboards returned ids = %v, want none", ids)
	}
	var auditCountAfter int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&auditCountAfter); err != nil {
		t.Fatal(err)
	}
	if auditCountAfter != auditCountBefore {
		t.Errorf("audit_log rows after empty InsertDashboardGroup = %d, want unchanged %d",
			auditCountAfter, auditCountBefore)
	}
}

// TestInsertDashboardGroupRefusesMismatchedWidgetSlices checks the ws
// length guard added alongside this test: a ws slice with a length
// other than 0 or len(ds) would otherwise silently drop a trailing
// dashboard's widgets (or index out of range), so it is refused before
// the transaction opens — nothing is written.
func TestInsertDashboardGroupRefusesMismatchedWidgetSlices(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ds := []store.Dashboard{
		{Owner: store.OwnerUser, Title: "D1", SortKey: "a", Sidebar: true},
		{Owner: store.OwnerUser, Title: "D2", SortKey: "b", Sidebar: true},
	}
	ws := [][]store.Widget{
		{{SortKey: "a", Width: 1, Height: 1, Name: "w1", SourceType: "events", Source: "a"}},
	} // one slice, two dashboards
	_, err := db.InsertDashboardGroup(ctx, ds, ws, store.AuditEntry{Actor: "agent", Action: "dashboard.group.create"})
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("InsertDashboardGroup with %d widget slices for %d dashboards = %v, want ErrInvalid",
			len(ws), len(ds), err)
	}
	dashes, err := db.ListDashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashes) != 0 {
		t.Errorf("dashboards after refused InsertDashboardGroup = %d, want 0 (nothing written)", len(dashes))
	}
}

// userGroup inserts n user dashboards as one group, the first a group of
// its own, and returns their ids.
func userGroup(t *testing.T, db *DB, n int) []int64 {
	t.Helper()
	ds := make([]store.Dashboard, n)
	for i := range ds {
		ds[i] = store.Dashboard{Owner: store.OwnerUser, Title: fmt.Sprintf("T%d", i), SortKey: fmt.Sprintf("k%d", i), Sidebar: true}
	}
	ids, err := db.InsertDashboardGroup(context.Background(), ds, nil,
		store.AuditEntry{Actor: "agent", Action: "dashboard.group.create"})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func groupTitleOf(t *testing.T, db *DB, id int64) string {
	t.Helper()
	d, err := db.GetDashboard(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d.GroupTitle
}

func TestSetGroupTitleUpsertsAndReadsBack(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ids := userGroup(t, db, 2)
	g := ids[0]
	a := store.AuditEntry{Actor: "agent", Action: "dashboard.group.rename"}
	if err := db.SetGroupTitle(ctx, g, "Ops", a); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if got := groupTitleOf(t, db, id); got != "Ops" {
			t.Errorf("GetDashboard(%d).GroupTitle = %q, want Ops", id, got)
		}
	}
	all, err := db.ListDashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range all {
		if d.GroupID == g && d.GroupTitle != "Ops" {
			t.Errorf("ListDashboards(%d).GroupTitle = %q, want Ops", d.ID, d.GroupTitle)
		}
	}
	if err := db.SetGroupTitle(ctx, g, "Ops 2", a); err != nil {
		t.Fatal(err)
	}
	if got := groupTitleOf(t, db, ids[1]); got != "Ops 2" {
		t.Errorf("GroupTitle after second rename = %q, want Ops 2", got)
	}
	var rows int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dashboard_groups`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("dashboard_groups rows = %d, want 1", rows)
	}
	var n int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_log WHERE action='dashboard.group.rename' AND subject=?`,
		"group/"+strconv.FormatInt(g, 10)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("rename audit rows for group/%d = %d, want 2", g, n)
	}
}

func TestMoveDashboardsRekeysName(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ids := userGroup(t, db, 3)
	if err := db.SetGroupTitle(ctx, ids[0], "Ops", store.AuditEntry{Actor: "agent", Action: "dashboard.group.rename"}); err != nil {
		t.Fatal(err)
	}
	err := db.MoveDashboards(ctx, []store.DashboardKey{
		{ID: ids[1], GroupID: ids[1], SortKey: "k1"},
		{ID: ids[2], GroupID: ids[1], SortKey: "k2"},
	}, store.GroupRekey{From: ids[0], To: ids[1]}, store.AuditEntry{Actor: "agent", Action: "dashboard.move"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids[1:] {
		if got := groupTitleOf(t, db, id); got != "Ops" {
			t.Errorf("dashboard %d GroupTitle = %q, want Ops", id, got)
		}
	}
	if got := groupTitleOf(t, db, ids[0]); got != "" {
		t.Errorf("dashboard %d (left behind) GroupTitle = %q, want none", ids[0], got)
	}
}

func TestMoveDashboardsZeroRekeyLeavesNames(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ids := userGroup(t, db, 2)
	if err := db.SetGroupTitle(ctx, ids[0], "Ops", store.AuditEntry{Actor: "agent", Action: "dashboard.group.rename"}); err != nil {
		t.Fatal(err)
	}
	err := db.MoveDashboards(ctx, []store.DashboardKey{
		{ID: ids[1], GroupID: ids[0], SortKey: "k9"},
	}, store.GroupRekey{}, store.AuditEntry{Actor: "agent", Action: "dashboard.move"})
	if err != nil {
		t.Fatal(err)
	}
	if got := groupNames(t, db); len(got) != 1 || got[ids[0]] != "Ops" {
		t.Errorf("names after a zero-rekey move = %v, want only %d=Ops", got, ids[0])
	}
}

func TestInsertDashboardGroupWritesName(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a := store.AuditEntry{Actor: "agent", Action: "dashboard.group.create"}
	ids, err := db.InsertDashboardGroup(ctx, []store.Dashboard{
		{Owner: store.OwnerUser, Title: "A", SortKey: "a", GroupTitle: "Ops (copy)", Sidebar: true},
		{Owner: store.OwnerUser, Title: "B", SortKey: "b", Sidebar: true},
	}, nil, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if got := groupTitleOf(t, db, id); got != "Ops (copy)" {
			t.Errorf("dashboard %d GroupTitle = %q, want Ops (copy)", id, got)
		}
	}
	more, err := db.InsertDashboardGroup(ctx, []store.Dashboard{
		{Owner: store.OwnerUser, Title: "C", SortKey: "c", Sidebar: true},
	}, nil, a)
	if err != nil {
		t.Fatal(err)
	}
	if got := groupTitleOf(t, db, more[0]); got != "" {
		t.Errorf("unnamed group GroupTitle = %q, want none", got)
	}
	if got := groupNames(t, db); len(got) != 1 {
		t.Errorf("name rows = %v, want one", got)
	}
}
