package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Reporting tools (spec 2026-09-25, D28–D30): thin adapters from each
// tool's input to one reporting.Service call. Validation, refusals and the
// audit entries are the service's; refusals are store.Refuse values, which
// writeError maps like any other.

type dashboardIn struct {
	DashboardID int64 `json:"dashboard_id" jsonschema:"dashboard id; list_dashboards names them"`
}

// dashboardGroupIn is archive_dashboard and restore_dashboard's input:
// the dashboard, and whether the operation acts on its whole group of
// tabs.
type dashboardGroupIn struct {
	DashboardID int64 `json:"dashboard_id" jsonschema:"dashboard id; list_dashboards names them"`
	WholeGroup  bool  `json:"whole_group,omitempty" jsonschema:"true acts on every dashboard in this dashboard's group (all its tabs)"`
}

// duplicateDashboardIn is duplicate_dashboard's input: dashboardGroupIn,
// plus the group a single tab's copy joins.
type duplicateDashboardIn struct {
	DashboardID int64 `json:"dashboard_id" jsonschema:"dashboard id; list_dashboards names them"`
	WholeGroup  bool  `json:"whole_group,omitempty" jsonschema:"true acts on every dashboard in this dashboard's group (all its tabs)"`
	GroupID     int64 `json:"group_id,omitempty" jsonschema:"the user group the copy joins as a tab: right after the source when it is the source's own group, last otherwise; omit for a dashboard of its own. Not with whole_group"`
}

type widgetIn struct {
	WidgetID int64 `json:"widget_id" jsonschema:"widget id; get_dashboard or list_widgets names them"`
}

type listComponentsOut struct {
	SourceTypes []string              `json:"source_types"`
	Components  []reporting.Component `json:"components"`
}

type listWidgetsIn struct {
	DashboardID int64  `json:"dashboard_id,omitempty" jsonschema:"only this dashboard's widgets"`
	Component   string `json:"component,omitempty" jsonschema:"only widgets using this component"`
}

type listWidgetsOut struct {
	Widgets []reporting.ListedWidget `json:"widgets"`
}

type widgetDataIn struct {
	WidgetID  int64  `json:"widget_id" jsonschema:"widget id"`
	ProjectID int64  `json:"project_id,omitempty" jsonschema:"required when the widget follows the project switcher (its SQL uses :project); ignored otherwise"`
	From      string `json:"from,omitempty" jsonschema:"YYYY-MM-DD; with to, required when the widget follows the range switcher (its SQL uses :from or :to); ignored otherwise"`
	To        string `json:"to,omitempty" jsonschema:"YYYY-MM-DD, at most 365 days after from; a day after today is clamped to today"`
	Fresh     bool   `json:"fresh,omitempty" jsonschema:"true reuses a cached result only up to REPORTING_REFRESH_SECONDS old instead of REPORTING_CACHE_SECONDS"`
	Filters   string `json:"filters,omitempty" jsonschema:"remote tables only (props.mode \"remote\"): a JSON list of {column, op, value}; op is =, !=, <, >, in or not in; in/not in take a list. See docs://reporting, Filtering and paging a table"`
	Sort      string `json:"sort,omitempty" jsonschema:"remote tables only: <column>:asc or <column>:desc; the whole result is sorted, not the page"`
	Distinct  string `json:"distinct,omitempty" jsonschema:"remote tables only: return [value, rows] for this column among rows matching the other filters, most frequent first"`
	Offset    int    `json:"offset,omitempty" jsonschema:"remote tables only: rows to skip"`
	Limit     int    `json:"limit,omitempty" jsonschema:"remote tables only: page size, 1 to CONSOLE_QUERY_MAX_ROWS (the default)"`
}

type createDashboardIn struct {
	Title   string                 `json:"title" jsonschema:"the dashboard's title, at least 2 characters (required)"`
	Range   string                 `json:"range,omitempty" jsonschema:"starting range: today, yesterday, 7d, 30d or 90d; default 7d"`
	GroupID int64                  `json:"group_id,omitempty" jsonschema:"add it as a tab of this group (a group_id from list_dashboards); after then names a tab of that group, 0 first; omit for a dashboard of its own"`
	After   *int64                 `json:"after,omitempty" jsonschema:"where the new dashboard goes. Without group_id: after the named dashboard's whole group in the sidebar, 0 first. With group_id: after that tab, 0 the first tab. Omit to put it last"`
	Widgets []reporting.WidgetSpec `json:"widgets,omitempty" jsonschema:"widgets in order; all are validated, and one refusal creates nothing"`
}

