package sqlite

import (
	"context"
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
