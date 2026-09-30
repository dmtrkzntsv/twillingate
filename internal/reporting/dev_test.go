package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// devDir builds a temp directory with one valid dashboard ("ok", holding
// the widgets given as name/component/ext/content) and one broken one
// ("broken", an unparsable dashboard.json), and returns its path.
func devDir(t *testing.T, widgets ...struct{ name, component, ext, content string }) string {
	t.Helper()
	root := t.TempDir()
	ok := filepath.Join(root, "ok")
	if err := os.MkdirAll(ok, 0o755); err != nil {
		t.Fatal(err)
	}
	var layout []string
	for _, w := range widgets {
		layout = append(layout, `{"widget":"`+w.name+`"}`)
		cfg := `{"component":"` + w.component + `","title":"` + w.name + `"}`
		if err := os.WriteFile(filepath.Join(ok, w.name+".json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ok, w.name+"."+w.ext), []byte(w.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dash := `{"title":"Views","range":"7d","layout":[` + strings.Join(layout, ",") + `]}`
	if err := os.WriteFile(filepath.Join(ok, "dashboard.json"), []byte(dash), 0o644); err != nil {
		t.Fatal(err)
	}

	broken := filepath.Join(root, "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "dashboard.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// devRequest is a request as a browser on this machine sends it: to a
// loopback host, the only kind reporting dev answers.
func devRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Host = "127.0.0.1:3100"
	return r
}

// getJSON runs a GET against h and decodes the JSON body into out.
func getJSON(t *testing.T, h http.Handler, path string, out any) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, devRequest(http.MethodGet, path))
	res := rec.Result()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v (body: %s)", path, err, rec.Body.String())
		}
	}
	return res
}

func TestDevHandlerListsDashboardAndReportsBrokenOnes(t *testing.T) {
	root := devDir(t, struct{ name, component, ext, content string }{"note", "markdown", "md", "hello"})
	h := DevHandler([]string{root}, newTestReadDB(t))

	var out Dashboards
	res := getJSON(t, h, "/api/dashboards", &out)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if !out.Dev {
		t.Error("dev = false, want true")
	}
	if len(out.Dashboards) != 1 || out.Dashboards[0].Title != "Views" {
		t.Fatalf("dashboards = %+v", out.Dashboards)
	}
	if out.Dashboards[0].Widgets != 1 {
		t.Errorf("widgets = %d, want 1", out.Dashboards[0].Widgets)
	}
	if len(out.Errors) != 1 || !strings.Contains(out.Errors[0].Dir, "broken") {
		t.Fatalf("errors = %+v, want one naming the broken directory", out.Errors)
	}
	if out.Errors[0].Message == "" {
		t.Error("errors[0].Message is empty")
	}
}

func TestDevHandlerDashboardDetail(t *testing.T) {
	root := devDir(t, struct{ name, component, ext, content string }{"note", "markdown", "md", "hello"})
	h := DevHandler([]string{root}, newTestReadDB(t))

	var list Dashboards
	getJSON(t, h, "/api/dashboards", &list)
	id := list.Dashboards[0].ID
	if id != firstDevDashboardID {
		t.Fatalf("id = %d, want %d (dashboard.json gives none)", id, firstDevDashboardID)
	}

	var detail DashboardDetail
	res := getJSON(t, h, "/api/dashboards/"+strconv.FormatInt(id, 10), &detail)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if len(detail.Widgets) != 1 || detail.Widgets[0].Name != "note" {
		t.Fatalf("widgets = %+v", detail.Widgets)
	}
	wantWidgetID := id*1000 + 1
	if detail.Widgets[0].ID != wantWidgetID {
		t.Errorf("widget id = %d, want %d", detail.Widgets[0].ID, wantWidgetID)
	}
}

