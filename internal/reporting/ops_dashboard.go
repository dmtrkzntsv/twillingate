package reporting

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/sortkey"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Presets is the closed vocabulary of a dashboard's stored range (D18).
// The UI resolves each to dates; Go knows them only to validate.
var Presets = []string{"today", "yesterday", "7d", "30d", "90d", "custom"}

// After, in CreateDashboard, UpdateDashboard, AddWidget and CopyWidget,
// places an item: nil last, 0 first, an id right after that item
// (Deviation 1; keyAfter in place.go for widgets, order.go for
// dashboards, where an id in another group means after that group).

// CreateDashboard makes a new group of one when GroupID is 0, placed by
// After in the sidebar; otherwise a tab of group GroupID, placed by After
// among its tabs (After must then be a member).
type CreateDashboard struct {
	Title, Range string
	GroupID      int64
	After        *int64
	Widgets      []WidgetSpec
}

// UpdateDashboard changes the title when Title is not "", and moves the
// dashboard when After or GroupID is not nil. GroupID nil keeps its
// group; 0 takes it out as a group of one (its own id; one already alone
// keeps the id it has); G makes it a tab of G. Sidebar and ProjectTab
// set the placement flags instead, in a call of their own (setPlacement).
type UpdateDashboard struct {
	ID      int64
	Title   string
	GroupID *int64
	After   *int64
	// WholeGroup renames ID's group with Title instead of the dashboard
	// (D5); it takes no After or GroupID.
	WholeGroup bool
	Sidebar    *bool // the whole group in or out of the sidebar; allowed on a built-in (spec 2026-10-05 D5, D7)
	ProjectTab *bool // a new project gets it as a tab; user dashboards only (spec 2026-10-05 D3)
}

// DuplicateDashboard copies dashboard ID: alone, or with WholeGroup its
// whole group. A single tab's copy is a group of one when GroupID is 0,
// as CreateDashboard's is, and otherwise a tab of group GroupID; a whole
// group's copy is always a new group, so GroupID must then be 0.
type DuplicateDashboard struct {
	ID         int64
	WholeGroup bool
	GroupID    int64
}

// View is a viewer's selection on a dashboard: each part only when the
// dashboard has that switcher, From and To only with Range "custom".
type View struct {
	DashboardID, ProjectID int64
	Range, From, To        string
}

// CreateDashboard creates a user dashboard with its widgets, all or
// nothing: every widget is checked before anything is written.
func (s *Service) CreateDashboard(ctx context.Context, actor string, in CreateDashboard) (DashboardDetail, error) {
	title, err := checkName("title", in.Title)
	if err != nil {
		return DashboardDetail{}, err
	}
	in.Title = title
	rng := in.Range
	if rng == "" {
		rng = "7d"
	}
	if err := checkPreset(rng); err != nil {
		return DashboardDetail{}, err
	}
	if rng == "custom" {
		return DashboardDetail{}, store.Refuse(store.ErrInvalid, "create with a preset; the viewer picks custom dates")
	}
	comps, err := s.components(ctx)
	if err != nil {
		return DashboardDetail{}, err
	}
	ws, err := buildWidgets(comps, in.Widgets, map[string]bool{})
	if err != nil {
		return DashboardDetail{}, err
	}
	keys, err := sortkey.Spread("", "", len(ws))
	if err != nil {
		return DashboardDetail{}, err
	}
	for i := range ws {
		if err := s.validateWidget(ctx, comps, ws[i]); err != nil {
			return DashboardDetail{}, inWidget(ws[i].Name, err)
		}
		ws[i].SortKey = keys[i]
	}
	var id int64
	err = s.placeDashboards(func() error {
		o, err := s.readOrder(ctx)
		if err != nil {
			return err
		}
		var key string
		if in.GroupID == 0 {
			key, err = o.keyAfterGroup(0, in.After)
		} else if err = refuseGroup(o, in.GroupID); err == nil {
			if err = refuseNewTab(o, in.GroupID); err == nil {
				key, err = o.keyInGroup(0, in.GroupID, in.After)
			}
		}
		if err != nil {
			return err
		}
		id, err = s.st.InsertDashboard(ctx,
			store.Dashboard{Owner: store.OwnerUser, Title: in.Title, SortKey: key, GroupID: in.GroupID, LastRange: rng, Sidebar: true},
			ws, store.AuditEntry{Actor: actor, Action: "dashboard.create"})
		return err
	})
	if err != nil {
		return DashboardDetail{}, err
	}
	return s.Dashboard(ctx, id)
}

