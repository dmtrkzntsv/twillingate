package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

// The engine fixture's days: 40 of them, from August 2 to today, so
// August becomes a month file, September's days day files and today a log.
const engineFrom = "2026-08-02"

// engineRules are project 1's rules (and one of project 2's): a views
// event renamed, a product event renamed and one blocked, and an attribute
// key renamed onto another.
var engineRules = archive.Rules{
	Events: []store.EventRule{
		evRule(1, store.FamilyViews, "ev_old", store.EventRuleRename, "landing"),
		evRule(1, store.FamilyProduct, "ev1", store.EventRuleRename, "signup"),
		evRule(1, store.FamilyProduct, "ev3", store.EventRuleBlock, ""),
		evRule(2, store.FamilyViews, "pageview", store.EventRuleBlock, ""),
	},
	Attributes: []store.AttributeRule{attrRule(1, "colour", store.EventRuleRename, "color")},
}

// engineCountry is actor i's country: 10 in DE, 6 in FR, 6 in US (a tie
// with FR), 3 in GB and 5 without one.
func engineCountry(i int) string {
	switch {
	case i < 10:
		return "DE"
	case i < 16:
		return "FR"
	case i < 22:
		return "US"
	case i < 25:
		return "GB"
	}
	return ""
}

// engineDay is one day's events of project 1: views by most of 30 actors
// (each always from one country) and two without an actor, product events
// with attributes, measures with values.
func engineDay(t testing.TB, day string, d int) []store.Event {
	t.Helper()
	base := date(t, day).Time().Add(8 * time.Hour)
	var out []store.Event
	n := 0
	add := func(e store.Event) {
		e.ProjectID = 1
		e.TS = base.Add(time.Duration(n) * time.Second)
		e.ID = fmt.Sprintf("e-%s-%d", day, n)
		e.Host = "example.com"
		n++
		out = append(out, e)
	}
	for i := range 30 {
		if (i+d)%3 == 0 || (i*d)%13 == 5 {
			continue
		}
		e := store.Event{Family: store.FamilyViews, EventName: "pageview", ActorID: fmt.Sprintf("a%02d", i),
			ActorKind: "install", Country: engineCountry(i), DisplayWidth: []int{0, 390, 1280}[(i+d)%3]}
		if i < 20 {
			e.Path = fmt.Sprintf("/p%d", (i+d)%7)
		} else if i%2 == 0 {
			e.Path = fmt.Sprintf("/q%d", i%3)
		}
		if i%4 == 0 {
			e.UserID = fmt.Sprintf("u%d", i)
		}
		if i%5 == 0 {
			e.GroupID = fmt.Sprintf("g%d", i%10)
		}
		if (i+d)%9 == 1 {
			e.EventName = "ev_old"
		}
		e.Consent = []store.Consent{store.ConsentUnknown, store.ConsentGiven, store.ConsentNone}[i%3]
		add(e)
	}
	for k := range 2 {
		add(store.Event{Family: store.FamilyViews, EventName: "pageview", Country: "DE", Path: fmt.Sprintf("/p%d", k)})
	}
	for k := range 15 {
		i := (k*7 + d) % 30
		e := store.Event{Family: store.FamilyProduct, EventName: fmt.Sprintf("ev%d", k%4), ActorID: fmt.Sprintf("a%02d", i),
			Country: engineCountry(i), DisplayWidth: []int{390, 1280}[k%2], Path: fmt.Sprintf("/p%d", k%3)}
		if k%3 == 0 {
			e.UserID = fmt.Sprintf("u%d", i%6)
		}
		if k%4 == 1 {
			e.GroupID = fmt.Sprintf("g%d", i%3)
		}
		e.Attributes = map[string]string{}
		if k%5 != 4 {
			e.Attributes["plan"] = []string{"free", "pro", "team"}[(k+d)%3]
		}
		switch k % 6 {
		case 1:
			e.Attributes["color"] = "red"
		case 2:
			e.Attributes["colour"] = "blue"
		case 3:
			e.Attributes["color"], e.Attributes["colour"] = "green", "grey"
		}
		add(e)
	}
	for k := range 10 {
		v := float64(k)*0.37 + float64(d)*0.01 + 0.5
		if k == 9 {
			v = 0
		}
		add(store.Event{Family: store.FamilyMeasures, EventName: []string{"lcp", "cls", "inp"}[k%3], Measure: "web",
			ActorID: fmt.Sprintf("a%02d", (k+d)%30), Value: &v, SampleRate: []float64{0, 0.5}[k%2],
			DisplayWidth: []int{390, 1280}[k%2], Attributes: map[string]string{"plan": "pro"}})
	}
	return out
}

