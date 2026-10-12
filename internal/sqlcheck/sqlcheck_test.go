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
	"api/ops_limits.go:capUsage":                           "the capped view and its dimension column are filled in with Sprintf",
	"api/ops_measures.go:measures":                         "the source view, the attribute columns and the attribute filter are joined in only when an attribute key is asked for",
	"api/ops_product.go:productAttributes":                 "the event filter is appended only when an event is asked for",
	"api/ops_read.go:viewsBreakdown":                       "the select list and the view are joined from the requested dimension",
	"api/ops_read.go:viewsOverview":                        "the kind filter is appended only when a kind is asked for",
	"shared/readsql/page.go:QueryPage":                     "wraps the caller's query in the page's filters, sort and window",
	"shared/readsql/page.go:countWhenEmpty":                "wraps the caller's query to count its rows",
	"shared/readsql/readsql.go:QueryLimit":                 "wraps the caller's query to cap its rows",
	"store/sqlite/aggregate_measures.go:rollupMeasureAttr": "the attribute expression and its presence filter are passed in",
	"store/sqlite/aggregate_product.go:rollupAttrValue":    "the attribute expression and its presence filter are passed in",
	"store/sqlite/aggregate_views.go:AggregateViewDay":     "each dimension builds its own rollup statement",
	"store/sqlite/aggregate_views.go:daysBefore":           "the source table is filled in with Sprintf",
	"store/sqlite/flatview.go:RebuildFlatView":             "the view's columns are the attribute keys received",
	"store/sqlite/forms.go:FindSubmissions":                "the FROM and WHERE come from the search, and a cursor condition is appended",
	"store/sqlite/forms.go:SubmissionIDsMatching":          "the FROM and WHERE come from the search",
	"store/sqlite/identities.go:AggregateIdentityDay":      "the raw column (user_id or group_id) is filled in with Sprintf",
	"store/sqlite/migrate.go:migrateThrough":               "runs each embedded migration file as read; every test that migrates runs them",
	"store/sqlite/prune.go:PruneAggregates":                "the aggregate table is filled in with Sprintf",
	"store/sqlite/reporting.go:ListWidgets":                "the WHERE and ORDER BY are appended depending on whether a dashboard is given",
	"store/sqlite/reporting.go:UpdateWidget":               "the SET list holds only the columns being written",
	"store/sqlite/retention.go:UpsertActors":               "the source table is filled in with Sprintf",
	"store/sqlite/usage_attributes.go:countReceived":       "receivedSQL is assembled once at startup by a function",
	"store/sqlite/usage_history.go:countAttributes":        "foldedSQL is assembled once at startup by a function",
	"store/sqlite/usage_history.go:measureSizes":           "the table names come from the database",
}

// minSites guards the scan: finding far fewer SQL sites than the tree
// holds means the scan broke, not that the code lost its queries.
const minSites = 150

// migrationSteps lists the files, relative to internal/, that hold Go
// migration steps (dataSteps in store/sqlite/migrate.go). Their SQL runs
// inside its migration, against that version's schema rather than the
// current one (migration 015's step reads a table the same migration
// drops), so it is not checked here: every test that migrates runs it.
var migrationSteps = map[string]bool{
	"store/sqlite/migration015.go": true,
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
