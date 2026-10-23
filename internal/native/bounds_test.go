package native

import (
	"context"
	"slices"
	"testing"
	"time"
)

// min and max take day, ts and received_at on the event tables (day,
// started_at and ended_at on sessions), as the system widgets' SQL takes
// them: users/top-users.sql's and groups/top-groups.sql's "Last seen",
// and the newest arrival usage reads; a day reads as YYYY-MM-DD, a time
// as DuckDB prints a TIMESTAMP.
func TestBoundsMatchDuckDB(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	actions := []Filter{{Column: "family", Op: "in", Values: []string{"views", "product"}}}
	top := func(id string) Query {
		return Query{Kind: "breakdown", Table: "events", Dimensions: []string{id}, Limit: 100,
			Where:   append(slices.Clone(actions), Filter{Column: id, Op: "is_set"}),
			Metrics: []Metric{{Op: "count", As: "actions"}, {Op: "max", Column: "day", As: "last_seen"}}}
	}
	product := []Filter{{Column: "family", Op: "eq", Values: []string{"product"}}}
	const byCountryMetrics = `count(*) AS c, max("received_at") FILTER (WHERE "family" = 'product') AS r, min("day") AS d`
	byCountry := `WITH g AS (SELECT "country", ` + byCountryMetrics + ` FROM events WHERE ` + inRange + ` GROUP BY "country"),
		top AS (SELECT * FROM g ORDER BY c DESC, "country" ASC NULLS LAST LIMIT 2) `
	type tc struct {
		name     string
		q        Query
		from, to string
		cols     []string
		sql      []string
		prev     bool
	}
	cases := []tc{
		{name: "top users last seen", q: top("user_id"), cols: []string{"user_id", "actions", "last_seen"},
			sql: []string{`SELECT "User", "Actions", "Last seen" FROM (` + widgetSQL(t, "users/top-users.sql") + `) ORDER BY "Actions" DESC, "User"`}},
		{name: "top groups last seen", q: top("group_id"), cols: []string{"group_id", "actions", "last_seen"},
			sql: []string{`SELECT "Group", "Actions", "Last seen" FROM (` + widgetSQL(t, "groups/top-groups.sql") + `) ORDER BY "Actions" DESC, "Group"`}},
		{name: "first and last day per user", q: Query{Kind: "breakdown", Table: "product", Dimensions: []string{"user_id"}, Order: "user_id",
			Metrics: []Metric{{Op: "min", Column: "day"}, {Op: "max", Column: "day"}}},
			cols: []string{"user_id", "min_day", "max_day"},
			sql: []string{`SELECT "user_id", min("day"), max("day") FROM product WHERE ` + inRange + `
				GROUP BY "user_id" ORDER BY "user_id" ASC NULLS LAST LIMIT 20`}},
		{name: "newest arrival", q: Query{Kind: "stat", Table: "events", Compare: "previous", Metrics: []Metric{
			{Op: "min", Column: "ts"}, {Op: "max", Column: "received_at"}}},
			from: "2026-08-26", prev: true, cols: []string{"min_ts", "max_received_at", "previous_min_ts", "previous_max_received_at"},
			sql: []string{`SELECT min("ts"), max("received_at") FROM events WHERE ` + inRange}},
		// Most views say nothing of when they arrived: the first time is
		// of those that do.
		{name: "first arrival over partly-null rows", q: Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "min", Column: "received_at"}}},
			cols: []string{"min_received_at"},
			sql: []string{`SELECT min("received_at") FROM views WHERE ` + inRange + `
				AND (SELECT count(*) FILTER (WHERE "received_at" IS NULL) > 0 AND count("received_at") > 0 FROM views WHERE ` + inRange + `)`}},
		{name: "other", q: Query{Kind: "breakdown", Table: "events", Dimensions: []string{"country"}, Limit: 2, Other: true,
			Metrics: []Metric{{Op: "count"}, {Op: "max", Column: "received_at", Where: product}, {Op: "min", Column: "day"}}},
			cols: []string{"country", "count", "max_received_at", "min_day"},
			sql: []string{byCountry + `SELECT "country", c, r, d FROM top ORDER BY c DESC, "country" ASC NULLS LAST`,
				byCountry + `SELECT 'Other', ` + byCountryMetrics + ` FROM events e WHERE ` + inRange + `
					AND NOT EXISTS (SELECT 1 FROM top WHERE top."country" IS NOT DISTINCT FROM e."country") HAVING count(*) > 0`}},
		{name: "ranked by the last day", q: Query{Kind: "breakdown", Table: "views", Dimensions: []string{"path"}, Limit: 4, Order: "max_day",
			Where:   []Filter{{Column: "day", Op: "lte", Values: []string{"2026-09-01"}}},
			Metrics: []Metric{{Op: "max", Column: "day"}}},
			cols: []string{"path", "max_day"},
			sql: []string{`SELECT "path", max("day") AS m FROM views WHERE ` + inRange + ` AND "day" <= DATE '2026-09-01'
				GROUP BY "path" ORDER BY m DESC NULLS LAST, "path" ASC NULLS LAST LIMIT 4`}},
		{name: "trend", q: Query{Kind: "trend", Table: "product", Metrics: []Metric{{Op: "max", Column: "received_at"}}},
			cols: []string{"day", "max_received_at"},
			sql:  []string{`SELECT "day", max("received_at") FROM product WHERE ` + inRange + ` GROUP BY "day" ORDER BY "day"`}},
		{name: "sessions", q: Query{Kind: "breakdown", Table: "sessions", Dimensions: []string{"entry_path"}, Limit: 5,
			Metrics: []Metric{{Op: "count"}, {Op: "min", Column: "started_at"}, {Op: "max", Column: "ended_at"}, {Op: "max", Column: "day"}}},
			cols: []string{"entry_path", "count", "min_started_at", "max_ended_at", "max_day"},
			sql: []string{`SELECT "entry_path", count(*) AS c, min("started_at"), max("ended_at"), max("day") FROM sessions WHERE ` + inRange + `
				GROUP BY "entry_path" ORDER BY c DESC, "entry_path" ASC NULLS LAST LIMIT 5`}},
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

