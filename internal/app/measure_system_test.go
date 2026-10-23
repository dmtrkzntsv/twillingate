//go:build measure

// One cell of the built-in dashboards' measurement (native engine step 3f):
// one dashboard over one range, every widget requested at once as the
// console does, on the native engine or on its old DuckDB SQL. Each cell is
// a fresh process so its peak RSS is its own; scripts/native-bench's
// dashboards subcommand runs the matrix and writes the tables. Not part of
// make check.
//
//	MEASURE_DIR=~/.cache/twbench-native/3f/1m MEASURE_MONTH_EVENTS=1000000 \
//	  go test -tags measure -run TestMeasureSystemDashboard -v ./internal/app
//
// MEASURE_DIR holds raw/ (generated on the first run: MEASURE_MONTHS
// months, default 12, of MEASURE_MONTH_EVENTS events ending 2026-09-30,
// with group ids and web vitals, then the daily pass). Without
// MEASURE_CELL the run only generates. MEASURE_CELL=<engine>:<dir>:<days>
// (engine native or duckdb, dir a system dashboard's directory such as
// views) runs the dashboard once (the first run) and MEASURE_PASSES times
// more (default 3), and writes the cell as JSON to MEASURE_OUT.
//
// Native runs the dashboard's query widgets through reporting.Service's
// WidgetData, with native.New's defaults (MaxScans 2, 2 workers). DuckDB
// runs the old SQL kept for parity (internal/reporting/testdata/system-sql,
// and vitals-by-device-browser.sql for the two vitals widgets split from
// it) through Service.Preview, the console editor's uncached run, so
// through archive.Reader and the engine child at EngineOptions' defaults
// (threads = GOMAXPROCS, one query at a time at 2 threads, 512 MB).
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
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
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

// systemCell is one cell's result, as the bench reads it.
type systemCell struct {
	Engine    string         `json:"engine"`
	Dashboard string         `json:"dashboard"`
	Days      int            `json:"days"`
	From      string         `json:"from"`
	To        string         `json:"to"`
	Widgets   int            `json:"widgets"`
	FirstMS   float64        `json:"first_ms"`
	WarmMS    []float64      `json:"warm_ms"`
	BestMS    float64        `json:"best_ms"`
	SelfPeak  int            `json:"self_peak_kb"`  // VmHWM of this process
	ChildPeak int            `json:"child_peak_kb"` // the sum over the engine children of each one's VmHWM
	Children  int            `json:"children"`
	IdleRSS   int            `json:"idle_rss_kb"` // VmRSS before the first request
	First     map[string]int `json:"first_widget_ms"`
	Best      map[string]int `json:"best_widget_ms"`
	Rows      map[string]int `json:"rows"`
	Load      string         `json:"load_at_start"`
}

func procStatusKB(pid, field string) int {
	b, err := os.ReadFile(filepath.Join("/proc", pid, "status"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, field+":"); ok {
			f := strings.Fields(v)
			if len(f) > 0 {
				kb, _ := strconv.Atoi(f[0])
				return kb
			}
		}
	}
	return 0
}

// childPeaks records the VmHWM of every child of this process it sees,
// by pid, until stop is closed: a child that exits between two looks
// keeps the last peak seen.
func childPeaks(stop <-chan struct{}) func() map[int]int {
	var mu sync.Mutex
	peaks := map[int]int{}
	look := func() {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return
		}
		me := strconv.Itoa(os.Getpid())
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
			rest := strings.Fields(s[strings.LastIndex(s, ")")+1:])
			if len(rest) < 2 || rest[1] != me {
				continue
			}
			if kb := procStatusKB(e.Name(), "VmHWM"); kb > 0 {
				mu.Lock()
				peaks[pid] = max(peaks[pid], kb)
				mu.Unlock()
			}
		}
	}
	go func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				look()
			}
		}
	}()
	return func() map[int]int {
		look()
		mu.Lock()
		defer mu.Unlock()
		return maps.Clone(peaks)
	}
}

