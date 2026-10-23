package native

import (
	"context"
	"math"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
)

// ratio divides one metric by another, each with its own where, as the
// system widgets' SQL divides them: users/actions-per-user.sql,
// groups/actions-per-group.sql and views/consent-share.sql, and the
// breakdowns, trends and comparisons the same division makes.
func TestRatioMatchesDuckDB(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	actions := []Filter{{Column: "family", Op: "in", Values: []string{"views", "product"}}}
	perActor := func(id string) Query {
		return Query{Kind: "stat", Table: "events", Where: append(slices.Clone(actions), Filter{Column: id, Op: "is_set"}),
			Metrics: []Metric{{Op: "ratio", As: "value", Round: ptr(1), Num: &Metric{Op: "count"}, Den: &Metric{Op: id[:len(id)-3] + "s"}}}}
	}
	consent := Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "ratio", As: "value",
		Num: &Metric{Op: "daily_sum", Of: "actors", Where: []Filter{{Column: "consent", Op: "eq", Values: []string{"1"}}}},
		Den: &Metric{Op: "daily_sum", Of: "actors", Where: []Filter{{Column: "consent", Op: "is_set"}}}}}}
	// Product events per buyer by country, the top 2 by views and Other:
	// both sides put together over the folded groups, then divided.
	const byCountryMetrics = `count(*) AS c, ROUND(count(*) FILTER (WHERE "family" = 'product')::DOUBLE
		/ NULLIF(count(DISTINCT "actor_id") FILTER (WHERE "family" = 'product'), 0), 3) AS pb`
	byCountry := `WITH g AS (SELECT "country", ` + byCountryMetrics + ` FROM events WHERE ` + inRange + ` GROUP BY "country"),
		top AS (SELECT * FROM g ORDER BY c DESC, "country" ASC NULLS LAST LIMIT 2) `
	product := []Filter{{Column: "family", Op: "eq", Values: []string{"product"}}}
	type tc struct {
		name     string
		q        Query
		from, to string
		cols     []string
		sql      []string
		prev     bool
	}
	cases := []tc{
		{name: "actions per user", q: perActor("user_id"), cols: []string{"value"}, sql: []string{widgetSQL(t, "users/actions-per-user.sql")}},
		{name: "actions per group", q: perActor("group_id"), cols: []string{"value"}, sql: []string{widgetSQL(t, "groups/actions-per-group.sql")}},
		{name: "consent share", q: consent, cols: []string{"value"}, sql: []string{widgetSQL(t, "views/consent-share.sql")}},
		{name: "breakdown with other", q: Query{Kind: "breakdown", Table: "events", Dimensions: []string{"country"}, Limit: 2, Other: true,
			Metrics: []Metric{{Op: "count"}, {Op: "ratio", As: "per_buyer", Round: ptr(3),
				Num: &Metric{Op: "count", Where: product}, Den: &Metric{Op: "actors", Where: product}}}},
			cols: []string{"country", "count", "per_buyer"},
			sql: []string{byCountry + `SELECT "country", c, pb FROM top ORDER BY c DESC, "country" ASC NULLS LAST`,
				byCountry + `SELECT 'Other', ` + byCountryMetrics + ` FROM events e WHERE ` + inRange + `
					AND NOT EXISTS (SELECT 1 FROM top WHERE top."country" IS NOT DISTINCT FROM e."country") HAVING count(*) > 0`}},
		{name: "breakdown ranked by the ratio", q: Query{Kind: "breakdown", Table: "views", Dimensions: []string{"path"}, Limit: 5, Order: "r",
			Metrics: []Metric{{Op: "ratio", As: "r", Round: ptr(2), Num: &Metric{Op: "count"}, Den: &Metric{Op: "actors"}}}},
			cols: []string{"path", "r"},
			sql: []string{`SELECT "path", ROUND(count(*)::DOUBLE / NULLIF(count(DISTINCT "actor_id"), 0), 2) AS r FROM views WHERE ` + inRange + `
				GROUP BY "path" ORDER BY r DESC NULLS LAST, "path" ASC NULLS LAST LIMIT 5`}},
		{name: "trend", q: Query{Kind: "trend", Table: "views", Metrics: []Metric{{Op: "ratio",
			Num: &Metric{Op: "count", Where: []Filter{{Column: "event_name", Op: "eq", Values: []string{"landing"}}}}, Den: &Metric{Op: "actors"}}}},
			cols: []string{"day", "ratio"},
			sql: []string{`SELECT "day", count(*) FILTER (WHERE "event_name" = 'landing')::DOUBLE / NULLIF(count(DISTINCT "actor_id"), 0)
				FROM views WHERE ` + inRange + ` GROUP BY "day" ORDER BY "day"`}},
		{name: "compare previous", q: Query{Kind: "stat", Table: "events", Compare: "previous", Metrics: []Metric{{Op: "count"},
			{Op: "ratio", As: "per_user", Round: ptr(2), Num: &Metric{Op: "count", Where: product}, Den: &Metric{Op: "users", Where: product}}}},
			from: "2026-08-26", prev: true, cols: []string{"count", "per_user", "previous_count", "previous_per_user"},
			sql: []string{`SELECT count(*), ROUND(count(*) FILTER (WHERE "family" = 'product')::DOUBLE
				/ NULLIF(count(DISTINCT "user_id") FILTER (WHERE "family" = 'product'), 0), 2) FROM events WHERE ` + inRange}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.from == "" {
				c.from = engineFrom
			}
			if c.to == "" {
				c.to = today
			}
			res, err := e.Answer(context.Background(), c.q, Params{Project: 1, From: date(t, c.from), To: date(t, c.to)}, engineRules)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(res.Columns, c.cols) {
				t.Fatalf("columns %q, want %q", res.Columns, c.cols)
			}
			var want [][]string
			for _, sql := range c.sql {
				want = append(want, duckTable(t, f, sql, c.from, c.to)...)
			}
			if c.prev {
				from, to := date(t, c.from), date(t, c.to)
				days := int(to.Time().Sub(from.Time()).Hours()/24) + 1
				pf := from.Time().AddDate(0, 0, -days).Format(time.DateOnly)
				pt := from.Time().AddDate(0, 0, -1).Format(time.DateOnly)
				want[0] = append(want[0], duckTable(t, f, c.sql[0], pf, pt)[0]...)
			}
			if len(want) == 0 || slices.ContainsFunc(want, func(r []string) bool { return r[len(r)-1] == "" }) {
				t.Fatalf("the SQL returns %q: a weak case", want)
			}
			sameTable(t, res.Rows, want)
			if res.Truncated || len(res.Approximate) > 0 {
				t.Errorf("truncated %v, approximate %q", res.Truncated, res.Approximate)
			}
		})
	}
}

