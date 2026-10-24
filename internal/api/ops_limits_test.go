package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/wire"
)

// limits reports retention, the late window and the wire format's fixed
// limits, in that order: settings with their value in force (0 included)
// and default, fixed limits from internal/wire with neither setting nor
// default.
func TestLimitsReportsTheLimitsInForce(t *testing.T) {
	h, cs := newTestHost(t)
	cfg := &config.Config{IngestLateDays: 2, Forms: config.FormsConfig{DraftDays: 3}}
	cfg.Retention.Days = 730
	h.limits = limitsFrom(cfg)
	out, err := h.listLimits(context.Background(), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		group, setting string
		value, df      float64
	}
	settings := []want{
		{groupRetention, "RETENTION_DAYS", 730, config.DefaultRetentionDays},
		{groupRetention, "RETENTION_ARCHIVED_DAYS", 0, config.DefaultArchivedDays},
		{groupRetention, "RETENTION_FORMS_DRAFT_DAYS", 3, config.DefaultFormDraftDays},
		{groupIngest, "INGEST_LATE_DAYS", 2, config.DefaultIngestLateDays},
	}
	if len(out.Limits) <= len(settings) {
		t.Fatalf("limits = %+v", out.Limits)
	}
	for i, w := range settings {
		l := out.Limits[i]
		if l.Group != w.group || l.Setting != w.setting || l.Value != w.value || l.Default == nil || *l.Default != w.df ||
			l.Name == "" || l.Description == "" {
			t.Errorf("limits[%d] = %+v, want %s %s %v (default %v) with a name and a description", i, l, w.group, w.setting, w.value, w.df)
		}
	}
	fixedByName := map[string]limitOut{}
	for _, l := range out.Limits[len(settings):] {
		if l.Group != groupIngest || l.Setting != "" || l.Default != nil || l.Description == "" {
			t.Errorf("fixed limit = %+v, want an ingest limit with no setting or default", l)
		}
		fixedByName[l.Name] = l
	}
	for name, v := range map[string]float64{
		"Request body": wire.MaxBody, "Events per batch": wire.MaxBatchEvents,
		"Attribute value length": wire.MaxAttrValue, "Timestamp ahead of the server": 300,
		"Measure value": wire.MaxMeasureValue, "Lowest sample rate": wire.MinSampleRate,
		"Form body": 64 << 10, "Form fields": 100, "Form field name length": 64, "Form value length": 8 << 10,
	} {
		if fixedByName[name].Value != v {
			t.Errorf("%s = %+v, want %v", name, fixedByName[name], v)
		}
	}
	// The MCP tool answers the same.
	var mcpOut limitsOut
	if err := json.Unmarshal([]byte(textOf(callTool(t, cs, "limits", map[string]any{}))), &mcpOut); err != nil {
		t.Fatal(err)
	}
	if len(mcpOut.Limits) != len(out.Limits) || mcpOut.Limits[0].Group != groupRetention {
		t.Errorf("MCP limits = %+v", mcpOut.Limits)
	}
}

// Nothing is capped any more, and SQLite keeps no raw window: limits has
// no caps group, no row for the raw or aggregate retention, and no
// raw_events.
func TestLimitsHasNoCaps(t *testing.T) {
	h, cs := newTestHost(t)
	h.limits = limitsFrom(&config.Config{})
	for _, l := range h.limits {
		switch l.Setting {
		case "RETENTION_RAW_DAYS", "RETENTION_AGGREGATE_DAYS", "RETENTION_EVENTS_RAW_DAYS", "RETENTION_EVENTS_AGGREGATE_DAYS",
			"ATTRIBUTE_VALUES_TOP_N", "ATTRIBUTE_BREAKDOWNS_MAX", "IDENTITIES_TOP_N":
			t.Errorf("limits reports %s: %+v", l.Setting, l)
		}
		if l.Group != groupRetention && l.Group != groupIngest {
			t.Errorf("limit %q in group %q, want retention or ingest", l.Name, l.Group)
		}
		if strings.Contains(l.Description, "rolled up") {
			t.Errorf("%s still speaks of days rolled up: %s", l.Name, l.Description)
		}
	}
	raw := textOf(callTool(t, cs, "limits", map[string]any{}))
	for _, gone := range []string{`"caps"`, "raw_events", "window_days"} {
		if strings.Contains(raw, gone) {
			t.Errorf("limits carries %s: %s", gone, raw)
		}
	}
}