// engineFixture writes 40 days of project 1 (and a little of project 2),
// runs the daily pass as of today, then writes late events to two closed
// days and today's events, which stay in logs.
func engineFixture(t testing.TB) *archivetest.Files {
	t.Helper()
	f := archivetest.New(t)
	start := date(t, engineFrom).Time()
	for d := range 40 {
		day := start.AddDate(0, 0, d).Format(time.DateOnly)
		if day == today {
			continue
		}
		f.Write(t, engineDay(t, day, d)...)
		if d%10 == 0 {
			f.Write(t, events(t, 2, day, 2)...)
		}
	}
	f.Maintain(t, today)
	f.Write(t, engineDay(t, today, 39)...)
	late := engineDay(t, "2026-08-20", 18)[:6]
	for i := range late {
		late[i].ID += "-late"
	}
	f.Write(t, late...)
	f.Write(t, events(t, 2, today, 3)...)
	return f
}

// duckTable runs sql (its ranges spelled as %[1]s and %[2]s, from and to)
// and returns its rows as the engine formats them: NULL as "".
func duckTable(t *testing.T, f *archivetest.Files, sql, from, to string) [][]string {
	t.Helper()
	q := fmt.Sprintf(sql, from, to)
	res, err := f.Reader.Query(context.Background(), q, 0, engineRules)
	if err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	out := make([][]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		r := make([]string, len(row))
		for j, v := range row {
			switch x := v.(type) {
			case nil:
				r[j] = ""
			case json.Number:
				r[j] = string(x)
			case string:
				r[j] = x
			default:
				r[j] = fmt.Sprint(x)
			}
		}
		out = append(out, r)
	}
	return out
}

// sameCell says whether two cells are equal: as strings, or as numbers
// within a rounding error (sums of doubles depend on their order).
func sameCell(a, b string) bool {
	if a == b {
		return true
	}
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	return err1 == nil && err2 == nil && math.Abs(x-y) <= 1e-9*math.Max(1, math.Abs(y))
}

