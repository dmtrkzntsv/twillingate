package reporting

import (
	"context"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/shared/sortkey"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// DuplicateDashboard copies dashboard ID: alone, or with WholeGroup its
// whole group. A single tab's copy is a group of one when GroupID is 0,
// as CreateDashboard's is, and otherwise a tab of group GroupID; a whole
// group's copy is always a new group, so GroupID must then be 0.
type DuplicateDashboard struct {
	ID         int64
	WholeGroup bool
	GroupID    int64
	Narrow     Narrow // what the copies' query widgets are narrowed to
}

// DuplicateDashboard makes a user copy of a dashboard, system ones
// included. Its live widgets are copied in order with fresh keys and
// the same content, and the same stored selection. The widgets are
// copied as they are, not re-checked, so the copy is faithful: a widget
// whose component was removed is copied too and still shows "component
// removed" there, until update_widget switches it. D15's no-copy rule is
// copy_widget's, which places one widget somewhere new.
//
// Duplicating never archives anything (nothing but archive_dashboard
// does): without WholeGroup it copies just in.ID, system or user — as a
// new user group of one, last in the sidebar, when GroupID is 0; as a
// tab of user group GroupID otherwise, right after the source when that
// is the source's own group and last among its tabs when not. WholeGroup
// copies every
// member of id's group, each with its live widgets, as one new user
// group placed last, in the same tab order; the first copy is titled
// "… (copy)", the rest keep their titles. For a user source only the
// live members are copied; a built-in is never archived (spec
// 2026-10-05 D5), so a system source's members all are. Every copy is
// in the sidebar, is no project's tab and is not given to new projects
// (spec 2026-10-05 D9).
//
// An archived user source is refused: its copy would otherwise land in
// a group that may have no live dashboard left, bringing that group
// back into the sidebar through the copy.
func (s *Service) DuplicateDashboard(ctx context.Context, actor string, in DuplicateDashboard) (DashboardDetail, error) {
	id := in.ID
	src, err := s.st.GetDashboard(ctx, id)
	if err != nil {
		return DashboardDetail{}, err
	}
	system := src.Owner == store.OwnerSystem
	if src.ArchivedAt != "" && !system {
		return DashboardDetail{}, store.Refuse(store.ErrInvalid, "dashboard %d is archived; restore_dashboard first", id)
	}
	if in.WholeGroup {
		if in.GroupID != 0 {
			return DashboardDetail{}, store.Refuse(store.ErrInvalid, "whole_group copies the group as a new dashboard; drop group_id")
		}
		return s.duplicateGroup(ctx, actor, src, in.Narrow)
	}
	return s.duplicateOne(ctx, actor, src, in.GroupID, in.Narrow)
}

// duplicateOne is DuplicateDashboard for a single tab: a group of its own
// when group is 0, else a tab of group (a live user group).
func (s *Service) duplicateOne(ctx context.Context, actor string, src store.Dashboard, group int64, n Narrow) (DashboardDetail, error) {
	ws, err := s.copyLiveWidgets(ctx, src.ID, n)
	if err != nil {
		return DashboardDetail{}, err
	}
	copyOf := store.Dashboard{
		Owner: store.OwnerUser, Title: src.Title + " (copy)", Sidebar: true,
		LastProjectID: src.LastProjectID, LastRange: src.LastRange, LastFrom: src.LastFrom, LastTo: src.LastTo,
	}
	var newID int64
	err = s.placeDashboards(func() error {
		o, err := s.readOrder(ctx)
		if err != nil {
			return err
		}
		switch {
		case group == 0:
			copyOf.SortKey, err = o.keyAfterGroup(0, nil)
		case src.Owner == store.OwnerUser && group == src.GroupID:
			copyOf.GroupID = group
			copyOf.SortKey, err = o.keyInGroup(0, group, &src.ID)
		default:
			if err = refuseGroup(o, group); err == nil {
				copyOf.GroupID = group
				copyOf.SortKey, err = o.keyInGroup(0, group, nil)
			}
		}
		if err != nil {
			return err
		}
		newID, err = s.st.InsertDashboard(ctx, copyOf, ws, store.AuditEntry{
			Actor: actor, Action: "dashboard.duplicate",
			Detail: fmt.Sprintf("from dashboard/%d", src.ID)})
		return err
	})
	if err != nil {
		return DashboardDetail{}, err
	}
	return s.Dashboard(ctx, newID)
}

// duplicateGroup is DuplicateDashboard for id's whole group, as one new
// user group placed last among the user dashboards, in the same tab
// order. For a system source, every member of src.GroupID owned by the
// system is copied (none is ever archived, spec 2026-10-05 D5). For a
// user source, only the live members are copied.
// Returns the copy at src's own position among the copied members, not
// always the group's first (that one is always titled "… (copy)").
//
// The membership read is computed inside placeDashboards' closure,
// alongside the placement read, so every placement attempt
// (retryConflict's rerun included) reads the group afresh rather than
// reusing an outer read. A bare archive or restore takes no placeMu and
// raises no sort-key conflict, so one racing the copy is not serialised
// with it: the copy follows the membership as its last attempt read it.
func (s *Service) duplicateGroup(ctx context.Context, actor string, src store.Dashboard, n Narrow) (DashboardDetail, error) {
	system := src.Owner == store.OwnerSystem
	var ids []int64
	var srcIndex int
	err := s.placeDashboards(func() error {
		all, err := s.st.ListDashboards(ctx)
		if err != nil {
			return err
		}
		var members []store.Dashboard
		for _, d := range all {
			if d.GroupID != src.GroupID || d.Owner != src.Owner {
				continue
			}
			if !system && d.ArchivedAt != "" {
				continue
			}
			members = append(members, d)
		}
		ds := make([]store.Dashboard, len(members))
		wss := make([][]store.Widget, len(members))
		srcIndex = 0
		for i, m := range members {
			if m.ID == src.ID {
				srcIndex = i
			}
			if wss[i], err = s.copyLiveWidgets(ctx, m.ID, n); err != nil {
				return err
			}
			title := m.Title
			if i == 0 {
				title += " (copy)"
			}
			ds[i] = store.Dashboard{
				Owner: store.OwnerUser, Title: title, Sidebar: true,
				LastProjectID: m.LastProjectID, LastRange: m.LastRange, LastFrom: m.LastFrom, LastTo: m.LastTo,
			}
		}
		// D7: the name is copied like the first tab's title. Taken from this
		// attempt's own read (every member row carries the group's name), not
		// from src, which a concurrent rename may have outdated.
		if len(members) > 0 && members[0].GroupTitle != "" {
			ds[0].GroupTitle = members[0].GroupTitle + " (copy)"
		}

		o, err := s.readOrder(ctx)
		if err != nil {
			return err
		}
		var last string
		if len(o) > 0 {
			last = o[len(o)-1].SortKey
		}
		keys, err := sortkey.Spread(last, "", len(ds))
		if err != nil {
			return err
		}
		for i := range ds {
			ds[i].SortKey = keys[i]
		}
		ids, err = s.st.InsertDashboardGroup(ctx, ds, wss, store.AuditEntry{
			Actor: actor, Action: "dashboard.duplicate",
			Detail: fmt.Sprintf("group of dashboard/%d", src.ID)})
		return err
	})
	if err != nil {
		return DashboardDetail{}, err
	}
	return s.Dashboard(ctx, ids[srcIndex])
}

// copyLiveWidgets returns fresh copies of dashboardID's live widgets, in
// order, with fresh spread keys; a widget whose component was removed
// still copies, faithfully. A sql widget, in a process with no external
// SQL engine, refuses the copy (ErrSQLEngine), as copy_widget and
// create_dashboard refuse one.
func (s *Service) copyLiveWidgets(ctx context.Context, dashboardID int64, n Narrow) ([]store.Widget, error) {
	all, err := s.st.ListWidgets(ctx, dashboardID)
	if err != nil {
		return nil, err
	}
	_, hasSQL := s.sources["sql"]
	var ws []store.Widget
	for _, w := range all {
		if w.ArchivedAt == "" && w.SourceType == "sql" && !hasSQL {
			return nil, ErrSQLEngine
		}
		if w.ArchivedAt == "" {
			c, err := narrowed(freshCopy(w), n)
			if err != nil {
				return nil, err
			}
			ws = append(ws, c)
		}
	}
	keys, err := sortkey.Spread("", "", len(ws))
	if err != nil {
		return nil, err
	}
	for i := range ws {
		ws[i].SortKey = keys[i]
	}
	return ws, nil
}
