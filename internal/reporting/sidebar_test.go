package reporting

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// A group's tabs share its sidebar flag (spec 2026-10-05 D3), and a live
// user dashboard is never out of the sidebar with no project tab (D5):
// every write that changes a group's membership keeps both.

// hiddenGroup makes a user group of AA and BB, each a tab of a new
// project, and takes it out of the sidebar. It returns the two and the
// project.
func hiddenGroup(t *testing.T, svc *Service, st store.Store) (a, b DashboardDetail, p int64) {
	t.Helper()
	a = mustCreate(t, svc, "AA")
	b = mustJoin(t, svc, "BB", a.ID)
	p = mustCreateProject(t, st, "demo")
	mustAddTab(t, svc, p, a.ID)
	mustAddTab(t, svc, p, b.ID)
	if _, err := svc.UpdateDashboard(context.Background(), "test", UpdateDashboard{ID: a.ID, Sidebar: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	return a, b, p
}

func wantInvalid(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, store.ErrInvalid) {
		t.Errorf("%s: err = %v, want ErrInvalid", what, err)
	}
}

// (a) Joining a group in the sidebar: a new tab, a copy as a tab and a
// moved dashboard all read sidebar true.
func TestJoiningVisibleGroupStaysInSidebar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	a := mustCreate(t, svc, "AA")
	b := mustJoin(t, svc, "BB", a.ID)
	cp, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: b.ID, GroupID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	c := mustCreate(t, svc, "CC")
	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: c.ID, GroupID: ptr(a.ID)}); err != nil {
		t.Fatal(err)
	}
	if got := sidebarOf(t, svc, a.ID, b.ID, cp.ID, c.ID); !reflect.DeepEqual(got, []bool{true, true, true, true}) {
		t.Errorf("sidebar = %v, want all true", got)
	}
}

// (a) A new dashboard is on no project's tabs, so it can't join a hidden
// group: not created as a tab, not copied as one.
func TestNewTabInHiddenGroupRefused(t *testing.T) {
	svc, st := newTestServiceOpts(t, Options{}, 1000)
	ctx := context.Background()
	a, b, _ := hiddenGroup(t, svc, st)
	other := mustCreate(t, svc, "Other")
	before := len(userRows(t, svc))

	_, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "CC", GroupID: a.ID})
	wantInvalid(t, "create as a tab", err)
	_, err = svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: other.ID, GroupID: a.ID})
	wantInvalid(t, "duplicate into the group", err)
	_, err = svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: b.ID, GroupID: a.ID})
	wantInvalid(t, "duplicate within its own group", err)
	if after := len(userRows(t, svc)); after != before {
		t.Errorf("dashboards = %d, want %d (nothing written)", after, before)
	}
}

// (a) A dashboard moving into a hidden group needs a project tab, and
// then takes the group's sidebar false.
func TestMoveIntoHiddenGroup(t *testing.T) {
	svc, st := newTestServiceOpts(t, Options{}, 1000)
	ctx := context.Background()
	a, b, p := hiddenGroup(t, svc, st)
	c := mustCreate(t, svc, "CC")

	_, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: c.ID, GroupID: ptr(a.ID)})
	wantInvalid(t, "move without a project tab", err)
	if d, _ := svc.st.GetDashboard(ctx, c.ID); d.GroupID != c.ID || !d.Sidebar {
		t.Errorf("refused move left %+v, want its own group, in the sidebar", d)
	}

	mustAddTab(t, svc, p, c.ID)
	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: c.ID, GroupID: ptr(a.ID)}); err != nil {
		t.Fatal(err)
	}
	if got := sidebarOf(t, svc, a.ID, b.ID, c.ID); !reflect.DeepEqual(got, []bool{false, false, false}) {
		t.Errorf("sidebar = %v, want the whole group false", got)
	}
}

// (b) Leaving a hidden group as a group of one puts the dashboard back
// in the sidebar; the group it left stays hidden.
func TestLeaveHiddenGroupShowsInSidebar(t *testing.T) {
	svc, st := newTestServiceOpts(t, Options{}, 1000)
	a, b, _ := hiddenGroup(t, svc, st)
	if _, err := svc.UpdateDashboard(context.Background(), "test", UpdateDashboard{ID: b.ID, GroupID: ptr[int64](0)}); err != nil {
		t.Fatal(err)
	}
	if got := sidebarOf(t, svc, a.ID, b.ID); !reflect.DeepEqual(got, []bool{false, true}) {
		t.Errorf("sidebar = %v, want [false true]", got)
	}
}

