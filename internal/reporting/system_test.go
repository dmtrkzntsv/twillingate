package reporting

import (
	"context"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// systemPresets is every fixed preset a system dashboard's range
// switcher offers (Presets minus custom), in switcher order.
var systemPresets = []string{"today", "yesterday", "7d", "30d", "90d"}

// presetDates resolves preset to the from/to dates the UI sends for it
// (web/src/lib/ranges.ts), with today the UTC calendar date.
func presetDates(preset string, today civil.Date) (from, to string) {
	switch preset {
	case "today":
		return today.String(), today.String()
	case "yesterday":
		y := today.AddDays(-1).String()
		return y, y
	case "7d":
		return today.AddDays(-7).String(), today.AddDays(-1).String()
	case "30d":
		return today.AddDays(-30).String(), today.AddDays(-1).String()
	case "90d":
		return today.AddDays(-90).String(), today.AddDays(-1).String()
	}
	panic("presetDates: unknown preset " + preset)
}

// mayBeEmpty names the system widgets ("dashboard/widget") allowed to
// return no rows for 7d on the seeded project, each with the reason. It
// is empty today: every system widget has rows for 7d once
// seedSystemData has run. A widget added here needs a reason that holds
// for any project, not only for this seed.
var mayBeEmpty = map[string]string{}

// TestSystemDashboards is the parity-before-release gate (spec Tests,
// "system dashboards"): on a database migrated to latest and synced with
// this release's embedded system definition, every system widget runs
// for every preset on a project seeded with raw rows (the views' live
// halves) and aggregates (their history), its rows fit its component,
// and it has rows for 7d.
func TestSystemDashboards(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	svc, pid, today := f.svc, f.project, f.today

	ds, err := svc.Dashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, d := range ds.Dashboards {
		if d.Owner == store.OwnerSystem {
			titles = append(titles, fmt.Sprintf("%d %s %s", d.ID, d.Title, d.Range))
		}
	}
	want := "1 Views 7d, 2 Product 7d, 3 Users 7d, 4 Groups 7d, 5 Retention 90d"
	if got := strings.Join(titles, ", "); got != want {
		t.Fatalf("system dashboards = %s, want %s", got, want)
	}

	seen := map[string]bool{}
	for _, d := range ds.Dashboards {
		if d.Owner != store.OwnerSystem {
			continue
		}
		detail, err := svc.Dashboard(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(detail.Widgets) == 0 {
			t.Errorf("dashboard %s has no widgets", d.Title)
		}
		for _, w := range detail.Widgets {
			key := strings.ToLower(d.Title) + "/" + w.Name
			seen[key] = true
			t.Run(key, func(t *testing.T) {
				for _, preset := range systemPresets {
					from, to := presetDates(preset, today)
					got, err := svc.WidgetData(ctx, DataRequest{WidgetID: w.ID, ProjectID: pid, From: from, To: to})
					if err != nil {
						t.Fatalf("%s: %v", preset, err)
					}
					if got.Removed {
						t.Fatalf("%s: removed", preset)
					}
					res, ok := got.Data.(readsql.Result)
					if !ok {
						continue // md: nothing to count
					}
					if preset != "7d" {
						continue
					}
					if len(res.Rows) == 0 {
						if _, ok := mayBeEmpty[key]; !ok {
							t.Errorf("7d: no rows (columns %s)", strings.Join(res.Columns, ", "))
						}
						continue
					}
					if w.Component != nil && *w.Component == "stat" {
						if v := column(t, res, 0, "value"); v == "" {
							t.Errorf("7d: stat value is empty")
						}
					}
				}
			})
		}
	}
	for key := range mayBeEmpty {
		if !seen[key] {
			t.Errorf("mayBeEmpty names %s, which is not a system widget", key)
		}
	}
}

// TestSystemDashboardsAreOneGroup is D16: Views (id 1) names no group of
// its own, so it is group 1; Product, Users, Groups and Retention (ids
// 2-5) each name "group": 1 in their dashboard.json. This release's
// embedded system definition (loaded by the real Migrate, not a test
// fixture) must leave every one of the five with GroupID 1.
func TestSystemDashboardsAreOneGroup(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)

	ds, err := f.svc.Dashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]int64{}
	for _, d := range ds.Dashboards {
		if d.Owner == store.OwnerSystem {
			got[d.ID] = d.GroupID
		}
	}
	want := map[int64]int64{1: 1, 2: 1, 3: 1, 4: 1, 5: 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("system dashboard GroupIDs = %v, want %v", got, want)
	}
}

