//go:build measure

// The peak memory and the time of one month's merge, Go against DuckDB
// (mergeQuery in an engine child of 4 threads and 512 MB): go test -tags
// measure -run TestMeasureMergePeak -v ./internal/archive with
// MEASURE_MERGE_MONTH, a month file of about a million events (the native
// bench's), MEASURE_MERGE_DIR, a directory, and MEASURE_MERGE_SIDE, go or
// duckdb, and MEASURE_MERGE_DAYS, duck (day files DuckDB wrote, one row
// group a day) or go (written by the Go merge, a group per family a
// day). The first run cuts the month into its day files, with two late
// segments of a hundredth of a day each (ids repeated), and stops, so the
// next run's peak is the merge's alone. Not part of make check.
package archive

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

func TestMeasureMergePeak(t *testing.T) {
	month, dir := os.Getenv("MEASURE_MERGE_MONTH"), os.Getenv("MEASURE_MERGE_DIR")
	if month == "" || dir == "" {
		t.Skip("MEASURE_MERGE_MONTH or MEASURE_MERGE_DIR unset")
	}
	data := t.TempDir()
	self, _ := os.Executable()
	newEngine := func() *engine.Engine {
		eng, err := engine.New(engine.Options{Path: self, Env: []string{childEnv + "=1"},
			AllowedDirs: []string{filepath.Dir(month), dir}, SpillRoot: filepath.Join(data, "spill"), SlotDir: filepath.Join(data, "slots"),
			MaxChildren: 1, MemoryLimit: "512MB", Threads: 4})
		if err != nil {
			t.Fatal(err)
		}
		return eng
	}
	duckDir, goDir := filepath.Join(dir, "duck"), filepath.Join(dir, "go")
	if _, err := os.Stat(goDir); err != nil {
		eng := newEngine()
		defer eng.Close()
		ctx := context.Background()
		for _, d := range []string{duckDir, goDir} {
			if err := os.MkdirAll(d, 0o750); err != nil {
				t.Fatal(err)
			}
		}
		res, err := eng.Query(ctx, "SELECT DISTINCT strftime(day, '%Y-%m-%d') FROM read_parquet("+quote(month)+") ORDER BY 1", 0)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for i, r := range res.Rows {
			day := r[0].(string)
			copyTo := func(name, where string) {
				out := filepath.Join(duckDir, name)
				if err := eng.Exec(ctx, "COPY (SELECT * FROM read_parquet("+quote(month)+") WHERE day = DATE '"+day+"'"+where+
					" ORDER BY family, ts) TO "+quote(out)+" (FORMAT parquet, COMPRESSION zstd)"); err != nil {
					t.Fatal(err)
				}
				names = append(names, name)
			}
			copyTo(day+".parquet", "")
			if i == 3 || i == 17 {
				copyTo(day+".1.parquet", " AND hash(id) % 100 = 0")
			}
		}
		for _, n := range names {
			if err := mergeFiles(ctx, []string{filepath.Join(duckDir, n)}, filepath.Join(goDir, n)); err != nil {
				t.Fatal(err)
			}
		}
		t.Skipf("cut %d day files and segments into %s; run again", len(names), dir)
	}
	src := duckDir
	if os.Getenv("MEASURE_MERGE_DAYS") == "go" {
		src = goDir
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	var inputs []string
	for _, e := range entries {
		if dayFileName.MatchString(e.Name()) {
			inputs = append(inputs, filepath.Join(src, e.Name()))
		}
	}
	// The merge's order: by day, then segment.
	slices.SortFunc(inputs, func(x, y string) int {
		mx, my := dayFileName.FindStringSubmatch(filepath.Base(x)), dayFileName.FindStringSubmatch(filepath.Base(y))
		return cmp.Or(strings.Compare(mx[1], my[1]), segment(mx[2])-segment(my[2]))
	})
	out := filepath.Join(dir, "merged-"+cmp.Or(os.Getenv("MEASURE_MERGE_SIDE"), "none")+".parquet")
	defer os.Remove(out)
	switch os.Getenv("MEASURE_MERGE_SIDE") {
	case "go":
		before := vmHWM(t, "self")
		var peak atomic.Uint64 // the heap's live objects, sampled
		stop := make(chan struct{})
		sampled := make(chan struct{})
		go func() {
			defer close(sampled)
			s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
			for {
				metrics.Read(s)
				if v := s[0].Value.Uint64(); v > peak.Load() {
					peak.Store(v)
				}
				select {
				case <-stop:
					return
				case <-time.After(5 * time.Millisecond):
				}
			}
		}()
		start := time.Now()
		// mergeFiles, its passes timed apart.
		m, err := openMerge(inputs)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.markRepeats(); err != nil {
			t.Fatal(err)
		}
		runtime.GC() // as mergeFiles does
		pass1, pass1RSS := time.Since(start), vmHWM(t, "self")
		if err := m.write(context.Background(), out); err != nil {
			t.Fatal(err)
		}
		m.close()
		took := time.Since(start)
		t.Logf("pass 1: %s, peak RSS %.0f MB by its end", pass1.Round(time.Millisecond), pass1RSS)
		close(stop)
		<-sampled
		st, _ := os.Stat(out)
		t.Logf("go: %d inputs from %s, %s, process peak RSS %.0f MB (%.0f MB before the merge), heap objects peak %d MB, %d MB file", len(inputs), filepath.Base(src),
			took.Round(time.Millisecond), vmHWM(t, "self"), before, peak.Load()>>20, st.Size()>>20)
	case "duckdb":
		eng := newEngine()
		defer eng.Close()
		if err := eng.Exec(context.Background(), "SELECT 1"); err != nil { // spawn first: the time is the merge's
			t.Fatal(err)
		}
		start := time.Now()
		if err := eng.Exec(context.Background(), "COPY ("+mergeQuery(inputs)+") TO "+quote(out)+" (FORMAT parquet, COMPRESSION zstd)"); err != nil {
			t.Fatal(err)
		}
		took := time.Since(start)
		st, _ := os.Stat(out)
		for _, pid := range children(t) {
			t.Logf("duckdb: %d inputs from %s, %s, child %s peak RSS %.0f MB; this process %.0f MB, %d MB file", len(inputs), filepath.Base(src),
				took.Round(time.Millisecond), pid, vmHWM(t, pid), vmHWM(t, "self"), st.Size()>>20)
		}
	default:
		t.Skip("MEASURE_MERGE_SIDE is go or duckdb")
	}
}
