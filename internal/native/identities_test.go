package native

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// identitiesProject is the identities fixture's project.
const identitiesProject = 4

// identitiesFrom is the fixture's first day: users and groups are first
// seen from then on, some of them before any range the tests ask.
const identitiesFrom = "2026-08-25"

// identityDay is one day (index d) of project 4's events: users u0 to
// u7 seen on views and product events from day k on (on two days of
// three), named on some of them; groups g0 to g2 beside some; views
// without a user; measures with a user only measures carry (m0, m1), and
// with u7 before its first view, which identities never count.
func identityDay(t testing.TB, day string, d int, tag string) []store.Event {
	t.Helper()
	base := date(t, day).Time().Add(6 * time.Hour)
	var out []store.Event
	n := 0
	add := func(e store.Event) {
		e.ProjectID = identitiesProject
		e.TS = base.Add(time.Duration(n) * time.Minute)
		e.ID = fmt.Sprintf("i-%s-%d%s", day, n, tag)
		e.Kind, e.ActorKind, e.Host = "web", "install", "example.com"
		n++
		out = append(out, e)
	}
	for k := range min(d+1, 8) {
		if (k+d)%3 == 2 {
			continue
		}
		e := store.Event{Family: store.FamilyViews, EventName: "pageview", ActorID: fmt.Sprintf("a%d", k),
			UserID: fmt.Sprintf("u%d", k), Path: fmt.Sprintf("/p%d", k%3)}
		if (k+d)%2 == 0 {
			e.UserName = fmt.Sprintf("User %d.%d", k, d)
		}
		if k%2 == 0 {
			e.GroupID = fmt.Sprintf("g%d", k%3)
			if d%3 == 0 {
				e.GroupName = fmt.Sprintf("Group %d.%d", k%3, d)
			}
		}
		add(e)
		if (k*d)%4 == 1 {
			add(store.Event{Family: store.FamilyProduct, EventName: "click", ActorID: fmt.Sprintf("a%d", k),
				UserID: fmt.Sprintf("u%d", k), GroupID: e.GroupID})
		}
	}
	add(store.Event{Family: store.FamilyViews, EventName: "pageview", ActorID: "anon", Path: "/anon"})
	v := 1.5
	add(store.Event{Family: store.FamilyMeasures, EventName: "lcp", Measure: "web", ActorID: "a1",
		UserID: fmt.Sprintf("m%d", d%2), Value: &v})
	if d < 7 {
		add(store.Event{Family: store.FamilyMeasures, EventName: "lcp", Measure: "web", ActorID: "a7", UserID: "u7", Value: &v})
	}
	return out
}