// A first or last day or time is empty over no rows: over a range
// without any, and in a group whose rows its where leaves out.
func TestBoundsEmpty(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 2})
	never := []Filter{{Column: "path", Op: "eq", Values: []string{"/nowhere"}}}
	metrics := []Metric{{Op: "count"}, {Op: "max", Column: "day", Where: never}, {Op: "min", Column: "received_at", Where: never}}
	q := Query{Kind: "stat", Table: "events", Metrics: metrics}
	res, err := e.Answer(context.Background(), q, Params{Project: 1, From: date(t, engineFrom), To: date(t, today)}, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Rows[0]; got[0] == "0" || got[1] != "" || got[2] != "" {
		t.Errorf("rows %q, want a count and two empty", res.Rows)
	}
	q.Metrics = []Metric{{Op: "max", Column: "day"}, {Op: "max", Column: "received_at"}}
	res, err = e.Answer(context.Background(), q, Params{Project: 1, From: date(t, "2026-07-01"), To: date(t, "2026-07-02")}, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"", ""}; !slices.Equal(res.Rows[0], want) {
		t.Errorf("no rows: %q, want %q", res.Rows, want)
	}
	s := Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "min", Column: "started_at"}, {Op: "max", Column: "day"}}}
	res, err = e.Answer(context.Background(), s, Params{Project: 1, From: date(t, "2026-07-01"), To: date(t, "2026-07-02")}, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"", ""}; !slices.Equal(res.Rows[0], want) {
		t.Errorf("no sessions: %q, want %q", res.Rows, want)
	}
}
