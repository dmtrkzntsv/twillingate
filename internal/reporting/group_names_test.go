package reporting

import (
	"context"
	"strconv"
	"testing"
)

// groupTitle is dashboard id's group name as the store reads it.
func groupTitle(t *testing.T, svc *Service, id int64) string {
	t.Helper()
	d, err := svc.st.GetDashboard(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d.GroupTitle
}

// groupNameRows is how many dashboard_groups rows exist.
func groupNameRows(t *testing.T, svc *Service) int {
	t.Helper()
	res, err := svc.db.Run(context.Background(), `SELECT COUNT(*) FROM dashboard_groups`)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(res.Rows[0][0])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// assertNoOrphanNames fails when a dashboard_groups row names a group no
// dashboard has (spec D4, D10).
func assertNoOrphanNames(t *testing.T, svc *Service, step string) {
	t.Helper()
	res, err := svc.db.Run(context.Background(), `SELECT group_id FROM dashboard_groups g
		WHERE NOT EXISTS (SELECT 1 FROM dashboards d WHERE d.group_id = g.group_id)`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 0 {
		t.Errorf("after %s: dashboard_groups rows with no dashboard: %v", step, res.Rows)
	}
}

func TestHandOverKeepsNameWithMembersLeft(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	f := mustCreate(t, svc, "Founder")
	t1 := mustJoin(t, svc, "Tab1", f.ID)
	t2 := mustJoin(t, svc, "Tab2", f.ID)
	if _, err := renameGroup(svc, f.ID, "Ops"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: f.ID, GroupID: ptr(int64(0))}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []DashboardDetail{t1, t2} {
		got, err := svc.st.GetDashboard(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.GroupTitle != "Ops" || got.GroupID != t1.ID {
			t.Errorf("tab %d = group %d %q, want group %d \"Ops\"", d.ID, got.GroupID, got.GroupTitle, t1.ID)
		}
	}
	if got := groupTitle(t, svc, f.ID); got != "" {
		t.Errorf("leaver's group title = %q, want none", got)
	}
}

func TestHandOverToAnotherGroupKeepsNameWithMembersLeft(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	f := mustCreate(t, svc, "Founder")
	t1 := mustJoin(t, svc, "Tab1", f.ID)
	t2 := mustJoin(t, svc, "Tab2", f.ID)
	b := mustCreate(t, svc, "Other")
	if _, err := renameGroup(svc, f.ID, "Ops"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: f.ID, GroupID: &b.ID}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{t1.ID, t2.ID} {
		if got := groupTitle(t, svc, id); got != "Ops" {
			t.Errorf("tab %d group title = %q, want \"Ops\"", id, got)
		}
	}
	if got := groupTitle(t, svc, f.ID); got != "" {
		t.Errorf("joiner's group title = %q, want none (B is unnamed)", got)
	}
	if got := groupTitle(t, svc, b.ID); got != "" {
		t.Errorf("B's group title = %q, want none", got)
	}
}

func TestGroupOfOneJoiningLosesName(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	a := mustCreate(t, svc, "Alone")
	b := mustCreate(t, svc, "Other")
	if _, err := renameGroup(svc, a.ID, "Solo group"); err != nil {
		t.Fatal(err)
	}
	if groupNameRows(t, svc) != 1 {
		t.Fatalf("name rows = %d, want 1 after the rename", groupNameRows(t, svc))
	}
	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: a.ID, GroupID: &b.ID}); err != nil {
		t.Fatal(err)
	}
	if got := groupNameRows(t, svc); got != 0 {
		t.Errorf("name rows = %d, want 0: the old group is gone", got)
	}
	assertNoOrphanNames(t, svc, "a named group of one joined another group")
	for _, id := range []int64{a.ID, b.ID} {
		if got := groupTitle(t, svc, id); got != "" {
			t.Errorf("dashboard %d group title = %q, want none", id, got)
		}
	}
}

func TestDuplicateCopiesGroupName(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	a := mustCreate(t, svc, "Alpha")
	b := mustJoin(t, svc, "Beta", a.ID)
	if _, err := renameGroup(svc, a.ID, "Ops"); err != nil {
		t.Fatal(err)
	}

	cp, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: a.ID, WholeGroup: true})
	if err != nil {
		t.Fatal(err)
	}
	if cp.GroupTitle != "Ops (copy)" || cp.Title != "Alpha (copy)" {
		t.Errorf("copy = group %q title %q, want \"Ops (copy)\" and \"Alpha (copy)\"", cp.GroupTitle, cp.Title)
	}
	for _, tab := range cp.Tabs {
		if got := groupTitle(t, svc, tab.ID); got != "Ops (copy)" {
			t.Errorf("copy tab %d group title = %q, want \"Ops (copy)\"", tab.ID, got)
		}
	}
	if len(cp.Tabs) != 2 {
		t.Errorf("copy tabs = %d, want 2", len(cp.Tabs))
	}
	for _, id := range []int64{a.ID, b.ID} {
		if got := groupTitle(t, svc, id); got != "Ops" {
			t.Errorf("original %d group title = %q, want \"Ops\" unchanged", id, got)
		}
	}

	// A single tab's copy carries no group name, joined or alone.
	one, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if one.GroupTitle != "" {
		t.Errorf("single-tab copy group title = %q, want none", one.GroupTitle)
	}
	joined, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: b.ID, GroupID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if joined.GroupID != a.ID || joined.GroupTitle != "Ops" {
		t.Errorf("tab copy = group %d %q, want it a tab of group %d, which keeps \"Ops\"", joined.GroupID, joined.GroupTitle, a.ID)
	}

	// An unnamed group's copy is unnamed.
	u := mustCreate(t, svc, "Plain")
	uc, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: u.ID, WholeGroup: true})
	if err != nil {
		t.Fatal(err)
	}
	if uc.GroupTitle != "" {
		t.Errorf("unnamed group's copy group title = %q, want none", uc.GroupTitle)
	}
	assertNoOrphanNames(t, svc, "the duplicates")
}

