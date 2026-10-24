package native

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// user_label, group_label, user_first_seen and group_first_seen are the
// identity's combined name (else the id) and first-seen day, as
// users/top-users.sql and groups/top-groups.sql join identities; the
// widgets themselves are a breakdown by the id, its label and first day,
// with the days metric as their active days. Each is checked over a
// current identities file with logs beside it, a stale file and none.
func TestIdentityLabelsMatchDuckDB(t *testing.T) {
	f := identitiesFixture(t)
	// A user and a group never named: their label is their id.
	f.Write(t, store.Event{ID: "i-nameless", ProjectID: identitiesProject, Family: store.FamilyViews, EventName: "pageview",
		TS: date(t, today).Time().Add(21 * time.Hour), Kind: "web", ActorID: "a8", UserID: "nobody", GroupID: "nogroup"})
	pdir := filepath.Join(f.Dir, strconv.Itoa(identitiesProject))
	const from, to = "2026-09-01", today
	actions := Filter{Column: "family", Op: "in", Values: []string{"views", "product"}}
	product := []Filter{{Column: "family", Op: "eq", Values: []string{"product"}}}
	views := []Filter{{Column: "family", Op: "eq", Values: []string{"views"}}}
	topUsers := Query{Kind: "breakdown", Table: "events", Dimensions: []string{"user_id", "user_label", "user_first_seen"}, Limit: 100,
		Where: []Filter{actions, {Column: "user_id", Op: "is_set"}},
		Metrics: []Metric{{Op: "count", As: "actions"}, {Op: "count", As: "events", Where: product}, {Op: "count", As: "views", Where: views},
			{Op: "days", As: "active_days"}, {Op: "max", Column: "day", As: "last_seen"}}}
	// The widget's columns: User, Actions, Events, Views, Active days,
	// First seen, Last seen.
	usersOut := func(r []string) []string { return []string{r[1], r[3], r[4], r[5], r[6], r[2], r[7]} }
	newUsers := topUsers
	newUsers.Where = append(slices.Clone(topUsers.Where), Filter{Column: "user_first_seen", Op: "gte", Values: []string{"2026-09-02"}})
	topGroups := Query{Kind: "breakdown", Table: "events", Dimensions: []string{"group_id", "group_label", "group_first_seen"}, Limit: 100,
		Where: []Filter{actions, {Column: "group_id", Op: "is_set"}},
		Metrics: []Metric{{Op: "count", As: "actions"}, {Op: "daily_max", Of: "users", As: "peak"},
			{Op: "days", As: "active_days"}, {Op: "max", Column: "day", As: "last_seen"}}}
	groupsOut := func(r []string) []string { return []string{r[1], r[3], r[4], r[5], r[2], r[6]} }
	type tc struct {
		name string
		q    Query
		out  func([]string) []string
		sql  string
		// check says what the case must show beside the SQL's rows.
		check func(t *testing.T, e *Engine, rows [][]string)
	}
	labelled := func(t *testing.T, _ *Engine, rows [][]string) {
		t.Helper()
		named, nameless := false, false
		for _, r := range rows {
			named = named || r[1] != r[0]
			nameless = nameless || ((r[0] == "nobody" || r[0] == "nogroup") && r[1] == r[0])
		}
		if !named || !nameless {
			t.Errorf("rows %q: want a named identity and a nameless one read by its id", rows)
		}
	}
	cases := []tc{
		{name: "top users", q: topUsers, out: usersOut, sql: widgetSQLOf(t, "users/top-users.sql", identitiesProject), check: labelled},
		{name: "top groups", q: topGroups, out: groupsOut, sql: widgetSQLOf(t, "groups/top-groups.sql", identitiesProject), check: labelled},
		{name: "users first seen lately", q: newUsers, out: usersOut,
			sql: `SELECT * FROM (` + widgetSQLOf(t, "users/top-users.sql", identitiesProject) + `) WHERE "First seen" >= DATE '2026-09-02'`,
			check: func(t *testing.T, e *Engine, rows [][]string) {
				var users [][]string
				for _, r := range answerRows(t, e, identitiesProject, topUsers, from, to, archive.Rules{}) {
					users = append(users, usersOut(r))
				}
				all := goldenTable(t, widgetSQLOf(t, "users/top-users.sql", identitiesProject), from, to, users)
				sameTable(t, users, all)
				if len(rows) == 0 || len(rows) == len(all) {
					t.Errorf("%d of %d users first seen lately: a weak case", len(rows), len(all))
				}
			}},
		{name: "views by label", q: Query{Kind: "breakdown", Table: "views", Dimensions: []string{"user_label"}, Limit: 100, Metrics: count()},
			sql: `SELECT COALESCE(i.name, NULLIF(e.user_id, '')) AS l, count(*) AS c FROM events e
				LEFT JOIN identities i ON i.project_id = 4 AND i.kind = 'user' AND i.id = e.user_id
				WHERE e.project_id = 4 AND e.family = 'views' AND e.day BETWEEN DATE '%[1]s' AND DATE '%[2]s'
				GROUP BY l ORDER BY c DESC, l ASC NULLS LAST LIMIT 100`,
			check: func(t *testing.T, e *Engine, rows [][]string) {
				if !slices.ContainsFunc(rows, func(r []string) bool { return r[0] == "" }) {
					t.Errorf("rows %q: no row without a user", rows)
				}
			}},
		{name: "views by older groups", q: Query{Kind: "trend", Table: "views", Series: "group_label", SeriesLimit: 50, Metrics: count(),
			Where: []Filter{{Column: "group_first_seen", Op: "lt", Values: []string{"2026-08-28"}}}},
			sql: `SELECT e.day, COALESCE(i.name, e.group_id) AS l, count(*) FROM events e
				JOIN identities i ON i.project_id = 4 AND i.kind = 'group' AND i.id = e.group_id
				WHERE e.project_id = 4 AND e.family = 'views' AND e.day BETWEEN DATE '%[1]s' AND DATE '%[2]s'
				AND i.first_seen::DATE < DATE '2026-08-28' GROUP BY ALL ORDER BY 1, 2`,
			check: func(t *testing.T, e *Engine, rows [][]string) {
				series := map[string]bool{}
				for _, r := range rows {
					series[r[1]] = true
				}
				if len(series) < 2 {
					t.Errorf("series %v: want two groups first seen before the 28th", series)
				}
			}},
	}
	run := func(t *testing.T, e *Engine) {
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				res, err := e.Answer(context.Background(), c.q, Params{Project: identitiesProject, From: date(t, from), To: date(t, to)}, archive.Rules{})
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(res.Columns, c.q.Columns()) {
					t.Fatalf("columns %q, want %q", res.Columns, c.q.Columns())
				}
				got := res.Rows
				if c.out != nil {
					got = make([][]string, len(res.Rows))
					for i, r := range res.Rows {
						got[i] = c.out(r)
					}
				}
				want := goldenTable(t, c.sql, from, to, got)
				if len(want) == 0 {
					t.Fatal("the SQL returns no rows: a weak case")
				}
				sameTable(t, got, want)
				if c.check != nil {
					c.check(t, e, res.Rows)
				}
				if res.Truncated || len(res.Approximate) > 0 {
					t.Errorf("truncated %v, approximate %q", res.Truncated, res.Approximate)
				}
			})
		}
	}
	t.Run("current file and logs", func(t *testing.T) {
		e := New(Options{Dir: f.Dir, Workers: 3})
		run(t, e)
		// u1 is renamed by the logs, whose last_seen is the latest: every
		// row of it reads that name.
		res, err := e.Answer(context.Background(), Query{Kind: "breakdown", Table: "events", Dimensions: []string{"user_id", "user_label"},
			Metrics: count(), Where: []Filter{{Column: "user_id", Op: "eq", Values: []string{"u1"}}}},
			Params{Project: identitiesProject, From: date(t, identitiesFrom), To: date(t, today)}, archive.Rules{})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != 1 || res.Rows[0][1] != "Renamed" {
			t.Errorf("u1's rows %q, want one group named Renamed", res.Rows)
		}
		run(t, e) // through the cache
	})
	t.Run("stale file", func(t *testing.T) {
		later := time.Now().Add(time.Hour)
		if err := os.Chtimes(filepath.Join(pdir, "2026-09-05.parquet"), later, later); err != nil {
			t.Fatal(err)
		}
		if p := listFiles(t, New(Options{Dir: f.Dir}))[identitiesProject].Identities; p != "" {
			t.Fatalf("identities file %q is current", p)
		}
		run(t, New(Options{Dir: f.Dir, Workers: 3}))
	})
	t.Run("missing file", func(t *testing.T) {
		if err := os.Remove(filepath.Join(pdir, "identities.parquet")); err != nil {
			t.Fatal(err)
		}
		run(t, New(Options{Dir: f.Dir, Workers: 2}))
	})
}

