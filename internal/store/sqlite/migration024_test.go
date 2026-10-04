package sqlite

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

var measureSeq int

// measureEvent is a measure on 2026-09-01 in project 1 with a fresh id.
func measureEvent(name string, v float64, kind string) store.Event {
	measureSeq++
	return store.Event{ID: fmt.Sprintf("0190bbbb-0000-7000-8000-%012d", measureSeq), ProjectID: 1,
		Family: store.FamilyMeasures, EventName: name, TS: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
		ActorID: "a", Value: &v, Measure: kind, SampleRate: 1}
}

// uuidFor is a fresh event id for tests that need one outside measureEvent.
func uuidFor(t *testing.T) string {
	t.Helper()
	measureSeq++
	return fmt.Sprintf("0190cccc-0000-7000-8000-%012d", measureSeq)
}

func TestMigration024AddsMeasures(t *testing.T) {
	db := newTestDB(t)
	for _, c := range []string{"value", "measure", "sample_rate"} {
		if !hasColumn(t, db, "events", c) {
			t.Errorf("events.%s missing", c)
		}
	}
	for _, tbl := range []string{"agg_measures_daily", "agg_measures_attrs"} {
		if !hasTable(t, db, tbl) {
			t.Errorf("%s missing", tbl)
		}
	}
}

// Rows already stored when 024 runs are views and product events: they
// take no value, no kind, rate 1 and no bucket, and every view answers
// as before.
func TestMigration024KeepsExistingRows(t *testing.T) {
	ctx := context.Background()
	db := newTestDBAt(t, 22) // seed023 writes the pre-023 shape
	seed023(t, db)
	if err := db.migrateThrough(ctx, 23); err != nil {
		t.Fatal(err)
	}
	before := snapshotViews(t, db)
	if err := db.migrateThrough(ctx, 24); err != nil {
		t.Fatal(err)
	}
	var total, clean int
	if err := db.db.QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE value IS NULL AND measure = ''
		AND sample_rate = 1 AND bucket IS NULL) FROM events`).Scan(&total, &clean); err != nil {
		t.Fatal(err)
	}
	if total == 0 || clean != total {
		t.Fatalf("%d of %d existing rows take the defaults", clean, total)
	}
	after := snapshotViews(t, db)
	for v, rows := range before {
		if fmt.Sprint(rows) != fmt.Sprint(after[v]) {
			t.Errorf("%s changed across 024", v)
		}
	}
}

func TestBucketColumn(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, c := range []struct {
		value float64
		want  int
	}{{340, 149}, {0.08, -64}, {1, 0}, {0, -1000}, {1e-18, -1000}} {
		v := c.value
		ev := measureEvent("m", v, "time")
		if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil {
			t.Fatal(err)
		}
		var got int
		if err := db.db.QueryRow(`SELECT bucket FROM raw_measures WHERE id=?`, ev.ID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("bucket(%v) = %d, want %d", c.value, got, c.want)
		}
	}
	p := store.Event{ID: uuidFor(t), ProjectID: 1, Family: store.FamilyProduct, EventName: "signup",
		TS: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC), ActorID: "a"}
	if err := db.WriteEvents(ctx, []store.Event{p}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM events WHERE family<>'measures' AND bucket IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("non-measure rows have a bucket")
	}
}

func TestWriteMeasureRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ev := measureEvent("checkout_api", 340, "time")
	ev.SampleRate = 0.25
	if err := db.WriteEvents(context.Background(), []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	var value, rate float64
	var measure string
	if err := db.db.QueryRow(`SELECT value, measure, sample_rate FROM raw_measures WHERE id=?`, ev.ID).
		Scan(&value, &measure, &rate); err != nil {
		t.Fatal(err)
	}
	if value != 340 || measure != "time" || rate != 0.25 {
		t.Fatalf("got %v %q %v", value, measure, rate)
	}
	// A measure sent with no rate is stored at rate 1.
	unset := measureEvent("checkout_api", 12, "time")
	unset.SampleRate = 0
	if err := db.WriteEvents(context.Background(), []store.Event{unset}); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`SELECT sample_rate FROM raw_measures WHERE id=?`, unset.ID).Scan(&rate); err != nil {
		t.Fatal(err)
	}
	if rate != 1 {
		t.Fatalf("unset sample rate stored as %v, want 1", rate)
	}
	// A product event stores NULL value and sample_rate 1.
	p := store.Event{ID: uuidFor(t), ProjectID: 1, Family: store.FamilyProduct, EventName: "signup",
		TS: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC), ActorID: "a"}
	if err := db.WriteEvents(context.Background(), []store.Event{p}); err != nil {
		t.Fatal(err)
	}
	var isNull bool
	if err := db.db.QueryRow(`SELECT value IS NULL AND sample_rate = 1 FROM raw_product WHERE id=?`, p.ID).Scan(&isNull); err != nil {
		t.Fatal(err)
	}
	if !isNull {
		t.Fatal("product row carries a value or a rate")
	}
}

// The measures family stays out of raw_views and raw_product, and in
// v_events_flat with its three columns.
func TestMeasuresAreTheirOwnFamily(t *testing.T) {
	db := newTestDB(t)
	ev := measureEvent("m", 5, "size")
	if err := db.WriteEvents(context.Background(), []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"raw_views", "raw_product"} {
		var n int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM `+v+` WHERE id=?`, ev.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s holds a measure", v)
		}
	}
	var value, rate float64
	var measure string
	if err := db.db.QueryRow(`SELECT value, measure, sample_rate FROM v_events_flat WHERE id=?`, ev.ID).
		Scan(&value, &measure, &rate); err != nil {
		t.Fatal(err)
	}
	if value != 5 || measure != "size" || rate != 1 {
		t.Fatalf("v_events_flat: %v %q %v", value, measure, rate)
	}
}

