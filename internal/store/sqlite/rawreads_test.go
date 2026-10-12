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

// Nothing reads the raw events table without naming its family, so a
// product query can never count pageviews by forgetting a filter. Every
// view but v_events_flat (which holds every family on purpose) follows
// each read of events with its family codes before the next read: the
// raw_* views, and the v_* live halves, which group on the stored ids
// (events encoding spec D13; v_identity_daily groups views and product
// rows together, once for users and once for groups, and names both). Go reads it only in a statement
// that names its family (the rewrites and lookups on encoded ids, spec D9),
// or in a migration step, which runs against its own version's schema.
// Widget SQL reads the family views.
func TestRawTableIsReadOnlyThroughFamilyViews(t *testing.T) {
	db := newTestDB(t)
	var identity string
	if err := db.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name = 'v_identity_daily'`).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	if n := len(rawRead.FindAllString(identity, -1)); n != 2 || strings.Count(identity, "WHERE family IN (1, 2)") != 2 {
		t.Errorf("v_identity_daily reads events %d times; want twice (users, groups), each under WHERE family IN (1, 2)", n)
	}
	rows, err := db.db.Query(`SELECT name, sql FROM sqlite_schema WHERE type='view' AND name <> 'v_events_flat'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, sql string
		if err := rows.Scan(&name, &sql); err != nil {
			t.Fatal(err)
		}
		reads := rawRead.FindAllStringIndex(sql, -1)
		for i, r := range reads {
			end := len(sql)
			if i+1 < len(reads) {
				end = reads[i+1][0]
			}
			if !familyFilter.MatchString(sql[r[1]:end]) {
				t.Errorf("view %s reads the raw events table without a family filter before its next read", name)
			}
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
