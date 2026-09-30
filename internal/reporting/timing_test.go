package reporting

import (
	"context"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
)

// timingLimit is API_QUERY_TIMEOUT's default: a system widget slower
// than this cold would fail to load on an install left at the default.
const timingLimit = 10 * time.Second

// TestSystemDashboardTiming measures every system widget cold (no cache)
// for every preset against a real database, opt-in: it runs only when
// REPORTING_TIMING_DB names a database file, e.g.
//
//	REPORTING_TIMING_DB=$PWD/local/twillingate.db go test ./internal/reporting/ -run Timing -v
//
// It reads the embedded system definition directly (the database need
// not have been synced with this build), picks the project with the most
// recent data, logs "dashboard/widget preset duration rows" slowest
// first, and fails if any single load exceeds timingLimit.
func TestSystemDashboardTiming(t *testing.T) {
	path := os.Getenv("REPORTING_TIMING_DB")
	if path == "" {
		t.Skip("REPORTING_TIMING_DB is not set")
	}
	ctx := context.Background()
	// The read timeout sits well above timingLimit, so a slow widget is
	// measured and reported rather than cut off.
	db, err := readsql.Open(path, 10*timingLimit, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	system, err := fs.Sub(systemFS, "system")
	if err != nil {
		t.Fatal(err)
	}
	dashboards, err := LoadDashboards(system)
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := sampleProject(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if projectID == 0 {
		t.Fatal("no active project in the database")
	}
	t.Logf("project %d", projectID)

	src := newSources(db, false, time.Now)["sql"]
	today := civil.Today(time.Now().UTC())
	type timing struct {
		widget, preset string
		took           time.Duration
		rows           int
	}
	var timings []timing
	for _, d := range dashboards {
		for _, w := range d.Widgets {
			if w.SourceType != "sql" {
				continue
			}
			for _, preset := range systemPresets {
				from, to := presetDates(preset, today)
				start := time.Now()
				v, err := src.Load(ctx, w.Source, Params{ProjectID: projectID, From: from, To: to})
				took := time.Since(start)
				if err != nil {
					t.Fatalf("%s/%s %s: %v", d.Title, w.Name, preset, err)
				}
				timings = append(timings, timing{strings.ToLower(d.Title) + "/" + w.Name, preset, took, len(v.(readsql.Result).Rows)})
			}
		}
	}
	sort.Slice(timings, func(i, j int) bool { return timings[i].took > timings[j].took })
	for _, tm := range timings {
		t.Logf("%-40s %-9s %8.1fms %5d rows", tm.widget, tm.preset, float64(tm.took.Microseconds())/1000, tm.rows)
		if tm.took > timingLimit {
			t.Errorf("%s %s took %s, over %s", tm.widget, tm.preset, tm.took, timingLimit)
		}
	}
}
