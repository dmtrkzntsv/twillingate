package sqlite

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/google/uuid"
)

// statID names one usage_history row: a stat, a project and a day.
type statID struct {
	key     string
	project int64
	day     string
}

// readStats answers every usage_history row's value by its key.
func readStats(t *testing.T, db *DB) map[statID]int64 {
	t.Helper()
	rows, err := db.db.Query(`SELECT key, project_id, measured_at, value FROM usage_history`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[statID]int64{}
	for rows.Next() {
		var id statID
		var v int64
		if err := rows.Scan(&id.key, &id.project, &id.day, &v); err != nil {
			t.Fatal(err)
		}
		out[id] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// sizes keeps the per-project size rows of stats.
func sizes(stats map[statID]int64) map[statID]int64 {
	out := map[statID]int64{}
	for id, v := range stats {
		if id.key == store.StatRawBytes || id.key == store.StatAggregateBytes {
			out[id] = v
		}
	}
	return out
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
// share of each table's rows, on the UTC day of the run; a project with no
// aggregate rows has an aggregate size of zero, not a missing one.
func TestRecordUsageHistory(t *testing.T) {
	db := seedStatsDB(t)
	ctx := context.Background()
	// 01:00 at +02:00 is still the 21st in UTC.
	now := time.Date(2026, 8, 22, 1, 0, 7, 0, time.FixedZone("x", 2*3600))
	if err := db.RecordUsageHistory(ctx, now); err != nil {
		t.Fatal(err)
	}
	all := readStats(t, db)
	stats := sizes(all)
	if len(stats) != 4 {
		t.Fatalf("%d size rows, want 4 (two stats for each of two projects): %v", len(stats), stats)
	}
	const day = "2026-08-21"
	if v := all[statID{store.StatDatabaseBytes, 0, day}]; v <= 0 {
		t.Errorf("database_bytes on %s = %d, want the file's size", day, v)
	}
	// events holds 3 of 4 rows for project 1 and 1 of 4 for project 2.
	events := bytesOf(t, db, "events")
	if got, want := stats[statID{store.StatRawBytes, 1, day}], int64(float64(events)*3/4); got != want {
		t.Errorf("project 1 raw_bytes on %s = %d, want %d (3/4 of %d); rows %v", day, got, want, events, stats)
	}
	if got, want := stats[statID{store.StatRawBytes, 2, day}], int64(float64(events)*1/4); got != want {
		t.Errorf("project 2 raw_bytes = %d, want %d (1/4 of %d)", got, want, events)
	}
	if got, want := stats[statID{store.StatAggregateBytes, 1, day}], bytesOf(t, db, "agg_views_daily"); got != want {
		t.Errorf("project 1 aggregate_bytes = %d, want %d (all of agg_views_daily)", got, want)
	}
	if v, ok := stats[statID{store.StatAggregateBytes, 2, day}]; !ok || v != 0 {
		t.Errorf("project 2 aggregate_bytes = %d (present %v), want a zero row", v, ok)
	}
}

// A second run on the same day (the pass runs at start and at 03:00)
// replaces that day's values; nothing is added.
func TestRecordUsageHistoryReplacesTheSameDay(t *testing.T) {
	db := seedStatsDB(t)
	ctx := context.Background()
	first := time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)
	if err := db.RecordUsageHistory(ctx, first); err != nil {
		t.Fatal(err)
	}
	p2 := statID{store.StatRawBytes, 2, "2026-08-22"}
	before := readStats(t, db)[p2]
	growProject2(t, db)
	if err := db.RecordUsageHistory(ctx, first.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	stats := sizes(readStats(t, db))
	if len(stats) != 4 {
		t.Errorf("%d size rows after a second run the same day, want 4: %v", len(stats), stats)
	}
	if got := stats[p2]; got <= before {
		t.Errorf("project 2 raw_bytes = %d after it grew from 1 to 3 rows, was %d", got, before)
	}
}

// A run on a new day adds that day's rows and leaves the earlier days: the
// table is a daily history.
func TestRecordUsageHistoryKeepsEarlierDays(t *testing.T) {
	db := seedStatsDB(t)
	ctx := context.Background()
	first := time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)
	if err := db.RecordUsageHistory(ctx, first); err != nil {
		t.Fatal(err)
	}
	day1 := sizes(readStats(t, db))
	growProject2(t, db)
	if err := db.RecordUsageHistory(ctx, first.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	stats := sizes(readStats(t, db))
	if len(stats) != 8 {
		t.Fatalf("%d size rows after two days, want 8: %v", len(stats), stats)
	}
	for id, v := range day1 {
		if stats[id] != v {
			t.Errorf("%v = %d after the next day's run, was %d", id, stats[id], v)
		}
	}
	if a, b := stats[statID{store.StatRawBytes, 2, "2026-08-22"}], stats[statID{store.StatRawBytes, 2, "2026-08-23"}]; b <= a {
		t.Errorf("project 2 raw_bytes went %d -> %d after it grew from 1 to 3 rows", a, b)
	}
}

// growProject2 gives project 2 two more raw rows: three of six.
func growProject2(t *testing.T, db *DB) {
	t.Helper()
	if err := db.WriteEvents(context.Background(), []store.Event{
		{Family: store.FamilyProduct, ID: uuid.NewString(), ProjectID: 2, EventName: "a", ActorID: "u", TS: ts("2026-08-10T13:00:00Z")},
		{Family: store.FamilyProduct, ID: uuid.NewString(), ProjectID: 2, EventName: "b", ActorID: "u", TS: ts("2026-08-10T14:00:00Z")},
	}); err != nil {
		t.Fatal(err)
	}
}

// A project whose rows are all gone has no size on the next run's day,
// whether that is the same day (its rows for the day go) or a later one
// (its earlier days stay, as history).
func TestRecordUsageHistoryDropsAProjectWithNoRows(t *testing.T) {
	db := seedStatsDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)
	if err := db.RecordUsageHistory(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DELETE FROM events WHERE project_id = 2`); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordUsageHistory(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	stats := sizes(readStats(t, db))
	for id := range stats {
		if id.project == 2 {
			t.Errorf("%v still there after project 2's rows were deleted the same day", id)
		}
	}
	if len(stats) != 2 {
		t.Errorf("%d rows, want project 1's two", len(stats))
	}
	if err := db.RecordUsageHistory(ctx, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if stats := sizes(readStats(t, db)); len(stats) != 4 {
		t.Errorf("%d rows the next day, want project 1's two per day: %v", len(stats), stats)
	}
}

// A database with no data writes nothing, and a project whose only rows
// are its registry entry and an ingest key is not a project with data.
func TestRecordUsageHistoryIgnoresTheRegistry(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.CreateProjectWithKey(ctx, store.RegistryProject{Name: "a", AllowedOrigins: "[]", Attributes: "[]"},
		store.RegistryKey{Key: "ak_x", Label: "web"},
		store.AuditEntry{Actor: "t", Action: "project.create"}, store.AuditEntry{Actor: "t", Action: "key.issue"}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordUsageHistory(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	for id, v := range readStats(t, db) {
		if id.key == store.StatDeclaredAttributes {
			if v != 0 {
				t.Errorf("%v = %d, want 0 declared", id, v)
			}
			continue
		}
		if id.project != 0 {
			t.Errorf("%v for a project with only a key, want none", id)
		}
	}
}

// DeleteProjectData takes the project's sizes with it.
func TestDeleteProjectDataRemovesUsageHistory(t *testing.T) {
	db := seedStatsDB(t)
	ctx := context.Background()
	id, err := db.CreateProject(ctx, store.RegistryProject{Name: "a", AllowedOrigins: "[]", Attributes: "[]"},
		store.AuditEntry{Actor: "t", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{store.StatRawBytes, store.StatAggregateBytes} {
		for _, p := range []int64{id, id + 1} {
			for _, day := range []string{"2026-08-21", "2026-08-22"} {
				if _, err := db.db.Exec(`INSERT INTO usage_history (key, project_id, measured_at, value) VALUES (?, ?, ?, 5)`, key, p, day); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := db.DeleteProjectData(ctx, id, store.AuditEntry{Actor: "t", Action: "project.delete"}); err != nil {
		t.Fatal(err)
	}
	others := 0
	for row := range readStats(t, db) {
		if row.project == id {
			t.Errorf("%v survived the project's deletion", row)
		} else {
			others++
		}
	}
	if others != 4 {
		t.Errorf("%d rows of another project left, want its 4", others)
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

// Counts: every day before the run's, from the aggregates where rolled up
// and the raw rows where not, per project and family; today's rows wait
// for tomorrow's run.
func TestRecordUsageHistoryCountsDays(t *testing.T) {
	db := seedStatsDB(t)
	ctx := context.Background()
	if err := db.WriteEvents(ctx, []store.Event{{Family: store.FamilyViews, ID: uuid.NewString(), ProjectID: 1, Kind: "web",
		ActorKind: store.ActorConnection, TS: ts("2026-08-22T01:00:00Z"), ActorID: "v", Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordUsageHistory(ctx, time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	stats := readStats(t, db)
	for id, want := range map[statID]int64{
		{store.StatViews, 1, "2026-07-01"}:    1, // rolled up
		{store.StatViews, 1, "2026-07-02"}:    2,
		{store.StatViews, 1, "2026-08-10"}:    1, // raw
		{store.StatEvents, 1, "2026-08-10"}:   1,
		{store.StatMeasures, 1, "2026-08-10"}: 1,
		{store.StatViews, 2, "2026-08-10"}:    1,
	} {
		if got, ok := stats[id]; !ok || got != want {
			t.Errorf("%v = %d (present %v), want %d", id, got, ok, want)
		}
	}
	for id := range stats {
		if id.key == store.StatViews && id.day == "2026-08-22" {
			t.Errorf("%v: today is counted tomorrow", id)
		}
		if id.key == store.StatEvents && id.project == 2 {
			t.Errorf("%v: a project with no events has no events row", id)
		}
	}
}

// A late event on a raw day is counted on the next run; a day whose
// aggregates are pruned keeps its count.
func TestRecordUsageHistoryRecountsAndOutlivesTheAggregates(t *testing.T) {
	db := seedStatsDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)
	if err := db.RecordUsageHistory(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteEvents(ctx, []store.Event{{Family: store.FamilyViews, ID: uuid.NewString(), ProjectID: 1, Kind: "web",
		ActorKind: store.ActorConnection, TS: ts("2026-08-10T23:00:00Z"), ActorID: "late", Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DELETE FROM agg_views_daily WHERE day = '2026-07-01'`); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordUsageHistory(ctx, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	stats := readStats(t, db)
	if got := stats[statID{store.StatViews, 1, "2026-08-10"}]; got != 2 {
		t.Errorf("views on 2026-08-10 = %d after a late view, want 2", got)
	}
	if got, ok := stats[statID{store.StatViews, 1, "2026-07-01"}]; !ok || got != 1 {
		t.Errorf("views on 2026-07-01 = %d (present %v) after its aggregates were pruned, want the 1 counted", got, ok)
	}
}

// attrsDB holds one project declaring "plan", with one raw day of product
// events (plan: free, pro, team; $os: windows, macos; an undeclared "ref")
// and measures (plan: free, pro), under a attributes_top_n of cap.
func attrsDB(t *testing.T, cap string) (*DB, int64) {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()
	id, err := db.CreateProject(ctx, store.RegistryProject{Name: "a", AllowedOrigins: "[]", Attributes: `["plan"]`},
		store.AuditEntry{Actor: "t", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES ('attributes_top_n', ?)`, cap); err != nil {
		t.Fatal(err)
	}
	var evs []store.Event
	for i, plan := range []string{"free", "free", "pro", "team"} {
		evs = append(evs, store.Event{Family: store.FamilyProduct, ID: uuid.NewString(), ProjectID: id, EventName: "signup",
			ActorID: "u", TS: ts("2026-08-10T10:00:00Z").Add(time.Duration(i) * time.Minute),
			OS: []string{"windows", "windows", "macos", "windows"}[i], Attributes: map[string]string{"plan": plan, "ref": "x"}})
	}
	boot := 120.0
	for i, plan := range []string{"free", "pro"} {
		evs = append(evs, store.Event{Family: store.FamilyMeasures, ID: uuid.NewString(), ProjectID: id, EventName: "boot",
			ActorID: "c", TS: ts("2026-08-10T12:00:00Z").Add(time.Duration(i) * time.Minute), Measure: store.MeasureTime, Value: &boot,
			Attributes: map[string]string{"plan": plan}})
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	return db, id
}

// Attributes: what each project declares, and per raw day the keys and
// values received (declared or not) and the values folded past the cap.
func TestRecordUsageHistoryCountsAttributes(t *testing.T) {
	db, id := attrsDB(t, "1")
	if err := db.RecordUsageHistory(context.Background(), time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	stats := readStats(t, db)
	for key, want := range map[statID]int64{
		{store.StatDeclaredAttributes, id, "2026-08-22"}: 1,
		{store.StatAttributeKeys, id, "2026-08-10"}:      2, // plan, ref
		{store.StatAttributeValues, id, "2026-08-10"}:    4, // plan: free, pro, team; ref: x
		// Product: plan keeps 1 of 3, $os 1 of 2; measures: plan 1 of 2.
		{store.StatAttributeValuesFolded, id, "2026-08-10"}: 4,
	} {
		if got, ok := stats[key]; !ok || got != want {
			t.Errorf("%v = %d (present %v), want %d", key, got, ok, want)
		}
	}
	// The views fold exactly there: an (other) row for plan and $os.
	var others int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM v_product_attrs WHERE project_id = ? AND attr_value = '(other)'`, id).Scan(&others); err != nil {
		t.Fatal(err)
	}
	if others != 2 {
		t.Errorf("v_product_attrs has %d (other) rows, want 2 (plan and $os)", others)
	}
}

// With no cap nothing folds, and the day still gets its row: 0, counted.
func TestRecordUsageHistoryFoldsNothingWithoutACap(t *testing.T) {
	db, id := attrsDB(t, "0")
	if err := db.RecordUsageHistory(context.Background(), time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if got, ok := readStats(t, db)[statID{store.StatAttributeValuesFolded, id, "2026-08-10"}]; !ok || got != 0 {
		t.Errorf("folded = %d (present %v), want a 0 row", got, ok)
	}
}

// Once a day's raw rows are gone (rolled up, keeping only what the cap let
// through), its attribute counts stay as they were counted while raw.
func TestRecordUsageHistoryKeepsAttributeCountsPastTheRawDays(t *testing.T) {
	db, id := attrsDB(t, "1")
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)
	if err := db.RecordUsageHistory(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DELETE FROM events WHERE project_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordUsageHistory(ctx, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	stats := readStats(t, db)
	for key, want := range map[string]int64{store.StatAttributeKeys: 2, store.StatAttributeValues: 4, store.StatAttributeValuesFolded: 4} {
		if got := stats[statID{key, id, "2026-08-10"}]; got != want {
			t.Errorf("%s on 2026-08-10 = %d after its raw rows went, want the %d counted while raw", key, got, want)
		}
	}
}

// The caps in force are recorded for the day, server-wide: the meta value
// the views read, 0 for no cap, else the default.
func TestRecordUsageHistoryRecordsTheCaps(t *testing.T) {
	db, _ := attrsDB(t, "0") // product attributes: no cap
	ctx := context.Background()
	if _, err := db.db.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES ('attribute_breakdowns_max', '12')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DELETE FROM meta WHERE key = 'identities_top_n'`); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordUsageHistory(ctx, time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	stats := readStats(t, db)
	for key, want := range map[string]int64{
		store.StatCapAttributes: 0,
		store.StatCapIdentities: 1000, // unset: the default
		store.StatCapBreakdowns: 12,
	} {
		if got, ok := stats[statID{key, 0, "2026-08-22"}]; !ok || got != want {
			t.Errorf("%s = %d (present %v), want %d", key, got, ok, want)
		}
	}
}