// ingest_disabled says whether INGEST_DISABLED is on, false included, in
// the host's answer and over MCP.
func TestLimitsReportsTheIngestSwitch(t *testing.T) {
	h, cs := newTestHost(t)
	for _, on := range []bool{false, true} {
		h.ingestDisabled = on
		out, err := h.listLimits(context.Background(), struct{}{})
		if err != nil {
			t.Fatal(err)
		}
		if out.IngestDisabled != on {
			t.Errorf("ingest_disabled = %v, want %v", out.IngestDisabled, on)
		}
		raw := textOf(callTool(t, cs, "limits", map[string]any{}))
		var viaMCP map[string]any
		if err := json.Unmarshal([]byte(raw), &viaMCP); err != nil {
			t.Fatal(err)
		}
		if got, ok := viaMCP["ingest_disabled"].(bool); !ok || got != on {
			t.Errorf("MCP ingest_disabled = %v (present %v), want %v: %s", viaMCP["ingest_disabled"], ok, on, raw)
		}
	}
}

// events_30d counts the events in the event files of the 30 days ending
// today, every family and project: an event 30 days old or older is not
// counted. The count is cached for
// eventCountTTL like the raw count it replaces; the MCP tool answers it
// too.
func TestLimitsCountsTheLast30Days(t *testing.T) {
	h, cs := newTestHost(t)
	clock := time.Now()
	h.events.now = func() time.Time { return clock }
	out, err := h.listLimits(context.Background(), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Events30d == nil {
		t.Fatal("events_30d absent")
	}
	base := out.Events30d.Count
	today := civil.Today(time.Now())
	if out.Events30d.To != today.String() || out.Events30d.From != today.AddDays(-29).String() {
		t.Errorf("window = %s..%s, want the 30 days ending %s", out.Events30d.From, out.Events30d.To, today)
	}

	noon := func(d civil.Date) time.Time {
		day, _ := time.Parse(time.DateOnly, d.String())
		return day.Add(12 * time.Hour)
	}
	one := 1.0
	writeHostEvents(t, h,
		store.Event{Family: store.FamilyViews, ProjectID: 1, Kind: "web", Path: "/", EventName: "$page_view", TS: time.Now().UTC()},
		store.Event{Family: store.FamilyProduct, ProjectID: 1, EventName: "signup", TS: noon(today.AddDays(-29))},
		store.Event{Family: store.FamilyMeasures, ProjectID: 2, EventName: "lcp", Value: &one, Measure: store.MeasureTime, TS: noon(today.AddDays(-1))},
		store.Event{Family: store.FamilyProduct, ProjectID: 1, EventName: "signup", TS: noon(today.AddDays(-30))},
		store.Event{Family: store.FamilyProduct, ProjectID: 2, EventName: "signup", TS: noon(today.AddDays(-31))})
	// Within the TTL the count is the cached one; past it, the stale count
	// answers while a refresh runs, and the next read has the new events.
	if out, _ = h.listLimits(context.Background(), struct{}{}); out.Events30d == nil || out.Events30d.Count != base {
		t.Errorf("events_30d within the TTL = %+v, want the cached %d", out.Events30d, base)
	}
	clock = clock.Add(eventCountTTL)
	if out, _ = h.listLimits(context.Background(), struct{}{}); out.Events30d == nil || out.Events30d.Count != base {
		t.Errorf("events_30d while refreshing = %+v, want the stale %d", out.Events30d, base)
	}
	h.events.settle()
	if out, _ = h.listLimits(context.Background(), struct{}{}); out.Events30d == nil || out.Events30d.Count != base+3 {
		t.Errorf("events_30d = %+v, want %d: today's, 29 and 1 days back, not 30 or 31 days back", out.Events30d, base+3)
	}

	var viaMCP struct {
		Events30d struct {
			Count int64 `json:"count"`
		} `json:"events_30d"`
	}
	if err := json.Unmarshal([]byte(textOf(callTool(t, cs, "limits", map[string]any{}))), &viaMCP); err != nil {
		t.Fatal(err)
	}
	if viaMCP.Events30d.Count != base+3 {
		t.Errorf("MCP events_30d = %+v", viaMCP.Events30d)
	}
}

// A count that cannot be read costs the answer its events_30d, not the
// whole tool: the limits still come back, and the failure is logged once
// per TTL. A process with no engine has no count to take: events_30d is
// left out and nothing is logged.
func TestLimitsWithoutEvents30dWhenTheCountCannotBeRead(t *testing.T) {
	h, cs := newTestHost(t)
	var logs bytes.Buffer
	h.logger = slog.New(slog.NewTextHandler(&logs, nil))
	h.native = &fakeNative{err: errors.New("the files are gone")}
	h.events = h.newHostEventCount()
	out, err := h.listLimits(context.Background(), struct{}{})
	if err != nil {
		t.Fatalf("listLimits = %v, want the limits without events_30d", err)
	}
	if out.Events30d != nil || len(out.Limits) == 0 {
		t.Errorf("out = %+v, want limits and no events_30d", out)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "events_30d") {
		t.Errorf("failure not logged: %q", logs.String())
	}
	if raw := textOf(callTool(t, cs, "limits", map[string]any{})); strings.Contains(raw, "events_30d") {
		t.Errorf("MCP answer carries events_30d: %s", raw)
	}
	// The failure is cached for the TTL: the second call did not log again.
	if n := strings.Count(logs.String(), "level=ERROR"); n != 1 {
		t.Errorf("logged %d errors over two calls within the TTL, want 1: %q", n, logs.String())
	}

	logs.Reset()
	h.native, h.events = nil, nil
	if out, err := h.listLimits(context.Background(), struct{}{}); err != nil || out.Events30d != nil || len(out.Limits) == 0 {
		t.Errorf("no engine: out = %+v, %v; want limits and no events_30d", out, err)
	}
	if logs.Len() != 0 {
		t.Errorf("no engine logged: %q", logs.String())
	}
}

// cap_usage is gone: no MCP tool, and its route answers 404.
func TestCapUsageIsGone(t *testing.T) {
	h, cs := newTestHost(t)
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name == "cap_usage" {
			t.Error("cap_usage is still an MCP tool")
		}
	}
	if rec := serveREST(t, newTestRegistrar(t, h), "GET", "/api/projects/1/cap-usage", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET cap-usage = %d %s, want 404", rec.Code, rec.Body)
	}
}

