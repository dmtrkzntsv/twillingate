package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/google/uuid"
)

// seedProductEvent writes one product event. os and appVersion are the
// typed system columns; attrs is the custom JSON blob.
func seedProductEvent(t *testing.T, db *DB, projectID int64, event, at string,
	attrs map[string]string, os, appVersion string) {
	t.Helper()
	if err := db.WriteEvents(context.Background(), []store.Event{{Family: store.FamilyProduct,
		ID: uuid.NewString(), ProjectID: projectID, EventName: event,
		ActorID: "u1", TS: ts(at), Attributes: attrs,
		OS: os, AppVersion: appVersion,
	}}); err != nil {
		t.Fatal(err)
	}
}

// seedProductDay: project 1, day 2026-08-10:
//
//	subscribed: u1 plan=pro source=ads; u2 plan=free source=ads; u2 plan=free (no source)
//	ping:       u1 (no attrs)
func seedProductDay(t *testing.T, db *DB) {
	t.Helper()
	evs := []store.Event{
		{Family: store.FamilyProduct, ID: "p1", ProjectID: 1, EventName: "subscribed", ActorID: "u1", TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"plan": "pro", "source": "ads"}},
		{Family: store.FamilyProduct, ID: "p2", ProjectID: 1, EventName: "subscribed", ActorID: "u2", TS: ts("2026-08-10T11:00:00Z"),
			Attributes: map[string]string{"plan": "free", "source": "ads"}},
		{Family: store.FamilyProduct, ID: "p3", ProjectID: 1, EventName: "subscribed", ActorID: "u2", TS: ts("2026-08-10T12:00:00Z"),
			Attributes: map[string]string{"plan": "free"}},
		{Family: store.FamilyProduct, ID: "p4", ProjectID: 1, EventName: "ping", ActorID: "u1", TS: ts("2026-08-10T13:00:00Z")},
	}
	if err := db.WriteEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

// TestAggregateProductRunsWithNoDeclaredAttributes replaces the old
// "disabled" case: rollups are unconditional now (enabled is gone), so a
// project with no declared attributes still gets agg_product_daily /
// agg_product_totals rows and the raw day is still deleted, but no
// agg_product_attrs rows are written since no keys were named.
func TestAggregateProductRunsWithNoDeclaredAttributes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	if err := db.AggregateProductDay(ctx, 1, day("2026-08-10"), nil, 50); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM raw_product`).Scan(&n)
	if n != 0 {
		t.Fatalf("raw remaining %d", n)
	}
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_daily`).Scan(&n)
	if n == 0 {
		t.Fatal("rollups are unconditional now; agg_product_daily must be populated even with no declared attributes")
	}
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs`).Scan(&n)
	if n != 0 {
		t.Fatal("no declared attributes must produce no attr rows")
	}
}

func TestAggregateProductDeclaredAttributes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	attrs := []string{"plan", "source"}
	if err := db.AggregateProductDay(ctx, 1, day("2026-08-10"), attrs, 50); err != nil {
		t.Fatal(err)
	}
	var count, uniq int
	if err := db.db.QueryRow(`SELECT count, unique_users FROM agg_product_daily
		WHERE project_id=1 AND day='2026-08-10' AND event_name='subscribed'`).Scan(&count, &uniq); err != nil {
		t.Fatal(err)
	}
	if count != 3 || uniq != 2 {
		t.Fatalf("subscribed: c=%d u=%d", count, uniq)
	}
	if err := db.db.QueryRow(`SELECT total_events, active_users FROM agg_product_totals
		WHERE project_id=1 AND day='2026-08-10'`).Scan(&count, &uniq); err != nil {
		t.Fatal(err)
	}
	if count != 4 || uniq != 2 {
		t.Fatalf("totals: e=%d dau=%d", count, uniq)
	}
	// plan breakdown for subscribed
	if err := db.db.QueryRow(`SELECT count, unique_users FROM agg_product_attrs
		WHERE project_id=1 AND day='2026-08-10' AND event_name='subscribed'
		AND attr_key='plan' AND attr_value='free'`).Scan(&count, &uniq); err != nil {
		t.Fatal(err)
	}
	if count != 2 || uniq != 1 {
		t.Fatalf("plan=free: c=%d u=%d", count, uniq)
	}
	// declared attributes apply across every event name; ping has neither
	// plan nor source -> no rows for it.
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs WHERE event_name='ping'`).Scan(&n)
	if n != 0 {
		t.Fatal("events without the attribute must produce no attr rows")
	}
	db.db.QueryRow(`SELECT count FROM agg_product_attrs
		WHERE event_name='subscribed' AND attr_key='source' AND attr_value='ads'`).Scan(&count)
	if count != 2 {
		t.Fatalf("source=ads count=%d", count)
	}
	db.db.QueryRow(`SELECT COUNT(*) FROM raw_product`).Scan(&n)
	if n != 0 {
		t.Fatal("raw must be deleted after rollup")
	}
}

