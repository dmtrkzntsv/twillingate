//go:build measure

// The system dashboards on a year of event files at the cap (raw-first PR
// 5d's "done when": the Views dashboard under 2 s at 2 threads and 512 MB):
//
//	go test -tags measure -run TestMeasureDashboards -v -timeout 2h ./internal/app
//
// Not part of make check. One project, MEASURE_MONTHS months (default 12)
// of MEASURE_MONTH_EVENTS events (default 1000000) ending 2026-09-30, month
// files and their sessions, identities and aliases as the daily pass writes
// them. MEASURE_DIR keeps the generated files between runs (default
// ~/.cache/twbench-5d; never /tmp, whose quota fills). MEASURE_PASSES (default
// 3) is how many times each mode runs the dashboard after the first query
// built the tables; MEASURE_THREADS (default 2) is the engine's. Every
// widget runs through reporting.Service.WidgetData with the cache off, over
// the last 365 days (Views) or 90 (Retention) ending 2026-09-30;
// MEASURE_RUNS (below) picks other dashboards and ranges, MEASURE_RUNS=2:365
// the Product dashboard over the year.
//
// To measure an install's own files instead (nothing is generated, derived
// or created; the database is opened as it is and reporting.Migrate may
// sync its system dashboards, so work on a copy):
//
//	MEASURE_DB=/path/twillingate.db   the database (EVENTS_DIR is raw/ beside it
//	                                  unless MEASURE_EVENTS_DIR says otherwise)
//	MEASURE_PROJECTS=2,1              the projects to run the dashboards for
//	MEASURE_RUNS=1:7,1:30,5:30        dashboard id:days, the range ending MEASURE_TO
//	MEASURE_TO=2026-10-11             the last day of the range
package app

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

func measureEnv(name string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return def
}

// childPeakKB is the high-water mark of resident memory (VmHWM, kB) of
// this process's child that serves the engine, 0 when there is none.
func childPeakKB(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	peak := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		s := string(stat)
		rest := strings.Fields(s[strings.LastIndex(s, ")")+1:]) // state, ppid, ...
		if len(rest) < 2 || rest[1] != strconv.Itoa(os.Getpid()) {
			continue
		}
		f, err := os.Open(filepath.Join("/proc", strconv.Itoa(pid), "status"))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "VmHWM:"); ok {
				kb, _ := strconv.Atoi(strings.Fields(v)[0])
				peak = max(peak, kb)
			}
		}
		f.Close()
	}
	return peak
}

func loadAvg() string {
	b, _ := os.ReadFile("/proc/loadavg")
	return strings.Join(strings.Fields(string(b))[:3], " ")
}

