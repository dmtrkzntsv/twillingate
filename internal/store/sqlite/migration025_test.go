package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// productAttrsRows reads every v_product_attrs row with its SQL types, so
// a value that turns from an integer into text shows as a difference.
func productAttrsRows(t *testing.T, db *DB) []string {
	t.Helper()
	rows, err := db.db.Query(`SELECT project_id, day, event_name, attr_key, attr_value,
		count, unique_users, unique_groups FROM v_product_attrs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		vals := make([]any, 8)
		ptrs := make([]any, 8)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			parts[i] = fmt.Sprintf("%T:%v", v, v)
		}
		out = append(out, strings.Join(parts, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// Migration 025 reshapes v_product_attrs' live half; its answer must not
// move. The fixture has what the shape handles separately: declared custom
// and $ keys beside the system ones, a cap of 3 so most keys have a tail,
// a real value spelled "(other)", JSON numbers and objects, an empty group,
// a project declaring nothing, and a day already rolled up.
func TestMigration025KeepsProductAttrsAnswer(t *testing.T) {
	db := newTestDBAt(t, 24)
	ctx := context.Background()
	if err := db.SetMeta(ctx, "product_attributes_top_n", "3"); err != nil {
		t.Fatal(err)
	}
	keys := []string{"plan", "seats", "meta", "$path", "$utm_source"}
	id := seedDeclaredProject(t, db, keys)
	bare := seedDeclaredProject(t, db, nil)
	var evs []store.Event
	for d, day := range []string{"2026-08-01", "2026-08-02", "2026-08-03"} {
		for i := 0; i < 40; i++ {
			for _, pid := range []int64{id, bare} {
				plan := []string{"free", "pro", "team", "(other)", "edu", "gov"}[(i*(d+1))%6]
				evs = append(evs, store.Event{
					ID: fmt.Sprintf("p%d-%s-%02d", pid, day, i), ProjectID: pid, Family: store.FamilyProduct,
					EventName: []string{"signup", "export"}[i%2], TS: ts(day + "T10:00:00Z"),
					ActorID: fmt.Sprintf("a%d", i%9), GroupID: []string{"", "g1", "g2"}[i%3],
					Platform: "web", OS: []string{"macos", "windows", "linux", "ios", "android"}[i%5],
					AppVersion: fmt.Sprintf("1.%d", i%7), Browser: "firefox",
					Path: fmt.Sprintf("/p/%d", i%11), UTMSource: []string{"", "hn"}[i%2],
					Attributes: map[string]string{"plan": plan},
				})
			}
		}
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	// Values the SDK never sends but a hand-written row can hold: a JSON
	// number keeps its type through json_extract, an object comes back as
	// its JSON text.
	for i, attrs := range []any{
		map[string]any{"seats": 5, "meta": map[string]any{"a": 1}},
		map[string]any{"seats": 5.5, "meta": []any{1, 2}},
		map[string]any{"seats": 12},
		map[string]any{"seats": "5"},
	} {
		b, err := json.Marshal(attrs)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(`INSERT INTO events (family, project_id, day, id, event_name, ts, actor_id, attributes)
			VALUES ('product', ?, '2026-08-02', ?, 'signup', '2026-08-02T11:00:00Z', ?, ?)`,
			id, fmt.Sprintf("json-%d", i), fmt.Sprintf("j%d", i), string(b)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AggregateProductDay(ctx, id, civil.DateOf(ts("2026-08-01T00:00:00Z")), keys, 3); err != nil {
		t.Fatal(err)
	}

	before := productAttrsRows(t, db)
	var others, raw int
	for _, r := range before {
		if strings.Contains(r, "|string:(other)|") {
			others++
		}
		if !strings.Contains(r, "|string:2026-08-01|") {
			raw++
		}
	}
	if others == 0 || raw == 0 {
		t.Fatalf("fixture has %d (other) rows and %d raw-day rows; both must be exercised", others, raw)
	}
	if err := db.migrateThrough(ctx, 25); err != nil {
		t.Fatal(err)
	}
	if after := productAttrsRows(t, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("v_product_attrs changed across 025:\nbefore %v\nafter  %v", before, after)
	}
}

// v_measures_attrs' answer must not move either. The fixture folds a
// tail per bucket, merges a kept value spelled "(other)" with it, reads a
// declared custom and $ key beside the system ones, and has a second
// metric, a day already rolled up and a project declaring nothing. Sample
// rates are powers of two, so weight and sum add up exactly in any order.
func TestMigration025KeepsMeasuresAttrsAnswer(t *testing.T) {
	db := newTestDBAt(t, 24)
	ctx := context.Background()
	if err := db.SetMeta(ctx, "product_attributes_top_n", "2"); err != nil {
		t.Fatal(err)
	}
	keys := []string{"endpoint", "$path"}
	id := seedDeclaredProject(t, db, keys)
	bare := seedDeclaredProject(t, db, nil)
	values := []float64{0, 100, 340, 5000, 12.5}
	var evs []store.Event
	for d, ts := range []time.Time{
		time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
	} {
		for i := 0; i < 30; i++ {
			for _, pid := range []int64{id, bare} {
				ev := measureEvent([]string{"checkout_api", "$lcp"}[i%2], values[(i+d)%5], "time")
				ev.ProjectID, ev.TS = pid, ts
				ev.SampleRate = []float64{1, 0.5, 0.25}[i%3]
				ev.Attributes = map[string]string{"endpoint": []string{"(other)", "a", "b", "c", "d"}[(i*(d+1))%5]}
				ev.Path = fmt.Sprintf("/p/%d", i%4)
				ev.Browser = []string{"Chrome", "Firefox", "Safari", "Edge"}[(i+d)%4]
				ev.Device = []string{"desktop", "mobile"}[i%2]
				evs = append(evs, ev)
			}
		}
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateMeasureDay(ctx, id, day("2026-09-01"), keys, 2); err != nil {
		t.Fatal(err)
	}

	const q = `SELECT * FROM v_measures_attrs ORDER BY 1,2,3,4,5,6,7`
	before := snapshotRows(t, db, q)
	var others, raw int
	if err := db.db.QueryRow(`SELECT COUNT(*) FILTER (WHERE attr_value = '(other)'),
		COUNT(*) FILTER (WHERE day > '2026-09-01') FROM v_measures_attrs`).Scan(&others, &raw); err != nil {
		t.Fatal(err)
	}
	if others == 0 || raw == 0 {
		t.Fatalf("fixture has %d (other) rows and %d raw-day rows; both must be exercised", others, raw)
	}
	if err := db.migrateThrough(ctx, 25); err != nil {
		t.Fatal(err)
	}
	if after := snapshotRows(t, db, q); !reflect.DeepEqual(before, after) {
		t.Fatalf("v_measures_attrs changed across 025:\nbefore %v\nafter  %v", before, after)
	}
}

// typedViews reads every v_* view but the raw rows (v_events_flat), each
// value with its SQL type, rows sorted.
func typedViews(t *testing.T, db *DB) map[string][]string {
	t.Helper()
	rows, err := db.db.Query(`SELECT name FROM sqlite_schema WHERE type='view'
		AND name LIKE 'v\_%' ESCAPE '\' AND name <> 'v_events_flat' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	out := map[string][]string{}
	for _, n := range names {
		r := snapshotRows(t, db, "SELECT * FROM "+n)
		sort.Strings(r)
		out[n] = r
	}
	return out
}

// Every view 025 reshapes answers as it did at 24. Project 1 has three
// days of views and product events with every breakdown column set,
// sessions both keyed and split by gaps, and its first day rolled up.
// Project 2 has one day past every cap: 600 paths (one spelled
// "(other)"), 600 kinds, browser versions, UTM campaigns, and 520 users
// and groups.
func TestMigration025KeepsViewsAnswers(t *testing.T) {
	db := newTestDBAt(t, 24)
	ctx := context.Background()
	// 024's views hard-code 500; 025's read the setting, written here as
	// a boot would write VIEWS_DIMENSIONS_TOP_N=500 and IDENTITIES_TOP_N=500.
	for _, key := range []string{"views_dimensions_top_n", "identities_top_n"} {
		if err := db.SetMeta(ctx, key, "500"); err != nil {
			t.Fatal(err)
		}
	}
	p1 := seedDeclaredProject(t, db, nil)
	p2 := seedDeclaredProject(t, db, nil)
	var evs []store.Event
	for d := 0; d < 3; d++ {
		base := time.Date(2026, 8, 1+d, 8, 0, 0, 0, time.UTC)
		for i := 0; i < 120; i++ {
			// Minutes apart, with every fifth gap past half an hour.
			ts := base.Add(time.Duration(i*7+(i/5)*40) * time.Minute)
			evs = append(evs, store.Event{Family: store.FamilyViews, ID: fmt.Sprintf("v1-%d-%03d", d, i),
				ProjectID: p1, TS: ts, ReceivedAt: ts, Kind: []string{"web", "app", "cli"}[i%3],
				ActorID: fmt.Sprintf("a%d", i%13), ActorKind: store.ActorConnection,
				SessionID: []string{"", "", fmt.Sprintf("s%d", i%4)}[i%3],
				UserID:    []string{"", fmt.Sprintf("u%d", i%9)}[i%2], GroupID: []string{"", fmt.Sprintf("g%d", i%5)}[i%2],
				Host: []string{"a.example", "b.example"}[i%2], Path: fmt.Sprintf("/p/%d", i%17),
				ReferrerSource: []string{"", "google", "hn"}[i%3],
				UTMSource:      []string{"", "hn", "x"}[i%3], UTMMedium: []string{"", "social"}[i%2], UTMCampaign: []string{"", "launch"}[i%2],
				Platform: []string{"web", "ios", "android"}[i%3], OS: []string{"linux", "ios", "android"}[i%3],
				OSVersion: fmt.Sprintf("%d", i%4), Browser: []string{"firefox", "chrome"}[i%2], BrowserVersion: fmt.Sprintf("%d", 120+i%5),
				BrowserLocale: []string{"", "en-US", "de-DE"}[i%3], AppLocale: []string{"", "en"}[i%2],
				AppVersion: []string{"", "2.4.1", "2.5.0"}[i%3], Device: []string{"desktop", "mobile"}[i%2],
				DeviceModel: []string{"", "Pixel 8"}[i%2], DisplayWidth: []int{0, 390, 1440}[i%3], DisplayHeight: []int{0, 844, 900}[i%3],
				Country: []string{"US", "DE", "FR"}[i%3]})
			if i%2 == 0 {
				evs = append(evs, store.Event{Family: store.FamilyProduct, ID: fmt.Sprintf("p1-%d-%03d", d, i),
					ProjectID: p1, EventName: "signup", TS: ts, ReceivedAt: ts,
					ActorID: fmt.Sprintf("a%d", i%13), ActorKind: store.ActorUser,
					UserID: fmt.Sprintf("u%d", i%11), GroupID: []string{"", fmt.Sprintf("g%d", i%3)}[i%4/2]})
			}
		}
	}
	// One actor whose views are 10, 29 and 61 minutes apart: the 30-minute
	// session gap splits them exactly once.
	for d := 1; d < 3; d++ {
		for i, m := range []int{0, 10, 39, 100} {
			ts := time.Date(2026, 8, 1+d, 20, m, 0, 0, time.UTC)
			evs = append(evs, store.Event{Family: store.FamilyViews, ID: fmt.Sprintf("z-%d-%d", d, i),
				ProjectID: p1, TS: ts, ReceivedAt: ts, Kind: "web", ActorID: "z", ActorKind: store.ActorConnection,
				Path: "/z"})
		}
	}
	base := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 1300; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		path := fmt.Sprintf("/q/%d", i%600)
		if i%11 == 0 {
			path = "(other)"
		}
		evs = append(evs, store.Event{Family: store.FamilyViews, ID: fmt.Sprintf("v2-%04d", i),
			ProjectID: p2, TS: ts, ReceivedAt: ts, Kind: fmt.Sprintf("k%d", i%600),
			ActorID: fmt.Sprintf("b%d", i%90), ActorKind: store.ActorConnection, Path: path,
			Browser: "firefox", BrowserVersion: fmt.Sprintf("%d", i%600),
			UTMSource: "hn", UTMMedium: "social", UTMCampaign: fmt.Sprintf("c%d", i%600),
			UserID: fmt.Sprintf("u%d", i%520), GroupID: fmt.Sprintf("g%d", i%520)})
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateIdentityDay(ctx, p1, day("2026-08-01"), 500); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateViewDay(ctx, p1, day("2026-08-01"), 500); err != nil {
		t.Fatal(err)
	}

	before := typedViews(t, db)
	for _, v := range []string{"v_identity_daily", "v_views_app_versions", "v_views_browsers", "v_views_countries",
		"v_views_daily", "v_views_devices", "v_views_displays", "v_views_hosts", "v_views_locales", "v_views_os",
		"v_views_paths", "v_views_platforms", "v_views_referrers", "v_views_utm"} {
		if len(before[v]) == 0 {
			t.Errorf("fixture: %s has no rows", v)
		}
	}
	var others int
	if err := db.db.QueryRow(`SELECT (SELECT COUNT(*) FROM v_views_paths WHERE path = '(other)')
		+ (SELECT COUNT(*) FROM v_views_daily WHERE kind = '(other)')
		+ (SELECT COUNT(*) FROM v_views_browsers WHERE browser_version = '(other)')
		+ (SELECT COUNT(*) FROM v_views_utm WHERE utm_campaign = '(other)')`).Scan(&others); err != nil {
		t.Fatal(err)
	}
	if others != 4 {
		t.Errorf("fixture: %d (other) rows across paths, kinds, versions and campaigns, want 4", others)
	}
	if err := db.migrateThrough(ctx, 25); err != nil {
		t.Fatal(err)
	}
	after := typedViews(t, db)
	for v, rows := range before {
		if !reflect.DeepEqual(rows, after[v]) {
			t.Errorf("%s changed across 025:\nbefore %v\nafter  %v", v, rows, after[v])
		}
	}
}
