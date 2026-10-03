package api

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

// The test host's blog (1): views aggregated on 08-20 (45) and 08-21 (30),
// a raw view on 08-26, signup x5 on 08-20, measures aggregated on 08-20 (5
// samples) and raw on 08-21 (2), plan declared and carried on 08-20/21.
func TestProjectStatsOneProject(t *testing.T) {
	h, _ := newTestHost(t)
	out, err := h.projectStats(context.Background(), statsIn{ProjectID: 1,
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
	day := map[string]statsDay{}
	for _, d := range p.Series {
		day[d.Day] = d
	}
	if day["2026-08-20"].Views != 45 || day["2026-08-21"].Views != 30 || day["2026-08-26"].Views != 1 ||
		day["2026-08-20"].Events != 5 || day["2026-08-20"].Measures != 5 || day["2026-08-21"].Measures != 2 ||
		day["2026-08-19"] != (statsDay{Day: "2026-08-19"}) {
		t.Errorf("series = %+v", p.Series)
	}
	if p.Totals != (statsTotals{Views: 76, Events: 5, Measures: 7}) {
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
func TestProjectStatsAllProjectsAndEmpty(t *testing.T) {
	h, cs := newTestHost(t)
	res := callTool(t, cs, "update_project", map[string]any{"project_id": 2, "attributes": []string{"plan"}})
	if res.IsError {
		t.Fatal(textOf(res))
	}
	out, err := h.projectStats(context.Background(), statsIn{usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-22"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Projects) != 2 {
		t.Fatalf("projects = %+v", out.Projects)
	}
	docs := out.Projects[1]
	if docs.ProjectID != 2 || len(docs.Series) != 3 || docs.Totals != (statsTotals{}) ||
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
	one, err := h.projectStats(context.Background(), statsIn{ProjectID: 2, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-22"}})
	if err != nil {
		t.Fatal(err)
	}
	if u := one.Projects[0].UnusedAttributes; u == nil || len(*u) != 1 || (*u)[0] != "plan" {
		t.Errorf("one-project docs unused = %v, want [plan]", u)
	}
	if _, err := h.projectStats(context.Background(), statsIn{ProjectID: 99}); !errors.Is(err, manage.ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}
	if _, err := h.projectStats(context.Background(), statsIn{usageRangeIn: usageRangeIn{From: "2026-09-02", To: "2026-09-01"}}); !errors.Is(err, manage.ErrInvalid) {
		t.Errorf("backwards range: %v", err)
	}
}

// A declared key no event carried is reported while a carried one is not.
func TestProjectStatsUnusedAttributesMixed(t *testing.T) {
	h, cs := newTestHost(t)
	res := callTool(t, cs, "update_project", map[string]any{"project_id": 1, "attributes": []string{"plan", "never_sent"}})
	if res.IsError {
		t.Fatal(textOf(res))
	}
	out, err := h.projectStats(context.Background(), statsIn{ProjectID: 1,
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
func TestProjectStatsViewsMatchTheView(t *testing.T) {
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
	out, err := h.projectStats(ctx, statsIn{ProjectID: 1,
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

// Sizes split each table's bytes by the project's share of its rows: blog
// has raw rows and aggregates, docs has neither. The database's size comes
// back too, and table sizes are read once for every project.
func TestProjectStatsSizes(t *testing.T) {
	h, _ := newTestHost(t)
	out, err := h.projectStats(context.Background(), statsIn{usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-21"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.DatabaseBytes <= 0 {
		t.Errorf("database_bytes = %d", out.DatabaseBytes)
	}
	blog, docs := out.Projects[0], out.Projects[1]
	if blog.Size == nil || blog.Size.RawBytes <= 0 || blog.Size.AggregateBytes <= 0 ||
		blog.Size.TotalBytes != blog.Size.RawBytes+blog.Size.AggregateBytes {
		t.Errorf("blog size = %+v", blog.Size)
	}
	if docs.Size == nil || docs.Size.TotalBytes != 0 {
		t.Errorf("docs size = %+v, want zeros", docs.Size)
	}
	if h.sizes.loads != 1 {
		t.Errorf("table sizes loaded %d times for two projects, want 1", h.sizes.loads)
	}
	if _, err := h.projectStats(context.Background(), statsIn{ProjectID: 1}); err != nil {
		t.Fatal(err)
	}
	if h.sizes.loads != 1 {
		t.Errorf("a second call within the TTL reloaded: %d", h.sizes.loads)
	}
}

// When dbstat cannot be read, size is null and the rest still answers.
func TestProjectStatsWithoutDbstat(t *testing.T) {
	h, _ := newTestHost(t)
	old := tableBytesSQL
	tableBytesSQL = `SELECT name, 0 FROM no_such_table`
	t.Cleanup(func() { tableBytesSQL = old })
	h.sizes = newSizeCache(time.Minute)
	out, err := h.projectStats(context.Background(), statsIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-21"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Projects[0].Size != nil || out.Projects[0].Totals.Views == 0 {
		t.Errorf("size %+v totals %+v; want null size and the totals", out.Projects[0].Size, out.Projects[0].Totals)
	}
}

// A failed reading is remembered for a minute: two calls take one reading,
// and both answer without sizes.
func TestProjectStatsFailedSizesAreCachedBriefly(t *testing.T) {
	h, _ := newTestHost(t)
	old := tableBytesSQL
	tableBytesSQL = `SELECT name, 0 FROM no_such_table`
	t.Cleanup(func() { tableBytesSQL = old })
	h.sizes = newSizeCache(time.Minute)
	for i := 0; i < 2; i++ {
		out, err := h.projectStats(context.Background(), statsIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-21"}})
		if err != nil {
			t.Fatal(err)
		}
		if out.Projects[0].Size != nil {
			t.Errorf("call %d: size %+v, want null", i, out.Projects[0].Size)
		}
	}
	if h.sizes.loads != 1 {
		t.Errorf("readings taken = %d across two calls, want 1", h.sizes.loads)
	}
	// Past the window the reading is tried again.
	h.sizes.failAt = time.Now().Add(-2 * failTTL)
	if _, err := h.projectStats(context.Background(), statsIn{ProjectID: 1}); err != nil {
		t.Fatal(err)
	}
	if h.sizes.loads != 2 {
		t.Errorf("readings taken = %d after the window, want 2", h.sizes.loads)
	}
}
