package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// schemaAt walks a decoded JSON schema along keys (object keys only).
func schemaAt(t *testing.T, v any, keys ...string) map[string]any {
	t.Helper()
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("at %s: %T is not an object", k, v)
		}
		v = m[k]
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%v: %T is not an object", keys, v)
	}
	return m
}

// widgetSchemaOK checks one widget schema carries the contract: the
// component enum and one allOf rule per component.
func widgetSchemaOK(t *testing.T, tool string, w map[string]any) {
	t.Helper()
	enum, _ := schemaAt(t, w, "properties", "component")["enum"].([]any)
	if !containsAny(enum, "stat") || !containsAny(enum, "markdown") {
		t.Errorf("%s: component enum = %v", tool, enum)
	}
	if rules, _ := w["allOf"].([]any); len(rules) != len(enum) && len(rules) != len(enum)-1 {
		t.Errorf("%s: %d allOf rules for %d enum values", tool, len(rules), len(enum))
	}
	if max := schemaAt(t, w, "properties", "width")["maximum"]; max != 12.0 {
		t.Errorf("%s: width maximum = %v", tool, max)
	}
}

func containsAny(vs []any, want any) bool {
	for _, v := range vs {
		if v == want {
			return true
		}
	}
	return false
}

// TestWidgetToolsCarryTheContract: tools/list shows the component enum,
// size bounds and per-component rules on all three widget-taking tools.
func TestWidgetToolsCarryTheContract(t *testing.T) {
	_, cs := newTestHost(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range res.Tools {
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var in any
		if err := json.Unmarshal(b, &in); err != nil {
			t.Fatal(err)
		}
		switch tool.Name {
		case "add_widget", "update_widget":
			widgetSchemaOK(t, tool.Name, in.(map[string]any))
			seen++
		case "create_dashboard":
			widgetSchemaOK(t, tool.Name, schemaAt(t, in, "properties", "widgets", "items"))
			seen++
		}
	}
	if seen != 3 {
		t.Errorf("found %d of the 3 widget tools", seen)
	}
}

// TestWidgetSchemaRefusesBeforeWriting: over MCP a bad prop is refused by
// the input schema and nothing is written; update_widget without a
// component is still refused, by the server.
func TestWidgetSchemaRefusesBeforeWriting(t *testing.T) {
	_, cs := newTestHost(t)
	var d struct {
		ID      int64 `json:"dashboard_id"`
		Widgets []any `json:"widgets"`
	}
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Schema"}, &d)

	res := callTool(t, cs, "add_widget", map[string]any{
		"dashboard_id": d.ID, "component": "stat", "props": map[string]any{"format": "pct"},
		"source": map[string]any{"type": "sql", "content": visitorsSQL},
	})
	if !res.IsError || !strings.Contains(textOf(res), "pct") {
		t.Fatalf("add_widget with a bad prop = %s", textOf(res))
	}
	toolJSON(t, cs, "get_dashboard", map[string]any{"dashboard_id": d.ID}, &d)
	if len(d.Widgets) != 0 {
		t.Fatalf("refused add_widget wrote %d widgets", len(d.Widgets))
	}

	var w struct {
		ID int64 `json:"widget_id"`
	}
	toolJSON(t, cs, "add_widget", map[string]any{
		"dashboard_id": d.ID, "component": "stat",
		"source": map[string]any{"type": "sql", "content": visitorsSQL},
	}, &w)
	res = callTool(t, cs, "update_widget", map[string]any{"widget_id": w.ID, "props": map[string]any{"curve": "step"}})
	if !res.IsError || !strings.Contains(textOf(res), "curve") {
		t.Errorf("update_widget without component, bad prop = %s", textOf(res))
	}
	toolJSON(t, cs, "update_widget", map[string]any{"widget_id": w.ID, "component": nil, "width": 4}, nil)

	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "No widgets", "widgets": nil}, nil)
}

// TestWidgetSchemaLeavesRESTToTheServer: REST bodies are not checked
// against the input schema, so width 0 still means the default and a bad
// prop gets the service's refusal.
func TestWidgetSchemaLeavesRESTToTheServer(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	var d struct {
		ID int64 `json:"dashboard_id"`
	}
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "REST"}, &d)
	target := "/api/dashboards/" + strconv.FormatInt(d.ID, 10) + "/widgets"
	src := `"source":{"type":"sql","content":"SELECT 1 AS value"}`

	rec := serveREST(t, r, "POST", target, `{"component":"stat","width":0,`+src+`}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("POST width 0 = %d %s", rec.Code, rec.Body.String())
	}
	rec = serveREST(t, r, "POST", target, `{"component":"stat","props":{"format":"pct"},`+src+`}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "stat: ") {
		t.Errorf("POST bad prop = %d %s", rec.Code, rec.Body.String())
	}
}

// TestOpenAPICarriesTheWidgetContract: /api/doc's add_widget body has the
// component enum and the per-component rules.
func TestOpenAPICarriesTheWidgetContract(t *testing.T) {
	h, _ := newTestHost(t)
	raw, err := openAPI(newTestRegistrar(t, h).specs)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	body := schemaAt(t, doc, "paths", "/api/dashboards/{dashboard_id}/widgets", "post",
		"requestBody", "content", "application/json", "schema")
	widgetSchemaOK(t, "POST widgets", body)
}
