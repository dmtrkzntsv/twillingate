package sqlite

import (
	"context"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const selectorsJSON = `[{"name":"deployment","label":"Deployment","column":"attr_deployment"}]`

// Migration 051 adds selectors to dashboards and selection to shares; rows
// that exist already get no selectors and the All selection.
func TestMigration051DefaultsExistingRows(t *testing.T) {
	db := newTestDBAt(t, 50)
	ctx := context.Background()
	execAll(t, db,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id) VALUES (1001, 'user', 'D', 'a', 1001)`,
		`INSERT INTO widget_shares (id, title, range_from, range_to, caption_project, image, image_2x)
		 VALUES ('0190a000-0000-7000-8000-000000000001', 'T', '2026-09-01', '2026-09-30', 0, x'00', x'00')`)
	if err := db.migrateThrough(ctx, 51); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM dashboards WHERE id = 1001 AND selectors = '[]'`); n != 1 {
		t.Errorf("dashboards with selectors '[]' = %d, want 1", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM widget_shares WHERE selection = ''`); n != 1 {
		t.Errorf("shares with selection '' = %d, want 1", n)
	}
	got, err := db.GetDashboard(ctx, 1001)
	if err != nil || got.Selectors != "[]" {
		t.Errorf("GetDashboard = %q, %v", got.Selectors, err)
	}
}

func TestDashboardSelectorsRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a := store.AuditEntry{Actor: "api", Action: "dashboard.create"}
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a", Sidebar: true,
		Selectors: selectorsJSON}, nil, a)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.GetDashboard(ctx, id)
	if err != nil || got.Selectors != selectorsJSON {
		t.Fatalf("GetDashboard = %q, %v", got.Selectors, err)
	}
	listed, err := db.ListDashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range listed {
		if d.ID == id {
			found = d.Selectors == selectorsJSON
		}
	}
	if !found {
		t.Error("ListDashboards did not return the selectors")
	}
	got.Selectors = "[]"
	if err := db.UpdateDashboard(ctx, got, store.AuditEntry{Actor: "api", Action: "dashboard.update"}); err != nil {
		t.Fatal(err)
	}
	if got, err = db.GetDashboard(ctx, id); err != nil || got.Selectors != "[]" {
		t.Errorf("after update: %q, %v", got.Selectors, err)
	}
	got.Selectors = ""
	if err := db.UpdateDashboard(ctx, got, a); err != nil {
		t.Fatal(err)
	}
	if got, err = db.GetDashboard(ctx, id); err != nil || got.Selectors != "[]" {
		t.Errorf("empty update: %q, %v", got.Selectors, err)
	}
}

// An empty Selectors is stored as "[]" by both inserts.
func TestInsertDashboardWithoutSelectorsStoresEmptyList(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a := store.AuditEntry{Actor: "api", Action: "dashboard.create"}
	id, err := db.InsertDashboard(ctx, store.Dashboard{Owner: store.OwnerUser, Title: "D", SortKey: "a", Sidebar: true}, nil, a)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := db.InsertDashboardGroup(ctx, []store.Dashboard{
		{Owner: store.OwnerUser, Title: "G1", SortKey: "b", Sidebar: true, Selectors: selectorsJSON},
		{Owner: store.OwnerUser, Title: "G2", SortKey: "c", Sidebar: true}}, nil, a)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range map[int64]string{id: "[]", ids[0]: selectorsJSON, ids[1]: "[]"} {
		if got, err := db.GetDashboard(ctx, i); err != nil || got.Selectors != want {
			t.Errorf("dashboard %d selectors = %q, %v; want %q", i, got.Selectors, err, want)
		}
	}
}

func TestShareSelectionRoundTrip(t *testing.T) {
	db, pid, wid := shareFixture(t)
	const sel = `{"deployment":["cloud"]}`
	got, err := db.InsertWidgetShare(context.Background(), store.NewWidgetShare{ID: shareID(1),
		WidgetID: wid, ProjectID: pid, From: "2026-09-01", To: "2026-09-30", Title: "Visitors",
		Image: png1(), Image2x: png2(), Selection: sel},
		store.AuditEntry{Actor: "api", Action: "widget_share.create"})
	if err != nil || got.Selection != sel {
		t.Fatalf("insert = %q, %v", got.Selection, err)
	}
	if got, err = db.GetWidgetShare(context.Background(), shareID(1)); err != nil || got.Selection != sel {
		t.Errorf("get = %q, %v", got.Selection, err)
	}
	if all := insertShare(t, db, shareID(2), pid, wid, ""); all.Selection != "" {
		t.Errorf("a share made at All has selection %q", all.Selection)
	}
}