func TestDevHandlerVersionChangesWhenAFileIsEdited(t *testing.T) {
	root := devDir(t, struct{ name, component, ext, content string }{"count", "table", "sql", "SELECT 'a' AS x"})
	h := DevHandler([]string{root}, newTestReadDB(t))

	var v1 map[string]string
	getJSON(t, h, "/api/dev/version", &v1)
	var v1again map[string]string
	getJSON(t, h, "/api/dev/version", &v1again)
	if v1["version"] == "" || v1["version"] != v1again["version"] {
		t.Fatalf("version not stable across two reads with no edit: %v vs %v", v1, v1again)
	}

	sqlPath := filepath.Join(root, "ok", "count.sql")
	if err := os.WriteFile(sqlPath, []byte("SELECT 'b' AS x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var v2 map[string]string
	getJSON(t, h, "/api/dev/version", &v2)
	if v2["version"] == v1["version"] {
		t.Fatal("version unchanged after editing a .sql file")
	}

	// The next data call must reflect the edit: 'b', not 'a'.
	widgetID := strconv.FormatInt(firstDevDashboardID*1000+1, 10)
	var data WidgetData
	res := getJSON(t, h, "/api/widgets/"+widgetID+"/data", &data)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	m, ok := data.Data.(map[string]any)
	if !ok {
		t.Fatalf("data.Data = %#v, want a sql result map", data.Data)
	}
	rows, _ := m["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want 1 row", rows)
	}
	row, _ := rows[0].([]any)
	if len(row) != 1 || row[0] != "b" {
		t.Fatalf("row = %v, want [\"b\"] (the edited content)", row)
	}
}

func TestDevHandlerInvalidWidgetAnswers400(t *testing.T) {
	root := devDir(t, struct{ name, component, ext, content string }{"bogus", "does-not-exist", "sql", "SELECT 1"})
	h := DevHandler([]string{root}, newTestReadDB(t))

	widgetID := strconv.FormatInt(firstDevDashboardID*1000+1, 10)
	var body map[string]map[string]string
	res := getJSON(t, h, "/api/widgets/"+widgetID+"/data", &body)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if body["error"]["code"] != "invalid" {
		t.Errorf("error.code = %q, want invalid", body["error"]["code"])
	}
	if !strings.Contains(body["error"]["message"], "does-not-exist") {
		t.Errorf("error.message = %q, want it to name the bad component", body["error"]["message"])
	}
}

func TestDevHandlerUnknownWidgetIs404(t *testing.T) {
	root := devDir(t)
	h := DevHandler([]string{root}, newTestReadDB(t))
	res := getJSON(t, h, "/api/widgets/999999/data", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

func TestDevHandlerRootOpensTheDashboards(t *testing.T) {
	h := DevHandler([]string{devDir(t)}, newTestReadDB(t))
	res := getJSON(t, h, "/", nil)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/app/" {
		t.Fatalf("GET / = %d to %q, want 302 to /app/", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestDevHandlerComponentsAndProjectsAndView(t *testing.T) {
	root := devDir(t)
	h := DevHandler([]string{root}, newTestReadDB(t))

	var comps struct {
		SourceTypes []string `json:"source_types"`
	}
	getJSON(t, h, "/api/components", &comps)
	if len(comps.SourceTypes) != 2 {
		t.Errorf("source_types = %v, want md and sql", comps.SourceTypes)
	}

	var projects struct {
		Projects []any `json:"projects"`
	}
	res := getJSON(t, h, "/api/projects", &projects)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, devRequest(http.MethodPut, "/api/dashboards/1001/view"))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT view status = %d", rec.Code)
	}
	var saved map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved["status"] != "saved" {
		t.Errorf("status = %q, want saved", saved["status"])
	}
}

// TestDevHandlerAnswersOnlyLoopbackHosts: a page that rebinds its own DNS
// name to 127.0.0.1 still sends that name in Host, and is refused.
func TestDevHandlerAnswersOnlyLoopbackHosts(t *testing.T) {
	h := DevHandler([]string{devDir(t)}, newTestReadDB(t))
	for host, want := range map[string]int{
		"127.0.0.1:3100":     http.StatusOK,
		"localhost:3100":     http.StatusOK,
		"LOCALHOST":          http.StatusOK,
		"[::1]:3100":         http.StatusOK,
		"127.8.9.10":         http.StatusOK,
		"evil.example:3100":  http.StatusForbidden,
		"evil.example":       http.StatusForbidden,
		"192.168.1.5:3100":   http.StatusForbidden,
		"localhost.evil.com": http.StatusForbidden,
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/dashboards", nil)
		r.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Errorf("Host %q: status = %d, want %d", host, rec.Code, want)
		}
	}
}

// TestDevHandlerSystemRangeIdsPreviewAsSystem: a directory whose
// dashboard.json gives an id in 1-999 is a system dashboard in the making,
// so it previews as one; one without an id (1001 on) is the user's.
func TestDevHandlerSystemRangeIdsPreviewAsSystem(t *testing.T) {
	root := devDir(t)
	sys := filepath.Join(root, "sys")
	if err := os.MkdirAll(sys, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sys, "dashboard.json"), []byte(`{"id":5,"title":"Sys","range":"7d","layout":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h := DevHandler([]string{root}, newTestReadDB(t))

	var out Dashboards
	getJSON(t, h, "/api/dashboards", &out)
	owners := map[int64]string{}
	for _, d := range out.Dashboards {
		owners[d.ID] = d.Owner
	}
	if owners[5] != store.OwnerSystem || owners[firstDevDashboardID] != store.OwnerUser {
		t.Errorf("owners = %v, want 5 system and %d user", owners, firstDevDashboardID)
	}
}

// TestDevHandlerGroupTabs mirrors Service.Dashboard's tab rule (D17): a
// system group previews with its tabs exactly as it will ship. Five
// directories, ids 1-5, named so an alphabetical directory scan would
// give the wrong order (groups, product, retention, users, views); 2-5
// name "group":1, 1 names none (so its own id, 1, is the group) — the
// same shape as the real Views/Product/Users/Groups/Retention release.
// loadDevDashboards sorts by id (the same rule LoadDashboards uses for
// the real release), so both devListDashboards and a group's Tabs come
// out in id order — Views, Product, Users, Groups, Retention — matching
// what ships, not the directory scan order.
func TestDevHandlerGroupTabs(t *testing.T) {
	root := t.TempDir()
	dash := func(id int64, title string, group int64) string {
		if group == 0 {
			return fmt.Sprintf(`{"id":%d,"title":%q,"range":"7d","layout":[]}`, id, title)
		}
		return fmt.Sprintf(`{"id":%d,"title":%q,"range":"7d","group":%d,"layout":[]}`, id, title, group)
	}
	dirs := []struct {
		name  string
		id    int64
		title string
		group int64
	}{
		{"views", 1, "Views", 0},
		{"product", 2, "Product", 1},
		{"users", 3, "Users", 1},
		{"groups", 4, "Groups", 1},
		{"retention", 5, "Retention", 1},
	}
	for _, d := range dirs {
		dir := filepath.Join(root, d.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "dashboard.json"), []byte(dash(d.id, d.title, d.group)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := DevHandler([]string{root}, newTestReadDB(t))

	want := []string{"Views", "Product", "Users", "Groups", "Retention"}

	var list Dashboards
	res := getJSON(t, h, "/api/dashboards", &list)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var listTitles []string
	for _, d := range list.Dashboards {
		listTitles = append(listTitles, d.Title)
	}
	if strings.Join(listTitles, ",") != strings.Join(want, ",") {
		t.Errorf("list order = %v, want %v (id order, not directory scan order)", listTitles, want)
	}

	var detail DashboardDetail
	res = getJSON(t, h, "/api/dashboards/3", &detail)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if detail.GroupID != 1 {
		t.Errorf("GroupID = %d, want 1", detail.GroupID)
	}
	if len(detail.Tabs) != 5 {
		t.Fatalf("Tabs = %+v, want 5", detail.Tabs)
	}
	var tabTitles []string
	for _, tab := range detail.Tabs {
		tabTitles = append(tabTitles, tab.Title)
	}
	if strings.Join(tabTitles, ",") != strings.Join(want, ",") {
		t.Errorf("Tabs = %v, want %v", tabTitles, want)
	}
}

// TestDevHandlerListsArchivedProjects: /api/projects answers as
// list_projects does, archived projects included and flagged.
func TestDevHandlerListsArchivedProjects(t *testing.T) {
	st, db := newTestStoreAndReadDB(t)
	live := mustCreateProject(t, st, "live")
	gone := mustCreateProject(t, st, "gone")
	if err := st.SetProjectArchived(context.Background(), gone, true,
		store.AuditEntry{Actor: "test", Action: "project.archive"}); err != nil {
		t.Fatal(err)
	}
	h := DevHandler([]string{devDir(t)}, db)

	var out devProjectsOut
	getJSON(t, h, "/api/projects", &out)
	archived := map[int64]bool{}
	for _, p := range out.Projects {
		archived[p.ProjectID] = p.Archived
	}
	if len(out.Projects) != 2 || archived[live] || !archived[gone] {
		t.Errorf("projects = %+v, want %d live and %d archived", out.Projects, live, gone)
	}
}
