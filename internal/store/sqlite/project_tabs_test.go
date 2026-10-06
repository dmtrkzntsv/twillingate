package sqlite

import (
	"context"
	"errors"
	"path/filepath"
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
	// Given to new projects, so 032's safety net leaves them live out of
	// the sidebar.
	execAll(t, db, `UPDATE dashboards SET project_tab=1 WHERE id IN (10, 11)`)
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

// UpdateDashboard writes the row's sidebar: a row read back keeps its
// value.
func TestUpdateDashboardWritesSidebar(t *testing.T) {
	ctx := context.Background()
	db := tabsDB(t)
	execAll(t, db, `UPDATE dashboards SET project_tab=1 WHERE id=10`) // see TestSetDashboardsSidebar
	upd := store.AuditEntry{Actor: "test", Action: "dashboard.update"}
	for _, want := range []bool{false, true} {
		d, err := db.GetDashboard(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		d.Sidebar = want
		if err := db.UpdateDashboard(ctx, d, upd); err != nil {
			t.Fatal(err)
		}
		if got, err := db.GetDashboard(ctx, 10); err != nil || got.Sidebar != want || got.Title != "A" {
			t.Errorf("after update: %+v, %v; want sidebar %v, title kept", got, err, want)
		}
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

// A refused write must leave the audit log alone: nothing happened.
func TestProjectTabRefusalsWriteNoAudit(t *testing.T) {
	ctx := context.Background()
	db := tabsDB(t)
	_ = db.DeleteProjectTab(ctx, 1, 10, store.AuditEntry{Actor: "test", Action: "project.tab.remove"})
	_ = db.MoveProjectTab(ctx, store.ProjectTabRow{ProjectID: 1, DashboardID: 10, SortKey: "b"}, store.AuditEntry{Actor: "test", Action: "project.tab.move"})
	_ = db.SetDashboardProjectTab(ctx, 99, true, store.AuditEntry{Actor: "test", Action: "dashboard.project_tab"})
	_ = db.InsertProjectTab(ctx, store.ProjectTabRow{ProjectID: 99, DashboardID: 10}, tabAudit)
	var n int
	execScan(t, db, `SELECT count(*) FROM audit_log WHERE action LIKE 'project.tab.%' OR action='dashboard.project_tab'`, &n)
	if n != 0 {
		t.Errorf("%d audit rows after refused writes, want 0", n)
	}
}

// Sidebar and project_tab writes are audited under the dashboard, one row
// per id; the actor and detail the caller passed are kept.
func TestPlacementAuditSubjects(t *testing.T) {
	ctx := context.Background()
	db := tabsDB(t)
	if err := db.SetDashboardsSidebar(ctx, []int64{10, 11}, false,
		store.AuditEntry{Actor: "me", Action: "dashboard.sidebar.hide", Detail: "d"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetDashboardProjectTab(ctx, 12, true,
		store.AuditEntry{Actor: "me", Action: "dashboard.project_tab"}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.QueryContext(ctx, `SELECT action, subject, actor, detail FROM audit_log ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var action, subject, actor, detail string
		if err := rows.Scan(&action, &subject, &actor, &detail); err != nil {
			t.Fatal(err)
		}
		got = append(got, action+" "+subject+" "+actor+" "+detail)
	}
	want := []string{
		"dashboard.sidebar.hide dashboard/10 me d",
		"dashboard.sidebar.hide dashboard/11 me d",
		"dashboard.project_tab dashboard/12 me ",
	}
	if !slices.Equal(got, want) {
		t.Errorf("audit rows = %q, want %q", got, want)
	}
}

func TestSetDashboardsSidebarEmptyIDsIsANoop(t *testing.T) {
	db := tabsDB(t)
	if err := db.SetDashboardsSidebar(context.Background(), nil, false,
		store.AuditEntry{Actor: "test", Action: "dashboard.sidebar.hide"}); err != nil {
		t.Fatal(err)
	}
	if n := countAudit(t, db, "dashboard.sidebar.hide"); n != 0 {
		t.Errorf("audit rows = %d, want 0", n)
	}
}

// After Close every project-tab operation must return an error that is
// neither nil nor a typed refusal: a dead database is not "not found".
func TestProjectTabOperationsOnClosedDB(t *testing.T) {
	db, err := openAt(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	a := store.AuditEntry{Actor: "test", Action: "x"}
	r := store.ProjectTabRow{ProjectID: 1, DashboardID: 10, SortKey: "a"}
	for name, op := range map[string]func() error{
		"ListProjectTabs":        func() error { _, err := db.ListProjectTabs(ctx, 1); return err },
		"ListDashboardProjects":  func() error { _, err := db.ListDashboardProjects(ctx, 10); return err },
		"InsertProjectTab":       func() error { return db.InsertProjectTab(ctx, r, a) },
		"DeleteProjectTab":       func() error { return db.DeleteProjectTab(ctx, 1, 10, a) },
		"MoveProjectTab":         func() error { return db.MoveProjectTab(ctx, r, a) },
		"SetDashboardsSidebar":   func() error { return db.SetDashboardsSidebar(ctx, []int64{10}, true, a) },
		"SetDashboardProjectTab": func() error { return db.SetDashboardProjectTab(ctx, 10, true, a) },
	} {
		err := op()
		if err == nil {
			t.Errorf("%s on a closed DB returned nil, want error", name)
		} else if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
			t.Errorf("%s on a closed DB returned a typed refusal: %v", name, err)
		}
	}
}

// The write and its audit row are one transaction: when the audit insert
// fails, the write is rolled back and the error surfaces.
func TestProjectTabWriteRollsBackWhenAuditFails(t *testing.T) {
	ctx := context.Background()
	db := tabsDB(t)
	execAll(t, db,
		`INSERT INTO project_tabs (project_id, dashboard_id, sort_key) VALUES (1, 10, 'a0')`,
		`CREATE TRIGGER audit_refuses BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'audit down'); END`)
	a := store.AuditEntry{Actor: "test", Action: "x"}

	check := func(name string, err error, query string, want int) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: want the audit error, got nil", name)
		}
		var got int
		execScan(t, db, query, &got)
		if got != want {
			t.Errorf("%s: %s = %d after the failed write, want %d", name, query, got, want)
		}
	}
	check("Insert", db.InsertProjectTab(ctx, store.ProjectTabRow{ProjectID: 1, DashboardID: 11, SortKey: "a1"}, a),
		`SELECT count(*) FROM project_tabs WHERE dashboard_id=11`, 0)
	check("Move", db.MoveProjectTab(ctx, store.ProjectTabRow{ProjectID: 1, DashboardID: 10, SortKey: "b0"}, a),
		`SELECT count(*) FROM project_tabs WHERE sort_key='b0'`, 0)
	check("Delete", db.DeleteProjectTab(ctx, 1, 10, a),
		`SELECT count(*) FROM project_tabs WHERE dashboard_id=10`, 1)
	check("Sidebar", db.SetDashboardsSidebar(ctx, []int64{10}, false, a),
		`SELECT count(*) FROM dashboards WHERE id=10 AND sidebar=0`, 0)
	check("ProjectTab", db.SetDashboardProjectTab(ctx, 10, true, a),
		`SELECT count(*) FROM dashboards WHERE id=10 AND project_tab=1`, 0)
}

// A statement that fails for a reason other than a missing row or a
// duplicate is a plain error, not a typed refusal, and nothing is audited.
func TestProjectTabWriteErrorsAreNotRefusals(t *testing.T) {
	ctx := context.Background()
	db := tabsDB(t)
	execAll(t, db,
		`INSERT INTO project_tabs (project_id, dashboard_id, sort_key) VALUES (1, 10, 'a0')`,
		`CREATE TRIGGER tabs_no_insert BEFORE INSERT ON project_tabs BEGIN SELECT RAISE(ABORT, 'disk full'); END`,
		`CREATE TRIGGER tabs_no_update BEFORE UPDATE ON project_tabs BEGIN SELECT RAISE(ABORT, 'disk full'); END`,
		`CREATE TRIGGER tabs_no_delete BEFORE DELETE ON project_tabs BEGIN SELECT RAISE(ABORT, 'disk full'); END`,
		`CREATE TRIGGER dash_no_update BEFORE UPDATE ON dashboards BEGIN SELECT RAISE(ABORT, 'disk full'); END`)
	a := store.AuditEntry{Actor: "test", Action: "x"}
	for name, err := range map[string]error{
		"Insert":     db.InsertProjectTab(ctx, store.ProjectTabRow{ProjectID: 1, DashboardID: 11, SortKey: "a1"}, a),
		"Move":       db.MoveProjectTab(ctx, store.ProjectTabRow{ProjectID: 1, DashboardID: 10, SortKey: "b0"}, a),
		"Delete":     db.DeleteProjectTab(ctx, 1, 10, a),
		"Sidebar":    db.SetDashboardsSidebar(ctx, []int64{10}, false, a),
		"ProjectTab": db.SetDashboardProjectTab(ctx, 10, true, a),
	} {
		if err == nil {
			t.Errorf("%s: want the statement's error, got nil", name)
		} else if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
			t.Errorf("%s: a failing statement came back as a typed refusal: %v", name, err)
		}
	}
	if n := countAudit(t, db, "x"); n != 0 {
		t.Errorf("audit rows = %d, want 0", n)
	}
}