func TestDuplicateSystemGroupCopiesItsName(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemGroup()...)
	ctx := context.Background()
	if got := groupTitle(t, svc, 12); got != "Reports" {
		t.Fatalf("system group title = %q, want \"Reports\"", got)
	}
	cp, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: 12, WholeGroup: true})
	if err != nil {
		t.Fatal(err)
	}
	if cp.GroupTitle != "Reports (copy)" {
		t.Errorf("copy group title = %q, want \"Reports (copy)\"", cp.GroupTitle)
	}
	if got := groupTitle(t, svc, 12); got != "Reports" {
		t.Errorf("system group title = %q after the copy, want \"Reports\"", got)
	}
}

// TestNoNameOutlivesItsGroup runs every placement operation, the purge
// and the release sync on named groups, and checks after each that no
// dashboard_groups row names a group without dashboards (spec D4, D10).
func TestNoNameOutlivesItsGroup(t *testing.T) {
	ctx := context.Background()
	svc, full := newTestServiceOpts(t, Options{}, 1000)
	check := func(step string) { t.Helper(); assertNoOrphanNames(t, svc, step) }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// 1. create group A and add tabs
	a := mustCreate(t, svc, "Alpha")
	a2 := mustJoin(t, svc, "Alpha2", a.ID)
	a3 := mustJoin(t, svc, "Alpha3", a.ID)
	other := mustCreate(t, svc, "Other")
	check("create")
	// 2. rename A
	_, err := renameGroup(svc, a.ID, "Group A")
	must(err)
	_, err = renameGroup(svc, other.ID, "Group O")
	must(err)
	check("rename")
	// 3. the founder leaves
	_, err = svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: a.ID, GroupID: ptr(int64(0))})
	must(err)
	check("the founder leaves")
	if got := groupTitle(t, svc, a2.ID); got != "Group A" {
		t.Errorf("heir's group title = %q, want \"Group A\"", got)
	}
	// 4. a tab joins another group
	_, err = svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: a3.ID, GroupID: &other.ID})
	must(err)
	check("a tab joins another group")
	// 5. move a tab within its group
	_, err = svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: a3.ID, After: ptr(int64(0))})
	must(err)
	check("a tab moves within its group")
	// 6. move the whole group
	_, err = svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: other.ID, After: ptr(int64(0))})
	must(err)
	check("the group moves")
	// 7. duplicate whole
	cp, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: other.ID, WholeGroup: true})
	must(err)
	check("duplicate whole")
	// 8. archive whole
	must(svc.ArchiveDashboard(ctx, "test", cp.ID, true))
	check("archive whole")
	// 9. restore whole
	must(svc.RestoreDashboard(ctx, "test", cp.ID, true))
	check("restore whole")
	// 10. archive whole and purge: the copy's name goes with its last row
	must(svc.ArchiveDashboard(ctx, "test", cp.ID, true))
	rawExec(t, full, `UPDATE dashboards SET archived_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-40 days')
		WHERE group_id = ?`, cp.GroupID)
	before := groupNameRows(t, svc)
	_, err = full.PurgeArchived(ctx, 30)
	must(err)
	check("purge")
	if after := groupNameRows(t, svc); after != before-1 {
		t.Errorf("name rows = %d, want %d: the purged group's name goes", after, before-1)
	}
	// 11. the release sync again, with a system group and then without
	syncReporting(t, svc, nil, systemGroup()...)
	check("sync with a system group")
	syncReporting(t, svc, nil)
	check("sync dropping it")
}