// The usage tools default to the 30 days ending today and refuse
// backwards or overlong ranges.
func TestUsageRange(t *testing.T) {
	now := time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC)
	from, to, err := usageRange(usageRangeIn{}, now)
	if err != nil || from.String() != "2026-09-04" || to.String() != "2026-10-03" {
		t.Errorf("default = %s..%s, %v; want 2026-09-04..2026-10-03", from, to, err)
	}
	from, to, err = usageRange(usageRangeIn{To: "2026-09-10"}, now)
	if err != nil || from.String() != "2026-08-12" || to.String() != "2026-09-10" {
		t.Errorf("to only = %s..%s, %v", from, to, err)
	}
	for _, in := range []usageRangeIn{
		{From: "2026-09-10", To: "2026-09-01"},
		{From: "2025-01-01", To: "2026-09-01"},
		{From: "yesterday"},
		{From: "2025-09-01", To: "2026-10-06"}, // 401 days inclusive
	} {
		if _, _, err := usageRange(in, now); !errors.Is(err, manage.ErrInvalid) {
			t.Errorf("%+v: err = %v, want invalid", in, err)
		}
	}
	// A range runs at most 400 days, both ends included.
	if _, _, err := usageRange(usageRangeIn{From: "2025-09-01", To: "2026-10-05"}, now); err != nil {
		t.Errorf("400 days inclusive: %v, want accepted", err)
	}
}
