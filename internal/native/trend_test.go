package native

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
)

// filled is sql, whose rows are (day, series, value), with a row for
// every day of its range and every one of series, in that order: a row
// sql lacks reads 0 for a counted series, NULL for the others; ordered by
// day, then series' order. It is the SQL a widget filling its days runs.
func filled(sql string, series []string, counted []bool) string {
	var vals []string
	for i, s := range series {
		vals = append(vals, fmt.Sprintf("('%s', %d, %t)", s, i, counted[i]))
	}
	return `WITH w AS (` + sql + `),
		d AS (SELECT CAST(range AS DATE) AS x FROM range(DATE '%[1]s', DATE '%[2]s' + INTERVAL 1 DAY, INTERVAL 1 DAY)),
		s AS (SELECT * FROM (VALUES ` + strings.Join(vals, ", ") + `) v(series, k, counted))
		SELECT d.x, s.series, CASE WHEN s.counted THEN COALESCE(w.y, 0) ELSE w.y END
		FROM d CROSS JOIN s LEFT JOIN w ON w.x = d.x AND w.series = s.series ORDER BY d.x, s.k`
}

// A trend with fill has a row for every day of the range (and every kept
// series): a counted metric's reads 0 where it has none, the others
// empty. A trend of several metrics is long (day, series, value), each
// metric's rows following its own days unless filled. They match the
// built-in widgets' SQL: daily active users (new and returning) and
// visitors and views.
func TestTrendMatchesGolden(t *testing.T) {
	ids := identitiesFixture(t)
	f := engineFixture(t)
	idEngine := New(Options{Dir: ids.Dir, Workers: 3})
	e := New(Options{Dir: f.Dir, Workers: 3})
	status := func(v string) []Filter { return []Filter{{Column: "user_status", Op: "eq", Values: []string{v}}} }
	early := []Filter{{Column: "day", Op: "lte", Values: []string{"2026-08-10"}}}
	landing := Filter{Column: "event_name", Op: "eq", Values: []string{"landing"}}
	const before = "2026-07-28" // days without rows before the fixture's
	type tc struct {
		name     string
		e        *Engine
		project  int64
		q        Query
		from, to string
		rules    archive.Rules
		sql      string
		// check says what the case must show beside the SQL's rows.
		check func(t *testing.T, rows [][]string)
	}
	zeros := func(t *testing.T, rows [][]string) {
		t.Helper()
		if !slices.ContainsFunc(rows, func(r []string) bool { return r[len(r)-1] == "0" }) {
			t.Errorf("rows %q: no day filled with 0", rows)
		}
	}
	dau := widgetSQLOf(t, "users/daily-active-users.sql", identitiesProject)
	visitors := widgetSQL(t, "views/visitors-and-views.sql")
	perMetric := `SELECT x, series, y FROM (
		SELECT "day" AS x, 'all' AS series, 0 AS k, count(*) AS y FROM views WHERE ` + inRange + ` GROUP BY 1
		UNION ALL SELECT "day", 'early', 1, count(*) FROM views WHERE ` + inRange + ` AND "day" <= DATE '2026-08-10' GROUP BY 1
		UNION ALL SELECT "day", 'early_width', 2, avg("display_width") FROM views WHERE ` + inRange + ` AND "day" <= DATE '2026-08-10' GROUP BY 1
		UNION ALL SELECT "day", 'early_peak', 3, count(DISTINCT "actor_id") FROM views WHERE ` + inRange + ` AND "day" <= DATE '2026-08-10' GROUP BY 1
	) ORDER BY x, k`
	perMetricQ := Query{Kind: "trend", Table: "views", Metrics: []Metric{{Op: "count", As: "all"}, {Op: "count", As: "early", Where: early},
		{Op: "avg", Column: "display_width", As: "early_width", Where: early}, {Op: "daily_max", Of: "actors", As: "early_peak", Where: early}}}
	perMetricFilled := perMetricQ
	perMetricFilled.Fill = "zero"
	bySeries := `WITH g AS (SELECT "country", count(*) AS c FROM views WHERE ` + inRange + ` AND "event_name" = 'landing' GROUP BY 1),
		top AS (SELECT "country" FROM g ORDER BY c DESC, "country" ASC NULLS LAST LIMIT 2),
		d AS (SELECT CAST(range AS DATE) AS x FROM range(DATE '%[1]s', DATE '%[2]s' + INTERVAL 1 DAY, INTERVAL 1 DAY)),
		v AS (SELECT "day", "country", count(*) AS c FROM views WHERE ` + inRange + ` AND "event_name" = 'landing' GROUP BY ALL)
		SELECT d.x, top."country", COALESCE(v.c, 0) FROM d CROSS JOIN top
		LEFT JOIN v ON v."day" = d.x AND v."country" IS NOT DISTINCT FROM top."country"
		ORDER BY d.x, top."country" ASC NULLS LAST`
	cases := []tc{
		{name: "daily active users, filled", e: idEngine, project: identitiesProject, from: "2026-08-20", to: today,
			q: Query{Kind: "trend", Table: "events", Fill: "zero",
				Where:   []Filter{{Column: "family", Op: "in", Values: []string{"views", "product"}}, {Column: "user_id", Op: "is_set"}},
				Metrics: []Metric{{Op: "users", As: "New users", Where: status("new")}, {Op: "users", As: "Returning users", Where: status("returning")}}},
			sql: filled(dau, []string{"New users", "Returning users"}, []bool{true, true}),
			check: func(t *testing.T, rows [][]string) {
				zeros(t, rows)
				// Days the users' views have, and new ones on some only.
				var newDays, empty int
				for _, r := range rows {
					if r[1] == "New users" && r[2] != "0" {
						newDays++
					}
					if r[0] < identitiesFrom {
						empty++
					}
				}
				if newDays == 0 || newDays == len(rows)/2 || empty != 10 {
					t.Errorf("%d days with new users of %d, %d rows before the first day: a weak case", newDays, len(rows)/2, empty)
				}
			}},
		{name: "visitors and views", e: e, project: 1, from: engineFrom, to: today, rules: engineRules,
			q:   Query{Kind: "trend", Table: "views", Metrics: []Metric{{Op: "count", As: "Views"}, {Op: "actors", As: "Visitors"}}},
			sql: visitors},
		{name: "visitors and views, filled", e: e, project: 1, from: before, to: today, rules: engineRules,
			q:     Query{Kind: "trend", Table: "views", Fill: "zero", Metrics: []Metric{{Op: "count", As: "Views"}, {Op: "actors", As: "Visitors"}}},
			sql:   filled(visitors, []string{"Views", "Visitors"}, []bool{true, true}),
			check: zeros},
		{name: "metrics with their own days", e: e, project: 1, from: engineFrom, to: today, rules: engineRules, q: perMetricQ, sql: perMetric,
			check: func(t *testing.T, rows [][]string) {
				if len(rows) >= 40*4 || len(rows) <= 40 {
					t.Errorf("%d rows over 40 days and 4 metrics: want the early ones on their days only", len(rows))
				}
			}},
		{name: "metrics with their own days, filled", e: e, project: 1, from: before, to: today, rules: engineRules, q: perMetricFilled,
			sql: filled(perMetric, []string{"all", "early", "early_width", "early_peak"}, []bool{true, true, false, true}),
			check: func(t *testing.T, rows [][]string) {
				zeros(t, rows)
				if len(rows) != 45*4 {
					t.Errorf("%d rows, want 45 days of 4 metrics", len(rows))
				}
			}},
		{name: "one metric, filled", e: e, project: 1, from: before, to: today, rules: engineRules,
			q: Query{Kind: "trend", Table: "views", Fill: "zero", Where: []Filter{landing}, Metrics: []Metric{{Op: "count"}}},
			sql: `WITH w AS (SELECT "day" AS x, count(*) AS y FROM views WHERE ` + inRange + ` AND "event_name" = 'landing' GROUP BY 1),
				d AS (SELECT CAST(range AS DATE) AS x FROM range(DATE '%[1]s', DATE '%[2]s' + INTERVAL 1 DAY, INTERVAL 1 DAY))
				SELECT d.x, COALESCE(w.y, 0) FROM d LEFT JOIN w USING (x) ORDER BY 1`,
			check: zeros},
		{name: "series, filled", e: e, project: 1, from: before, to: today, rules: engineRules,
			q:   Query{Kind: "trend", Table: "views", Fill: "zero", Series: "country", SeriesLimit: 2, Where: []Filter{landing}, Metrics: count()},
			sql: bySeries,
			check: func(t *testing.T, rows [][]string) {
				zeros(t, rows)
				if len(rows) != 45*2 {
					t.Errorf("%d rows, want 45 days of 2 series", len(rows))
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := c.e.Answer(context.Background(), c.q, Params{Project: c.project, From: date(t, c.from), To: date(t, c.to)}, c.rules)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(res.Columns, c.q.Columns()) {
				t.Fatalf("columns %q, want %q", res.Columns, c.q.Columns())
			}
			want := goldenTableWith(t, c.rules, c.sql, c.from, c.to, res.Rows)
			if len(want) == 0 {
				t.Fatal("the SQL returns no rows: a weak case")
			}
			sameTable(t, res.Rows, want)
			if c.check != nil {
				c.check(t, res.Rows)
			}
			if res.Truncated || len(res.Approximate) > 0 {
				t.Errorf("truncated %v, approximate %q", res.Truncated, res.Approximate)
			}
		})
	}
}

// Over a range without rows, a filled trend still has every day: an
// unsplit one each metric's, 0 or empty; a split one none, having no
// series.
func TestTrendFillsEmptyRange(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 2})
	p := Params{Project: 1, From: date(t, "2026-07-01"), To: date(t, "2026-07-02")}
	for _, c := range []struct {
		name string
		q    Query
		want [][]string
	}{
		{"one metric", Query{Kind: "trend", Table: "views", Fill: "zero", Metrics: []Metric{{Op: "daily_sum", Of: "actors"}}},
			[][]string{{"2026-07-01", "0"}, {"2026-07-02", "0"}}},
		{"several metrics", Query{Kind: "trend", Table: "measures", Fill: "zero", Metrics: []Metric{{Op: "weight"},
			{Op: "percentile", Q: 0.75}, {Op: "ratio", Num: &Metric{Op: "count"}, Den: &Metric{Op: "actors"}}, {Op: "days"}}},
			[][]string{{"2026-07-01", "weight", "0"}, {"2026-07-01", "p75", ""}, {"2026-07-01", "ratio", ""}, {"2026-07-01", "days", "0"},
				{"2026-07-02", "weight", "0"}, {"2026-07-02", "p75", ""}, {"2026-07-02", "ratio", ""}, {"2026-07-02", "days", "0"}}},
		{"several metrics unfilled", Query{Kind: "trend", Table: "views", Metrics: []Metric{{Op: "count"}, {Op: "actors"}}}, [][]string{}},
		{"split", Query{Kind: "trend", Table: "views", Fill: "zero", Series: "path", Metrics: count()}, [][]string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, err := e.Answer(context.Background(), c.q, p, engineRules)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.EqualFunc(res.Rows, c.want, slices.Equal) {
				t.Errorf("rows %q, want %q", res.Rows, c.want)
			}
		})
	}
}

