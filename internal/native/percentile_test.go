package native

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// vitalsFrom is the first day of the vitals fixture; it runs to today.
const vitalsFrom = "2026-09-01"

// vitalsFixture writes measures of project 1 from vitalsFrom to today:
// the vitals $lcp, $inp and $cls on 60 paths and the empty one, with a
// sample rate of 0.5 on a third of the samples, zeros (bucket -1000) among
// the $cls values and no $cls on September 4 and 7, nothing at all on
// September 6; and an unknown system name $foo, the custom metrics load (time and number, one sample
// without a value), payload and score. The days before today are dumped
// to Parquet; today's stay in a log.
func vitalsFixture(t testing.TB) *archivetest.Files {
	t.Helper()
	f := archivetest.New(t)
	start := date(t, vitalsFrom).Time()
	for d := 0; ; d++ {
		at := start.AddDate(0, 0, d)
		day := at.Format(time.DateOnly)
		if day > today {
			break
		}
		if day == "2026-09-06" {
			continue
		}
		var evs []store.Event
		add := func(name, measure string, k int, v *float64) {
			e := store.Event{ProjectID: 1, Family: store.FamilyMeasures, EventName: name, Measure: measure,
				TS: at.Add(8*time.Hour + time.Duration(len(evs))*time.Second), ID: fmt.Sprintf("m-%s-%d", day, len(evs)),
				Host: "example.com", ActorID: fmt.Sprintf("a%02d", (k+d)%30), Value: v,
				SampleRate: []float64{0.5, 1, 0}[k%3]}
			if k%11 != 0 {
				e.Path = fmt.Sprintf("/p%d", (k*3+d)%60)
			}
			evs = append(evs, e)
		}
		val := func(x float64) *float64 { return &x }
		for k := range 150 {
			add("$lcp", store.MeasureTime, k, val(float64(800+(k*37+d*11)%4000)))
			add("$inp", store.MeasureTime, k, val(float64(40+(k*13+d)%600)))
		}
		if day != "2026-09-04" && day != "2026-09-07" {
			for k := range 100 {
				add("$cls", store.MeasureNumber, k, val(0.001*float64((k*7+d)%400)))
			}
		}
		for k := range 7 {
			add("$foo", store.MeasureNumber, k, val(float64(k+1)))
		}
		for k := range 40 {
			add("load", store.MeasureTime, k, val(float64(k*k%900)+0.25))
			add("load", store.MeasureNumber, k, val(float64(k%17)))
			add("payload", store.MeasureSize, k, val(float64(1000+k*731%50000)))
			add("score", store.MeasureNumber, k, val(float64(k%5)*0.2))
		}
		add("load", store.MeasureTime, 1, nil)
		f.Write(t, evs...)
	}
	f.Maintain(t, today)
	return f
}

// widgetSQL is a system widget's SQL for duckTable: project 1, its range
// %[1]s to %[2]s.
func widgetSQL(t *testing.T, name string) string {
	t.Helper()
	return widgetSQLOf(t, name, 1)
}

// widgetSQLOf is widgetSQL for project.
func widgetSQLOf(t *testing.T, name string, project int64) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "reporting", "system", name))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(b), "%", "%%")
	return strings.NewReplacer(":project", strconv.FormatInt(project, 10), ":from", "DATE '%[1]s'", ":to", "DATE '%[2]s'").Replace(s)
}

// pctSQL is the widgets' percentile recipe over measures grouped by
// group (a column list, "" for none) where cond holds: per group the
// sample count, weight, weighted mean, and the percentiles qs.
func pctSQL(group, cond string, qs ...string) string {
	by, part, sel := "", "", ""
	if group != "" {
		by, part, sel = group+", ", "PARTITION BY "+group, group+", "
	}
	var pcts []string
	for _, q := range qs {
		pcts = append(pcts, "MIN(approx_value) FILTER (WHERE run >= "+q+" * total)")
	}
	groupBy := ""
	if group != "" {
		groupBy = " GROUP BY " + group + " ORDER BY " + group
	}
	return `WITH h AS (
		SELECT ` + by + `bucket, CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
			count(*) AS samples, SUM(1 / sample_rate) AS w, SUM(value / sample_rate) AS s
		FROM measures WHERE ` + inRange + ` AND ` + cond + ` GROUP BY ` + by + `bucket),
	c AS (SELECT ` + sel + `samples, w, s, approx_value, SUM(w) OVER (` + part + ` ORDER BY bucket) AS run, SUM(w) OVER (` + part + `) AS total FROM h)
	SELECT ` + sel + `SUM(samples), SUM(w), SUM(s) / SUM(w), ` + strings.Join(pcts, ", ") + ` FROM c` + groupBy
}