// identitiesFixture writes project 4's days August 25 to September 9 and
// runs the daily pass as of today, which derives identities.parquet; then
// writes today's events and late events on September 3 (a new user, and
// u1 renamed, then seen unnamed today), which stay in logs over the
// current file.
func identitiesFixture(t testing.TB) *archivetest.Files {
	t.Helper()
	f := archivetest.New(t)
	start := date(t, identitiesFrom).Time()
	days := 0
	for ; ; days++ {
		day := start.AddDate(0, 0, days).Format(time.DateOnly)
		if day == today {
			break
		}
		f.Write(t, identityDay(t, day, days, "")...)
	}
	f.Maintain(t, today)
	f.Write(t, identityDay(t, today, days, "")...)
	late := date(t, "2026-09-03").Time().Add(20 * time.Hour)
	f.Write(t,
		store.Event{ID: "i-late-1", ProjectID: identitiesProject, Family: store.FamilyViews, EventName: "pageview", TS: late,
			Kind: "web", ActorID: "a9", UserID: "late", UserName: "Late"},
		store.Event{ID: "i-late-2", ProjectID: identitiesProject, Family: store.FamilyProduct, EventName: "click", TS: late.Add(time.Minute),
			Kind: "web", ActorID: "a1", UserID: "u1", UserName: "Renamed"},
		// Unnamed, but the logs' latest of u1: their name, Renamed, is the
		// one combined, the file's last_seen being earlier.
		store.Event{ID: "i-late-3", ProjectID: identitiesProject, Family: store.FamilyViews, EventName: "pageview",
			TS: date(t, today).Time().Add(22 * time.Hour), Kind: "web", ActorID: "a1", UserID: "u1"})
	all, err := archive.ListFiles(f.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if p := all[identitiesProject]; p.Identities == "" || len(p.Logs) < 2 {
		t.Fatalf("identities file %q, logs %q: want a current file and two logs", p.Identities, p.Logs)
	}
	return f
}

// identityColumnList is the identities table's columns, for DuckDB.
var identityColumnList = `"` + strings.Join(identityColumns, `", "`) + `"`

// identityRows is project's identities first seen in r (every one, r nil)
// as the engine loads them, formatted as the goldens keep the rows,
// sorted.
func identityRows(t *testing.T, e *Engine, project int64, r *Range) [][]string {
	t.Helper()
	ids, err := e.identities(context.Background(), project, listFiles(t, e)[project], func(int64) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	b := identityBatch(ids.rowsIn(r), project)
	rows := make([][]string, b.N)
	for i := range b.N {
		row := make([]string, len(identityColumns))
		for j, name := range identityColumns {
			_, c := b.col(name)
			if c == nil {
				t.Fatalf("batch without %s", name)
			}
			row[j] = cellText(c, i, false)
		}
		rows[i] = row
	}
	sortRows(rows)
	return rows
}

func listFiles(t *testing.T, e *Engine) map[int64]archive.Files {
	t.Helper()
	all, err := archive.ListFiles(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func TestIdentitiesRowsMatchDuckDB(t *testing.T) {
	f := identitiesFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	pdir := filepath.Join(f.Dir, strconv.Itoa(identitiesProject))
	check := func(t *testing.T) {
		t.Helper()
		identityRows(t, e, identitiesProject, nil) // through the cache too

		got := identityRows(t, e, identitiesProject, nil)
		want := goldenTable(t, `SELECT `+identityColumnList+` FROM identities WHERE "project_id" = 4 AND '%[1]s' = '%[2]s'`, "", "", got)
		sortRows(want)
		if len(want) < 10 {
			t.Fatalf("the golden has %d identities: a weak case", len(want))
		}
		sameTable(t, got, want)

		// Those first seen in a range, put together alone.
		r := rng(t, "2026-09-01", today)
		inRange := identityRows(t, e, identitiesProject, &r)
		want = goldenTable(t, `SELECT `+identityColumnList+` FROM identities WHERE "project_id" = 4
			AND first_seen::DATE BETWEEN DATE '%[1]s' AND DATE '%[2]s'`, "2026-09-01", today, inRange)
		sortRows(want)
		if len(want) == 0 || len(want) == len(got) {
			t.Fatalf("the golden has %d of %d identities first seen in range: a weak case", len(want), len(got))
		}
		sameTable(t, inRange, want)

		// The first-seen days a status looks up are every identity's, and
		// the names a label looks up their names.
		first, err := e.firstSeen(context.Background(), identitiesProject, listFiles(t, e)[identitiesProject], true, func(int64) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range got {
			day, ok := first.firstDay(row[1] == "group", row[2])
			if !ok || formatDay(day) != row[4][:10] {
				t.Errorf("%s %s first seen on %s (%t), want %s", row[1], row[2], formatDay(day), ok, row[4][:10])
			}
			if name, _ := first.name(identityKey{Group: row[1] == "group", ID: row[2]}); name != row[3] {
				t.Errorf("%s %s named %q, want %q", row[1], row[2], name, row[3])
			}
		}
		if n := len(first.base.users) + len(first.base.groups) + len(first.live.users) + len(first.live.groups); n < len(got) {
			t.Errorf("%d first-seen days for %d identities", n, len(got))
		}
	}
	t.Run("current file and logs", func(t *testing.T) {
		check(t)
		got := identityRows(t, e, identitiesProject, nil)
		if !slices.ContainsFunc(got, func(r []string) bool { return r[2] == "u1" && r[3] == "Renamed" }) {
			t.Error("u1 is not named by the logs, whose last_seen is the latest")
		}
	})
	t.Run("a log grown", func(t *testing.T) {
		at := date(t, today).Time().Add(23 * time.Hour)
		f.Write(t, store.Event{ID: "i-grown", ProjectID: identitiesProject, Family: store.FamilyViews, EventName: "pageview",
			TS: at, Kind: "web", ActorID: "a2", UserID: "u2", UserName: "Grown", GroupID: "g9"})
		check(t)
		got := identityRows(t, e, identitiesProject, nil)
		if !slices.ContainsFunc(got, func(r []string) bool { return r[2] == "g9" }) {
			t.Error("the grown log's group is not read")
		}
	})
	t.Run("stale file", func(t *testing.T) {
		later := time.Now().Add(time.Hour)
		if err := os.Chtimes(filepath.Join(pdir, "2026-09-05.parquet"), later, later); err != nil {
			t.Fatal(err)
		}
		all, err := archive.ListFiles(f.Dir)
		if err != nil {
			t.Fatal(err)
		}
		if p := all[identitiesProject].Identities; p != "" {
			t.Fatalf("identities file %q is current", p)
		}
		check(t)
	})
	t.Run("missing file", func(t *testing.T) {
		if err := os.Remove(filepath.Join(pdir, "identities.parquet")); err != nil {
			t.Fatal(err)
		}
		e = New(Options{Dir: f.Dir, Workers: 3}) // nothing cached
		check(t)
	})
}

// statusSQL is the events of project 4 in range with their user_status
// and group_status, as the system widgets join identities.
const statusSQL = `WITH s AS (SELECT e.*,
		CASE WHEN e."day" = u.first_seen::DATE THEN 'new' WHEN e."day" > u.first_seen::DATE THEN 'returning' END AS us,
		CASE WHEN e."day" = g.first_seen::DATE THEN 'new' WHEN e."day" > g.first_seen::DATE THEN 'returning' END AS gs
	FROM events e
	LEFT JOIN identities u ON u.project_id = 4 AND u.kind = 'user' AND u.id = e.user_id
	LEFT JOIN identities g ON g.project_id = 4 AND g.kind = 'group' AND g.id = e.group_id
	WHERE e."project_id" = 4 AND e."day" BETWEEN DATE '%[1]s' AND DATE '%[2]s') `

func TestIdentitiesQueriesMatchDuckDB(t *testing.T) {
	f := identitiesFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 2})
	const from, to = "2026-09-01", today
	widgetFamilies := Filter{Column: "family", Op: "in", Values: []string{"views", "product"}}
	firstIn := `"project_id" = 4 AND first_seen::DATE BETWEEN DATE '%[1]s' AND DATE '%[2]s'`
	cases := []struct {
		name     string
		q        Query
		cols     []string
		sql      string
		from, to string
	}{
		{name: "daily active users", q: Query{Kind: "trend", Table: "events", Metrics: []Metric{{Op: "users"}}, Series: "user_status",
			Where: []Filter{widgetFamilies, {Column: "user_id", Op: "is_set"}}}, cols: []string{"day", "series", "users"},
			sql: statusSQL + `SELECT "day", us, count(DISTINCT "user_id") FROM s WHERE "family" IN ('views', 'product') AND "user_id" <> ''
				GROUP BY ALL ORDER BY "day", us ASC NULLS LAST`},
		// The new-users widget's own SQL.
		{name: "new users", q: Query{Kind: "stat", Table: "events", Metrics: []Metric{{Op: "users"}},
			Where: []Filter{widgetFamilies, {Column: "user_status", Op: "eq", Values: []string{"new"}}}}, cols: []string{"users"},
			sql: `SELECT count(DISTINCT e.user_id) FILTER (WHERE e.day = i.first_seen::DATE) FROM events e
				JOIN identities i ON i.project_id = 4 AND i.kind = 'user' AND i.id = e.user_id
				WHERE e.project_id = 4 AND e.family IN ('views', 'product') AND e.user_id <> ''
				AND e.day BETWEEN DATE '%[1]s' AND DATE '%[2]s'`},
		{name: "new groups", q: Query{Kind: "stat", Table: "product", Metrics: []Metric{{Op: "groups"}, {Op: "count"}},
			Where: []Filter{{Column: "group_status", Op: "eq", Values: []string{"new"}}}, Compare: "previous"},
			cols: []string{"groups", "count", "previous_groups", "previous_count"},
			sql: statusSQL + `, p AS (SELECT count(DISTINCT "group_id") AS g, count(*) AS c FROM s WHERE "family" = 'product' AND gs = 'new'),
				q AS (SELECT count(DISTINCT e."group_id") AS g, count(*) AS c FROM events e
					JOIN identities i ON i.project_id = 4 AND i.kind = 'group' AND i.id = e.group_id
					WHERE e."project_id" = 4 AND e."family" = 'product' AND e."day" BETWEEN DATE '2026-08-22' AND DATE '2026-08-31'
					AND e."day" = i.first_seen::DATE)
				SELECT p.g, p.c, q.g, q.c FROM p, q`},
		{name: "status on every family", q: Query{Kind: "breakdown", Table: "events", Metrics: []Metric{{Op: "count"}, {Op: "users"}, {Op: "groups"}},
			Dimensions: []string{"user_status", "group_status", "family"}, Limit: 100}, from: identitiesFrom, cols: []string{"user_status", "group_status", "family", "count", "users", "groups"},
			sql: statusSQL + `SELECT us, gs, "family", count(*) AS c, count(DISTINCT "user_id"), count(DISTINCT "group_id") FROM s
				GROUP BY ALL ORDER BY c DESC, us ASC NULLS LAST, gs ASC NULLS LAST, "family" ASC LIMIT 100`},
		{name: "views by status", q: Query{Kind: "breakdown", Table: "views", Metrics: count(), Dimensions: []string{"user_status", "path"},
			Where: []Filter{{Column: "user_status", Op: "ne", Values: []string{"returning"}}}, Limit: 100},
			cols: []string{"user_status", "path", "count"},
			sql: statusSQL + `SELECT us, "path", count(*) AS c FROM s WHERE "family" = 'views' AND us <> 'returning'
				GROUP BY ALL ORDER BY c DESC, us ASC NULLS LAST, "path" ASC NULLS LAST LIMIT 100`},
		{name: "new users beside all", q: Query{Kind: "stat", Table: "events", Where: []Filter{widgetFamilies},
			Metrics: []Metric{{Op: "users"}, {Op: "users", As: "new", Where: []Filter{{Column: "user_status", Op: "eq", Values: []string{"new"}}}}}},
			cols: []string{"users", "new"},
			sql:  statusSQL + `SELECT count(DISTINCT "user_id"), count(DISTINCT "user_id") FILTER (WHERE us = 'new') FROM s WHERE "family" IN ('views', 'product')`},
		{name: "identities by kind", q: Query{Kind: "breakdown", Table: "identities",
			Metrics:    []Metric{{Op: "count"}, {Op: "sum", Column: "views"}, {Op: "avg", Column: "events"}, {Op: "max", Column: "views"}},
			Dimensions: []string{"kind"}}, cols: []string{"kind", "count", "sum_views", "avg_events", "max_views"},
			from: identitiesFrom,
			sql: `SELECT "kind", count(*) AS c, sum("views"), avg("events"), max("views") FROM identities WHERE ` + firstIn + `
				GROUP BY ALL ORDER BY c DESC, "kind" ASC`},
		{name: "identities first seen per day", q: Query{Kind: "trend", Table: "identities", Metrics: count(), Series: "kind"},
			cols: []string{"day", "series", "count"}, from: identitiesFrom,
			sql: `SELECT first_seen::DATE, "kind", count(*) FROM identities WHERE ` + firstIn + ` GROUP BY ALL ORDER BY 1, 2`},
		{name: "identities by name", q: Query{Kind: "breakdown", Table: "identities", Metrics: []Metric{{Op: "sum", Column: "events"}},
			Dimensions: []string{"id", "name", "day"}, Where: []Filter{{Column: "kind", Op: "eq", Values: []string{"user"}},
				{Column: "views", Op: "gt", Values: []string{"2"}}}, Limit: 100},
			cols: []string{"id", "name", "day", "sum_events"}, from: identitiesFrom,
			sql: `SELECT "id", "name", first_seen::DATE, sum("events") AS s FROM identities WHERE ` + firstIn + `
				AND "kind" = 'user' AND "views" > 2 GROUP BY ALL ORDER BY s DESC, "id" ASC, "name" ASC NULLS LAST, 3 LIMIT 100`},
		{name: "identities in range", q: Query{Kind: "stat", Table: "identities", Metrics: []Metric{{Op: "count"}, {Op: "min", Column: "events"}}},
			cols: []string{"count", "min_events"},
			sql:  `SELECT count(*), min("events") FROM identities WHERE ` + firstIn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from, to := from, to
			if c.from != "" {
				from = c.from
			}
			if c.to != "" {
				to = c.to
			}
			res, err := e.Answer(context.Background(), c.q, Params{Project: identitiesProject, From: date(t, from), To: date(t, to)}, engineRules)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(res.Columns, c.cols) {
				t.Fatalf("columns %q, want %q", res.Columns, c.cols)
			}
			want := goldenTable(t, c.sql, from, to, res.Rows)
			if len(want) == 0 {
				t.Fatal("the SQL returns no rows: a weak case")
			}
			sameTable(t, res.Rows, want)
		})
	}
	// The fixture is built for this: users first seen before the range,
	// and some within it.
	byStatus := Query{Kind: "breakdown", Table: "events", Metrics: []Metric{{Op: "users"}}, Dimensions: []string{"user_status"},
		Where: []Filter{{Column: "user_status", Op: "is_set"}}}
	got := answerRows(t, e, identitiesProject, byStatus, from, to, engineRules)
	sortRows(got) // ORDER BY 1
	rows := goldenTable(t, statusSQL+`SELECT us, count(DISTINCT "user_id") FROM s WHERE us IS NOT NULL GROUP BY ALL ORDER BY 1`, from, to, got)
	sameTable(t, got, rows)
	if len(rows) != 2 || rows[0][0] != "new" || rows[1][0] != "returning" || rows[0][1] == "0" {
		t.Errorf("users by status in range: %q, want both new and returning", rows)
	}
}

// TestIdentitiesCharged: the identities a query loads count against
// MaxBytes.
func TestIdentitiesCharged(t *testing.T) {
	f := identitiesFixture(t)
	e := New(Options{Dir: f.Dir, MaxBytes: 64})
	for _, q := range []Query{
		{Kind: "stat", Table: "identities", Metrics: count()},
		{Kind: "stat", Table: "views", Metrics: count(), Where: []Filter{{Column: "user_status", Op: "is_set"}}},
	} {
		_, err := e.Answer(context.Background(), q, Params{Project: identitiesProject, From: date(t, identitiesFrom), To: date(t, today)}, archive.Rules{})
		if !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: err %v, want ErrTooLarge", q.Table, err)
		}
	}
}

// copyTo is evs as project's events, their ids marked with it.
func copyTo(project int64, evs []store.Event) []store.Event {
	out := slices.Clone(evs)
	for i := range out {
		out[i].ProjectID = project
		out[i].ID = fmt.Sprintf("%s-p%d", out[i].ID, project)
	}
	return out
}

// TestIdentitiesCacheBounded: the cached identities of several projects
// take at most MaxBytes together, the least recently used dropped.
func TestIdentitiesCacheBounded(t *testing.T) {
	f := archivetest.New(t)
	start := date(t, identitiesFrom).Time()
	projects := []int64{4, 5, 6, 7, 8, 9, 10, 11}
	for d := 0; d < 6; d++ {
		day := start.AddDate(0, 0, d).Format(time.DateOnly)
		for _, p := range projects {
			f.Write(t, copyTo(p, identityDay(t, day, d, ""))...)
		}
	}
	f.Maintain(t, today)
	q := Query{Kind: "stat", Table: "identities", Metrics: count()}
	params := func(p int64) Params { return Params{Project: p, From: date(t, identitiesFrom), To: date(t, today)} }

	big := New(Options{Dir: f.Dir})
	for _, p := range projects {
		if _, err := big.Answer(context.Background(), q, params(p), archive.Rules{}); err != nil {
			t.Fatal(err)
		}
	}
	one := big.idCache.size() / int64(len(projects))
	if n := len(cachedProjects(big)); one == 0 || n != len(projects) {
		t.Fatalf("%d bytes a project, %d cached: want every project cached", one, n)
	}

	// Room for five projects' sets (a query needs about three times one:
	// the set, put together, and its batch).
	limit := 5*one + one/2
	e := New(Options{Dir: f.Dir, MaxBytes: limit})
	for _, p := range projects {
		if _, err := e.Answer(context.Background(), q, params(p), archive.Rules{}); err != nil {
			t.Fatal(err)
		}
		if n := e.idCache.size(); n > limit {
			t.Fatalf("after project %d: %d bytes cached, over %d", p, n, limit)
		}
	}
	if cached, want := cachedProjects(e), []int64{7, 8, 9, 10, 11}; !slices.Equal(cached, want) {
		t.Errorf("projects %v cached, want the last used %v", cached, want)
	}
}

// cachedProjects is the projects whose stored identities part e caches
// (the live one is empty once the logs are dumped), sorted.
func cachedProjects(e *Engine) []int64 {
	e.idCache.mu.Lock()
	defer e.idCache.mu.Unlock()
	var out []int64
	for part, x := range e.idCache.entries {
		if !part.live && x.bytes > 0 {
			out = append(out, part.project)
		}
	}
	slices.Sort(out)
	return out
}

// countLoads counts the identity parts loaded, by part, until the test
// ends.
func countLoads(t *testing.T) func() map[idPart]int {
	t.Helper()
	var mu sync.Mutex
	loads := map[idPart]int{}
	onIdentityLoad = func(p idPart) {
		mu.Lock()
		defer mu.Unlock()
		loads[p]++
	}
	t.Cleanup(func() { onIdentityLoad = func(idPart) {} })
	return func() map[idPart]int {
		mu.Lock()
		defer mu.Unlock()
		return maps.Clone(loads)
	}
}

// TestIdentitiesPartsCachedApart: a log's flush loads the live part again
// and not the stored one; the first-seen days are cached as the rows are,
// at a fraction of their bytes.
func TestIdentitiesPartsCachedApart(t *testing.T) {
	f := identitiesFixture(t)
	e := New(Options{Dir: f.Dir})
	loads := countLoads(t)
	ctx, free := context.Background(), func(int64) error { return nil }
	load := func() {
		t.Helper()
		files := listFiles(t, e)[identitiesProject]
		if _, err := e.identities(ctx, identitiesProject, files, free); err != nil {
			t.Fatal(err)
		}
		if _, err := e.firstSeen(ctx, identitiesProject, files, false, free); err != nil {
			t.Fatal(err)
		}
	}
	load()
	load()
	base, live := idPart{project: identitiesProject}, idPart{project: identitiesProject, live: true}
	firstBase, firstLive := idPart{project: identitiesProject, first: true}, idPart{project: identitiesProject, live: true, first: true}
	if got, want := loads(), map[idPart]int{base: 1, live: 1, firstBase: 1, firstLive: 1}; !maps.Equal(got, want) {
		t.Fatalf("loads %v, want %v: each part once", got, want)
	}
	f.Write(t, store.Event{ID: "i-flushed", ProjectID: identitiesProject, Family: store.FamilyViews, EventName: "pageview",
		TS: date(t, today).Time().Add(23 * time.Hour), Kind: "web", ActorID: "a3", UserID: "u-flushed"})
	load()
	if got, want := loads(), map[idPart]int{base: 1, live: 2, firstBase: 1, firstLive: 2}; !maps.Equal(got, want) {
		t.Errorf("loads %v after a log's flush, want %v: the live parts alone again", got, want)
	}

	e.idCache.mu.Lock()
	full := e.idCache.entries[base].bytes + e.idCache.entries[live].bytes
	first := e.idCache.entries[firstBase].bytes + e.idCache.entries[firstLive].bytes
	e.idCache.mu.Unlock()
	if first == 0 || 2*first > full {
		t.Errorf("first-seen days take %d bytes, the rows %d: want under half", first, full)
	}
}

// TestIdentitiesLoadedOnce: concurrent queries that miss the cache for
// one project load each part of its identities once.
func TestIdentitiesLoadedOnce(t *testing.T) {
	f := identitiesFixture(t)
	e := New(Options{Dir: f.Dir})
	var loads atomic.Int64
	release := make(chan struct{})
	onIdentityLoad = func(idPart) {
		loads.Add(1)
		<-release // hold the load while the others queue behind it
	}
	t.Cleanup(func() { onIdentityLoad = func(idPart) {} })
	files := listFiles(t, e)[identitiesProject]
	var wg sync.WaitGroup
	sets := make([]*identitySet, 8)
	for i := range sets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			set, err := e.identities(context.Background(), identitiesProject, files, func(int64) error { return nil })
			if err != nil {
				t.Error(err)
			}
			sets[i] = set
		}()
	}
	for loads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let the others reach the wait
	close(release)
	wg.Wait()
	if n := loads.Load(); n != 2 {
		t.Errorf("%d loads, want 2: the stored part and the live one", n)
	}
	same := func(a, b identityMap) bool {
		return reflect.ValueOf(a).UnsafePointer() == reflect.ValueOf(b).UnsafePointer()
	}
	for _, s := range sets {
		if !same(s.base, sets[0].base) || !same(s.live, sets[0].live) {
			t.Fatal("the queries got different sets")
		}
	}
}
