package native

import (
	"context"
	"slices"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
)

// The vitals' ratings, and their shares, as vitals/ratings.sql reads them.
func TestRatingsMatchWidget(t *testing.T) {
	f := vitalsFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	q := Query{Kind: "breakdown", Table: "measures", Dimensions: []string{"vital", "vital_rating"}, Order: "vital",
		Where:   []Filter{{Column: "vital", Op: "is_set"}},
		Metrics: []Metric{{Op: "share", Of: "weight", As: "y"}}}
	res, err := e.Answer(context.Background(), q, Params{Project: 1, From: date(t, vitalsFrom), To: date(t, today)}, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"vital", "vital_rating", "y"}; !slices.Equal(res.Columns, want) {
		t.Fatalf("columns %q, want %q", res.Columns, want)
	}
	want := goldenTable(t, widgetSQL(t, "vitals/ratings.sql"), vitalsFrom, today, res.Rows)
	if len(want) < 6 {
		t.Fatalf("the SQL returns %d rows: a weak case", len(want))
	}
	sameTable(t, res.Rows, want)
	if res.Truncated || len(res.Approximate) > 0 {
		t.Errorf("truncated %v, approximate %q", res.Truncated, res.Approximate)
	}

	// Without the where, the other measures are a group of NULLs, last.
	q.Where = nil
	q.Metrics = []Metric{{Op: "count"}}
	res, err = e.Answer(context.Background(), q, Params{Project: 1, From: date(t, vitalsFrom), To: date(t, today)}, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	last := res.Rows[len(res.Rows)-1]
	want = goldenTable(t, `SELECT CASE WHEN event_name IN ('$lcp', '$inp', '$cls', '$fcp', '$ttfb') THEN upper(substr(event_name, 2)) END AS v,
		count(*) FROM measures WHERE `+inRange+` GROUP BY v HAVING v IS NULL`, vitalsFrom, today, [][]string{{last[0], last[2]}})
	if last[0] != "" || last[1] != "" || last[2] != want[0][1] {
		t.Errorf("the non-vitals' row %q, want NULL, NULL, %s", last, want[0][1])
	}
}

// share's partitions, as SQL's window SUM(x) OVER (PARTITION BY the first
// dimension) reads them before LIMIT: an Other row's is every group's.
func TestShareMatchesDuckDB(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	type tc struct {
		name string
		q    Query
		cols []string
		sql  string
	}
	cases := []tc{
		{"country with other", Query{Kind: "breakdown", Table: "events", Dimensions: []string{"country"}, Limit: 2, Other: true,
			Metrics: []Metric{{Op: "count"}, {Op: "share", Of: "count"}}},
			[]string{"country", "count", "share_count"},
			`WITH g AS (SELECT "country", count(*) AS c FROM events WHERE ` + inRange + ` GROUP BY "country"),
			t AS (SELECT sum(c) AS total FROM g),
			top AS (SELECT * FROM g ORDER BY c DESC, "country" ASC NULLS LAST LIMIT 2)
			SELECT * EXCLUDE (o) FROM (
				SELECT 0 AS o, "country", c, c::DOUBLE / total FROM top, t
				UNION ALL
				SELECT 1, 'Other', sum(c), sum(c)::DOUBLE / any_value(total) FROM g, t
				WHERE NOT EXISTS (SELECT 1 FROM top WHERE top."country" IS NOT DISTINCT FROM g."country"))
			ORDER BY o, c DESC, "country" ASC NULLS LAST`},
		{"by country then family", Query{Kind: "breakdown", Table: "events", Dimensions: []string{"country", "family"}, Limit: 4, Other: true,
			Metrics: []Metric{{Op: "count"}, {Op: "share", Of: "count", As: "s", Round: ptr(4)}}},
			[]string{"country", "family", "count", "s"},
			`WITH g AS (SELECT "country", "family", count(*) AS c,
				ROUND(count(*)::DOUBLE / SUM(count(*)) OVER (PARTITION BY "country"), 4) AS s
				FROM events WHERE ` + inRange + ` GROUP BY "country", "family"),
			t AS (SELECT sum(c) AS total FROM g),
			top AS (SELECT * FROM g ORDER BY c DESC, "country" ASC NULLS LAST, "family" ASC NULLS LAST LIMIT 4)
			SELECT * EXCLUDE (o) FROM (
				SELECT 0 AS o, "country", "family", c, s FROM top
				UNION ALL
				SELECT 1, 'Other', 'Other', sum(c), ROUND(sum(c)::DOUBLE / any_value(total), 4) FROM g, t
				WHERE NOT EXISTS (SELECT 1 FROM top WHERE top."country" IS NOT DISTINCT FROM g."country" AND top."family" = g."family"))
			ORDER BY o, c DESC, "country" ASC NULLS LAST, "family" ASC NULLS LAST`},
		{"filtered share", Query{Kind: "breakdown", Table: "events", Dimensions: []string{"path"}, Limit: 100,
			Metrics: []Metric{{Op: "count"}, {Op: "share", Of: "count", As: "views",
				Where: []Filter{{Column: "family", Op: "eq", Values: []string{"views"}}}}}},
			[]string{"path", "count", "views"},
			`SELECT "path", count(*) AS c, (count(*) FILTER (WHERE "family" = 'views'))::DOUBLE
				/ SUM(count(*) FILTER (WHERE "family" = 'views')) OVER ()
				FROM events WHERE ` + inRange + ` GROUP BY "path" ORDER BY c DESC, "path" ASC NULLS LAST`},
		{"measures weight", Query{Kind: "breakdown", Table: "measures", Dimensions: []string{"event_name"}, Order: "event_name",
			Metrics: []Metric{{Op: "share", Of: "weight"}, {Op: "share", Of: "count"}}},
			[]string{"event_name", "share_weight", "share_count"},
			`SELECT "event_name", SUM(1 / sample_rate) / SUM(SUM(1 / sample_rate)) OVER (), count(*)::DOUBLE / SUM(count(*)) OVER ()
				FROM measures WHERE ` + inRange + ` GROUP BY "event_name" ORDER BY "event_name" ASC NULLS LAST`},
		{"trend by country", Query{Kind: "trend", Table: "events", Series: "country", SeriesLimit: 2,
			Metrics: []Metric{{Op: "share", Of: "count"}}},
			[]string{"day", "series", "share_count"},
			`WITH top AS (SELECT "country" FROM events WHERE ` + inRange + ` GROUP BY "country"
				ORDER BY count(*) DESC, "country" ASC NULLS LAST LIMIT 2),
			d AS (SELECT "day", "country", count(*)::DOUBLE / SUM(count(*)) OVER (PARTITION BY "day") AS s
				FROM events WHERE ` + inRange + ` GROUP BY "day", "country")
			SELECT "day", d."country", s FROM d JOIN top ON d."country" IS NOT DISTINCT FROM top."country"
			ORDER BY "day", d."country" ASC NULLS LAST`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := e.Answer(context.Background(), c.q, Params{Project: 1, From: date(t, engineFrom), To: date(t, today)}, engineRules)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(res.Columns, c.cols) {
				t.Fatalf("columns %q, want %q", res.Columns, c.cols)
			}
			want := goldenTable(t, c.sql, engineFrom, today, res.Rows)
			if len(want) < 3 {
				t.Fatalf("the SQL returns %d rows: a weak case", len(want))
			}
			sameTable(t, res.Rows, want)
			if res.Truncated || len(res.Approximate) > 0 {
				t.Errorf("truncated %v, approximate %q", res.Truncated, res.Approximate)
			}
		})
	}
}