func TestPercentilesMatchWidgets(t *testing.T) {
	f := vitalsFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	eq := func(col, v string) []Filter { return []Filter{{Column: col, Op: "eq", Values: []string{v}}} }
	p75 := func(name string, round int, where []Filter) Metric {
		return Metric{Op: "percentile", Q: 0.75, As: name, Round: ptr(round), Where: where}
	}
	type tc struct {
		name string
		q    Query
		cols []string
		sql  string
	}
	cases := []tc{
		{"lcp p75", Query{Kind: "stat", Table: "measures", Where: eq("event_name", "$lcp"),
			Metrics: []Metric{{Op: "percentile", Q: 0.75, As: "value", Round: ptr(0)}}},
			[]string{"value"}, widgetSQL(t, "vitals/lcp.sql")},
		{"cls p75 by day", Query{Kind: "trend", Table: "measures", Where: eq("event_name", "$cls"),
			Metrics: []Metric{{Op: "percentile", Q: 0.75, Round: ptr(3)}}},
			[]string{"day", "p75"}, widgetSQL(t, "vitals/cls-by-day.sql")},
		{"metrics", Query{Kind: "breakdown", Table: "measures", Dimensions: []string{"event_name", "measure"}, Limit: 100,
			Where: []Filter{{Column: "event_name", Op: "not_prefix", Values: []string{"$"}}},
			Metrics: []Metric{{Op: "count", As: "samples"}, {Op: "mean", Round: ptr(3)},
				{Op: "percentile", Q: 0.5, Round: ptr(3)}, {Op: "percentile", Q: 0.75, Round: ptr(3)},
				{Op: "percentile", Q: 0.95, Round: ptr(3)}}},
			[]string{"event_name", "measure", "samples", "mean", "p50", "p75", "p95"}, widgetSQL(t, "measures/metrics.sql")},
		{"not_prefix leaves out every system name", Query{Kind: "breakdown", Table: "measures", Dimensions: []string{"event_name"}, Order: "event_name",
			Where:   []Filter{{Column: "event_name", Op: "not_prefix", Values: []string{"$"}}},
			Metrics: []Metric{{Op: "count", As: "samples"}, {Op: "mean", Round: ptr(3)}}},
			[]string{"event_name", "samples", "mean"},
			`SELECT "event_name", count(*), ROUND(SUM(value / sample_rate) / SUM(1 / sample_rate), 3) FROM measures
			WHERE ` + inRange + ` AND "event_name" NOT LIKE '$%%' GROUP BY "event_name" ORDER BY "event_name"`},
		{"by page", Query{Kind: "breakdown", Table: "measures", Dimensions: []string{"path"}, Limit: 50,
			Where: []Filter{{Column: "event_name", Op: "in", Values: []string{"$lcp", "$inp", "$cls"}},
				{Column: "path", Op: "ne", Values: []string{""}}},
			Metrics: []Metric{{Op: "count", As: "samples", Where: eq("event_name", "$lcp")},
				p75("lcp", 0, eq("event_name", "$lcp")), p75("inp", 0, eq("event_name", "$inp")),
				p75("cls", 3, eq("event_name", "$cls"))}},
			[]string{"path", "samples", "lcp", "inp", "cls"}, widgetSQL(t, "vitals/by-page.sql")},
		{"weight and mean with other", Query{Kind: "breakdown", Table: "measures", Dimensions: []string{"event_name"}, Limit: 2, Other: true,
			Order:   "event_name",
			Where:   []Filter{{Column: "event_name", Op: "ne", Values: []string{"$foo"}}},
			Metrics: []Metric{{Op: "count"}, {Op: "weight"}, {Op: "mean"}, {Op: "percentile", Q: 0.999}, {Op: "percentile", Q: 0.05}}},
			[]string{"event_name", "count", "weight", "mean", "p99.9", "p5"},
			`SELECT * EXCLUDE o FROM (SELECT 0 AS o, * FROM (` + pctSQL(`"event_name"`, `"event_name" IN ('$cls', '$inp')`, "0.999", "0.05") + `)
			UNION ALL SELECT 1, 'Other', * FROM (` + pctSQL("", `"event_name" NOT IN ('$cls', '$inp', '$foo')`, "0.999", "0.05") + `)) ORDER BY o, 2`},
		{"stat compare", Query{Kind: "stat", Table: "measures", Where: eq("event_name", "$cls"), Compare: "previous",
			Metrics: []Metric{{Op: "count"}, {Op: "weight"}, {Op: "mean"}, {Op: "percentile", Q: 0.9}, {Op: "percentile", Q: 0.1}}},
			[]string{"count", "weight", "mean", "p90", "p10", "previous_count", "previous_weight", "previous_mean", "previous_p90", "previous_p10"},
			pctSQL("", `"event_name" = '$cls'`, "0.9", "0.1")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from := vitalsFrom
			if c.q.Compare != "" {
				from = "2026-09-06"
			}
			res, err := e.Answer(context.Background(), c.q, Params{Project: 1, From: date(t, from), To: date(t, today)}, archive.Rules{})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(res.Columns, c.cols) {
				t.Fatalf("columns %q, want %q", res.Columns, c.cols)
			}
			want := duckTable(t, f, c.sql, from, today)
			if c.q.Compare != "" {
				want[0] = append(want[0], duckTable(t, f, c.sql, "2026-08-31", "2026-09-05")[0]...)
			}
			if len(want) < 2 && c.q.Kind != "stat" {
				t.Fatalf("the SQL returns %d rows: a weak case", len(want))
			}
			sameTable(t, res.Rows, want)
			if res.Truncated || len(res.Approximate) > 0 {
				t.Errorf("truncated %v, approximate %q", res.Truncated, res.Approximate)
			}
		})
	}
}

