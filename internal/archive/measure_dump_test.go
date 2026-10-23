//go:build measure

// The peak memory and the time of one dump of TestDumpKeepsAnIDsFirstLine's
// 45 MB log, the Go dump against DuckDB's (dumpQuery in an engine child of
// 4 threads and 512 MB, as that test ran it): go test -tags measure -run
// TestMeasureDumpPeak -v ./internal/archive with MEASURE_DUMP_LOG, a path,
// and MEASURE_DUMP_SIDE, go or duckdb. The first run writes the log and
// stops, so the next run's peak is the dump's alone. Not part of make check.
package archive

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

// vmHWM is the peak resident set of process pid ("self" for this one), in MB.
func vmHWM(t *testing.T, pid string) float64 {
	t.Helper()
	b, err := os.ReadFile("/proc/" + pid + "/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "VmHWM:" {
			kb, _ := strconv.ParseFloat(f[1], 64)
			return kb / 1024
		}
	}
	t.Fatal("no VmHWM")
	return 0
}

// children lists the pids whose parent is this process.
func children(t *testing.T) []string {
	t.Helper()
	self := strconv.Itoa(os.Getpid())
	entries, _ := os.ReadDir("/proc")
	var out []string
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			if f := strings.Fields(sc.Text()); len(f) == 2 && f[0] == "PPid:" && f[1] == self {
				out = append(out, e.Name())
			}
		}
	}
	return out
}

func TestMeasureDumpPeak(t *testing.T) {
	src := os.Getenv("MEASURE_DUMP_LOG")
	if src == "" {
		t.Skip("MEASURE_DUMP_LOG unset")
	}
	if _, err := os.Stat(src); err != nil {
		if err := os.WriteFile(src, []byte(bigDumpLog(t)), 0o640); err != nil {
			t.Fatal(err)
		}
		t.Skip("wrote the log; run again")
	}
	out := filepath.Join(t.TempDir(), "out.parquet")
	switch os.Getenv("MEASURE_DUMP_SIDE") {
	case "go":
		before := vmHWM(t, "self")
		start := time.Now()
		if err := dumpLog(src, out); err != nil {
			t.Fatal(err)
		}
		t.Logf("go: %s, process peak RSS %.0f MB (%.0f MB before the dump)", time.Since(start).Round(time.Millisecond), vmHWM(t, "self"), before)
	case "duckdb":
		data := t.TempDir()
		self, _ := os.Executable()
		eng, err := engine.New(engine.Options{Path: self, Env: []string{childEnv + "=1"},
			AllowedDirs: []string{filepath.Dir(src), filepath.Dir(out)}, SpillRoot: filepath.Join(data, "spill"), SlotDir: filepath.Join(data, "slots"),
			MaxChildren: 1, MemoryLimit: "512MB", Threads: 4})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()
		if err := eng.Exec(context.Background(), "SELECT 1"); err != nil { // spawn first: the time is the dump's
			t.Fatal(err)
		}
		q, err := dumpQuery(src)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err := eng.Exec(context.Background(), "COPY ("+q+") TO "+quote(out)+" (FORMAT parquet, COMPRESSION zstd)"); err != nil {
			t.Fatal(err)
		}
		took := time.Since(start)
		for _, pid := range children(t) {
			t.Logf("duckdb: %s, child %s peak RSS %.0f MB; this process %.0f MB", took.Round(time.Millisecond), pid, vmHWM(t, pid), vmHWM(t, "self"))
		}
	default:
		t.Skip("MEASURE_DUMP_SIDE is go or duckdb")
	}
}