func sameTable(t *testing.T, got, want [][]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d rows, want %d:\n got %q\nwant %q", len(got), len(want), got, want)
	}
	for i := range want {
		ok := len(got[i]) == len(want[i])
		for j := 0; ok && j < len(want[i]); j++ {
			ok = sameCell(got[i][j], want[i][j])
		}
		if !ok {
			t.Fatalf("row %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

// The where clause of project 1 over a range, for duckTable's SQL.
const inRange = `"project_id" = 1 AND "day" BETWEEN DATE '%[1]s' AND DATE '%[2]s'`

func TestKindsMatchDuckDB(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	type tc struct {
		name     string
		q        Query
		from, to string
		cols     []string
		// sql are run in turn, their rows concatenated; with prev, the
		// first one's columns are followed by its columns over the
		// previous range.
		sql  []string
		prev bool
	}
	all := func(name string, q Query, cols []string, sql ...string) tc {
		return tc{name: name, q: q, from: engineFrom, to: today, cols: cols, sql: sql}
	}
	viewsStat := Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count"}, {Op: "actors"}, {Op: "users"},
		{Op: "groups"}, {Op: "sum", Column: "value"}, {Op: "avg", Column: "value"}, {Op: "daily_avg", Of: "actors"}}}
	viewsStatSQL := `SELECT count(*), count(DISTINCT "actor_id"), count(DISTINCT "user_id"), count(DISTINCT "group_id"),
		sum("value"), avg("value"),
		(SELECT avg(n) FROM (SELECT count(DISTINCT "actor_id") AS n FROM views WHERE ` + inRange + ` GROUP BY "day"))
		FROM views WHERE ` + inRange
	viewsStatCols := []string{"count", "actors", "users", "groups", "sum_value", "avg_value", "daily_avg_actors"}
	compare := viewsStat
	compare.Compare = "previous"
	pathTop := `WITH g AS (SELECT "path", count(DISTINCT "actor_id") AS a, count(*) AS c FROM views WHERE ` + inRange + ` GROUP BY "path"),
		top AS (SELECT * FROM g ORDER BY a DESC, "path" ASC NULLS LAST LIMIT 3) `
	dailyTop := `WITH d AS (SELECT "path", "day", count(DISTINCT "actor_id") AS n FROM views WHERE ` + inRange + ` GROUP BY "path", "day"),
		g AS (SELECT "path", sum(n) AS s FROM d GROUP BY "path"),
		top AS (SELECT * FROM g ORDER BY s DESC, "path" ASC NULLS LAST LIMIT 2) `
	cases := []tc{
		all("stat views", viewsStat, viewsStatCols, viewsStatSQL),
		{name: "stat compare", q: compare, from: "2026-08-26", to: today, prev: true, sql: []string{viewsStatSQL},
			cols: append(slices.Clone(viewsStatCols), "previous_count", "previous_actors", "previous_users", "previous_groups",
				"previous_sum_value", "previous_avg_value", "previous_daily_avg_actors")},
		{name: "stat empty", q: viewsStat, from: "2026-07-01", to: "2026-07-05", cols: viewsStatCols, sql: []string{viewsStatSQL}},
		all("stat measures", Query{Kind: "stat", Table: "measures", Metrics: []Metric{{Op: "min", Column: "value"},
			{Op: "max", Column: "value"}, {Op: "sum", Column: "display_width"}, {Op: "avg", Column: "display_width"},
			{Op: "max", Column: "sample_rate"}, {Op: "daily_max", Of: "actors"}, {Op: "daily_sum", Of: "actors"}}},
			[]string{"min_value", "max_value", "sum_display_width", "avg_display_width", "max_sample_rate", "daily_max_actors", "daily_sum_actors"},
			`SELECT min("value"), max("value"), sum("display_width"), avg("display_width"), max("sample_rate"),
				(SELECT max(n) FROM (SELECT count(DISTINCT "actor_id") AS n FROM measures WHERE `+inRange+` GROUP BY "day")),
				(SELECT sum(n) FROM (SELECT count(DISTINCT "actor_id") AS n FROM measures WHERE `+inRange+` GROUP BY "day"))
				FROM measures WHERE `+inRange),
		all("trend count", Query{Kind: "trend", Table: "views", Metrics: count()}, []string{"day", "count"},
			`SELECT "day", count(*) FROM views WHERE `+inRange+` GROUP BY "day" ORDER BY "day"`),
		all("trend actors by country", Query{Kind: "trend", Table: "views", Metrics: []Metric{{Op: "actors"}},
			Series: "country", SeriesLimit: 2}, []string{"day", "series", "actors"},
			`WITH top AS (SELECT "country" FROM views WHERE `+inRange+` GROUP BY "country"
				ORDER BY count(DISTINCT "actor_id") DESC, "country" ASC NULLS LAST LIMIT 2)
			SELECT "day", v."country", count(DISTINCT "actor_id") FROM views v JOIN top ON v."country" IS NOT DISTINCT FROM top."country"
			WHERE `+inRange+` GROUP BY "day", v."country" ORDER BY "day", v."country" ASC NULLS LAST`),
		all("trend width series", Query{Kind: "trend", Table: "product", Metrics: []Metric{{Op: "users"}}, Series: "display_width"},
			[]string{"day", "series", "users"},
			`SELECT "day", "display_width", count(DISTINCT "user_id") FROM product WHERE `+inRange+`
				GROUP BY "day", "display_width" ORDER BY "day", "display_width" ASC NULLS LAST`),
		all("breakdown path other", Query{Kind: "breakdown", Table: "views", Metrics: []Metric{{Op: "actors"}, {Op: "count"}},
			Dimensions: []string{"path"}, Limit: 3, Other: true}, []string{"path", "actors", "count"},
			pathTop+`SELECT "path", a, c FROM top ORDER BY a DESC, "path" ASC NULLS LAST`,
			pathTop+`SELECT 'Other', count(DISTINCT "actor_id"), count(*) FROM views v WHERE `+inRange+`
				AND NOT EXISTS (SELECT 1 FROM top WHERE top."path" IS NOT DISTINCT FROM v."path") HAVING count(*) > 0`),
		all("breakdown daily other", Query{Kind: "breakdown", Table: "views", Metrics: []Metric{{Op: "daily_sum", Of: "actors"}},
			Dimensions: []string{"path"}, Limit: 2, Other: true}, []string{"path", "daily_sum_actors"},
			dailyTop+`SELECT "path", s FROM top ORDER BY s DESC, "path" ASC NULLS LAST`,
			dailyTop+`SELECT 'Other', sum(n) FROM (SELECT "day", count(DISTINCT "actor_id") AS n FROM views v WHERE `+inRange+`
				AND NOT EXISTS (SELECT 1 FROM top WHERE top."path" IS NOT DISTINCT FROM v."path") GROUP BY "day") HAVING count(*) > 0`),
		all("breakdown event by plan", Query{Kind: "breakdown", Table: "product", Metrics: []Metric{{Op: "count"}, {Op: "users"}},
			Dimensions: []string{"event_name", "attr_plan"}}, []string{"event_name", "attr_plan", "count", "users"},
			`SELECT "event_name", "attr_plan", count(*) AS c, count(DISTINCT "user_id") FROM product WHERE `+inRange+`
				GROUP BY ALL ORDER BY c DESC, "event_name" ASC NULLS LAST, "attr_plan" ASC NULLS LAST LIMIT 20`),
		all("breakdown renamed color", Query{Kind: "breakdown", Table: "product", Metrics: []Metric{{Op: "count"}, {Op: "groups"}},
			Dimensions: []string{"attr_color", "country", "display_width"}, Limit: 50}, []string{"attr_color", "country", "display_width", "count", "groups"},
			`SELECT "attr_color", "country", "display_width", count(*) AS c, count(DISTINCT "group_id") FROM product WHERE `+inRange+`
				GROUP BY ALL ORDER BY c DESC, "attr_color" ASC NULLS LAST, "country" ASC NULLS LAST, "display_width" ASC NULLS LAST LIMIT 50`),
		all("breakdown in and prefix", Query{Kind: "breakdown", Table: "views", Metrics: count(), Dimensions: []string{"path"},
			Where: []Filter{{Column: "country", Op: "in", Values: []string{"DE", "FR"}}, {Column: "path", Op: "prefix", Values: []string{"/p"}}}},
			[]string{"path", "count"},
			`SELECT "path", count(*) AS c FROM views WHERE `+inRange+` AND "country" IN ('DE', 'FR') AND starts_with("path", '/p')
				GROUP BY "path" ORDER BY c DESC, "path" ASC NULLS LAST LIMIT 20`),
		all("breakdown other filters", Query{Kind: "breakdown", Table: "product", Metrics: []Metric{{Op: "count"}, {Op: "actors"}},
			Dimensions: []string{"day"}, Limit: 100, Where: []Filter{
				{Column: "attr_plan", Op: "ne", Values: []string{"free"}}, {Column: "attr_color", Op: "is_set"},
				{Column: "event_name", Op: "not_in", Values: []string{"ev0"}}, {Column: "path", Op: "contains", Values: []string{"1"}},
				{Column: "display_width", Op: "gte", Values: []string{"391"}}, {Column: "day", Op: "lte", Values: []string{"2026-09-08"}},
				{Column: "country", Op: "eq", Values: []string{"DE"}}}},
			[]string{"day", "count", "actors"},
			`SELECT "day", count(*) AS c, count(DISTINCT "actor_id") FROM product WHERE `+inRange+` AND "attr_plan" <> 'free'
				AND "attr_color" IS NOT NULL AND "attr_color" <> '' AND "event_name" NOT IN ('ev0') AND contains("path", '1')
				AND "display_width" >= 391 AND "day" <= DATE '2026-09-08' AND "country" = 'DE'
				GROUP BY "day" ORDER BY c DESC, "day" ASC LIMIT 100`),
		{name: "breakdown mid-month", q: Query{Kind: "breakdown", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "actors"}},
			Dimensions: []string{"family", "consent"}}, from: "2026-08-15", to: "2026-09-04", cols: []string{"family", "consent", "count", "actors"},
			sql: []string{`SELECT "family", "consent", count(*) AS c, count(DISTINCT "actor_id") FROM events WHERE ` + inRange + `
				GROUP BY ALL ORDER BY c DESC, "family" ASC NULLS LAST, "consent" ASC NULLS LAST LIMIT 20`}},
		all("measures by event", Query{Kind: "breakdown", Table: "measures", Metrics: []Metric{{Op: "avg", Column: "value"}, {Op: "count"}},
			Dimensions: []string{"event_name"}}, []string{"event_name", "avg_value", "count"},
			`SELECT "event_name", avg("value") AS a, count(*) FROM measures WHERE `+inRange+`
				GROUP BY "event_name" ORDER BY a DESC NULLS LAST, "event_name" ASC NULLS LAST LIMIT 20`),
		all("measures by bucket", Query{Kind: "breakdown", Table: "measures", Metrics: count(), Dimensions: []string{"bucket"}, Limit: 5,
			Where: []Filter{{Column: "bucket", Op: "lt", Values: []string{"30"}}}}, []string{"bucket", "count"},
			`SELECT "bucket", count(*) AS c FROM measures WHERE `+inRange+` AND "bucket" < 30
				GROUP BY "bucket" ORDER BY c DESC, "bucket" ASC LIMIT 5`),
		all("renamed event", Query{Kind: "breakdown", Table: "views", Metrics: count(), Dimensions: []string{"event_name"}},
			[]string{"event_name", "count"},
			`SELECT "event_name", count(*) AS c FROM views WHERE `+inRange+` GROUP BY "event_name" ORDER BY c DESC, "event_name" ASC NULLS LAST LIMIT 20`),
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
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
			if len(want) == 0 {
				t.Fatal("the SQL returns no rows: a weak case")
			}
			sameTable(t, res.Rows, want)
			if res.Truncated || len(res.Approximate) > 0 {
				t.Errorf("truncated %v, approximate %q", res.Truncated, res.Approximate)
			}
		})
	}
}

