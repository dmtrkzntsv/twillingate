package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/wire"
)

// limits reports retention, the caps and the wire format's fixed limits, in
// that order: settings with their value in force (0 included) and default,
// fixed limits from internal/wire with neither setting nor default.
func TestLimitsReportsTheLimitsInForce(t *testing.T) {
	h, cs := newTestHost(t)
	cfg := &config.Config{AttributeValuesTopN: 7, IdentitiesTopN: 2000, AttributeBreakdownsMax: 12,
		Forms: config.FormsConfig{DraftDays: 3}}
	cfg.Retention.Events = config.RetentionClass{RawDays: 7, AggregateDays: 90}
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
		{groupRetention, "RETENTION_EVENTS_RAW_DAYS", 7, config.DefaultRawDays},
		{groupRetention, "RETENTION_EVENTS_AGGREGATE_DAYS", 90, config.DefaultAggregateDays},
		{groupRetention, "RETENTION_ARCHIVED_DAYS", 0, config.DefaultArchivedDays},
		{groupRetention, "FORMS_DRAFT_DAYS", 3, config.DefaultFormDraftDays},
		{groupCaps, "ATTRIBUTE_VALUES_TOP_N", 7, config.DefaultAttributeValuesTopN},
		{groupCaps, "ATTRIBUTE_BREAKDOWNS_MAX", 12, config.DefaultAttributeBreakdownsMax},
		{groupCaps, "IDENTITIES_TOP_N", 2000, config.DefaultIdentitiesTopN},
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
	if h.capOf("NO_SUCH_TOP_N") != 0 || h.capOf(settingAttrs) != 7 || h.capOf(settingIdentities) != 2000 {
		t.Errorf("capOf disagrees with limits: %+v", out.Limits)
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

// heldByFamily counts the stored raw rows family by family, through the
// raw_* views, so the expectation does not share listLimits' query.
func heldByFamily(t *testing.T, h *host) int64 {
	t.Helper()
	var n int64
	for _, v := range []string{"raw_views", "raw_product", "raw_measures"} {
		res, err := h.subs.Run(context.Background(), "SELECT COUNT(*) FROM "+v)
		if err != nil {
			t.Fatal(err)
		}
		k, err := strconv.ParseInt(res.Rows[0][0], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		n += k
	}
	return n
}

// raw_events is the raw rows stored, of every family and project, counted
// through the console's read handle at most every rawCountTTL, beside the
// raw window it is read against; the MCP tool answers it too.
func TestLimitsReportsTheRawEventsHeld(t *testing.T) {
	h, cs := newTestHost(t)
	h.rawDays = 7
	clock := time.Now()
	h.raw.now = func() time.Time { return clock }
	out, err := h.listLimits(context.Background(), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	base := heldByFamily(t, h)
	if base == 0 {
		t.Fatal("the template database holds no raw rows; the test needs some")
	}
	if out.RawEvents == nil || out.RawEvents.Held != base || out.RawEvents.WindowDays != 7 {
		t.Fatalf("raw_events = %+v, want %d held in 7 days", out.RawEvents, base)
	}

	st, err := store.Open("sqlite://" + testDBPaths[h])
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	one := 1.0
	now := time.Now().UTC()
	evs := []store.Event{
		{Family: store.FamilyViews, ProjectID: 1, Kind: "web", Path: "/"},
		{Family: store.FamilyProduct, ProjectID: 1, EventName: "signup"},
		{Family: store.FamilyMeasures, ProjectID: 2, EventName: "lcp", Value: &one},
	}
	for i := range evs {
		evs[i].ID = "0190eeee-0000-7000-8000-00000000000" + strconv.Itoa(i)
		evs[i].TS, evs[i].ActorID = now, "a"
	}
	if err := st.WriteEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
	// Within the TTL the count is the cached one; past it, the stale count
	// answers while a refresh runs, and the next read has the new rows.
	out, err = h.listLimits(context.Background(), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if out.RawEvents == nil || out.RawEvents.Held != base {
		t.Errorf("raw_events within the TTL = %+v, want the cached %d", out.RawEvents, base)
	}
	clock = clock.Add(rawCountTTL)
	if out, _ = h.listLimits(context.Background(), struct{}{}); out.RawEvents == nil || out.RawEvents.Held != base {
		t.Errorf("raw_events while refreshing = %+v, want the stale %d", out.RawEvents, base)
	}
	h.raw.settle()
	out, err = h.listLimits(context.Background(), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if out.RawEvents == nil || out.RawEvents.Held != base+3 || heldByFamily(t, h) != base+3 {
		t.Errorf("raw_events after one row of each family = %+v, want %d held", out.RawEvents, base+3)
	}

	var viaMCP struct {
		RawEvents struct {
			Held       int64 `json:"held"`
			WindowDays int   `json:"window_days"`
		} `json:"raw_events"`
	}
	if err := json.Unmarshal([]byte(textOf(callTool(t, cs, "limits", map[string]any{}))), &viaMCP); err != nil {
		t.Fatal(err)
	}
	if viaMCP.RawEvents.Held != base+3 || viaMCP.RawEvents.WindowDays != 7 {
		t.Errorf("MCP raw_events = %+v", viaMCP.RawEvents)
	}
}

// A count that cannot be read costs the answer its raw_events, not the
// whole tool: the limits still come back, and the failure is logged.
func TestLimitsWithoutRawEventsWhenTheCountCannotBeRead(t *testing.T) {
	h, cs := newTestHost(t)
	var logs bytes.Buffer
	h.logger = slog.New(slog.NewTextHandler(&logs, nil))
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := h.listLimits(context.Background(), struct{}{})
	if err != nil {
		t.Fatalf("listLimits = %v, want the limits without raw_events", err)
	}
	if out.RawEvents != nil || len(out.Limits) == 0 {
		t.Errorf("out = %+v, want limits and no raw_events", out)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "raw_events") {
		t.Errorf("failure not logged: %q", logs.String())
	}
	if raw := textOf(callTool(t, cs, "limits", map[string]any{})); strings.Contains(raw, "raw_events") {
		t.Errorf("MCP answer carries raw_events: %s", raw)
	}
	// The failure is cached for the TTL: the second call did not log again.
	if n := strings.Count(logs.String(), "level=ERROR"); n != 1 {
		t.Errorf("logged %d errors over two calls within the TTL, want 1: %q", n, logs.String())
	}
}

// The count reads the raw rows through v_events_flat, the view of every
// raw row, never the events table (TestRawTableIsReadOnlyThroughFamilyViews
// holds the store to that).
func TestRawEventsCountReadsTheFlatView(t *testing.T) {
	rawRead := regexp.MustCompile(`\b(FROM|JOIN)\s+events\b`)
	if rawRead.MatchString(rawEventsSQL) || !strings.Contains(rawEventsSQL, "FROM v_events_flat") {
		t.Errorf("rawEventsSQL = %s, want a count of v_events_flat", rawEventsSQL)
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

// cap_usage reports, per capped dimension, the busiest day, the days with
// data, the days that folded into (other) and the share folded. The test
// host has blog (1) with aggregated days 2026-08-20/21 and a raw view on
// 2026-08-26 (path /live, user u1); this adds a folded paths day and a
// folded plan day, a tier key with a folded measure day and a product day,
// a values cap of 2 (views breakdowns and attributes alike) and an
// identities cap of 1 so the identity days (u1 on 2026-08-20 aggregated,
// and again on 2026-08-26 from the raw view) reach theirs.
func TestCapUsage(t *testing.T) {
	h, _ := newTestHost(t)
	h.limits = limitsFrom(&config.Config{AttributeValuesTopN: 2, IdentitiesTopN: 1})
	for _, q := range []string{
		`INSERT INTO agg_views_paths (project_id, day, path, visitors, views) VALUES
		 (1,'2026-08-22','/a',3,10), (1,'2026-08-22','/b',2,5), (1,'2026-08-22','(other)',4,15)`,
		`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups) VALUES
		 (1,'2026-08-22','signup','plan','basic',6,5,1), (1,'2026-08-22','signup','plan','(other)',4,3,1),
		 (1,'2026-08-24','signup','tier','x',5,5,0), (1,'2026-08-24','signup','tier','y',5,5,0)`,
		`INSERT INTO agg_measures_attrs (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum) VALUES
		 (1,'2026-08-23','load','ms','tier','a',1,2,2,4), (1,'2026-08-23','load','ms','tier','a',2,1,1,3),
		 (1,'2026-08-23','load','ms','tier','(other)',1,1,1,2)`,
	} {
		if _, err := rawExec(h.ops.St, q); err != nil {
			t.Fatal(err)
		}
	}
	out, err := h.capUsage(context.Background(), capUsageIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-26"}})
	if err != nil {
		t.Fatal(err)
	}
	byDim := map[string]capUsageRow{}
	for _, d := range out.Dimensions {
		byDim[d.Dimension] = d
	}
	paths := byDim["paths"]
	// Days 08-20 (3 paths, 37 views), 08-22 (3 rows, 30 views, 15 folded),
	// 08-26 (the raw /live view): the busiest is the earliest of the two 3s.
	if paths.Setting != settingAttrs || paths.Cap != 2 || paths.MaxValuesPerDay != 3 || paths.MaxDay != "2026-08-20" ||
		paths.Days != 3 || paths.DaysCapped != 1 || paths.FoldedShare == nil || math.Abs(*paths.FoldedShare-15.0/68) > 1e-9 {
		t.Errorf("paths = %+v (share %v)", paths, paths.FoldedShare)
	}
	plan := byDim["plan"]
	// pro (08-20, 3), team (08-21, 2), basic + (other) (08-22, 6 + 4).
	if plan.Setting != settingAttrs || plan.Cap != 2 || plan.MaxValuesPerDay != 2 || plan.MaxDay != "2026-08-22" ||
		plan.Days != 3 || plan.DaysCapped != 1 || plan.FoldedShare == nil || math.Abs(*plan.FoldedShare-4.0/15) > 1e-9 {
		t.Errorf("plan = %+v (share %v)", plan, plan.FoldedShare)
	}
	tier := byDim["tier"]
	// a (3 samples over two buckets) + (other) (1) on 08-23 from the
	// measure, x + y (10) on 08-24 from the event: two values a day, the
	// busiest the earlier; the measure's buckets count one value.
	if tier.Setting != settingAttrs || tier.MaxValuesPerDay != 2 || tier.MaxDay != "2026-08-23" ||
		tier.Days != 2 || tier.DaysCapped != 1 || tier.FoldedShare == nil || math.Abs(*tier.FoldedShare-1.0/14) > 1e-9 {
		t.Errorf("tier = %+v (share %v)", tier, tier.FoldedShare)
	}
	users := byDim["users"]
	// u1 on 08-20 (aggregated) and u1 again on 08-26 (the raw /live view
	// carries user_id u1): two days of one user, each at the cap of 1.
	if users.Setting != settingIdentities || users.Cap != 1 || users.MaxValuesPerDay != 1 || users.MaxDay != "2026-08-20" ||
		users.Days != 2 || users.DaysCapped != 2 || users.FoldedShare != nil {
		t.Errorf("users = %+v", users)
	}
	if _, ok := byDim["consent"]; ok {
		t.Error("consent is listed; it is never capped")
	}
	// Views dimensions come first, in a fixed order; identities last.
	if out.Dimensions[0].Dimension != "paths" || out.Dimensions[len(out.Dimensions)-1].Setting != settingIdentities {
		t.Errorf("order: first %+v, last %+v", out.Dimensions[0], out.Dimensions[len(out.Dimensions)-1])
	}
}

// With no cap (0) the rows report cap 0; a day rolled up under an older
// cap keeps its (other) row and still counts as capped; identities, which
// keep no trace, report no capped day.
func TestCapUsageWithNoCap(t *testing.T) {
	h, _ := newTestHost(t)
	h.limits = limitsFrom(&config.Config{})
	if _, err := rawExec(h.ops.St, `INSERT INTO agg_views_paths (project_id, day, path, visitors, views) VALUES (1,'2026-08-22','(other)',4,15)`); err != nil {
		t.Fatal(err)
	}
	out, err := h.capUsage(context.Background(), capUsageIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-26"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range out.Dimensions {
		if d.Cap != 0 {
			t.Errorf("%s cap = %d, want 0", d.Dimension, d.Cap)
		}
		if d.Dimension == "paths" && d.DaysCapped != 1 {
			t.Errorf("paths days_capped = %d, want 1 (the folded day stays folded)", d.DaysCapped)
		}
		if (d.Dimension == "users" || d.Dimension == "groups") && d.DaysCapped != 0 {
			t.Errorf("%s days_capped = %d, want 0", d.Dimension, d.DaysCapped)
		}
	}
}

// A project with no data in the range answers no dimensions; an unknown
// one is not_found.
func TestCapUsageEmptyAndUnknown(t *testing.T) {
	h, _ := newTestHost(t)
	out, err := h.capUsage(context.Background(), capUsageIn{ProjectID: 2, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-26"}})
	if err != nil || len(out.Dimensions) != 0 || out.Dimensions == nil {
		t.Errorf("project 2 (no data) = %+v, %v; want an empty, non-nil list", out, err)
	}
	if _, err := h.capUsage(context.Background(), capUsageIn{ProjectID: 99}); !errors.Is(err, manage.ErrNotFound) {
		t.Errorf("unknown project: %v, want not_found", err)
	}
}
