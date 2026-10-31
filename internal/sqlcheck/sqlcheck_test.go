// Package sqlcheck holds only tests. They find every SQL string the
// binary runs, read from the source under internal/, and check it against
// a database migrated to the current schema: each statement prepares,
// binds as many parameters as its call passes, and returns as many
// columns as its Scan reads. SQL built at run time cannot be checked, so
// each function that builds some is listed in dynamic, with the reason.
package sqlcheck

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	_ "github.com/dmtrkzntsv/twillingate/internal/shared/readsql" // registers tw_cell and tw_num, which fixed SQL may call
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

// dynamic lists the functions whose SQL is built at run time, keyed
// file:Func with the file relative to internal/, and why it cannot be a
// constant. TestDynamicSitesAreListed checks it both ways: a function
// that builds SQL needs an entry, and an entry needs such a function.
var dynamic = map[string]string{
	"shared/readsql/page.go:QueryPage":       "wraps the caller's query in the page's filters, sort and window",
	"shared/readsql/page.go:countWhenEmpty":  "wraps the caller's query to count its rows",
	"shared/readsql/readsql.go:QueryLimit":   "wraps the caller's query to cap its rows",
	"store/sqlite/migrate.go:migrateThrough": "runs each embedded migration file as read; every test that migrates runs them",
	"store/sqlite/reporting.go:UpdateWidget": "the SET list holds only the columns being written",
}

// minSites guards the scan: finding far fewer SQL sites than the tree
// holds means the scan broke, not that the code lost its queries.
const minSites = 150

// migrationSteps lists the files, relative to internal/, whose SQL runs
// against an older schema than the current one, so it is not checked here.
// A Go migration step (dataSteps in store/sqlite/migrate.go) runs inside
// its migration, against that version's schema (migration 015's step reads
// a table the same migration drops): every test that migrates runs it.
// Migration 042's pre-check (preChecks) reads the events table that
// migration drops, on the schema 041 left: store/sqlite's tests run it.
var migrationSteps = map[string]bool{
	"store/sqlite/migration015.go": true,
	"store/sqlite/migration042.go": true,
}

// migrationFile matches a Go migration step's file name.
var migrationFile = regexp.MustCompile(`^store/sqlite/migration\d+\.go$`)

// siteFile is the file part of a site's key.
func siteFile(s site) string { return s.key[:strings.LastIndexByte(s.key, ':')] }

// scanInternal returns the SQL sites under internal/.
func scanInternal(t *testing.T) []site {
	t.Helper()
	sites, err := scanSource("..")
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) < minSites {
		t.Fatalf("found only %d SQL sites under internal/, want at least %d: the scan is broken, not the code", len(sites), minSites)
	}
	checked := sites[:0]
	for _, s := range sites {
		if !migrationSteps[siteFile(s)] {
			checked = append(checked, s)
		}
	}
	return checked
}

// openMigrated opens a copy of the database storetest migrates once per
// test binary.
func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "check.db")
	storetest.Copy(t, path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestEverySQLStringPrepares(t *testing.T) {
	sites := scanInternal(t)
	db := openMigrated(t)
	for _, s := range sites {
		for _, p := range problems(context.Background(), db, s) {
			t.Errorf("%s: %s", s.pos, p)
		}
	}
}

// unlisted compares the dynamic sites with the listed functions: missing
// holds the sites whose function has no entry, stale the entries (sorted)
// whose function builds no SQL at run time.
func unlisted(sites []site, listed map[string]string) (missing []site, stale []string) {
	found := map[string]bool{}
	for _, s := range sites {
		if s.sqls != nil {
			continue
		}
		found[s.key] = true
		if _, ok := listed[s.key]; !ok {
			missing = append(missing, s)
		}
	}
	for key := range listed {
		if !found[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	return missing, stale
}

func TestDynamicSitesAreListed(t *testing.T) {
	missing, stale := unlisted(scanInternal(t), dynamic)
	for _, s := range missing {
		t.Errorf("%s: SQL built at run time (%s). Make it resolve (a constant, a local, placeholders), add %q to dynamic with the reason, or, if the call is not SQL at all, rename the function so it does not share a SQL wrapper's name", s.pos, s.why, s.key)
	}
	for _, key := range stale {
		t.Errorf("dynamic lists %s, which builds no SQL at run time any more: remove the entry", key)
	}
}

func TestMigrationStepsAreListed(t *testing.T) {
	sites, err := scanSource("..")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range sites {
		f := siteFile(s)
		seen[f] = true
		if migrationFile.MatchString(f) && !migrationSteps[f] {
			t.Errorf("%s: a Go migration step runs SQL against its own version's schema; add %q to migrationSteps", s.pos, f)
		}
	}
	for f := range migrationSteps {
		if !seen[f] {
			t.Errorf("migrationSteps lists %s, which runs no SQL any more: remove the entry", f)
		}
	}
}
