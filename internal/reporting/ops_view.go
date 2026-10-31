package reporting

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// View is a viewer's selection on a dashboard: each part only when the
// dashboard has that switcher, From and To only with Range "custom".
type View struct {
	DashboardID, ProjectID int64
	Range, From, To        string
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
			if _, _, err := checkDates(in.From, in.To); err != nil {
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
// most 365 days apart, and returns the two days.
func checkDates(from, to string) (civil.Date, civil.Date, error) {
	f, errFrom := civil.Parse(from)
	t, errTo := civil.Parse(to)
	if errFrom != nil || errTo != nil {
		return civil.Date{}, civil.Date{}, store.Refuse(store.ErrInvalid, "from and to are days, YYYY-MM-DD")
	}
	if t.Before(f) {
		return civil.Date{}, civil.Date{}, store.Refuse(store.ErrInvalid, "from %s is after to %s", from, to)
	}
	if t.Time().Sub(f.Time()) > 365*24*time.Hour {
		return civil.Date{}, civil.Date{}, store.Refuse(store.ErrInvalid, "from %s to %s spans more than 365 days", from, to)
	}
	return f, t, nil
}