// The names a label looks up are charged against MaxBytes, and cached
// apart from the first-seen days a status alone needs.
func TestIdentityLabelsCharged(t *testing.T) {
	f := identitiesFixture(t)
	p := Params{Project: identitiesProject, From: date(t, identitiesFrom), To: date(t, today)}
	q := Query{Kind: "breakdown", Table: "views", Metrics: count(), Dimensions: []string{"group_label"}}
	if _, err := New(Options{Dir: f.Dir, MaxBytes: 64}).Answer(context.Background(), q, p, archive.Rules{}); err == nil {
		t.Error("labels within 64 bytes")
	}
	var loads []idPart
	onIdentityLoad = func(part idPart) { loads = append(loads, part) }
	defer func() { onIdentityLoad = func(idPart) {} }()
	e := New(Options{Dir: f.Dir, Workers: 1})
	for _, q := range []Query{
		{Kind: "stat", Table: "views", Metrics: count(), Where: []Filter{{Column: "user_status", Op: "is_set"}}},
		q, q,
		{Kind: "stat", Table: "views", Metrics: count(), Where: []Filter{{Column: "user_first_seen", Op: "is_set"}}},
	} {
		if _, err := e.Answer(context.Background(), q, p, archive.Rules{}); err != nil {
			t.Fatal(err)
		}
	}
	// The status loads the first-seen days, the labels the names (once),
	// and the first-seen days are those cached already.
	if len(loads) != 4 || loads[0].names || !loads[2].names || !loads[3].names {
		t.Errorf("loads %+v, want the two parts of first-seen days, then of names", loads)
	}
}

