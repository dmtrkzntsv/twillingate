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

// Nothing reads the raw events table except raw_views, raw_product and
// v_events_flat (which holds both families on purpose). Everything else
// reads through the two family views, so a product query can never count
// pageviews by forgetting a filter.
func TestRawTableIsReadOnlyThroughFamilyViews(t *testing.T) {
	db := newTestDB(t)
	rows, err := db.db.Query(`SELECT name, sql FROM sqlite_schema
		WHERE type='view' AND name NOT IN ('raw_views', 'raw_product', 'v_events_flat')`)
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
			t.Errorf("%s:%d reads the raw events table directly; use raw_views or raw_product", f, line)
		}
	}
}
