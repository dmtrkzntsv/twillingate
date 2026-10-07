package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// visitorsSQL follows both switchers, so a widget built from it gives its
// dashboard a project and a range switcher.
const visitorsSQL = `SELECT SUM(visitors) AS value FROM v_views_daily WHERE project_id = :project AND day BETWEEN :from AND :to`

// toolJSON calls a tool, fails the test on a tool error, and decodes its
// output into out.
func toolJSON(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	res := callTool(t, cs, name, args)
	if res.IsError {
		t.Fatalf("%s: %s", name, textOf(res))
	}
	if out != nil {
		if err := json.Unmarshal([]byte(textOf(res)), out); err != nil {
			t.Fatalf("%s output %s: %v", name, textOf(res), err)
		}
	}
}

// TestReportingRoundTrip drives the whole authoring loop over MCP: create
// a dashboard, read it back, add a widget, load its data, then every other
// write tool once, so each tool's input and output schemas are exercised.
func TestReportingRoundTrip(t *testing.T) {
	_, cs := newTestHost(t)

	var d struct {
		ID     int64  `json:"dashboard_id"`
		Title  string `json:"title"`
		Owner  string `json:"owner"`
		Range  string `json:"range"`
		Widget []any  `json:"widgets"`
	}
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Traffic"}, &d)
	if d.ID < 1000 || d.Owner != "user" || d.Range != "7d" {
		t.Fatalf("create_dashboard = %+v", d)
	}
	toolJSON(t, cs, "get_dashboard", map[string]any{"dashboard_id": d.ID}, &d)
	if d.Title != "Traffic" || len(d.Widget) != 0 {
		t.Fatalf("get_dashboard = %+v", d)
	}

	var w struct {
		ID             int64           `json:"widget_id"`
		Name           string          `json:"name"`
		Props          json.RawMessage `json:"props"`
		FollowsProject bool            `json:"follows_project"`
		FollowsRange   bool            `json:"follows_range"`
	}
	toolJSON(t, cs, "add_widget", map[string]any{
		"dashboard_id": d.ID, "component": "stat", "title": "Visitors",
		"props":  map[string]any{"format": "number"},
		"source": map[string]any{"type": "sql", "content": visitorsSQL},
	}, &w)
	if w.Name != "visitors" || !w.FollowsProject || !w.FollowsRange {
		t.Fatalf("add_widget = %+v", w)
	}

	var data struct {
		WidgetID  int64  `json:"widget_id"`
		ProjectID int64  `json:"project_id"`
		From      string `json:"from"`
		To        string `json:"to"`
		Data      struct {
			Columns []string   `json:"columns"`
			Rows    [][]string `json:"rows"`
		} `json:"data"`
	}
	toolJSON(t, cs, "widget_data", map[string]any{
		"widget_id": w.ID, "project_id": 1, "from": "2026-08-20", "to": "2026-08-21", "fresh": true,
	}, &data)
	if data.ProjectID != 1 || data.From != "2026-08-20" || data.To != "2026-08-21" || len(data.Data.Rows) != 1 || data.Data.Rows[0][0] != "28" {
		t.Fatalf("widget_data = %+v", data)
	}

	toolJSON(t, cs, "update_widget", map[string]any{"widget_id": w.ID, "props": map[string]any{"format": "percent"}, "width": 4}, &w)
	if string(w.Props) != `{"format":"percent"}` {
		t.Errorf("update_widget props = %s", w.Props)
	}
	var copied struct {
		ID   int64  `json:"widget_id"`
		Name string `json:"name"`
	}
	toolJSON(t, cs, "copy_widget", map[string]any{"widget_id": w.ID, "dashboard_id": d.ID, "after": 0}, &copied)
	if copied.Name != "visitors-2" {
		t.Errorf("copy_widget name = %q, want visitors-2", copied.Name)
	}
	toolJSON(t, cs, "archive_widget", map[string]any{"widget_id": copied.ID}, nil)
	var listed struct {
		Widgets []struct {
			ID         int64  `json:"widget_id"`
			ArchivedAt string `json:"archived_at"`
		} `json:"widgets"`
	}
	toolJSON(t, cs, "list_widgets", map[string]any{"dashboard_id": d.ID}, &listed)
	if len(listed.Widgets) != 2 {
		t.Fatalf("list_widgets = %+v, want both widgets, the archived one included", listed)
	}
	toolJSON(t, cs, "restore_widget", map[string]any{"widget_id": copied.ID}, nil)

	toolJSON(t, cs, "update_dashboard", map[string]any{"dashboard_id": d.ID, "title": "Traffic, daily", "after": 0}, nil)
	var dup struct {
		ID      int64  `json:"dashboard_id"`
		Title   string `json:"title"`
		Widgets []any  `json:"widgets"`
	}
	toolJSON(t, cs, "duplicate_dashboard", map[string]any{"dashboard_id": d.ID}, &dup)
	if dup.Title != "Traffic, daily (copy)" || len(dup.Widgets) != 2 {
		t.Errorf("duplicate_dashboard = %+v", dup)
	}
	toolJSON(t, cs, "archive_dashboard", map[string]any{"dashboard_id": dup.ID}, nil)
	toolJSON(t, cs, "restore_dashboard", map[string]any{"dashboard_id": dup.ID}, nil)

	var comps struct {
		SourceTypes []string `json:"source_types"`
		Components  []struct {
			Name string `json:"name"`
		} `json:"components"`
	}
	toolJSON(t, cs, "list_components", map[string]any{}, &comps)
	if strings.Join(comps.SourceTypes, ",") != "md,sql" || len(comps.Components) == 0 {
		t.Errorf("list_components = %+v", comps)
	}
	var ds struct {
		Timezone   string `json:"timezone"`
		Dashboards []any  `json:"dashboards"`
	}
	toolJSON(t, cs, "list_dashboards", map[string]any{}, &ds)
	if ds.Timezone != "UTC" || len(ds.Dashboards) < 3 { // the system ones, Traffic and its copy
		t.Errorf("list_dashboards = %+v", ds)
	}
}

