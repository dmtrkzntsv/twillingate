package sqlite

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The rollup writes one histogram row per (metric, bucket): samples count
// rows, weight sums 1/sample_rate and sum sums value/sample_rate, exactly;
// then the day's raw rows are gone.
func TestAggregateMeasureDayHistogram(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var evs []store.Event
	for _, v := range []float64{100, 100, 340, 0} {
		ev := measureEvent("checkout_api", v, "time")
		if v == 0 {
			ev.SampleRate = 0.5
		}
		evs = append(evs, ev)
	}
	evs = append(evs, measureEvent("$cls", 0.08, "number"))
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateMeasureDay(ctx, 1, day("2026-09-01"), nil, defaultAttrsTopN); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.Query(`SELECT event_name, measure, bucket, samples, weight, sum
		FROM agg_measures_daily WHERE project_id=1 AND day='2026-09-01'
		ORDER BY event_name, measure, bucket`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name, measure string
		var bucket, samples int
		var weight, sum float64
		if err := rows.Scan(&name, &measure, &bucket, &samples, &weight, &sum); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s/%s b%d n%d w%g s%g", name, measure, bucket, samples, weight, sum))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"$cls/number b-64 n1 w1 s0.08",
		"checkout_api/time b-1000 n1 w2 s0",
		fmt.Sprintf("checkout_api/time b%d n2 w2 s200", bucketOf(100)),
		"checkout_api/time b149 n1 w1 s340",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agg_measures_daily:\n got  %q\n want %q", got, want)
	}
	var raw int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM raw_measures WHERE project_id=1 AND day='2026-09-01'`).
		Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != 0 {
		t.Fatalf("%d raw measures left for the rolled-up day", raw)
	}
}

// A non-positive topN falls back to the default cap rather than folding
// every value into (other); a declared $ key the store does not map
// breaks nothing down; a day with no raw rows left is a no-op.
func TestAggregateMeasureDayDefaultsAndNoOp(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var evs []store.Event
	for i := 0; i < 3; i++ {
		ev := measureEvent("checkout_api", 340, "time")
		ev.Attributes = map[string]string{"plan": fmt.Sprintf("p%d", i)}
		evs = append(evs, ev)
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	attrs := []string{"plan", "$bogus"}
	if err := db.AggregateMeasureDay(ctx, 1, day("2026-09-01"), attrs, 0); err != nil {
		t.Fatal(err)
	}
	read := func() []string {
		return snapshotRows(t, db, `SELECT attr_key, attr_value, samples FROM agg_measures_attrs
			WHERE attr_key IN ('plan', '$bogus') ORDER BY 1, 2`)
	}
	want := []string{
		`[]interface {}{"plan", "p0", 1}`,
		`[]interface {}{"plan", "p1", 1}`,
		`[]interface {}{"plan", "p2", 1}`,
	}
	got := read()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("topN 0:\n got  %q\n want %q", got, want)
	}
	if err := db.AggregateMeasureDay(ctx, 1, day("2026-09-01"), attrs, 0); err != nil {
		t.Fatal(err)
	}
	if again := read(); !reflect.DeepEqual(again, got) {
		t.Fatalf("second run changed the rollup:\n got  %q\n want %q", again, got)
	}
}

// bucketOf is the bucket column's formula in Go.
func bucketOf(v float64) int {
	if v <= 1e-17 {
		return -1000
	}
	return int(math.Ceil(math.Log(v) / math.Log(1.04)))
}

// snapshotRows renders every row of q as text, in the query's order.
func snapshotRows(t *testing.T, db *DB, q string) []string {
	t.Helper()
	rows, err := db.db.Query(q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%#v", vals))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Both measure views answer the same before and after their day is rolled
// up: the rollup writes exactly what the live half computes, with the same
// keys, the same cap, the same tie-break (samples, then value ascending),
// the same per-bucket rows and the same (other) -- including a client
// value that is literally "(other)", which the live half merges with the
// tail.
func TestMeasuresViewsSameAcrossRollup(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	id := seedDeclaredProject(t, db, []string{"endpoint", "$path"})
	if err := db.SetMeta(ctx, "product_attributes_top_n", "2"); err != nil {
		t.Fatal(err)
	}
	// Nine samples of checkout_api. Every kept value spans at least two
	// buckets (the values cycle 100, 340, 0, and the last is 5000).
	//   endpoint: "(other)" 4, b 2, c 2, d 1 -> keeps "(other)" and b (b
	//     beats c on the tie), c and d merge into the literal "(other)".
	//   $path: /a 3, /b 2, /c 1, absent 3 -> /c goes to (other).
	//   $browser: Chrome 3, Edge 2, Firefox 2, Safari 2 -> a three-way tie
	//     at the boundary: Edge is kept, Firefox and Safari fold.
	endpoints := []string{"(other)", "(other)", "(other)", "(other)", "b", "b", "c", "c", "d"}
	paths := []string{"/a", "/b", "", "/a", "/c", "/b", "", "/a", ""}
	browsers := []string{"Chrome", "Firefox", "Chrome", "Safari", "Edge", "Chrome", "Firefox", "Safari", "Edge"}
	values := []float64{100, 340, 0}
	var evs []store.Event
	for i := range endpoints {
		v := values[i%3]
		if i == 8 {
			v = 5000 // a tail bucket the literal "(other)" does not have
		}
		ev := measureEvent("checkout_api", v, "time")
		ev.ProjectID = id
		ev.Attributes = map[string]string{"endpoint": endpoints[i]}
		ev.Path, ev.Browser = paths[i], browsers[i]
		ev.Device = []string{"desktop", "mobile"}[i%2]
		switch {
		case v == 0:
			ev.SampleRate = 0.5
		case i == 1:
			ev.SampleRate = 0.25
		}
		evs = append(evs, ev)
	}
	// A second metric, and one on the next day, which stays raw.
	for _, v := range []float64{1200, 2500} {
		ev := measureEvent("$lcp", v, "time")
		ev.ProjectID, ev.Path, ev.Browser = id, "/a", "Chrome"
		evs = append(evs, ev)
	}
	next := measureEvent("checkout_api", 340, "time")
	next.ProjectID, next.TS = id, time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	next.Attributes = map[string]string{"endpoint": "b"}
	evs = append(evs, next)
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}

	const dailyQ = `SELECT * FROM v_measures_daily ORDER BY 1,2,3,4,5`
	const attrsQ = `SELECT * FROM v_measures_attrs ORDER BY 1,2,3,4,5,6,7`
	daily, attrs := snapshotRows(t, db, dailyQ), snapshotRows(t, db, attrsQ)

	// The fixture reaches every case it claims to: samples per value on
	// the rolled-up day, summed over buckets.
	for _, c := range []struct {
		key, value string
		want       int
	}{
		{"endpoint", "(other)", 7}, {"endpoint", "b", 2}, {"endpoint", "c", 0},
		{"$path", "(other)", 1}, {"$path", "/a", 3},
		{"$browser", "Edge", 2}, {"$browser", "(other)", 4}, {"$browser", "Firefox", 0},
	} {
		var n int
		if err := db.db.QueryRow(`SELECT COALESCE(SUM(samples), 0) FROM v_measures_attrs
			WHERE day='2026-09-01' AND event_name='checkout_api' AND attr_key=? AND attr_value=?`,
			c.key, c.value).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != c.want {
			t.Errorf("fixture: %s=%s has %d samples, want %d", c.key, c.value, n, c.want)
		}
	}
	var spread int
	if err := db.db.QueryRow(`SELECT MIN(buckets) FROM (SELECT COUNT(DISTINCT bucket) AS buckets
		FROM v_measures_attrs WHERE day='2026-09-01' AND event_name='checkout_api'
		  AND attr_value <> '(other)' GROUP BY attr_key, attr_value)`).Scan(&spread); err != nil {
		t.Fatal(err)
	}
	if spread < 2 {
		t.Errorf("fixture: a kept value spans %d bucket(s), want at least 2", spread)
	}

	// endpoint is listed twice: the live half reads the declaration once,
	// so the rollup must not add its (other) tail twice.
	if err := db.AggregateMeasureDay(ctx, id, day("2026-09-01"), []string{"endpoint", "$path", "endpoint"}, 2); err != nil {
		t.Fatal(err)
	}
	var raw, rolled int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM raw_measures WHERE day='2026-09-01'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM agg_measures_attrs`).Scan(&rolled); err != nil {
		t.Fatal(err)
	}
	if raw != 0 || rolled == 0 {
		t.Fatalf("after the rollup: %d raw rows, %d attribute rows", raw, rolled)
	}
	if after := snapshotRows(t, db, dailyQ); !reflect.DeepEqual(daily, after) {
		t.Errorf("v_measures_daily changed across the rollup:\nbefore %s\nafter  %s", daily, after)
	}
	if after := snapshotRows(t, db, attrsQ); !reflect.DeepEqual(attrs, after) {
		t.Errorf("v_measures_attrs changed across the rollup:\nbefore %s\nafter  %s", attrs, after)
	}
}

