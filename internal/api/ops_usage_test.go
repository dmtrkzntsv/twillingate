package api

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The test host's blog (1): views aggregated on 08-20 (45) and 08-21 (30),
// a raw view on 08-26, signup x5 on 08-20, measures aggregated on 08-20 (5
// samples) and raw on 08-21 (2), plan declared and carried on 08-20/21.
func TestUsageOneProject(t *testing.T) {
	h, _ := newTestHost(t)
	out, err := h.usage(context.Background(), usageIn{ProjectID: 1,
		usageRangeIn: usageRangeIn{From: "2026-08-19", To: "2026-08-27"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.From != "2026-08-19" || out.To != "2026-08-27" || len(out.Projects) != 1 {
		t.Fatalf("out = %+v", out)
	}
	p := out.Projects[0]
	if len(p.Series) != 9 || p.Series[0].Day != "2026-08-19" || p.Series[8].Day != "2026-08-27" {
		t.Fatalf("series must hold every day of the range: %+v", p.Series)
	}
	day := map[string]usageDay{}
	for _, d := range p.Series {
		day[d.Day] = d
	}
	if day["2026-08-20"].Views != 45 || day["2026-08-21"].Views != 30 || day["2026-08-26"].Views != 1 ||
		day["2026-08-20"].Events != 5 || day["2026-08-20"].Measures != 5 || day["2026-08-21"].Measures != 2 ||
		day["2026-08-19"] != (usageDay{Day: "2026-08-19"}) {
		t.Errorf("series = %+v", p.Series)
	}
	if p.Totals != (usageTotals{Views: 76, Events: 5, Measures: 7}) {
		t.Errorf("totals = %+v", p.Totals)
	}
	if p.LastReceivedAt == nil || *p.LastReceivedAt < "2026-08-26" {
		t.Errorf("last_received_at = %v", p.LastReceivedAt)
	}
	if p.FirstDay == nil || *p.FirstDay != "2026-08-20" || p.RawDays != 2 || p.RolledUpDays != 2 {
		t.Errorf("first_day %v raw %d rolled up %d", p.FirstDay, p.RawDays, p.RolledUpDays)
	}
	if p.UnusedAttributes == nil || len(*p.UnusedAttributes) != 0 {
		t.Errorf("plan is carried, yet unused = %v (want an empty array)", p.UnusedAttributes)
	}
}

// Without project_id every project comes back; a project with no data has
// a full series of zeros and nulls. Unused attributes are not computed for
// the all-projects answer: null, not [].
func TestUsageAllProjectsAndEmpty(t *testing.T) {
	h, cs := newTestHost(t)
	res := callTool(t, cs, "update_project", map[string]any{"project_id": 2, "attributes": []string{"plan"}})
	if res.IsError {
		t.Fatal(textOf(res))
	}
	out, err := h.usage(context.Background(), usageIn{usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-22"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Projects) != 2 {
		t.Fatalf("projects = %+v", out.Projects)
	}
	docs := out.Projects[1]
	if docs.ProjectID != 2 || len(docs.Series) != 3 || docs.Totals != (usageTotals{}) ||
		docs.LastReceivedAt != nil || docs.FirstDay != nil || docs.RawDays != 0 || docs.RolledUpDays != 0 {
		t.Errorf("docs = %+v", docs)
	}
	if docs.UnusedAttributes != nil || out.Projects[0].UnusedAttributes != nil {
		t.Errorf("all-projects unused = %v, %v; want null for both", docs.UnusedAttributes, out.Projects[0].UnusedAttributes)
	}
	body, err := json.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"unused_attributes":null`) {
		t.Errorf("unused_attributes must be present as null: %s", body)
	}
	one, err := h.usage(context.Background(), usageIn{ProjectID: 2, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-22"}})
	if err != nil {
		t.Fatal(err)
	}
	if u := one.Projects[0].UnusedAttributes; u == nil || len(*u) != 1 || (*u)[0] != "plan" {
		t.Errorf("one-project docs unused = %v, want [plan]", u)
	}
	if _, err := h.usage(context.Background(), usageIn{ProjectID: 99}); !errors.Is(err, manage.ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}
	if _, err := h.usage(context.Background(), usageIn{usageRangeIn: usageRangeIn{From: "2026-09-02", To: "2026-09-01"}}); !errors.Is(err, manage.ErrInvalid) {
		t.Errorf("backwards range: %v", err)
	}
}

// A declared key no event carried is reported while a carried one is not.
func TestUsageUnusedAttributesMixed(t *testing.T) {
	h, cs := newTestHost(t)
	res := callTool(t, cs, "update_project", map[string]any{"project_id": 1, "attributes": []string{"plan", "never_sent"}})
	if res.IsError {
		t.Fatal(textOf(res))
	}
	out, err := h.usage(context.Background(), usageIn{ProjectID: 1,
		usageRangeIn: usageRangeIn{From: "2026-08-19", To: "2026-08-27"}})
	if err != nil {
		t.Fatal(err)
	}
	if u := out.Projects[0].UnusedAttributes; u == nil || len(*u) != 1 || (*u)[0] != "never_sent" {
		t.Errorf("unused = %v, want [never_sent] (plan is carried)", u)
	}
}

// The views series skips v_views_daily's sessionizing live half; this pins
// that it still equals the view, day by day, across rolled-up and raw days.
func TestUsageViewsMatchTheView(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := context.Background()
	res, err := h.db.Run(ctx, `SELECT day, SUM(views) FROM v_views_daily
		WHERE project_id = 1 AND day BETWEEN '2026-08-19' AND '2026-08-27' GROUP BY day`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) < 3 {
		t.Fatalf("the fixture should hold rolled-up and raw days, view rows = %v", res.Rows)
	}
	want := map[string]int64{}
	for _, r := range res.Rows {
		want[r[0]], _ = strconv.ParseInt(r[1], 10, 64)
	}
	out, err := h.usage(ctx, usageIn{ProjectID: 1,
		usageRangeIn: usageRangeIn{From: "2026-08-19", To: "2026-08-27"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range out.Projects[0].Series {
		if d.Views != want[d.Day] {
			t.Errorf("%s: views %d, v_views_daily %d", d.Day, d.Views, want[d.Day])
		}
	}
}

// Until the daily pass has measured, a project has no size: null, with the
// rest of the answer standing and the database's size live.
func TestUsageSizeIsNullUntilMeasured(t *testing.T) {
	h, _ := newTestHost(t)
	out, err := h.usage(context.Background(), usageIn{usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-21"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.DatabaseBytes <= 0 {
		t.Errorf("database_bytes = %d", out.DatabaseBytes)
	}
	for _, p := range out.Projects {
		if p.Size != nil {
			t.Errorf("project %d size = %+v, want null before a measurement", p.ProjectID, p.Size)
		}
	}
	if out.Projects[0].Totals.Views == 0 {
		t.Errorf("totals = %+v; the rest of the answer must stand", out.Projects[0].Totals)
	}
	body, err := json.Marshal(out.Projects[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"size":null`) {
		t.Errorf("size must be present as null: %s", body)
	}
}

// putStat writes one server_stats row, as the daily pass would.
func putStat(t *testing.T, h *host, key string, project int64, day string, v int64) {
	t.Helper()
	if _, err := rawExec(h.ops.St, `INSERT INTO server_stats (key, project_id, measured_at, value) VALUES (?,?,?,?)`,
		key, project, day, v); err != nil {
		t.Fatal(err)
	}
}

// Sizes are what the daily pass stored in server_stats, read for every
// project at once: both projects get theirs from the newest day measured,
// with that day, and a stat this answer does not know is left alone.
func TestUsageSizesComeFromServerStats(t *testing.T) {
	h, _ := newTestHost(t)
	putStat(t, h, store.StatRawBytes, 1, "2026-08-21", 1)
	putStat(t, h, store.StatAggregateBytes, 1, "2026-08-21", 2)
	putStat(t, h, store.StatRawBytes, 1, "2026-08-22", 812000)
	putStat(t, h, store.StatAggregateBytes, 1, "2026-08-22", 1450000)
	putStat(t, h, store.StatRawBytes, 2, "2026-08-22", 7)
	putStat(t, h, store.StatAggregateBytes, 2, "2026-08-22", 0)
	putStat(t, h, "something_else", 1, "2026-08-22", 99)
	out, err := h.usage(context.Background(), usageIn{usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-21"}})
	if err != nil {
		t.Fatal(err)
	}
	blog, docs := out.Projects[0], out.Projects[1]
	if want := (usageSize{RawBytes: 812000, AggregateBytes: 1450000, TotalBytes: 2262000, MeasuredAt: "2026-08-22"}); blog.Size == nil || *blog.Size != want {
		t.Errorf("blog size = %+v, want %+v", blog.Size, want)
	}
	if want := (usageSize{RawBytes: 7, TotalBytes: 7, MeasuredAt: "2026-08-22"}); docs.Size == nil || *docs.Size != want {
		t.Errorf("docs size = %+v, want %+v", docs.Size, want)
	}
	one, err := h.usage(context.Background(), usageIn{ProjectID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if one.Projects[0].Size == nil || one.Projects[0].Size.TotalBytes != 7 {
		t.Errorf("one-project size = %+v, want docs' 7", one.Projects[0].Size)
	}
	body, err := json.Marshal(blog.Size)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"measured_at":"2026-08-22"`) {
		t.Errorf("size JSON = %s, want measured_at", body)
	}
}

// A project the newest measurement left out had no rows left that day: it
// has no size, not the figure an earlier day measured.
func TestUsageSizeIsNullWhenTheNewestDayLeftItOut(t *testing.T) {
	h, _ := newTestHost(t)
	putStat(t, h, store.StatRawBytes, 1, "2026-08-21", 10)
	putStat(t, h, store.StatRawBytes, 2, "2026-08-21", 20)
	putStat(t, h, store.StatRawBytes, 1, "2026-08-22", 11)
	out, err := h.usage(context.Background(), usageIn{usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-22"}})
	if err != nil {
		t.Fatal(err)
	}
	if s := out.Projects[0].Size; s == nil || s.TotalBytes != 11 {
		t.Errorf("blog size = %+v, want the newest day's 11", s)
	}
	if s := out.Projects[1].Size; s != nil {
		t.Errorf("docs size = %+v, want null: the newest day did not measure it", s)
	}
	// Its history stands.
	if n := out.Projects[1].Series[1].TotalBytes; n == nil || *n != 20 {
		t.Errorf("docs total_bytes on 2026-08-21 = %v, want 20", n)
	}
}

// Every day of the series carries that day's measured size, raw and
// aggregate summed; a day not measured is null, also in the JSON.
func TestUsageSeriesCarriesTheSizeHistory(t *testing.T) {
	h, _ := newTestHost(t)
	putStat(t, h, store.StatRawBytes, 1, "2026-08-19", 1) // before the range
	putStat(t, h, store.StatRawBytes, 1, "2026-08-20", 100)
	putStat(t, h, store.StatAggregateBytes, 1, "2026-08-20", 50)
	putStat(t, h, store.StatRawBytes, 1, "2026-08-22", 300)
	putStat(t, h, store.StatRawBytes, 2, "2026-08-21", 9) // another project
	out, err := h.usage(context.Background(), usageIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-22"}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*int64{}
	for _, d := range out.Projects[0].Series {
		got[d.Day] = d.TotalBytes
	}
	for day, want := range map[string]int64{"2026-08-20": 150, "2026-08-22": 300} {
		if got[day] == nil || *got[day] != want {
			t.Errorf("total_bytes on %s = %v, want %d", day, got[day], want)
		}
	}
	if got["2026-08-21"] != nil {
		t.Errorf("total_bytes on 2026-08-21 = %d, want null (only another project was measured)", *got["2026-08-21"])
	}
	body, err := json.Marshal(out.Projects[0].Series[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"total_bytes":null`) {
		t.Errorf("an unmeasured day must say null: %s", body)
	}
}

// A project measured on one side only (raw rows, no aggregate pair yet)
// still answers, with the missing side zero.
func TestUsageSizeWithOneStat(t *testing.T) {
	h, _ := newTestHost(t)
	putStat(t, h, store.StatRawBytes, 1, "2026-08-22", 40)
	out, err := h.usage(context.Background(), usageIn{ProjectID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if s := out.Projects[0].Size; s == nil || s.RawBytes != 40 || s.AggregateBytes != 0 || s.TotalBytes != 40 {
		t.Errorf("size = %+v, want raw 40 only", s)
	}
}

// Days before the newest daily pass come from its stored counts (a day
// with no row counts 0); that day and later are counted live. The totals
// follow the series, and the database's stored size is a series of its own.
func TestUsageReadsStoredCountsBeforeTheNewestPass(t *testing.T) {
	h, _ := newTestHost(t)
	putStat(t, h, store.StatDatabaseBytes, 0, "2026-08-21", 5000) // the pass ran on the 21st
	putStat(t, h, store.StatViews, 1, "2026-08-20", 777)          // not what the views hold (45): stored wins
	putStat(t, h, store.StatEvents, 1, "2026-08-20", 3)
	out, err := h.usage(context.Background(), usageIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-19", To: "2026-08-21"}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]usageDay{}
	for _, d := range out.Projects[0].Series {
		got[d.Day] = d
	}
	if d := got["2026-08-20"]; d.Views != 777 || d.Events != 3 || d.Measures != 0 {
		t.Errorf("2026-08-20 = %+v, want the stored 777 views and 3 events", d)
	}
	if d := got["2026-08-19"]; d.Views != 0 {
		t.Errorf("2026-08-19 = %+v, want 0: counted, nothing stored", d)
	}
	if d := got["2026-08-21"]; d.Views != 30 {
		t.Errorf("2026-08-21 = %+v, want the live 30: the pass counts only the days before its own", d)
	}
	if tot := out.Projects[0].Totals; tot.Views != 807 || tot.Events != 3 {
		t.Errorf("totals = %+v, want the series' 807 views and 3 events", tot)
	}
	five := int64(5000)
	want := []dbDay{{Day: "2026-08-19"}, {Day: "2026-08-20"}, {Day: "2026-08-21", Bytes: &five}}
	if len(out.DatabaseSeries) != len(want) {
		t.Fatalf("database_series = %+v", out.DatabaseSeries)
	}
	for i, d := range out.DatabaseSeries {
		if d.Day != want[i].Day || (d.Bytes == nil) != (want[i].Bytes == nil) || (d.Bytes != nil && *d.Bytes != *want[i].Bytes) {
			t.Errorf("database_series[%d] = %+v, want %+v", i, d, want[i])
		}
	}
}

// Before any pass every day is counted live, as before server_stats.
func TestUsageCountsLiveBeforeAnyPass(t *testing.T) {
	h, _ := newTestHost(t)
	putStat(t, h, store.StatViews, 1, "2026-08-20", 777) // no database_bytes row: no pass yet
	out, err := h.usage(context.Background(), usageIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-20"}})
	if err != nil {
		t.Fatal(err)
	}
	if d := out.Projects[0].Series[0]; d.Views != 45 {
		t.Errorf("2026-08-20 = %+v, want the live 45", d)
	}
}

// first_day reaches as far back as the stored counts, which outlive the
// aggregates, so it matches the oldest day the series can show.
func TestUsageFirstDayIncludesStoredCounts(t *testing.T) {
	h, _ := newTestHost(t)
	before, err := h.usage(context.Background(), usageIn{ProjectID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if d := before.Projects[0].FirstDay; d == nil || *d <= "2025-03-14" {
		t.Fatalf("fixture first_day = %v, want a day after the stored count added below", d)
	}
	putStat(t, h, store.StatEvents, 1, "2025-03-14", 2) // its aggregates long pruned
	out, err := h.usage(context.Background(), usageIn{ProjectID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if d := out.Projects[0].FirstDay; d == nil || *d != "2025-03-14" {
		t.Errorf("first_day = %v, want the stored count's 2025-03-14 (was %v)", d, before.Projects[0].FirstDay)
	}
	putStat(t, h, store.StatViews, 2, "2024-01-01", 1) // another project's count
	out, err = h.usage(context.Background(), usageIn{ProjectID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if d := out.Projects[0].FirstDay; d == nil || *d != "2025-03-14" {
		t.Errorf("first_day = %v after another project's older count, want 2025-03-14", d)
	}
}

// The attribute stats come through per day as stored, null where not.
func TestUsageSeriesCarriesAttributeStats(t *testing.T) {
	h, _ := newTestHost(t)
	putStat(t, h, store.StatDeclaredAttributes, 1, "2026-08-21", 3)
	putStat(t, h, store.StatAttributeKeys, 1, "2026-08-20", 5)
	putStat(t, h, store.StatAttributeValues, 1, "2026-08-20", 140)
	putStat(t, h, store.StatAttributeValuesFolded, 1, "2026-08-20", 12)
	out, err := h.usage(context.Background(), usageIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-21"}})
	if err != nil {
		t.Fatal(err)
	}
	d20, d21 := out.Projects[0].Series[0], out.Projects[0].Series[1]
	val := func(p *int64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	if val(d20.AttributeKeys) != int64(5) || val(d20.AttributeValues) != int64(140) || val(d20.AttributeValuesFolded) != int64(12) || d20.DeclaredAttributes != nil {
		t.Errorf("2026-08-20 = keys %v values %v folded %v declared %v", val(d20.AttributeKeys), val(d20.AttributeValues), val(d20.AttributeValuesFolded), val(d20.DeclaredAttributes))
	}
	if val(d21.DeclaredAttributes) != int64(3) || d21.AttributeKeys != nil {
		t.Errorf("2026-08-21 = declared %v keys %v, want 3 and null", val(d21.DeclaredAttributes), val(d21.AttributeKeys))
	}
	body, err := json.Marshal(d21)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"attribute_values_folded":null`) {
		t.Errorf("a day not counted must say null: %s", body)
	}
}
