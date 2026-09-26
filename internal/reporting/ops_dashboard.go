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
// (Deviation 1; keyAfter in place.go).

type CreateDashboard struct {
	Title, Range string
	After        *int64
	Widgets      []WidgetSpec
}

// UpdateDashboard changes the title when Title is not "", and moves the
// dashboard when After is not nil.
type UpdateDashboard struct {
	ID    int64
	Title string
	After *int64
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
	if strings.TrimSpace(in.Title) == "" {
		return DashboardDetail{}, store.Refuse(store.ErrInvalid, "title must not be empty")
	}
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
	err = retryConflict(func() error {
		key, err := s.dashboardKey(ctx, 0, in.After)
		if err != nil {
			return err
		}
		id, err = s.st.InsertDashboard(ctx,
			store.Dashboard{Owner: store.OwnerUser, Title: in.Title, SortKey: key, LastRange: rng},
			ws, store.AuditEntry{Actor: actor, Action: "dashboard.create"})
		return err
	}, lostDashboardRace)
	if err != nil {
		return DashboardDetail{}, err
	}
	return s.Dashboard(ctx, id)
}

// UpdateDashboard retitles and/or moves a user dashboard.
func (s *Service) UpdateDashboard(ctx context.Context, actor string, in UpdateDashboard) (DashboardInfo, error) {
	d, err := s.editableDashboard(ctx, in.ID)
	if err != nil {
		return DashboardInfo{}, err
	}
	if in.Title == "" && in.After == nil {
		return DashboardInfo{}, store.Refuse(store.ErrInvalid, "nothing to update; give title or after")
	}
	if in.Title != "" {
		if strings.TrimSpace(in.Title) == "" {
			return DashboardInfo{}, store.Refuse(store.ErrInvalid, "title must not be empty")
		}
		d.Title = in.Title
	}
	err = retryConflict(func() error {
		if in.After != nil && *in.After != d.ID { // after itself: stays where it is
			key, err := s.dashboardKey(ctx, d.ID, in.After)
			if err != nil {
				return err
			}
			d.SortKey = key
		}
		return s.st.UpdateDashboard(ctx, d, store.AuditEntry{Actor: actor, Action: "dashboard.update"})
	}, lostDashboardRace)
	if err != nil {
		return DashboardInfo{}, err
	}
	d, err = s.st.GetDashboard(ctx, d.ID)
	return dashboardInfo(d), err
}

// DuplicateDashboard makes a user copy of any dashboard, system ones
// included, placed last: its live widgets in order with fresh keys and
// the same content, and the same stored selection. The widgets are
// copied as they are, not re-checked, so the copy is faithful: a widget
// whose component was removed is copied too and still shows "component
// removed" there, until update_widget switches it. D15's no-copy rule is
// copy_widget's, which places one widget somewhere new.
func (s *Service) DuplicateDashboard(ctx context.Context, actor string, id int64) (DashboardDetail, error) {
	src, err := s.st.GetDashboard(ctx, id)
	if err != nil {
		return DashboardDetail{}, err
	}
	all, err := s.st.ListWidgets(ctx, id)
	if err != nil {
		return DashboardDetail{}, err
	}
	var ws []store.Widget
	for _, w := range all {
		if w.ArchivedAt == "" {
			ws = append(ws, freshCopy(w))
		}
	}
	keys, err := sortkey.Spread("", "", len(ws))
	if err != nil {
		return DashboardDetail{}, err
	}
	for i := range ws {
		ws[i].SortKey = keys[i]
	}
	copyOf := store.Dashboard{
		Owner: store.OwnerUser, Title: src.Title + " (copy)",
		LastProjectID: src.LastProjectID, LastRange: src.LastRange, LastFrom: src.LastFrom, LastTo: src.LastTo,
	}
	var newID int64
	err = retryConflict(func() error {
		key, err := s.dashboardKey(ctx, 0, nil)
		if err != nil {
			return err
		}
		copyOf.SortKey = key
		newID, err = s.st.InsertDashboard(ctx, copyOf, ws, store.AuditEntry{
			Actor: actor, Action: "dashboard.duplicate", Detail: fmt.Sprintf("from dashboard/%d", id)})
		return err
	}, lostDashboardRace)
	if err != nil {
		return DashboardDetail{}, err
	}
	return s.Dashboard(ctx, newID)
}

func (s *Service) ArchiveDashboard(ctx context.Context, actor string, id int64) error {
	return s.setDashboardArchived(ctx, actor, id, true)
}

func (s *Service) RestoreDashboard(ctx context.Context, actor string, id int64) error {
	return s.setDashboardArchived(ctx, actor, id, false)
}

func (s *Service) setDashboardArchived(ctx context.Context, actor string, id int64, archived bool) error {
	d, err := s.st.GetDashboard(ctx, id)
	if err != nil {
		return err
	}
	if err := refuseSystem(d); err != nil {
		return err
	}
	return s.st.SetDashboardArchived(ctx, id, archived,
		store.AuditEntry{Actor: actor, Action: archiveAction("dashboard", archived)})
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
