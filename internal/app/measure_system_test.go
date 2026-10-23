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
// with group ids and web vitals, then the daily pass). With MEASURE_FROM,
// another such directory, its month files are not generated but rebuilt
// through the daily pass in Go (step 4): each month written as one log a
// day, as ingest writes them, then dumped, merged and derived, a month at a
// time, so the files have the Go pass's row groups. Without MEASURE_CELL
// the run only generates. MEASURE_CELL=<engine>:<dir>:<days>
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
//
// The profile (step 4c): MEASURE_PROFILE=<prefix> writes a CPU profile of
// the warm runs to <prefix>-cpu.pb.gz, a block profile of them (every
// blocking event, so the time widgets wait on the engine's turn) to
// <prefix>-block.pb.gz, and a heap profile after them to
// <prefix>-heap.pb.gz. MEASURE_OPEN_LOG=<file> with MEASURE_TODAY=<day>
// adds an open log: the cell reads a copy of raw/ (links to its files)
// holding <file> as <day>.log, its range ends on that day, and the
// engine's clock says it is that day. MEASURE_MARK=1 stats a missing
// /measure-mark-<n> before each load, so a trace of the process (strace)
// can tell the loads apart; MEASURE_NO_POLL=1 does not poll a DuckDB
// cell's engine children either (a native cell never polls: the poll of
// /proc takes CPU from the cell's GOMAXPROCS; it looks once at the end);
// MEASURE_CHUNKS=<file> writes each Parquet
// file's column chunks (path, offset, row group, column) to it, to name
// the trace's reads.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

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
	WarmCPU   []float64      `json:"warm_cpu_ms"` // user + system CPU of this process over each warm run
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
// by pid, every 20 ms until stop is closed when poll, else only when the
// returned function is called: a child that exits between two looks
// keeps the last peak seen.
func childPeaks(stop <-chan struct{}, poll bool) func() map[int]int {
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
	if !poll {
		return func() map[int]int {
			look()
			return maps.Clone(peaks)
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

// rebuildThroughPass writes each month file of src as logs under raw/1
// and runs the daily pass in Go as of the third day of the next month, a
// month at a time: the dump, the merge, the sessions, and the identities
// and aliases over every month so far. Each month's pass is logged with
// its time and peak RSS (VmHWM, reset before it).
func rebuildThroughPass(t *testing.T, src, raw string) {
	months, err := filepath.Glob(filepath.Join(src, "????-??.parquet"))
	if err != nil || len(months) == 0 {
		t.Fatalf("no month files in %s: %v", src, err)
	}
	slices.Sort(months)
	pdir := filepath.Join(raw, "1")
	if err := os.MkdirAll(pdir, 0o750); err != nil {
		t.Fatal(err)
	}
	log, err := archive.OpenLog(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	a := archive.New(log, slog.Default())
	for _, m := range months {
		ym := strings.TrimSuffix(filepath.Base(m), ".parquet")
		first, err := civil.Parse(ym + "-01")
		if err != nil {
			t.Fatal(err)
		}
		began := time.Now()
		lines := archivetest.WriteLogs(t, m, pdir)
		wrote := time.Since(began)
		if err := os.WriteFile("/proc/self/clear_refs", []byte("5"), 0); err != nil {
			t.Fatal(err)
		}
		began = time.Now()
		today := civil.DateOf(first.Time().AddDate(0, 1, 2))
		if err := a.Maintain(context.Background(), today, 1, 3650); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(pdir, ym+".parquet")); err != nil {
			t.Fatalf("%s: not merged: %v", ym, err)
		}
		t.Logf("%s: %d lines written as logs in %v; the pass (dump, merge, derive) %v, peak RSS %d MB",
			ym, lines, wrote.Round(time.Millisecond), time.Since(began).Round(time.Millisecond), procStatusKB("self", "VmHWM")/1024)
	}
}

// withOpenLog is a copy of raw under work, its files linked, with log
// added to project 1 as day's open log.
func withOpenLog(t *testing.T, raw, work, log string, day civil.Date) string {
	out := filepath.Join(work, "raw")
	if err := os.MkdirAll(filepath.Join(out, "1"), 0o750); err != nil {
		t.Fatal(err)
	}
	link := func(dir, name string) {
		if err := os.Symlink(filepath.Join(raw, dir, name), filepath.Join(out, dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	top, err := os.ReadDir(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range top {
		if e.Name() != "1" {
			link("", e.Name())
		}
	}
	files, err := os.ReadDir(filepath.Join(raw, "1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range files {
		link("1", e.Name())
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "1", day.String()+".log"), b, 0o640); err != nil {
		t.Fatal(err)
	}
	return out
}

// writeChunks writes every Parquet file's column chunks under raw/1 to
// path, a line each: path, the chunk's first byte, row group, column.
func writeChunks(t *testing.T, raw, path string) {
	paths, err := filepath.Glob(filepath.Join(raw, "1", "*.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	more, _ := filepath.Glob(filepath.Join(raw, "1", "sessions", "*.parquet"))
	var sb strings.Builder
	for _, p := range append(paths, more...) {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		st, _ := f.Stat()
		pf, err := parquet.OpenFile(f, st.Size(), parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true))
		if err != nil {
			t.Fatal(err)
		}
		real, _ := filepath.EvalSymlinks(p)
		for i, rg := range pf.Metadata().RowGroups {
			for _, cc := range rg.Columns {
				off := cc.MetaData.DataPageOffset
				if d := cc.MetaData.DictionaryPageOffset; d > 0 && d < off {
					off = d
				}
				fmt.Fprintf(&sb, "%s\t%d\t%d\t%s\n", real, off, i, strings.Join(cc.MetaData.PathInSchema, "."))
			}
		}
		f.Close()
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// cpuMS is this process's user and system CPU so far.
func cpuMS() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return float64(ru.Utime.Nano()+ru.Stime.Nano()) / 1e6
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
	if _, err := os.Stat(filepath.Join(raw, "1", "identities.parquet")); err != nil && os.Getenv("MEASURE_FROM") != "" {
		rebuildThroughPass(t, filepath.Join(os.Getenv("MEASURE_FROM"), "raw", "1"), raw)
	} else if err != nil {
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
	now := time.Now
	if log := os.Getenv("MEASURE_OPEN_LOG"); log != "" {
		today, err := civil.Parse(os.Getenv("MEASURE_TODAY"))
		if err != nil {
			t.Fatalf("MEASURE_TODAY: %v", err)
		}
		raw, last = withOpenLog(t, raw, work, log, today), today
		now = func() time.Time { return today.Time().Add(12 * time.Hour) }
	}
	if path := os.Getenv("MEASURE_CHUNKS"); path != "" {
		writeChunks(t, raw, path)
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
	nat := native.New(native.Options{Dir: raw, Now: now})
	svc := reporting.New(st, files, reporting.Options{Native: nat})

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
		// MEASURE_MANY: the dashboard's query widgets as one
		// native.AnswerMany, straight to the engine.
		if os.Getenv("MEASURE_MANY") != "" {
			var nreqs []native.Request
			var names []string
			for _, w := range ws {
				if w.SourceType != "query" {
					continue
				}
				var c struct {
					Query native.Query `json:"query"`
				}
				if err := json.Unmarshal([]byte(w.Source), &c); err != nil {
					t.Fatalf("%s: %v", w.Name, err)
				}
				nreqs = append(nreqs, native.Request{Query: c.Query, Params: native.Params{Project: 1, From: from, To: last}})
				names = append(names, w.Name)
			}
			reqs = append(reqs, request{"many", func() (int, error) {
				n := 0
				for i, a := range nat.AnswerAll(ctx, nreqs) {
					if a.Err != nil {
						return 0, fmt.Errorf("%s: %w", names[i], a.Err)
					}
					n += len(a.Result.Rows)
				}
				return n, nil
			}})
			break
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
	// Only a DuckDB cell polls for its engine children: the poll reads /proc
	// every 20 ms on this process's own CPUs, which a native cell's queries
	// run on (a DuckDB cell's run in the children). A native cell looks
	// once, at the end, to check it spawned none.
	stop := make(chan struct{})
	peaks := childPeaks(stop, eng == "duckdb" && os.Getenv("MEASURE_NO_POLL") == "")
	cell := systemCell{Engine: eng, Dashboard: dash, Days: days, From: from.String(), To: last.String(), Widgets: len(reqs),
		IdleRSS: procStatusKB("self", "VmRSS"), Rows: map[string]int{}, Load: loadAvg()}
	mark := os.Getenv("MEASURE_MARK") != ""
	loads := 0
	load := func() (time.Duration, map[string]int) {
		if mark {
			os.Stat(fmt.Sprintf("/measure-mark-%d", loads)) //nolint:errcheck // the trace sees the call
		}
		loads++
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
	prof := os.Getenv("MEASURE_PROFILE")
	if prof != "" {
		f, err := os.Create(prof + "-cpu.pb.gz")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			t.Fatal(err)
		}
		runtime.SetBlockProfileRate(1)
	}
	for pass := 0; pass < passes; pass++ {
		cpu := cpuMS()
		wall, per := load()
		cell.WarmCPU = append(cell.WarmCPU, cpuMS()-cpu)
		ms := float64(wall.Microseconds()) / 1000
		cell.WarmMS = append(cell.WarmMS, ms)
		if cell.Best == nil || ms < cell.BestMS {
			cell.BestMS, cell.Best = ms, per
		}
	}
	if prof != "" {
		pprof.StopCPUProfile()
		runtime.SetBlockProfileRate(0)
		for _, name := range []string{"block", "heap"} {
			f, err := os.Create(prof + "-" + name + ".pb.gz")
			if err != nil {
				t.Fatal(err)
			}
			if err := pprof.Lookup(name).WriteTo(f, 0); err != nil {
				t.Fatal(err)
			}
			f.Close()
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
	t.Logf("%s: first %.0f ms, warm %v ms (CPU %v ms), self peak %d MB, children %d peak %d MB; slowest %v",
		cellSpec, cell.FirstMS, cell.WarmMS, cell.WarmCPU, cell.SelfPeak/1024, cell.Children, cell.ChildPeak/1024, names[:min(3, len(names))])
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
