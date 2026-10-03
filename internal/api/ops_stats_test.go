package api

import (
	"context"
	"errors"
	"strconv"
	"testing"

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
	if len(p.UnusedAttributes) != 0 {
		t.Errorf("plan is carried, yet unused = %v", p.UnusedAttributes)
	}
}

// Without project_id every project comes back; a project with no data has
// a full series of zeros and nulls, and its declared keys are unused.
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
	if len(docs.UnusedAttributes) != 1 || docs.UnusedAttributes[0] != "plan" {
		t.Errorf("docs unused = %v, want [plan]", docs.UnusedAttributes)
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
	if u := out.Projects[0].UnusedAttributes; len(u) != 1 || u[0] != "never_sent" {
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