// (c) Hiding and showing write every member, archived ones included; an
// archived member with no project tab does not block hiding.
func TestSidebarWritesArchivedMembers(t *testing.T) {
	svc, st := newTestServiceOpts(t, Options{}, 1000)
	ctx := context.Background()
	a := mustCreate(t, svc, "AA")
	b := mustJoin(t, svc, "BB", a.ID)
	c := mustJoin(t, svc, "CC", a.ID)
	p := mustCreateProject(t, st, "demo")
	mustAddTab(t, svc, p, a.ID)
	mustAddTab(t, svc, p, b.ID)
	if err := svc.ArchiveDashboard(ctx, "test", c.ID, false); err != nil {
		t.Fatal(err)
	}
	sidebar := func() []bool {
		out := make([]bool, 3)
		for i, id := range []int64{a.ID, b.ID, c.ID} {
			d, err := svc.st.GetDashboard(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			out[i] = d.Sidebar
		}
		return out
	}

	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: a.ID, Sidebar: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if got := sidebar(); !reflect.DeepEqual(got, []bool{false, false, false}) {
		t.Errorf("after hide = %v, want all false, archived CC included", got)
	}
	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: a.ID, Sidebar: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if got := sidebar(); !reflect.DeepEqual(got, []bool{true, true, true}) {
		t.Errorf("after show = %v, want all true", got)
	}
}

// (d) Restoring into a hidden group a dashboard with no project tab
// shows the whole group again; one with a tab leaves it hidden.
func TestRestoreIntoHiddenGroup(t *testing.T) {
	for _, whole := range []bool{false, true} {
		svc, st := newTestServiceOpts(t, Options{}, 1000)
		ctx := context.Background()
		a := mustCreate(t, svc, "AA")
		b := mustJoin(t, svc, "BB", a.ID)
		c := mustJoin(t, svc, "CC", a.ID)
		p := mustCreateProject(t, st, "demo")
		mustAddTab(t, svc, p, a.ID)
		mustAddTab(t, svc, p, b.ID)
		mustAddTab(t, svc, p, c.ID)
		if err := svc.ArchiveDashboard(ctx, "test", b.ID, false); err != nil {
			t.Fatal(err)
		}
		if err := svc.ArchiveDashboard(ctx, "test", c.ID, false); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: a.ID, Sidebar: ptr(false)}); err != nil {
			t.Fatal(err)
		}

		// BB has a project tab: restored, the group stays hidden.
		if err := svc.RestoreDashboard(ctx, "test", b.ID, false); err != nil {
			t.Fatal(err)
		}
		if got := sidebarOf(t, svc, a.ID, b.ID); !reflect.DeepEqual(got, []bool{false, false}) {
			t.Errorf("whole=%v: after restoring BB = %v, want the group still hidden", whole, got)
		}

		// CC loses its tab while archived: restoring it shows the group.
		if err := svc.st.DeleteProjectTab(ctx, p, c.ID, store.AuditEntry{Actor: "test", Action: "project.tab.remove"}); err != nil {
			t.Fatal(err)
		}
		if err := svc.RestoreDashboard(ctx, "test", c.ID, whole); err != nil {
			t.Fatal(err)
		}
		if got := sidebarOf(t, svc, a.ID, b.ID, c.ID); !reflect.DeepEqual(got, []bool{true, true, true}) {
			t.Errorf("whole=%v: after restoring CC = %v, want the whole group back in the sidebar", whole, got)
		}
	}
}

// (e) Hiding reads the group and writes it under placeMu, so no other
// placement runs between its read and its write.
func TestSetPlacementTakesPlaceMu(t *testing.T) {
	svc := newTestService(t)
	a := mustCreate(t, svc, "AA")
	svc.placeMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := svc.UpdateDashboard(context.Background(), "test", UpdateDashboard{ID: a.ID, ProjectTab: ptr(true)})
		done <- err
	}()
	select {
	case err := <-done:
		svc.placeMu.Unlock()
		t.Fatalf("setPlacement finished (err %v) while placeMu was held", err)
	case <-time.After(100 * time.Millisecond):
	}
	svc.placeMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