// Other never folds exactly one group: with limit 4, five groups are all
// listed, six are four and Other, as views/browsers.sql has it
// (ROW_NUMBER() <= 4, or every one when there are at most five).
func TestOtherNeverOneGroup(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	browsersStyle := func(paths string) string {
		// Other's actors are distinct over the folded paths together, not
		// a sum of each path's.
		return `WITH b AS (SELECT "path" AS label, ROW_NUMBER() OVER (ORDER BY count(*) DESC, "path") AS n
			FROM views WHERE ` + inRange + ` AND "path" IN (` + paths + `) GROUP BY "path"),
			k AS (SELECT label, n, CASE WHEN n <= 4 OR (SELECT count(*) FROM b) <= 5 THEN label ELSE 'Other' END AS g FROM b)
			SELECT k.g, count(*), count(DISTINCT "actor_id")
			FROM views JOIN k ON "path" = k.label WHERE ` + inRange + `
			GROUP BY k.g ORDER BY MIN(k.n)`
	}
	for _, c := range []struct {
		name  string
		paths []string
		rows  int
		other bool
	}{
		{"four", []string{"/p0", "/p1", "/p2", "/p3"}, 4, false},
		{"five", []string{"/p0", "/p1", "/p2", "/p3", "/q0"}, 5, false},
		{"six", []string{"/p0", "/p1", "/p2", "/p3", "/q0", "/p4"}, 5, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := Query{Kind: "breakdown", Table: "views", Dimensions: []string{"path"}, Limit: 4, Other: true,
				Where:   []Filter{{Column: "path", Op: "in", Values: c.paths}},
				Metrics: []Metric{{Op: "count"}, {Op: "actors"}}}
			res, err := e.Answer(context.Background(), q, Params{Project: 1, From: date(t, engineFrom), To: date(t, today)}, engineRules)
			if err != nil {
				t.Fatal(err)
			}
			want := goldenTable(t, browsersStyle("'"+strings.Join(c.paths, "', '")+"'"), engineFrom, today, res.Rows)
			sameTable(t, res.Rows, want)
			last := res.Rows[len(res.Rows)-1][0]
			if len(res.Rows) != c.rows || (last == "Other") != c.other {
				t.Errorf("rows %q: want %d, Other %v", res.Rows, c.rows, c.other)
			}
		})
	}
}
