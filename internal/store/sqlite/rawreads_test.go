package sqlite

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// rawRead matches a read of the raw table: FROM or JOIN events. A delete
// ("DELETE FROM events") is a write and is excluded by the caller.
var rawRead = regexp.MustCompile(`\b(FROM|JOIN)\s+events\b`)

// Nothing reads the raw events table except raw_views, raw_product,
// raw_measures, v_events_flat (which holds every family on purpose) and
// v_identity_daily, whose live half groups views and product rows in one
// aggregate over the table (025_live_halves.sql) and names both families.
// Everything else reads through the family views, so a product query can
// never count pageviews by forgetting a filter.
func TestRawTableIsReadOnlyThroughFamilyViews(t *testing.T) {
	db := newTestDB(t)
	var identity string
	if err := db.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name = 'v_identity_daily'`).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	if n := len(rawRead.FindAllString(identity, -1)); n != 1 || !strings.Contains(identity, "WHERE family IN ('views', 'product')") {
		t.Errorf("v_identity_daily reads events %d times; want once, under WHERE family IN ('views', 'product')", n)
	}
	rows, err := db.db.Query(`SELECT name, sql FROM sqlite_schema
		WHERE type='view' AND name NOT IN ('raw_views', 'raw_product', 'raw_measures', 'v_events_flat', 'v_identity_daily')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, sql string
		if err := rows.Scan(&name, &sql); err != nil {
			t.Fatal(err)
		}
		if rawRead.MatchString(sql) {
			t.Errorf("view %s reads the raw events table directly", name)
		}
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "flatview.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, loc := range rawRead.FindAllIndex(src, -1) {
			before := strings.ToUpper(string(src[max(0, loc[0]-8):loc[0]]))
			if strings.Contains(before, "DELETE") {
				continue
			}
			line := 1 + strings.Count(string(src[:loc[0]]), "\n")
			t.Errorf("%s:%d reads the raw events table directly; use raw_views, raw_product or raw_measures", f, line)
		}
	}
}
