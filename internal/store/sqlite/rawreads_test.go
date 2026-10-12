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

// familyFilter matches a statement's filter on one family code, or a
// list of them.
var familyFilter = regexp.MustCompile(`\bfamily\s*(=\s*(\d|\?)|IN\s*\()`)

// migrationStep matches a Go migration step's file.
var migrationStep = regexp.MustCompile(`^migration\d+\.go$`)

// Nothing reads the raw events table except raw_views, raw_product,
// raw_measures, v_events_flat (which holds every family on purpose) and
// v_identity_daily, whose live half groups views and product rows in one
// aggregate over the table (025_live_halves.sql) and names both families.
// Go reads it only in a statement that names its family (the rewrites and
// lookups on encoded ids, events encoding spec D9), or in a migration step,
// which runs against its own version's schema. Everything else reads
// through the family views, so a product query can never count pageviews
// by forgetting a filter.
func TestRawTableIsReadOnlyThroughFamilyViews(t *testing.T) {
	db := newTestDB(t)
	var identity string
	if err := db.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name = 'v_identity_daily'`).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	if n := len(rawRead.FindAllString(identity, -1)); n != 1 || !strings.Contains(identity, "WHERE family IN (1, 2)") {
		t.Errorf("v_identity_daily reads events %d times; want once, under WHERE family IN (1, 2)", n)
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
		if strings.HasSuffix(f, "_test.go") || f == "flatview.go" || migrationStep.MatchString(f) {
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
			// The rest of the statement, up to its closing backquote.
			rest := string(src[loc[1]:])
			if end := strings.IndexByte(rest, '`'); end >= 0 && familyFilter.MatchString(rest[:end]) {
				continue
			}
			line := 1 + strings.Count(string(src[:loc[0]]), "\n")
			t.Errorf("%s:%d reads the raw events table directly; use raw_views, raw_product or raw_measures", f, line)
		}
	}
}