// TestReportingRESTStatuses: creations answer 201, the rest 200, and the
// shapes are what the web app reads.
func TestReportingRESTStatuses(t *testing.T) {
	h, _ := newTestHost(t)
	r := newTestRegistrar(t, h)
	must := func(method, target, body string, want int) map[string]any {
		t.Helper()
		rec := serveREST(t, r, method, target, body)
		if rec.Code != want {
			t.Fatalf("%s %s = %d %s, want %d", method, target, rec.Code, rec.Body.String(), want)
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s body %s: %v", method, target, rec.Body.String(), err)
		}
		return out
	}
	d := must("POST", "/api/dashboards", `{"title":"Traffic","range":"30d"}`, http.StatusCreated)
	id := int64(d["dashboard_id"].(float64))
	w := must("POST", fmt.Sprintf("/api/dashboards/%d/widgets", id),
		`{"component":"markdown","source":{"type":"md","content":"Read me."}}`, http.StatusCreated)
	wid := int64(w["widget_id"].(float64))
	must("POST", fmt.Sprintf("/api/widgets/%d/copy", wid), fmt.Sprintf(`{"dashboard_id":%d}`, id), http.StatusCreated)
	must("POST", fmt.Sprintf("/api/dashboards/%d/duplicate", id), "", http.StatusCreated)
	must("PATCH", fmt.Sprintf("/api/widgets/%d", wid), `{"title":"Notes"}`, http.StatusOK)
	must("PATCH", fmt.Sprintf("/api/dashboards/%d", id), `{"title":"Traffic 2"}`, http.StatusOK)
	must("POST", fmt.Sprintf("/api/widgets/%d/archive", wid), "", http.StatusOK)
	must("POST", fmt.Sprintf("/api/widgets/%d/restore", wid), "", http.StatusOK)
	must("POST", fmt.Sprintf("/api/dashboards/%d/archive", id), "", http.StatusOK)
	must("POST", fmt.Sprintf("/api/dashboards/%d/restore", id), "", http.StatusOK)

	data := must("GET", fmt.Sprintf("/api/widgets/%d/data", wid), "", http.StatusOK)
	if md, _ := data["data"].(map[string]any); md["markdown"] != "Read me." {
		t.Errorf("widget data = %v", data)
	}
	comps := must("GET", "/api/components", "", http.StatusOK)
	if _, ok := comps["source_types"]; !ok {
		t.Errorf("GET /api/components = %v, want source_types", comps)
	}
	if list := must("GET", fmt.Sprintf("/api/widgets?dashboard_id=%d&component=markdown", id), "", http.StatusOK); len(list["widgets"].([]any)) != 2 {
		t.Errorf("GET /api/widgets = %v, want the widget and its copy", list)
	}
	if got := must("GET", fmt.Sprintf("/api/dashboards/%d", id), "", http.StatusOK); got["title"] != "Traffic 2" {
		t.Errorf("GET dashboard = %v", got)
	}
	if got := must("GET", "/api/dashboards", "", http.StatusOK); got["timezone"] != "UTC" {
		t.Errorf("GET /api/dashboards = %v", got)
	}
}

