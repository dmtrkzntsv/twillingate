package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// trialAlert is create_alert's arguments for an event_count alert on
// trial_started, which the test projects never sent (they do send
// signup).
func trialAlert(name string) map[string]any {
	return map[string]any{"project_id": 1, "name": name, "template": "event_count",
		"fields":    map[string]any{"event": "trial_started"},
		"condition": map[string]any{"type": "threshold", "op": "below", "value": 5}}
}

// The eight tools over MCP: an alert made from a template, listed,
// renamed, asked for a check, archived, restored, and its empty history.
func TestAlertToolsOverMCP(t *testing.T) {
	_, cs := newTestHost(t)
	created := callAs[alertOut](t, cs, "create_alert", trialAlert("Trials"))
	if created.State != store.AlertPending || created.Kind != "stat" || !created.CheckPending || created.CheckRequestedAt == "" ||
		created.Template != "event_count" || created.Fields["event"] != "trial_started" || created.WindowDays != 1 || created.Read.Metric != "count" {
		t.Errorf("create_alert = %+v", created)
	}
	if len(created.Warnings) != 1 || !strings.Contains(created.Warnings[0], "`trial_started`") {
		t.Errorf("warnings = %q, want one naming trial_started", created.Warnings)
	}
	if created.Condition.Type != "threshold" || created.Condition.Op != "below" || created.Condition.Value == nil || *created.Condition.Value != 5 {
		t.Errorf("condition = %+v", created.Condition)
	}
	id := map[string]any{"project_id": 1, "id": created.ID}

	list := callAs[alertsOut](t, cs, "list_alerts", map[string]any{"project_id": 1})
	if list.MaxLive != 100 || len(list.Alerts) != 1 || list.Alerts[0].ID != created.ID || list.Alerts[0].Warnings != nil {
		t.Errorf("list_alerts = %+v", list)
	}
	if list.Alerts[0].Unit != "count" {
		t.Errorf("unit = %q, want count", list.Alerts[0].Unit)
	}

	renamed := callAs[alertOut](t, cs, "update_alert", map[string]any{"project_id": 1, "id": created.ID, "name": "Trials/day"})
	if renamed.Name != "Trials/day" || !renamed.CheckPending || renamed.CheckRequestedAt != created.CheckRequestedAt || renamed.Warnings != nil {
		t.Errorf("update_alert (rename) = %+v", renamed)
	}

	checked := callAs[alertOut](t, cs, "check_alert", id)
	if checked.CheckRequestedAt != created.CheckRequestedAt || !checked.CheckPending {
		t.Errorf("check_alert = %+v, want the pending request kept (%s)", checked, created.CheckRequestedAt)
	}

	archived := callAs[alertOut](t, cs, "archive_alert", id)
	if archived.ArchivedAt == "" {
		t.Errorf("archive_alert = %+v", archived)
	}
	if l := callAs[alertsOut](t, cs, "list_alerts", map[string]any{"project_id": 1}); len(l.Alerts) != 0 {
		t.Errorf("list_alerts after archive = %+v", l)
	}
	if l := callAs[alertsOut](t, cs, "list_alerts", map[string]any{"project_id": 1, "archived": true}); len(l.Alerts) != 1 || l.Alerts[0].ID != created.ID {
		t.Errorf("list_alerts archived = %+v", l)
	}
	restored := callAs[alertOut](t, cs, "restore_alert", id)
	if restored.ArchivedAt != "" || restored.State != store.AlertPending {
		t.Errorf("restore_alert = %+v", restored)
	}
	if l := callAs[alertsOut](t, cs, "list_alerts", map[string]any{"project_id": 1}); len(l.Alerts) != 1 {
		t.Errorf("list_alerts after restore = %+v", l)
	}

	raw := textOf(callTool(t, cs, "alert_history", id))
	if !strings.Contains(raw, `"evaluations":[]`) || strings.Contains(raw, "next_cursor") {
		t.Errorf("alert_history = %s, want no evaluation and no next page", raw)
	}
}

