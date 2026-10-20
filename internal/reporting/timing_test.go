package reporting

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
)

// timingLimit is CONSOLE_QUERY_TIMEOUT's default: a system widget slower
// than this cold would fail to load on an install left at the default.
const timingLimit = 10 * time.Second

// TestSystemDashboardTiming measures every system widget cold (no cache)
// for every preset against real event files, opt-in: it runs only when
// REPORTING_TIMING_EVENTS_DIR names a raw/ directory, e.g.
//
//	REPORTING_TIMING_EVENTS_DIR=$PWD/local/raw go test ./internal/reporting/ -run Timing -v
//
// It reads the embedded system definition directly, picks the project
// with the newest events, logs "dashboard/widget preset duration rows"
// slowest first, and fails if any single load exceeds timingLimit. The
// engine has DuckDB's default memory limit and four threads.
func TestSystemDashboardTiming(t *testing.T) {
	dir := os.Getenv("REPORTING_TIMING_EVENTS_DIR")
	if dir == "" {
		t.Skip("REPORTING_TIMING_EVENTS_DIR is not set")
	}
	ctx := context.Background()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	eng, err := engine.New(engine.Options{Path: self, Env: []string{archivetest.ChildEnv + "=1"},
		AllowedDirs: []string{dir}, SpillRoot: filepath.Join(tmp, "spill"), SlotDir: filepath.Join(tmp, "slots"),
		MaxChildren: 1, Threads: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	reader, err := archive.NewReader(dir, eng)
	if err != nil {
		t.Fatal(err)
	}
	// The read timeout sits well above timingLimit, so a slow widget is
	// measured and reported rather than cut off.
	files := &testFiles{Files: &archivetest.Files{Dir: dir, Eng: eng, Reader: reader}, timeout: 10 * timingLimit, maxRows: 1000}

	system, err := fs.Sub(systemFS, "system")
	if err != nil {
		t.Fatal(err)
	}
	dashboards, err := LoadDashboards(system)
	if err != nil {
		t.Fatal(err)
	}
	days, err := archive.LatestDays(dir)
	if err != nil {
		t.Fatal(err)
	}
	var projectID int64
	for id, day := range days {
		if projectID == 0 || day > days[projectID] || day == days[projectID] && id < projectID {
			projectID = id
		}
	}
	if projectID == 0 {
		t.Fatal("no project with events in the directory")
	}
	t.Logf("project %d", projectID)

	src := newSources(files, false, time.Now)["sql"]
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