func TestResultIndependentOfWorkers(t *testing.T) {
	f := engineFixture(t)
	q := Query{Kind: "breakdown", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "actors"}, {Op: "daily_max", Of: "users"}},
		Dimensions: []string{"path", "family"}, Limit: 1000, Other: true}
	p := Params{Project: 1, From: date(t, engineFrom), To: date(t, today)}
	var first Result
	for _, w := range []int{1, 2, 5} {
		res, err := New(Options{Dir: f.Dir, Workers: w}).Answer(context.Background(), q, p, engineRules)
		if err != nil {
			t.Fatal(err)
		}
		if w == 1 {
			first = res
			continue
		}
		sameTable(t, res.Rows, first.Rows)
	}
}

func TestRunRefusesBadValues(t *testing.T) {
	e := New(Options{Dir: t.TempDir()})
	p := Params{Project: 1, From: date(t, "2026-09-01"), To: date(t, today)}
	for _, c := range []struct {
		f    Filter
		path string
	}{
		{Filter{Column: "day", Op: "eq", Values: []string{"x"}}, "where[0].values"},
		{Filter{Column: "display_width", Op: "in", Values: []string{"1", "wide"}}, "where[0].values"},
		{Filter{Column: "consent", Op: "contains", Values: []string{"1"}}, "where[0].op"},
		{Filter{Column: "bucket", Op: "ne", Values: []string{"1.5"}}, "where[0].values"},
	} {
		q := Query{Kind: "stat", Table: "measures", Metrics: count(), Where: []Filter{c.f}}
		_, err := e.Answer(context.Background(), q, p, archive.Rules{})
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.path) {
			t.Errorf("%+v: %v, want ErrInvalid naming %s", c.f, err, c.path)
		}
	}
	q := Query{Kind: "stat", Table: "views", Metrics: count()}
	if _, err := e.Answer(context.Background(), q, Params{Project: 1, From: date(t, today), To: date(t, "2026-09-01")}, archive.Rules{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("to before from: %v", err)
	}
	// No files: one row of zeros.
	res, err := e.Answer(context.Background(), q, p, archive.Rules{})
	if err != nil || !slices.Equal(res.Rows[0], []string{"0"}) {
		t.Errorf("no files: %v %q", err, res.Rows)
	}
}

