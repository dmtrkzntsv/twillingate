package reporting

import (
	"context"
	"reflect"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Your own dashboards are always in the sidebar (spec 2026-10-05 D5);
// only a built-in group leaves it, and its tabs share the flag (D3).

// Joining a group: a new tab, a copy as a tab and a moved dashboard
// all read sidebar true, and so does one leaving its group.
func TestOwnDashboardsStayInSidebar(t *testing.T) {
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
	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: b.ID, GroupID: ptr[int64](0)}); err != nil {
		t.Fatal(err)
	}
	if got := sidebarOf(t, svc, a.ID, b.ID, cp.ID, c.ID); !reflect.DeepEqual(got, []bool{true, true, true, true}) {
		t.Errorf("sidebar = %v, want all true", got)
	}
}

// Hiding and showing a built-in group write every member, one an older
// binary archived included, so its tabs share the flag.
func TestSidebarWritesArchivedBuiltinMembers(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemGroup()...)
	ctx := context.Background()
	ids := []int64{10, 11, 12, 13, 14}
	if err := svc.st.SetDashboardsArchived(ctx, []int64{13}, true, store.AuditEntry{Actor: "older binary", Action: "dashboard.archive"}); err != nil {
		t.Fatal(err)
	}
	sidebar := func() []bool {
		out := make([]bool, len(ids))
		for i, id := range ids {
			d, err := svc.st.GetDashboard(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			out[i] = d.Sidebar
		}
		return out
	}

	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: 10, Sidebar: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if got := sidebar(); !reflect.DeepEqual(got, []bool{false, false, false, false, false}) {
		t.Errorf("after hide = %v, want all false, archived 13 included", got)
	}
	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: 10, Sidebar: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if got := sidebar(); !reflect.DeepEqual(got, []bool{true, true, true, true, true}) {
		t.Errorf("after show = %v, want all true", got)
	}
}
