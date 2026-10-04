package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Migration 027 moves the attribute cap's meta row to attributes_top_n
// with its value, and the attribute views read it there: a cap of 2 set
// under the old name still folds the same values after the migration.
func TestMigration027MovesTheAttributeCap(t *testing.T) {
	db := newTestDBAt(t, 26)
	ctx := context.Background()
	if err := db.SetMeta(ctx, "product_attributes_top_n", "2"); err != nil {
		t.Fatal(err)
	}
	id := seedDeclaredProject(t, db, []string{"plan"})
	boot := 120.0
	var evs []store.Event
	for i, plan := range []string{"free", "free", "free", "pro", "pro", "team", "edu"} {
		evs = append(evs,
			store.Event{ID: fmt.Sprintf("p%d", i), ProjectID: id, Family: store.FamilyProduct, EventName: "signup",
				TS: ts("2026-08-02T10:00:00Z"), ActorID: "a", Attributes: map[string]string{"plan": plan}},
			store.Event{ID: fmt.Sprintf("m%d", i), ProjectID: id, Family: store.FamilyMeasures, EventName: "boot",
				TS: ts("2026-08-02T11:00:00Z"), ActorID: "c", Measure: store.MeasureTime, Value: &boot,
				Attributes: map[string]string{"plan": plan}})
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	measures := func() []string {
		rows, err := db.db.Query(`SELECT attr_value, SUM(samples) FROM v_measures_attrs WHERE project_id = ? GROUP BY 1 ORDER BY 1`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var v string
			var n int
			if err := rows.Scan(&v, &n); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%s=%d", v, n))
		}
		return out
	}
	product, measured := productAttrsRows(t, db), measures()
	if !strings.Contains(strings.Join(product, " "), "(other)") || !strings.Contains(strings.Join(measured, " "), "(other)") {
		t.Fatalf("the cap of 2 must fold before the migration: product %v, measures %v", product, measured)
	}

	if err := db.migrateThrough(ctx, 27); err != nil {
		t.Fatal(err)
	}
	if v, err := db.GetMeta(ctx, "attributes_top_n"); err != nil || v != "2" {
		t.Errorf("attributes_top_n = %q (%v), want the 2 set under the old name", v, err)
	}
	if v, err := db.GetMeta(ctx, "product_attributes_top_n"); err != nil || v != "" {
		t.Errorf("product_attributes_top_n = %q (%v), want it gone", v, err)
	}
	if after := productAttrsRows(t, db); !reflect.DeepEqual(product, after) {
		t.Errorf("v_product_attrs changed across 027:\nbefore %v\nafter  %v", product, after)
	}
	if after := measures(); !reflect.DeepEqual(measured, after) {
		t.Errorf("v_measures_attrs changed across 027:\nbefore %v\nafter  %v", measured, after)
	}
}
