package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TestViewsLiveHalvesUseTheDayIndex is the physical-plan regression for the
// day-index perf change. It asserts two things for the live halves of
// v_views_paths and v_views_daily:
//
//  1. Nothing in the plan touches idx_views_project_ts any more -- before
//     the day column existed, that was the only index available, and every
//     access via it either ignored the day range entirely or (for the raw
//     row scan feeding COUNT(DISTINCT actor_id)/session detection) applied
//     only the project filter.
//  2. At least one access is a SEARCH on idx_views_project_day carrying an
//     actual day bound (">", "<" or "="), not just "(project=?)". That is
//     the raw-row scan driving each live half (the one EXPLAIN labels "v"),
//     and it is the dominant cost on a large raw table: BenchmarkViewsPathsLiveHalf
//     and BenchmarkViewsDailyLiveHalf in bench_test.go show the wall-clock
//     effect (~13%/~32% faster on 150k rows -- see the day-index report).
//
// It deliberately does NOT assert "no SCAN views at all". One SCAN survives
// in every dimension view and in v_views_daily: the ranking subquery that
// computes each day's top-500 cap (aliased "r"/"k" in 012_views.sql) is
// joined to raw rows, and separately verified (see the day-index report)
// to remain an un-day-bounded index scan under every formulation tried --
// including one with no join at all, using COUNT(*) OVER/DENSE_RANK()
// directly on `views`. The common factor is that this subquery is always
// the second arm of the view's `agg_* UNION ALL live-computation`
// structure (012_views.sql's own design, not something introduced here):
// SQLite's push-down-into-window-function-subquery optimization does not
// operate across a UNION ALL arm, so a WHERE term on the compound view
// never reaches a window function computed inside one of its arms, no
// matter how directly that arm's columns trace back to `views`. Removing
// that residual scan would mean giving up the aggregate/live UNION ALL
// shape these views are built on -- out of scope for this change.
func TestViewsLiveHalvesUseTheDayIndex(t *testing.T) {
	db := newTestDB(t)
	seedViewDay(t, db) // project 1, day 2026-08-10

	queries := map[string]string{
		"paths": `SELECT path, SUM(visitors), SUM(views) FROM v_views_paths
			WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY path`,
		"daily": `SELECT kind, SUM(visitors), SUM(views) FROM v_views_daily
			WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY kind`,
	}

	for name, q := range queries {
		t.Run(name, func(t *testing.T) {
			rows, err := db.db.Query("EXPLAIN QUERY PLAN "+q, 1, "2026-08-01", "2026-08-10")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var details []string
			for rows.Next() {
				var id, parent, notUsed int
				var detail string
				if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
					t.Fatal(err)
				}
				details = append(details, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			sawDayBoundSearch := false
			for _, d := range details {
				if strings.Contains(d, "idx_views_project_ts") {
					t.Errorf("%s: plan still uses idx_views_project_ts, the day-oblivious index", name)
				}
				if strings.Contains(d, "SEARCH v USING INDEX idx_views_project_day") &&
					(strings.Contains(d, "day>") || strings.Contains(d, "day<") || strings.Contains(d, "day=")) {
					sawDayBoundSearch = true
				}
			}
			if !sawDayBoundSearch {
				t.Errorf("%s: no access searches idx_views_project_day bounded by day", name)
			}
			if t.Failed() {
				for _, d := range details {
					t.Logf("plan: %s", d)
				}
			}
		})
	}
}

// The invariant that makes Evidence dashboards boundary-free (spec §8.1):
// v_* views must return IDENTICAL numbers before and after aggregation.
func TestStitchViewsInvariantDaily(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViewDay(t, db) // raw only

	read := func(kind string) dailyRow { return readDaily(t, db, "v_views_daily", kind) }
	webBefore, appBefore := read("web"), read("app")
	if webBefore != (dailyRow{2, 4, 3, 2, 600}) || appBefore != (dailyRow{2, 3, 2, 1, 300}) {
		t.Fatalf("live v_views_daily web=%+v app=%+v; fixture expectations wrong", webBefore, appBefore)
	}
	if err := db.AggregateViewDay(ctx, 1, day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	if got := read("web"); got != webBefore {
		t.Errorf("web stitch mismatch: before %+v after %+v", webBefore, got)
	}
	if got := read("app"); got != appBefore {
		t.Errorf("app stitch mismatch: before %+v after %+v", appBefore, got)
	}
}

// Every dimension view must hold the invariant, including the cap and the
// "(other)" tail, or one dimension jumps the moment aggregation runs.
func TestStitchViewsInvariantAllViewsDimensions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViewDay(t, db)
	// Push one dimension past the cap so the other bucket is exercised on
	// both sides of the boundary.
	var extra []store.View
	for i := 0; i < topNDimension+5; i++ {
		extra = append(extra, store.View{ID: fmt.Sprintf("x-%d", i), TS: at(13, 0).Add(time.Duration(i) * time.Second),
			ActorID: "v3", Path: fmt.Sprintf("/x/%d", i), Platform: "web", OS: "linux", Browser: "firefox", BrowserVersion: "127", Device: "desktop"})
	}
	seedViews(t, db, extra...)

	type dim struct{ view, key string }
	dims := []dim{
		{"v_views_paths", "path"},
		{"v_views_hosts", "host"},
		{"v_views_referrers", "source"},
		{"v_views_utm", "utm_source || '|' || utm_medium || '|' || utm_campaign"},
		{"v_views_countries", "country"},
		{"v_views_platforms", "platform"},
		{"v_views_os", "os || '|' || os_version"},
		{"v_views_browsers", "browser || '|' || browser_version"},
		{"v_views_app_versions", "platform || '|' || app_version"},
		{"v_views_devices", "device || '|' || device_model"},
		{"v_views_displays", "display"},
	}
	snapshot := func(d dim) map[string][2]int {
		t.Helper()
		rows, err := db.db.Query(fmt.Sprintf(
			`SELECT %s, visitors, views FROM %s WHERE project_id=1 AND day='2026-08-10'`, d.key, d.view))
		if err != nil {
			t.Fatalf("%s: %v", d.view, err)
		}
		defer rows.Close()
		out := map[string][2]int{}
		for rows.Next() {
			var k string
			var v, pv int
			if err := rows.Scan(&k, &v, &pv); err != nil {
				t.Fatal(err)
			}
			out[k] = [2]int{v, pv}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := map[string]map[string][2]int{}
	for _, d := range dims {
		before[d.view] = snapshot(d)
		if len(before[d.view]) == 0 {
			t.Fatalf("%s returned no live rows; invariant check would be vacuous", d.view)
		}
	}
	if _, ok := before["v_views_paths"]["(other)"]; !ok {
		t.Fatal("paths fixture did not exceed the cap; the other-bucket parity is untested")
	}
	if err := db.AggregateViewDay(ctx, 1, day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	for _, d := range dims {
		after := snapshot(d)
		if !reflect.DeepEqual(after, before[d.view]) {
			t.Errorf("%s: before %v, after %v", d.view, before[d.view], after)
		}
	}
}

// Views carrying no UTM parameters must not create an all-empty UTM row on
// either side of the boundary.
func TestStitchViewUTMExcludesEmpty(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViewDay(t, db)
	count := func() int {
		t.Helper()
		var n int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM v_views_utm
			WHERE project_id=1 AND day='2026-08-10'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Only visitor v2 carries UTM tags in the fixture.
	if n := count(); n != 1 {
		t.Fatalf("live v_views_utm rows = %d, want 1", n)
	}
	if err := db.AggregateViewDay(ctx, 1, day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("aggregated v_views_utm rows = %d, want 1", n)
	}
}

func TestStitchViewsInvariantProduct(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	read := func() (c, u int) {
		t.Helper()
		err := db.db.QueryRow(`SELECT count, unique_users FROM v_product_daily
			WHERE project_id=1 AND day='2026-08-10' AND event_name='subscribed'`).Scan(&c, &u)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	c1, u1 := read()
	if c1 != 3 || u1 != 2 {
		t.Fatalf("live v_product_daily subscribed = (%d,%d), want (3,2)", c1, u1)
	}
	if err := db.AggregateProductDay(ctx, 1, day("2026-08-10"), nil, 50); err != nil {
		t.Fatal(err)
	}
	c2, u2 := read()
	if c1 != c2 || u1 != u2 {
		t.Fatalf("product stitch mismatch: (%d,%d) vs (%d,%d)", c1, u1, c2, u2)
	}
	var dau int
	if err := db.db.QueryRow(`SELECT active_users FROM v_product_totals
		WHERE project_id=1 AND day='2026-08-10'`).Scan(&dau); err != nil {
		t.Fatal(err)
	}
	if dau != 2 {
		t.Fatalf("dau = %d", dau)
	}
}

// v_product_totals must report true DAU, not the sum of per-event uniques:
// u1 and u2 both appear under more than one event name in the fixture.
func TestStitchViewProductTotalsIsTrueDAU(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	read := func() (events, users int) {
		t.Helper()
		if err := db.db.QueryRow(`SELECT total_events, active_users FROM v_product_totals
			WHERE project_id=1 AND day='2026-08-10'`).Scan(&events, &users); err != nil {
			t.Fatal(err)
		}
		return
	}
	e1, u1 := read()
	if e1 != 4 || u1 != 2 {
		t.Fatalf("live v_product_totals = (%d,%d), want (4,2)", e1, u1)
	}
	if err := db.AggregateProductDay(ctx, 1, day("2026-08-10"), nil, 50); err != nil {
		t.Fatal(err)
	}
	if e2, u2 := read(); e1 != e2 || u1 != u2 {
		t.Fatalf("totals stitch mismatch: (%d,%d) vs (%d,%d)", e1, u1, e2, u2)
	}
}

// Days on either side of the boundary must coexist without double counting:
// one aggregated day plus one still-raw day yields exactly two rows.
func TestStitchViewsMixedAggregatedAndRawDays(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViewDay(t, db) // 2026-08-10
	if err := db.WriteViews(ctx, []store.View{
		{ID: "9", ProjectID: 1, TS: ts("2026-08-11T10:00:00Z"), Kind: "web",
			ActorKind: store.ActorConnection, ActorID: "v9", Path: "/a"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateViewDay(ctx, 1, day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.Query(`SELECT day, views FROM v_views_daily
		WHERE project_id=1 AND kind='web' ORDER BY day`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var d string
		var pv int
		if err := rows.Scan(&d, &pv); err != nil {
			t.Fatal(err)
		}
		if _, dup := got[d]; dup {
			t.Fatalf("day %s appears twice: raw and aggregate are both contributing", d)
		}
		got[d] = pv
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["2026-08-10"] != 4 || got["2026-08-11"] != 1 {
		t.Fatalf("v_views_daily = %v, want {2026-08-10:4 2026-08-11:1}", got)
	}
}

// The daily pass only rolls up days that have aged out of the raw window, so
// the identity stitch view must serve recent days from raw or the users and
// groups pages would be blank for the whole window.
func TestStitchViewIdentityDailyCoversRawDays(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tsV := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	if err := db.WriteViews(ctx, []store.View{
		{ID: "1", ProjectID: 1, TS: tsV, ReceivedAt: tsV, Kind: "app", ActorKind: store.ActorInstall, ActorID: "a",
			UserID: "u1", GroupID: "org9", Path: "/x"},
		{ID: "2", ProjectID: 1, TS: tsV, ReceivedAt: tsV, Kind: "app", ActorKind: store.ActorInstall, ActorID: "b",
			UserID: "u2", GroupID: "org9", Path: "/x"},
	}); err != nil {
		t.Fatal(err)
	}

	var actors, users, views int
	if err := db.db.QueryRowContext(ctx,
		`SELECT actors, users, views FROM v_identity_daily
		 WHERE project_id=1 AND kind='group' AND id='org9'`).
		Scan(&actors, &users, &views); err != nil {
		t.Fatalf("group row before aggregation: %v", err)
	}
	if actors != 2 || users != 2 || views != 2 {
		t.Errorf("live group row = actors %d users %d views %d; want 2 2 2", actors, users, views)
	}

	// After aggregation the same figures must come from the aggregate half,
	// with no double counting from the raw rows the pass deletes.
	if err := db.AggregateIdentityDay(ctx, 1, day("2026-08-23")); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateViewDay(ctx, 1, day("2026-08-23")); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM v_identity_daily WHERE project_id=1 AND kind='group' AND id='org9'`).
		Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("group rows after aggregation = %d, want exactly 1 (no double count)", rows)
	}
	if err := db.db.QueryRowContext(ctx,
		`SELECT actors, users, views FROM v_identity_daily
		 WHERE project_id=1 AND kind='group' AND id='org9'`).
		Scan(&actors, &users, &views); err != nil {
		t.Fatal(err)
	}
	if actors != 2 || users != 2 || views != 2 {
		t.Errorf("aggregated group row = actors %d users %d views %d; want 2 2 2", actors, users, views)
	}
}

// The daily pass rolls identity days up while their raw rows are still there
// -- it runs over every raw day, not only aged-out ones -- so the view must
// not add the live half on top of a day that is already rolled up.
func TestStitchViewIdentityDailyDoesNotDoubleCountRetainedRawDays(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	if err := db.WriteViews(ctx, []store.View{
		{ID: "1", ProjectID: 1, TS: ts, ReceivedAt: ts, Kind: "web",
			ActorKind: store.ActorUser, ActorID: "a", UserID: "u1", GroupID: "org9", Path: "/x"},
		{ID: "2", ProjectID: 1, TS: ts, ReceivedAt: ts, Kind: "web",
			ActorKind: store.ActorUser, ActorID: "a", UserID: "u1", GroupID: "org9", Path: "/y"},
	}); err != nil {
		t.Fatal(err)
	}
	// AggregateIdentityDay does not delete raw rows; only AggregateViewDay
	// does, and the pass rolls identity up long before that.
	if err := db.AggregateIdentityDay(ctx, 1, day("2026-08-23")); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"user", "group"} {
		var rows, views int
		if err := db.db.QueryRowContext(ctx,
			`SELECT COUNT(*), COALESCE(SUM(views),0) FROM v_identity_daily
			 WHERE project_id=1 AND kind=?`, kind).Scan(&rows, &views); err != nil {
			t.Fatal(err)
		}
		if rows != 1 || views != 2 {
			t.Errorf("%s: rows %d views %d; want 1 row of 2 views", kind, rows, views)
		}
	}
}

// AggregateIdentityDay keeps the top topNDimension ids per kind and day, so
// the live half must rank and cut the same way or a busy project's figures
// jump when the day rolls up.
func TestStitchViewIdentityDailyCapsLikeTheAggregate(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	n := topNDimension + 5
	var views []store.View
	var events []store.ProductEvent
	for i := 0; i < n; i++ {
		u, g := fmt.Sprintf("u%03d", i), fmt.Sprintf("g%03d", i)
		views = append(views, store.View{ID: fmt.Sprintf("v%d", i), ProjectID: 1, TS: ts, ReceivedAt: ts,
			Kind: "web", ActorKind: store.ActorUser, ActorID: u, UserID: u, GroupID: g, Path: "/"})
		// The last ids sort after the cut by id alone; an extra product
		// event ranks them first, so only a count-ordered cap keeps them.
		if i >= n-5 {
			events = append(events, store.ProductEvent{ID: fmt.Sprintf("e%d", i), ProjectID: 1, EventName: "clicked",
				TS: ts, ReceivedAt: ts, ActorID: u, ActorKind: store.ActorUser, UserID: u, GroupID: g})
		}
	}
	if err := db.WriteViews(ctx, views); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteProductEvents(ctx, events); err != nil {
		t.Fatal(err)
	}

	snapshot := func() map[string][4]int {
		t.Helper()
		rows, err := db.db.QueryContext(ctx,
			`SELECT kind, id, actors, users, views, events FROM v_identity_daily WHERE project_id=1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string][4]int{}
		for rows.Next() {
			var kind, id string
			var r [4]int
			if err := rows.Scan(&kind, &id, &r[0], &r[1], &r[2], &r[3]); err != nil {
				t.Fatal(err)
			}
			out[kind+"|"+id] = r
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	live := snapshot()
	if err := db.AggregateIdentityDay(ctx, 1, day("2026-08-23")); err != nil {
		t.Fatal(err)
	}
	agg := snapshot()
	if len(agg) != 2*topNDimension {
		t.Fatalf("aggregate kept %d rows, want %d; the fixture does not exercise the cap", len(agg), 2*topNDimension)
	}
	if _, ok := agg[fmt.Sprintf("user|u%03d", n-1)]; !ok {
		t.Fatal("aggregate dropped a boosted user; the fixture does not exercise the ranking")
	}
	if !reflect.DeepEqual(live, agg) {
		t.Errorf("live half has %d rows, aggregate %d; they must match across the boundary", len(live), len(agg))
	}
}

// seedDeclaredProject registers a project row with a declared attribute
// list and returns its id. v_product_attrs' live half reads
// projects.attributes by project_id, so the row must exist or the declared
// half of the view is empty.
func seedDeclaredProject(t *testing.T, db *DB, attrs []string) int64 {
	t.Helper()
	if attrs == nil {
		attrs = []string{}
	}
	b, err := json.Marshal(attrs)
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.CreateProject(context.Background(), store.RegistryProject{
		Name: "Blog", AllowedOrigins: "[]", Attributes: string(b)},
		store.AuditEntry{Actor: "test", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// attrRow mirrors one v_product_attrs row for before/after comparison.
// Groups is NULL only for a day rolled up before migration 016; the live
// half always measures, so every row read here must be Valid.
type attrRow struct {
	Event, Key, Value string
	Count, Uniques    int
	Groups            sql.NullInt64
}

// readAttrs drains every v_product_attrs row for one project/day into a
// slice. It fully drains and closes the cursor before returning: the pool
// is SetMaxOpenConns(1), so holding rows open while the caller issues the
// next query would deadlock.
func readAttrs(t *testing.T, db *DB, projectID int64, day string) []attrRow {
	t.Helper()
	rows, err := db.db.Query(`SELECT event_name, attr_key, attr_value, count, unique_users, unique_groups
		FROM v_product_attrs WHERE project_id=? AND day=?
		ORDER BY event_name, attr_key, attr_value`, projectID, day)
	if err != nil {
		t.Fatal(err)
	}
	var out []attrRow
	for rows.Next() {
		var r attrRow
		if err := rows.Scan(&r.Event, &r.Key, &r.Value, &r.Count, &r.Uniques, &r.Groups); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// seedAttrDay writes 60 distinct "plan" values for one event on one day --
// more than the 50 cap, so the top-N cutoff and the "(other)" tail are both
// exercised. Counts vary (1..3) so the ranking is not a pure alphabetical
// tiebreak, and the four actors repeat across values so the tail's
// unique_users is strictly less than the sum of its per-value uniques --
// the exact case a summed "(other)" row would get wrong. Groups cycle
// through "", g1 and g2 by (i/3+n)%3, so the tail (the count-1 values
// p30..p57, i.e. i/3 in 10..19) holds empties as well as both groups:
// its distinct non-empty groups are 2 while its per-value sum is 7.
func seedAttrDay(t *testing.T, db *DB, projectID int64) {
	t.Helper()
	groups := []string{"", "g1", "g2"}
	var evs []store.ProductEvent
	id := 0
	for i := 0; i < 60; i++ {
		for n := 0; n <= i%3; n++ {
			id++
			evs = append(evs, store.ProductEvent{
				ID: fmt.Sprintf("e%04d", id), ProjectID: projectID, EventName: "signup",
				ActorID: fmt.Sprintf("a%d", (i+n)%4), GroupID: groups[(i/3+n)%3],
				TS:         ts("2026-08-01T10:00:00Z"),
				Attributes: map[string]string{"plan": fmt.Sprintf("p%02d", i)},
				OS:         []string{"ios", "android"}[i%2],
				AppVersion: []string{"1.0", "2.0", "3.0"}[i%3],
			})
		}
	}
	// A second event name, so the per-event partitioning is exercised too.
	evs = append(evs, store.ProductEvent{
		ID: "ping1", ProjectID: projectID, EventName: "ping", ActorID: "a9", GroupID: "g1",
		TS: ts("2026-08-01T11:00:00Z"), Attributes: map[string]string{"plan": "pro"},
		OS: "web", AppVersion: "1.0",
	})
	if err := db.WriteProductEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

// The binding invariant (002_views.sql:5-8) for the attribute breakdown:
// v_product_attrs must return identical rows before and after the day is
// aggregated, including the top-N cutoff, its count-desc/value-asc
// tiebreak, and the recomputed "(other)" tail.
func TestProductAttrsViewInvariant(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	id := seedDeclaredProject(t, db, []string{"plan"})
	seedAttrDay(t, db, id)

	before := readAttrs(t, db, id, "2026-08-01")
	if len(before) == 0 {
		t.Fatal("live v_product_attrs returned no rows; the comparison would be vacuous")
	}
	// Sanity: the cap and the tail must actually be in play, or the
	// interesting half of the invariant is untested.
	var plans, other int
	for _, r := range before {
		if r.Event == "signup" && r.Key == "plan" {
			plans++
			if r.Value == "(other)" {
				other = r.Uniques
			}
		}
	}
	if plans != 51 {
		t.Fatalf("live signup/plan rows = %d, want 51 (50 capped + one (other))", plans)
	}
	if other == 0 {
		t.Fatal("no (other) row: the tail path is untested")
	}
	// The live half must always measure: NULL is reserved for days rolled
	// up before 016, and a NULL here would make the before/after
	// comparison agree for the wrong reason once the rollup writes NULL too.
	for _, r := range before {
		if !r.Groups.Valid {
			t.Fatalf("live row %s/%s=%s has NULL unique_groups; the live half must always measure", r.Event, r.Key, r.Value)
		}
	}

	if err := db.AggregateProductDay(ctx, id,
		civil.DateOf(ts("2026-08-01T00:00:00Z")), []string{"plan"}, 50); err != nil {
		t.Fatal(err)
	}
	after := readAttrs(t, db, id, "2026-08-01")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("view changed when the day aggregated:\nbefore %v\nafter  %v", before, after)
	}
}

// The "(other)" row's unique_users and unique_groups must be fresh
// COUNT(DISTINCT ...) over the tail, not a sum of the per-value figures: an
// actor or a group appearing under several tail values would otherwise be
// counted once per value.
func TestProductAttrsViewOtherRecomputesUniques(t *testing.T) {
	db := newTestDB(t)
	id := seedDeclaredProject(t, db, []string{"plan"})
	seedAttrDay(t, db, id)
	var count, uniques int
	var groups sql.NullInt64
	if err := db.db.QueryRow(`SELECT count, unique_users, unique_groups FROM v_product_attrs
		WHERE project_id=? AND day='2026-08-01' AND event_name='signup'
		  AND attr_key='plan' AND attr_value='(other)'`, id).Scan(&count, &uniques, &groups); err != nil {
		t.Fatal(err)
	}
	if uniques >= count {
		t.Fatalf("(other) = count %d uniques %d; the fixture repeats actors across "+
			"tail values, so uniques must be strictly smaller than a summed count",
			count, uniques)
	}
	// The tail is the ten count-1 values p30..p57 (i/3 in 10..19): groups
	// g1, g2 and "" in rotation, so the distinct non-empty count is 2
	// while a per-value sum would be 7.
	if !groups.Valid || groups.Int64 != 2 {
		t.Fatalf("(other) unique_groups = %+v, want 2 (distinct across the tail, not summed)", groups)
	}
}

// System dimensions roll up unconditionally (task 2), so the live half must
// produce them for a project that declares no attributes at all -- and the
// invariant must hold for them too.
func TestProductAttrsViewSystemDimensionsWithoutDeclaredKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	id := seedDeclaredProject(t, db, nil)
	seedAttrDay(t, db, id)

	before := readAttrs(t, db, id, "2026-08-01")
	var sys, custom int
	for _, r := range before {
		switch r.Key {
		case "$os", "$platform", "$app_version":
			sys++
		default:
			custom++
		}
	}
	if sys == 0 {
		t.Fatal("no $os/$platform/$app_version rows for an undeclared project")
	}
	if custom != 0 {
		t.Fatalf("%d rows for undeclared custom keys; only system dimensions were expected", custom)
	}
	if err := db.AggregateProductDay(ctx, id,
		civil.DateOf(ts("2026-08-01T00:00:00Z")), nil, 50); err != nil {
		t.Fatal(err)
	}
	if after := readAttrs(t, db, id, "2026-08-01"); !reflect.DeepEqual(before, after) {
		t.Fatalf("system dimensions changed when the day aggregated:\nbefore %v\nafter  %v",
			before, after)
	}
}

// The cap comes from the meta row the app writes at boot, so a non-default
// value must move the cutoff in the live half exactly as it moves it in the
// aggregation.
func TestProductAttrsViewHonoursMetaCap(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.SetMeta(ctx, "product_attributes_top_n", "3"); err != nil {
		t.Fatal(err)
	}
	id := seedDeclaredProject(t, db, []string{"plan"})
	seedAttrDay(t, db, id)
	before := readAttrs(t, db, id, "2026-08-01")
	var plans int
	for _, r := range before {
		if r.Event == "signup" && r.Key == "plan" {
			plans++
		}
	}
	if plans != 4 {
		t.Fatalf("live signup/plan rows = %d, want 4 (3 capped + one (other))", plans)
	}
	if err := db.AggregateProductDay(ctx, id,
		civil.DateOf(ts("2026-08-01T00:00:00Z")), []string{"plan"}, 3); err != nil {
		t.Fatal(err)
	}
	if after := readAttrs(t, db, id, "2026-08-01"); !reflect.DeepEqual(before, after) {
		t.Fatalf("capped view changed when the day aggregated:\nbefore %v\nafter  %v",
			before, after)
	}
}

// A missing meta row must fall back to the same default the aggregation
// clamps to (defaultAttrsTopN), not silently return zero live rows -- which
// would make every invariant assertion above pass vacuously.
func TestProductAttrsViewDefaultsCapWhenMetaMissing(t *testing.T) {
	db := newTestDB(t)
	id := seedDeclaredProject(t, db, []string{"plan"})
	seedAttrDay(t, db, id)
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM meta
		WHERE key='product_attributes_top_n'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("fixture unexpectedly has a cap row (%d)", n)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM v_product_attrs
		WHERE project_id=? AND day='2026-08-01' AND event_name='signup'
		  AND attr_key='plan'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != defaultAttrsTopN+1 {
		t.Fatalf("signup/plan rows with no cap row = %d, want %d", n, defaultAttrsTopN+1)
	}
}

// aggregate_product.go:29-31 clamps a non-positive topN to defaultAttrsTopN
// precisely so breakdowns are not silently lost -- `rn <= 0` keeps nothing,
// which would sweep every value into "(other)". The view's cap must clamp
// identically, or PRODUCT_ATTRIBUTES_TOP_N=0 (or a hand-edited meta row)
// makes the current day collapse to a single "(other)" row while the same
// day after rollup shows the full top-N: exactly the jump the invariant
// forbids.
func TestProductAttrsViewClampsBadMetaCap(t *testing.T) {
	for _, tc := range []struct {
		name, meta string
		goTopN     int // what the Go side is handed for the same setting
	}{
		{"zero", "0", 0},
		{"negative", "-7", -7},
		// A non-numeric value casts to 0 in SQL. No env value produces it,
		// so the Go side is handed the configured default while meta has
		// been hand-edited to garbage; both must still agree.
		{"non numeric", "banana", defaultAttrsTopN},
		{"empty", "", defaultAttrsTopN},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			ctx := context.Background()
			if err := db.SetMeta(ctx, "product_attributes_top_n", tc.meta); err != nil {
				t.Fatal(err)
			}
			id := seedDeclaredProject(t, db, []string{"plan"})
			seedAttrDay(t, db, id)

			before := readAttrs(t, db, id, "2026-08-01")
			var plans int
			for _, r := range before {
				if r.Event == "signup" && r.Key == "plan" {
					plans++
				}
			}
			// The clamp must land on defaultAttrsTopN, not on "keep
			// nothing": one row per kept value plus the tail.
			if plans != defaultAttrsTopN+1 {
				t.Fatalf("live signup/plan rows with meta=%q = %d, want %d "+
					"(the cap must clamp to defaultAttrsTopN, not collapse to (other))",
					tc.meta, plans, defaultAttrsTopN+1)
			}
			if err := db.AggregateProductDay(ctx, id,
				civil.DateOf(ts("2026-08-01T00:00:00Z")), []string{"plan"}, tc.goTopN); err != nil {
				t.Fatal(err)
			}
			if after := readAttrs(t, db, id, "2026-08-01"); !reflect.DeepEqual(before, after) {
				t.Fatalf("view changed when the day aggregated with meta=%q:\nbefore %v\nafter  %v",
					tc.meta, before, after)
			}
		})
	}
}

// v_views_platforms must agree with agg_views_platforms across the
// aggregate ∪ live boundary, including the (other) cap on a day with more
// than 500 distinct platforms — the shape it copies from countries.
func TestStitchViewPlatformsAcrossBoundaryWithCap(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViewDay(t, db)
	var extra []store.View
	for i := 0; i < topNDimension+5; i++ {
		extra = append(extra, store.View{ID: fmt.Sprintf("p-%d", i), TS: at(13, 0).Add(time.Duration(i) * time.Second),
			ActorID: "v3", Path: "/x", Platform: fmt.Sprintf("p%d", i), OS: "linux", Browser: "firefox", Device: "desktop"})
	}
	seedViews(t, db, extra...)
	snapshot := func() map[string][2]int {
		t.Helper()
		rows, err := db.db.Query(`SELECT platform, visitors, views FROM v_views_platforms WHERE project_id=1 AND day='2026-08-10'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string][2]int{}
		for rows.Next() {
			var k string
			var v, pv int
			if err := rows.Scan(&k, &v, &pv); err != nil {
				t.Fatal(err)
			}
			out[k] = [2]int{v, pv}
		}
		return out
	}
	before := snapshot()
	if before["web"] != [2]int{2, 4} || before["ios"] != [2]int{1, 2} || before["android"] != [2]int{1, 1} {
		t.Fatalf("live half = %v", before)
	}
	if _, ok := before[otherBucket]; !ok {
		t.Fatal("platform fixture did not exceed the cap")
	}
	if err := db.AggregateViewDay(ctx, 1, day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); !reflect.DeepEqual(after, before) {
		t.Errorf("v_views_platforms changed across the boundary:\nbefore %v\nafter  %v", before, after)
	}
}