// UpdateDashboard retitles and/or moves a user dashboard: among its
// group's tabs, with its whole group in the sidebar, into another group,
// or out of its group (spec decisions 6 and 7). Sidebar or ProjectTab
// sets the placement flags instead (setPlacement).
func (s *Service) UpdateDashboard(ctx context.Context, actor string, in UpdateDashboard) (DashboardInfo, error) {
	if in.Sidebar != nil || in.ProjectTab != nil {
		return s.setPlacement(ctx, actor, in)
	}
	if in.WholeGroup {
		return s.renameGroup(ctx, actor, in)
	}
	d, err := s.editableDashboard(ctx, in.ID)
	if err != nil {
		return DashboardInfo{}, err
	}
	if in.Title == "" && in.After == nil && in.GroupID == nil {
		return DashboardInfo{}, store.Refuse(store.ErrInvalid, "nothing to update; give title, after or group_id")
	}
	if in.Title != "" {
		t, err := checkName("title", in.Title)
		if err != nil {
			return DashboardInfo{}, err
		}
		in.Title = t
	}
	a := store.AuditEntry{Actor: actor, Action: "dashboard.update"}
	err = s.placeDashboards(func() error {
		o, err := s.readOrder(ctx)
		if err != nil {
			return err
		}
		row, ok := o.find(d.ID)
		if !ok {
			return store.Refuse(store.ErrNotFound, "dashboard %d: not found", d.ID)
		}
		if in.Title != "" {
			row.Title = in.Title
		}
		return s.placeDashboard(ctx, o, row, in, a)
	})
	if err != nil {
		return DashboardInfo{}, err
	}
	d, err = s.st.GetDashboard(ctx, d.ID)
	return dashboardInfo(d), err
}

// setPlacement writes the placement flags (spec 2026-10-05 D3, D5, D7):
// sidebar for every member of the dashboard's group, archived ones
// included so a restored tab comes back with its group's flag, and
// project_tab for a user dashboard alone. A user group leaving the
// sidebar must keep a way in: each live member needs a project tab, or
// it would be unreachable. WholeGroup is ignored, since sidebar always
// applies to the whole group. The group is read and written under
// placeMu, so no move changes its membership in between.
func (s *Service) setPlacement(ctx context.Context, actor string, in UpdateDashboard) (DashboardInfo, error) {
	if in.Title != "" || in.After != nil || in.GroupID != nil {
		return DashboardInfo{}, store.Refuse(store.ErrInvalid, "sidebar and project_tab go on their own; give title, after or group_id in another call")
	}
	var d store.Dashboard
	err := s.placeDashboards(func() error {
		var err error
		if d, err = s.st.GetDashboard(ctx, in.ID); err != nil {
			return err
		}
		if d.ArchivedAt != "" {
			return store.Refuse(store.ErrInvalid, "dashboard %d is archived; restore_dashboard first", d.ID)
		}
		system := d.Owner == store.OwnerSystem
		if in.ProjectTab != nil && system {
			return store.Refuse(store.ErrInvalid, "project_tab of a built-in dashboard is the release's")
		}
		if in.Sidebar != nil {
			all, err := s.st.ListDashboards(ctx)
			if err != nil {
				return err
			}
			var ids []int64
			for _, m := range all {
				if m.Owner != d.Owner || m.GroupID != d.GroupID {
					continue
				}
				if !*in.Sidebar && !system && m.ArchivedAt == "" {
					if err := s.refuseUnreachable(ctx, m.ID, 0); err != nil {
						return err
					}
				}
				ids = append(ids, m.ID)
			}
			action := "dashboard.sidebar.show"
			if !*in.Sidebar {
				action = "dashboard.sidebar.hide"
			}
			if err := s.st.SetDashboardsSidebar(ctx, ids, *in.Sidebar, store.AuditEntry{Actor: actor, Action: action}); err != nil {
				return err
			}
		}
		if in.ProjectTab != nil {
			return s.st.SetDashboardProjectTab(ctx, d.ID, *in.ProjectTab, store.AuditEntry{Actor: actor, Action: "dashboard.project_tab"})
		}
		return nil
	})
	if err != nil {
		return DashboardInfo{}, err
	}
	d, err = s.st.GetDashboard(ctx, d.ID)
	return dashboardInfo(d), err
}

