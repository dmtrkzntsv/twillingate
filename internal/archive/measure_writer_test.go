//go:build measure

// The writer's peak memory and time by width: go test -tags measure -run
// TestMeasureWriterPeak -v ./internal/archive with MEASURE_WRITER_ATTRS,
// the attribute columns past wideCols' 40 fixed ones (15000 for the
// widest case the bound was set by), and MEASURE_WRITER_ROWS (default
// 20,000), MEASURE_WRITER_EVERY the rows a group is cut at (default none);
// MEASURE_WRITER_BEFORE=1 writes every width by row, as before
// wide files were written by column. Run one width per process: the peak RSS is the process's.
// Not part of make check.
package archive

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMeasureWriterPeak(t *testing.T) {
	attrs, err := strconv.Atoi(os.Getenv("MEASURE_WRITER_ATTRS"))
	if err != nil {
		t.Skip("MEASURE_WRITER_ATTRS unset")
	}
	rows := 20000
	if n, err := strconv.Atoi(os.Getenv("MEASURE_WRITER_ROWS")); err == nil {
		rows = n
	}
	if os.Getenv("MEASURE_WRITER_BEFORE") == "1" {
		byColumnCols = 1 << 30
	}
	p := filepath.Join(t.TempDir(), "wide.parquet")
	before := vmHWM(t, "self")
	start := time.Now()
	every := 0
	if n, err := strconv.Atoi(os.Getenv("MEASURE_WRITER_EVERY")); err == nil {
		every = n
	}
	peak, _, _, _ := writeWide(t, p, attrs, rows, every)
	took := time.Since(start)
	rss := vmHWM(t, "self") // before the footer is read back below
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d columns, %d rows, %d groups: %s, peak heap %.0f MB, process peak RSS %.0f MB (%.0f MB before), file %.2f MB",
		40+attrs, rows, len(openMeta(t, p).Metadata().RowGroups), took.Round(time.Millisecond), float64(peak)/1e6, rss, before, float64(st.Size())/1e6)
}

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
