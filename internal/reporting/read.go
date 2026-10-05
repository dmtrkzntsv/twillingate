package reporting

import (
	"context"
	"encoding/json"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// DashboardInfo is one dashboard as list_dashboards returns it: the row,
// its stored selection and its live widget count, without the widgets.
type DashboardInfo struct {
	ID         int64  `json:"dashboard_id"`
	Title      string `json:"title"`
	Owner      string `json:"owner"`
	GroupID    int64  `json:"group_id"`              // the group it is a tab of: its first dashboard's id, or a number reserved for one that left a group
	GroupTitle string `json:"group_title,omitempty"` // the group's name; omitted when it has none and the first live tab's title stands in
	ProjectID  int64  `json:"project_id,omitempty"`  // stored selection
	Range      string `json:"range,omitempty"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Widgets    int    `json:"widgets"` // live widgets
	ArchivedAt string `json:"archived_at,omitempty"`
}

// Dashboards is list_dashboards' answer: every dashboard in sidebar
// order (system first, each group by sort key), and the timezone days
// are grouped in.
type Dashboards struct {
	Timezone       string          `json:"timezone"` // "UTC" (D19)
	Dashboards     []DashboardInfo `json:"dashboards"`
	PurgeAfterDays int             `json:"purge_after_days,omitempty"` // RETENTION_ARCHIVED_DAYS; 0 (omitted): kept forever
	// AutoRefreshSeconds is how often the page reloads a dashboard whose
	// viewer turned auto-refresh on: the longer of REPORTING_CACHE_SECONDS
	// and REPORTING_REFRESH_SECONDS, so a reload never comes back with the
	// answer it already has. 0 (omitted, both off): no auto-refresh.
	AutoRefreshSeconds int        `json:"auto_refresh_seconds,omitempty"`
	Dev                bool       `json:"dev,omitempty"`    // set by reporting dev
	Errors             []DevError `json:"errors,omitempty"` // reporting dev only: directories that failed to load
}

// WidgetInfo is one widget as the API returns it. Component is nil when
// the component was removed from the code (D14).
type WidgetInfo struct {
	ID             int64           `json:"widget_id"`
	DashboardID    int64           `json:"dashboard_id"`
	Name           string          `json:"name"`
	Component      *string         `json:"component"` // null: removed from the code
	Title          string          `json:"title,omitempty"`
	Width          int             `json:"width"`
	Height         int             `json:"height"`
	Props          json.RawMessage `json:"props"`
	Source         Source          `json:"source"`
	FollowsProject bool            `json:"follows_project"`
	FollowsRange   bool            `json:"follows_range"`
	ArchivedAt     string          `json:"archived_at,omitempty"`
}

// DashboardDetail is get_dashboard's answer: the dashboard, which
// switchers its page shows, and its live widgets in order.
type DashboardDetail struct {
	DashboardInfo
	FollowsProject bool         `json:"follows_project"` // any live widget does: show the project switcher
	FollowsRange   bool         `json:"follows_range"`
	Tabs           []Tab        `json:"tabs"`    // the group's live members in order, the same from every member; always non-nil
	Widgets        []WidgetInfo `json:"widgets"` // live, in order; each carries width and height (the layout)
}

// Tab is one entry of a group's tab bar.
type Tab struct {
	ID    int64  `json:"dashboard_id"`
	Title string `json:"title"`
}

// ListedWidget is one list_widgets row: the widget, its dashboard, and
// its place there.
type ListedWidget struct {
	WidgetInfo
	Dashboard DashboardInfo `json:"dashboard"`
	Position  int           `json:"position"` // 1-based among the dashboard's widgets, archived included
}

// SourceTypes lists the registered source type names, sorted.
func (s *Service) SourceTypes() []string { return sortedSourceNames(s.sources) }

// Components lists the registered components, by name. Each row's parse
// is memoised keyed by the row itself: resolving a props schema is the
// costly part of every widget_data call, rows change only when a
// release's migration rewrites them (a changed row is a new key, so a
// stale parse is never served), and the map holds a handful of rows.
func (s *Service) Components(ctx context.Context) ([]Component, error) {
	rows, err := s.st.ListComponents(ctx)
	if err != nil {
		return nil, err
	}
	s.parsedMu.Lock()
	defer s.parsedMu.Unlock()
	if s.parsed == nil {
		s.parsed = map[store.Component]Component{}
	}
	out := make([]Component, 0, len(rows))
	for _, r := range rows {
		c, ok := s.parsed[r]
		if !ok {
			if c, err = fromRow(r); err != nil {
				return nil, err
			}
			s.parsed[r] = c
		}
		out = append(out, c)
	}
	return out, nil
}

// components is Components keyed by name: what one operation loads once
// and checks every widget it writes against.
func (s *Service) components(ctx context.Context) (map[string]Component, error) {
	cs, err := s.Components(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Component, len(cs))
	for _, c := range cs {
		out[c.Name] = c
	}
	return out, nil
}

// Dashboards lists every dashboard, archived ones included, in sidebar
// order.
func (s *Service) Dashboards(ctx context.Context) (Dashboards, error) {
	ds, err := s.st.ListDashboards(ctx)
	if err != nil {
		return Dashboards{}, err
	}
	out := Dashboards{
		Timezone: "UTC", Dashboards: make([]DashboardInfo, 0, len(ds)), PurgeAfterDays: s.archivedDays,
		AutoRefreshSeconds: int(max(s.cache.cacheAge, s.cache.refreshAge) / time.Second),
	}
	for _, d := range ds {
		out.Dashboards = append(out.Dashboards, dashboardInfo(d))
	}
	return out, nil
}

// Dashboard returns dashboard id with its group's tabs and its live
// widgets in order.
func (s *Service) Dashboard(ctx context.Context, id int64) (DashboardDetail, error) {
	d, err := s.st.GetDashboard(ctx, id)
	if err != nil {
		return DashboardDetail{}, err
	}
	ws, err := s.st.ListWidgets(ctx, id)
	if err != nil {
		return DashboardDetail{}, err
	}
	ds, err := s.st.ListDashboards(ctx)
	if err != nil {
		return DashboardDetail{}, err
	}
	out := DashboardDetail{DashboardInfo: dashboardInfo(d), Tabs: []Tab{}, Widgets: []WidgetInfo{}}
	// An archived dashboard is not a tab, but its detail still shows the
	// tabs of the group it belongs to.
	for _, x := range ds {
		if x.GroupID == d.GroupID && x.Owner == d.Owner && x.ArchivedAt == "" {
			out.Tabs = append(out.Tabs, Tab{ID: x.ID, Title: x.Title})
		}
	}
	for _, w := range ws {
		if w.ArchivedAt != "" {
			continue
		}
		info := s.widgetInfo(w)
		out.FollowsProject = out.FollowsProject || info.FollowsProject
		out.FollowsRange = out.FollowsRange || info.FollowsRange
		out.Widgets = append(out.Widgets, info)
	}
	return out, nil
}

// Widgets lists widgets, archived ones included, each with its dashboard
// and position: every widget when dashboardID is 0 and component "",
// otherwise only those on that dashboard and/or naming that component.
func (s *Service) Widgets(ctx context.Context, dashboardID int64, component string) ([]ListedWidget, error) {
	if dashboardID != 0 {
		if _, err := s.st.GetDashboard(ctx, dashboardID); err != nil {
			return nil, err
		}
	}
	ds, err := s.st.ListDashboards(ctx)
	if err != nil {
		return nil, err
	}
	dashboards := make(map[int64]DashboardInfo, len(ds))
	for _, d := range ds {
		dashboards[d.ID] = dashboardInfo(d)
	}
	ws, err := s.st.ListWidgets(ctx, dashboardID)
	if err != nil {
		return nil, err
	}
	out := []ListedWidget{}
	positions := map[int64]int{}
	for _, w := range ws { // by dashboard, then sort key: positions count up per dashboard
		positions[w.DashboardID]++
		if component != "" && w.Component != component {
			continue
		}
		out = append(out, ListedWidget{
			WidgetInfo: s.widgetInfo(w),
			Dashboard:  dashboards[w.DashboardID],
			Position:   positions[w.DashboardID],
		})
	}
	return out, nil
}

func dashboardInfo(d store.Dashboard) DashboardInfo {
	return DashboardInfo{
		ID: d.ID, Title: d.Title, Owner: d.Owner, GroupID: d.GroupID, GroupTitle: d.GroupTitle,
		ProjectID: d.LastProjectID, Range: d.LastRange, From: d.LastFrom, To: d.LastTo,
		Widgets: d.LiveWidgets, ArchivedAt: d.ArchivedAt,
	}
}

func (s *Service) widgetInfo(w store.Widget) WidgetInfo {
	info := WidgetInfo{
		ID: w.ID, DashboardID: w.DashboardID, Name: w.Name, Title: w.Title,
		Width: w.Width, Height: w.Height, Props: json.RawMessage(w.Props),
		Source:     Source{Type: w.SourceType, Content: w.Source},
		ArchivedAt: w.ArchivedAt,
	}
	if w.Component != "" {
		info.Component = &w.Component
	}
	if len(info.Props) == 0 {
		info.Props = json.RawMessage(`{}`)
	}
	info.FollowsProject, info.FollowsRange = s.follows(w)
	return info
}

// follows reports which switchers w's content reads; a source type this
// build does not register follows neither.
func (s *Service) follows(w store.Widget) (project, rng bool) {
	if st, ok := s.sources[w.SourceType]; ok {
		return st.Follows(w.Source)
	}
	return false, false
}
