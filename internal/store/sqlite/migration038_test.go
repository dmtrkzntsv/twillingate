package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Migration 038 adds $country to the system keys of both attribute views:
// raw product and measure rows written before it break down by country
// after it, and every row the views held before is still there.
func TestMigration038BreaksDownCountry(t *testing.T) {
	db := newTestDBAt(t, 37)
	withWriteTables(t, db)
	ctx := context.Background()
	id := seedDeclaredProject(t, db, []string{"plan"})
	boot := 120.0
	var evs []store.Event
	for i, c := range []string{"DE", "DE", "US"} {
		evs = append(evs,
			store.Event{ID: fmt.Sprintf("p%d", i), ProjectID: id, Family: store.FamilyProduct, EventName: "signup",
				TS: ts("2026-08-02T10:00:00Z"), ActorID: "a", OS: "ios", Country: c,
				Attributes: map[string]string{"plan": "pro"}},
			store.Event{ID: fmt.Sprintf("m%d", i), ProjectID: id, Family: store.FamilyMeasures, EventName: "boot",
				TS: ts("2026-08-02T11:00:00Z"), ActorID: "c", Measure: store.MeasureTime, Value: &boot,
				OS: "ios", Country: c})
	}
	if err := db.writeTextEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	countries := func() (product, measures string) {
		t.Helper()
		read := func(q string) string {
			var s string
			if err := db.db.QueryRow(q, id).Scan(&s); err != nil {
				t.Fatal(err)
			}
			return s
		}
		return read(`SELECT COALESCE(group_concat(attr_value || '=' || count, ' '), '') FROM
				(SELECT attr_value, count FROM v_product_attrs
				 WHERE project_id = ? AND attr_key = '$country' ORDER BY attr_value)`),
			read(`SELECT COALESCE(group_concat(attr_value || '=' || n, ' '), '') FROM
				(SELECT attr_value, SUM(samples) AS n FROM v_measures_attrs
				 WHERE project_id = ? AND attr_key = '$country' GROUP BY attr_value ORDER BY attr_value)`)
	}
	if p, m := countries(); p != "" || m != "" {
		t.Fatalf("$country before 038: product %q, measures %q; want none", p, m)
	}
	before := productAttrsRows(t, db)

	if err := db.migrateThrough(ctx, 38); err != nil {
		t.Fatal(err)
	}
	if p, m := countries(); p != "DE=2 US=1" || m != "DE=2 US=1" {
		t.Errorf("$country after 038: product %q, measures %q; want DE=2 US=1 for both", p, m)
	}
	after := strings.Join(productAttrsRows(t, db), "\n")
	for _, r := range before {
		if !strings.Contains(after, r) {
			t.Errorf("v_product_attrs lost %q across 038", r)
		}
	}
}
