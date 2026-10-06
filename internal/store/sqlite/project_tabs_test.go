package sqlite

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// tabsDB returns a migrated store with projects 1 and 2 and user
// dashboards 10, 11 and 12 (none a project tab, so no project is seeded).
func tabsDB(t *testing.T) *DB {
	t.Helper()
	db := newTestDB(t)
	execAll(t, db,
		`INSERT INTO projects (id, name) VALUES (1, 'One'), (2, 'Two')`,
		`INSERT INTO dashboards (id, owner, title, sort_key, group_id) VALUES
			(10, 'user', 'A', 'a0', 10),
			(11, 'user', 'B', 'a1', 11),
			(12, 'user', 'C', 'a2', 12)`)
	return db
}

var tabAudit = store.AuditEntry{Actor: "test", Action: "project.tab.add"}

func countAudit(t *testing.T, db *DB, action string) int {
	t.Helper()
	var n int
	execScan(t, db, `SELECT count(*) FROM audit_log WHERE action='`+action+`'`, &n)
	return n
}

func TestListProjectTabs(t *testing.T) {
	ctx := context.Background()
	db := tabsDB(t)
	if _, err := db.ListProjectTabs(ctx, 99); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown project: err = %v, want ErrNotFound", err)
	}
	got, err := db.ListProjectTabs(ctx, 1)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("no rows: got %v, %v; want empty non-nil, nil", got, err)
	}
	execAll(t, db, `INSERT INTO project_tabs (project_id, dashboard_id, sort_key) VALUES
		(1, 12, 'a1'), (1, 11, 'a1'), (1, 10, 'a0'), (2, 10, 'a0')`)
	got, err = db.ListProjectTabs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.ProjectTabRow{
		{ProjectID: 1, DashboardID: 10, SortKey: "a0"},
		{ProjectID: 1, DashboardID: 11, SortKey: "a1"},
		{ProjectID: 1, DashboardID: 12, SortKey: "a1"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestInsertProjectTab(t *testing.T) {
	ctx := context.Background()
	db := tabsDB(t)
	r := store.ProjectTabRow{ProjectID: 1, DashboardID: 10, SortKey: "a0"}
	if err := db.InsertProjectTab(ctx, r, tabAudit); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertProjectTab(ctx, r, tabAudit); !errors.Is(err, store.ErrConflict) {
		t.Errorf("twice: err = %v, want ErrConflict", err)
	}
	if err := db.InsertProjectTab(ctx, store.ProjectTabRow{ProjectID: 99, DashboardID: 10}, tabAudit); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown project: err = %v, want ErrNotFound", err)
	}
	if err := db.InsertProjectTab(ctx, store.ProjectTabRow{ProjectID: 1, DashboardID: 99}, tabAudit); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown dashboard: err = %v, want ErrNotFound", err)
	}
	if n := countAudit(t, db, "project.tab.add"); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
	var subject string
	execScan(t, db, `SELECT subject FROM audit_log WHERE action='project.tab.add'`, &subject)
	if subject != "project/1/tab/10" {
		t.Errorf("subject = %q", subject)
	}
}

func TestDeleteProjectTab(t *testing.T) {
	ctx := context.Background()
	db := tabsDB(t)
	a := store.AuditEntry{Actor: "test", Action: "project.tab.remove"}
	if err := db.DeleteProjectTab(ctx, 1, 10, a); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing row: err = %v, want ErrNotFound", err)
	}
	execAll(t, db, `INSERT INTO project_tabs (project_id, dashboard_id, sort_key) VALUES (1, 10, 'a0')`)
	if err := db.DeleteProjectTab(ctx, 1, 10, a); err != nil {
		t.Fatal(err)
	}
	if got := projectTabRows(t, db); len(got) != 0 {
		t.Errorf("rows left: %v", got)
	}
	if n := countAudit(t, db, "project.tab.remove"); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
}