// visits is one view per actor on day, n actors from first.
func visits(project int64, day time.Time, first, n int) []store.Event {
	out := make([]store.Event, n)
	for i := range out {
		out[i] = store.Event{ID: fmt.Sprintf("v%d-%s-%d", project, day.Format(time.DateOnly), first+i), ProjectID: project,
			Family: store.FamilyViews, EventName: "pageview", TS: day.Add(time.Duration(i) * time.Millisecond),
			ActorID: fmt.Sprintf("actor-%d", first+i), Path: fmt.Sprintf("/x%d", (first+i)%2)}
	}
	return out
}

func TestDistinctTurnsApproximate(t *testing.T) {
	f := archivetest.New(t)
	d1, d2 := date(t, "2026-09-08").Time(), date(t, "2026-09-09").Time()
	f.Write(t, visits(1, d1, 0, 1000)...)
	f.Write(t, visits(1, d1, 1000, 24)...) // 1,024 on the 8th
	f.Write(t, visits(1, d2, 500, 3000)...)
	f.Maintain(t, today)
	e := New(Options{Dir: f.Dir, Workers: 2})
	q := Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count"}, {Op: "actors"}}}
	res, err := e.Answer(context.Background(), q, Params{Project: 1, From: date(t, "2026-09-08"), To: date(t, "2026-09-08")}, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Rows[0], []string{"1024", "1024"}) || len(res.Approximate) > 0 {
		t.Errorf("1,024 actors: %q approximate %q", res.Rows, res.Approximate)
	}
	res, err = e.Answer(context.Background(), q, Params{Project: 1, From: date(t, "2026-09-08"), To: date(t, "2026-09-09")}, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := strconv.ParseFloat(res.Rows[0][1], 64)
	if err != nil {
		t.Fatal(err)
	}
	if exact := 3500.0; math.Abs(got-exact) > 0.02*exact || res.Rows[0][0] != "4024" {
		t.Errorf("3,500 actors: %q", res.Rows)
	}
	if !slices.Equal(res.Approximate, []string{"actors"}) {
		t.Errorf("approximate %q, want actors", res.Approximate)
	}
	// Per day, distinct counts stay exact.
	res, err = e.Answer(context.Background(), Query{Kind: "trend", Table: "views", Metrics: []Metric{{Op: "actors"}}},
		Params{Project: 1, From: date(t, "2026-09-08"), To: date(t, "2026-09-09")}, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"2026-09-08", "1024"}, {"2026-09-09", "3000"}}; !slices.EqualFunc(res.Rows, want, slices.Equal) || len(res.Approximate) > 0 {
		t.Errorf("trend %q approximate %q", res.Rows, res.Approximate)
	}
}