func TestMeasureDaysBefore(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a := measureEvent("m", 1, "time")
	b := measureEvent("m", 1, "time")
	b.TS = time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	p := store.Event{ID: uuidFor(t), ProjectID: 1, Family: store.FamilyProduct, EventName: "signup",
		TS: time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC), ActorID: "a"}
	if err := db.WriteEvents(ctx, []store.Event{a, b, p}); err != nil {
		t.Fatal(err)
	}
	got, err := db.MeasureDaysBefore(ctx, 1, day("2026-09-03"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].String() != "2026-09-01" {
		t.Fatalf("MeasureDaysBefore = %v, want [2026-09-01]", got)
	}
}

func approxValue(bucket int) float64 {
	if bucket == -1000 {
		return 0
	}
	return 2 * math.Pow(1.04, float64(bucket)) / 2.04
}

// v_measures_daily joins the rolled-up histogram with the same one
// computed live over raw_measures: samples count rows, weight sums
// 1/sample_rate, sum sums value/sample_rate.
func TestMeasuresDailyView(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a, b, z := measureEvent("api", 340, "time"), measureEvent("api", 341, "time"), measureEvent("api", 0, "time")
	z.SampleRate = 0.5
	if err := db.WriteEvents(ctx, []store.Event{a, b, z}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`INSERT INTO agg_measures_daily
		(project_id, day, event_name, measure, bucket, samples, weight, sum)
		VALUES (1, '2026-08-01', 'api', 'time', 10, 3, 4, 5)`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.Query(`SELECT day, bucket, approx_value, samples, weight, sum
		FROM v_measures_daily WHERE project_id=1 AND event_name='api' AND measure='time' ORDER BY day, bucket`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		day            string
		bucket         int
		approx         float64
		samples        int
		weight, sumVal float64
	}
	want := []row{
		{"2026-08-01", 10, approxValue(10), 3, 4, 5},
		{"2026-09-01", -1000, 0, 1, 2, 0},
		{"2026-09-01", 149, approxValue(149), 2, 2, 681},
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.day, &r.bucket, &r.approx, &r.samples, &r.weight, &r.sumVal); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.day != w.day || g.bucket != w.bucket || g.samples != w.samples ||
			math.Abs(g.approx-w.approx) > 1e-9 || g.weight != w.weight || g.sumVal != w.sumVal {
			t.Errorf("row %d = %+v, want %+v", i, g, w)
		}
	}
}

// v_measures_attrs breaks the live histogram down by the system keys and
// the project's declared keys, capped per key like v_product_attrs, and
// joins the rolled-up rows.
func TestMeasuresAttrsView(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO projects (id, name, attributes) VALUES (1, 'Site', '["plan", "$path"]')`,
		`INSERT OR REPLACE INTO meta (key, value) VALUES ('attributes_top_n', '1')`,
		`INSERT INTO agg_measures_attrs (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum)
		 VALUES (1, '2026-08-01', 'api', 'time', 'plan', 'pro', 10, 3, 4, 5)`,
	} {
		if _, err := db.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var evs []store.Event
	for i, c := range []struct{ platform, plan, path string }{
		{"web", "pro", "/a"}, {"web", "free", "/a"}, {"ios", "pro", ""},
	} {
		ev := measureEvent("api", 340, "time")
		if i == 2 {
			ev.SampleRate = 0.5
		}
		ev.Platform, ev.Path = c.platform, c.path
		ev.Attributes = map[string]string{"plan": c.plan}
		evs = append(evs, ev)
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.Query(`SELECT day, attr_key, attr_value, bucket, approx_value, samples, weight, sum
		FROM v_measures_attrs WHERE project_id=1 AND event_name='api' AND measure='time'
		ORDER BY day, attr_key, attr_value`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var d, k, v string
		var bucket, samples int
		var approx, weight, sum float64
		if err := rows.Scan(&d, &k, &v, &bucket, &approx, &samples, &weight, &sum); err != nil {
			t.Fatal(err)
		}
		if math.Abs(approx-approxValue(bucket)) > 1e-9 {
			t.Errorf("%s %s=%s: approx_value %v for bucket %d", d, k, v, approx, bucket)
		}
		got = append(got, fmt.Sprintf("%s %s=%s b%d n%d w%g s%g", d, k, v, bucket, samples, weight, sum))
	}
	want := []string{
		"2026-08-01 plan=pro b10 n3 w4 s5",
		// $path: "/a" twice, the empty path is absent.
		"2026-09-01 $path=/a b149 n2 w2 s680",
		// top_n = 1: web (2 samples) is kept, ios folds into (other).
		"2026-09-01 $platform=(other) b149 n1 w2 s680",
		"2026-09-01 $platform=web b149 n2 w2 s680",
		// pro (2 samples) is kept, free folds into (other).
		"2026-09-01 plan=(other) b149 n1 w1 s340",
		"2026-09-01 plan=pro b149 n2 w3 s1020",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("v_measures_attrs:\n got  %q\n want %q", got, want)
	}
}