// A ratio is empty when its denominator is 0 or empty, or its numerator
// empty; never a division by zero.
func TestRatioEmpty(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 2})
	never := []Filter{{Column: "path", Op: "eq", Values: []string{"/nowhere"}}}
	q := Query{Kind: "stat", Table: "views", Metrics: []Metric{
		{Op: "ratio", As: "den_zero", Num: &Metric{Op: "count"}, Den: &Metric{Op: "count", Where: never}},
		{Op: "ratio", As: "den_empty", Num: &Metric{Op: "count"}, Den: &Metric{Op: "daily_sum", Of: "actors", Where: never}},
		{Op: "ratio", As: "num_empty", Num: &Metric{Op: "max", Column: "display_width", Where: never}, Den: &Metric{Op: "count"}},
		{Op: "ratio", As: "num_zero", Num: &Metric{Op: "count", Where: never}, Den: &Metric{Op: "count"}},
	}}
	p := Params{Project: 1, From: date(t, engineFrom), To: date(t, today)}
	res, err := e.Answer(context.Background(), q, p, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"", "", "", "0"}; !slices.Equal(res.Rows[0], want) {
		t.Errorf("rows %q, want %q", res.Rows, want)
	}
	// No rows at all: every side 0 or empty.
	res, err = e.Answer(context.Background(), q, Params{Project: 1, From: date(t, "2026-07-01"), To: date(t, "2026-07-02")}, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"", "", "", ""}; !slices.Equal(res.Rows[0], want) {
		t.Errorf("no rows: %q, want %q", res.Rows, want)
	}
}

// A ratio is approximate when either side is.
func TestRatioApproximate(t *testing.T) {
	f := archivetest.New(t)
	d1, d2 := date(t, "2026-09-08").Time(), date(t, "2026-09-09").Time()
	f.Write(t, visits(1, d1, 0, 1000)...)
	f.Write(t, visits(1, d2, 500, 3000)...)
	f.Maintain(t, today)
	e := New(Options{Dir: f.Dir, Workers: 2})
	q := Query{Kind: "stat", Table: "views", Metrics: []Metric{
		{Op: "ratio", As: "exact", Num: &Metric{Op: "count"}, Den: &Metric{Op: "daily_sum", Of: "actors"}},
		{Op: "ratio", As: "estimated", Num: &Metric{Op: "count"}, Den: &Metric{Op: "actors"}}}}
	res, err := e.Answer(context.Background(), q, Params{Project: 1, From: date(t, "2026-09-08"), To: date(t, "2026-09-09")}, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows[0][0] != "1" {
		t.Errorf("exact %q, want 1", res.Rows[0][0])
	}
	got, err := strconv.ParseFloat(res.Rows[0][1], 64)
	if err != nil {
		t.Fatal(err)
	}
	if exact := 4000.0 / 3500; math.Abs(got-exact) > 0.02*exact {
		t.Errorf("estimated %v, want about %v", got, exact)
	}
	if !slices.Equal(res.Approximate, []string{"estimated"}) {
		t.Errorf("approximate %q, want estimated", res.Approximate)
	}
}
