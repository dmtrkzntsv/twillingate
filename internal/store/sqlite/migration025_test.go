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

// The point of 025: a query's project and day reach the raw rows, so each
// live half runs over the days the range covers and none other. Pinned on
// the plan, since a result cannot show which rows were read.
func TestAttrsLiveHalvesReadOnlyTheRange(t *testing.T) {
	db := newTestDB(t)
	for _, view := range []string{"v_product_attrs", "v_measures_attrs"} {
		rows, err := db.db.Query(`EXPLAIN QUERY PLAN SELECT * FROM `+view+`
			WHERE project_id = ? AND day BETWEEN ? AND ?`, 1, "2026-08-01", "2026-08-07")
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		var raw []string
		for _, d := range plan {
			if strings.Contains(d, "events") {
				raw = append(raw, d)
			}
		}
		want := "USING PRIMARY KEY (family=? AND project_id=? AND day>? AND day<?)"
		if len(raw) != 1 || !strings.Contains(raw[0], want) {
			t.Errorf("%s reads events %q, want one read %s\nplan:\n%s", view, raw, want, strings.Join(plan, "\n"))
		}
	}
}
