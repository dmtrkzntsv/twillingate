package sqlite

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/google/uuid"
)

// statRow is one server_stats row as a test reads it.
type statRow struct {
	value      int64
	measuredAt string
}

// readStats answers every server_stats row as key -> project -> row.
func readStats(t *testing.T, db *DB) map[string]map[int64]statRow {
	t.Helper()
	rows, err := db.db.Query(`SELECT key, project_id, value, measured_at FROM server_stats`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]map[int64]statRow{}
	for rows.Next() {
		var key string
		var id int64
		var r statRow
		if err := rows.Scan(&key, &id, &r.value, &r.measuredAt); err != nil {
			t.Fatal(err)
		}
		if out[key] == nil {
			out[key] = map[int64]statRow{}
		}
		out[key][id] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func statCount(stats map[string]map[int64]statRow) int {
	n := 0
	for _, byProject := range stats {
		n += len(byProject)
	}
	return n
}

// seedStatsDB: project 1 holds three raw rows (a view, a product event, a
// measure) and two rows of agg_views_daily; project 2 holds one raw row and
// no aggregate.
func seedStatsDB(t *testing.T) *DB {
	t.Helper()
	db := newTestDB(t)
	boot := 120.0
	evs := []store.Event{
		{Family: store.FamilyViews, ID: uuid.NewString(), ProjectID: 1, Kind: "web", ActorKind: store.ActorConnection,
			TS: ts("2026-08-10T10:00:00Z"), ActorID: "v", Path: "/"},
		{Family: store.FamilyProduct, ID: uuid.NewString(), ProjectID: 1, EventName: "subscribed", ActorID: "u1",
			TS: ts("2026-08-10T11:00:00Z")},
		{Family: store.FamilyMeasures, ID: uuid.NewString(), ProjectID: 1, EventName: "boot", ActorID: "c1",
			TS: ts("2026-08-10T12:00:00Z"), Measure: store.MeasureTime, Value: &boot},
		{Family: store.FamilyViews, ID: uuid.NewString(), ProjectID: 2, Kind: "web", ActorKind: store.ActorConnection,
			TS: ts("2026-08-10T10:00:00Z"), ActorID: "w", Path: "/"},
	}
	if err := db.WriteEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`INSERT INTO agg_views_daily VALUES (1,'2026-07-01','web',1,1,1,0,0), (1,'2026-07-02','web',2,2,2,0,0)`); err != nil {
		t.Fatal(err)
	}
	return db
}

// bytesOf reads a table's bytes (indexes included) the way the
// measurement does, but as one table at a time.
func bytesOf(t *testing.T, db *DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.db.QueryRow(`SELECT SUM(d.pgsize) FROM dbstat d JOIN sqlite_schema s ON s.name = d.name
		WHERE d.aggregate = TRUE AND s.tbl_name = ?`, table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Every project with rows gets a raw and an aggregate size, split by its
// share of each table's rows; a project with no aggregate rows has an
// aggregate size of zero, not a missing one.
func TestMeasureServerStats(t *testing.T) {
	db := seedStatsDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 3, 0, 7, 0, time.FixedZone("x", 2*3600))
	if err := db.MeasureServerStats(ctx, now); err != nil {
		t.Fatal(err)
	}
	stats := readStats(t, db)
	if n := statCount(stats); n != 4 {
		t.Fatalf("%d server_stats rows, want 4 (two stats for each of two projects): %v", n, stats)
	}
	wantAt := "2026-08-22T01:00:07Z"
	for key, byProject := range stats {
		for id, r := range byProject {
			if r.measuredAt != wantAt {
				t.Errorf("%s project %d measured_at = %q, want %q (UTC)", key, id, r.measuredAt, wantAt)
			}
		}
	}
	// events holds 3 of 4 rows for project 1 and 1 of 4 for project 2.
	events := bytesOf(t, db, "events")
	if got, want := stats[store.StatRawBytes][1].value, int64(float64(events)*3/4); got != want {
		t.Errorf("project 1 raw_bytes = %d, want %d (3/4 of %d)", got, want, events)
	}
	if got, want := stats[store.StatRawBytes][2].value, int64(float64(events)*1/4); got != want {
		t.Errorf("project 2 raw_bytes = %d, want %d (1/4 of %d)", got, want, events)
	}
	if got, want := stats[store.StatAggregateBytes][1].value, bytesOf(t, db, "agg_views_daily"); got != want {
		t.Errorf("project 1 aggregate_bytes = %d, want %d (all of agg_views_daily)", got, want)
	}
	if r, ok := stats[store.StatAggregateBytes][2]; !ok || r.value != 0 {
		t.Errorf("project 2 aggregate_bytes = %+v (present %v), want a zero row", r, ok)
	}
}

// Running again replaces the values and the time; nothing is added.
func TestMeasureServerStatsReplacesTheLastMeasurement(t *testing.T) {
	db := seedStatsDB(t)
	ctx := context.Background()
	first := time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)
	if err := db.MeasureServerStats(ctx, first); err != nil {
		t.Fatal(err)
	}
	before := readStats(t, db)[store.StatRawBytes][2].value
	// Project 2 now holds three of its own rows out of six.
	if err := db.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyProduct, ID: uuid.NewString(), ProjectID: 2, EventName: "a", ActorID: "u", TS: ts("2026-08-10T13:00:00Z")},
		{Family: store.FamilyProduct, ID: uuid.NewString(), ProjectID: 2, EventName: "b", ActorID: "u", TS: ts("2026-08-10T14:00:00Z")},
	}); err != nil {
		t.Fatal(err)
	}
	second := first.Add(24 * time.Hour)
	if err := db.MeasureServerStats(ctx, second); err != nil {
		t.Fatal(err)
	}
	stats := readStats(t, db)
	if n := statCount(stats); n != 4 {
		t.Errorf("%d rows after a second run, want 4", n)
	}
	if got := stats[store.StatRawBytes][2].value; got <= before {
		t.Errorf("project 2 raw_bytes = %d after it grew from 1 to 3 rows, was %d", got, before)
	}
	for _, r := range stats[store.StatRawBytes] {
		if r.measuredAt != "2026-08-23T03:00:00Z" {
			t.Errorf("measured_at = %q, want the second run's time", r.measuredAt)
		}
	}
}

