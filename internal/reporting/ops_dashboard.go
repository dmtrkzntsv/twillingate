package reporting

import (
	"context"
	"fmt"

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
// keeps the id it has); G makes it a tab of G. Sidebar sets a built-in
// group's placement instead, in a call of its own (setPlacement).
type UpdateDashboard struct {
	ID      int64
	Title   string
	GroupID *int64
	After   *int64
	// WholeGroup renames ID's group with Title instead of the dashboard
	// (D5); it takes no After or GroupID.
	WholeGroup bool
	Sidebar    *bool // the whole group in or out of the sidebar; built-in dashboards only, refused on your own (spec 2026-10-05 D5, D7)
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
			key, err = o.keyInGroup(0, in.GroupID, in.After)
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
// or out of its group (spec decisions 6 and 7). Sidebar puts a built-in
// group in or out of the sidebar instead (setPlacement).
func (s *Service) UpdateDashboard(ctx context.Context, actor string, in UpdateDashboard) (DashboardInfo, error) {
	if in.Sidebar != nil {
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

// setPlacement puts a built-in dashboard's whole group in or out of the
// sidebar (spec 2026-10-05 D3, D5, D7), archived members included (only
// an older binary archives one) so its tabs share the flag. Your own
// dashboards are always in the sidebar, so it is refused on one.
// WholeGroup is ignored, since sidebar always applies to the whole group.
// A built-in group's membership changes only with a release, so no lock
// is needed between the read and the write.
func (s *Service) setPlacement(ctx context.Context, actor string, in UpdateDashboard) (DashboardInfo, error) {
	if in.Title != "" || in.After != nil || in.GroupID != nil {
		return DashboardInfo{}, store.Refuse(store.ErrInvalid, "sidebar goes on its own; give title, after or group_id in another call")
	}
	d, err := s.st.GetDashboard(ctx, in.ID)
	if err != nil {
		return DashboardInfo{}, err
	}
	if d.Owner != store.OwnerSystem {
		return DashboardInfo{}, store.Refuse(store.ErrInvalid,
			"dashboard %d is your own: your own dashboards are always in the sidebar; archive_dashboard takes one away", d.ID)
	}
	if d.ArchivedAt != "" {
		return DashboardInfo{}, store.Refuse(store.ErrInvalid, "dashboard %d is archived; restore_dashboard first", d.ID)
	}
	all, err := s.st.ListDashboards(ctx)
	if err != nil {
		return DashboardInfo{}, err
	}
	var ids []int64
	for _, m := range all {
		if m.Owner == d.Owner && m.GroupID == d.GroupID {
			ids = append(ids, m.ID)
		}
	}
	action := "dashboard.sidebar.show"
	if !*in.Sidebar {
		action = "dashboard.sidebar.hide"
	}
	if err := s.st.SetDashboardsSidebar(ctx, ids, *in.Sidebar, store.AuditEntry{Actor: actor, Action: action}); err != nil {
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
// (spec 2026-10-05 D5).
func (s *Service) setDashboardArchived(ctx context.Context, actor string, id int64, archived, wholeGroup bool) error {
	d, err := s.st.GetDashboard(ctx, id)
	if err != nil {
		return err
	}
	if d.Owner == store.OwnerSystem {
		return store.Refuse(store.ErrInvalid,
			"dashboard %d is a built-in dashboard and is never archived; update_dashboard {sidebar: false} takes its group out of the sidebar", d.ID)
	}
	ids := []int64{id}
	if wholeGroup {
		all, err := s.st.ListDashboards(ctx)
		if err != nil {
			return err
		}
		ids = nil
		for _, m := range all {
			if m.Owner == d.Owner && m.GroupID == d.GroupID && (m.ArchivedAt == "") == archived {
				ids = append(ids, m.ID)
			}
		}
	}
	return s.st.SetDashboardsArchived(ctx, ids, archived,
		store.AuditEntry{Actor: actor, Action: archiveAction("dashboard", archived)})
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