// TestReportingRefusals: a system dashboard is read-only (D27's message,
// over both transports) and an unknown id is a 404.
func TestReportingRefusals(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	const system = "dashboard 1 is a system dashboard and changes only with a release; duplicate_dashboard makes an editable copy"

	rec := serveREST(t, r, "PATCH", "/api/dashboards/1", `{"title":"Mine"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), system) {
		t.Errorf("PATCH system dashboard = %d %s", rec.Code, rec.Body.String())
	}
	const archiveSystem = "dashboard 1 is a built-in dashboard and is never archived; update_dashboard {sidebar: false} takes its group out of the sidebar"
	res := callTool(t, cs, "archive_dashboard", map[string]any{"dashboard_id": 1})
	if !res.IsError || !strings.Contains(textOf(res), archiveSystem) {
		t.Errorf("archive_dashboard(1) = %s", textOf(res))
	}
	for _, target := range []string{"/api/dashboards/99999", "/api/widgets/99999/data"} {
		if rec := serveREST(t, r, "GET", target, ""); rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
			t.Errorf("GET %s = %d %s, want 404", target, rec.Code, rec.Body.String())
		}
	}
}

// TestViewRouteIsRESTOnly: the viewer's selection is stored through PUT
// and read back on the dashboard; no MCP tool writes it.
func TestViewRouteIsRESTOnly(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	var d struct {
		ID int64 `json:"dashboard_id"`
	}
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Traffic", "widgets": []any{map[string]any{
		"component": "stat", "title": "Visitors", "source": map[string]any{"type": "sql", "content": visitorsSQL},
	}}}, &d)

	rec := serveREST(t, r, "PUT", fmt.Sprintf("/api/dashboards/%d/view", d.ID), `{"project_id":2,"range":"30d"}`)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"saved"}` {
		t.Fatalf("PUT view = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		ProjectID int64  `json:"project_id"`
		Range     string `json:"range"`
	}
	toolJSON(t, cs, "get_dashboard", map[string]any{"dashboard_id": d.ID}, &got)
	if got.ProjectID != 2 || got.Range != "30d" {
		t.Errorf("selection after PUT = %+v, want project 2, 30d", got)
	}

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Name, "view") && !strings.HasPrefix(tool.Name, "views_") {
			t.Errorf("MCP lists %s; the view route is REST-only", tool.Name)
		}
	}
}

// TestReportingResourcesMatchTools: each schema:// resource is what its
// list tool returns, built on the read — a write made just before shows.
func TestReportingResourcesMatchTools(t *testing.T) {
	_, cs := newTestHost(t)
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Fresh", "widgets": []any{map[string]any{
		"component": "markdown", "source": map[string]any{"type": "md", "content": "Hello."},
	}}}, nil)
	for _, c := range []struct{ uri, tool string }{
		{"schema://components", "list_components"},
		{"schema://dashboards", "list_dashboards"},
		{"schema://widgets", "list_widgets"},
	} {
		res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: c.uri})
		if err != nil {
			t.Fatalf("%s: %v", c.uri, err)
		}
		var fromResource, fromTool any
		if err := json.Unmarshal([]byte(res.Contents[0].Text), &fromResource); err != nil {
			t.Fatalf("%s is not JSON: %v", c.uri, err)
		}
		toolJSON(t, cs, c.tool, map[string]any{}, &fromTool)
		if fmt.Sprint(fromResource) != fmt.Sprint(fromTool) {
			t.Errorf("%s differs from %s:\n%s\n%v", c.uri, c.tool, res.Contents[0].Text, fromTool)
		}
		if c.uri != "schema://components" && !strings.Contains(res.Contents[0].Text, "Fresh") {
			t.Errorf("%s does not show the dashboard just created", c.uri)
		}
	}
}

func TestReportingDocServed(t *testing.T) {
	_, cs := newTestHost(t)
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "docs://reporting"})
	if err != nil {
		t.Fatalf("docs://reporting: %v", err)
	}
	if body := res.Contents[0].Text; !strings.HasPrefix(body, "# Reporting") {
		t.Errorf("docs://reporting starts %.40q", body)
	}
}