// A project whose rows are all gone has no size at the next run, so the
// console reads "not measured" rather than a stale figure.
func TestMeasureServerStatsDropsAProjectWithNoRows(t *testing.T) {
	db := seedStatsDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)
	if err := db.MeasureServerStats(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DELETE FROM events WHERE project_id = 2`); err != nil {
		t.Fatal(err)
	}
	if err := db.MeasureServerStats(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	stats := readStats(t, db)
	for key, byProject := range stats {
		if _, ok := byProject[2]; ok {
			t.Errorf("%s still has a row for project 2 after its rows were deleted", key)
		}
	}
	if n := statCount(stats); n != 2 {
		t.Errorf("%d rows, want project 1's two", n)
	}
}

// A database with no data writes nothing, and a project whose only rows
// are its registry entry and an ingest key is not a project with data.
func TestMeasureServerStatsIgnoresTheRegistry(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.CreateProjectWithKey(ctx, store.RegistryProject{Name: "a", AllowedOrigins: "[]", Attributes: "[]"},
		store.RegistryKey{Key: "ak_x", Label: "web"},
		store.AuditEntry{Actor: "t", Action: "project.create"}, store.AuditEntry{Actor: "t", Action: "key.issue"}); err != nil {
		t.Fatal(err)
	}
	if err := db.MeasureServerStats(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := statCount(readStats(t, db)); n != 0 {
		t.Errorf("%d rows for a project with only a key, want 0", n)
	}
}

// DeleteProjectData takes the project's sizes with it.
func TestDeleteProjectDataRemovesServerStats(t *testing.T) {
	db := seedStatsDB(t)
	ctx := context.Background()
	id, err := db.CreateProject(ctx, store.RegistryProject{Name: "a", AllowedOrigins: "[]", Attributes: "[]"},
		store.AuditEntry{Actor: "t", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{store.StatRawBytes, store.StatAggregateBytes} {
		for _, p := range []int64{id, id + 1} {
			if _, err := db.db.Exec(`INSERT INTO server_stats VALUES (?, ?, 5, '2026-08-22T03:00:00Z')`, key, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.DeleteProjectData(ctx, id, store.AuditEntry{Actor: "t", Action: "project.delete"}); err != nil {
		t.Fatal(err)
	}
	stats := readStats(t, db)
	for key, byProject := range stats {
		if _, ok := byProject[id]; ok {
			t.Errorf("%s still has a row for the deleted project", key)
		}
		if _, ok := byProject[id+1]; !ok {
			t.Errorf("%s lost another project's row", key)
		}
	}
}

// A table's bytes times a project's rows would overflow int64 long before
// either is unusual; the share is computed in floating point.
func TestShareOfBytesDoesNotOverflow(t *testing.T) {
	if got := shareOfBytes(math.MaxInt64, 3, 4); got < math.MaxInt64/2 || got > math.MaxInt64 {
		t.Errorf("share of MaxInt64 by 3/4 = %d", got)
	}
	if got := shareOfBytes(4096, 1, 4); got != 1024 {
		t.Errorf("share of 4096 by 1/4 = %d, want 1024", got)
	}
	if got := shareOfBytes(4096, 0, 4); got != 0 {
		t.Errorf("a project with no rows has a share of %d, want 0", got)
	}
	if got := shareOfBytes(4096, 4, 4); got != 4096 {
		t.Errorf("all the rows is %d, want the whole table", got)
	}
}
