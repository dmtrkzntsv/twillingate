// Project tabs (spec 2026-10-05): the dashboards a project page shows,
// Setup aside (the web app's own tab, not a row). Built-in tabs keep the
// release's order and can only be removed and added back; the user's own
// follow, ordered per project.
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

// AddProjectTab gives project ProjectID a tab for dashboard DashboardID.
// A built-in goes back to its own place, so After is refused for one.
type AddProjectTab struct {
	ProjectID, DashboardID int64
	After                  *int64 // user tabs only: nil last among the user's tabs, 0 first among them, an id right after that user tab
}

// MoveProjectTab places one of the user's tabs of project ProjectID.
type MoveProjectTab struct {
	ProjectID, DashboardID int64
	After                  int64 // 0: first among the user's tabs; otherwise a user tab of this project
}

// ProjectTabs lists project projectID's tabs in shown order; never nil.
func (s *Service) ProjectTabs(ctx context.Context, projectID int64) ([]ProjectTab, error) {
	tabs, _, _, err := s.shownTabs(ctx, projectID)
	return tabs, err
}

// AddProjectTab adds a live dashboard to a project's tabs.
func (s *Service) AddProjectTab(ctx context.Context, actor string, in AddProjectTab) ([]ProjectTab, error) {
	err := s.placeTabs(func() error {
		_, own, ds, err := s.shownTabs(ctx, in.ProjectID)
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
		key := d.SortKey // a built-in's row key is unused: it sorts by this
		if d.Owner == store.OwnerSystem {
			if in.After != nil {
				return store.Refuse(store.ErrInvalid, "a built-in tab goes back to its own place; drop after")
			}
		} else if key, err = s.ownTabKey(ctx, actor, in.ProjectID, own, ds, in.After); err != nil {
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

// MoveProjectTab places one of the user's tabs after another (0: first
// among them); built-in tabs keep the release's order.
func (s *Service) MoveProjectTab(ctx context.Context, actor string, in MoveProjectTab) ([]ProjectTab, error) {
	err := s.placeTabs(func() error {
		_, own, ds, err := s.shownTabs(ctx, in.ProjectID)
		if err != nil {
			return err
		}
		d, ok := ds[in.DashboardID]
		if !ok {
			return store.Refuse(store.ErrNotFound, "dashboard %d: not found", in.DashboardID)
		}
		if d.Owner == store.OwnerSystem {
			return store.Refuse(store.ErrInvalid, "built-in tabs keep the release's order")
		}
		isMoving := func(r store.ProjectTabRow) bool { return r.DashboardID == d.ID }
		if !slices.ContainsFunc(own, isMoving) {
			return store.Refuse(store.ErrNotFound, "project %d has no tab for dashboard %d", in.ProjectID, d.ID)
		}
		if in.After == d.ID {
			return nil
		}
		key, err := s.ownTabKey(ctx, actor, in.ProjectID, slices.DeleteFunc(own, isMoving), ds, &in.After)
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
// placeMu, so tab writes in this process don't interleave: ownTabKey's
// respread rewrites every user row of a project, and a removal or
// another placement between its read and its writes would make it fail
// or place from a stale order. Unlike placeDashboards it does not retry
// on ErrConflict: a tab row's only conflict is a duplicate tab, which a
// retry can't cure and lostDashboardRace would misname.
func (s *Service) placeTabs(place func() error) error {
	s.placeMu.Lock()
	defer s.placeMu.Unlock()
	return place()
}

// shownTabs joins rows to dashboards: built-ins by dashboard sort key,
// then the user's by row key; archived dashboards left out (their rows
// kept, so a restore brings the tab back where it was). It also returns
// the user rows in that order, archived ones included at their place so
// key arithmetic and a respread keep it, and the dashboards by id.
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
		da, db := ds[a.DashboardID], ds[b.DashboardID]
		sa, sb := da.Owner == store.OwnerSystem, db.Owner == store.OwnerSystem
		switch {
		case sa && !sb:
			return -1
		case !sa && sb:
			return 1
		case sa:
			return cmp.Or(cmp.Compare(da.SortKey, db.SortKey), cmp.Compare(da.ID, db.ID))
		}
		return cmp.Or(cmp.Compare(a.SortKey, b.SortKey), cmp.Compare(a.DashboardID, b.DashboardID))
	})
	tabs := []ProjectTab{}
	var own []store.ProjectTabRow
	for _, r := range rows {
		d := ds[r.DashboardID]
		if d.Owner == store.OwnerUser {
			own = append(own, r)
		}
		if d.ArchivedAt == "" {
			tabs = append(tabs, ProjectTab{ID: d.ID, Title: d.Title, Owner: d.Owner, GroupID: d.GroupID})
		}
	}
	return tabs, own, ds, nil
}

// ownTabKey returns a key for a user tab of project projectID placed
// after `after` among own, the project's user rows in shown order without
// the tab being placed: nil last, 0 first, an id right after that live
// user tab. Neighbours sharing a key (seeding copies dashboards.sort_key,
// another namespace) leave no key between them, so own is first respread
// over fresh keys in its current order.
func (s *Service) ownTabKey(ctx context.Context, actor string, projectID int64, own []store.ProjectTabRow, ds map[int64]store.Dashboard, after *int64) (string, error) {
	i := len(own) // the tab goes before own[i]
	if after != nil {
		if *after == 0 {
			i = 0
		} else {
			i = slices.IndexFunc(own, func(r store.ProjectTabRow) bool { return r.DashboardID == *after })
			if i < 0 || ds[*after].ArchivedAt != "" {
				return "", store.Refuse(store.ErrInvalid, "after %d is not one of project %d's own tabs", *after, projectID)
			}
			i++
		}
	}
	if i > 0 && i < len(own) && own[i-1].SortKey == own[i].SortKey {
		keys, err := sortkey.Spread("", "", len(own))
		if err != nil {
			return "", err
		}
		for j := range own {
			own[j].SortKey = keys[j]
			if err := s.st.MoveProjectTab(ctx, own[j], store.AuditEntry{Actor: actor, Action: "project.tab.move"}); err != nil {
				return "", err
			}
		}
	}
	var prev, next string
	if i > 0 {
		prev = own[i-1].SortKey
	}
	if i < len(own) {
		next = own[i].SortKey
	}
	return sortkey.Between(prev, next)
}
