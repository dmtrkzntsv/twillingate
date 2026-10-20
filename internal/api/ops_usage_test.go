package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TestUsageReadsTheFiles: the series counts each day's views, product
// events and measures in the event files, the totals sum it, first_day
// is the oldest day the files name and last_received_at when the newest
// row arrived, whatever the range. One project or every project, in one
// answer; SQLite's event tables are not read.
//
// The template's files hold blog's (1) views on 2026-08-20 (45), 08-21
// (30), 08-22..24 (1 each), 08-25 (2) and 08-26 (1, received 10:00),
// signup x5 on 08-20 and x2 on 08-21, and measures x5 on 08-20 and x2 on
// 08-21, all merged into August's file.
func TestUsageReadsTheFiles(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := context.Background()
	value := 1.0
	writeHostEvents(t, h,
		store.Event{ProjectID: 2, Family: store.FamilyViews, EventName: "$page_view", Kind: "web", Path: "/",
			TS: time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)},
		store.Event{ProjectID: 2, Family: store.FamilyMeasures, EventName: "load", Value: &value, Measure: store.MeasureTime,
			TS: time.Date(2026, 8, 22, 9, 30, 0, 0, time.UTC)})
	if _, err := rawExec(h.ops.St, `INSERT OR REPLACE INTO agg_views_daily (project_id, day, kind, visitors, views, sessions, bounces, duration_sec)
		VALUES (1, '2026-08-19', 'web', 1, 99, 1, 0, 0)`); err != nil {
		t.Fatal(err)
	}

	out, err := h.usage(ctx, usageIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-19", To: "2026-08-27"}})
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
		day["2026-08-20"].Events != 5 || day["2026-08-21"].Events != 2 ||
		day["2026-08-20"].Measures != 5 || day["2026-08-21"].Measures != 2 ||
		day["2026-08-19"] != (usageDay{Day: "2026-08-19"}) {
		t.Errorf("series = %+v", p.Series)
	}
	if p.Totals != (usageTotals{Views: 81, Events: 7, Measures: 7}) {
		t.Errorf("totals = %+v", p.Totals)
	}
	if p.LastReceivedAt == nil || *p.LastReceivedAt != "2026-08-26T10:00:00Z" {
		t.Errorf("last_received_at = %v, want 2026-08-26T10:00:00Z", deref(p.LastReceivedAt))
	}
	if p.FirstDay == nil || *p.FirstDay != "2026-08-01" {
		t.Errorf("first_day = %v, want August's file's first day", deref(p.FirstDay))
	}

	// A range before the newest day still says when the newest row arrived.
	early, err := h.usage(ctx, usageIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-01", To: "2026-08-02"}})
	if err != nil {
		t.Fatal(err)
	}
	if lr := early.Projects[0].LastReceivedAt; lr == nil || *lr != "2026-08-26T10:00:00Z" {
		t.Errorf("last_received_at over an earlier range = %v", deref(lr))
	}

	all, err := h.usage(ctx, usageIn{usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-22"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Projects) != 2 || all.Projects[0].Totals != (usageTotals{Views: 76, Events: 7, Measures: 7}) {
		t.Fatalf("projects = %+v", all.Projects)
	}
	docs := all.Projects[1]
	if docs.ProjectID != 2 || len(docs.Series) != 3 || docs.Series[1].Views != 1 || docs.Series[2].Measures != 1 ||
		docs.Totals != (usageTotals{Views: 1, Measures: 1}) {
		t.Errorf("docs = %+v", docs)
	}
	if docs.LastReceivedAt == nil || *docs.LastReceivedAt != "2026-08-22T09:30:00Z" || docs.FirstDay == nil || *docs.FirstDay != "2026-08-21" {
		t.Errorf("docs last_received_at %v, first_day %v; want 2026-08-22T09:30:00Z and 2026-08-21", deref(docs.LastReceivedAt), deref(docs.FirstDay))
	}
	body, err := json.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"raw_days", "rolled_up_days", "unused_attributes", "attribute_keys", "declared_attributes"} {
		if strings.Contains(string(body), `"`+gone+`"`) {
			t.Errorf("usage still carries %s: %s", gone, body)
		}
	}

	if _, err := h.usage(ctx, usageIn{ProjectID: 99}); !errors.Is(err, manage.ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}
	if _, err := h.usage(ctx, usageIn{usageRangeIn: usageRangeIn{From: "2026-09-02", To: "2026-09-01"}}); !errors.Is(err, manage.ErrInvalid) {
		t.Errorf("backwards range: %v", err)
	}
}

// A project with nothing in the files has a full series of zeros and no
// days.
func TestUsageOfAProjectWithNoFiles(t *testing.T) {
	h, _ := newTestHost(t)
	out, err := h.usage(context.Background(), usageIn{ProjectID: 2, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-22"}})
	if err != nil {
		t.Fatal(err)
	}
	docs := out.Projects[0]
	if len(docs.Series) != 3 || docs.Totals != (usageTotals{}) || docs.LastReceivedAt != nil || docs.FirstDay != nil || docs.Size != nil {
		t.Errorf("docs = %+v", docs)
	}
}

// Until the daily pass of the process that keeps the files has measured
// them, a project has no size: null, with the rest of the answer standing
// and the database's size live, also once a pass with no files (the
// console's, in a split install) has measured the database.
func TestUsageSizeIsNullUntilMeasured(t *testing.T) {
	h, _ := newTestHost(t)
	putStat(t, h, store.StatDatabaseBytes, 0, "2026-08-21", 5000)
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

// putStat writes one usage_history row, as the daily pass would.
func putStat(t *testing.T, h *host, key string, project int64, day string, v int64) {
	t.Helper()
	if _, err := rawExec(h.ops.St, `INSERT INTO usage_history (key, project_id, measured_at, value) VALUES (?,?,?,?)`,
		key, project, day, v); err != nil {
		t.Fatal(err)
	}
}

// TestUsageSeriesCarriesTheFileBytes: size is each project's file bytes
// on the newest day the pass measured any, with that day; a project that
// day left out has no files and no size, though its history stands. Every
// day of the series carries that day's file bytes, null on a day not
// measured (also in the JSON), and the database's size per day is a
// series of its own. A stat this answer does not know is left alone.
func TestUsageSeriesCarriesTheFileBytes(t *testing.T) {
	h, _ := newTestHost(t)
	putStat(t, h, store.StatFileBytes, 1, "2026-08-19", 1) // before the range
	putStat(t, h, store.StatFileBytes, 1, "2026-08-20", 100)
	putStat(t, h, store.StatFileBytes, 2, "2026-08-20", 20)
	putStat(t, h, store.StatFileBytes, 1, "2026-08-22", 300)
	putStat(t, h, store.StatDatabaseBytes, 0, "2026-08-22", 5000)
	putStat(t, h, "raw_bytes", 1, "2026-08-21", 99)
	out, err := h.usage(context.Background(), usageIn{usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-22"}})
	if err != nil {
		t.Fatal(err)
	}
	blog, docs := out.Projects[0], out.Projects[1]
	if want := (usageSize{TotalBytes: 300, MeasuredAt: "2026-08-22"}); blog.Size == nil || *blog.Size != want {
		t.Errorf("blog size = %+v, want %+v", blog.Size, want)
	}
	if docs.Size != nil {
		t.Errorf("docs size = %+v, want null: the newest day did not measure it", docs.Size)
	}
	got := map[string]*int64{}
	for _, d := range blog.Series {
		got[d.Day] = d.TotalBytes
	}
	for day, want := range map[string]int64{"2026-08-20": 100, "2026-08-22": 300} {
		if got[day] == nil || *got[day] != want {
			t.Errorf("total_bytes on %s = %v, want %d", day, numOf(got[day]), want)
		}
	}
	if got["2026-08-21"] != nil {
		t.Errorf("total_bytes on 2026-08-21 = %d, want null (not measured)", *got["2026-08-21"])
	}
	if n := docs.Series[0].TotalBytes; n == nil || *n != 20 {
		t.Errorf("docs total_bytes on 2026-08-20 = %v, want its history's 20", numOf(n))
	}
	body, err := json.Marshal(blog.Series[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"total_bytes":null`) {
		t.Errorf("an unmeasured day must say null: %s", body)
	}
	five := int64(5000)
	want := []dbDay{{Day: "2026-08-20"}, {Day: "2026-08-21"}, {Day: "2026-08-22", Bytes: &five}}
	if len(out.DatabaseSeries) != len(want) {
		t.Fatalf("database_series = %+v", out.DatabaseSeries)
	}
	for i, d := range out.DatabaseSeries {
		if d.Day != want[i].Day || (d.Bytes == nil) != (want[i].Bytes == nil) || (d.Bytes != nil && *d.Bytes != *want[i].Bytes) {
			t.Errorf("database_series[%d] = %+v, want %+v", i, d, want[i])
		}
	}
	one, err := h.usage(context.Background(), usageIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-22"}})
	if err != nil {
		t.Fatal(err)
	}
	if s := one.Projects[0].Size; s == nil || s.TotalBytes != 300 {
		t.Errorf("one-project size = %+v, want 300", s)
	}
	b, _ := json.Marshal(blog.Size)
	if string(b) != `{"total_bytes":300,"measured_at":"2026-08-22"}` {
		t.Errorf("size JSON = %s", b)
	}
}

// numOf is *p, or nil, for a message.
func numOf(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
