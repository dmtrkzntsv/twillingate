//go:build measure

// The daily pass of one month, Go against DuckDB (native engine step 4):
// the dump of every day's log, the month merge, then the derived files
// (the month's sessions, identities, aliases), each phase timed and its
// peak RSS taken apart (VmHWM, reset between phases through clear_refs).
// scripts/native-bench's pass subcommand runs the matrix. Not part of make
// check.
//
//	MEASURE_PASS_MONTH=~/.cache/twbench-native/3f/1m/raw/1/2026-09.parquet \
//	MEASURE_PASS_DIR=~/.cache/twbench-native/4/pass/1m \
//	MEASURE_PASS_SIDE=go MEASURE_PASS_CASE=pass MEASURE_PASS_LOGS=plain \
//	  go test -tags measure -run TestMeasurePass -v ./internal/archive
//
// The first run (MEASURE_PASS_SIDE unset) writes the month's events as one
// NDJSON log a day, as ingest writes them (logs/plain), the same with
// every product attribute key spread over 250 names, 2,000 keys in all
// (logs/wide), and the Go dump of each (days-plain, days-wide), and stops.
//
// MEASURE_PASS_CASE is dump (the month's 30 logs, one after another),
// pass (the merge day's pass: the last day's log dumped, the month merged
// from it and the other days' files, dumped earlier, then derived) or
// leftover (a crashed merge's month file, the DuckDB-written source, next
// to the Go day files: merge, derive); merge (the Go day files: merge,
// derive) and derive (the month a go pass kept) take the pass apart. DuckDB runs the old statements
// (dumpQuery, mergeQuery, sessionsSelect, identitiesSelect,
// aliasesSelect over eventsOf) in an engine child of 2 threads and 512 MB,
// the server's defaults at GOMAXPROCS=2, spawned before the clock starts.
// The cell is written as JSON to MEASURE_PASS_OUT. MEASURE_PASS_HEAPPROF,
// a path, gets a heap profile (after a collection) each time the live heap
// grows by a fifth: the last one is near the peak.
package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"runtime/pprof"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

// passPhase is one phase of a cell.
type passPhase struct {
	Name      string  `json:"name"`
	WallMS    float64 `json:"wall_ms"`
	SelfPeak  int     `json:"self_peak_kb"`  // VmHWM of this process over the phase
	ChildPeak int     `json:"child_peak_kb"` // VmHWM of the engine child over the phase
	GCs       uint64  `json:"gcs"`
	GCCPUMS   float64 `json:"gc_cpu_ms"` // the GC's CPU time (runtime/metrics estimate)
	CPUMS     float64 `json:"cpu_ms"`    // all of the Go process's CPU time
}

type passCell struct {
	Side        string      `json:"side"`
	Case        string      `json:"case"`
	Logs        string      `json:"logs"`
	GoMemLimit  string      `json:"gomemlimit"`
	Inputs      int         `json:"inputs"`
	Rows        int64       `json:"rows"`
	Columns     int         `json:"columns"`
	RowGroups   int         `json:"row_groups"`
	BaselineKB  int         `json:"baseline_kb"` // VmRSS before the first phase
	Phases      []passPhase `json:"phases"`
	MonthBytes  int64       `json:"month_bytes"`
	Load        string      `json:"load_at_start"`
	HeapPeakMiB uint64      `json:"heap_peak_mib"` // the heap's live objects, sampled every 5 ms
}

func procKB(t *testing.T, pid, field string) int {
	t.Helper()
	b, err := os.ReadFile("/proc/" + pid + "/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == field+":" {
			var kb int
			fmt.Sscan(f[1], &kb)
			return kb
		}
	}
	t.Fatalf("no %s", field)
	return 0
}

// resetPeak sets pid's VmHWM back to its current RSS (Linux clear_refs 5).
func resetPeak(t *testing.T, pid string) {
	t.Helper()
	if err := os.WriteFile("/proc/"+pid+"/clear_refs", []byte("5"), 0); err != nil {
		t.Fatalf("reset the peak of %s: %v", pid, err)
	}
}