// renameGroup names id's group (spec 2026-10-04 D5). The title is
// required: a name is never cleared, only replaced. The group id is read
// inside the placement mutex, so a handover running beside it can't
// leave the name on the group's old number.
func (s *Service) renameGroup(ctx context.Context, actor string, in UpdateDashboard) (DashboardInfo, error) {
	if in.After != nil || in.GroupID != nil {
		return DashboardInfo{}, store.Refuse(store.ErrInvalid, "whole_group renames the group; it takes no after or group_id")
	}
	title, err := checkName("group title", in.Title)
	if err != nil {
		return DashboardInfo{}, err
	}
	d, err := s.editableDashboard(ctx, in.ID)
	if err != nil {
		return DashboardInfo{}, err
	}
	err = s.placeDashboards(func() error {
		o, err := s.readOrder(ctx)
		if err != nil {
			return err
		}
		row, ok := o.find(d.ID)
		if !ok {
			return store.Refuse(store.ErrNotFound, "dashboard %d: not found", d.ID)
		}
		return s.st.SetGroupTitle(ctx, row.GroupID, title, store.AuditEntry{
			Actor: actor, Action: "dashboard.group.rename", Detail: fmt.Sprintf("dashboard/%d", d.ID)})
	})
	if err != nil {
		return DashboardInfo{}, err
	}
	d, err = s.st.GetDashboard(ctx, d.ID)
	return dashboardInfo(d), err
}

// placeDashboard writes row (already retitled if asked) at the place in
// asks for, computed over o, the user order read for this attempt.
func (s *Service) placeDashboard(ctx context.Context, o order, row store.Dashboard, in UpdateDashboard, a store.AuditEntry) error {
	switch {
	case in.GroupID != nil && *in.GroupID == 0:
		return s.leaveGroup(ctx, o, row, in.After, a)

	case in.GroupID != nil:
		g := *in.GroupID
		if err := refuseGroup(o, g); err != nil {
			return err
		}
		if in.After != nil && *in.After == row.ID && row.GroupID == g {
			break // after itself in its own group: stays where it is
		}
		key, err := o.keyInGroup(row.ID, g, in.After)
		if err != nil {
			return err
		}
		if g != row.GroupID {
			// The row takes its new group's sidebar flag (spec
			// 2026-10-05 D3); out of the sidebar, it needs a project
			// tab to stay reachable (D5).
			sidebar := groupInSidebar(o, g)
			if !sidebar {
				ps, err := s.st.ListDashboardProjects(ctx, row.ID)
				if err != nil {
					return err
				}
				if len(ps) == 0 {
					return store.Refuse(store.ErrInvalid,
						"group %d is hidden from the sidebar; add dashboard %d to a project first, or show the group", g, row.ID)
				}
			}
			heirs := o.handOver(row)
			row.GroupID, row.SortKey, row.Sidebar = g, key, sidebar
			return s.writePlaced(ctx, row, heirs, a)
		}
		row.SortKey = key

	case in.After != nil && *in.After != row.ID: // after itself: stays where it is
		if x, ok := o.find(*in.After); ok && x.GroupID == row.GroupID {
			key, err := o.keyInGroup(row.ID, row.GroupID, in.After) // among its tabs
			if err != nil {
				return err
			}
			row.SortKey = key
			break
		}
		keys, err := o.moveGroup(row.GroupID, *in.After) // with its whole group
		if err != nil {
			return err
		}
		if keys == nil {
			break // already there: only the title, if any, below
		}
		// MoveDashboards audits under its first key's dashboard: name
		// the one the caller moved. The keys are parked before any is
		// written, so their order is free.
		i := slices.IndexFunc(keys, func(k store.DashboardKey) bool { return k.ID == row.ID })
		keys[0], keys[i] = keys[i], keys[0]
		if err := s.st.MoveDashboards(ctx, keys, store.GroupRekey{}, a); err != nil {
			return err
		}
		if in.Title == "" {
			return nil
		}
		// A new title is a second write (and a second audit row): the
		// move rewrites keys only. row takes its new key so the title
		// write keeps it.
		row.SortKey = keys[0].SortKey
	}
	return s.st.UpdateDashboard(ctx, row, a)
}

