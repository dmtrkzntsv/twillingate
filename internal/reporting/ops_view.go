package reporting

import (
	"context"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// View is a viewer's selection on a dashboard: the project, which only a
// dashboard with a project switcher stores. The range is the web app's
// alone, kept in the browser for every dashboard at once.
type View struct {
	DashboardID, ProjectID int64
}

// SetView stores a viewer's project. Viewer state, not definition: allowed
// on system dashboards, and not audited.
func (s *Service) SetView(ctx context.Context, in View) error {
	if in.ProjectID <= 0 {
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
	hasProject := false
	for _, w := range ws {
		if w.ArchivedAt == "" {
			p, _ := s.follows(w)
			hasProject = hasProject || p
		}
	}
	if !hasProject {
		return store.Refuse(store.ErrInvalid, "dashboard %d has no project switcher", d.ID)
	}
	d.LastProjectID = in.ProjectID
	return s.st.SetDashboardView(ctx, d)
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
