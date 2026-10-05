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

// Ingest records each key a product or measure row carried, per project
// and day, with events 0 for the daily pass to count; views are not
// recorded; a key with a quote or a dot is kept verbatim; the declarable
// reserved keys are recorded from their columns when set.
func TestWriteEventsRecordsReceivedAttributes(t *testing.T) {
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
		"1/2026-08-02/$path=0",
		"1/2026-08-02/plan=0",
		`1/2026-08-02/we"ird.key=0`,
		"1/2026-08-03/plan=0",
		"2/2026-08-02/$device_model=0",
		"2/2026-08-02/plan=0",
	}
	if got := receivedRows(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
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

// A batch whose keys ingest has already written writes nothing more:
// with the rows deleted behind its back, a second batch on the same keys
// leaves the table empty, and only a new key (or a retried batch's ids,
// which insert nothing) is written. A fresh store (a restart) writes a
// known key again, as INSERT OR IGNORE.
func TestWriteEventsWritesOnlyNewReceivedKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ev := func(id, day string, attrs map[string]string) store.Event {
		return store.Event{ID: id, ProjectID: 1, Family: store.FamilyProduct, EventName: "signup",
			TS: ts(day + "T10:00:00Z"), ActorID: "a", Attributes: attrs}
	}
	first := []store.Event{ev("a1", "2026-08-02", map[string]string{"plan": "pro", "seats": "3"})}
	if err := db.WriteEvents(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DELETE FROM received_attributes`); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteEvents(ctx, []store.Event{
		first[0], // a retried id: inserts nothing, records nothing
		ev("a2", "2026-08-02", map[string]string{"plan": "free", "region": "eu"}),
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := receivedRows(t, db), []string{"1/2026-08-02/region=0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second batch wrote %v, want only the new key %v", got, want)
	}
	// A restart forgets what was written; the next batch records its keys again.
	db.seen = receivedSeen{}
	if err := db.WriteEvents(ctx, []store.Event{ev("a3", "2026-08-02", map[string]string{"plan": "pro"})}); err != nil {
		t.Fatal(err)
	}
	if got, want := receivedRows(t, db), []string{"1/2026-08-02/plan=0", "1/2026-08-02/region=0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after a restart got %v, want %v", got, want)
	}
}

// The seen set keeps the newest day a batch carried and the day before;
// an older day leaves it once the newest day moves past its eve, and a
// rolled-back batch's keys are never remembered (WriteEvents remembers
// only after commit).
func TestReceivedSeenKeepsTheNewestTwoDays(t *testing.T) {
	var s receivedSeen
	k := func(day, key string) receivedKey { return receivedKey{1, day, key} }
	s.remember([]receivedKey{k("2026-08-01", "a"), k("2026-08-02", "b")})
	if got := s.unseen(receivedKeys{k("2026-08-01", "a"): {}, k("2026-08-02", "b"): {}}); len(got) != 0 {
		t.Fatalf("unseen = %v, want none", got)
	}
	s.remember([]receivedKey{k("2026-08-03", "c")})
	got := s.unseen(receivedKeys{k("2026-08-01", "a"): {}, k("2026-08-02", "b"): {}, k("2026-08-03", "c"): {}})
	if !reflect.DeepEqual(got, []receivedKey{k("2026-08-01", "a")}) {
		t.Fatalf("unseen after the newest day moved = %v, want only 08-01's key", got)
	}
	// An older day's key is not remembered at all.
	s.remember([]receivedKey{k("2026-07-30", "old")})
	if got := s.unseen(receivedKeys{k("2026-07-30", "old"): {}}); len(got) != 1 {
		t.Fatalf("an old day's key was remembered: unseen = %v", got)
	}
}

// The pass recounts every raw day before today exactly, fills max_values
// with the busiest (event, measure) partition's distinct values, leaves
// today's row as ingest wrote it (events 0), and drops days whose raw rows
// are gone.
func TestRecordUsageHistoryRecountsReceivedAttributes(t *testing.T) {
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
	// 2026-08-04 is "today" for the pass: recorded by ingest only.
	evs = append(evs, store.Event{ID: "t1", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup",
		TS: ts("2026-08-04T09:00:00Z"), ActorID: "a", Attributes: map[string]string{"plan": "pro"}})
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	// A stale row for a day with no raw rows left (rolled up): pruned.
	if _, err := db.db.Exec(`INSERT INTO received_attributes VALUES (1, '2026-07-01', 'plan', 9, 2)`); err != nil {
		t.Fatal(err)
	}
	// Ingest's events 0 (here a stray number) for a raw day: counted.
	if _, err := db.db.Exec(`UPDATE received_attributes SET events = 99 WHERE day = '2026-08-02'`); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordUsageHistory(ctx, ts("2026-08-04T03:00:00Z")); err != nil {
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
	want := []string{"2026-08-02/plan=4/{3 true}", "2026-08-04/plan=0/{0 false}"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

// The recount partitions by (family, event, measure, key): a product event
// and a measure sharing the name "boot", and two measures under it, are
// separate partitions, so max_values is the busiest one, never their
// union; events sum across them. The reserved columns count under their
// declarable keys ($path, $device_model) next to the custom ones.
func TestRecordUsageHistoryRecountsPartitionsAndReservedColumns(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	v := 1.0
	day := ts("2026-08-02T10:00:00Z")
	product := func(id, build, path, model string) store.Event {
		return store.Event{ID: id, ProjectID: 1, Family: store.FamilyProduct, EventName: "boot", TS: day,
			ActorID: "a", Path: path, DeviceModel: model, Attributes: map[string]string{"build": build}}
	}
	measure := func(id, m, build, path, model string) store.Event {
		return store.Event{ID: id, ProjectID: 1, Family: store.FamilyMeasures, EventName: "boot", TS: day,
			ActorID: "a", Measure: m, Value: &v, Path: path, DeviceModel: model, Attributes: map[string]string{"build": build}}
	}
	evs := []store.Event{
		// product boot: build {a, b}, $path {/home, /settings}, $device_model {Pixel 8}
		product("p1", "a", "/home", "Pixel 8"),
		product("p2", "b", "/settings", ""),
		// measure boot/time: build {a, c, d}, $device_model {iPhone15,2}
		measure("m1", store.MeasureTime, "a", "", "iPhone15,2"),
		measure("m2", store.MeasureTime, "c", "", "iPhone15,2"),
		measure("m3", store.MeasureTime, "d", "", "iPhone15,2"),
		// measure boot/size: build {e}, $path {/home}, $device_model {Pixel 8}
		measure("m4", store.MeasureSize, "e", "/home", "Pixel 8"),
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordUsageHistory(ctx, ts("2026-08-04T03:00:00Z")); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.Query(`SELECT attr_key, events, max_values FROM received_attributes
		WHERE project_id = 1 AND day = '2026-08-02' ORDER BY attr_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var key string
		var n, mv int64
		if err := rows.Scan(&key, &n, &mv); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s=%d/%d", key, n, mv))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Merged partitions would give build 5 values (or 4 for the two
	// measures together) and $device_model 2.
	want := []string{"$device_model=5/1", "$path=3/2", "build=6/3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}