func TestMoveProjectTab(t *testing.T) {
	ctx := context.Background()
	db := tabsDB(t)
	a := store.AuditEntry{Actor: "test", Action: "project.tab.move"}
	r := store.ProjectTabRow{ProjectID: 1, DashboardID: 10, SortKey: "b0"}
	if err := db.MoveProjectTab(ctx, r, a); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing row: err = %v, want ErrNotFound", err)
	}
	execAll(t, db, `INSERT INTO project_tabs (project_id, dashboard_id, sort_key) VALUES (1, 10, 'a0')`)
	if err := db.MoveProjectTab(ctx, r, a); err != nil {
		t.Fatal(err)
	}
	var key string
	execScan(t, db, `SELECT sort_key FROM project_tabs WHERE project_id=1 AND dashboard_id=10`, &key)
	if key != "b0" {
		t.Errorf("sort_key = %q, want b0", key)
	}
	if n := countAudit(t, db, "project.tab.move"); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
}

func TestListDashboardProjects(t *testing.T) {
	ctx := context.Background()
	db := tabsDB(t)
	execAll(t, db, `INSERT INTO project_tabs (project_id, dashboard_id, sort_key) VALUES
		(2, 10, 'a0'), (1, 10, 'a0'), (1, 11, 'a1')`)
	got, err := db.ListDashboardProjects(ctx, 10)
	if err != nil || !slices.Equal(got, []int64{1, 2}) {
		t.Errorf("got %v, %v; want [1 2]", got, err)
	}
	got, err = db.ListDashboardProjects(ctx, 12)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("none: got %v, %v; want empty non-nil", got, err)
	}
}

func TestSetDashboardsSidebar(t *testing.T) {
	ctx := context.Background()
	db := tabsDB(t)
	sidebar := func(id int64) int {
		var v int
		execScan(t, db, `SELECT sidebar FROM dashboards WHERE id=`+strconv.FormatInt(id, 10), &v)
		return v
	}
	hide := store.AuditEntry{Actor: "test", Action: "dashboard.sidebar.hide"}
	if err := db.SetDashboardsSidebar(ctx, []int64{10, 99}, false, hide); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
	if sidebar(10) != 1 || countAudit(t, db, "dashboard.sidebar.hide") != 0 {
		t.Error("an unknown id must leave every row and the audit log untouched")
	}
	if err := db.SetDashboardsSidebar(ctx, []int64{10, 11}, false, hide); err != nil {
		t.Fatal(err)
	}
	if sidebar(10) != 0 || sidebar(11) != 0 || sidebar(12) != 1 {
		t.Errorf("sidebar = %d %d %d, want 0 0 1", sidebar(10), sidebar(11), sidebar(12))
	}
	if n := countAudit(t, db, "dashboard.sidebar.hide"); n != 2 {
		t.Errorf("audit rows = %d, want 2", n)
	}
	show := store.AuditEntry{Actor: "test", Action: "dashboard.sidebar.show"}
	if err := db.SetDashboardsSidebar(ctx, []int64{10}, true, show); err != nil {
		t.Fatal(err)
	}
	if sidebar(10) != 1 {
		t.Error("show did not set sidebar 1")
	}
}

func TestSetDashboardProjectTab(t *testing.T) {
	ctx := context.Background()
	db := tabsDB(t)
	a := store.AuditEntry{Actor: "test", Action: "dashboard.project_tab"}
	if err := db.SetDashboardProjectTab(ctx, 99, true, a); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
	for _, on := range []bool{true, false} {
		if err := db.SetDashboardProjectTab(ctx, 10, on, a); err != nil {
			t.Fatal(err)
		}
		var v int
		execScan(t, db, `SELECT project_tab FROM dashboards WHERE id=10`, &v)
		if (v == 1) != on {
			t.Errorf("project_tab = %d after on=%v", v, on)
		}
	}
	if n := countAudit(t, db, "dashboard.project_tab"); n != 2 {
		t.Errorf("audit rows = %d, want 2", n)
	}
}
