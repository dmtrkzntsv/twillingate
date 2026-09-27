package reporting

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
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

// getJSON runs a GET against h and decodes the JSON body into out.
func getJSON(t *testing.T, h http.Handler, path string, out any) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
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
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/dashboards/1001/view", nil))
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
