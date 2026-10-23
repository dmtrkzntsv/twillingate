package native

import (
	"context"
	"errors"
	"fmt"
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

// retentionFrom is the retention fixture's first day: three months
// before today, so June, July and August become month files.
const retentionFrom = "2026-06-12"

// retentionDay is one day (index d) of project 1's events for the
// retention scan:
//   - installs i00 to i29, each from its own first day on, back every
//     few days for a while; a third of them sign in three days after
//     their first (the same actor_id sent as a user from then on), some
//     are users from their first day; their events are views, and on
//     some days a product event (ev1, renamed signup);
//   - b1, whose first event is a blocked product event (ev3), then views
//     from day 20;
//   - m1, a measure on day 1, views from day 30 (measures do not count);
//   - anonymous actors: a connection, and an install without an id.
func retentionDay(t testing.TB, day string, d int) []store.Event {
	t.Helper()
	base := date(t, day).Time().Add(8 * time.Hour)
	var out []store.Event
	n := 0
	add := func(e store.Event) {
		e.ProjectID = 1
		e.TS = base.Add(time.Duration(n) * time.Second)
		e.ID = fmt.Sprintf("r-%s-%d", day, n)
		e.Host = "example.com"
		if e.EventName == "" {
			e.EventName = "pageview"
		}
		n++
		out = append(out, e)
	}
	for k := range 30 {
		start := (k * 3) % 80
		if d < start || (d-start)%(k%4+1) != 0 || (d-start >= 40 && k%5 != 0) {
			continue
		}
		e := store.Event{Family: store.FamilyViews, ActorID: fmt.Sprintf("i%02d", k), ActorKind: store.ActorInstall,
			Country: []string{"DE", "FR"}[k%2], Path: fmt.Sprintf("/p%d", (k+d)%3)}
		if (k%3 == 0 && d >= start+3) || k%7 == 1 {
			e.ActorKind, e.UserID = store.ActorUser, fmt.Sprintf("u%02d", k)
		}
		if d%4 == k%4 {
			e.Family, e.EventName = store.FamilyProduct, "ev1"
		}
		add(e)
	}
	if d == 2 {
		add(store.Event{Family: store.FamilyProduct, EventName: "ev3", ActorID: "b1", ActorKind: store.ActorInstall, Country: "DE"})
	}
	if d >= 20 && d%5 == 0 {
		add(store.Event{Family: store.FamilyViews, ActorID: "b1", ActorKind: store.ActorInstall, Country: "DE"})
	}
	if d == 1 {
		v := 1.5
		add(store.Event{Family: store.FamilyMeasures, EventName: "lcp", Measure: "web", ActorID: "m1", ActorKind: store.ActorInstall, Value: &v})
	}
	if d >= 30 && d%3 == 0 {
		add(store.Event{Family: store.FamilyViews, ActorID: "m1", ActorKind: store.ActorInstall, Country: "FR"})
	}
	add(store.Event{Family: store.FamilyViews, ActorID: fmt.Sprintf("c%d", d%4), ActorKind: store.ActorConnection, Country: "DE"})
	add(store.Event{Family: store.FamilyViews, ActorKind: store.ActorInstall, Country: "DE"})
	return out
}

// retentionFixture writes the retention days up to the day before today
// and runs the daily pass; then writes a late view of i25 (first seen on
// day 75) on July 1, in a log beside July's month file, and today's
// events (an actor seen only today among them), which the scan leaves out.
func retentionFixture(t testing.TB) *archivetest.Files {
	t.Helper()
	f := archivetest.New(t)
	start := date(t, retentionFrom).Time()
	for d := 0; ; d++ {
		day := start.AddDate(0, 0, d).Format(time.DateOnly)
		if day == today {
			f.Maintain(t, today)
			f.Write(t, retentionDay(t, day, d)...)
			break
		}
		f.Write(t, retentionDay(t, day, d)...)
	}
	f.Write(t,
		store.Event{ID: "r-late", ProjectID: 1, Family: store.FamilyViews, EventName: "pageview", ActorID: "i25",
			ActorKind: store.ActorInstall, Country: "FR", TS: date(t, "2026-07-01").Time().Add(9 * time.Hour)},
		store.Event{ID: "r-today", ProjectID: 1, Family: store.FamilyViews, EventName: "pageview", ActorID: "t1",
			ActorKind: store.ActorUser, TS: date(t, today).Time().Add(9 * time.Hour)})
	all, err := archive.ListFiles(f.Dir)
	if err != nil {
		t.Fatal(err)
	}
	months := 0
	for _, p := range all[1].Parquet {
		if monthName.MatchString(p[strings.LastIndex(p, "/")+1:]) {
			months++
		}
	}
	if months < 3 || len(all[1].Logs) < 2 {
		t.Fatalf("parquet %q, logs %q: want three month files and two logs", all[1].Parquet, all[1].Logs)
	}
	return f
}

// retentionStateSQL is the retention widgets' acts and firsts CTEs, as
// cohorts.sql has them (today, the offsets and extra conditions put in),
// and per actor of a cohort in the range its days back within the offsets.
const retentionStateSQL = `WITH acts AS (
  SELECT DISTINCT actor_id, actor_kind, day
  FROM events
  WHERE project_id = 1 AND family IN ('views', 'product') AND actor_id <> ''
    AND actor_kind IN ('user', 'install')
    AND day < DATE '{today}' AND day <= DATE '%[2]s' + {offsets} {where}
),
firsts AS (
  SELECT actor_id, MIN(day) AS cohort_day, MIN(day) FILTER (WHERE actor_kind = 'user') AS user_day
  FROM acts
  WHERE actor_kind IN ('user', 'install')
  GROUP BY actor_id
),
days AS (SELECT DISTINCT actor_id, day FROM acts)
SELECT f.actor_id, strftime(f.cohort_day, '%%Y-%%m-%%d'), strftime(f.user_day, '%%Y-%%m-%%d'),
  string_agg(CAST(d.day - f.cohort_day AS VARCHAR), ',' ORDER BY d.day)
FROM firsts f JOIN days d ON d.actor_id = f.actor_id AND d.day >= f.cohort_day AND d.day <= f.cohort_day + {offsets}
WHERE f.cohort_day BETWEEN DATE '%[1]s' AND DATE '%[2]s'
GROUP BY ALL ORDER BY f.actor_id`

// retentionRows is the engine's per-actor retention state as
// retentionStateSQL's rows.
func retentionRows(t *testing.T, e *Engine, where []Filter, from, to string, offsets int) ([][]string, error) {
	t.Helper()
	filters, err := compileFilters(where, "where")
	if err != nil {
		t.Fatal(err)
	}
	rs, err := e.ruleSet(1, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	s := scan{e: e, filters: filters, project: 1, rules: rs}
	all, err := archive.ListFiles(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	actors, err := s.retentionActors(context.Background(), retentionSpec{from: date(t, from), to: date(t, to), offsets: offsets},
		all[1], e.charge())
	if err != nil {
		return nil, err
	}
	rows := make([][]string, 0, len(actors))
	for _, a := range actors {
		user := ""
		if a.user != noUserDay {
			user = formatDay(a.user)
		}
		var days []string
		for o := range offsets + 1 {
			if a.activeOn(o) {
				days = append(days, strconv.Itoa(o))
			}
		}
		rows = append(rows, []string{a.id, formatDay(a.cohort), user, strings.Join(days, ",")})
	}
	sortRows(rows)
	return rows, nil
}

func TestRetentionStateMatchesDuckDB(t *testing.T) {
	f := retentionFixture(t)
	for _, c := range []struct {
		name     string
		from, to string
		offsets  int
		today    string
		where    []Filter
		sql      string // the where's conditions, for DuckDB
	}{
		{name: "every day", from: retentionFrom, to: "2026-09-09", offsets: 45},
		{name: "first seen before the range", from: "2026-07-15", to: "2026-07-31", offsets: 30},
		{name: "past today", from: "2026-08-15", to: "2026-09-09", offsets: 90},
		{name: "one day", from: "2026-07-01", to: "2026-07-01", offsets: 1},
		{name: "earlier today", from: "2026-08-01", to: "2026-08-10", offsets: 20, today: "2026-08-20"},
		{name: "filtered", from: "2026-07-01", to: "2026-08-15", offsets: 45,
			where: []Filter{{Column: "country", Op: "eq", Values: []string{"DE"}}, {Column: "event_name", Op: "ne", Values: []string{"signup"}}},
			sql:   `AND "country" = 'DE' AND "event_name" <> 'signup'`},
		{name: "before any event", from: "2026-05-01", to: "2026-05-10", offsets: 45},
		{name: "from today", from: today, to: "2026-09-20", offsets: 10},
	} {
		t.Run(c.name, func(t *testing.T) {
			now := c.today
			if now == "" {
				now = today
			}
			clock := date(t, now).Time().Add(13 * time.Hour)
			e := New(Options{Dir: f.Dir, Workers: 3, Now: func() time.Time { return clock }})
			got, err := retentionRows(t, e, c.where, c.from, c.to, c.offsets)
			if err != nil {
				t.Fatal(err)
			}
			sql := strings.NewReplacer("{today}", now, "{offsets}", strconv.Itoa(c.offsets), "{where}", c.sql).Replace(retentionStateSQL)
			want := duckTable(t, f, sql, c.from, c.to)
			sortRows(want)
			t.Logf("%d actors", len(want))
			if empty := c.from < retentionFrom || c.from == today; len(want) == 0 && !empty {
				t.Fatal("DuckDB has no actors: a weak case")
			}
			sameTable(t, got, want)
		})
	}
}

// The fixture's edge cases, by name: the rule, the measure, the late log
// and today's actor are each where the parity test assumes them.
func TestRetentionStateEdges(t *testing.T) {
	f := retentionFixture(t)
	e := New(Options{Dir: f.Dir, Now: func() time.Time { return date(t, today).Time() }})
	rows, err := retentionRows(t, e, nil, retentionFrom, today, 45)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string][]string{}
	for _, r := range rows {
		byID[r[0]] = r
	}
	for id, cohort := range map[string]string{"b1": "2026-07-02", "m1": "2026-07-12", "i25": "2026-07-01", "i03": "2026-06-21",
		"i00": "2026-06-12"} {
		if r := byID[id]; r == nil || r[1] != cohort {
			t.Errorf("%s: %q, want cohort %s", id, r, cohort)
		}
	}
	if r := byID["i00"]; r == nil || r[2] != "2026-06-15" {
		t.Errorf("i00 signs in three days on: %q", r)
	}
	for _, id := range []string{"t1", "c0", ""} {
		if r, ok := byID[id]; ok {
			t.Errorf("%q counted: %q", id, r)
		}
	}
}

// The per-actor state is charged: a small budget refuses the scan with
// ErrTooLarge, and a done context stops it.
func TestRetentionStateBounded(t *testing.T) {
	f := retentionFixture(t)
	now := func() time.Time { return date(t, today).Time() }
	if _, err := retentionRows(t, New(Options{Dir: f.Dir, Now: now}), nil, retentionFrom, today, 90); err != nil {
		t.Fatalf("default budget: %v", err)
	}
	_, err := retentionRows(t, New(Options{Dir: f.Dir, Now: now, MaxBytes: 1 << 10}), nil, retentionFrom, today, 90)
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("1 KiB budget: %v, want ErrTooLarge", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := New(Options{Dir: f.Dir, Now: now})
	rs, err := e.ruleSet(1, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	s := scan{e: e, project: 1, rules: rs}
	if _, err := s.retentionActors(ctx, retentionSpec{from: date(t, retentionFrom), to: date(t, today), offsets: 45}, archive.Files{}, e.charge()); !errors.Is(err, context.Canceled) {
		t.Errorf("done context: %v, want context.Canceled", err)
	}
}

// Files that cannot hold a row before min(today − 1, to + offsets) are
// not read; every earlier one is, whatever from is.
func TestRetentionFiles(t *testing.T) {
	f := retentionFixture(t)
	all, err := archive.ListFiles(f.Dir)
	if err != nil {
		t.Fatal(err)
	}
	pq, logs := retentionFiles(all[1], date(t, "2026-07-20"))
	if got := strings.Join(names(pq), " "); got != "2026-06.parquet 2026-07.parquet" {
		t.Errorf("parquet %s, want June's and July's", got)
	}
	if got := strings.Join(names(logs), " "); got != "2026-07-01.log" {
		t.Errorf("logs %s, want July 1's", got)
	}
}

// retentionSQL is the built-in retention widget file name's SQL as
// written, run on the fixture: today put in for current_date, the
// parameters bound (from and to as duckTable's %[1]s and %[2]s), the
// offsets put in for its 45, and extra conditions added to acts.
func retentionSQL(t *testing.T, name, now string, offsets int, where string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "reporting", "system", "retention", name+".sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	acts := "AND day <= :to + 45\n"
	if !strings.Contains(sql, acts) {
		t.Fatalf("%s: no %q in acts", name, acts)
	}
	sql = strings.Replace(sql, acts, "AND day <= :to + 45 "+where+"\n", 1)
	n := strconv.Itoa(offsets)
	return strings.NewReplacer("current_date", "DATE '"+now+"'", ":project", "1", ":from", "DATE '%[1]s'", ":to", "DATE '%[2]s'",
		"+ 45", "+ "+n, "BETWEEN 0 AND 45", "BETWEEN 0 AND "+n, "range(0, 46)", "range(0, "+strconv.Itoa(offsets+1)+")").Replace(sql)
}

// The retention kind's grid, curves and milestones equal the built-in
// retention widgets' SQL, run as written.
func TestRetentionMatchesSQL(t *testing.T) {
	f := retentionFixture(t)
	for _, c := range []struct {
		name     string
		from, to string
		offsets  int
		today    string
		where    []Filter
		sql      string
	}{
		{name: "every day", from: retentionFrom, to: "2026-09-09"},
		{name: "first seen before the range", from: "2026-07-15", to: "2026-08-15"},
		{name: "past today", from: "2026-08-15", to: today},
		{name: "90 days", from: "2026-06-20", to: "2026-07-20", offsets: 90},
		{name: "30 days", from: "2026-06-20", to: "2026-07-20", offsets: 30},
		{name: "earlier today", from: "2026-07-01", to: "2026-08-10", today: "2026-08-20"},
		{name: "filtered", from: "2026-06-15", to: "2026-08-15",
			where: []Filter{{Column: "country", Op: "eq", Values: []string{"DE"}}, {Column: "event_name", Op: "ne", Values: []string{"signup"}}},
			sql:   `AND "country" = 'DE' AND "event_name" <> 'signup'`},
		{name: "empty range", from: "2026-05-01", to: "2026-05-10"},
	} {
		t.Run(c.name, func(t *testing.T) {
			now := c.today
			if now == "" {
				now = today
			}
			offsets := c.offsets
			if offsets == 0 {
				offsets = 45
			}
			clock := date(t, now).Time().Add(13 * time.Hour)
			e := New(Options{Dir: f.Dir, Workers: 3, Now: func() time.Time { return clock }})
			p := Params{Project: 1, From: date(t, c.from), To: date(t, c.to)}
			run := func(q Query, file string) {
				t.Helper()
				q.Kind, q.Offsets, q.Where = "retention", c.offsets, c.where
				res, err := e.Answer(context.Background(), q, p, engineRules)
				if err != nil {
					t.Fatalf("%s: %v", file, err)
				}
				if !slices.Equal(res.Columns, q.Columns()) {
					t.Errorf("%s: columns %v, want %v", file, res.Columns, q.Columns())
				}
				want := duckTable(t, f, retentionSQL(t, file, now, offsets, c.sql), c.from, c.to)
				t.Logf("%s: %d rows", file, len(want))
				if len(want) == 0 && c.name != "empty range" {
					t.Fatalf("%s: no rows: a weak case", file)
				}
				sameTable(t, res.Rows, want)
			}
			run(Query{Output: "grid"}, "cohorts")
			run(Query{Output: "curve", IdentifiedBy: "install"}, "install-retention")
			run(Query{Output: "curve", IdentifiedBy: "user"}, "signed-in-retention")
			if offsets >= 45 {
				run(Query{Output: "milestones", IdentifiedBy: "install"}, "install-milestones")
				run(Query{Output: "milestones", IdentifiedBy: "user"}, "signed-in-milestones")
			}
		})
	}
}

// An install that signs in on its fourth day counts as an install on days
// 0 to 2 and as a user from day 3, in its install cohort, whose size it
// stays in; a user cohort's size holds only those users on their first
// day. Retention is 0 over no cohort, and milestones read empty past the
// curve.
func TestRetentionSignsIn(t *testing.T) {
	f := archivetest.New(t)
	at := func(day string) time.Time { return date(t, day).Time().Add(9 * time.Hour) }
	view := func(id, actor string, kind string, day string) store.Event {
		return store.Event{ID: id, ProjectID: 1, Family: store.FamilyViews, EventName: "pageview", ActorID: actor,
			ActorKind: kind, Host: "example.com", TS: at(day)}
	}
	f.Write(t,
		view("a0", "a", store.ActorInstall, "2026-09-01"),
		view("a1", "a", store.ActorInstall, "2026-09-02"),
		view("a3", "a", store.ActorUser, "2026-09-04"),
		view("a5", "a", store.ActorInstall, "2026-09-06"), // after signing in: still a user
		view("b0", "b", store.ActorInstall, "2026-09-01"),
		view("u0", "u", store.ActorUser, "2026-09-02"),
		view("u1", "u", store.ActorUser, "2026-09-03"),
		view("t0", "t", store.ActorUser, today)) // today: left out
	e := New(Options{Dir: f.Dir, Now: func() time.Time { return at(today) }})
	p := Params{Project: 1, From: date(t, "2026-09-01"), To: date(t, "2026-09-09")}
	answer := func(q Query) [][]string {
		t.Helper()
		q.Kind = "retention"
		q.Offsets = 7
		res, err := e.Answer(context.Background(), q, p, archive.Rules{})
		if err != nil {
			t.Fatal(err)
		}
		return res.Rows
	}
	grid := answer(Query{Output: "grid"})
	wantGrid := [][]string{
		{"2026-09-02", "user", "0", "1", "1", "1"},
		{"2026-09-02", "user", "1", "1", "1", "1"},
		{"2026-09-01", "install", "0", "2", "2", "1"},
		{"2026-09-01", "install", "1", "2", "1", "0.5"},
	}
	if fmt.Sprint(grid) != fmt.Sprint(wantGrid) {
		t.Errorf("grid %q, want %q (a's user days have no user cohort of 2026-09-01)", grid, wantGrid)
	}
	if got := answer(Query{Output: "curve", IdentifiedBy: "install"}); fmt.Sprint(got) != fmt.Sprint([][]string{
		{"0", "1"}, {"1", "0.5"}, {"2", "0"}, {"3", "0"}, {"4", "0"}, {"5", "0"}, {"6", "0"}, {"7", "0"}}) {
		t.Errorf("install curve %q", got)
	}
	// The user cohort of 2026-09-02 has reached offsets 0 to 7 (9 is today).
	if got := answer(Query{Output: "curve", IdentifiedBy: "user"}); len(got) != 8 || fmt.Sprint(got[:3]) != fmt.Sprint([][]string{{"0", "1"}, {"1", "1"}, {"2", "0"}}) {
		t.Errorf("user curve %q", got)
	}
	if got := answer(Query{Output: "milestones", IdentifiedBy: "user", Milestones: []int{1, 7}}); fmt.Sprint(got) != fmt.Sprint([][]string{{"1", "0"}}) {
		t.Errorf("user milestones %q", got)
	}
	// No cohort: no grid row, and one row of empty milestones.
	p.From = date(t, "2026-09-08")
	if got := answer(Query{Output: "milestones", IdentifiedBy: "install", Milestones: []int{0, 2}}); fmt.Sprint(got) != fmt.Sprint([][]string{{"", ""}}) {
		t.Errorf("milestones over no cohort %q, want one row of empties", got)
	}
	if got := answer(Query{Output: "grid"}); len(got) != 0 {
		t.Errorf("grid over no cohort %q", got)
	}
}

// A retention query's per-actor state is held to MaxBytes.
func TestRetentionTooLarge(t *testing.T) {
	f := retentionFixture(t)
	e := New(Options{Dir: f.Dir, MaxBytes: 1 << 10, Now: func() time.Time { return date(t, today).Time() }})
	_, err := e.Answer(context.Background(), Query{Kind: "retention", Output: "grid"},
		Params{Project: 1, From: date(t, retentionFrom), To: date(t, today)}, engineRules)
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}