func TestAggregateProductTopNCollapsesTail(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var evs []store.Event
	// 5 distinct values; v0 appears 3x, v1 2x, v2..v4 once each.
	id := 0
	add := func(user, val string) {
		id++
		evs = append(evs, store.Event{Family: store.FamilyProduct, ID: fmt.Sprintf("e%d", id), ProjectID: 1,
			EventName: "clicked", ActorID: user, TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"button": val}})
	}
	add("u1", "v0")
	add("u2", "v0")
	add("u3", "v0")
	add("u1", "v1")
	add("u2", "v1")
	add("u1", "v2")
	add("u2", "v3")
	add("u3", "v4")
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateProductDay(ctx, 1, day("2026-08-10"), []string{"button"}, 2); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs WHERE attr_key='button'`).Scan(&n)
	if n != 3 { // v0, v1, (other)
		t.Fatalf("rows = %d, want 3 (top2 + other)", n)
	}
	var count, uniq int
	if err := db.db.QueryRow(`SELECT count, unique_users FROM agg_product_attrs
		WHERE attr_key='button' AND attr_value='(other)'`).Scan(&count, &uniq); err != nil {
		t.Fatal(err)
	}
	if count != 3 || uniq != 3 { // v2+v3+v4: 3 events by 3 distinct users
		t.Fatalf("(other): c=%d u=%d", count, uniq)
	}
}

func TestAggregateProductIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.AggregateProductDay(ctx, 1, day("2026-08-10"), nil, 50))
	must(db.AggregateProductDay(ctx, 1, day("2026-08-10"), nil, 50)) // no raw left: no-op
	var c int
	db.db.QueryRow(`SELECT count FROM agg_product_daily WHERE event_name='subscribed'`).Scan(&c)
	if c != 3 {
		t.Fatalf("second run corrupted: %d", c)
	}
}

// TestAggregateProductClampsNonPositiveTopN guards the destructive failure
// mode a topN<=0 would otherwise cause: rollupAttr's `rn <= topN` filter
// keeps nothing, so every distinct value silently collapses into
// "(other)" instead of erroring. Not reachable from production callers
// today (jobs.Runner always sets topN from
// config.Config.ProductAttributesTopN, which defaults to 50), but
// AggregateProductDay clamps it anyway as a last line of defense.
func TestAggregateProductClampsNonPositiveTopN(t *testing.T) {
	for _, topN := range []int{0, -5} {
		t.Run(fmt.Sprintf("topN=%d", topN), func(t *testing.T) {
			db := newTestDB(t)
			ctx := context.Background()
			seedProductDay(t, db) // subscribed: plan in {pro, free} -- 2 distinct values
			if err := db.AggregateProductDay(ctx, 1, day("2026-08-10"), []string{"plan"}, topN); err != nil {
				t.Fatal(err)
			}
			var other int
			db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs
				WHERE attr_key='plan' AND attr_value='(other)'`).Scan(&other)
			if other != 0 {
				t.Fatal("non-positive topN collapsed every value into (other) instead of clamping to the default")
			}
			var kept int
			db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs
				WHERE attr_key='plan' AND attr_value IN ('pro','free')`).Scan(&kept)
			if kept != 2 {
				t.Fatalf("kept = %d, want 2 (both real values, clamped topN=%d must behave like the default)", kept, defaultAttrsTopN)
			}
		})
	}
}

func TestRollupWritesSystemDimensions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductEvent(t, db, 1, "signup", "2026-08-01T10:00:00Z",
		map[string]string{}, "ios", "1.2.0") // os, app_version columns
	if err := db.AggregateProductDay(ctx, 1,
		civil.DateOf(ts("2026-08-01T00:00:00Z")), nil, 50); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := db.db.QueryRow(`SELECT attr_value FROM agg_product_attrs
		WHERE project_id=1 AND attr_key='$os'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "ios" {
		t.Fatalf("$os = %q, want ios", v)
	}
}

