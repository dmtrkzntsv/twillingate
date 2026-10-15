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
// built the tables; MEASURE_THREADS (default 2) is the engine's. Every widget runs through reporting.Service.WidgetData
// with the cache off, over the last 365 days (Views) or 90 (Retention).
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
	raw := filepath.Join(dir, "raw")
	work := filepath.Join(dir, fmt.Sprintf("work-%d", os.Getpid()))
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(work) })
	ctx := context.Background()
	self, _ := os.Executable()

	// 1. The files: generated once, derived by the daily pass.
	genEng, err := engine.New(engine.Options{Path: self, Env: []string{appEngineChild + "=1"}, AllowedDirs: []string{raw},
		SpillRoot: filepath.Join(work, "gen-spill"), SlotDir: filepath.Join(work, "gen-slots"), MaxChildren: 1,
		MemoryLimit: "2GB", Threads: 3})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).AddDate(0, -(months - 1), 0)
	last, err := civil.Parse("2026-09-30")
	if err != nil {
		t.Fatal(err)
	}
	archivetest.Generated{Project: 1, Start: start, Months: months, PerMonth: perMonth, RareMonth: -1}.Generate(t, genEng, raw)
	log, err := archive.OpenLog(raw)
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if err := archive.New(log, genEng, slog.Default()).Maintain(ctx, last.AddDays(2), 1, 3650); err != nil {
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

	// 2. A store with the release's system dashboards, validated on the files.
	dbPath := filepath.Join(work, "twillingate.db")
	st := storetest.Open(t, dbPath)
	cfg := configtest.Load(t, map[string]string{"DATABASE_DSN": "sqlite://" + dbPath, "EVENTS_DIR": raw,
		"ENGINE_MEMORY_LIMIT": "512MB", "CONSOLE_QUERY_TIMEOUT": "10m", "CONSOLE_QUERY_ROWS_MAX": "1000"})
	reg := manage.New(st, slog.New(slog.DiscardHandler))
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if p, err := manage.NewOps(reg, st).CreateProject(ctx, "bench", manage.ProjectSpec{Name: "bench"}); err != nil || p.ID != 1 {
		t.Fatalf("project: %v, %v", p, err)
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
	for _, d := range []struct {
		id   int64
		name string
		days int
		span string
	}{
		{1, "Views", 365, "1 year"},
		{5, "Retention", 90, "90 days"},
	} {
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
		t.Logf("=== %s dashboard: %d sql widgets, %s to %s (%s), %d months x %d events, load average at start %s",
			d.name, len(sqlWidgets), from, to, d.span, months, perMonth, loadAvg())
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
						out, err := svc.WidgetData(ctx, reporting.DataRequest{WidgetID: w.ID, ProjectID: 1, From: from.String(), To: to.String()})
						took[i] = time.Since(began)
						if err != nil {
							t.Errorf("%s/%s: %v", d.name, w.Name, err)
							return
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
						t.Logf("  %-26s %8v", w.Name, took[i].Round(time.Millisecond))
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
