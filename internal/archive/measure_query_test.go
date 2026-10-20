//go:build measure

// Questions over 3 years of files through the Reader (raw-first PR 4's
// "done when"; D10's targets: a 3-year question under 30 s, a timeline
// under 1 s, at 2 threads and 512 MB): go test -tags measure -run
// TestMeasureQueries -v -timeout 2h ./internal/archive. Not part of make
// check. QUERY_MONTHS (default 36), QUERY_MONTH_EVENTS (default 1000000)
// and QUERY_THREADS (default 2) override the defaults; QUERY_DIR keeps the
// generated files between runs (they are generated only when missing).
package archive_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

func queryEnv(name string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return def
}

func TestMeasureQueries(t *testing.T) {
	months, perMonth, threads := queryEnv("QUERY_MONTHS", 36), queryEnv("QUERY_MONTH_EVENTS", 1_000_000), queryEnv("QUERY_THREADS", 2)
	data := os.Getenv("QUERY_DIR")
	if data == "" {
		data = t.TempDir()
	}
	raw := filepath.Join(data, "raw")
	self, _ := os.Executable()
	// The test binary's TestMain (maintain_test.go) serves this variable.
	newEngine := func(memory string, threads int) *engine.Engine {
		eng, err := engine.New(engine.Options{Path: self, Env: []string{"ARCHIVE_TEST_ENGINE_CHILD=1"}, AllowedDirs: []string{raw},
			SpillRoot: filepath.Join(data, "spill"), SlotDir: filepath.Join(data, "slots"), MaxChildren: 1,
			MemoryLimit: memory, Threads: threads})
		if err != nil {
			t.Fatal(err)
		}
		return eng
	}
	ctx := context.Background()
	gen := newEngine("2GB", 3)
	archivetest.Generated{Project: 1, Start: time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC), Months: months, PerMonth: perMonth,
		RareMonth: 17}.Generate(t, gen, raw)
	gen.Close()
	eng := newEngine("512MB", threads)
	defer eng.Close()
	r, err := archive.NewReader(raw, eng)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, sql string
		max       time.Duration
	}{
		{"build (first query)", "SELECT count(*) FROM events", 30 * time.Second},
		{"views and visitors per day, 3 years", "SELECT day, count(*), count(DISTINCT actor_id) FROM views GROUP BY day", 30 * time.Second},
		{"distinct users, 3 years", "SELECT count(DISTINCT user_id) FROM product", 30 * time.Second},
		{"plan x variant", "SELECT attr_plan, attr_variant, count(*) FROM product GROUP BY ALL", 30 * time.Second},
		{"plan x country", "SELECT attr_plan, country, count(*) FROM product GROUP BY ALL", 30 * time.Second},
		{"events per name per month", "SELECT date_trunc('month', ts), event_name, count(*) FROM product GROUP BY ALL", 30 * time.Second},
		{"first rare event", "SELECT min(ts) FROM product WHERE event_name = 'rare'", 30 * time.Second},
		{"one user's timeline, 3 years", "SELECT ts, event_name FROM events WHERE user_id = 'u-123' ORDER BY ts", time.Second},
	} {
		began := time.Now()
		res, err := r.Query(ctx, c.sql, 0, archive.Rules{})
		took := time.Since(began)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		t.Logf("%-40s %8v  %d rows", c.name, took.Round(time.Millisecond), len(res.Rows))
		if took > c.max {
			t.Errorf("%s took %v, over the %v target", c.name, took, c.max)
		}
	}
}