// days is the number of distinct days with rows, as count(DISTINCT day)
// counts them: with a metric where, of the rows that pass it; in a
// trend 1 or 0 per day; in an Other row over its folded groups.
func TestDaysMatchesDuckDB(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	landing := []Filter{{Column: "event_name", Op: "eq", Values: []string{"landing"}}}
	p1 := []Filter{{Column: "path", Op: "eq", Values: []string{"/p1"}}}
	const byCountryMetrics = `count(*) AS c, count(DISTINCT "day") AS d, count(DISTINCT "day") FILTER (WHERE "path" = '/p1') AS p`
	byCountry := `WITH g AS (SELECT "country", ` + byCountryMetrics + ` FROM views WHERE ` + inRange + ` AND "day" >= DATE '2026-09-04' GROUP BY "country"),
		top AS (SELECT * FROM g ORDER BY c DESC, "country" ASC NULLS LAST LIMIT 2) `
	cases := []struct {
		name string
		q    Query
		sql  []string
		prev bool
		from string
	}{
		{name: "stat", q: Query{Kind: "stat", Table: "events", Compare: "previous", Metrics: []Metric{{Op: "days"},
			{Op: "days", As: "landing", Where: landing}}}, prev: true, from: "2026-08-26",
			sql: []string{`SELECT count(DISTINCT "day"), count(DISTINCT "day") FILTER (WHERE "event_name" = 'landing') FROM events WHERE ` + inRange}},
		{name: "breakdown with other", q: Query{Kind: "breakdown", Table: "views", Dimensions: []string{"country"}, Limit: 2, Other: true,
			Where:   []Filter{{Column: "day", Op: "gte", Values: []string{"2026-09-04"}}},
			Metrics: []Metric{{Op: "count"}, {Op: "days"}, {Op: "days", As: "p1", Where: p1}}},
			sql: []string{byCountry + `SELECT "country", c, d, p FROM top ORDER BY c DESC, "country" ASC NULLS LAST`,
				byCountry + `SELECT 'Other', ` + byCountryMetrics + ` FROM views e WHERE ` + inRange + ` AND "day" >= DATE '2026-09-04'
					AND NOT EXISTS (SELECT 1 FROM top WHERE top."country" IS NOT DISTINCT FROM e."country") HAVING count(*) > 0`}},
		{name: "ranked by days", q: Query{Kind: "breakdown", Table: "product", Dimensions: []string{"user_id"}, Limit: 4,
			Metrics: []Metric{{Op: "days"}}},
			sql: []string{`SELECT "user_id", count(DISTINCT "day") AS d FROM product WHERE ` + inRange + `
				GROUP BY "user_id" ORDER BY d DESC, "user_id" ASC NULLS LAST LIMIT 4`}},
		{name: "trend", q: Query{Kind: "trend", Table: "views", Metrics: []Metric{{Op: "days", Where: landing}}},
			sql: []string{`SELECT "day", count(DISTINCT "day") FILTER (WHERE "event_name" = 'landing') FROM views WHERE ` + inRange + `
				GROUP BY "day" ORDER BY "day"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from := engineFrom
			if c.from != "" {
				from = c.from
			}
			res, err := e.Answer(context.Background(), c.q, Params{Project: 1, From: date(t, from), To: date(t, today)}, engineRules)
			if err != nil {
				t.Fatal(err)
			}
			want := goldenTables(t, c.sql, from, today, c.prev, res.Rows)
			if len(want) == 0 {
				t.Fatal("the SQL returns no rows: a weak case")
			}
			sameTable(t, res.Rows, want)
			if res.Truncated || len(res.Approximate) > 0 {
				t.Errorf("truncated %v, approximate %q", res.Truncated, res.Approximate)
			}
		})
	}
	// Over no rows days is 0, as a count.
	res, err := e.Answer(context.Background(), Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "days"}}},
		Params{Project: 1, From: date(t, "2026-07-01"), To: date(t, "2026-07-02")}, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"0"}}; !slices.EqualFunc(res.Rows, want, slices.Equal) {
		t.Errorf("no rows: %q, want %q", res.Rows, want)
	}
}