type updateDashboardIn struct {
	DashboardID int64  `json:"dashboard_id" jsonschema:"dashboard id"`
	Title       string `json:"title,omitempty" jsonschema:"new title, at least 2 characters; omit to keep"`
	WholeGroup  bool   `json:"whole_group,omitempty" jsonschema:"true renames this dashboard's group with title (required, at least 2 characters) and leaves every dashboard's own title alone; takes no after or group_id"`
	GroupID     *int64 `json:"group_id,omitempty" jsonschema:"move it into this group as a tab (after then names a tab there; 0 first); 0 takes it out as a dashboard of its own; omit to stay"`
	After       *int64 `json:"after,omitempty" jsonschema:"a dashboard id: one in the same group moves this tab after it; one in another group moves the whole group after that group (with group_id, it names a tab of that group; with group_id 0, the dashboard goes after that group on its own). 0: without group_id, moves the whole group to the top of the sidebar; with group_id, makes it the first tab (group_id 0: the top of the sidebar)"`
	Sidebar     *bool  `json:"sidebar,omitempty" jsonschema:"built-in dashboards only: true or false puts its whole group in or out of the sidebar. Your own dashboards are always in the sidebar, so it is refused on one (archive_dashboard takes one away). Not with title, after or group_id"`
}

type projectTabsIn struct {
	ProjectID int64 `json:"project_id" jsonschema:"project id; call list_projects first"`
}

type addProjectTabIn struct {
	ProjectID   int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	DashboardID int64  `json:"dashboard_id" jsonschema:"the dashboard to show on this project's page; list_dashboards names them"`
	After       *int64 `json:"after,omitempty" jsonschema:"your own dashboards only: after this tab of the project (one of your own), 0 first among your own; omit to put it last. A built-in goes back to its own place and takes no after"`
}

type projectTabIn struct {
	ProjectID   int64 `json:"project_id" jsonschema:"project id"`
	DashboardID int64 `json:"dashboard_id" jsonschema:"a tab of this project"`
}

type moveProjectTabIn struct {
	ProjectID   int64 `json:"project_id" jsonschema:"project id"`
	DashboardID int64 `json:"dashboard_id" jsonschema:"one of your own tabs of this project"`
	After       int64 `json:"after" jsonschema:"one of your own tabs of this project to go after, 0 first among your own; built-in tabs keep the release's order"`
}

// projectTabsOut is every project tab tool's answer: the page's tabs
// after the call, in order.
type projectTabsOut struct {
	Tabs []reporting.ProjectTab `json:"tabs"`
}

type addWidgetIn struct {
	DashboardID int64  `json:"dashboard_id" jsonschema:"the user dashboard to add to"`
	After       *int64 `json:"after,omitempty" jsonschema:"place it after this widget id; 0 puts it first; omit to put it last"`
	reporting.WidgetSpec
}

type updateWidgetIn struct {
	WidgetID  int64             `json:"widget_id" jsonschema:"widget id"`
	Name      *string           `json:"name,omitempty" jsonschema:"new name, unique on the dashboard; omit to keep"`
	Component *string           `json:"component,omitempty" jsonschema:"new component from list_components; omit to keep"`
	Title     *string           `json:"title,omitempty" jsonschema:"new title; empty removes the header; omit to keep"`
	Props     json.RawMessage   `json:"props,omitempty" jsonschema:"replaces the props object; omit to keep"`
	Source    *reporting.Source `json:"source,omitempty" jsonschema:"replaces the source; omit to keep"`
	Width     *int              `json:"width,omitempty" jsonschema:"columns out of 12 (1–12); omit to keep"`
	Height    *int              `json:"height,omitempty" jsonschema:"rows of 40px (1–12); omit to keep"`
}

