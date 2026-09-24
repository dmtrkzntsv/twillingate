package api

import (
	"reflect"
	"strings"
	"testing"
)

func TestListProjects(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "list_projects", nil)
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, want := range []string{`"project_id":1`, "blog", `"project_id":2`, "docs", "https://blog.example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, `"identity"`) {
		t.Errorf("list_projects still carries an identity field: %s", out)
	}
}

func TestViewsOverviewStitchesAggregatedAndLive(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "views_overview", map[string]any{
		"project_id": 1, "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	// two aggregated days plus the live day from the raw view
	for _, day := range []string{"2026-08-20", "2026-08-21", "2026-08-26"} {
		if !strings.Contains(out, day) {
			t.Errorf("missing day %s in %s", day, out)
		}
	}
	if !strings.Contains(out, "bounce_rate") {
		t.Errorf("no derived bounce_rate in %s", out)
	}
}

func TestViewsOverviewSumsKindsUnlessFiltered(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "views_overview", map[string]any{
		"project_id": 1, "from": "2026-08-20", "to": "2026-08-20"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	// web 10 visitors + app 6 = 16; views 25 + 20 = 45
	if out := textOf(res); !strings.Contains(out, `"16"`) || !strings.Contains(out, `"45"`) {
		t.Errorf("unfiltered overview should sum kinds: %s", out)
	}
	res = callTool(t, cs, "views_overview", map[string]any{
		"project_id": 1, "from": "2026-08-20", "to": "2026-08-20", "kind": "app"})
	if out := textOf(res); !strings.Contains(out, `"6"`) || strings.Contains(out, `"16"`) {
		t.Errorf("kind filter not applied: %s", out)
	}
}

func TestViewsBreakdownLimit(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "views_breakdown", map[string]any{
		"project_id": 1, "from": "2026-08-01", "to": "2026-08-31",
		"dimension": "paths", "limit": 1})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "/post-1") {
		t.Errorf("top page missing: %s", out)
	}
	if strings.Contains(out, "/post-2") {
		t.Errorf("limit 1 not applied: %s", out)
	}
	// invalid dimension is a tool error listing the valid ones
	res = callTool(t, cs, "views_breakdown", map[string]any{
		"project_id": 1, "from": "2026-08-01", "to": "2026-08-31",
		"dimension": "sandwiches"})
	if !res.IsError || !strings.Contains(textOf(res), "paths") {
		t.Errorf("bad dimension: %v %s", res.IsError, textOf(res))
	}
}

func TestViewsBreakdownUTMDimension(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "views_breakdown", map[string]any{
		"project_id": 1, "from": "2026-08-01", "to": "2026-08-31",
		"dimension": "utm"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, want := range []string{"newsletter", "email", "august"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
}

func TestViewsBreakdownEveryDimension(t *testing.T) {
	_, cs := newTestHost(t)
	want := map[string]string{
		"kinds": "web", "paths": "/post-1", "hosts": "blog.example.com", "utm": "newsletter",
		"countries": "US", "platforms": "web", "os": "ios", "browsers": "126", "app_versions": "2.4.1",
		"devices": "iPhone15,3", "displays": "1920x1080",
	}
	for dim, needle := range want {
		res := callTool(t, cs, "views_breakdown", map[string]any{
			"project_id": 1, "from": "2026-08-01", "to": "2026-08-31", "dimension": dim})
		if res.IsError {
			t.Errorf("%s: %s", dim, textOf(res))
			continue
		}
		if out := textOf(res); !strings.Contains(out, needle) {
			t.Errorf("%s: missing %q in %s", dim, needle, out)
		}
	}
	res := callTool(t, cs, "views_breakdown", map[string]any{
		"project_id": 1, "from": "2026-08-01", "to": "2026-08-31", "dimension": "referrers"})
	if res.IsError {
		t.Errorf("referrers with no rows must not error: %s", textOf(res))
	}
	res = callTool(t, cs, "views_breakdown", map[string]any{
		"project_id": 1, "from": "2026-08-01", "to": "2026-08-31", "dimension": "sandwiches"})
	if !res.IsError || !strings.Contains(textOf(res), "app_versions") {
		t.Errorf("invalid dimension should list the valid ones: %v %s", res.IsError, textOf(res))
	}
}

func TestBreakdownEnumMatchesDimensions(t *testing.T) {
	f, _ := reflect.TypeOf(breakdownIn{}).FieldByName("Dimension")
	if want := "one of: " + dimensionNames(); f.Tag.Get("jsonschema") != want {
		t.Errorf("breakdownIn.Dimension jsonschema = %q, want %q", f.Tag.Get("jsonschema"), want)
	}
}

func TestUnknownProjectErrorListsIdsAndNames(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "views_overview", map[string]any{
		"project_id": 7, "from": "2026-08-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("unknown project accepted")
	}
	if msg := textOf(res); !strings.Contains(msg, "unknown project 7; valid projects: 1 (blog), 2 (docs)") {
		t.Fatalf("error = %q, want the id (name) list", msg)
	}
}

// A project spanning two hosts must be able to tell them apart -- the
// reason host is stored at all.
func TestViewsBreakdownHostsDimension(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "views_breakdown", map[string]any{
		"project_id": 1, "from": "2026-08-01", "to": "2026-08-31",
		"dimension": "hosts"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, want := range []string{"blog.example.com", "shop.example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("hosts breakdown missing %q: %s", want, out)
		}
	}
}