func TestMeasureDashboards(t *testing.T) {
	months, perMonth, passes := measureEnv("MEASURE_MONTHS", 12), measureEnv("MEASURE_MONTH_EVENTS", 1_000_000), measureEnv("MEASURE_PASSES", 3)
	threads := measureEnv("MEASURE_THREADS", 2)
	dir := os.Getenv("MEASURE_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		dir = filepath.Join(home, ".cache", "twbench-5d")
	}
	existing := os.Getenv("MEASURE_DB")
	raw := filepath.Join(dir, "raw")
	if existing != "" {
		raw = os.Getenv("MEASURE_EVENTS_DIR")
		if raw == "" {
			raw = filepath.Join(filepath.Dir(existing), "raw")
		}
	}
	work := filepath.Join(dir, fmt.Sprintf("work-%d", os.Getpid()))
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(work) })
	ctx := context.Background()
	self, _ := os.Executable()

	// 1. The files: generated once, derived by the daily pass.
	last, err := civil.Parse("2026-09-30")
	if err != nil {
		t.Fatal(err)
	}
	var began time.Time
	if existing != "" {
		if last, err = civil.Parse(envOr("MEASURE_TO", "2026-10-11")); err != nil {
			t.Fatal(err)
		}
	} else {
		genEng, err := engine.New(engine.Options{Path: self, Env: []string{appEngineChild + "=1"}, AllowedDirs: []string{raw},
			SpillRoot: filepath.Join(work, "gen-spill"), SlotDir: filepath.Join(work, "gen-slots"), MaxChildren: 1,
			MemoryLimit: "2GB", Threads: 3})
		if err != nil {
			t.Fatal(err)
		}
		start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).AddDate(0, -(months - 1), 0)
		archivetest.Generated{Project: 1, Start: start, Months: months, PerMonth: perMonth, RareMonth: -1}.Generate(t, genEng, raw)
		log, err := archive.OpenLog(raw)
		if err != nil {
			t.Fatal(err)
		}
		began = time.Now()
		if err := archive.New(log, slog.Default()).Maintain(ctx, last.AddDays(2), 1, 3650); err != nil {
			t.Fatal(err)
		}
		t.Logf("daily pass (derive sessions, identities, aliases): %v", time.Since(began).Round(time.Millisecond))
		log.Close()
		genEng.Close()
		sessions, _ := filepath.Glob(filepath.Join(raw, "1", "sessions", "*.parquet"))
		if len(sessions) != months {
			t.Fatalf("%d session files, want %d", len(sessions), months)
		}
		for _, f := range []string{"identities.parquet", "aliases.parquet"} {
			if _, err := os.Stat(filepath.Join(raw, "1", f)); err != nil {
				t.Fatal(err)
			}
		}
	}

	// 2. A store with the release's system dashboards, validated on the files.
	dbPath := filepath.Join(work, "twillingate.db")
	var st store.Store
	if existing != "" {
		dbPath = existing
		var err error
		if st, err = store.Open("sqlite://" + dbPath); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
	} else {
		st = storetest.Open(t, dbPath)
	}
	cfg := configtest.Load(t, map[string]string{"DATABASE_DSN": "sqlite://" + dbPath, "EVENTS_DIR": raw,
		"ENGINE_MEMORY_LIMIT": "512MB", "CONSOLE_QUERY_TIMEOUT": "10m", "CONSOLE_QUERY_ROWS_MAX": "1000"})
	reg := manage.New(st, slog.New(slog.DiscardHandler))
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if existing == "" {
		if p, err := manage.NewOps(reg, st).CreateProject(ctx, "bench", manage.ProjectSpec{Name: "bench"}); err != nil || p.ID != 1 {
			t.Fatalf("project: %v, %v", p, err)
		}
	}
	newEngine := func(parallel int) (*engine.Engine, *archive.Reader) {
		t.Helper()
		opts, err := EngineOptions(cfg)
		if err != nil {
			t.Fatal(err)
		}
		opts.Threads, opts.Parallel = threads, parallel
		opts.SpillRoot, opts.SlotDir = filepath.Join(work, "spill"), filepath.Join(work, "slots")
		eng, err := engine.New(opts)
		if err != nil {
			t.Fatal(err)
		}
		reader, err := archive.NewReader(raw, eng)
		if err != nil {
			t.Fatal(err)
		}
		return eng, reader
	}
	eng, reader := newEngine(1)
	began = time.Now()
	if err := reporting.Migrate(ctx, st, newWidgetFiles(cfg, reader, reg)); err != nil {
		t.Fatal(err)
	}
	t.Logf("reporting.Migrate (first query builds the tables, 59 widgets checked at LIMIT 0): %v", time.Since(began).Round(time.Millisecond))
	eng.Close()

	// 3. The dashboards.
	to := last
	runs := []measureRun{{1, 365}, {5, 90}}
	if existing != "" {
		runs = parseRuns(t, envOr("MEASURE_RUNS", "1:7,1:30,5:30"))
	} else if spec := os.Getenv("MEASURE_RUNS"); spec != "" {
		runs = parseRuns(t, spec)
	}
	projects := []int64{1}
	if existing != "" {
		projects = nil
		for _, f := range strings.Split(envOr("MEASURE_PROJECTS", "1"), ",") {
			n, err := strconv.ParseInt(strings.TrimSpace(f), 10, 64)
			if err != nil {
				t.Fatalf("MEASURE_PROJECTS: %v", err)
			}
			projects = append(projects, n)
		}
	}
	for _, project := range projects {
		for _, r := range runs {
			d := struct {
				id   int64
				name string
				days int
				span string
			}{r.dashboard, map[int64]string{1: "Views", 2: "Product", 5: "Retention"}[r.dashboard], r.days, fmt.Sprintf("%d days", r.days)}
			if d.name == "" {
				d.name = fmt.Sprintf("dashboard %d", d.id)
			}
			from := to.AddDays(1 - d.days)
			ws, err := st.ListWidgets(ctx, d.id)
			if err != nil {
				t.Fatal(err)
			}
			var sqlWidgets []store.Widget
			for _, w := range ws {
				if w.SourceType == "sql" {
					sqlWidgets = append(sqlWidgets, w)
				}
			}
			t.Logf("=== project %d, %s dashboard: %d sql widgets, %s to %s (%s), load average at start %s",
				project, d.name, len(sqlWidgets), from, to, d.span, loadAvg())
			for _, mode := range []struct {
				name         string
				parallel, at int // engine slots, widgets in flight
			}{
				{"one at a time (engine parallel 1)", 1, 1},
				{"two at a time (engine parallel 2)", 2, 2},
				{"all at once, as a browser sends them (engine parallel 1; the engine's default at 2 threads)", 1, len(sqlWidgets)},
			} {
				eng, reader := newEngine(mode.parallel)
				svc := reporting.New(st, newWidgetFiles(cfg, reader, reg), reporting.Options{})
				load := loadAvg()
				began := time.Now()
				if _, err := reader.Query(ctx, "SELECT count(*) FROM events", 0, archive.Rules{}); err != nil {
					t.Fatal(err)
				}
				build := time.Since(began)
				t.Logf("--- %s, %s: first query (build) %v, load average %s", d.name, mode.name, build.Round(time.Millisecond), load)

				var best []time.Duration
				var bestTotal time.Duration
				for pass := 1; pass <= passes; pass++ {
					took := make([]time.Duration, len(sqlWidgets))
					rows := make([]int, len(sqlWidgets))
					sem := make(chan struct{}, mode.at)
					var wg sync.WaitGroup
					began := time.Now()
					for i, w := range sqlWidgets {
						wg.Add(1)
						sem <- struct{}{}
						go func() {
							defer wg.Done()
							defer func() { <-sem }()
							began := time.Now()
							out, err := svc.WidgetData(ctx, reporting.DataRequest{WidgetID: w.ID, ProjectID: project, From: from.String(), To: to.String()})
							took[i] = time.Since(began)
							if err != nil {
								t.Errorf("%s/%s: %v", d.name, w.Name, err)
								return
							}
							if res, ok := out.Data.(readsql.Result); ok {
								rows[i] = len(res.Rows)
							}
							// A look at what the stat widgets say, to see the data is a plausible year.
							if pass == 1 && mode.at == 1 && w.Component == "stat" {
								if res, ok := out.Data.(readsql.Result); ok && len(res.Rows) > 0 {
									t.Logf("  (%s = %v)", w.Name, res.Rows[0])
								}
							}
						}()
					}
					wg.Wait()
					wall := time.Since(began)
					var sum time.Duration
					for _, x := range took {
						sum += x
					}
					t.Logf("pass %d: wall %v (sum of widgets %v)", pass, wall.Round(time.Millisecond), sum.Round(time.Millisecond))
					if pass == 1 {
						for i, w := range sqlWidgets {
							t.Logf("  %-26s %8v  %d rows", w.Name, took[i].Round(time.Millisecond), rows[i])
						}
					}
					if best == nil || wall < bestTotal {
						best, bestTotal = took, wall
					}
				}
				type named struct {
					name string
					took time.Duration
				}
				var slow []named
				for i, w := range sqlWidgets {
					slow = append(slow, named{w.Name, best[i]})
				}
				slices.SortFunc(slow, func(a, b named) int { return int(b.took - a.took) })
				var top []string
				for _, s := range slow[:min(5, len(slow))] {
					top = append(top, fmt.Sprintf("%s %v", s.name, s.took.Round(time.Millisecond)))
				}
				t.Logf("best pass wall %v; slowest: %s; engine child peak RSS %d MB; load average after %s",
					bestTotal.Round(time.Millisecond), strings.Join(top, ", "), childPeakKB(t)/1024, loadAvg())
				eng.Close()
			}
		}
	}
}

// measureRun is a dashboard and how many days its range covers.
type measureRun struct {
	dashboard int64
	days      int
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// parseRuns reads "1:7,1:30,5:30" (dashboard id:days).
func parseRuns(t *testing.T, spec string) []measureRun {
	t.Helper()
	var runs []measureRun
	for _, f := range strings.Split(spec, ",") {
		id, days, ok := strings.Cut(strings.TrimSpace(f), ":")
		a, errA := strconv.ParseInt(id, 10, 64)
		b, errB := strconv.Atoi(days)
		if !ok || errA != nil || errB != nil || b < 1 {
			t.Fatalf("MEASURE_RUNS: %q is not dashboard:days", f)
		}
		runs = append(runs, measureRun{a, b})
	}
	return runs
}
