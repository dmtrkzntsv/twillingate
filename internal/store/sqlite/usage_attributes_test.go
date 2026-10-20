package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// receivedRows reads usage_attributes as "project/day/key=events" lines, sorted.
func receivedRows(t *testing.T, db *DB) []string {
	t.Helper()
	rows, err := db.db.Query(`SELECT project_id, day, attr_key, events FROM usage_attributes`)
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
	if _, err := db.db.Exec(`DELETE FROM usage_attributes`); err != nil {
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
	s.remember([]receivedKey{k("2026-08-01", "a"), k("2026-08-02", "b")}, 0)
	if got, _ := s.unseen(receivedKeys{k("2026-08-01", "a"): {}, k("2026-08-02", "b"): {}}); len(got) != 0 {
		t.Fatalf("unseen = %v, want none", got)
	}
	s.remember([]receivedKey{k("2026-08-03", "c")}, 0)
	got, _ := s.unseen(receivedKeys{k("2026-08-01", "a"): {}, k("2026-08-02", "b"): {}, k("2026-08-03", "c"): {}})
	if !reflect.DeepEqual(got, []receivedKey{k("2026-08-01", "a")}) {
		t.Fatalf("unseen after the newest day moved = %v, want only 08-01's key", got)
	}
	// An older day's key is not remembered at all.
	s.remember([]receivedKey{k("2026-07-30", "old")}, 0)
	if got, _ := s.unseen(receivedKeys{k("2026-07-30", "old"): {}}); len(got) != 1 {
		t.Fatalf("an old day's key was remembered: unseen = %v", got)
	}
}

// catalogRows reads project_attributes as "project/key=breakdown" lines, sorted.
func catalogRows(t *testing.T, db *DB) []string {
	t.Helper()
	rows, err := db.db.Query(`SELECT project_id, key, breakdown FROM project_attributes`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p, b int64
		var key string
		if err := rows.Scan(&p, &key, &b); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d/%s=%d", p, key, b))
	}
	sort.Strings(out)
	return out
}

// Ingest keeps no catalog: project_attributes is read and written by nothing
// until migration 042 drops it.
func TestWriteEventsAddsNoCatalogRows(t *testing.T) {
	db := newTestDB(t)
	if id := seedDeclaredProject(t, db, nil); id != 1 {
		t.Fatalf("project id %d, want 1", id)
	}
	evs := []store.Event{
		{ID: "p1", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup", TS: ts("2026-08-02T10:00:00Z"),
			ActorID: "a", Host: "example.com", Attributes: map[string]string{"plan": "pro"}},
		{ID: "v1", ProjectID: 1, Family: store.FamilyViews, TS: ts("2026-08-02T10:00:00Z"), Kind: "web",
			ActorID: "a", Path: "/", Attributes: map[string]string{"x": "yes"}},
	}
	if err := db.WriteEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
	if got := catalogRows(t, db); len(got) != 0 {
		t.Fatalf("catalog rows = %v, want none", got)
	}
}