func TestMeasurePass(t *testing.T) {
	month, dir := os.Getenv("MEASURE_PASS_MONTH"), os.Getenv("MEASURE_PASS_DIR")
	if month == "" || dir == "" {
		t.Skip("MEASURE_PASS_MONTH or MEASURE_PASS_DIR unset")
	}
	side := os.Getenv("MEASURE_PASS_SIDE")
	if _, err := os.Stat(filepath.Join(dir, "days-wide")); err != nil || side == "" {
		preparePass(t, month, dir)
		t.Skip("wrote the logs and the day files; run again")
	}
	cs, logs := os.Getenv("MEASURE_PASS_CASE"), os.Getenv("MEASURE_PASS_LOGS")
	if logs == "" {
		logs = "plain"
	}
	ctx := context.Background()
	work := filepath.Join(dir, fmt.Sprintf("work-%d", os.Getpid()))
	pdir := filepath.Join(work, "raw", "1")
	if err := os.MkdirAll(pdir, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(work) })
	ym := strings.TrimSuffix(filepath.Base(month), ".parquet")
	monthOut := filepath.Join(pdir, ym+".parquet")

	// The inputs, copied in (not timed).
	var logPaths, days []string
	switch cs {
	case "dump":
		logPaths = copyAll(t, filepath.Join(dir, "logs", logs), pdir)
	case "pass":
		// The month's merge day: the other days dumped on earlier passes,
		// the last day's log dumped by this one.
		days = copyAll(t, filepath.Join(dir, "days-"+logs), pdir)
		last := days[len(days)-1]
		days = days[:len(days)-1]
		if err := os.Remove(last); err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(dir, "logs", logs, filepath.Base(last)+".dumping")
		logPaths = []string{last + ".dumping"}
		copyFile(t, src, logPaths[0])
	case "leftover":
		copyFile(t, month, monthOut)
		days = copyAll(t, filepath.Join(dir, "days-plain"), pdir)
	case "merge":
		days = copyAll(t, filepath.Join(dir, "days-"+logs), pdir)
	case "derive":
		copyFile(t, filepath.Join(dir, "months", logs+".parquet"), monthOut)
	default:
		t.Fatalf("MEASURE_PASS_CASE %q is dump, pass or leftover", cs)
	}
	cell := passCell{Side: side, Case: cs, Logs: logs, GoMemLimit: os.Getenv("GOMEMLIMIT"), Load: loadAvg1()}

	var eng *engine.Engine
	child := ""
	if side == "duckdb" {
		self, _ := os.Executable()
		var err error
		eng, err = engine.New(engine.Options{Path: self, Env: []string{childEnv + "=1"}, AllowedDirs: []string{work},
			SpillRoot: filepath.Join(work, "spill"), SlotDir: filepath.Join(work, "slots"), MaxChildren: 1,
			MemoryLimit: "512MB", Threads: 2})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()
		if err := eng.Exec(ctx, "SELECT 1"); err != nil { // spawn first: the time is the pass's
			t.Fatal(err)
		}
		kids := children(t)
		if len(kids) != 1 {
			t.Fatalf("%d engine children", len(kids))
		}
		child = kids[0]
	} else if side != "go" {
		t.Fatalf("MEASURE_PASS_SIDE %q is go or duckdb", side)
	}
	copyTo := func(sel, out string) {
		tmp := out + ".tmp"
		if err := eng.Exec(ctx, "COPY ("+sel+") TO "+quote(tmp)+" (FORMAT parquet, COMPRESSION zstd)"); err != nil {
			t.Fatal(err)
		}
		if err := syncFile(tmp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, out); err != nil {
			t.Fatal(err)
		}
	}

	// The heap's live objects, sampled.
	var heapPeak, lastProf uint64
	stop, sampled := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(sampled)
		s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
		for {
			metrics.Read(s)
			if v := s[0].Value.Uint64(); v > heapPeak {
				if prof := os.Getenv("MEASURE_PASS_HEAPPROF"); prof != "" && v > lastProf+lastProf/5 {
					lastProf = v
					runtime.GC()
					if f, err := os.Create(prof); err == nil {
						pprof.Lookup("heap").WriteTo(f, 0)
						f.Close()
					}
				}
				heapPeak = v
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	gcSamples := []metrics.Sample{{Name: "/gc/cycles/total:gc-cycles"}, {Name: "/cpu/classes/gc/total:cpu-seconds"},
		{Name: "/cpu/classes/total:cpu-seconds"}}
	readGC := func() (uint64, float64, float64) {
		metrics.Read(gcSamples)
		return gcSamples[0].Value.Uint64(), gcSamples[1].Value.Float64(), gcSamples[2].Value.Float64()
	}
	runtime.GC()
	cell.BaselineKB = procKB(t, "self", "VmRSS")
	phase := func(name string, run func()) {
		resetPeak(t, "self")
		if child != "" {
			resetPeak(t, child)
		}
		g0, gc0, cpu0 := readGC()
		start := time.Now()
		run()
		p := passPhase{Name: name, WallMS: float64(time.Since(start).Microseconds()) / 1000, SelfPeak: procKB(t, "self", "VmHWM")}
		g1, gc1, cpu1 := readGC()
		p.GCs, p.GCCPUMS, p.CPUMS = g1-g0, (gc1-gc0)*1000, (cpu1-cpu0)*1000
		if child != "" {
			p.ChildPeak = procKB(t, child, "VmHWM")
		}
		cell.Phases = append(cell.Phases, p)
		t.Logf("%s: %.0f ms, peak %d MB (child %d MB), %d GCs, GC CPU %.0f ms of %.0f ms", name, p.WallMS,
			p.SelfPeak/1024, p.ChildPeak/1024, p.GCs, p.GCCPUMS, p.CPUMS)
	}

	a := New(nil, slog.New(slog.DiscardHandler))
	// 1. The dump, a day at a time.
	if cs == "dump" || cs == "pass" {
		phase("dump", func() {
			for _, src := range logPaths {
				out := strings.TrimSuffix(src, ".dumping")
				if side == "go" {
					if err := dumpLog(src, out); err != nil {
						t.Fatal(err)
					}
				} else {
					q, err := dumpQuery(src)
					if err != nil {
						t.Fatal(err)
					}
					copyTo(q, out)
				}
				if err := os.Remove(src); err != nil {
					t.Fatal(err)
				}
				days = append(days, out)
			}
		})
	}
	if cs == "dump" {
		cell.Inputs = len(logPaths)
		writeCell(t, cell)
		return
	}
	if cs != "derive" {
		// 2. The merge: the month file first when there is one, then the days.
		inputs := slices.Clone(days)
		if cs == "leftover" {
			inputs = append([]string{monthOut}, days...)
		}
		cell.Inputs = len(inputs)
		phase("merge", func() {
			if side == "go" {
				if err := mergeFiles(ctx, inputs, monthOut); err != nil {
					t.Fatal(err)
				}
			} else {
				copyTo(mergeQuery(inputs), monthOut)
			}
			for _, d := range days {
				if err := os.Remove(d); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	// 3. The derived files of the month.
	phase("derive", func() {
		sdir := filepath.Join(pdir, sessionsDir)
		if err := os.MkdirAll(sdir, 0o750); err != nil {
			t.Fatal(err)
		}
		if side == "go" {
			var st passStats
			if err := a.derive(ctx, pdir, true, &st); err != nil {
				t.Fatal(err)
			}
			if st.derived != 3 {
				t.Fatalf("derived %d files, want 3", st.derived)
			}
			return
		}
		files := []string{monthOut}
		copyTo(sessionsSelect(eventsOf(files)), filepath.Join(sdir, ym+".parquet"))
		copyTo(identitiesSelect(eventsOf(files)), filepath.Join(pdir, identitiesFile))
		copyTo(aliasesSelect(eventsOf(files)), filepath.Join(pdir, aliasesFile))
	})
	close(stop)
	<-sampled
	cell.HeapPeakMiB = heapPeak >> 20
	if keep := filepath.Join(dir, "months", logs+".parquet"); cs == "pass" && side == "go" && !exists(keep) {
		os.MkdirAll(filepath.Dir(keep), 0o750)
		copyFile(t, monthOut, keep)
	}
	st, err := os.Stat(monthOut)
	if err != nil {
		t.Fatal(err)
	}
	cell.MonthBytes = st.Size()
	f, err := os.Open(monthOut)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pf, err := parquet.OpenFile(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	cell.Rows, cell.Columns, cell.RowGroups = pf.NumRows(), len(pf.Schema().Columns()), len(pf.RowGroups())
	t.Logf("%s %s %s: %d inputs, %d rows, %d columns, %d row groups, %d MB month, baseline %d MB, heap peak %d MiB",
		side, cs, logs, cell.Inputs, cell.Rows, cell.Columns, cell.RowGroups, cell.MonthBytes>>20, cell.BaselineKB/1024, cell.HeapPeakMiB)
	writeCell(t, cell)
}

func writeCell(t *testing.T, cell passCell) {
	t.Helper()
	if out := os.Getenv("MEASURE_PASS_OUT"); out != "" {
		b, err := json.Marshal(cell)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func loadAvg1() string {
	b, _ := os.ReadFile("/proc/loadavg")
	return strings.Join(strings.Fields(string(b))[:3], " ")
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// copyAll copies every file of src into dst and returns the copies in
// name order.
func copyAll(t *testing.T, src, dst string) []string {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		p := filepath.Join(dst, e.Name())
		copyFile(t, filepath.Join(src, e.Name()), p)
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// preparePass writes dir/logs/plain and dir/logs/wide (one log a day of
// month's events, the lines shaped as Log's line writes them: empty
// values left out, ts and received_at in tsFormat, v 1) and dir/days (the
// Go dump of each plain log).
func preparePass(t *testing.T, month, dir string) {
	t.Helper()
	plain, wide := filepath.Join(dir, "logs", "plain"), filepath.Join(dir, "logs", "wide")
	for _, d := range []string{plain, wide, filepath.Join(dir, "days"), filepath.Join(dir, "days-plain"), filepath.Join(dir, "days-wide")} {
		if err := os.RemoveAll(d); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(month)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	pf, err := parquet.OpenFile(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	type leaf struct {
		name string
		typ  colType
	}
	var leaves []leaf
	for _, path := range pf.Schema().Columns() {
		col, _ := pf.Schema().Lookup(path...)
		typ, _, ok := leafType(col.Node.Type())
		if !ok {
			t.Fatalf("column %v: %v", path, col.Node.Type())
		}
		leaves = append(leaves, leaf{path[0], typ})
	}
	type out struct{ plain, wide *os.File }
	files := map[string]out{}
	defer func() {
		for _, o := range files {
			o.plain.Close()
			o.wide.Close()
		}
	}()
	epoch := time.Unix(0, 0).UTC()
	n := 0
	for _, rg := range pf.RowGroups() {
		rows := rg.Rows()
		buf := make([]parquet.Row, 1024)
		for {
			k, err := rows.ReadRows(buf)
			for _, r := range buf[:k] {
				m := map[string]any{"v": lineVersion}
				var id, day string
				for _, v := range r {
					if v.IsNull() {
						continue
					}
					l := leaves[v.Column()]
					switch l.typ {
					case colVarchar:
						s := v.String()
						if s == "" {
							continue
						}
						m[l.name] = s
					case colBigint:
						m[l.name] = v.Int64()
					case colDouble:
						m[l.name] = v.Double()
					case colDate:
						m[l.name] = epoch.AddDate(0, 0, int(v.Int32())).Format("2006-01-02")
					case colTimestamp:
						m[l.name] = time.UnixMicro(v.Int64()).UTC().Format(tsFormat)
					}
				}
				id, _ = m["id"].(string)
				day, _ = m["day"].(string)
				if day == "" {
					t.Fatalf("a row without a day: %v", m)
				}
				o, ok := files[day]
				if !ok {
					p, err := os.Create(filepath.Join(plain, day+".parquet.dumping"))
					if err != nil {
						t.Fatal(err)
					}
					w, err := os.Create(filepath.Join(wide, day+".parquet.dumping"))
					if err != nil {
						t.Fatal(err)
					}
					o = out{p, w}
					files[day] = o
				}
				b, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := o.plain.Write(append(b, '\n')); err != nil {
					t.Fatal(err)
				}
				// Wide: each attribute key spread over 250 names by the id.
				h := fnv.New32a()
				h.Write([]byte(id))
				spread := h.Sum32() % 250
				var attrs []string
				for k := range m {
					if strings.HasPrefix(k, "attr_") {
						attrs = append(attrs, k)
					}
				}
				for _, k := range attrs {
					m[fmt.Sprintf("%s_%03d", k, spread)] = m[k]
					delete(m, k)
				}
				if b, err = json.Marshal(m); err != nil {
					t.Fatal(err)
				}
				if _, err := o.wide.Write(append(b, '\n')); err != nil {
					t.Fatal(err)
				}
				n++
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		rows.Close()
	}
	for _, o := range files {
		if err := o.plain.Close(); err != nil {
			t.Fatal(err)
		}
		if err := o.wide.Close(); err != nil {
			t.Fatal(err)
		}
	}
	files = nil
	os.RemoveAll(filepath.Join(dir, "days"))
	var entries []os.DirEntry
	for _, v := range []string{"plain", "wide"} {
		var err error
		if entries, err = os.ReadDir(filepath.Join(dir, "logs", v)); err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if err := dumpLog(filepath.Join(dir, "logs", v, e.Name()),
				filepath.Join(dir, "days-"+v, strings.TrimSuffix(e.Name(), ".dumping"))); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Logf("wrote %d lines in %d days (plain, wide) and their Go day files", n, len(entries))
}