// percentileSQL is the documented pattern (spec decision 19), for p50, p75
// and p95 at once.
const percentileSQL = `
WITH h AS (SELECT event_name, bucket, approx_value, SUM(weight) AS w FROM v_measures_daily
           WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY 1, 2, 3),
     c AS (SELECT *, SUM(w) OVER (PARTITION BY event_name ORDER BY bucket) AS run,
                     SUM(w) OVER (PARTITION BY event_name) AS total FROM h)
SELECT event_name,
       MIN(approx_value) FILTER (WHERE run >= 0.50 * total) AS p50,
       MIN(approx_value) FILTER (WHERE run >= 0.75 * total) AS p75,
       MIN(approx_value) FILTER (WHERE run >= 0.95 * total) AS p95
FROM c GROUP BY 1`

func percentiles(t *testing.T, db *DB, projectID int64, from, to string) map[string][3]float64 {
	t.Helper()
	rows, err := db.db.Query(percentileSQL, projectID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][3]float64{}
	for rows.Next() {
		var name string
		var p [3]float64
		if err := rows.Scan(&name, &p[0], &p[1], &p[2]); err != nil {
			t.Fatal(err)
		}
		out[name] = p
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// exactQuantile is the nearest-rank quantile of sorted values.
func exactQuantile(sorted []float64, q float64) float64 {
	return sorted[int(math.Ceil(q*float64(len(sorted))))-1]
}

// Percentiles read from the buckets stay within 2% of the exact ones, on
// the live half and on the rolled-up histogram alike.
func TestMeasurePercentilesWithinTwoPercent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := rand.New(rand.NewPCG(24, 4))
	dists := []struct {
		name string
		gen  func(i int) float64
	}{
		{"uniform", func(int) float64 { return 1 + r.Float64()*4999 }},
		{"lognormal", func(int) float64 { return math.Exp(r.NormFloat64()*1 + 6) }},
		{"zero_heavy", func(i int) float64 { return float64(i%2) * (1 + r.Float64()*4999) }},
	}
	exact := map[string][]float64{}
	var evs []store.Event
	for _, d := range dists {
		name, gen := d.name, d.gen
		for i := 0; i < 2000; i++ {
			v := gen(i)
			exact[name] = append(exact[name], v)
			evs = append(evs, measureEvent(name, v, "time"))
		}
		sort.Float64s(exact[name])
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	check := func(stage string) {
		got := percentiles(t, db, 1, "2026-09-01", "2026-09-01")
		for name, vals := range exact {
			for i, q := range []float64{0.50, 0.75, 0.95} {
				want, g := exactQuantile(vals, q), got[name][i]
				t.Logf("%s %s p%.0f: got %.3f, exact %.3f (%.2f%%)", stage, name, q*100, g, want,
					100*math.Abs(g-want)/math.Max(want, 1e-300))
				if math.Abs(g-want) > 0.02*want+1e-9 {
					t.Errorf("%s %s p%.0f = %v, exact %v: off by more than 2%%", stage, name, q*100, g, want)
				}
			}
		}
		if p50 := got["zero_heavy"][0]; p50 != 0 {
			t.Errorf("%s zero_heavy p50 = %v, want exactly 0", stage, p50)
		}
	}
	check("live")
	if err := db.AggregateMeasureDay(ctx, 1, day("2026-09-01"), nil, defaultAttrsTopN); err != nil {
		t.Fatal(err)
	}
	check("rolled up")
}

// A metric that is always 0 reads 0 at every percentile, not a tiny
// positive number from a bucket formula.
func TestAllZeroMetricPercentileIsZero(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var evs []store.Event
	for i := 0; i < 10; i++ {
		evs = append(evs, measureEvent("$cls", 0, "number"))
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"live", "rolled up"} {
		if stage == "rolled up" {
			if err := db.AggregateMeasureDay(ctx, 1, day("2026-09-01"), nil, defaultAttrsTopN); err != nil {
				t.Fatal(err)
			}
		}
		got, ok := percentiles(t, db, 1, "2026-09-01", "2026-09-01")["$cls"]
		if !ok || got != [3]float64{0, 0, 0} {
			t.Fatalf("%s: $cls p50/p75/p95 = %v (present %v), want all 0", stage, got, ok)
		}
	}
}

// A sampled metric reads like the unsampled one it samples (spec decision
// 6: each stored row counts as 1/rate). One distribution, two populations
// (fast and slow page loads), is written three ways: "full", every sample
// at rate 1; "sampled", every 4th sample of both at rate 0.25; "mixed",
// every fast sample at rate 1 and every 4th slow one at 0.25, where
// dropping the weights would over-count the fast half four to one and pull
// every percentile down. Both read p50/p75/p95 and est_count within 10% of
// "full", on the live half and on the rolled-up histogram alike.
func TestSampledMeasuresMatchUnsampled(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := rand.New(rand.NewPCG(6, 25))
	const n = 8000
	var evs []store.Event
	add := func(name string, v, rate float64) {
		ev := measureEvent(name, v, "time")
		ev.SampleRate = rate
		evs = append(evs, ev)
	}
	for i := 0; i < n; i++ {
		slow := i%2 == 1
		mu := 6.5 // fast loads, ~650 ms
		if slow {
			mu = 8 // slow loads, ~3 s
		}
		v := math.Exp(r.NormFloat64()*0.5 + mu)
		add("full", v, 1)
		if i%8 < 2 { // every 4th of each population
			add("sampled", v, 0.25)
		}
		switch {
		case !slow:
			add("mixed", v, 1)
		case i%8 == 1:
			add("mixed", v, 0.25)
		}
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	within := func(got, want float64) bool { return math.Abs(got-want) <= 0.10*want }
	check := func(stage string) {
		p := percentiles(t, db, 1, "2026-09-01", "2026-09-01")
		for _, name := range []string{"sampled", "mixed"} {
			for i, q := range []string{"p50", "p75", "p95"} {
				full, got := p["full"][i], p[name][i]
				t.Logf("%s %s %s: %.1f, unsampled %.1f", stage, name, q, got, full)
				if full <= 0 || !within(got, full) {
					t.Errorf("%s %s %s = %v, unsampled %v: more than 10%% apart", stage, name, q, got, full)
				}
			}
		}
		counts := map[string]float64{}
		rows, err := db.db.Query(`SELECT event_name, SUM(weight) FROM v_measures_daily
			WHERE project_id = 1 AND day = '2026-09-01' GROUP BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			var w float64
			if err := rows.Scan(&name, &w); err != nil {
				t.Fatal(err)
			}
			counts[name] = w
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if counts["full"] != n {
			t.Errorf("%s est_count full = %v, want %d", stage, counts["full"], n)
		}
		for _, name := range []string{"sampled", "mixed"} {
			if !within(counts[name], counts["full"]) {
				t.Errorf("%s est_count %s = %v, unsampled %v: more than 10%% apart", stage, name, counts[name], counts["full"])
			}
		}
	}
	check("live")
	if err := db.AggregateMeasureDay(ctx, 1, day("2026-09-01"), nil, defaultAttrsTopN); err != nil {
		t.Fatal(err)
	}
	check("rolled up")
}