// leaveGroup takes row out of its group as a group of one (group_id: 0),
// placed by after in the sidebar, or right after the group it left.
//
// A dashboard already alone is a group of one: it keeps its group id and
// moves only if after says so. One with others takes its own id as its
// group id: that id is free, because a group whose id it was is handed
// to another member as row leaves (order.handOver, spec decision 2). It
// lands in the sidebar whatever its old group's flag, so it is never
// left unreachable on its own (spec 2026-10-05 D5).
func (s *Service) leaveGroup(ctx context.Context, o order, row store.Dashboard, after *int64, a store.AuditEntry) error {
	if after != nil && *after == row.ID {
		after = nil // after itself: no place of its own to name
	}
	others := o.without(row.ID).group(row.GroupID)
	var heirs []store.DashboardKey
	if len(others) > 0 {
		heirs = o.handOver(row)
		row.GroupID, row.Sidebar = row.ID, true
		if after == nil {
			after = &others[len(others)-1].ID
		}
	}
	if after != nil {
		key, err := o.keyAfterGroup(row.ID, after)
		if err != nil {
			return err
		}
		row.SortKey = key
	}
	return s.writePlaced(ctx, row, heirs, a)
}

// writePlaced writes row at its new group and key. When row leaves a
// group that used its id, heirs repoints the members left behind in the
// same transaction (MoveDashboards), so no moment exists where two
// groups share a number; a title change, if any, is a second write. The
// group's name, if it has one, moves with the heirs (D3): heirs is
// non-empty only when row founded the group, so its old number is row.ID.
func (s *Service) writePlaced(ctx context.Context, row store.Dashboard, heirs []store.DashboardKey, a store.AuditEntry) error {
	if len(heirs) == 0 {
		return s.st.UpdateDashboard(ctx, row, a)
	}
	// MoveDashboards audits under its first key's dashboard: row, the one
	// the caller moved.
	keys := append([]store.DashboardKey{{ID: row.ID, GroupID: row.GroupID, SortKey: row.SortKey}}, heirs...)
	rekey := store.GroupRekey{From: row.ID, To: heirs[0].GroupID}
	if err := s.st.MoveDashboards(ctx, keys, rekey, a); err != nil {
		return err
	}
	return s.st.UpdateDashboard(ctx, row, a)
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
		return s.duplicateGroup(ctx, actor, src)
	}
	return s.duplicateOne(ctx, actor, src, in.GroupID)
}

