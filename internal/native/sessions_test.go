package native

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// sessionsProject is the sessions fixture's project.
const sessionsProject = 3

// sessionDay is one day of project 3's events, built to exercise the
// session rules: an undeclared run split by a 40-minute silence and kept
// whole across 1800.2 seconds (1800 whole ones); declared session ids
// (one of them "2") interleaved with undeclared views of the same actor;
// a view at 23:50 and one at 00:10 (midnight cuts them); one actor id
// under two kinds; two views in one second whose ids order them; views
// without an actor; a product event between views, which no session
// counts. late marks a late write's ids.
func sessionDay(t testing.TB, day, late string, salt int) []store.Event {
	t.Helper()
	base := date(t, day).Time()
	var out []store.Event
	n := 0
	add := func(actor, kind, sid, path string, at time.Duration, edit func(*store.Event)) {
		e := store.Event{ID: fmt.Sprintf("s-%s-%03d%s", day, n, late), ProjectID: sessionsProject, Family: store.FamilyViews,
			EventName: "pageview", TS: base.Add(at), Kind: kind, ActorID: actor, ActorKind: "install", SessionID: sid,
			Host: "example.com", Path: path}
		n++
		if edit != nil {
			edit(&e)
		}
		out = append(out, e)
	}
	hm := func(h, m int) time.Duration { return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute }
	ref := func(src, utm string) func(*store.Event) {
		return func(e *store.Event) {
			e.ReferrerSource, e.UTMSource, e.UTMMedium, e.UTMCampaign = src, utm, "cpc", "c"+utm
		}
	}
	if late != "" {
		add("x0", "web", "", "/late", hm(11, 26), nil)
		add("x9", "web", "", "/new", hm(12, 0), ref("bing", ""))
		return out
	}
	add("x0", "web", "", "/a", hm(10, 0)+700*time.Millisecond, ref("google", "news"))
	add("x0", "web", "", "/b", hm(10, 10), nil)
	add("x0", "web", "", "/c", hm(10, 40)+200*time.Millisecond, nil)
	add("x0", "web", "", "/d", hm(11, 20), func(e *store.Event) { e.ActorKind = "user" })
	add("x0", "web", "", "/e", hm(11, 25), nil)
	out = append(out, store.Event{ID: fmt.Sprintf("s-%s-p", day), ProjectID: sessionsProject, Family: store.FamilyProduct,
		EventName: "click", TS: base.Add(hm(10, 30)), Kind: "web", ActorID: "x0", Path: "/product"})

	add("x1", "web", "s1", "/s1a", hm(10, 0), ref("", "mail"))
	add("x1", "web", "", "/u1", hm(10, 2), nil)
	add("x1", "web", "s1", "/s1b", hm(10, 5), nil)
	add("x1", "web", "", "/u2", hm(10, 20), nil)
	add("x1", "web", "2", "/d2", hm(12, 0), nil)
	add("x1", "web", "", "/u3", hm(13, 0), nil)
	add("x1", "web", "2", "/d2b", hm(13, 10), nil)

	add("x2", "web", "", "/early", hm(0, 10), nil)
	add("x2", "web", "", "/late", hm(23, 50), func(e *store.Event) { e.Host = "m.example.com" })

	add("x3", "web", "", "/w", hm(9, 0), nil)
	add("x3", "app", "", "/app", hm(9, 5), func(e *store.Event) { e.Host = "" })

	// One second, two views: the ids put /first first, whatever the
	// order written.
	add("x4", "web", "", "/second", hm(15, 0), func(e *store.Event) { e.ID = fmt.Sprintf("s-%s-x4-b", day) })
	add("x4", "web", "", "/first", hm(15, 0), func(e *store.Event) { e.ID = fmt.Sprintf("s-%s-x4-a", day) })

	add("", "web", "", "/anon", hm(14, 0), func(e *store.Event) { e.ActorKind = "" })
	add("", "web", "", "/anon2", hm(14, 10), func(e *store.Event) { e.ActorKind = "" })

	if salt%2 == 0 {
		add("x5", "cli", "", "/x5", hm(16, 0), ref("duck", "x"))
	}
	return out
}