// TestWidgetDataRESTDecodesQuery: the data route reads project_id, from,
// to and fresh from the query string, and the envelope echoes what it
// applied.
func TestWidgetDataRESTDecodesQuery(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	var d struct {
		Widgets []struct {
			ID int64 `json:"widget_id"`
		} `json:"widgets"`
	}
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Traffic", "widgets": []any{map[string]any{
		"component": "stat", "source": map[string]any{"type": "sql", "content": visitorsSQL},
	}}}, &d)
	target := fmt.Sprintf("/api/widgets/%d/data?project_id=1&from=2026-08-20&to=2026-08-21&fresh=true", d.Widgets[0].ID)
	rec := serveREST(t, r, "GET", target, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", target, rec.Code, rec.Body.String())
	}
	var got struct {
		ProjectID int64  `json:"project_id"`
		From      string `json:"from"`
		To        string `json:"to"`
		Data      struct {
			Rows [][]string `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// 10 + 12 web visitors and 6 app visitors over the two days.
	if got.ProjectID != 1 || got.From != "2026-08-20" || got.To != "2026-08-21" ||
		len(got.Data.Rows) != 1 || got.Data.Rows[0][0] != "28" {
		t.Errorf("GET %s = %s", target, rec.Body.String())
	}
	if rec := serveREST(t, r, "GET", fmt.Sprintf("/api/widgets/%d/data?project_id=1&from=2026-08-20&to=2026-08-21&fresh=maybe", d.Widgets[0].ID), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("fresh=maybe = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if rec := serveREST(t, r, "GET", fmt.Sprintf("/api/widgets/%d/data?from=2026-08-20&to=2026-08-21", d.Widgets[0].ID), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("no project_id = %d %s, want 400", rec.Code, rec.Body.String())
	}
}

// TestWidgetDataRemoteTable: a remote table's paging arguments reach the
// service from the REST query string and from the MCP tool alike, and the
// envelope carries the page block.
func TestWidgetDataRemoteTable(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	var d struct {
		Widgets []struct {
			ID int64 `json:"widget_id"`
		} `json:"widgets"`
	}
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Rows", "widgets": []any{map[string]any{
		"component": "table", "props": map[string]any{"mode": "remote"}, "source": map[string]any{"type": "sql",
			"content": `SELECT 'a' AS "Key", 3 AS "N" UNION ALL SELECT 'b', 1 UNION ALL SELECT 'c', 2`},
	}}}, &d)
	id := d.Widgets[0].ID
	type envelope struct {
		Data struct {
			Rows      [][]string `json:"rows"`
			Truncated bool       `json:"truncated"`
		} `json:"data"`
		Page struct {
			Offset, Limit, Matched, Total int
			Sort                          string
			Filters                       []map[string]any
		} `json:"page"`
	}
	check := func(via string, got envelope) {
		t.Helper()
		if len(got.Data.Rows) != 1 || got.Data.Rows[0][0] != "a" || !got.Data.Truncated ||
			got.Page.Limit != 1 || got.Page.Matched != 2 || got.Page.Total != 3 || got.Page.Sort != "N:desc" || len(got.Page.Filters) != 1 {
			t.Errorf("%s: %+v", via, got)
		}
	}

	filters := `[{"column":"N","op":">","value":"1"}]`
	target := fmt.Sprintf("/api/widgets/%d/data?filters=%s&sort=N:desc&limit=1", id, url.QueryEscape(filters))
	rec := serveREST(t, r, "GET", target, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", target, rec.Code, rec.Body.String())
	}
	var got envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	check("REST", got)

	var viaMCP envelope
	toolJSON(t, cs, "widget_data", map[string]any{"widget_id": id, "filters": filters, "sort": "N:desc", "limit": 1}, &viaMCP)
	check("MCP", viaMCP)

	if rec := serveREST(t, r, "GET", fmt.Sprintf("/api/widgets/%d/data?sort=N:up", id), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("sort=N:up = %d %s, want 400", rec.Code, rec.Body.String())
	}
}

// dashboardArchivedAt returns every dashboard's archived_at, keyed by id,
// fresh from list_dashboards each call. archived_at is omitempty, so a
// struct decoded into repeatedly would keep a stale value once a
// dashboard is restored; a fresh local avoids that trap.
func dashboardArchivedAt(t *testing.T, cs *mcp.ClientSession) map[int64]string {
	t.Helper()
	var list struct {
		Dashboards []struct {
			ID         int64  `json:"dashboard_id"`
			ArchivedAt string `json:"archived_at"`
		} `json:"dashboards"`
	}
	toolJSON(t, cs, "list_dashboards", map[string]any{}, &list)
	out := make(map[int64]string, len(list.Dashboards))
	for _, d := range list.Dashboards {
		out[d.ID] = d.ArchivedAt
	}
	return out
}

// TestDashboardGroups: group_id on create and update makes and moves
// tabs, whole_group extends duplicate/archive/restore to every tab, and
// get_dashboard shows the group's tabs (Task 6, group_id/whole_group on
// the existing tools and routes).
func TestDashboardGroups(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)

	var one struct {
		ID      int64 `json:"dashboard_id"`
		GroupID int64 `json:"group_id"`
	}
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "One"}, &one)

	// create_dashboard with group_id joins that group as a tab.
	var two struct {
		ID      int64 `json:"dashboard_id"`
		GroupID int64 `json:"group_id"`
	}
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Two", "group_id": one.GroupID}, &two)
	if two.GroupID != one.GroupID {
		t.Fatalf("create_dashboard group_id = %d, want %d", two.GroupID, one.GroupID)
	}

	// get_dashboard shows group_id and both tabs.
	var detail struct {
		GroupID int64 `json:"group_id"`
		Tabs    []struct {
			ID    int64  `json:"dashboard_id"`
			Title string `json:"title"`
		} `json:"tabs"`
	}
	toolJSON(t, cs, "get_dashboard", map[string]any{"dashboard_id": one.ID}, &detail)
	if detail.GroupID != one.GroupID || len(detail.Tabs) != 2 {
		t.Fatalf("get_dashboard tabs = %+v", detail)
	}

	// REST: PATCH with group_id 0 takes it out as a dashboard of its own.
	rec := serveREST(t, r, "PATCH", fmt.Sprintf("/api/dashboards/%d", two.ID), `{"group_id":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH group_id=0 = %d %s", rec.Code, rec.Body.String())
	}
	toolJSON(t, cs, "get_dashboard", map[string]any{"dashboard_id": two.ID}, &detail)
	if detail.GroupID == one.GroupID {
		t.Fatalf("group_id=0 left it in the group: %+v", detail)
	}

	// REST: PATCH with group_id moves it back in as a tab.
	rec = serveREST(t, r, "PATCH", fmt.Sprintf("/api/dashboards/%d", two.ID), fmt.Sprintf(`{"group_id":%d}`, one.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH group_id = %d %s", rec.Code, rec.Body.String())
	}
	toolJSON(t, cs, "get_dashboard", map[string]any{"dashboard_id": two.ID}, &detail)
	if detail.GroupID != one.GroupID {
		t.Fatalf("group_id move did not join the group: %+v", detail)
	}

	// REST: POST archive with an empty body archives just the one dashboard.
	rec = serveREST(t, r, "POST", fmt.Sprintf("/api/dashboards/%d/archive", two.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("archive one = %d %s", rec.Code, rec.Body.String())
	}
	if archivedAt := dashboardArchivedAt(t, cs); archivedAt[two.ID] == "" || archivedAt[one.ID] != "" {
		t.Fatalf("archive one archived the wrong set: %+v", archivedAt)
	}
	toolJSON(t, cs, "restore_dashboard", map[string]any{"dashboard_id": two.ID}, nil)

	// REST: POST archive with a literal {} body (whole_group omitted)
	// archives just the one dashboard too.
	rec = serveREST(t, r, "POST", fmt.Sprintf("/api/dashboards/%d/archive", two.ID), "{}")
	if rec.Code != http.StatusOK {
		t.Fatalf("archive one with {} body = %d %s", rec.Code, rec.Body.String())
	}
	if archivedAt := dashboardArchivedAt(t, cs); archivedAt[two.ID] == "" || archivedAt[one.ID] != "" {
		t.Fatalf("archive one with {} body archived the wrong set: %+v", archivedAt)
	}
	toolJSON(t, cs, "restore_dashboard", map[string]any{"dashboard_id": two.ID}, nil)

	// REST: POST archive with whole_group archives every tab.
	rec = serveREST(t, r, "POST", fmt.Sprintf("/api/dashboards/%d/archive", two.ID), `{"whole_group":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive whole_group = %d %s", rec.Code, rec.Body.String())
	}
	if archivedAt := dashboardArchivedAt(t, cs); archivedAt[one.ID] == "" || archivedAt[two.ID] == "" {
		t.Fatalf("archive whole_group left a tab live: %+v", archivedAt)
	}

	// MCP: restore_dashboard with whole_group restores every tab.
	toolJSON(t, cs, "restore_dashboard", map[string]any{"dashboard_id": one.ID, "whole_group": true}, nil)
	if archivedAt := dashboardArchivedAt(t, cs); archivedAt[one.ID] != "" || archivedAt[two.ID] != "" {
		t.Fatalf("restore whole_group left a tab archived: %+v", archivedAt)
	}

	// MCP: duplicate_dashboard with whole_group copies every tab as one
	// new group.
	var dup struct {
		ID      int64 `json:"dashboard_id"`
		GroupID int64 `json:"group_id"`
	}
	toolJSON(t, cs, "duplicate_dashboard", map[string]any{"dashboard_id": one.ID, "whole_group": true}, &dup)
	toolJSON(t, cs, "get_dashboard", map[string]any{"dashboard_id": dup.ID}, &detail)
	if len(detail.Tabs) != 2 {
		t.Fatalf("duplicate_dashboard whole_group tabs = %+v", detail)
	}

	// REST: POST /api/dashboards with group_id creates a tab.
	created := serveREST(t, r, "POST", "/api/dashboards", fmt.Sprintf(`{"title":"Three","group_id":%d}`, one.ID))
	if created.Code != http.StatusCreated {
		t.Fatalf("POST create with group_id = %d %s", created.Code, created.Body.String())
	}
	var three map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &three); err != nil {
		t.Fatal(err)
	}
	if int64(three["group_id"].(float64)) != one.GroupID {
		t.Fatalf("POST create group_id = %v, want %d", three["group_id"], one.GroupID)
	}
}

// TestDashboardGroupRouteBodies: the restore and duplicate routes, like
// archive, act on the one dashboard with an empty body or {} and on its
// whole group with {"whole_group": true}.
func TestDashboardGroupRouteBodies(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	type dash struct {
		ID      int64 `json:"dashboard_id"`
		GroupID int64 `json:"group_id"`
		Tabs    []struct {
			ID int64 `json:"dashboard_id"`
		} `json:"tabs"`
	}
	var one, two dash
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "One"}, &one)
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Two", "group_id": one.GroupID}, &two)

	for _, tc := range []struct {
		body      string
		wantWhole bool
	}{{"", false}, {"{}", false}, {`{"whole_group":true}`, true}} {
		toolJSON(t, cs, "archive_dashboard", map[string]any{"dashboard_id": one.ID, "whole_group": true}, nil)
		rec := serveREST(t, r, "POST", fmt.Sprintf("/api/dashboards/%d/restore", two.ID), tc.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("restore with body %q = %d %s", tc.body, rec.Code, rec.Body.String())
		}
		archivedAt := dashboardArchivedAt(t, cs)
		if archivedAt[two.ID] != "" || (archivedAt[one.ID] == "") != tc.wantWhole {
			t.Fatalf("restore with body %q restored the wrong set: %+v", tc.body, archivedAt)
		}
	}

	for _, tc := range []struct {
		body      string
		wantWhole bool
		wantJoin  bool
	}{{"", false, false}, {"{}", false, false}, {fmt.Sprintf(`{"group_id":%d}`, one.GroupID), false, true}, {`{"whole_group":true}`, true, false}} {
		var before dash
		toolJSON(t, cs, "get_dashboard", map[string]any{"dashboard_id": one.ID}, &before)
		rec := serveREST(t, r, "POST", fmt.Sprintf("/api/dashboards/%d/duplicate", two.ID), tc.body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("duplicate with body %q = %d %s", tc.body, rec.Code, rec.Body.String())
		}
		var cp dash
		if err := json.Unmarshal(rec.Body.Bytes(), &cp); err != nil {
			t.Fatal(err)
		}
		if tc.wantWhole {
			// A new group holding a copy of every tab.
			if cp.GroupID == one.GroupID || len(cp.Tabs) != len(before.Tabs) {
				t.Fatalf("duplicate with body %q = group %d with %d tabs, want a new group of %d",
					tc.body, cp.GroupID, len(cp.Tabs), len(before.Tabs))
			}
			continue
		}
		if !tc.wantJoin {
			// One copy, a dashboard of its own.
			if cp.GroupID == one.GroupID || len(cp.Tabs) != 1 {
				t.Fatalf("duplicate with body %q = group %d with %d tabs, want a new group of 1", tc.body, cp.GroupID, len(cp.Tabs))
			}
			continue
		}
		// One copy, the next tab of the group named.
		if cp.GroupID != one.GroupID || len(cp.Tabs) != len(before.Tabs)+1 {
			t.Fatalf("duplicate with body %q = group %d with %d tabs, want group %d with %d",
				tc.body, cp.GroupID, len(cp.Tabs), one.GroupID, len(before.Tabs)+1)
		}
	}
}

// TestDuplicateSystemDashboardRESTLeavesItLive: duplicating a system
// dashboard over the REST route copies it without archiving the source.
func TestDuplicateSystemDashboardRESTLeavesItLive(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	rec := serveREST(t, r, "POST", "/api/dashboards/1/duplicate", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("duplicate = %d %s", rec.Code, rec.Body.String())
	}
	if archivedAt := dashboardArchivedAt(t, cs); archivedAt[1] != "" {
		t.Errorf("system dashboard 1 archived_at = %q, want live", archivedAt[1])
	}
}

// TestViewRouteRefusals: each part is required when the dashboard has
// that switcher and refused when it has none; presets are a closed list;
// an unknown dashboard is a 404.
func TestViewRouteRefusals(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	var both, none struct {
		ID int64 `json:"dashboard_id"`
	}
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Both", "widgets": []any{map[string]any{
		"component": "stat", "source": map[string]any{"type": "sql", "content": visitorsSQL},
	}}}, &both)
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Notes", "widgets": []any{map[string]any{
		"component": "markdown", "source": map[string]any{"type": "md", "content": "Read me."},
	}}}, &none)
	for _, c := range []struct {
		id   int64
		body string
		want int
	}{
		{both.ID, `{"range":"7d"}`, http.StatusBadRequest},                  // no project_id
		{both.ID, `{"project_id":1}`, http.StatusBadRequest},                // no range
		{both.ID, `{"project_id":1,"range":"week"}`, http.StatusBadRequest}, // not a preset
		{both.ID, `{"project_id":1,"range":"7d","from":"2026-08-01","to":"2026-08-02"}`, http.StatusBadRequest},
		{both.ID, `{"project_id":1,"range":"custom","from":"2026-08-02","to":"2026-08-01"}`, http.StatusBadRequest},
		{none.ID, `{"project_id":1}`, http.StatusBadRequest}, // no project switcher
		{none.ID, `{"range":"7d"}`, http.StatusBadRequest},   // no range switcher
		{none.ID, `{}`, http.StatusOK},
		{both.ID, `{"project_id":1,"range":"custom","from":"2026-08-01","to":"2026-08-31"}`, http.StatusOK},
		{99999, `{"project_id":1,"range":"7d"}`, http.StatusNotFound},
	} {
		rec := serveREST(t, r, "PUT", fmt.Sprintf("/api/dashboards/%d/view", c.id), c.body)
		if rec.Code != c.want {
			t.Errorf("PUT view %d %s = %d %s, want %d", c.id, c.body, rec.Code, rec.Body.String(), c.want)
		}
	}
}

// TestUpdateDashboardWholeGroup: whole_group with title renames the
// dashboard's group and answers group_title; it needs a title of at least
// two characters, and a plain title needs them too.
func TestUpdateDashboardWholeGroup(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)

	var d struct {
		ID int64 `json:"dashboard_id"`
	}
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Traffic"}, &d)
	path := fmt.Sprintf("/api/dashboards/%d", d.ID)

	rec := serveREST(t, r, "PATCH", path, `{"whole_group":true,"title":"Ops"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"group_title":"Ops"`) {
		t.Fatalf("PATCH whole_group = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"title":"Traffic"`) {
		t.Errorf("whole_group changed the dashboard's own title: %s", rec.Body.String())
	}

	var info struct {
		GroupTitle string `json:"group_title"`
	}
	toolJSON(t, cs, "update_dashboard", map[string]any{"dashboard_id": d.ID, "whole_group": true, "title": "Ops team"}, &info)
	if info.GroupTitle != "Ops team" {
		t.Errorf("update_dashboard whole_group group_title = %q", info.GroupTitle)
	}

	for _, body := range []string{`{"whole_group":true}`, `{"title":"a"}`, `{"whole_group":true,"title":"x"}`} {
		if rec := serveREST(t, r, "PATCH", path, body); rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d %s, want 400", body, rec.Code, rec.Body.String())
		}
	}
	if res := callTool(t, cs, "create_dashboard", map[string]any{"title": "a"}); !res.IsError {
		t.Errorf("create_dashboard with a 1-character title succeeded")
	}
}

// projectTabs is list_project_tabs' answer, decoded.
type projectTabs struct {
	Tabs []struct {
		ID      int64  `json:"dashboard_id"`
		Title   string `json:"title"`
		Owner   string `json:"owner"`
		GroupID int64  `json:"group_id"`
	} `json:"tabs"`
}

// TestProjectTabTools drives the four project tab tools over MCP: a copy
// of a built-in added to a project's page, moved, then removed, leaves
// the page as it was.
func TestProjectTabTools(t *testing.T) {
	_, cs := newTestHost(t)
	var before, tabs projectTabs
	toolJSON(t, cs, "list_project_tabs", map[string]any{"project_id": 1}, &before)
	if len(before.Tabs) == 0 || before.Tabs[0].Owner != "system" {
		t.Fatalf("list_project_tabs = %+v, want the built-ins first", before)
	}

	var a, b struct {
		ID int64 `json:"dashboard_id"`
	}
	toolJSON(t, cs, "duplicate_dashboard", map[string]any{"dashboard_id": 1}, &a)
	toolJSON(t, cs, "duplicate_dashboard", map[string]any{"dashboard_id": 1}, &b)
	toolJSON(t, cs, "add_project_tab", map[string]any{"project_id": 1, "dashboard_id": a.ID}, nil)
	toolJSON(t, cs, "add_project_tab", map[string]any{"project_id": 1, "dashboard_id": b.ID}, &tabs)
	n := len(before.Tabs)
	if len(tabs.Tabs) != n+2 || tabs.Tabs[n].ID != a.ID || tabs.Tabs[n+1].ID != b.ID || tabs.Tabs[n].Owner != "user" {
		t.Fatalf("add_project_tab = %+v, want the copies last, in the order added", tabs)
	}
	if res := callTool(t, cs, "add_project_tab", map[string]any{"project_id": 1, "dashboard_id": a.ID}); !res.IsError {
		t.Errorf("adding a tab twice succeeded")
	}
	toolJSON(t, cs, "move_project_tab", map[string]any{"project_id": 1, "dashboard_id": b.ID, "after": 0}, &tabs)
	if tabs.Tabs[n].ID != b.ID || tabs.Tabs[n+1].ID != a.ID {
		t.Errorf("move_project_tab = %+v, want %d first among the user's own, after the built-ins", tabs, b.ID)
	}
	toolJSON(t, cs, "remove_project_tab", map[string]any{"project_id": 1, "dashboard_id": a.ID}, nil)
	toolJSON(t, cs, "remove_project_tab", map[string]any{"project_id": 1, "dashboard_id": b.ID}, &tabs)
	if fmt.Sprint(tabs) != fmt.Sprint(before) {
		t.Errorf("after remove_project_tab = %+v, want %+v", tabs, before)
	}
}

// TestUpdateDashboardSidebarTool: sidebar on update_dashboard takes a
// built-in's whole group out of the sidebar.
func TestUpdateDashboardSidebarTool(t *testing.T) {
	_, cs := newTestHost(t)
	var info struct {
		Sidebar *bool `json:"sidebar"`
	}
	toolJSON(t, cs, "update_dashboard", map[string]any{"dashboard_id": 1, "sidebar": false}, &info)
	if info.Sidebar == nil || *info.Sidebar {
		t.Fatalf("update_dashboard sidebar = %v, want false", info.Sidebar)
	}
	var list struct {
		Dashboards []struct {
			ID      int64 `json:"dashboard_id"`
			GroupID int64 `json:"group_id"`
			Owner   string
			Sidebar bool `json:"sidebar"`
		} `json:"dashboards"`
	}
	toolJSON(t, cs, "list_dashboards", map[string]any{}, &list)
	members := 0
	for _, d := range list.Dashboards {
		if d.Owner == "system" && d.GroupID == 1 {
			members++
			if d.Sidebar {
				t.Errorf("dashboard %d is still in the sidebar", d.ID)
			}
		}
	}
	if members < 2 {
		t.Errorf("group 1 has %d members, want the built-ins", members)
	}
}

// TestArchiveBuiltinRefused: a built-in is never archived, not even with
// its whole group; over REST that is a 400.
func TestArchiveBuiltinRefused(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	if res := callTool(t, cs, "archive_dashboard", map[string]any{"dashboard_id": 1, "whole_group": true}); !res.IsError {
		t.Errorf("archive_dashboard(1, whole_group) = %s, want a refusal", textOf(res))
	}
	if rec := serveREST(t, r, "POST", "/api/dashboards/1/archive", `{"whole_group":true}`); rec.Code != http.StatusBadRequest {
		t.Errorf("POST archive whole_group = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if archivedAt := dashboardArchivedAt(t, cs); archivedAt[1] != "" {
		t.Errorf("dashboard 1 archived_at = %q, want live", archivedAt[1])
	}
}