func TestMeasureSystemDashboard(t *testing.T) {
	months, perMonth, passes := measureEnv("MEASURE_MONTHS", 12), measureEnv("MEASURE_MONTH_EVENTS", 1_000_000), measureEnv("MEASURE_PASSES", 3)
	dir := os.Getenv("MEASURE_DIR")
	if dir == "" {
		t.Fatal("MEASURE_DIR is required")
	}
	raw := filepath.Join(dir, "raw")
	work := filepath.Join(dir, fmt.Sprintf("work-%d", os.Getpid()))
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(work) })
	ctx := context.Background()
	last, err := civil.Parse("2026-09-30")
	if err != nil {
		t.Fatal(err)
	}

	// 1. The files, generated once and derived by the daily pass.
	if _, err := os.Stat(filepath.Join(raw, "1", "identities.parquet")); err != nil {
		self, _ := os.Executable()
		genEng, err := engine.New(engine.Options{Path: self, Env: []string{appEngineChild + "=1"}, AllowedDirs: []string{raw},
			SpillRoot: filepath.Join(work, "gen-spill"), SlotDir: filepath.Join(work, "gen-slots"), MaxChildren: 1,
			MemoryLimit: "2GB", Threads: 3})
		if err != nil {
			t.Fatal(err)
		}
		start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).AddDate(0, -(months - 1), 0)
		began := time.Now()
		archivetest.Generated{Project: 1, Start: start, Months: months, PerMonth: perMonth, RareMonth: -1,
			Groups: 2000, Vitals: true}.Generate(t, genEng, raw)
		t.Logf("generated %d months in %v", months, time.Since(began).Round(time.Millisecond))
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
	}
	cellSpec := os.Getenv("MEASURE_CELL")
	if cellSpec == "" {
		return
	}
	parts := strings.Split(cellSpec, ":")
	if len(parts) != 3 {
		t.Fatalf("MEASURE_CELL %q is not engine:dir:days", cellSpec)
	}
	eng, dash := parts[0], parts[1]
	days, err := strconv.Atoi(parts[2])
	if err != nil || days < 1 {
		t.Fatalf("MEASURE_CELL %q: days", cellSpec)
	}
	from := last.AddDays(1 - days)

	// 2. A store with the system dashboards and the project.
	dbPath := filepath.Join(work, "twillingate.db")
	st := storetest.Open(t, dbPath)
	cfg := configtest.Load(t, map[string]string{"DATABASE_DSN": "sqlite://" + dbPath, "EVENTS_DIR": raw,
		"CONSOLE_QUERY_TIMEOUT": "10m"})
	reg := manage.New(st, slog.New(slog.DiscardHandler))
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if p, err := manage.NewOps(reg, st).CreateProject(ctx, "bench", manage.ProjectSpec{Name: "bench"}); err != nil || p.ID != 1 {
		t.Fatalf("project: %v, %v", p, err)
	}
	opts, err := EngineOptions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	duck, err := engine.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer duck.Close()
	reader, err := archive.NewReader(raw, duck)
	if err != nil {
		t.Fatal(err)
	}
	files := newWidgetFiles(cfg, reader, reg)
	if err := reporting.Migrate(ctx, st, files); err != nil {
		t.Fatal(err)
	}
	svc := reporting.New(st, files, reporting.Options{Native: native.New(native.Options{Dir: raw})})

	// 3. The requests: one function per widget.
	type request struct {
		name string
		run  func() (int, error)
	}
	var reqs []request
	switch eng {
	case "native":
		var meta struct {
			ID int64 `json:"id"`
		}
		b, err := os.ReadFile(filepath.Join("..", "reporting", "system", dash, "dashboard.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &meta); err != nil {
			t.Fatal(err)
		}
		ws, err := st.ListWidgets(ctx, meta.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range ws {
			if w.SourceType != "query" {
				continue
			}
			reqs = append(reqs, request{w.Name, func() (int, error) {
				out, err := svc.WidgetData(ctx, reporting.DataRequest{WidgetID: w.ID, ProjectID: 1, From: from.String(), To: last.String()})
				if err != nil {
					return 0, err
				}
				if res, ok := out.Data.(readsql.Result); ok {
					return len(res.Rows), nil
				}
				return 0, fmt.Errorf("data is %T", out.Data)
			}})
		}
	case "duckdb":
		sqlDir := filepath.Join("..", "reporting", "testdata", "system-sql", dash)
		paths, err := filepath.Glob(filepath.Join(sqlDir, "*.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if dash == "vitals" {
			paths = append(paths, filepath.Join("..", "reporting", "testdata", "vitals-by-device-browser.sql"))
		}
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			sql := string(b)
			reqs = append(reqs, request{strings.TrimSuffix(filepath.Base(p), ".sql"), func() (int, error) {
				out, err := svc.Preview(ctx, reporting.PreviewIn{Component: "table", Source: reporting.Source{Type: "sql", Content: sql},
					ProjectID: 1, From: from.String(), To: last.String()})
				if err != nil {
					return 0, err
				}
				return len(out.Rows), nil
			}})
		}
	default:
		t.Fatalf("engine %q is not native or duckdb", eng)
	}
	if len(reqs) == 0 {
		t.Fatalf("%s: no widgets", cellSpec)
	}

	// 4. The loads: all at once, as the console sends them.
	stop := make(chan struct{})
	peaks := childPeaks(stop)
	cell := systemCell{Engine: eng, Dashboard: dash, Days: days, From: from.String(), To: last.String(), Widgets: len(reqs),
		IdleRSS: procStatusKB("self", "VmRSS"), Rows: map[string]int{}, Load: loadAvg()}
	load := func() (time.Duration, map[string]int) {
		took := make([]time.Duration, len(reqs))
		rows := make([]int, len(reqs))
		var wg sync.WaitGroup
		began := time.Now()
		for i, r := range reqs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				b := time.Now()
				n, err := r.run()
				took[i] = time.Since(b)
				if err != nil {
					t.Errorf("%s %s: %v", cellSpec, r.name, err)
				}
				rows[i] = n
			}()
		}
		wg.Wait()
		wall := time.Since(began)
		per := map[string]int{}
		for i, r := range reqs {
			per[r.name] = int(took[i].Milliseconds())
			cell.Rows[r.name] = rows[i]
		}
		return wall, per
	}
	wall, per := load()
	cell.FirstMS, cell.First = float64(wall.Microseconds())/1000, per
	for pass := 0; pass < passes; pass++ {
		wall, per := load()
		ms := float64(wall.Microseconds()) / 1000
		cell.WarmMS = append(cell.WarmMS, ms)
		if cell.Best == nil || ms < cell.BestMS {
			cell.BestMS, cell.Best = ms, per
		}
	}
	got := peaks()
	close(stop)
	cell.SelfPeak = procStatusKB("self", "VmHWM")
	for _, kb := range got {
		cell.ChildPeak += kb
	}
	cell.Children = len(got)
	if eng == "native" && cell.Children > 0 {
		t.Errorf("%s: the native cell spawned %d engine children", cellSpec, cell.Children)
	}
	names := make([]string, 0, len(cell.First))
	for n := range cell.First {
		names = append(names, n)
	}
	slices.SortFunc(names, func(a, b string) int { return cell.Best[b] - cell.Best[a] })
	t.Logf("%s: first %.0f ms, warm %v ms, self peak %d MB, children %d peak %d MB; slowest %v",
		cellSpec, cell.FirstMS, cell.WarmMS, cell.SelfPeak/1024, cell.Children, cell.ChildPeak/1024, names[:min(3, len(names))])
	if out := os.Getenv("MEASURE_OUT"); out != "" {
		b, err := json.Marshal(cell)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