// The routes answer 201 on create and map the refusals: a taken name
// 409, another project's alert 404, a bad condition or cursor 400.
func TestAlertRoutes(t *testing.T) {
	h := newBareHost(t)
	r := newTestRegistrar(t, h)
	body := `{"name":"Trials","template":"event_count","fields":{"event":"trial_started"},"condition":{"type":"threshold","op":"below","value":5}}`
	rec := serveREST(t, r, "POST", "/api/projects/1/alerts", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	var a alertOut
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil || a.ID == 0 || a.ProjectID != 1 {
		t.Fatalf("POST = %s (%v)", rec.Body, err)
	}
	alert := "/api/projects/1/alerts/" + strconv.FormatInt(a.ID, 10)

	if rec := serveREST(t, r, "POST", "/api/projects/1/alerts", body); rec.Code != http.StatusConflict || errorCode(t, rec) != "conflict" {
		t.Errorf("a taken name = %d %s, want 409 conflict", rec.Code, rec.Body)
	}
	if rec := serveREST(t, r, "PATCH", "/api/projects/2/alerts/"+strconv.FormatInt(a.ID, 10), `{"name":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("another project's alert = %d %s, want 404", rec.Code, rec.Body)
	}
	bad := `{"name":"Other","template":"event_count","fields":{"event":"trial_started"},"condition":{"type":"threshold","op":"sideways","value":5}}`
	if rec := serveREST(t, r, "POST", "/api/projects/1/alerts", bad); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid" {
		t.Errorf("a bad condition = %d %s, want 400 invalid", rec.Code, rec.Body)
	}
	if rec := serveREST(t, r, "GET", alert+"/history?cursor=x", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad cursor = %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := serveREST(t, r, "GET", alert+"/history?limit=5", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"evaluations":[]`) {
		t.Errorf("history = %d %s", rec.Code, rec.Body)
	}
	if rec := serveREST(t, r, "GET", "/api/projects/99/alerts", ""); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "valid projects") {
		t.Errorf("an unknown project = %d %s, want 404 listing the projects", rec.Code, rec.Body)
	}
	for _, op := range []string{"check", "archive", "restore"} {
		if rec := serveREST(t, r, "POST", alert+"/"+op, ""); rec.Code != http.StatusOK {
			t.Errorf("POST %s = %d %s", op, rec.Code, rec.Body)
		}
	}
}

// preview_alert backtests a definition over the seeded files, 90 days
// ending yesterday, or a saved alert by id; both at once are refused.
func TestPreviewAlertTool(t *testing.T) {
	_, cs := newTestHost(t)
	def := map[string]any{"project_id": 1, "template": "event_count", "fields": map[string]any{"event": "$page_view"},
		"condition": map[string]any{"type": "threshold", "op": "below", "value": 1}}
	yesterday := civil.Today(time.Now()).AddDays(-1)
	check := func(name string, out alertPreviewOut) {
		t.Helper()
		if len(out.Days) != 90 || out.Days[89].Day != yesterday.String() || out.Days[0].Day != yesterday.AddDays(-89).String() || out.Gaps == nil {
			t.Fatalf("%s = %d days, %+v … %+v, gaps %v", name, len(out.Days), out.Days[0], out.Days[len(out.Days)-1], out.Gaps)
		}
		for _, d := range out.Days {
			if d.Outcome != store.AlertOK && d.Outcome != store.AlertBreached && d.Outcome != store.AlertNoData {
				t.Errorf("%s: day %+v has no outcome", name, d)
			}
		}
	}
	check("a definition", callAs[alertPreviewOut](t, cs, "preview_alert", def))

	def["name"] = "Views"
	created := callAs[alertOut](t, cs, "create_alert", def)
	check("by id", callAs[alertPreviewOut](t, cs, "preview_alert", map[string]any{"project_id": 1, "id": created.ID}))

	def["id"] = created.ID
	if msg := refused(t, cs, "preview_alert", def); !strings.Contains(msg, "not both") {
		t.Errorf("a definition and an id = %s", msg)
	}
}

