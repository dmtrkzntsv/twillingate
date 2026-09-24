package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func day(s string) civil.Date {
	d, err := civil.Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

func at(h, m int) time.Time { return time.Date(2026, 8, 10, h, m, 0, 0, time.UTC) }

// seedViews writes views into project 1 with defaults filled in.
func seedViews(t *testing.T, db *DB, views ...store.View) {
	t.Helper()
	for i := range views {
		if views[i].ReceivedAt.IsZero() {
			views[i].ReceivedAt = views[i].TS
		}
		if views[i].ProjectID == 0 {
			views[i].ProjectID = 1
		}
		if views[i].Kind == "" {
			views[i].Kind = "web"
		}
		if views[i].ActorKind == "" {
			views[i].ActorKind = store.ActorConnection
		}
	}
	if err := db.WriteViews(context.Background(), views); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// seedViewDay is the deterministic fixture for 2026-08-10, project 1:
//
//	web v1: 10:00 /a, 10:10 /b          -> 1 session, 2 views, dur 600
//	web v1: 12:00 /a                    -> gap > 30 min: 2nd session, bounce
//	web v2: 11:00 /a (DE, mobile, chrome 126, android, google, hn/social/launch,
//	        display 390x844)            -> 1 session, bounce
//	app  i1: 10:00 /home s1, 10:05 /settings s1 (ios 17.2, 2.4.1, iPhone15,2, DE)
//	                                    -> 1 client session, 2 views, dur 300
//	app  i2: 11:00 /home s2 (android 14, 2.4.1, Pixel 8, FR)
//	                                    -> 1 session, bounce
//
// web totals: visitors 2, views 4, sessions 3, bounces 2, duration 600.
// app totals: visitors 2, views 3, sessions 2, bounces 1, duration 300.
//
// Values speak the closed vocabularies 015 introduced: platform is web on
// a web row and the os on an app row, and a column the client never set
// is unknown, not empty.
func seedViewDay(t *testing.T, db *DB) {
	t.Helper()
	web := func(id, actor, path string, ts time.Time) store.View {
		return store.View{ID: id, TS: ts, ActorID: actor, Kind: "web", Platform: "web",
			Host: "shop.example.com", Path: path, Country: "US",
			Device: "desktop", Browser: "firefox", BrowserVersion: "127", OS: "linux"}
	}
	v2 := web("4", "v2", "/a", at(11, 0))
	v2.Country, v2.Device, v2.Browser, v2.BrowserVersion, v2.OS = "DE", "mobile", "chrome", "126", "android"
	v2.ReferrerSource = "google"
	v2.UTMSource, v2.UTMMedium, v2.UTMCampaign = "hn", "social", "launch"
	v2.DisplayWidth, v2.DisplayHeight = 390, 844
	v2.BrowserLocale, v2.AppLocale = "de-DE", "en"
	app := func(id, actor, path, session, os, osv, model, country string, ts time.Time) store.View {
		return store.View{ID: id, TS: ts, ActorID: actor, ActorKind: store.ActorInstall, Kind: "app",
			Platform: os, SessionID: session, Path: path, OS: os, OSVersion: osv, AppVersion: "2.4.1",
			Device: "unknown", Browser: "unknown",
			DeviceModel: model, BrowserLocale: "en-US", AppLocale: "en", Country: country}
	}
	seedViews(t, db,
		web("1", "v1", "/a", at(10, 0)), web("2", "v1", "/b", at(10, 10)), web("3", "v1", "/a", at(12, 0)), v2,
		app("5", "i1", "/home", "s1", "ios", "17.2", "iPhone15,2", "DE", at(10, 0)),
		app("6", "i1", "/settings", "s1", "ios", "17.2", "iPhone15,2", "DE", at(10, 5)),
		app("7", "i2", "/home", "s2", "android", "14", "Pixel 8", "FR", at(11, 0)),
	)
}

type dailyRow struct{ visitors, views, sessions, bounces, dur int }

func readDaily(t *testing.T, db *DB, table, kind string) dailyRow {
	t.Helper()
	var r dailyRow
	err := db.db.QueryRow(fmt.Sprintf(`SELECT visitors, views, sessions, bounces, duration_sec
		FROM %s WHERE project_id=1 AND day='2026-08-10' AND kind=?`, table), kind).
		Scan(&r.visitors, &r.views, &r.sessions, &r.bounces, &r.dur)
	if err != nil {
		t.Fatalf("%s/%s: %v", table, kind, err)
	}
	return r
}

func TestAggregateViewDayPerKind(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViewDay(t, db)
	if err := db.AggregateViewDay(ctx, 1, day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	if got, want := readDaily(t, db, "agg_views_daily", "web"), (dailyRow{2, 4, 3, 2, 600}); got != want {
		t.Errorf("web = %+v, want %+v", got, want)
	}
	if got, want := readDaily(t, db, "agg_views_daily", "app"), (dailyRow{2, 3, 2, 1, 300}); got != want {
		t.Errorf("app = %+v, want %+v", got, want)
	}
	var raw int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM views WHERE project_id=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != 0 {
		t.Errorf("raw rows left after aggregation: %d", raw)
	}
}

func TestAggregateViewDayDimensions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViewDay(t, db)
	if err := db.AggregateViewDay(ctx, 1, day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	check := func(q string, args []any, wantV, wantP int) {
		t.Helper()
		var v, p int
		if err := db.db.QueryRow(q, args...).Scan(&v, &p); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if v != wantV || p != wantP {
			t.Errorf("%s %v = (%d,%d), want (%d,%d)", q, args, v, p, wantV, wantP)
		}
	}
	const w = ` WHERE project_id=1 AND day='2026-08-10' AND `
	check(`SELECT visitors, views FROM agg_views_paths`+w+`path=?`, []any{"/a"}, 2, 3)
	check(`SELECT visitors, views FROM agg_views_paths`+w+`path=?`, []any{"/home"}, 2, 2)
	check(`SELECT visitors, views FROM agg_views_hosts`+w+`host=?`, []any{""}, 2, 3)
	check(`SELECT visitors, views FROM agg_views_referrers`+w+`source=?`, []any{"google"}, 1, 1)
	check(`SELECT visitors, views FROM agg_views_utm`+w+`utm_source=?`, []any{"hn"}, 1, 1)
	check(`SELECT visitors, views FROM agg_views_countries`+w+`country=?`, []any{"DE"}, 2, 3)
	check(`SELECT visitors, views FROM agg_views_platforms`+w+`platform=?`, []any{"web"}, 2, 4)
	check(`SELECT visitors, views FROM agg_views_platforms`+w+`platform=?`, []any{"ios"}, 1, 2)
	check(`SELECT visitors, views FROM agg_views_os`+w+`os=? AND os_version=?`, []any{"ios", "17.2"}, 1, 2)
	check(`SELECT visitors, views FROM agg_views_os`+w+`os=? AND os_version=?`, []any{"linux", ""}, 1, 3)
	check(`SELECT visitors, views FROM agg_views_browsers`+w+`browser=? AND browser_version=?`, []any{"chrome", "126"}, 1, 1)
	check(`SELECT visitors, views FROM agg_views_app_versions`+w+`platform=? AND app_version=?`, []any{"ios", "2.4.1"}, 1, 2)
	check(`SELECT visitors, views FROM agg_views_app_versions`+w+`platform=? AND app_version=?`, []any{"android", "2.4.1"}, 1, 1)
	check(`SELECT visitors, views FROM agg_views_devices`+w+`device=? AND device_model=?`, []any{"unknown", "Pixel 8"}, 1, 1)
	check(`SELECT visitors, views FROM agg_views_devices`+w+`device=? AND device_model=?`, []any{"desktop", ""}, 1, 3)
	check(`SELECT visitors, views FROM agg_views_displays`+w+`display=?`, []any{"390x844"}, 1, 1)
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_views_utm WHERE utm_source='' AND utm_medium='' AND utm_campaign=''`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("empty utm row written: %d", n)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_views_app_versions WHERE app_version=''`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("empty app_version row written: %d", n)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_views_displays`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("displays rows = %d, want 1 (rows without a display are skipped)", n)
	}
}

// Every dimension collapses past the cap; the trailing key becomes
// "(other)" with visitors recomputed as distinct actors, not summed.
func TestAggregateViewDayCapsDimensions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var views []store.View
	for i := 0; i < topNDimension+10; i++ {
		// two actors see every collapsed path, so a summed count would say 20
		for _, actor := range []string{"a", "b"} {
			views = append(views, store.View{ID: fmt.Sprintf("%s-%d", actor, i), TS: at(9, 0).Add(time.Duration(i) * time.Second),
				ActorID: actor, Path: fmt.Sprintf("/p/%04d", i), OS: "linux", OSVersion: fmt.Sprintf("%d", i)})
		}
	}
	// One popular path stays out of the tail.
	for i := 0; i < 5; i++ {
		views = append(views, store.View{ID: fmt.Sprintf("hot-%d", i), TS: at(10, 0), ActorID: "c", Path: "/hot", OS: "linux", OSVersion: "0"})
	}
	seedViews(t, db, views...)
	if err := db.AggregateViewDay(ctx, 1, day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	var rows, otherV, otherP int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_views_paths WHERE project_id=1`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != topNDimension+1 {
		t.Errorf("paths rows = %d, want %d (top-N plus the other bucket)", rows, topNDimension+1)
	}
	if err := db.db.QueryRow(`SELECT visitors, views FROM agg_views_paths WHERE project_id=1 AND path='(other)'`).Scan(&otherV, &otherP); err != nil {
		t.Fatal(err)
	}
	if otherV != 2 || otherP != 22 {
		t.Errorf("(other) = (%d,%d), want (2,22): 11 collapsed paths x 2 actors, 2 distinct actors", otherV, otherP)
	}
	// Two-key dimension: the leading key stays intact, only os_version collapses.
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_views_os WHERE project_id=1 AND os='linux' AND os_version='(other)'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("os other bucket rows = %d, want 1", rows)
	}
}

func TestAggregateViewDayIsIdempotentAndSkipsEmptyDay(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViewDay(t, db)
	for i := 0; i < 2; i++ {
		if err := db.AggregateViewDay(ctx, 1, day("2026-08-10")); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := readDaily(t, db, "agg_views_daily", "web"), (dailyRow{2, 4, 3, 2, 600}); got != want {
		t.Errorf("after re-run web = %+v, want %+v", got, want)
	}
	if err := db.AggregateViewDay(ctx, 1, day("2026-08-11")); err != nil {
		t.Fatalf("empty day must be a no-op: %v", err)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_views_daily WHERE day='2026-08-11'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("empty day wrote %d rows", n)
	}
}

func TestViewDaysBefore(t *testing.T) {
	db := newTestDB(t)
	seedViews(t, db,
		store.View{ID: "1", TS: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC), ActorID: "a", Path: "/"},
		store.View{ID: "2", TS: time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC), ActorID: "a", Path: "/"},
		store.View{ID: "3", TS: time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC), ActorID: "a", Path: "/"},
	)
	days, err := db.ViewDaysBefore(context.Background(), 1, day("2026-08-05"))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0].String() != "2026-08-01" || days[1].String() != "2026-08-03" {
		t.Errorf("days = %v", days)
	}
}