// systemFixture is a store migrated to latest and synced with this
// release's embedded system definition, a Service over it with the cache
// off, and two seeded projects: project, with seedSystemData's 100 days,
// and neighbour, with raw views on the same days and aggregates of its
// own, so a query that forgets its project filter reads wrong numbers.
type systemFixture struct {
	svc                *Service
	db                 *readsql.DB
	project, neighbour int64
	today              civil.Date
}

func newSystemFixture(t *testing.T) systemFixture {
	t.Helper()
	ctx := context.Background()
	st, path := newTestStore(t)
	// A generous timeout: this runs under -race in make test, where the
	// pure-Go SQLite driver is several times slower than in production.
	db, err := readsql.Open(path, 60*time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(ctx, st, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	today := civil.Today(time.Now().UTC())
	pid := mustCreateProject(t, st, "demo")
	seedSystemData(t, st, pid, today)
	other := mustCreateProject(t, st, "neighbour")
	seedNeighbour(t, st, other, today)
	return systemFixture{svc: New(st, db, Options{}), db: db, project: pid, neighbour: other, today: today}
}

// column returns row i's value in res's column name, failing the test
// when res has no such column or row.
func column(t *testing.T, res readsql.Result, i int, name string) string {
	t.Helper()
	for c, col := range res.Columns {
		if col == name {
			if i >= len(res.Rows) {
				t.Fatalf("no row %d (%d rows)", i, len(res.Rows))
			}
			return res.Rows[i][c]
		}
	}
	t.Fatalf("no column %s in %s", name, strings.Join(res.Columns, ", "))
	return ""
}

// viewsDailyReference is, for each Views widget whose SQL reads
// agg_views_daily and raw_views directly instead of v_views_daily, the
// same answer read from v_views_daily in the plain shapes
// (totals, daily, kinds) and, for the two stats with a trend, the
// previous window read the obvious way. The widget exists only because
// the view is slow; it must never answer differently.
var viewsDailyReference = map[string]string{
	"visitors": `
SELECT COALESCE(SUM(visitors), 0) AS value,
       (SELECT COALESCE(SUM(visitors), 0) FROM v_views_daily
         WHERE project_id = :project
           AND day BETWEEN date(:from, '-' || (julianday(:to) - julianday(:from) + 1) || ' days')
                       AND date(:from, '-1 day')) AS previous
FROM v_views_daily
WHERE project_id = :project AND day BETWEEN :from AND :to`,
	"views": `
SELECT COALESCE(SUM(views), 0) AS value,
       (SELECT COALESCE(SUM(views), 0) FROM v_views_daily
         WHERE project_id = :project
           AND day BETWEEN date(:from, '-' || (julianday(:to) - julianday(:from) + 1) || ' days')
                       AND date(:from, '-1 day')) AS previous
FROM v_views_daily
WHERE project_id = :project AND day BETWEEN :from AND :to`,
	"bounce-rate": `
SELECT CASE WHEN SUM(sessions) > 0 THEN SUM(bounces) * 1.0 / SUM(sessions) ELSE 0 END AS value
FROM v_views_daily
WHERE project_id = :project AND day BETWEEN :from AND :to`,
	"avg-session": `
SELECT CASE WHEN SUM(sessions) > 0 THEN SUM(duration_sec) * 1.0 / SUM(sessions) ELSE 0 END AS value
FROM v_views_daily
WHERE project_id = :project AND day BETWEEN :from AND :to`,
	"visitors-and-views": `
WITH daily AS (
  SELECT day, SUM(visitors) AS visitors, SUM(views) AS views
  FROM v_views_daily
  WHERE project_id = :project AND day BETWEEN :from AND :to
  GROUP BY day
)
SELECT day AS x, 'Visitors' AS series, visitors AS y FROM daily
UNION ALL
SELECT day, 'Views', views FROM daily
ORDER BY x, series`,
	"visitors-by-kind": `
SELECT day AS x, kind AS series, visitors AS y
FROM v_views_daily
WHERE project_id = :project AND day BETWEEN :from AND :to
ORDER BY day, kind`,
	"avg-session-length": `
SELECT day AS x,
       CASE WHEN SUM(sessions) > 0 THEN SUM(duration_sec) * 1.0 / SUM(sessions) ELSE 0 END AS y
FROM v_views_daily
WHERE project_id = :project AND day BETWEEN :from AND :to
GROUP BY day
ORDER BY day`,
}

// TestViewsDailyEquivalence pins the Views widgets that stitch
// agg_views_daily and raw_views themselves to v_views_daily (migration
// 020): for both seeded projects and every preset, each returns exactly
// the columns and rows its reference query over the view returns. A
// change to the view's stitching or sessionization that the widgets do
// not follow fails here.
func TestViewsDailyEquivalence(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	system, err := fs.Sub(systemFS, "system")
	if err != nil {
		t.Fatal(err)
	}
	views, err := LoadDashboard(system, "views")
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, w := range views.Widgets {
		sources[w.Name] = w.Source
	}
	run := func(q string, p Params) readsql.Result {
		t.Helper()
		params, err := readsql.Check(q)
		if err != nil {
			t.Fatal(err)
		}
		res, err := f.db.Query(ctx, q, bindArgs(params, p.ProjectID, p.From, p.To)...)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for name, ref := range viewsDailyReference {
		src, ok := sources[name]
		if !ok {
			t.Errorf("views has no widget %s", name)
			continue
		}
		for _, pid := range []int64{f.project, f.neighbour} {
			for _, preset := range systemPresets {
				from, to := presetDates(preset, f.today)
				p := Params{ProjectID: pid, From: from, To: to}
				got, want := run(src, p), run(ref, p)
				if preset == "7d" && len(want.Rows) == 0 {
					t.Errorf("%s project %d 7d: reference has no rows; the seed does not exercise it", name, pid)
				}
				if !reflect.DeepEqual(got.Columns, want.Columns) || !reflect.DeepEqual(got.Rows, want.Rows) {
					t.Errorf("%s project %d %s:\n got  %v %v\n want %v %v", name, pid, preset,
						got.Columns, got.Rows, want.Columns, want.Rows)
				}
			}
		}
	}
}

// TestRetentionMilestonesMatchCurve checks the two ways Retention reads
// the same curve agree: each milestone (D7, D30, D45) equals the curve's
// y at that offset, or is empty where the curve has not reached it.
func TestRetentionMilestonesMatchCurve(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	d, err := f.svc.Dashboard(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, w := range d.Widgets {
		ids[w.Name] = w.ID
	}
	load := func(name, preset string) readsql.Result {
		t.Helper()
		id, ok := ids[name]
		if !ok {
			t.Fatalf("retention has no widget %s", name)
		}
		from, to := presetDates(preset, f.today)
		got, err := f.svc.WidgetData(ctx, DataRequest{WidgetID: id, ProjectID: f.project, From: from, To: to})
		if err != nil {
			t.Fatal(err)
		}
		return got.Data.(readsql.Result)
	}
	for _, pair := range [][2]string{{"signed-in-milestones", "signed-in-retention"}, {"install-milestones", "install-retention"}} {
		for _, preset := range systemPresets {
			milestones, curve := load(pair[0], preset), load(pair[1], preset)
			ys := map[string]string{}
			for i := range curve.Rows {
				ys[column(t, curve, i, "x")] = column(t, curve, i, "y")
			}
			for _, k := range []string{"7", "30", "45"} {
				if got, want := column(t, milestones, 0, "D"+k), ys[k]; got != want {
					t.Errorf("%s %s: D%s = %q, curve y at %s = %q", pair[0], preset, k, got, k, want)
				}
			}
			if preset == "90d" && ys["45"] == "" {
				t.Errorf("%s 90d: curve never reaches 45; the seed does not exercise the milestones", pair[1])
			}
		}
	}
}

// seedNeighbour gives projectID raw views on the same three days as
// seedSystemData's project, with sessions of its own (three views per
// actor, the last 50 minutes after the second: two sessions without an
// id), plus agg_views_daily rows for every older day.
func seedNeighbour(t *testing.T, st store.Store, projectID int64, today civil.Date) {
	t.Helper()
	var views, agg []string
	for back := 0; back < 3; back++ {
		day := today.AddDays(-back).String()
		for i := 0; i < 5; i++ {
			for v, minute := range []int{0, 10, 60} {
				ts := fmt.Sprintf("%sT%02d:%02d:00Z", day, 2+i+minute/60, minute%60)
				views = append(views, fmt.Sprintf("('n-%d-%d-%d', %d, 'views', '$page_view', '%s', '%s', 'web', 'n%d', 'user', '/')",
					back, i, v, projectID, ts, ts, i))
			}
		}
	}
	valuesInsert(t, st, "events", "id, project_id, family, event_name, ts, received_at, kind, actor_id, actor_kind, path", views)
	for back := 3; back < 100; back++ {
		agg = append(agg, fmt.Sprintf("(%d, '%s', 'web', 7, 30, 9, 4, 700)", projectID, today.AddDays(-back)))
	}
	valuesInsert(t, st, "agg_views_daily", "project_id, day, kind, visitors, views, sessions, bounces, duration_sec", agg)
}

// valuesInsert runs INSERT INTO table (cols) VALUES rows in chunks,
// each row already rendered as SQL literals: the seed writes a few
// thousand rows, too many for one statement's bound parameters and too
// slow as one statement each.
func valuesInsert(t *testing.T, st store.Store, table, cols string, rows []string) {
	t.Helper()
	for len(rows) > 0 {
		n := min(len(rows), 500)
		rawExec(t, st, "INSERT INTO "+table+" ("+cols+") VALUES "+strings.Join(rows[:n], ", "))
		rows = rows[n:]
	}
}

// seedSystemData gives projectID 100 days of data ending today, the way
// a running install holds it: raw events rows for the last three days
// (today, yesterday and the day before — the views' live halves) and
// aggregate rows in every agg_* table a system dashboard reads for each
// older day. It covers retention cohorts of both actor kinds, user and
// group identities with display names, two app versions, all three
// consent states, and product events carrying a declared attribute.
func seedSystemData(t *testing.T, st store.Store, projectID int64, today civil.Date) {
	t.Helper()
	rawExec(t, st, `UPDATE projects SET attributes = '["plan"]' WHERE id = ?`, projectID)
	p := projectID
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

	// Raw rows: the last three days. Twelve actors a day; a third are an
	// iOS app, the rest web browsers. Each sends two views a few minutes
	// apart (one session, not a bounce) — every fourth only one (a
	// bounce) — and two product events. Sessionization gets both of its
	// rules exercised: actors 2, 6 and 10 send client session ids, a third
	// view 42 minutes after the second in the same session (an id wins
	// over the 30-minute gap) and a fourth 3 minutes later in a new one
	// (a new id splits even without a gap); actors 4 and 8 send no id and
	// a second view 40 minutes after the first (two sessions, both
	// bounces).
	var events []string
	for back := 0; back < 3; back++ {
		day := today.AddDays(-back).String()
		for i := 0; i < 12; i++ {
			actor := fmt.Sprintf("a%d-%d", back, i)
			userID, groupID := "", ""
			if i < 8 {
				userID = fmt.Sprintf("u%d", i%5)
			}
			if i < 9 {
				groupID = fmt.Sprintf("g%d", i%3)
			}
			consent := []string{"1", "0", "NULL"}[i%3]
			country := []string{"US", "DE", "FR", "GB"}[i%4]
			app := i%3 == 0
			kind, name, platform, os, osVersion := "web", "$page_view", "web", "macos", "14"
			host, path, ref, browser, browserVersion := "example.com", []string{"/", "/pricing", "/docs"}[i%3], []string{"", "google", "twitter"}[i%3], "chrome", "126"
			device, model, width, height, appVersion, appLocale := "desktop", "", 1920, 1080, "", ""
			utmSource, utmMedium, utmCampaign := "", "", ""
			if i%4 == 1 {
				utmSource, utmMedium, utmCampaign = "newsletter", "email", "launch"
			}
			if app {
				kind, name, platform, os, osVersion = "app", "$screen_view", "ios", "ios", "17.4"
				host, path, ref, browser, browserVersion = "", "/home", "", "", ""
				device, model, width, height = "mobile", "iPhone15,3", 390, 844
				appVersion, appLocale = []string{"2.4.1", "2.5.0"}[i%2], "en"
			}
			minutes, sessions := []int{0, 3}, []string{"", ""}
			switch {
			case i%4 == 3:
				minutes = []int{0}
			case i%4 == 2:
				minutes = []int{0, 3, 45, 48}
				sessions = []string{"s1-" + actor, "s1-" + actor, "s1-" + actor, "s2-" + actor}
			case i == 4 || i == 8:
				minutes = []int{0, 40}
			}
			for v, minute := range minutes {
				sessionID := ""
				if v < len(sessions) {
					sessionID = sessions[v]
				}
				ts := fmt.Sprintf("%sT%02d:%02d:00Z", day, 1+i, minute)
				events = append(events, fmt.Sprintf("(%s, %d, 'views', %s, %s, %s, %s, %s, 'user', %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, 'en-US', %s, %s, %s, %s, %d, %d, %s, %s, '{}')",
					q(fmt.Sprintf("v-%s-%d", actor, v)), p, q(name), q(ts), q(ts), q(kind), q(actor),
					q(userID), q(groupID), q(sessionID), q(host), q(path), q(ref), q(utmSource), q(utmMedium), q(utmCampaign),
					q(platform), q(os), q(osVersion), q(browser), q(browserVersion),
					q(appVersion), q(appLocale), q(device), q(model), width, height, q(country), consent))
			}
			for e, event := range []string{"signup", "click"} {
				ts := fmt.Sprintf("%sT%02d:%02d:30Z", day, 1+i, e)
				events = append(events, fmt.Sprintf("(%s, %d, 'product', %s, %s, %s, %s, %s, 'user', %s, %s, '', '', '', '', '', '', '', %s, %s, %s, %s, %s, 'en-US', %s, 'en', %s, '', 0, 0, %s, %s, %s)",
					q(fmt.Sprintf("e-%s-%d", actor, e)), p, q(event), q(ts), q(ts), q(kind), q(actor),
					q(userID), q(groupID), q(platform), q(os), q(osVersion), q(browser), q(browserVersion),
					q([]string{"2.4.1", "2.5.0"}[i%2]), q(device), q(country), consent,
					q(fmt.Sprintf(`{"plan":"%s"}`, []string{"free", "pro"}[i%2]))))
			}
		}
	}
	valuesInsert(t, st, "events", `id, project_id, family, event_name, ts, received_at, kind, actor_id, actor_kind,
		user_id, group_id, session_id, host, path, referrer_source, utm_source, utm_medium, utm_campaign,
		platform, os, os_version, browser, browser_version, browser_locale, app_version, app_locale,
		device, device_model, display_width, display_height, country, consent, attributes`, events)

	// Aggregates: every older day back to today-99, as the daily pass
	// would have left them.
	agg := map[string][]string{}
	add := func(table, format string, args ...any) {
		agg[table] = append(agg[table], fmt.Sprintf(format, args...))
	}
	for back := 3; back < 100; back++ {
		day := q(today.AddDays(-back).String())
		n := 100 - back // traffic grows toward today
		add("agg_views_daily", "(%d, %s, 'web', %d, %d, %d, %d, %d)", p, day, 20+n, 50+2*n, 25+n, 10, 1200+30*n)
		add("agg_views_daily", "(%d, %s, 'app', %d, %d, %d, %d, %d)", p, day, 8, 20, 10, 2, 600)
		for i, path := range []string{"/", "/pricing", "/docs"} {
			add("agg_views_paths", "(%d, %s, %s, %d, %d)", p, day, q(path), 10-i, 20-i)
		}
		for i, host := range []string{"example.com", "app.example.com"} {
			add("agg_views_hosts", "(%d, %s, %s, %d, %d)", p, day, q(host), 10-i, 20-i)
		}
		for i, src := range []string{"", "google", "twitter"} {
			add("agg_views_referrers", "(%d, %s, %s, %d, %d)", p, day, q(src), 10-i, 20-i)
		}
		add("agg_views_utm", "(%d, %s, 'newsletter', 'email', 'launch', 4, 6)", p, day)
		for i, c := range []string{"US", "DE", "FR"} {
			add("agg_views_countries", "(%d, %s, %s, %d, %d)", p, day, q(c), 10-i, 20-i)
		}
		for i, o := range [][2]string{{"macos", "14"}, {"windows", ""}, {"ios", "17.4"}} {
			add("agg_views_os", "(%d, %s, %s, %s, %d, %d)", p, day, q(o[0]), q(o[1]), 10-i, 20-i)
		}
		for i, b := range [][2]string{{"chrome", "126"}, {"safari", "17"}} {
			add("agg_views_browsers", "(%d, %s, %s, %s, %d, %d)", p, day, q(b[0]), q(b[1]), 10-i, 20-i)
		}
		for i, d := range [][2]string{{"desktop", ""}, {"mobile", "iPhone15,3"}} {
			add("agg_views_devices", "(%d, %s, %s, %s, %d, %d)", p, day, q(d[0]), q(d[1]), 10-i, 20-i)
		}
		for i, d := range []string{"1920x1080", "390x844"} {
			add("agg_views_displays", "(%d, %s, %s, %d, %d)", p, day, q(d), 10-i, 20-i)
		}
		for i, c := range []string{"given", "none", "unknown"} {
			add("agg_views_consent", "(%d, %s, %s, %d, %d)", p, day, q(c), 10-i, 20-i)
		}
		for i, v := range []string{"2.4.1", "2.5.0"} {
			add("agg_views_app_versions", "(%d, %s, 'ios', %s, %d, %d)", p, day, q(v), 4+i, 10+i)
		}
		add("agg_product_daily", "(%d, %s, 'signup', 5, 4)", p, day)
		add("agg_product_daily", "(%d, %s, 'click', 20, 6)", p, day)
		add("agg_product_totals", "(%d, %s, 25, 8)", p, day)
		add("agg_product_attrs", "(%d, %s, 'signup', '$app_version', '2.4.1', 5, 4, 2)", p, day)
		add("agg_product_attrs", "(%d, %s, 'click', '$app_version', '2.5.0', 20, 6, NULL)", p, day)
		add("agg_product_attrs", "(%d, %s, 'signup', 'plan', 'pro', 3, 3, 1)", p, day)
		// Five regulars on alternate days, and one user and one group
		// seen only that day (new, never returning).
		for k := 0; k < 5; k++ {
			if (back+k)%2 == 0 {
				add("agg_identity_daily", "(%d, %s, 'user', %s, 1, 1, %d, %d)", p, day, q(fmt.Sprintf("u%d", k)), 3+k, 2)
			}
		}
		add("agg_identity_daily", "(%d, %s, 'user', %s, 1, 1, 1, 0)", p, day, q(fmt.Sprintf("once-%d", back)))
		for k := 0; k < 3; k++ {
			add("agg_identity_daily", "(%d, %s, 'group', %s, 2, 2, %d, %d)", p, day, q(fmt.Sprintf("g%d", k)), 6+k, 3)
		}
		add("agg_identity_daily", "(%d, %s, 'group', %s, 1, 1, 1, 0)", p, day, q(fmt.Sprintf("team-%d", back)))
	}
	// Retention: a cohort of each actor kind every day through yesterday,
	// followed up to 45 days, as far as each cohort has reached.
	for back := 1; back < 100; back++ {
		for _, kind := range []string{"user", "install"} {
			for off := 0; off <= 45 && off < back; off++ {
				actors := 10
				if off > 0 {
					actors = max(1, 6-off/10)
				}
				add("agg_retention", "(%d, %s, %s, %d, %d)", p, q(kind), q(today.AddDays(-back).String()), off, actors)
			}
		}
	}
	cols := map[string]string{
		"agg_views_daily":        "project_id, day, kind, visitors, views, sessions, bounces, duration_sec",
		"agg_views_paths":        "project_id, day, path, visitors, views",
		"agg_views_hosts":        "project_id, day, host, visitors, views",
		"agg_views_referrers":    "project_id, day, source, visitors, views",
		"agg_views_utm":          "project_id, day, utm_source, utm_medium, utm_campaign, visitors, views",
		"agg_views_countries":    "project_id, day, country, visitors, views",
		"agg_views_os":           "project_id, day, os, os_version, visitors, views",
		"agg_views_browsers":     "project_id, day, browser, browser_version, visitors, views",
		"agg_views_devices":      "project_id, day, device, device_model, visitors, views",
		"agg_views_displays":     "project_id, day, display, visitors, views",
		"agg_views_consent":      "project_id, day, consent, visitors, views",
		"agg_views_app_versions": "project_id, day, platform, app_version, visitors, views",
		"agg_product_daily":      "project_id, day, event_name, count, unique_users",
		"agg_product_totals":     "project_id, day, total_events, active_users",
		"agg_product_attrs":      "project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups",
		"agg_identity_daily":     "project_id, day, kind, id, actors, users, views, events",
		"agg_retention":          "project_id, actor_kind, cohort_day, day_offset, actors",
	}
	for table, rows := range agg {
		valuesInsert(t, st, table, cols[table], rows)
	}

	var names []string
	for k := 0; k < 5; k++ {
		names = append(names, fmt.Sprintf("(%d, 'user', 'u%d', 'User %d')", p, k, k))
	}
	for k := 0; k < 3; k++ {
		names = append(names, fmt.Sprintf("(%d, 'group', 'g%d', 'Team %d')", p, k, k))
	}
	valuesInsert(t, st, "identities", "project_id, kind, id, name", names)
}