// TestRollupSystemDimensionsDoNotCollideWithCustomKeys guards the $ prefix
// invariant from the design doc: an event carrying both the typed os
// column and a custom "platform" attribute must produce two distinct
// attr_key rows ($os and platform), never merged.
func TestRollupSystemDimensionsDoNotCollideWithCustomKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductEvent(t, db, 1, "signup", "2026-08-01T10:00:00Z",
		map[string]string{"platform": "custom-value"}, "ios", "1.2.0")
	if err := db.AggregateProductDay(ctx, 1,
		civil.DateOf(ts("2026-08-01T00:00:00Z")), []string{"platform"}, 50); err != nil {
		t.Fatal(err)
	}
	var sysVal, customVal string
	if err := db.db.QueryRow(`SELECT attr_value FROM agg_product_attrs
		WHERE project_id=1 AND attr_key='$os'`).Scan(&sysVal); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`SELECT attr_value FROM agg_product_attrs
		WHERE project_id=1 AND attr_key='platform'`).Scan(&customVal); err != nil {
		t.Fatal(err)
	}
	if sysVal != "ios" || customVal != "custom-value" {
		t.Fatalf("$os=%q platform=%q, want ios / custom-value", sysVal, customVal)
	}
}

// TestRollupSystemDimensionsSurviveRawDeletion checks the retention story
// this task closes: $os/$app_version rows must exist after the raw day is
// deleted, for a project declaring no custom attributes at all.
func TestRollupSystemDimensionsSurviveRawDeletion(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductEvent(t, db, 1, "signup", "2026-08-01T10:00:00Z",
		map[string]string{}, "android", "3.0.0")
	if err := db.AggregateProductDay(ctx, 1,
		civil.DateOf(ts("2026-08-01T00:00:00Z")), nil, 50); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM raw_product`).Scan(&n)
	if n != 0 {
		t.Fatalf("raw remaining %d", n)
	}
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs
		WHERE attr_key IN ('$os','$app_version')`).Scan(&n)
	if n != 2 {
		t.Fatalf("system dimension rows = %d, want 2", n)
	}
}