type copyWidgetIn struct {
	WidgetID    int64  `json:"widget_id" jsonschema:"the widget to copy; a system widget may be copied"`
	DashboardID int64  `json:"dashboard_id" jsonschema:"the user dashboard to copy it onto"`
	After       *int64 `json:"after,omitempty" jsonschema:"place it after this widget id there; 0 puts it first; omit to put it last"`
}

type viewIn struct {
	DashboardID int64  `json:"dashboard_id"`
	ProjectID   int64  `json:"project_id,omitempty"`
	Range       string `json:"range,omitempty"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
}

func (h *host) listComponents(ctx context.Context, _ struct{}) (listComponentsOut, error) {
	cs, err := h.rep.Components(ctx)
	return listComponentsOut{SourceTypes: h.rep.SourceTypes(), Components: cs}, err
}

func (h *host) listDashboards(ctx context.Context, _ struct{}) (reporting.Dashboards, error) {
	return h.rep.Dashboards(ctx)
}

func (h *host) getDashboard(ctx context.Context, in dashboardIn) (reporting.DashboardDetail, error) {
	return h.rep.Dashboard(ctx, in.DashboardID)
}

func (h *host) listWidgets(ctx context.Context, in listWidgetsIn) (listWidgetsOut, error) {
	ws, err := h.rep.Widgets(ctx, in.DashboardID, in.Component)
	return listWidgetsOut{Widgets: ws}, err
}

func (h *host) widgetData(ctx context.Context, in widgetDataIn) (reporting.WidgetData, error) {
	return h.rep.WidgetData(ctx, reporting.DataRequest{
		WidgetID: in.WidgetID, ProjectID: in.ProjectID, From: in.From, To: in.To, Fresh: in.Fresh,
		Filters: in.Filters, Sort: in.Sort, Distinct: in.Distinct, Offset: in.Offset, Limit: in.Limit})
}

func (h *host) createDashboard(ctx context.Context, in createDashboardIn) (reporting.DashboardDetail, error) {
	return h.rep.CreateDashboard(ctx, actorFrom(ctx), reporting.CreateDashboard{
		Title: in.Title, Range: in.Range, GroupID: in.GroupID, After: in.After, Widgets: in.Widgets})
}

func (h *host) updateDashboard(ctx context.Context, in updateDashboardIn) (reporting.DashboardInfo, error) {
	return h.rep.UpdateDashboard(ctx, actorFrom(ctx), reporting.UpdateDashboard{
		ID: in.DashboardID, Title: in.Title, WholeGroup: in.WholeGroup, GroupID: in.GroupID, After: in.After,
		Sidebar: in.Sidebar})
}

func (h *host) duplicateDashboard(ctx context.Context, in duplicateDashboardIn) (reporting.DashboardDetail, error) {
	return h.rep.DuplicateDashboard(ctx, actorFrom(ctx), reporting.DuplicateDashboard{
		ID: in.DashboardID, WholeGroup: in.WholeGroup, GroupID: in.GroupID})
}

func (h *host) archiveDashboard(ctx context.Context, in dashboardGroupIn) (okOut, error) {
	if err := h.rep.ArchiveDashboard(ctx, actorFrom(ctx), in.DashboardID, in.WholeGroup); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "archived; hidden, reversible with restore_dashboard, purged after RETENTION_ARCHIVED_DAYS"}, nil
}

func (h *host) restoreDashboard(ctx context.Context, in dashboardGroupIn) (okOut, error) {
	if err := h.rep.RestoreDashboard(ctx, actorFrom(ctx), in.DashboardID, in.WholeGroup); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "restored"}, nil
}

func (h *host) listProjectTabs(ctx context.Context, in projectTabsIn) (projectTabsOut, error) {
	ts, err := h.rep.ProjectTabs(ctx, in.ProjectID)
	return projectTabsOut{Tabs: ts}, err
}

func (h *host) addProjectTab(ctx context.Context, in addProjectTabIn) (projectTabsOut, error) {
	ts, err := h.rep.AddProjectTab(ctx, actorFrom(ctx), reporting.AddProjectTab{
		ProjectID: in.ProjectID, DashboardID: in.DashboardID, After: in.After})
	return projectTabsOut{Tabs: ts}, err
}

func (h *host) removeProjectTab(ctx context.Context, in projectTabIn) (projectTabsOut, error) {
	ts, err := h.rep.RemoveProjectTab(ctx, actorFrom(ctx), in.ProjectID, in.DashboardID)
	return projectTabsOut{Tabs: ts}, err
}

func (h *host) moveProjectTab(ctx context.Context, in moveProjectTabIn) (projectTabsOut, error) {
	ts, err := h.rep.MoveProjectTab(ctx, actorFrom(ctx), reporting.MoveProjectTab{
		ProjectID: in.ProjectID, DashboardID: in.DashboardID, After: in.After})
	return projectTabsOut{Tabs: ts}, err
}

func (h *host) addWidget(ctx context.Context, in addWidgetIn) (reporting.WidgetInfo, error) {
	return h.rep.AddWidget(ctx, actorFrom(ctx), reporting.AddWidget{
		DashboardID: in.DashboardID, After: in.After, WidgetSpec: in.WidgetSpec})
}

func (h *host) updateWidget(ctx context.Context, in updateWidgetIn) (reporting.WidgetInfo, error) {
	return h.rep.UpdateWidget(ctx, actorFrom(ctx), reporting.UpdateWidget{
		ID: in.WidgetID, Name: in.Name, Component: in.Component, Title: in.Title,
		Props: in.Props, Source: in.Source, Width: in.Width, Height: in.Height})
}

func (h *host) copyWidget(ctx context.Context, in copyWidgetIn) (reporting.WidgetInfo, error) {
	return h.rep.CopyWidget(ctx, actorFrom(ctx), reporting.CopyWidget{
		ID: in.WidgetID, DashboardID: in.DashboardID, After: in.After})
}

func (h *host) archiveWidget(ctx context.Context, in widgetIn) (okOut, error) {
	if err := h.rep.ArchiveWidget(ctx, actorFrom(ctx), in.WidgetID); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "archived; hidden in place, reversible with restore_widget, purged after RETENTION_ARCHIVED_DAYS"}, nil
}

func (h *host) restoreWidget(ctx context.Context, in widgetIn) (okOut, error) {
	if err := h.rep.RestoreWidget(ctx, actorFrom(ctx), in.WidgetID); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "restored"}, nil
}

// setView stores the viewer's selection. Viewer state, not definition:
// allowed on system dashboards and not audited (D30).
func (h *host) setView(ctx context.Context, in viewIn) (okOut, error) {
	if err := h.rep.SetView(ctx, reporting.View{
		DashboardID: in.DashboardID, ProjectID: in.ProjectID, Range: in.Range, From: in.From, To: in.To}); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "saved"}, nil
}

// registerReporting exposes the reporting operations. Every write is
// refused on system dashboards; each one's description says how to undo
// it or where to start.
func (h *host) registerReporting(r *registrar) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	no := false
	write := &mcp.ToolAnnotations{DestructiveHint: &no}
	idem := &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}
	const d = "/api/dashboards/{dashboard_id}"
	const w = "/api/widgets/{widget_id}"

	// The widget tools' input schemas carry the widget contract, from
	// this build's own component manifest: the one migrate syncs.
	comps, err := reporting.ParseManifest(reporting.Manifest())
	if err != nil {
		panic("api: " + err.Error()) // embedded at build time: this build is broken
	}
	widget := func(pick func(*jsonschema.Schema) *jsonschema.Schema) func(*jsonschema.Schema) {
		return func(in *jsonschema.Schema) {
			if err := reporting.ConstrainWidget(pick(in), comps, h.rep.SourceTypes()); err != nil {
				panic(fmt.Sprintf("api: widget schema: %v", err))
			}
		}
	}
	self := func(in *jsonschema.Schema) *jsonschema.Schema { return in }
	items := func(in *jsonschema.Schema) *jsonschema.Schema { return in.Properties["widgets"].Items }

	expose(r, spec{Name: "reporting_guide", Annotations: ro, // MCP only
		Description: "Call before building or changing a dashboard. One read returns what you author against: the running version and its release notes, the source types and every component (when to use it, the columns its query returns, its props and default size), the queryable views, the active projects, the existing dashboards, and the workflow and rules to follow. docs://reporting is the full reference."},
		h.reportingGuide)
	expose(r, spec{Name: "list_components", Annotations: ro, Method: "GET", Path: "/api/components",
		Description: "The source types (sql, md) and the components a widget can use: each one's description, the source types it accepts, the columns its query must return (inputs), its props schema and its default width and height."},
		h.listComponents)
	expose(r, spec{Name: "list_dashboards", Annotations: ro, Method: "GET", Path: "/api/dashboards",
		Description: "Every dashboard in sidebar order (system first, then user), archived ones included: id, title, owner (system or user), group_id (the group it is a tab of; it changes when the dashboard whose id it is leaves the group, so read it here before using it), stored project and range, live widget count, archived_at, sidebar (its group is in the sidebar; only a built-in group is ever out of it) and project_tab (a built-in that projects created from now on get as a tab; always false for your own); plus the timezone days are grouped in, purge_after_days, how long an archived user dashboard is kept before it is deleted (absent: kept forever), and auto_refresh_seconds, how often the page reloads a dashboard whose viewer turned auto-refresh on (absent: never)."},
		h.listDashboards)
	expose(r, spec{Name: "get_dashboard", Annotations: ro, Method: "GET", Path: d,
		Description: "One dashboard with its group's tabs and its live widgets in order: tabs (the group's live dashboards, this one included, in tab order) and each widget's id, name, component, title, width, height, props, source, and whether it follows the project and range switchers."},
		h.getDashboard)
	expose(r, spec{Name: "list_widgets", Annotations: ro, Method: "GET", Path: "/api/widgets",
		Description: "Widgets, archived ones included, each with its dashboard and its 1-based position there. Filter by dashboard_id and/or component; use it to find an archived widget to restore, or every widget on a component."},
		h.listWidgets)
	expose(r, spec{Name: "widget_data", Annotations: ro, Method: "GET", Path: w + "/data",
		Description: "Load one widget's content, as its card draws it. project_id is required when the widget's SQL uses :project, and from/to (YYYY-MM-DD, at most 365 days) when it uses :from or :to; each is ignored otherwise, and the answer echoes the values applied. fresh=true reuses a cached result only up to REPORTING_REFRESH_SECONDS old. A query that no longer runs, or rows that no longer fit the component, is refused with the reason; removed=true means the widget's component left the code. A table with props.mode \"remote\" also takes filters, sort, distinct, offset and limit, applied in SQL over its whole result; the answer's page block echoes them with matched and total counts (docs://reporting, Filtering and paging a table)."},
		h.widgetData)

	expose(r, spec{Name: "create_dashboard", Annotations: write, Method: "POST", Path: "/api/dashboards", Status: http.StatusCreated, constrain: widget(items),
		Description: "Call reporting_guide first. Create a user dashboard: title (at least 2 characters), optional starting range (default 7d), optional group_id to add it as a tab of that group, optional after (a dashboard id; 0 first), and optional widgets in order. All or nothing: one invalid widget creates nothing."},
		h.createDashboard)
	expose(r, spec{Name: "update_dashboard", Annotations: write, Method: "PATCH", Path: d,
		Description: "Rename a user dashboard (title, at least 2 characters) and/or move it with group_id (join a group as a tab, or 0 to leave one) and/or after (a dashboard id; 0 first). whole_group with title renames its group instead: the sidebar name, which otherwise is the first tab's title; a group name can be replaced, never cleared. sidebar, in a call of its own and for built-in dashboards only, puts the whole group in or out of the sidebar; your own dashboards are always in the sidebar, so it is refused on one (archive_dashboard takes one away). Your own dashboards are added to projects one at a time with add_project_tab. Otherwise system dashboards are read-only."},
		h.updateDashboard)
	expose(r, spec{Name: "duplicate_dashboard", Annotations: write, Method: "POST", Path: d + "/duplicate", Status: http.StatusCreated,
		Description: "Copy any dashboard with copies of its live widgets; the copy is a user dashboard. The copy is a new dashboard last in the sidebar; with group_id it joins that user group as a tab instead (right after the source when that is the source's own group, last otherwise). A system dashboard is copied too, also one out of the sidebar. An archived user dashboard is refused (restore it first). whole_group copies the group as a new dashboard with the same tabs: a system group whole, a user group's live tabs; it takes no group_id. The copy is in the sidebar and on no project's tabs. Duplicating never hides the source: to replace a system group, take it out of the sidebar with update_dashboard {sidebar: false}, and add your copy to projects with add_project_tab."},
		h.duplicateDashboard)
	expose(r, spec{Name: "archive_dashboard", Annotations: idem, Method: "POST", Path: d + "/archive",
		Description: "Archive one of your own dashboards: it leaves the sidebar and every project page, with its widgets. Reversible with restore_dashboard, which brings its project tabs back; purged, with its widgets, RETENTION_ARCHIVED_DAYS (default 30) after archiving unless restored. whole_group archives every tab of its group. A built-in dashboard is never archived: update_dashboard {sidebar: false} takes its group out of the sidebar."},
		h.archiveDashboard)
	expose(r, spec{Name: "restore_dashboard", Annotations: idem, Method: "POST", Path: d + "/restore",
		Description: "Unhide an archived dashboard of your own, where it was in the sidebar and on project pages. whole_group restores every archived tab of its group. A built-in dashboard is never archived, so it is refused."},
		h.restoreDashboard)
	const pt = "/api/projects/{project_id}/tabs"
	expose(r, spec{Name: "list_project_tabs", Annotations: ro, Method: "GET", Path: pt,
		Description: "A project page's tabs after Setup, in order: built-in dashboards (release order), then your own (ordered per project). Each: dashboard_id, title, owner, group_id."},
		h.listProjectTabs)
	expose(r, spec{Name: "add_project_tab", Annotations: write, Method: "POST", Path: pt, Status: http.StatusCreated,
		Description: "Show a dashboard as a tab of a project's page. A built-in goes back to its own place; your own goes after `after`, or last. A dashboard already there is refused."},
		h.addProjectTab)
	expose(r, spec{Name: "remove_project_tab", Annotations: idem, Method: "POST", Path: pt + "/{dashboard_id}/remove",
		Description: "Take a tab off a project's page. The dashboard is kept, and add_project_tab brings the tab back. Your own dashboard is always in the sidebar, so its last tab can go too."},
		h.removeProjectTab)
	expose(r, spec{Name: "move_project_tab", Annotations: write, Method: "POST", Path: pt + "/{dashboard_id}/move",
		Description: "Reorder your own tabs on a project's page; built-in tabs keep the release's order."},
		h.moveProjectTab)
	expose(r, spec{Name: "add_widget", Annotations: write, Method: "POST", Path: d + "/widgets", Status: http.StatusCreated, constrain: widget(self),
		Description: "Call reporting_guide first. Add a widget to a user dashboard: a component, a source ({type: sql|md, content}), optional title, props, width and height (default from the component), name (derived from the title when omitted) and after (a widget id; 0 first; omitted, last). The SQL is run once to check its columns against the component's inputs."},
		h.addWidget)
	expose(r, spec{Name: "update_widget", Annotations: write, Method: "PATCH", Path: w, constrain: widget(self),
		Description: "Call reporting_guide first. Change a widget on a user dashboard: name, component, title, props, source, width or height. Fields you omit are kept; the result is validated whole."},
		h.updateWidget)
	expose(r, spec{Name: "copy_widget", Annotations: write, Method: "POST", Path: w + "/copy", Status: http.StatusCreated,
		Description: "Call reporting_guide first. Copy a widget, a system one included, onto a user dashboard (dashboard_id), keeping its size; optional after (a widget id there; 0 first; omitted, last). The copy is independent of the original."},
		h.copyWidget)
	expose(r, spec{Name: "archive_widget", Annotations: idem, Method: "POST", Path: w + "/archive",
		Description: "Hide a widget in place: this is the undo for add_widget. Reversible with restore_widget, which puts it back where it was; purged RETENTION_ARCHIVED_DAYS (default 30) after archiving unless restored."},
		h.archiveWidget)
	expose(r, spec{Name: "restore_widget", Annotations: idem, Method: "POST", Path: w + "/restore",
		Description: "Unhide an archived widget, in its old place."},
		h.restoreWidget)

	restOnly(r, spec{Name: "view", Method: "PUT", Path: d + "/view",
		Description: "Store the viewer's project and range selection for a dashboard."},
		h.setView)
	h.registerWidgetShares(r)
}