func TestBreakdownCapTruncates(t *testing.T) {
	f := archivetest.New(t)
	ts := date(t, today).Time()
	var evs []store.Event
	for p := range 50 {
		for k := range p + 1 {
			evs = append(evs, store.Event{ID: fmt.Sprintf("c-%d-%d", p, k), ProjectID: 1, Family: store.FamilyViews,
				EventName: "pageview", TS: ts.Add(time.Duration(len(evs)) * time.Second), Path: fmt.Sprintf("/p%02d", p)})
		}
	}
	f.Write(t, evs...)
	e := New(Options{Dir: f.Dir, Workers: 1, MaxGroups: 10})
	q := Query{Kind: "breakdown", Table: "views", Metrics: count(), Dimensions: []string{"path"}, Limit: 3}
	res, err := e.Answer(context.Background(), q, Params{Project: 1, From: date(t, today), To: date(t, today)}, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	// The first ten paths read are tracked; the rows are their top three.
	want := [][]string{{"/p09", "10"}, {"/p08", "9"}, {"/p07", "8"}}
	if !res.Truncated || !slices.EqualFunc(res.Rows, want, slices.Equal) {
		t.Errorf("truncated %v rows %q, want %q", res.Truncated, res.Rows, want)
	}
	// A trend's series too.
	q = Query{Kind: "trend", Table: "views", Metrics: count(), Series: "path", SeriesLimit: 2}
	res, err = e.Answer(context.Background(), q, Params{Project: 1, From: date(t, today), To: date(t, today)}, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	want = [][]string{{today, "/p08", "9"}, {today, "/p09", "10"}}
	if !res.Truncated || !slices.EqualFunc(res.Rows, want, slices.Equal) {
		t.Errorf("trend: truncated %v rows %q, want %q", res.Truncated, res.Rows, want)
	}
}

func TestMaxScansWaits(t *testing.T) {
	e := New(Options{Dir: t.TempDir(), MaxScans: 1})
	q := Query{Kind: "stat", Table: "views", Metrics: count()}
	p := Params{Project: 1, From: date(t, today), To: date(t, today)}
	e.sem <- struct{}{} // a scan running
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := e.Answer(ctx, q, p, archive.Rules{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled while waiting: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.Answer(context.Background(), q, p, archive.Rules{})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("ran beside a running scan: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	<-e.sem // the running scan ends
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting after the scan ended")
	}
}

func TestRulesColumnsCached(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir})
	defer func(r func(string, bool) ([]string, error)) { readFileColumns = r }(readFileColumns)
	var reads []string
	readFileColumns = func(path string, log bool) ([]string, error) {
		reads = append(reads, filepath.Base(path))
		return fileColumns(path, log)
	}
	a, err := e.columns()
	if err != nil {
		t.Fatal(err)
	}
	want, err := unionColumns(f.Dir)
	if err != nil || !slices.Equal(a, want) {
		t.Fatalf("columns %q, want unionColumns' %q (%v)", a, want, err)
	}
	reads = nil
	b, err := e.columns()
	if err != nil || !slices.Equal(a, b) || len(reads) != 0 {
		t.Fatalf("second call: %v %q, read %q", err, b, reads)
	}
	if slices.Contains(a, "attr_fresh") {
		t.Fatal("attr_fresh before it is written")
	}
	f.Write(t, store.Event{ID: "fresh", ProjectID: 1, Family: store.FamilyProduct, EventName: "x",
		TS: date(t, today).Time().Add(time.Hour), Attributes: map[string]string{"fresh": "1"}})
	c, err := e.columns()
	if err != nil || !slices.Contains(c, "attr_fresh") {
		t.Fatalf("after a log grew: %v %q", err, c)
	}
	if !slices.Equal(reads, []string{today + ".log"}) {
		t.Errorf("read %q, want only the log written to", reads)
	}
	if want, _ := unionColumns(f.Dir); !slices.Equal(c, want) {
		t.Errorf("columns %q, want %q", c, want)
	}
}

func TestMaxBytesTruncates(t *testing.T) {
	f := engineFixture(t)
	q := Query{Kind: "breakdown", Table: "views", Metrics: []Metric{{Op: "actors"}, {Op: "daily_max", Of: "users"}},
		Dimensions: []string{"path", "country"}, Limit: 1000}
	p := Params{Project: 1, From: date(t, engineFrom), To: date(t, today)}
	full, err := New(Options{Dir: f.Dir}).Answer(context.Background(), q, p, engineRules)
	if err != nil || full.Truncated {
		t.Fatalf("default budget: %v truncated %v", err, full.Truncated)
	}
	small, err := New(Options{Dir: f.Dir, MaxBytes: 1}).Answer(context.Background(), q, p, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	if !small.Truncated || len(small.Rows) == 0 || len(small.Rows) >= len(full.Rows) {
		t.Errorf("1-byte budget: truncated %v, %d rows of %d", small.Truncated, len(small.Rows), len(full.Rows))
	}
}

func TestDistinctInternsTrackedRowsOnly(t *testing.T) {
	filters, err := compileFilters([]Filter{{Column: "path", Op: "eq", Values: []string{"/a"}}})
	if err != nil {
		t.Fatal(err)
	}
	a, ms := newAgg(aggOptions{metrics: []metricSpec{{Metric: Metric{Op: "daily_max", Of: "actors"}}}, filters: filters,
		dims: []string{"path"}, ndays: 1, workers: 1, maxGroups: 10, maxBytes: 1 << 20})
	b := &Batch{N: 4, Cols: map[string]*Column{
		"day":      {Ints: []int64{0, 0, 0, 0}},
		"path":     {Dict: []string{"/a", "/b"}, Idx: []int32{0, 1, 1, 0}},
		"actor_id": {Dict: []string{"x", "y", "z", "w"}, Idx: []int32{0, 1, 2, 0}},
	}}
	if err := a.add(0, b); err != nil {
		t.Fatal(err)
	}
	if n := len(a.distinct[ms[0].distinct].in.ids); n != 1 {
		t.Errorf("%d ids interned, want 1 (only the filtered rows' actor)", n)
	}
	if a.mem.n.Load() <= 0 {
		t.Error("no memory counted")
	}
}

func TestRunRetriesVanishedFiles(t *testing.T) {
	defer func(n int, b time.Duration) { maxAttempts, retryBackoff = n, b }(maxAttempts, retryBackoff)
	maxAttempts, retryBackoff = 3, time.Millisecond
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A log listed but gone when read, on every attempt.
	if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "1", today+".log")); err != nil {
		t.Fatal(err)
	}
	e := New(Options{Dir: dir})
	q := Query{Kind: "stat", Table: "views", Metrics: count()}
	_, err := e.Answer(context.Background(), q, Params{Project: 1, From: date(t, today), To: date(t, today)}, archive.Rules{})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist after the retries", err)
	}
}