// The other sources and the edits a template alert does not reach: a
// silence alert over where, a stat given as query and read, a snooze set
// and cleared, a pause.
func TestAlertSourcesAndSnooze(t *testing.T) {
	_, cs := newTestHost(t)
	silence := callAs[alertOut](t, cs, "create_alert", map[string]any{"project_id": 1, "name": "Heartbeat",
		"where":     []any{map[string]any{"column": "event_name", "op": "eq", "values": []any{"$page_view"}}},
		"condition": map[string]any{"type": "silence", "hours": 6}})
	if silence.Kind != "silence" || len(silence.Where) != 1 || silence.Where[0].Column != "event_name" || silence.Condition.Hours != 6 || silence.Template != "" {
		t.Errorf("silence alert = %+v", silence)
	}

	stat := callAs[alertOut](t, cs, "create_alert", map[string]any{"project_id": 1, "name": "Events",
		"query":       map[string]any{"kind": "stat", "table": "events", "metrics": []any{map[string]any{"op": "count"}}},
		"read":        map[string]any{"metric": "count"},
		"condition":   map[string]any{"type": "change", "direction": "drop", "percent": 30, "against": "week_ago"},
		"window_days": 7, "min_volume": 10})
	if stat.Kind != "stat" || stat.WindowDays != 7 || stat.MinVolume != 10 || stat.Read.Metric != "count" || stat.Where != nil ||
		stat.Condition.Against != "week_ago" || !strings.Contains(string(stat.Query), `"kind":"stat"`) {
		t.Errorf("query alert = %+v", stat)
	}

	until := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	snoozed := callAs[alertOut](t, cs, "update_alert", map[string]any{"project_id": 1, "id": stat.ID, "snoozed_until": until})
	if snoozed.State != store.AlertSnoozed || snoozed.SnoozedUntil != until {
		t.Errorf("snoozed = %+v, want snoozed until %s", snoozed, until)
	}
	woken := callAs[alertOut](t, cs, "update_alert", map[string]any{"project_id": 1, "id": stat.ID, "snoozed_until": nil})
	if woken.State == store.AlertSnoozed || woken.SnoozedUntil != "" {
		t.Errorf("snooze cleared = %+v", woken)
	}
	if msg := refused(t, cs, "update_alert", map[string]any{"project_id": 1, "id": stat.ID, "snoozed_until": "tomorrow"}); !strings.Contains(msg, "RFC 3339") {
		t.Errorf("a bad snoozed_until = %s", msg)
	}
	paused := callAs[alertOut](t, cs, "update_alert", map[string]any{"project_id": 1, "id": stat.ID, "paused": true})
	if paused.State != store.AlertPaused || !paused.Paused {
		t.Errorf("paused = %+v", paused)
	}

	// A new definition is checked again and warned about.
	edited := callAs[alertOut](t, cs, "update_alert", map[string]any{"project_id": 1, "id": silence.ID,
		"template": "no_events", "fields": map[string]any{"event": "trial_started"}, "condition": map[string]any{"type": "silence", "hours": 12}})
	if edited.Template != "no_events" || edited.Condition.Hours != 12 || len(edited.Warnings) != 1 {
		t.Errorf("edited = %+v", edited)
	}
	widened := callAs[alertOut](t, cs, "update_alert", map[string]any{"project_id": 1, "id": stat.ID,
		"query": map[string]any{"kind": "stat", "table": "events", "metrics": []any{map[string]any{"op": "actors"}}},
		"read":  map[string]any{"metric": "actors"}, "window_days": 3, "min_volume": 0})
	if widened.Read.Metric != "actors" || widened.WindowDays != 3 || widened.MinVolume != 0 {
		t.Errorf("widened = %+v", widened)
	}
	moved := callAs[alertOut](t, cs, "update_alert", map[string]any{"project_id": 1, "id": stat.ID,
		"where": []any{}, "condition": map[string]any{"type": "silence", "hours": 2}, "window_days": 1, "min_volume": 0})
	if moved.Kind != "silence" {
		t.Errorf("moved to silence = %+v", moved)
	}
}

// A process built without alerts refuses every alert tool as invalid.
func TestAlertToolsWithoutAlerts(t *testing.T) {
	h := newBareHost(t)
	h.alerts = nil
	ctx := context.Background()
	want := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), "not available") {
			t.Errorf("%s = %v, want invalid: not available", name, err)
		}
	}
	_, err := h.listAlerts(ctx, listAlertsIn{ProjectID: 1})
	want("list_alerts", err)
	_, err = h.createAlert(ctx, createAlertIn{ProjectID: 1})
	want("create_alert", err)
	_, err = h.updateAlert(ctx, updateAlertIn{ProjectID: 1, ID: 1})
	want("update_alert", err)
	_, err = h.previewAlert(ctx, previewAlertIn{ProjectID: 1, ID: 1})
	want("preview_alert", err)
	_, err = h.archiveAlert(ctx, alertIDIn{ProjectID: 1, ID: 1})
	want("archive_alert", err)
	_, err = h.restoreAlert(ctx, alertIDIn{ProjectID: 1, ID: 1})
	want("restore_alert", err)
	_, err = h.checkAlert(ctx, alertIDIn{ProjectID: 1, ID: 1})
	want("check_alert", err)
	_, err = h.alertHistory(ctx, alertHistoryIn{ProjectID: 1, ID: 1})
	want("alert_history", err)
}

// A funnel alert reads a fraction, and list_alerts says so in unit.
func TestListAlertsUnit(t *testing.T) {
	_, cs := newTestHost(t)
	callAs[alertOut](t, cs, "create_alert", map[string]any{"project_id": 1, "name": "Signup to trial", "template": "conversion",
		"fields":    map[string]any{"from_event": "signup", "to_event": "trial_started"},
		"condition": map[string]any{"type": "threshold", "op": "below", "value": 0.2}})
	list := callAs[alertsOut](t, cs, "list_alerts", map[string]any{"project_id": 1})
	if len(list.Alerts) != 1 || list.Alerts[0].Kind != "funnel" || list.Alerts[0].Unit != "fraction" {
		t.Errorf("list_alerts = %+v, want one funnel alert with unit fraction", list.Alerts)
	}
}