// sessionsFixture writes project 3's days August 29 to September 5 and
// runs the daily pass as of today, so August is a month file with its
// session file, and September 1 to 5 day files with theirs. Then:
//   - a late log on September 2 (a gap over a current session file);
//   - September 4's session file deleted (a gap read from its day file);
//   - September 6 to 9 and today written to logs, never dumped (gaps);
//
// so September 1, 3 and 5 alone are read from session files.
func sessionsFixture(t testing.TB) *archivetest.Files {
	t.Helper()
	f := archivetest.New(t)
	day := func(d int) string { return date(t, "2026-08-29").Time().AddDate(0, 0, d).Format(time.DateOnly) }
	for d := range 8 {
		f.Write(t, sessionDay(t, day(d), "", d)...)
	}
	f.Maintain(t, today)
	f.Write(t, sessionDay(t, "2026-09-02", "-late", 0)...)
	pdir := filepath.Join(f.Dir, strconv.Itoa(sessionsProject))
	if err := os.Remove(filepath.Join(pdir, "sessions", "2026-09-04.parquet")); err != nil {
		t.Fatal(err)
	}
	for d := 8; d < 13; d++ {
		f.Write(t, sessionDay(t, day(d), "", d)...)
	}
	all, err := archive.ListFiles(f.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range all[sessionsProject].Sessions {
		names = append(names, filepath.Base(s))
	}
	if want := []string{"2026-08.parquet", "2026-09-01.parquet", "2026-09-03.parquet", "2026-09-05.parquet"}; !slices.Equal(names, want) {
		t.Fatalf("session files %q, want %q", names, want)
	}
	gaps := all[sessionsProject].SessionGaps
	if want := []archive.Span{{From: "2026-09-02", To: "2026-09-02"}, {From: "2026-09-04", To: "2026-09-04"},
		{From: "2026-09-06", To: today}}; !slices.Equal(gaps, want) {
		t.Fatalf("session gaps %v, want %v", gaps, want)
	}
	return f
}

// sessionColumnList is the sessions table's columns, comma-separated, as
// the goldens' SQL keys name them.
var sessionColumnList = `"` + strings.Join(sessionColumns, `", "`) + `"`

// sessionRows reads project's sessions of [from, to] with every column,
// formatted as the goldens hold them (a timestamp in RFC 3339), sorted.
func sessionRows(t *testing.T, e *Engine, project int64, from, to string) [][]string {
	t.Helper()
	var mu sync.Mutex
	var rows [][]string
	err := e.scanSessions(context.Background(), project, rng(t, from, to), sessionColumns, func(int64) error { return nil },
		func(_ int, b *Batch) error {
			mu.Lock()
			defer mu.Unlock()
			for i := range b.N {
				row := make([]string, len(sessionColumns))
				for j, name := range sessionColumns {
					_, c := b.col(name)
					if c == nil {
						t.Errorf("batch without %s", name)
						continue
					}
					row[j] = cellText(c, i, name == "day")
				}
				rows = append(rows, row)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	sortRows(rows)
	return rows
}

// cellText is c's row i as text, as the goldens keep the values.
func cellText(c *Column, i int, day bool) string {
	switch {
	case c.Idx != nil:
		if c.Idx[i] < 0 {
			return ""
		}
		return c.Dict[c.Idx[i]]
	case c.Null != nil && c.Null[i]:
		return ""
	case c.Times != nil:
		return time.UnixMicro(c.Times[i]).UTC().Format(time.RFC3339Nano)
	case c.Floats != nil:
		return strconv.FormatFloat(c.Floats[i], 'f', -1, 64)
	case day:
		return formatDay(c.Ints[i])
	}
	return strconv.FormatInt(c.Ints[i], 10)
}

func sortRows(rows [][]string) {
	slices.SortFunc(rows, func(a, b []string) int { return slices.Compare(a, b) })
}

func TestSessionsRowsMatchGolden(t *testing.T) {
	f := sessionsFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	for _, c := range []struct{ name, from, to string }{
		{"session files only", "2026-08-29", "2026-09-01"},
		{"one day of a month file", "2026-08-30", "2026-08-30"},
		{"gaps only", "2026-09-06", today},
		{"late log over a current file", "2026-09-02", "2026-09-02"},
		{"missing session file", "2026-09-04", "2026-09-04"},
		{"both", "2026-08-29", today},
		{"none", "2026-07-01", "2026-07-31"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := sessionRows(t, e, sessionsProject, c.from, c.to)
			want := goldenTable(t, `SELECT `+sessionColumnList+` FROM sessions
				WHERE "project_id" = 3 AND "day" BETWEEN DATE '%[1]s' AND DATE '%[2]s'`, c.from, c.to, got)
			sortRows(want)
			if c.name != "none" && len(want) == 0 {
				t.Fatal("the golden has no sessions: a weak case")
			}
			sameTable(t, got, want)
		})
	}
	// The rules the fixture is built for, read off the sessions per actor.
	perActor := map[string]int{}
	actor := slices.Index(sessionColumns, "actor_id")
	for _, r := range sessionRows(t, e, sessionsProject, "2026-09-03", "2026-09-03") {
		perActor[r[actor]]++
	}
	var got [][]string
	for a, n := range perActor {
		got = append(got, []string{a, strconv.Itoa(n)})
	}
	sortRows(got)
	rows := goldenTable(t, `SELECT "actor_id", count(*) FROM sessions WHERE "project_id" = 3 AND "day" = DATE '%[1]s'
		GROUP BY ALL`, "2026-09-03", "", got)
	sortRows(rows)
	sameTable(t, got, rows)
	if want := [][]string{{"", "1"}, {"x0", "2"}, {"x1", "4"}, {"x2", "2"}, {"x3", "2"}, {"x4", "1"}}; !slices.EqualFunc(rows, want, slices.Equal) {
		t.Errorf("sessions per actor on September 3: %q, want %q", rows, want)
	}
}

// TestSessionsStaleMonthMatchesGolden: a month's session file older than
// its events file (the month merged again after it was derived) is stale,
// so the month is derived from its events, alone or beside the next one.
func TestSessionsStaleMonthMatchesGolden(t *testing.T) {
	f := sessionsFixture(t)
	pdir := filepath.Join(f.Dir, strconv.Itoa(sessionsProject))
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(pdir, "2026-08.parquet"), later, later); err != nil {
		t.Fatal(err)
	}
	all, err := archive.ListFiles(f.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all[sessionsProject].Sessions {
		if filepath.Base(s) == "2026-08.parquet" {
			t.Fatalf("session file %s is current", s)
		}
	}
	e := New(Options{Dir: f.Dir, Workers: 3})
	for _, c := range []struct{ name, from, to string }{
		{"the month alone", "2026-08-01", "2026-08-31"},
		{"across the month's end", "2026-08-30", "2026-09-03"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := sessionRows(t, e, sessionsProject, c.from, c.to)
			want := goldenTable(t, `SELECT `+sessionColumnList+` FROM sessions
				WHERE "project_id" = 3 AND "day" BETWEEN DATE '%[1]s' AND DATE '%[2]s'`, c.from, c.to, got)
			sortRows(want)
			if len(want) == 0 {
				t.Fatal("the golden has no sessions: a weak case")
			}
			sameTable(t, got, want)
		})
	}
}

func TestSessionsQueriesMatchGolden(t *testing.T) {
	f := sessionsFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 2})
	const from, to = "2026-08-29", today
	const in = `"project_id" = 3 AND "day" BETWEEN DATE '%[1]s' AND DATE '%[2]s'`
	// The metrics of sessions, as the built-in widgets read them.
	metrics := []Metric{{Op: "count"}, {Op: "bounces"}, {Op: "bounce_rate"}, {Op: "avg", Column: "duration_sec"},
		{Op: "sum", Column: "views"}, {Op: "min", Column: "duration_sec"}, {Op: "max", Column: "views"}}
	metricCols := []string{"count", "bounces", "bounce_rate", "avg_duration_sec", "sum_views", "min_duration_sec", "max_views"}
	const metricSQL = `count(*), count(*) FILTER (WHERE "views" = 1),
		CASE WHEN count(*) > 0 THEN count(*) FILTER (WHERE "views" = 1)::DOUBLE / count(*) END,
		avg("duration_sec"), sum("views"), min("duration_sec"), max("views")`
	// byRate ranks referrers by bounce rate, ties by the referrer.
	const byRate = `WITH g AS (SELECT "referrer_source" AS r, count(*) FILTER (WHERE "views" = 1)::DOUBLE / count(*) AS b,
				count(*) FILTER (WHERE "views" = 1) AS n, count(*) AS c
			FROM sessions WHERE ` + in + ` GROUP BY ALL),
		top AS (SELECT * FROM g ORDER BY b DESC, r ASC NULLS LAST LIMIT 2) `
	referred := []Filter{{Column: "referrer_source", Op: "is_set"}}
	cases := []struct {
		name     string
		q        Query
		cols     []string
		sql      string
		from, to string
	}{
		{name: "stat session metrics", q: Query{Kind: "stat", Table: "sessions", Metrics: metrics}, cols: metricCols,
			sql: `SELECT ` + metricSQL + ` FROM sessions WHERE ` + in},
		{name: "stat empty", q: Query{Kind: "stat", Table: "sessions", Metrics: metrics}, cols: metricCols,
			sql: `SELECT ` + metricSQL + ` FROM sessions WHERE ` + in, from: "2026-07-01", to: "2026-07-05"},
		{name: "stat compare", q: Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "count"}, {Op: "bounce_rate"}}, Compare: "previous"},
			cols: []string{"count", "bounce_rate", "previous_count", "previous_bounce_rate"}, from: "2026-09-03", to: "2026-09-06",
			sql: `WITH c AS (SELECT count(*) AS n, count(*) FILTER (WHERE "views" = 1)::DOUBLE / count(*) AS r FROM sessions WHERE ` + in + `),
				p AS (SELECT count(*) AS n, count(*) FILTER (WHERE "views" = 1)::DOUBLE / count(*) AS r FROM sessions
					WHERE "project_id" = 3 AND "day" BETWEEN DATE '2026-08-30' AND DATE '2026-09-02')
				SELECT c.n, c.r, p.n, p.r FROM c, p`},
		{name: "trend bounce rate", q: Query{Kind: "trend", Table: "sessions", Metrics: []Metric{{Op: "bounce_rate"}}},
			cols: []string{"day", "bounce_rate"},
			sql:  `SELECT "day", count(*) FILTER (WHERE "views" = 1)::DOUBLE / count(*) FROM sessions WHERE ` + in + ` GROUP BY "day" ORDER BY "day"`},
		{name: "breakdown entry path", q: Query{Kind: "breakdown", Table: "sessions", Metrics: count(), Dimensions: []string{"entry_path"}},
			cols: []string{"entry_path", "count"},
			sql: `SELECT "entry_path", count(*) AS c FROM sessions WHERE ` + in + `
				GROUP BY ALL ORDER BY c DESC, "entry_path" ASC NULLS LAST LIMIT 20`},
		{name: "breakdown bounce rate other", q: Query{Kind: "breakdown", Table: "sessions",
			Metrics: []Metric{{Op: "bounce_rate"}, {Op: "bounces"}, {Op: "count"}}, Dimensions: []string{"referrer_source"}, Limit: 2, Other: true},
			cols: []string{"referrer_source", "bounce_rate", "bounces", "count"},
			sql: byRate + `SELECT r, b, n, c FROM (
					SELECT 0 AS o, r, b, n, c FROM top
					UNION ALL
					SELECT 1, 'Other', count(*) FILTER (WHERE "views" = 1)::DOUBLE / count(*), count(*) FILTER (WHERE "views" = 1), count(*)
					FROM sessions s WHERE ` + in + ` AND NOT EXISTS (SELECT 1 FROM top WHERE top.r IS NOT DISTINCT FROM s."referrer_source")
					HAVING count(*) > 0)
				ORDER BY o, b DESC, r ASC NULLS LAST`},
		{name: "breakdown referred bounces", q: Query{Kind: "breakdown", Table: "sessions", Dimensions: []string{"entry_path"}, Limit: 100,
			Metrics: []Metric{{Op: "count"}, {Op: "bounce_rate", As: "rb", Round: ptr(2), Where: referred},
				{Op: "bounces", As: "b", Where: referred}, {Op: "avg", Column: "duration_sec", As: "d", Where: referred}}},
			cols: []string{"entry_path", "count", "rb", "b", "d"},
			sql: `SELECT "entry_path", count(*) AS c,
				ROUND(CASE WHEN count(*) FILTER (WHERE "referrer_source" <> '') > 0 THEN
					count(*) FILTER (WHERE "views" = 1 AND "referrer_source" <> '')::DOUBLE / count(*) FILTER (WHERE "referrer_source" <> '') END, 2),
				count(*) FILTER (WHERE "views" = 1 AND "referrer_source" <> ''), avg("duration_sec") FILTER (WHERE "referrer_source" <> '')
				FROM sessions WHERE ` + in + ` GROUP BY ALL ORDER BY c DESC, "entry_path" ASC NULLS LAST LIMIT 100`},
		{name: "stat", q: Query{Kind: "stat", Table: "sessions", Metrics: []Metric{{Op: "count"}, {Op: "actors"}}},
			cols: []string{"count", "actors"}, sql: `SELECT count(*), count(DISTINCT "actor_id") FROM sessions WHERE ` + in},
		{name: "trend", q: Query{Kind: "trend", Table: "sessions", Metrics: count()}, cols: []string{"day", "count"},
			sql: `SELECT "day", count(*) FROM sessions WHERE ` + in + ` GROUP BY "day" ORDER BY "day"`},
		{name: "trend by kind", q: Query{Kind: "trend", Table: "sessions", Metrics: []Metric{{Op: "actors"}}, Series: "kind"},
			cols: []string{"day", "series", "actors"},
			sql:  `SELECT "day", "kind", count(DISTINCT "actor_id") FROM sessions WHERE ` + in + ` GROUP BY ALL ORDER BY "day", "kind"`},
		{name: "breakdown", q: Query{Kind: "breakdown", Table: "sessions", Metrics: []Metric{{Op: "count"}, {Op: "actors"}},
			Dimensions: []string{"entry_path", "exit_path"}, Limit: 100}, cols: []string{"entry_path", "exit_path", "count", "actors"},
			sql: `SELECT "entry_path", "exit_path", count(*) AS c, count(DISTINCT "actor_id") FROM sessions WHERE ` + in + `
				GROUP BY ALL ORDER BY c DESC, "entry_path" ASC NULLS LAST, "exit_path" ASC NULLS LAST LIMIT 100`},
		{name: "filtered", q: Query{Kind: "breakdown", Table: "sessions", Metrics: count(), Dimensions: []string{"referrer_source", "day"},
			Where: []Filter{{Column: "views", Op: "gt", Values: []string{"1"}}, {Column: "duration_sec", Op: "lte", Values: []string{"1800"}},
				{Column: "session_id", Op: "ne", Values: []string{"2"}}, {Column: "entry_host", Op: "is_set"}}, Limit: 100},
			cols: []string{"referrer_source", "day", "count"},
			sql: `SELECT "referrer_source", "day", count(*) AS c FROM sessions WHERE ` + in + ` AND "views" > 1 AND "duration_sec" <= 1800
				AND "session_id" <> '2' AND "entry_host" <> ''
				GROUP BY ALL ORDER BY c DESC, "referrer_source" ASC NULLS LAST, "day" ASC LIMIT 100`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from, to := from, to
			if c.from != "" {
				from, to = c.from, c.to
			}
			res, err := e.Answer(context.Background(), c.q, Params{Project: sessionsProject, From: date(t, from), To: date(t, to)}, engineRules)
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
}

// TestSessionsDerivationCharged: the views a gap day's derivation holds
// count against MaxBytes, so a stat over a gap is refused past it.
func TestSessionsDerivationCharged(t *testing.T) {
	f := sessionsFixture(t)
	e := New(Options{Dir: f.Dir, MaxBytes: 1 << 10})
	_, err := e.Answer(context.Background(), Query{Kind: "stat", Table: "sessions", Metrics: count()},
		Params{Project: sessionsProject, From: date(t, "2026-09-06"), To: date(t, today)}, archive.Rules{})
	if err == nil || !strings.Contains(err.Error(), ErrTooLarge.Error()) {
		t.Fatalf("err %v, want ErrTooLarge", err)
	}
	var charged, peak atomic.Int64
	if err := e.scanSessions(context.Background(), sessionsProject, rng(t, "2026-09-06", today), sessionColumns,
		func(n int64) error {
			if v := charged.Add(n); v > peak.Load() {
				peak.Store(v)
			}
			return nil
		}, func(int, *Batch) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if n := charged.Load(); n != 0 || peak.Load() == 0 {
		t.Errorf("%d bytes left charged after the scan (peak %d), want 0 (peak above 0)", n, peak.Load())
	}
}
