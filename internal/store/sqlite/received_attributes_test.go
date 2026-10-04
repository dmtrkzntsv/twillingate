package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// withReceivedAttributes creates received_attributes on a database pinned
// below migration 028, for a test that writes events through WriteEvents
// before migrating further (never through 28, which would create it again).
func withReceivedAttributes(t *testing.T, db *DB) {
	t.Helper()
	body, err := migrationFS.ReadFile("migrations/028_received_attributes.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
}

// receivedRows reads received_attributes as "project/day/key=events" lines, sorted.
func receivedRows(t *testing.T, db *DB) []string {
	t.Helper()
	rows, err := db.db.Query(`SELECT project_id, day, attr_key, events FROM received_attributes`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p, n int64
		var day, key string
		if err := rows.Scan(&p, &day, &key, &n); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d/%s/%s=%d", p, day, key, n))
	}
	sort.Strings(out)
	return out
}

// Ingest counts each key once per inserted product or measure row, per
// project and day; views are not counted; a retried batch (same ids) adds
// nothing; a key with a quote or a dot is kept verbatim; the declarable
// reserved keys count from their columns when set.
func TestWriteEventsCountsReceivedAttributes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	boot := 1.0
	evs := []store.Event{
		{ID: "p1", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup", TS: ts("2026-08-02T10:00:00Z"),
			ActorID: "a", Path: "/pricing", Attributes: map[string]string{"plan": "pro", `we"ird.key`: "x"}},
		{ID: "p2", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup", TS: ts("2026-08-02T11:00:00Z"),
			ActorID: "b", Attributes: map[string]string{"plan": "free"}},
		{ID: "p3", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup", TS: ts("2026-08-03T11:00:00Z"),
			ActorID: "b", Attributes: map[string]string{"plan": "free"}},
		{ID: "m1", ProjectID: 2, Family: store.FamilyMeasures, EventName: "boot", TS: ts("2026-08-02T11:00:00Z"),
			ActorID: "c", Measure: store.MeasureTime, Value: &boot, DeviceModel: "iPhone15,2",
			Attributes: map[string]string{"plan": "pro"}},
		{ID: "v1", ProjectID: 1, Family: store.FamilyViews, TS: ts("2026-08-02T10:00:00Z"), Kind: "web",
			ActorID: "a", Path: "/", Attributes: map[string]string{"ignored": "yes"}},
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"1/2026-08-02/$path=1",
		"1/2026-08-02/plan=2",
		`1/2026-08-02/we"ird.key=1`,
		"1/2026-08-03/plan=1",
		"2/2026-08-02/$device_model=1",
		"2/2026-08-02/plan=1",
	}
	if got := receivedRows(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("after first write\n got %v\nwant %v", got, want)
	}
	// The same batch again: every row is ignored as a duplicate id.
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if got := receivedRows(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("a retried batch changed the counts\n got %v\nwant %v", got, want)
	}
}

// declarableValues covers exactly store.DeclarableAttributes' keys.
func TestDeclarableValuesMatchesTheDeclarableKeys(t *testing.T) {
	var got []string
	for _, kv := range declarableValues(store.Event{}) {
		got = append(got, kv.key)
	}
	sort.Strings(got)
	if want := store.DeclarableAttributeKeys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("declarableValues keys = %v, want %v", got, want)
	}
}

// A later batch on the same project, day and keys adds to the stored
// counts (the upsert's accumulate branch); an id repeated from an earlier
// batch is ignored and adds nothing.
func TestWriteEventsAccumulatesReceivedAttributes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	first := []store.Event{
		{ID: "a1", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup", TS: ts("2026-08-02T10:00:00Z"),
			ActorID: "a", Attributes: map[string]string{"plan": "pro", "seats": "3"}},
		{ID: "a2", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup", TS: ts("2026-08-02T11:00:00Z"),
			ActorID: "b", Attributes: map[string]string{"plan": "free"}},
	}
	if err := db.WriteEvents(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := []store.Event{
		first[0], // already stored: ignored, counts nothing
		{ID: "a3", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup", TS: ts("2026-08-02T12:00:00Z"),
			ActorID: "c", Attributes: map[string]string{"plan": "team", "region": "eu"}},
		{ID: "a4", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup", TS: ts("2026-08-02T13:00:00Z"),
			ActorID: "d", Attributes: map[string]string{"plan": "pro"}},
	}
	if err := db.WriteEvents(ctx, second); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"1/2026-08-02/plan=4",
		"1/2026-08-02/region=1",
		"1/2026-08-02/seats=1",
	}
	if got := receivedRows(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("after two batches\n got %v\nwant %v", got, want)
	}
}

// The pass recounts every raw day before today exactly, fills max_values
// with the busiest (event, measure) partition's distinct values, leaves
// today's ingest counts alone, and drops days whose raw rows are gone.
func TestMeasureServerStatsRecountsReceivedAttributes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var evs []store.Event
	// 2026-08-02: signup carries plan free/pro/team (3 values), upgrade
	// carries plan pro (1): max_values 3, events 4.
	for i, plan := range []string{"free", "pro", "team"} {
		evs = append(evs, store.Event{ID: fmt.Sprintf("s%d", i), ProjectID: 1, Family: store.FamilyProduct,
			EventName: "signup", TS: ts("2026-08-02T10:00:00Z"), ActorID: "a", Attributes: map[string]string{"plan": plan}})
	}
	evs = append(evs, store.Event{ID: "u1", ProjectID: 1, Family: store.FamilyProduct, EventName: "upgrade",
		TS: ts("2026-08-02T12:00:00Z"), ActorID: "a", Attributes: map[string]string{"plan": "pro"}})
	// 2026-08-04 is "today" for the pass: counted by ingest only.
	evs = append(evs, store.Event{ID: "t1", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup",
		TS: ts("2026-08-04T09:00:00Z"), ActorID: "a", Attributes: map[string]string{"plan": "pro"}})
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	// A stale row for a day with no raw rows left (rolled up): pruned.
	if _, err := db.db.Exec(`INSERT INTO received_attributes VALUES (1, '2026-07-01', 'plan', 9, 2)`); err != nil {
		t.Fatal(err)
	}
	// A wrong ingest count for a raw day: corrected.
	if _, err := db.db.Exec(`UPDATE received_attributes SET events = 99 WHERE day = '2026-08-02'`); err != nil {
		t.Fatal(err)
	}
	if err := db.MeasureServerStats(ctx, ts("2026-08-04T03:00:00Z")); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.Query(`SELECT day, attr_key, events, max_values FROM received_attributes ORDER BY day, attr_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var day, key string
		var n int64
		var mv sql.NullInt64
		if err := rows.Scan(&day, &key, &n, &mv); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s/%s=%d/%v", day, key, n, mv))
	}
	want := []string{"2026-08-02/plan=4/{3 true}", "2026-08-04/plan=1/{0 false}"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}
