package sqlite

import (
	"context"
	"errors"
	"strconv"
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
		Owner: store.OwnerUser, Title: "Marketing", SortKey: "a",
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
		got.LastFrom != dash.LastFrom || got.LastTo != dash.LastTo {
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
			store.Dashboard{Owner: owner, Title: title, SortKey: sortKey}, nil, audit); err != nil {
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
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a"}, ws, audit)
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
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a"}, nil, audit)
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
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a"},
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
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a"},
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
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a"},
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
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a"},
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

func TestReportingWritesDoNotBumpConfigVersion(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	v0, err := db.ConfigVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a"},
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
