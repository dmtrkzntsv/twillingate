package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func decodeMonitor(t *testing.T, raw []byte) monitorOut {
	t.Helper()
	var m monitorOut
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("monitor %q: %v", raw, err)
	}
	return m
}

// TestMonitorToolsAndRoutesAgree: get_monitor and update_monitor answer the
// same over MCP and REST, eligible origins are the exact ones, and the
// audit names the transport's actor.
func TestMonitorToolsAndRoutesAgree(t *testing.T) {
	h, cs := newTestHost(t)
	h.monitorInterval = 2 * time.Minute
	h.monitorTimeout = 7 * time.Second
	ctx := context.Background()
	if _, err := h.ops.UpdateProject(ctx, "test", manage.ProjectSpec{ID: 1, AllowedOrigins: []string{"https://blog.example.com", "https://*.example.org", "tauri://app"}}); err != nil {
		t.Fatal(err)
	}
	r := newTestRegistrar(t, h)

	rec := serveREST(t, r, "GET", "/api/projects/1/monitor", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body)
	}
	got := decodeMonitor(t, rec.Body.Bytes())
	if got.ProjectID != 1 || got.Enabled || got.IntervalSeconds != 120 || got.TimeoutSeconds != 7 ||
		!slices.Equal(got.EligibleOrigins, []string{"https://blog.example.com"}) {
		t.Errorf("GET = %+v", got)
	}
	viaMCP := decodeMonitor(t, []byte(textOf(callTool(t, cs, "get_monitor", map[string]any{"project_id": 1}))))
	if viaMCP.ProjectID != 1 || viaMCP.IntervalSeconds != 120 || viaMCP.TimeoutSeconds != 7 || !slices.Equal(viaMCP.EligibleOrigins, got.EligibleOrigins) {
		t.Errorf("MCP get = %+v", viaMCP)
	}

	rec = serveREST(t, r, "PATCH", "/api/projects/1/monitor", `{"enabled":true,"contains":"Welcome"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d %s", rec.Code, rec.Body)
	}
	got = decodeMonitor(t, rec.Body.Bytes())
	if !got.Enabled || got.Origin != "https://blog.example.com" || got.Contains != "Welcome" {
		t.Errorf("PATCH = %+v, want enabled on the only origin", got)
	}
	res := callTool(t, cs, "update_monitor", map[string]any{"project_id": 1, "contains": ""})
	if res.IsError {
		t.Fatalf("update_monitor: %s", textOf(res))
	}
	if m := decodeMonitor(t, []byte(textOf(res))); !m.Enabled || m.Contains != "" {
		t.Errorf("MCP update = %+v, want still enabled, contains cleared", m)
	}
	out, err := h.db.Run(ctx, "SELECT actor FROM audit_log WHERE action='monitor.update' AND subject=? ORDER BY rowid", "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 2 || out.Rows[0][0] == "mcp" || out.Rows[1][0] != "mcp" {
		t.Errorf("audit actors = %v, want REST's then mcp", out.Rows)
	}
}

func TestMonitorRefusals(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	if rec := serveREST(t, r, "PATCH", "/api/projects/1/monitor", `{"origin":"https://evil.example"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("foreign origin = %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := serveREST(t, r, "GET", "/api/projects/99/monitor", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project GET = %d, want 404", rec.Code)
	}
	if rec := serveREST(t, r, "PATCH", "/api/projects/99/monitor", `{"enabled":false}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project PATCH = %d, want 404", rec.Code)
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{{"update_monitor", map[string]any{"project_id": 99, "enabled": false}}, {"get_monitor", map[string]any{"project_id": 99}}} {
		res := callTool(t, cs, c.tool, c.args)
		if !res.IsError || !strings.Contains(textOf(res), "unknown project 99") {
			t.Errorf("%s unknown = %s", c.tool, textOf(res))
		}
	}
}

// TestUsageIgnoresChecks: check rows are not the site's traffic: usage's
// samples and last_received_at and limits' events_30d stay as they were.
func TestUsageIgnoresChecks(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := context.Background()
	in := usageIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-30"}}
	before, err := h.usage(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	limBefore, err := h.listLimits(ctx, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	ms := 120.0
	check := func(ts time.Time) store.Event {
		return store.Event{Family: store.FamilyChecks, ProjectID: 1, EventName: "uptime", Value: &ms,
			Measure: store.MeasureTime, TS: ts, Attributes: map[string]string{"up": "1", "status": "200"}}
	}
	// A check in a month after the template's last event, and today's: the
	// newest file now holds nothing but checks.
	writeHostEvents(t, h, check(time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)), check(time.Now().UTC()))
	h.events = h.newHostEventCount()
	after, err := h.usage(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	b, a := before.Projects[0], after.Projects[0]
	if a.Totals != b.Totals || b.LastReceivedAt == nil || deref(a.LastReceivedAt) != deref(b.LastReceivedAt) {
		t.Errorf("usage changed by checks: %+v %v -> %+v %v", b.Totals, deref(b.LastReceivedAt), a.Totals, deref(a.LastReceivedAt))
	}
	act := getActivity(t, h)
	if len(act) == 0 || act[0].ProjectID != 1 || act[0].LastEventDay == nil || *act[0].LastEventDay != "2026-08-26" {
		t.Errorf("project-activity last_event_day = %+v, want 2026-08-26, the newest real event", act)
	}
	limAfter, err := h.listLimits(ctx, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if limBefore.Events30d == nil || limAfter.Events30d == nil || limAfter.Events30d.Count != limBefore.Events30d.Count {
		t.Errorf("events_30d %+v -> %+v, want unchanged", limBefore.Events30d, limAfter.Events30d)
	}
}

// TestMonitorReportsRedirects: the last check's redirect count is in
// get_monitor, over REST and MCP.
func TestMonitorReportsRedirects(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	if _, err := h.ops.UpdateProject(ctx, "test", manage.ProjectSpec{ID: 1, AllowedOrigins: []string{"https://blog.example.com"}}); err != nil {
		t.Fatal(err)
	}
	r := newTestRegistrar(t, h)
	if rec := serveREST(t, r, "PATCH", "/api/projects/1/monitor", `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d %s", rec.Code, rec.Body)
	}
	if got := decodeMonitor(t, serveREST(t, r, "GET", "/api/projects/1/monitor", "").Body.Bytes()); got.Redirects != 0 {
		t.Errorf("fresh monitor: redirects %d, want 0", got.Redirects)
	}
	// What the runner does after a check: its own handle on the same file.
	st, err := store.Open("sqlite://" + testDBPaths[h])
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMonitorRedirects(ctx, 1, "https://blog.example.com", 3); err != nil {
		t.Fatal(err)
	}
	rec := serveREST(t, r, "GET", "/api/projects/1/monitor", "")
	if !strings.Contains(rec.Body.String(), `"redirects":3`) || decodeMonitor(t, rec.Body.Bytes()).Redirects != 3 {
		t.Errorf("GET = %s, want redirects 3", rec.Body)
	}
	if m := decodeMonitor(t, []byte(textOf(callTool(t, cs, "get_monitor", map[string]any{"project_id": 1})))); m.Redirects != 3 {
		t.Errorf("MCP get = %+v, want redirects 3", m)
	}
}

