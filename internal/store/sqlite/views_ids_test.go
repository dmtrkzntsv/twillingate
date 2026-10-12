package sqlite

import (
	"strings"
	"testing"
)

// TestViewsReadEventsDirectly pins D13: no v_* live half reads a raw_*
// view (they would decode every row before grouping).
func TestViewsReadEventsDirectly(t *testing.T) {
	db := newTestDB(t)
	rows, err := db.db.Query(`SELECT name, sql FROM sqlite_schema WHERE type = 'view' AND name LIKE 'v\_%' ESCAPE '\'
		AND name NOT IN ('v_events_flat', 'v_retention', 'v_identities')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatal(err)
		}
		n++
		if strings.Contains(def, "raw_views") || strings.Contains(def, "raw_product") || strings.Contains(def, "raw_measures") {
			t.Errorf("%s still reads a raw_* view", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 20 {
		t.Errorf("checked %d views, want the 20 live halves", n)
	}
}
