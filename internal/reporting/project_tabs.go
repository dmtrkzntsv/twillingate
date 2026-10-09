// Project tabs (spec 2026-10-05): the dashboards a project page shows,
// Setup aside (the web app's own tab, not a row). Every tab, built-in or
// your own, is ordered per project by its row (spec 2026-10-08 D1).
package reporting

import (
	"cmp"
	"context"
	"slices"

	"github.com/dmtrkzntsv/twillingate/internal/shared/sortkey"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// ProjectTab is one tab of a project page (spec 2026-10-05 D1), in shown order.
type ProjectTab struct {
	ID      int64  `json:"dashboard_id"`
	Title   string `json:"title"`
	Owner   string `json:"owner"`
	GroupID int64  `json:"group_id"`
}

// AddProjectTab gives project ProjectID a tab for dashboard DashboardID,
// built-in or the user's.
type AddProjectTab struct {
	ProjectID, DashboardID int64
	After                  *int64 // nil last among all tabs, 0 first, an id right after that tab
}

// MoveProjectTab places one of project ProjectID's tabs.
type MoveProjectTab struct {
	ProjectID, DashboardID int64
	After                  int64 // 0: first; otherwise a tab of this project
}

// ProjectTabs lists project projectID's tabs in shown order; never nil.
func (s *Service) ProjectTabs(ctx context.Context, projectID int64) ([]ProjectTab, error) {
	tabs, _, _, err := s.shownTabs(ctx, projectID)
	return tabs, err
}

// AddProjectTab adds a live dashboard to a project's tabs.
func (s *Service) AddProjectTab(ctx context.Context, actor string, in AddProjectTab) ([]ProjectTab, error) {
	err := s.placeTabs(func() error {
		_, rows, ds, err := s.shownTabs(ctx, in.ProjectID)
		if err != nil {
			return err
		}
		d, ok := ds[in.DashboardID]
		if !ok {
			return store.Refuse(store.ErrNotFound, "dashboard %d: not found", in.DashboardID)
		}
		if d.ArchivedAt != "" {
			return store.Refuse(store.ErrInvalid, "dashboard %d is archived; restore_dashboard first", d.ID)
		}
		key, err := s.tabKey(ctx, actor, in.ProjectID, rows, ds, in.After)
		if err != nil {
			return err
		}
		// A tab the project already has is the store's ErrConflict.
		return s.st.InsertProjectTab(ctx,
			store.ProjectTabRow{ProjectID: in.ProjectID, DashboardID: d.ID, SortKey: key},
			store.AuditEntry{Actor: actor, Action: "project.tab.add"})
	})
	if err != nil {
		return nil, err
	}
	return s.ProjectTabs(ctx, in.ProjectID)
}

// RemoveProjectTab takes a dashboard off a project's tabs. Your own
// dashboard may lose its last tab: it is always in the sidebar (spec
// 2026-10-05 D5).
func (s *Service) RemoveProjectTab(ctx context.Context, actor string, projectID, dashboardID int64) ([]ProjectTab, error) {
	err := s.placeTabs(func() error {
		if _, err := s.st.ListProjectTabs(ctx, projectID); err != nil {
			return err // an unknown project is named as such
		}
		return s.st.DeleteProjectTab(ctx, projectID, dashboardID,
			store.AuditEntry{Actor: actor, Action: "project.tab.remove"})
	})
	if err != nil {
		return nil, err
	}
	return s.ProjectTabs(ctx, projectID)
}

// MoveProjectTab places one of a project's tabs after another (0: first).
func (s *Service) MoveProjectTab(ctx context.Context, actor string, in MoveProjectTab) ([]ProjectTab, error) {
	err := s.placeTabs(func() error {
		_, rows, ds, err := s.shownTabs(ctx, in.ProjectID)
		if err != nil {
			return err
		}
		d, ok := ds[in.DashboardID]
		if !ok {
			return store.Refuse(store.ErrNotFound, "dashboard %d: not found", in.DashboardID)
		}
		isMoving := func(r store.ProjectTabRow) bool { return r.DashboardID == d.ID }
		if !slices.ContainsFunc(rows, isMoving) {
			return store.Refuse(store.ErrNotFound, "project %d has no tab for dashboard %d", in.ProjectID, d.ID)
		}
		if in.After == d.ID {
			return nil
		}
		key, err := s.tabKey(ctx, actor, in.ProjectID, slices.DeleteFunc(rows, isMoving), ds, &in.After)
		if err != nil {
			return err
		}
		return s.st.MoveProjectTab(ctx,
			store.ProjectTabRow{ProjectID: in.ProjectID, DashboardID: d.ID, SortKey: key},
			store.AuditEntry{Actor: actor, Action: "project.tab.move"})
	})
	if err != nil {
		return nil, err
	}
	return s.ProjectTabs(ctx, in.ProjectID)
}

// placeTabs runs place, a tab write and the reads it decides on, under
// placeMu, so tab writes in this process don't interleave: tabKey's
// respread rewrites every row of a project, and a removal or
// another placement between its read and its writes would make it fail
// or place from a stale order. Unlike placeDashboards it does not retry
// on ErrConflict: a tab row's only conflict is a duplicate tab, which a
// retry can't cure and lostDashboardRace would misname.
func (s *Service) placeTabs(place func() error) error {
	s.placeMu.Lock()
	defer s.placeMu.Unlock()
	return place()
}

// shownTabs joins rows to dashboards in row-key order (ties by dashboard
// id); archived dashboards are left out of the tabs (their rows kept, so a
// restore brings the tab back where it was). It also returns every row in
// that order, archived ones included at their place so key arithmetic and
// a respread keep it, and the dashboards by id.
func (s *Service) shownTabs(ctx context.Context, projectID int64) ([]ProjectTab, []store.ProjectTabRow, map[int64]store.Dashboard, error) {
	rows, err := s.st.ListProjectTabs(ctx, projectID)
	if err != nil {
		return nil, nil, nil, err
	}
	all, err := s.st.ListDashboards(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	ds := make(map[int64]store.Dashboard, len(all))
	for _, d := range all {
		ds[d.ID] = d
	}
	slices.SortStableFunc(rows, func(a, b store.ProjectTabRow) int {
		return cmp.Or(cmp.Compare(a.SortKey, b.SortKey), cmp.Compare(a.DashboardID, b.DashboardID))
	})
	tabs := []ProjectTab{}
	for _, r := range rows {
		d := ds[r.DashboardID]
		if d.ArchivedAt == "" {
			tabs = append(tabs, ProjectTab{ID: d.ID, Title: d.Title, Owner: d.Owner, GroupID: d.GroupID})
		}
	}
	return tabs, rows, ds, nil
}

// tabKey returns a key for a tab of project projectID placed after
// `after` among rows, the project's tab rows in shown order without the
// tab being placed: nil last, 0 first, an id right after that live tab.
// Neighbours sharing a key (seeding copies dashboards.sort_key, another
// namespace) leave no key between them, so rows is first respread over
// fresh keys in its current order.
func (s *Service) tabKey(ctx context.Context, actor string, projectID int64, rows []store.ProjectTabRow, ds map[int64]store.Dashboard, after *int64) (string, error) {
	i := len(rows) // the tab goes before rows[i]
	if after != nil {
		if *after == 0 {
			i = 0
		} else {
			i = slices.IndexFunc(rows, func(r store.ProjectTabRow) bool { return r.DashboardID == *after })
			if i < 0 || ds[*after].ArchivedAt != "" {
				return "", store.Refuse(store.ErrInvalid, "after %d is not one of project %d's tabs", *after, projectID)
			}
			i++
		}
	}
	if i > 0 && i < len(rows) && rows[i-1].SortKey == rows[i].SortKey {
		keys, err := sortkey.Spread("", "", len(rows))
		if err != nil {
			return "", err
		}
		for j := range rows {
			rows[j].SortKey = keys[j]
			if err := s.st.MoveProjectTab(ctx, rows[j], store.AuditEntry{Actor: actor, Action: "project.tab.move"}); err != nil {
				return "", err
			}
		}
	}
	var prev, next string
	if i > 0 {
		prev = rows[i-1].SortKey
	}
	if i < len(rows) {
		next = rows[i].SortKey
	}
	return sortkey.Between(prev, next)
}
