package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func decodeMonitors(t *testing.T, raw []byte) monitorsOut {
	t.Helper()
	var m monitorsOut
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("monitors %q: %v", raw, err)
	}
	return m
}

const (
	monBlog = "https://blog.example.com"
	monShop = "https://shop.example.com"
	monLand = "https://land.example.org"
)

// TestMonitorToolsAndRoutesAgree: list_monitors and update_monitor
// answer the same over MCP and REST, every exact origin is listed, and the audit
// names the transport's actor.
func TestMonitorToolsAndRoutesAgree(t *testing.T) {
	h, cs := newTestHost(t)
	h.monitorInterval = 2 * time.Minute
	h.monitorTimeout = 7 * time.Second
	ctx := context.Background()
	if _, err := h.ops.UpdateProject(ctx, "test", manage.ProjectSpec{ID: 1, AllowedOrigins: []string{monBlog, monShop, monLand, "https://*.example.org", "tauri://app"}}); err != nil {
		t.Fatal(err)
	}
	r := newTestRegistrar(t, h)

	rec := serveREST(t, r, "GET", "/api/projects/1/monitors", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"domains":[]`) {
		t.Errorf("GET = %s, want an empty domains array", rec.Body)
	}
	got := decodeMonitors(t, rec.Body.Bytes())
	var origins []string
	for _, m := range got.Monitors {
		origins = append(origins, m.Origin)
		if m.Enabled {
			t.Errorf("%s is on", m.Origin)
		}
	}
	if !slices.Equal(origins, []string{monBlog, monShop, monLand}) || got.IntervalSeconds != 120 || got.TimeoutSeconds != 7 || got.CheckEverySeconds != 120 {
		t.Errorf("GET = %+v", got)
	}
	viaMCP := decodeMonitors(t, []byte(textOf(callTool(t, cs, "list_monitors", map[string]any{"project_id": 1}))))
	if !reflect.DeepEqual(viaMCP, got) {
		t.Errorf("MCP list = %+v, want %+v", viaMCP, got)
	}

	rec = serveREST(t, r, "PATCH", "/api/projects/1/monitors", `{"origin":"`+monBlog+`","enabled":true,"contains":"Welcome"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d %s", rec.Code, rec.Body)
	}
	got = decodeMonitors(t, rec.Body.Bytes())
	if m := got.Monitors[0]; !m.Enabled || m.Origin != monBlog || m.Contains != "Welcome" {
		t.Errorf("PATCH = %+v", m)
	}
	if got.Monitors[1].Enabled {
		t.Errorf("PATCH turned another on: %+v", got.Monitors[1])
	}
	if got.CheckEverySeconds != 120 {
		t.Errorf("one on: check_every_seconds %d, want 120", got.CheckEverySeconds)
	}
	res := callTool(t, cs, "update_monitor", map[string]any{"project_id": 1, "origin": monBlog, "contains": ""})
	if res.IsError {
		t.Fatalf("update_monitor: %s", textOf(res))
	}
	if m := decodeMonitors(t, []byte(textOf(res))).Monitors[0]; !m.Enabled || m.Contains != "" {
		t.Errorf("MCP update = %+v, want still enabled, contains cleared", m)
	}
	out, err := h.db.Run(ctx, "SELECT actor FROM audit_log WHERE action='monitor.update' AND subject=? ORDER BY rowid", "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 2 || out.Rows[0][0] == "mcp" || out.Rows[1][0] != "mcp" {
		t.Errorf("audit actors = %v, want REST's then mcp", out.Rows)
	}

	// Two more on: the sites take turns on the project's budget, and the domains
	// list what they use, with their origins.
	for _, o := range []string{monShop, monLand} {
		if rec := serveREST(t, r, "PATCH", "/api/projects/1/monitors", `{"origin":"`+o+`","enabled":true}`); rec.Code != http.StatusOK {
			t.Fatalf("PATCH %s = %d %s", o, rec.Code, rec.Body)
		}
	}
	got = decodeMonitors(t, serveREST(t, r, "GET", "/api/projects/1/monitors", "").Body.Bytes())
	if got.CheckEverySeconds != 360 || got.IntervalSeconds != 120 {
		t.Errorf("three on: check_every_seconds %d, interval %d", got.CheckEverySeconds, got.IntervalSeconds)
	}
	want := []monitorDomainOut{
		{Domain: "example.com", Origins: []string{monBlog, monShop}},
		{Domain: "example.org", Origins: []string{monLand}},
	}
	if !reflect.DeepEqual(got.Domains, want) {
		t.Errorf("domains = %+v, want %+v", got.Domains, want)
	}
	if v := decodeMonitors(t, []byte(textOf(callTool(t, cs, "list_monitors", map[string]any{"project_id": 1})))); !reflect.DeepEqual(v, got) {
		t.Errorf("MCP list = %+v, want %+v", v, got)
	}
}

func TestMonitorRefusals(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	if rec := serveREST(t, r, "PATCH", "/api/projects/1/monitors", `{"origin":"https://evil.example"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("foreign origin = %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := serveREST(t, r, "PATCH", "/api/projects/1/monitors", `{"enabled":true}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no origin = %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := serveREST(t, r, "GET", "/api/projects/99/monitors", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project GET = %d, want 404", rec.Code)
	}
	if rec := serveREST(t, r, "PATCH", "/api/projects/99/monitors", `{"origin":"https://a.example","enabled":false}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project PATCH = %d, want 404", rec.Code)
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{{"update_monitor", map[string]any{"project_id": 99, "origin": "https://a.example", "enabled": false}}, {"list_monitors", map[string]any{"project_id": 99}}} {
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

// TestMonitorReportsReadings: what the runner stored reads back through
// list_monitors, over REST and MCP: a monitor's redirect count, its domain's date.
func TestMonitorReportsReadings(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	if _, err := h.ops.UpdateProject(ctx, "test", manage.ProjectSpec{ID: 1, AllowedOrigins: []string{monBlog}}); err != nil {
		t.Fatal(err)
	}
	r := newTestRegistrar(t, h)
	if rec := serveREST(t, r, "PATCH", "/api/projects/1/monitors", `{"origin":"`+monBlog+`","enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d %s", rec.Code, rec.Body)
	}
	if got := decodeMonitors(t, serveREST(t, r, "GET", "/api/projects/1/monitors", "").Body.Bytes()); got.Monitors[0].Redirects != 0 {
		t.Errorf("fresh monitor: redirects %d, want 0", got.Monitors[0].Redirects)
	}
	// What the runner does after a check: its own handle on the same file.
	st, err := store.Open("sqlite://" + testDBPaths[h])
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMonitorRedirects(ctx, 1, monBlog, 3); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMonitorDomain(ctx, 1, "example.com", store.DomainState{ExpiresAt: "2027-08-13", CheckedAt: "2026-11-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	rec := serveREST(t, r, "GET", "/api/projects/1/monitors", "")
	if !strings.Contains(rec.Body.String(), `"redirects":3`) || decodeMonitors(t, rec.Body.Bytes()).Monitors[0].Redirects != 3 {
		t.Errorf("GET = %s, want redirects 3", rec.Body)
	}
	want := []monitorDomainOut{{Domain: "example.com", Origins: []string{monBlog}, ExpiresAt: "2027-08-13", CheckedAt: "2026-11-01T00:00:00Z"}}
	m := decodeMonitors(t, []byte(textOf(callTool(t, cs, "list_monitors", map[string]any{"project_id": 1}))))
	if m.Monitors[0].Redirects != 3 || !reflect.DeepEqual(m.Domains, want) {
		t.Errorf("MCP list = %+v, want redirects 3 and %+v", m, want)
	}
}

// TestCheckMonitor: check_monitor answers the same over MCP and REST at once,
// with the request on that monitor, and refuses over both as the others do.
func TestCheckMonitor(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	if _, err := h.ops.UpdateProject(ctx, "test", manage.ProjectSpec{ID: 1, AllowedOrigins: []string{monBlog, monShop}}); err != nil {
		t.Fatal(err)
	}
	r := newTestRegistrar(t, h)
	const path = "/api/projects/1/monitors/check"
	body := `{"origin":"` + monBlog + `"}`
	args := map[string]any{"project_id": 1, "origin": monBlog}

	// Not on: 400 and an MCP error.
	rec := serveREST(t, r, "POST", path, body)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "turn the monitor on first") {
		t.Errorf("not on = %d %s, want 400", rec.Code, rec.Body)
	}
	if res := callTool(t, cs, "check_monitor", args); !res.IsError || !strings.Contains(textOf(res), "turn the monitor on first") {
		t.Errorf("MCP not on = %s", textOf(res))
	}
	// A foreign origin: 400.
	if rec := serveREST(t, r, "POST", path, `{"origin":"https://evil.example"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("foreign = %d %s, want 400", rec.Code, rec.Body)
	}
	// Unknown project: 404.
	if rec := serveREST(t, r, "POST", "/api/projects/99/monitors/check", body); rec.Code != http.StatusNotFound {
		t.Errorf("unknown = %d %s, want 404", rec.Code, rec.Body)
	}
	if res := callTool(t, cs, "check_monitor", map[string]any{"project_id": 99, "origin": monBlog}); !res.IsError || !strings.Contains(textOf(res), "unknown project 99") {
		t.Errorf("MCP unknown = %s", textOf(res))
	}

	on := true
	for _, o := range []string{monBlog, monShop} {
		if _, err := h.ops.SetMonitor(ctx, "test", 1, o, manage.MonitorSpec{Enabled: &on}); err != nil {
			t.Fatal(err)
		}
	}
	rec = serveREST(t, r, "POST", path, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	all := decodeMonitors(t, rec.Body.Bytes())
	got := all.Monitors[0]
	if got.Check.RequestedAt == "" || got.Check.FinishedAt != "" || !got.Enabled {
		t.Errorf("POST = %+v, want it requested and not finished", got)
	}
	if all.Monitors[1].Check.RequestedAt != "" {
		t.Errorf("POST asked the other monitor too: %+v", all.Monitors[1])
	}
	// Pending: 429 over REST, an error over MCP, on this monitor only.
	if rec := serveREST(t, r, "POST", path, body); rec.Code != http.StatusTooManyRequests {
		t.Errorf("pending = %d %s, want 429", rec.Code, rec.Body)
	}
	if res := callTool(t, cs, "check_monitor", args); !res.IsError {
		t.Errorf("MCP pending = %s", textOf(res))
	}
	if res := callTool(t, cs, "check_monitor", map[string]any{"project_id": 1, "origin": monShop}); res.IsError {
		t.Errorf("MCP the other monitor = %s", textOf(res))
	}

	// The result reads back through list_monitors, the same both ways.
	st, err := store.Open("sqlite://" + testDBPaths[h])
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.FinishMonitorCheck(ctx, 1, monBlog, store.MonitorCheckResult{
		FinishedAt: got.Check.RequestedAt, Status: 500, Reason: "http_5xx", Error: "status 500", MS: 42}); err != nil {
		t.Fatal(err)
	}
	want := monitorCheckOut{RequestedAt: got.Check.RequestedAt, FinishedAt: got.Check.RequestedAt, Status: 500, Reason: "http_5xx", Error: "status 500", MS: 42}
	rec = serveREST(t, r, "GET", "/api/projects/1/monitors", "")
	if c := decodeMonitors(t, rec.Body.Bytes()).Monitors[0].Check; c != want {
		t.Errorf("GET check = %+v, want %+v", c, want)
	}
	if c := decodeMonitors(t, []byte(textOf(callTool(t, cs, "list_monitors", map[string]any{"project_id": 1})))).Monitors[0].Check; c != want {
		t.Errorf("MCP check = %+v, want %+v", c, want)
	}
	// Finished, but within the cooldown: still 429, with the server's message.
	rec = serveREST(t, r, "POST", path, body)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "try again in") {
		t.Errorf("cooldown = %d %s, want 429", rec.Code, rec.Body)
	}
}