// A percentile picks the bucket whose running weight first reaches q of
// the total, an exact boundary included, as DuckDB's window does; bucket
// -1000 reads 0.
func TestPercentileBoundary(t *testing.T) {
	f := archivetest.New(t)
	at := date(t, today).Time().Add(time.Hour)
	var evs []store.Event
	add := func(name string, v, rate float64) {
		evs = append(evs, store.Event{ProjectID: 1, Family: store.FamilyMeasures, EventName: name, Measure: store.MeasureNumber,
			TS: at.Add(time.Duration(len(evs)) * time.Second), ID: fmt.Sprintf("b%d", len(evs)), Value: &v, SampleRate: rate})
	}
	for _, v := range []float64{10, 20, 30, 40} { // weights 1, 1, 1, 1: q 0.5 lands on 20's run exactly
		add("even", v, 1)
	}
	add("half", 0, 0.5) // weight 2 in bucket -1000: q 0.5 lands on it exactly
	add("half", 5, 1)
	add("half", 7, 1)
	f.Write(t, evs...)
	e := New(Options{Dir: f.Dir})
	q := Query{Kind: "breakdown", Table: "measures", Dimensions: []string{"event_name"}, Order: "event_name",
		Metrics: []Metric{{Op: "percentile", Q: 0.5}, {Op: "percentile", Q: 0.75}, {Op: "weight"}, {Op: "mean"}}}
	res, err := e.Answer(context.Background(), q, Params{Project: 1, From: date(t, today), To: date(t, today)}, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	want := duckTable(t, f, `SELECT event_name, p50, p75, w, m FROM (`+pctSQL(`"event_name"`, "true", "0.5", "0.75")+`) t(event_name, n, w, m, p50, p75) ORDER BY event_name`, today, today)
	sameTable(t, res.Rows, want)
	approx := func(b int64) string { return value{f: 2 * math.Pow(1.04, float64(b)) / 2.04}.String() }
	if got, w := res.Rows[0][1], approx(bucketOf(20, false)); !sameCell(got, w) {
		t.Errorf("even p50 = %s, want 20's bucket value %s", got, w)
	}
	if got := res.Rows[1][1]; got != "0" {
		t.Errorf("half p50 = %s, want 0 (bucket -1000)", got)
	}
}