// $platform rolls up beside $os and $app_version without being declared.
func TestRollupWritesPlatformSystemDimension(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WriteEvents(ctx, []store.Event{{Family: store.FamilyProduct,
		ID: uuid.NewString(), ProjectID: 1, EventName: "signup", ActorID: "u1",
		TS: ts("2026-08-01T10:00:00Z"), Platform: "electron", OS: "macos", AppVersion: "1.2.0",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateProductDay(ctx, 1, civil.DateOf(ts("2026-08-01T00:00:00Z")), nil, 50); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := db.db.QueryRow(`SELECT attr_value FROM agg_product_attrs
		WHERE project_id=1 AND attr_key='$platform'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "electron" {
		t.Fatalf("$platform = %q, want electron", v)
	}
}

// Zero is a measurement: a day whose events carry no group at all rolls up
// to unique_groups = 0, never NULL. NULL is reserved for days rolled up
// before migration 016, and the whole nullable decision rests on the two
// never being confused.
func TestAggregateProductGroupsZeroIsMeasured(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db) // no event carries a group_id
	if err := db.AggregateProductDay(ctx, 1, day("2026-08-10"), []string{"plan"}, 50); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"free", "pro"} {
		g := groupsOf(t, db, `SELECT unique_groups FROM agg_product_attrs
			WHERE event_name='subscribed' AND attr_key='plan' AND attr_value=?`, value)
		if !g.Valid || g.Int64 != 0 {
			t.Fatalf("plan=%s: unique_groups = %+v, want 0 (measured, none)", value, g)
		}
	}
}

// Distinctness: many events from one group count as one group, and the
// same group under two event names counts once per event row -- which is
// why the Evidence page takes max(unique_groups) rather than a sum.
func TestAggregateProductGroupsAreDistinctPerEvent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := func(m int) string { return fmt.Sprintf("2026-08-10T10:%02d:00Z", m) }
	pro := map[string]string{"plan": "pro"}
	if err := db.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyProduct, ID: "g1", ProjectID: 1, EventName: "signup", ActorID: "u1", GroupID: "acme", TS: ts(at(0)), Attributes: pro},
		{Family: store.FamilyProduct, ID: "g2", ProjectID: 1, EventName: "signup", ActorID: "u2", GroupID: "acme", TS: ts(at(1)), Attributes: pro},
		{Family: store.FamilyProduct, ID: "g3", ProjectID: 1, EventName: "signup", ActorID: "u3", GroupID: "acme", TS: ts(at(2)), Attributes: pro},
		{Family: store.FamilyProduct, ID: "g4", ProjectID: 1, EventName: "signup", ActorID: "u4", GroupID: "globex", TS: ts(at(3)), Attributes: pro},
		{Family: store.FamilyProduct, ID: "g5", ProjectID: 1, EventName: "renew", ActorID: "u1", GroupID: "acme", TS: ts(at(4)), Attributes: pro},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateProductDay(ctx, 1, day("2026-08-10"), []string{"plan"}, 50); err != nil {
		t.Fatal(err)
	}
	for event, want := range map[string]int64{"signup": 2, "renew": 1} {
		g := groupsOf(t, db, `SELECT unique_groups FROM agg_product_attrs
			WHERE event_name=? AND attr_key='plan' AND attr_value='pro'`, event)
		if !g.Valid || g.Int64 != want {
			t.Fatalf("%s/plan=pro: unique_groups = %+v, want %d", event, g, want)
		}
	}
	// acme appears under both events: the per-event figures sum to 3
	// while only two groups exist, so a reader must not add them up.
}

// The tail: with topN forced low, "(other)" carries the distinct group
// count across the whole tail, computed from raw. Overlapping groups make
// that strictly less than the sum of the tail's own per-value counts.
func TestAggregateProductGroupsInTail(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var evs []store.Event
	id := 0
	add := func(user, group, val string) {
		id++
		evs = append(evs, store.Event{Family: store.FamilyProduct, ID: fmt.Sprintf("t%d", id), ProjectID: 1,
			EventName: "clicked", ActorID: user, GroupID: group, TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"button": val}})
	}
	add("u1", "acme", "v0") // v0 x3 is the one kept value
	add("u2", "globex", "v0")
	add("u3", "", "v0")
	add("u1", "acme", "v1") // the tail: acme twice, globex once, one with no group
	add("u2", "acme", "v2")
	add("u3", "globex", "v3")
	add("u4", "", "v4")
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateProductDay(ctx, 1, day("2026-08-10"), []string{"button"}, 1); err != nil {
		t.Fatal(err)
	}
	if g := groupsOf(t, db, `SELECT unique_groups FROM agg_product_attrs
		WHERE attr_key='button' AND attr_value='v0'`); !g.Valid || g.Int64 != 2 {
		t.Fatalf("v0: unique_groups = %+v, want 2 (the empty group does not count)", g)
	}
	var count int
	var groups sql.NullInt64
	if err := db.db.QueryRow(`SELECT count, unique_groups FROM agg_product_attrs
		WHERE attr_key='button' AND attr_value='(other)'`).Scan(&count, &groups); err != nil {
		t.Fatal(err)
	}
	if count != 4 || !groups.Valid || groups.Int64 != 2 {
		t.Fatalf("(other): count=%d unique_groups=%+v, want 4 and 2 (acme, globex; a summed tail would say 3)", count, groups)
	}
}