// TestCheckMonitor: check_monitor answers the same over MCP and REST at once,
// with the request on the monitor, and refuses over both as the others do.
func TestCheckMonitor(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	if _, err := h.ops.UpdateProject(ctx, "test", manage.ProjectSpec{ID: 1, AllowedOrigins: []string{"https://blog.example.com"}}); err != nil {
		t.Fatal(err)
	}
	r := newTestRegistrar(t, h)

	// Not on: 400 and an MCP error.
	rec := serveREST(t, r, "POST", "/api/projects/1/monitor/check", "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "turn the monitor on first") {
		t.Errorf("not on = %d %s, want 400", rec.Code, rec.Body)
	}
	if res := callTool(t, cs, "check_monitor", map[string]any{"project_id": 1}); !res.IsError || !strings.Contains(textOf(res), "turn the monitor on first") {
		t.Errorf("MCP not on = %s", textOf(res))
	}
	// Unknown project: 404.
	if rec := serveREST(t, r, "POST", "/api/projects/99/monitor/check", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown = %d %s, want 404", rec.Code, rec.Body)
	}
	if res := callTool(t, cs, "check_monitor", map[string]any{"project_id": 99}); !res.IsError || !strings.Contains(textOf(res), "unknown project 99") {
		t.Errorf("MCP unknown = %s", textOf(res))
	}

	on := true
	if _, err := h.ops.SetMonitor(ctx, "test", 1, manage.MonitorSpec{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	rec = serveREST(t, r, "POST", "/api/projects/1/monitor/check", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	got := decodeMonitor(t, rec.Body.Bytes())
	if got.Check.RequestedAt == "" || got.Check.FinishedAt != "" || !got.Enabled {
		t.Errorf("POST = %+v, want it requested and not finished", got)
	}
	// Pending: 429 over REST, an error over MCP.
	if rec := serveREST(t, r, "POST", "/api/projects/1/monitor/check", ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("pending = %d %s, want 429", rec.Code, rec.Body)
	}
	if res := callTool(t, cs, "check_monitor", map[string]any{"project_id": 1}); !res.IsError {
		t.Errorf("MCP pending = %s", textOf(res))
	}

	// The result reads back through get_monitor, the same both ways.
	st, err := store.Open("sqlite://" + testDBPaths[h])
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.FinishMonitorCheck(ctx, 1, "https://blog.example.com", store.MonitorCheckResult{
		FinishedAt: got.Check.RequestedAt, Status: 500, Reason: "http_5xx", Error: "status 500", MS: 42}); err != nil {
		t.Fatal(err)
	}
	want := monitorCheckOut{RequestedAt: got.Check.RequestedAt, FinishedAt: got.Check.RequestedAt, Status: 500, Reason: "http_5xx", Error: "status 500", MS: 42}
	rec = serveREST(t, r, "GET", "/api/projects/1/monitor", "")
	if c := decodeMonitor(t, rec.Body.Bytes()).Check; c != want {
		t.Errorf("GET check = %+v, want %+v", c, want)
	}
	if c := decodeMonitor(t, []byte(textOf(callTool(t, cs, "get_monitor", map[string]any{"project_id": 1})))).Check; c != want {
		t.Errorf("MCP check = %+v, want %+v", c, want)
	}
	// Finished, but within the cooldown: still 429, with the server's message.
	rec = serveREST(t, r, "POST", "/api/projects/1/monitor/check", "")
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "try again in") {
		t.Errorf("cooldown = %d %s, want 429", rec.Code, rec.Body)
	}
}