// duplicateOne is DuplicateDashboard for a single tab: a group of its own
// when group is 0, else a tab of group (a live user group).
func (s *Service) duplicateOne(ctx context.Context, actor string, src store.Dashboard, group int64) (DashboardDetail, error) {
	ws, err := s.copyLiveWidgets(ctx, src.ID)
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
			if err = refuseNewTab(o, group); err == nil {
				copyOf.GroupID = group
				copyOf.SortKey, err = o.keyInGroup(0, group, &src.ID)
			}
		default:
			if err = refuseGroup(o, group); err == nil {
				if err = refuseNewTab(o, group); err == nil {
					copyOf.GroupID = group
					copyOf.SortKey, err = o.keyInGroup(0, group, nil)
				}
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
func (s *Service) duplicateGroup(ctx context.Context, actor string, src store.Dashboard) (DashboardDetail, error) {
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
			if wss[i], err = s.copyLiveWidgets(ctx, m.ID); err != nil {
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
// still copies, faithfully.
func (s *Service) copyLiveWidgets(ctx context.Context, dashboardID int64) ([]store.Widget, error) {
	all, err := s.st.ListWidgets(ctx, dashboardID)
	if err != nil {
		return nil, err
	}
	var ws []store.Widget
	for _, w := range all {
		if w.ArchivedAt == "" {
			ws = append(ws, freshCopy(w))
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

func (s *Service) ArchiveDashboard(ctx context.Context, actor string, id int64, wholeGroup bool) error {
	return s.setDashboardArchived(ctx, actor, id, true, wholeGroup)
}

func (s *Service) RestoreDashboard(ctx context.Context, actor string, id int64, wholeGroup bool) error {
	return s.setDashboardArchived(ctx, actor, id, false, wholeGroup)
}

// setDashboardArchived archives or restores id, or (wholeGroup) every
// live member (archiving) or archived member (restoring) of its group,
// in one call to the store (D12–D14). A built-in is never archived or
// restored, alone or with its group: hiding one is sidebar = false
// (spec 2026-10-05 D5). A restore that would bring back a dashboard out
// of the sidebar and on no project's tabs first puts its whole group
// back in the sidebar (showIfUnreachable). It runs under placeMu, like
// setPlacement, so a hide can't check the live members while one is
// being restored.
func (s *Service) setDashboardArchived(ctx context.Context, actor string, id int64, archived, wholeGroup bool) error {
	d, err := s.st.GetDashboard(ctx, id)
	if err != nil {
		return err
	}
	if d.Owner == store.OwnerSystem {
		return store.Refuse(store.ErrInvalid,
			"dashboard %d is a built-in dashboard and is never archived; update_dashboard {sidebar: false} takes its group out of the sidebar", d.ID)
	}
	return s.placeDashboards(func() error {
		all, err := s.st.ListDashboards(ctx)
		if err != nil {
			return err
		}
		cur := d // re-read under placeMu: a move may have changed its group
		for _, m := range all {
			if m.ID == id {
				cur = m
			}
		}
		var members, targets []store.Dashboard
		for _, m := range all {
			if m.Owner != cur.Owner || m.GroupID != cur.GroupID {
				continue
			}
			members = append(members, m)
			if wholeGroup && (m.ArchivedAt == "") == archived || !wholeGroup && m.ID == id {
				targets = append(targets, m)
			}
		}
		if !archived {
			if err := s.showIfUnreachable(ctx, actor, members, targets); err != nil {
				return err
			}
		}
		ids := make([]int64, 0, len(targets))
		for _, m := range targets {
			ids = append(ids, m.ID)
		}
		if !wholeGroup && len(ids) == 0 {
			ids = []int64{id} // purged since the first read: the store answers for it
		}
		return s.st.SetDashboardsArchived(ctx, ids, archived,
			store.AuditEntry{Actor: actor, Action: archiveAction("dashboard", archived)})
	})
}

// showIfUnreachable puts every member of a group back in the sidebar when
// one of restoring, the dashboards about to be restored, is out of it and
// on no project's tabs (spec 2026-10-05 D5), so it is reachable and the
// group's tabs still share one flag (D3). It writes before the restore:
// a group shown early is never unreachable, a dashboard restored first
// could be.
func (s *Service) showIfUnreachable(ctx context.Context, actor string, members, restoring []store.Dashboard) error {
	for _, m := range restoring {
		if m.Sidebar {
			continue
		}
		ps, err := s.st.ListDashboardProjects(ctx, m.ID)
		if err != nil {
			return err
		}
		if len(ps) > 0 {
			continue
		}
		ids := make([]int64, 0, len(members))
		for _, x := range members {
			ids = append(ids, x.ID)
		}
		return s.st.SetDashboardsSidebar(ctx, ids, true, store.AuditEntry{Actor: actor, Action: "dashboard.sidebar.show"})
	}
	return nil
}

// SetView stores a viewer's selection. Which parts it takes follows from
// the dashboard's switchers, i.e. what its live widgets read; the parts
// it has no switcher for keep their stored values. Viewer state, not
// definition: allowed on system dashboards, and not audited.
func (s *Service) SetView(ctx context.Context, in View) error {
	if in.ProjectID < 0 {
		return store.Refuse(store.ErrInvalid, "project_id must be a positive id")
	}
	d, err := s.st.GetDashboard(ctx, in.DashboardID)
	if err != nil {
		return err
	}
	ws, err := s.st.ListWidgets(ctx, d.ID)
	if err != nil {
		return err
	}
	var hasProject, hasRange bool
	for _, w := range ws {
		if w.ArchivedAt == "" {
			p, r := s.follows(w)
			hasProject, hasRange = hasProject || p, hasRange || r
		}
	}
	switch {
	case hasProject && in.ProjectID == 0:
		return store.Refuse(store.ErrInvalid, "project_id is required: dashboard %d has a project switcher", d.ID)
	case !hasProject && in.ProjectID != 0:
		return store.Refuse(store.ErrInvalid, "dashboard %d has no project switcher; omit project_id", d.ID)
	case hasProject:
		d.LastProjectID = in.ProjectID
	}
	switch {
	case hasRange && in.Range == "":
		return store.Refuse(store.ErrInvalid, "range is required: dashboard %d has a range switcher", d.ID)
	case !hasRange && (in.Range != "" || in.From != "" || in.To != ""):
		return store.Refuse(store.ErrInvalid, "dashboard %d has no range switcher; omit range, from and to", d.ID)
	case hasRange:
		if err := checkPreset(in.Range); err != nil {
			return err
		}
		if in.Range == "custom" {
			if err := checkDates(in.From, in.To); err != nil {
				return err
			}
		} else if in.From != "" || in.To != "" {
			return store.Refuse(store.ErrInvalid, "from and to go only with range custom")
		}
		d.LastRange, d.LastFrom, d.LastTo = in.Range, in.From, in.To
	}
	return s.st.SetDashboardView(ctx, d)
}

// checkPreset refuses a range outside Presets.
func checkPreset(r string) error {
	if !slices.Contains(Presets, r) {
		return store.Refuse(store.ErrInvalid, "range must be one of %s", strings.Join(Presets, ", "))
	}
	return nil
}

// checkDates refuses a custom range that is not two days, from ≤ to, at
// most 365 days apart.
func checkDates(from, to string) error {
	f, errFrom := time.Parse(time.DateOnly, from)
	t, errTo := time.Parse(time.DateOnly, to)
	if errFrom != nil || errTo != nil {
		return store.Refuse(store.ErrInvalid, "from and to are days, YYYY-MM-DD")
	}
	if f.After(t) {
		return store.Refuse(store.ErrInvalid, "from %s is after to %s", from, to)
	}
	if t.Sub(f) > 365*24*time.Hour {
		return store.Refuse(store.ErrInvalid, "from %s to %s spans more than 365 days", from, to)
	}
	return nil
}

// refuseSystem refuses a write to a system dashboard: those change only
// through the release's migrator (D11).
func refuseSystem(d store.Dashboard) error {
	if d.Owner == store.OwnerSystem {
		return store.Refuse(store.ErrInvalid,
			"dashboard %d is a system dashboard and changes only with a release; duplicate_dashboard makes an editable copy", d.ID)
	}
	return nil
}

// editableDashboard loads dashboard id for a write that changes it or
// what it holds: refused on a system or an archived dashboard.
func (s *Service) editableDashboard(ctx context.Context, id int64) (store.Dashboard, error) {
	d, err := s.st.GetDashboard(ctx, id)
	if err != nil {
		return store.Dashboard{}, err
	}
	if err := refuseSystem(d); err != nil {
		return store.Dashboard{}, err
	}
	if d.ArchivedAt != "" {
		return store.Dashboard{}, store.Refuse(store.ErrInvalid, "dashboard %d is archived; restore_dashboard first", id)
	}
	return d, nil
}

func archiveAction(kind string, archived bool) string {
	if archived {
		return kind + ".archive"
	}
	return kind + ".restore"
}
