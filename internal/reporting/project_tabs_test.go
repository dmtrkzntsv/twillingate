package reporting

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// tabsProject syncs built-ins 1 and 2 (each its own group, a project tab
// on new projects) and creates project P, which the trigger gives both.
// It returns the service, the store and P.
func tabsProject(t *testing.T) (*Service, store.Store, int64) {
	t.Helper()
	svc, st := newTestServiceOpts(t, Options{}, 1000)
	builtin := func(id int64, title, key string) store.SystemDashboard {
		return store.SystemDashboard{
			ID: id, Title: title, SortKey: key, Range: "7d", Sidebar: true, ProjectTab: true,
			Widgets: []store.Widget{{
				Name: "note", Component: "markdown", SortKey: "a0", Width: 12, Height: 2,
				Props: "{}", SourceType: "md", Source: "system text",
			}},
		}
	}
	syncReporting(t, svc, nil, builtin(1, "Views", "a0"), builtin(2, "Product", "a1"))
	return svc, st, mustCreateProject(t, st, "P")
}

// shownTabIDs lists project p's tabs' dashboard ids in shown order.
func shownTabIDs(t *testing.T, svc *Service, p int64) []int64 {
	t.Helper()
	tabs, err := svc.ProjectTabs(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return ids(tabs)
}

func ids(tabs []ProjectTab) []int64 {
	out := []int64{}
	for _, x := range tabs {
		out = append(out, x.ID)
	}
	return out
}

// mustAddProjectTab adds dashboardID to project p after after, through
// the service.
func mustAddProjectTab(t *testing.T, svc *Service, p, dashboardID int64, after *int64) []ProjectTab {
	t.Helper()
	tabs, err := svc.AddProjectTab(context.Background(), "test",
		AddProjectTab{ProjectID: p, DashboardID: dashboardID, After: after})
	if err != nil {
		t.Fatalf("AddProjectTab(%d, %d): %v", p, dashboardID, err)
	}
	return tabs
}

func TestProjectTabsOrder(t *testing.T) {
	svc, st, p := tabsProject(t)
	ctx := context.Background()
	u1 := mustCreate(t, svc, "U1")
	u2 := mustCreate(t, svc, "U2")
	mustAddProjectTab(t, svc, p, u1.ID, nil)
	tabs := mustAddProjectTab(t, svc, p, u2.ID, nil)
	if got, want := ids(tabs), []int64{1, 2, u1.ID, u2.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("after adds = %v, want %v", got, want)
	}
	if tabs[0] != (ProjectTab{ID: 1, Title: "Views", Owner: store.OwnerSystem, GroupID: 1}) {
		t.Errorf("tabs[0] = %+v", tabs[0])
	}
	if tabs[2] != (ProjectTab{ID: u1.ID, Title: "U1", Owner: store.OwnerUser, GroupID: u1.ID}) {
		t.Errorf("tabs[2] = %+v", tabs[2])
	}
	tabs, err := svc.MoveProjectTab(ctx, "test", MoveProjectTab{ProjectID: p, DashboardID: u2.ID, After: 0})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(tabs), []int64{u2.ID, 1, 2, u1.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("after move first = %v, want %v", got, want)
	}
	tabs, err = svc.MoveProjectTab(ctx, "test", MoveProjectTab{ProjectID: p, DashboardID: u2.ID, After: u1.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(tabs), []int64{1, 2, u1.ID, u2.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("after move after u1 = %v, want %v", got, want)
	}
	u3 := mustCreate(t, svc, "U3")
	tabs = mustAddProjectTab(t, svc, p, u3.ID, &u1.ID)
	if got, want := ids(tabs), []int64{1, 2, u1.ID, u3.ID, u2.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("after add after u1 = %v, want %v", got, want)
	}
	tabs = mustAddProjectTab(t, svc, mustCreateProject(t, st, "Q"), u3.ID, ptr(int64(0)))
	if got, want := ids(tabs), []int64{u3.ID, 1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("Q after add first = %v, want %v", got, want)
	}
	if got := auditRowsLike(t, svc, "project.tab.%"); len(got) != 6 || got[0] != "test project.tab.add" || got[2] != "test project.tab.move" {
		t.Errorf("audit = %v", got)
	}
}

// auditRowsLike is every audit row whose action matches like, oldest
// first, as "actor action".
func auditRowsLike(t *testing.T, svc *Service, like string) []string {
	t.Helper()
	res, err := svc.db.Run(context.Background(),
		`SELECT actor, action FROM audit_log WHERE action LIKE '`+like+`' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range res.Rows {
		out = append(out, r[0]+" "+r[1])
	}
	return out
}

func TestProjectTabsEmptyNotNil(t *testing.T) {
	svc, st := newTestServiceOpts(t, Options{}, 1000)
	tabs, err := svc.ProjectTabs(context.Background(), mustCreateProject(t, st, "bare"))
	if err != nil {
		t.Fatal(err)
	}
	if tabs == nil || len(tabs) != 0 {
		t.Errorf("tabs = %#v, want an empty, non-nil list", tabs)
	}
}

func TestProjectTabsSkipsArchived(t *testing.T) {
	svc, _, p := tabsProject(t)
	ctx := context.Background()
	u1 := mustCreate(t, svc, "U1")
	u2 := mustCreate(t, svc, "U2")
	mustAddProjectTab(t, svc, p, u1.ID, nil)
	mustAddProjectTab(t, svc, p, u2.ID, nil)
	if err := svc.ArchiveDashboard(ctx, "test", u1.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, want := shownTabIDs(t, svc, p), []int64{1, 2, u2.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("archived = %v, want %v", got, want)
	}
	if err := svc.RestoreDashboard(ctx, "test", u1.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, want := shownTabIDs(t, svc, p), []int64{1, 2, u1.ID, u2.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("restored = %v, want %v", got, want)
	}
}

func TestRemoveAndReAddBuiltin(t *testing.T) {
	svc, _, p := tabsProject(t)
	ctx := context.Background()
	u := mustCreate(t, svc, "Mine")
	mustAddProjectTab(t, svc, p, u.ID, nil)
	tabs, err := svc.RemoveProjectTab(ctx, "test", p, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(tabs), []int64{1, u.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("removed = %v, want %v", got, want)
	}
	tabs = mustAddProjectTab(t, svc, p, 2, nil)
	if got, want := ids(tabs), []int64{1, u.ID, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("re-added = %v, want %v", got, want)
	}
	tabs, err = svc.RemoveProjectTab(ctx, "test", p, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(tabs), []int64{1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("user tab removed = %v, want %v", got, want)
	}
}

func TestAddProjectTabRefusals(t *testing.T) {
	svc, st, p := tabsProject(t)
	ctx := context.Background()
	u := mustCreate(t, svc, "Mine")
	mustAddProjectTab(t, svc, p, u.ID, nil)
	other := mustCreate(t, svc, "Other")
	gone := mustCreate(t, svc, "Gone")
	if err := svc.ArchiveDashboard(ctx, "test", gone.ID, false); err != nil {
		t.Fatal(err)
	}
	q := mustCreateProject(t, st, "Q")
	mustAddProjectTab(t, svc, q, other.ID, nil)
	fresh := mustCreate(t, svc, "Fresh")
	if _, err := svc.RemoveProjectTab(ctx, "test", p, 2); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		in   AddProjectTab
		kind error
		msg  string
	}{
		{"archived", AddProjectTab{ProjectID: p, DashboardID: gone.ID}, store.ErrInvalid,
			"dashboard " + itoa(gone.ID) + " is archived; restore_dashboard first"},
		{"unknown dashboard", AddProjectTab{ProjectID: p, DashboardID: 9999}, store.ErrNotFound,
			"dashboard 9999: not found"},
		{"unknown project", AddProjectTab{ProjectID: 9999, DashboardID: u.ID}, store.ErrNotFound,
			"project 9999: not found"},
		{"already a tab", AddProjectTab{ProjectID: p, DashboardID: u.ID}, store.ErrConflict,
			"project " + itoa(p) + " already has dashboard " + itoa(u.ID) + " as a tab"},
		{"built-in already a tab", AddProjectTab{ProjectID: p, DashboardID: 1}, store.ErrConflict,
			"project " + itoa(p) + " already has dashboard 1 as a tab"},
		{"after a removed built-in", AddProjectTab{ProjectID: p, DashboardID: fresh.ID, After: ptr(int64(2))}, store.ErrInvalid,
			"after 2 is not one of project " + itoa(p) + "'s tabs"},
		{"after another project's tab", AddProjectTab{ProjectID: p, DashboardID: fresh.ID, After: &other.ID}, store.ErrInvalid,
			"after " + itoa(other.ID) + " is not one of project " + itoa(p) + "'s tabs"},
		{"after an unknown id", AddProjectTab{ProjectID: p, DashboardID: fresh.ID, After: ptr(int64(9999))}, store.ErrInvalid,
			"after 9999 is not one of project " + itoa(p) + "'s tabs"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.AddProjectTab(ctx, "test", c.in)
			wantRefusal(t, err, c.kind, c.msg)
		})
	}
	if got, want := shownTabIDs(t, svc, p), []int64{1, u.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("after refusals = %v, want %v", got, want)
	}
}

// Your own dashboard may lose its last tab: it is always in the sidebar
// (spec 2026-10-05 D5), so it stays live and reachable.
func TestRemoveProjectTabLastOfOwn(t *testing.T) {
	svc, _, p := tabsProject(t)
	ctx := context.Background()
	u := mustCreate(t, svc, "Mine")
	mustAddProjectTab(t, svc, p, u.ID, nil)
	tabs, err := svc.RemoveProjectTab(ctx, "test", p, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(tabs), []int64{1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("removed = %v, want %v", got, want)
	}
	if d, err := svc.st.GetDashboard(ctx, u.ID); err != nil || d.ArchivedAt != "" || !d.Sidebar {
		t.Errorf("after removing its last tab: %+v, %v; want live, in the sidebar", d, err)
	}

	_, err = svc.RemoveProjectTab(ctx, "test", p, u.ID)
	wantRefusal(t, err, store.ErrNotFound, "project "+itoa(p)+" has no tab for dashboard "+itoa(u.ID))
	_, err = svc.RemoveProjectTab(ctx, "test", 9999, u.ID)
	wantRefusal(t, err, store.ErrNotFound, "project 9999: not found")
}

// An archived dashboard's tab can go too.
func TestRemoveProjectTabOfArchived(t *testing.T) {
	svc, _, p := tabsProject(t)
	ctx := context.Background()
	u := mustCreate(t, svc, "Mine")
	mustAddProjectTab(t, svc, p, u.ID, nil)
	if err := svc.ArchiveDashboard(ctx, "test", u.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RemoveProjectTab(ctx, "test", p, u.ID); err != nil {
		t.Fatal(err)
	}
}

// tabsFixture is tabsProject plus two user dashboards added as tabs: the
// service, the project, the built-in tab ids and the user's, in shown order.
func tabsFixture(t *testing.T) (svc *Service, project int64, builtins, own []int64) {
	t.Helper()
	svc, _, project = tabsProject(t)
	u1 := mustCreate(t, svc, "U1")
	u2 := mustCreate(t, svc, "U2")
	mustAddProjectTab(t, svc, project, u1.ID, nil)
	mustAddProjectTab(t, svc, project, u2.ID, nil)
	return svc, project, []int64{1, 2}, []int64{u1.ID, u2.ID}
}

func TestMoveBuiltinTab(t *testing.T) {
	ctx := context.Background()
	s, project, builtins, own := tabsFixture(t)
	first := builtins[0]
	// Built-in to the end, after the user's last tab.
	tabs, err := s.MoveProjectTab(ctx, "t", MoveProjectTab{ProjectID: project, DashboardID: first, After: own[len(own)-1]})
	if err != nil {
		t.Fatal(err)
	}
	if got := tabs[len(tabs)-1].ID; got != first {
		t.Fatalf("last tab = %d, want %d", got, first)
	}
	// A user tab to the front, before every built-in.
	tabs, err = s.MoveProjectTab(ctx, "t", MoveProjectTab{ProjectID: project, DashboardID: own[0], After: 0})
	if err != nil {
		t.Fatal(err)
	}
	if tabs[0].ID != own[0] {
		t.Fatalf("first tab = %d, want %d", tabs[0].ID, own[0])
	}
	if got, want := ids(tabs), []int64{own[0], builtins[1], own[1], first}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestAddBuiltinBackWithAfter(t *testing.T) {
	ctx := context.Background()
	s, project, builtins, own := tabsFixture(t)
	b := builtins[1]
	if _, err := s.RemoveProjectTab(ctx, "t", project, b); err != nil {
		t.Fatal(err)
	}
	after := own[0]
	tabs, err := s.AddProjectTab(ctx, "t", AddProjectTab{ProjectID: project, DashboardID: b, After: &after})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(tabs, func(x ProjectTab) bool { return x.ID == own[0] })
	if tabs[i+1].ID != b {
		t.Fatalf("tab after %d = %d, want %d", own[0], tabs[i+1].ID, b)
	}
}

func TestAddBuiltinBackGoesLast(t *testing.T) {
	ctx := context.Background()
	s, project, builtins, _ := tabsFixture(t)
	b := builtins[0]
	if _, err := s.RemoveProjectTab(ctx, "t", project, b); err != nil {
		t.Fatal(err)
	}
	tabs, err := s.AddProjectTab(ctx, "t", AddProjectTab{ProjectID: project, DashboardID: b})
	if err != nil {
		t.Fatal(err)
	}
	if tabs[len(tabs)-1].ID != b {
		t.Fatalf("last = %d, want %d", tabs[len(tabs)-1].ID, b)
	}
}

// A user's tab added with after 0 goes before the built-ins too.
func TestAddUserTabFirst(t *testing.T) {
	s, project, builtins, own := tabsFixture(t)
	u3 := mustCreate(t, s, "U3")
	tabs := mustAddProjectTab(t, s, project, u3.ID, ptr(int64(0)))
	if got, want := ids(tabs), []int64{u3.ID, builtins[0], builtins[1], own[0], own[1]}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestMoveProjectTabRefusals(t *testing.T) {
	svc, st, p := tabsProject(t)
	ctx := context.Background()
	u1 := mustCreate(t, svc, "U1")
	u2 := mustCreate(t, svc, "U2")
	mustAddProjectTab(t, svc, p, u1.ID, nil)
	mustAddProjectTab(t, svc, p, u2.ID, nil)
	elsewhere := mustCreate(t, svc, "Elsewhere")
	mustAddProjectTab(t, svc, mustCreateProject(t, st, "Q"), elsewhere.ID, nil)

	for _, c := range []struct {
		name string
		in   MoveProjectTab
		kind error
		msg  string
	}{
		{"not a tab", MoveProjectTab{ProjectID: p, DashboardID: elsewhere.ID}, store.ErrNotFound,
			"project " + itoa(p) + " has no tab for dashboard " + itoa(elsewhere.ID)},
		{"unknown dashboard", MoveProjectTab{ProjectID: p, DashboardID: 9999}, store.ErrNotFound,
			"dashboard 9999: not found"},
		{"unknown project", MoveProjectTab{ProjectID: 9999, DashboardID: u1.ID}, store.ErrNotFound,
			"project 9999: not found"},
		{"after another project's tab", MoveProjectTab{ProjectID: p, DashboardID: u2.ID, After: elsewhere.ID}, store.ErrInvalid,
			"after " + itoa(elsewhere.ID) + " is not one of project " + itoa(p) + "'s tabs"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.MoveProjectTab(ctx, "test", c.in)
			wantRefusal(t, err, c.kind, c.msg)
		})
	}

	tabs, err := svc.MoveProjectTab(ctx, "test", MoveProjectTab{ProjectID: p, DashboardID: u2.ID, After: u2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(tabs), []int64{1, 2, u1.ID, u2.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("after itself = %v, want %v", got, want)
	}
	if got := auditRowsLike(t, svc, "project.tab.move"); len(got) != 0 {
		t.Errorf("refused or no-op moves wrote %v", got)
	}
}

func TestMoveProjectTabRespreadsEqualKeys(t *testing.T) {
	svc, st, p := tabsProject(t)
	ctx := context.Background()
	u1 := mustCreate(t, svc, "U1")
	u2 := mustCreate(t, svc, "U2")
	u3 := mustCreate(t, svc, "U3")
	for _, u := range []int64{u1.ID, u2.ID, u3.ID} {
		mustAddProjectTab(t, svc, p, u, nil)
	}
	rawExec(t, st, `UPDATE project_tabs SET sort_key='a2' WHERE project_id=? AND dashboard_id IN (?,?)`, p, u1.ID, u2.ID)
	if got, want := shownTabIDs(t, svc, p), []int64{1, 2, u1.ID, u2.ID, u3.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tied = %v, want %v", got, want)
	}
	tabs, err := svc.MoveProjectTab(ctx, "test", MoveProjectTab{ProjectID: p, DashboardID: u3.ID, After: u1.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(tabs), []int64{1, 2, u1.ID, u3.ID, u2.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("moved between tied = %v, want %v", got, want)
	}
	rows, err := st.ListProjectTabs(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.SortKey] {
			t.Errorf("key %q still shared: %v", r.SortKey, rows)
		}
		seen[r.SortKey] = true
	}
}

func TestProjectTabsUnknownProject(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.ProjectTabs(context.Background(), 9999)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// lockCheck wraps a Store and records, at each project tab write,
// whether placeMu is held: TryLock fails only while someone holds it.
type lockCheck struct {
	Store
	svc      *Service
	unlocked []string
}

func (l *lockCheck) check(what string) {
	if l.svc.placeMu.TryLock() {
		l.svc.placeMu.Unlock()
		l.unlocked = append(l.unlocked, what)
	}
}

func (l *lockCheck) InsertProjectTab(ctx context.Context, r store.ProjectTabRow, a store.AuditEntry) error {
	l.check("InsertProjectTab")
	return l.Store.InsertProjectTab(ctx, r, a)
}

func (l *lockCheck) DeleteProjectTab(ctx context.Context, p, d int64, a store.AuditEntry) error {
	l.check("DeleteProjectTab")
	return l.Store.DeleteProjectTab(ctx, p, d, a)
}

func (l *lockCheck) MoveProjectTab(ctx context.Context, r store.ProjectTabRow, a store.AuditEntry) error {
	l.check("MoveProjectTab")
	return l.Store.MoveProjectTab(ctx, r, a)
}

// Every project tab write runs under placeMu, so a respread never
// interleaves with another tab write.
func TestProjectTabWritesHoldPlaceMu(t *testing.T) {
	svc, st, p := tabsProject(t)
	ctx := context.Background()
	u1 := mustCreate(t, svc, "U1")
	u2 := mustCreate(t, svc, "U2")
	u3 := mustCreate(t, svc, "U3")
	l := &lockCheck{Store: svc.st, svc: svc}
	svc.st = l
	mustAddProjectTab(t, svc, p, u1.ID, nil)
	mustAddProjectTab(t, svc, p, u2.ID, nil)
	rawExec(t, st, `UPDATE project_tabs SET sort_key='a2' WHERE project_id=? AND dashboard_id IN (?,?)`, p, u1.ID, u2.ID)
	if _, err := svc.MoveProjectTab(ctx, "test", MoveProjectTab{ProjectID: p, DashboardID: 2, After: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddProjectTab(ctx, "test", AddProjectTab{ProjectID: p, DashboardID: u3.ID, After: &u1.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RemoveProjectTab(ctx, "test", p, u1.ID); err != nil {
		t.Fatal(err)
	}
	if len(l.unlocked) != 0 {
		t.Errorf("ran without placeMu: %v", l.unlocked)
	}
	if got := auditRowsLike(t, svc, "project.tab.move"); len(got) < 2 {
		t.Errorf("respread wrote %v, want a move per user row", got)
	}
}
