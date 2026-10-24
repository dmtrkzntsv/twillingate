//go:build measure

// The log cache's memory and times on a bench log (native engine step
// 4d): what one open log's entry takes, counted (logEntry.measure) and on
// the heap, decoded cold, grown by a tenth, and read warm. Not part of
// make check.
//
//	MEASURE_LOG=~/.cache/twbench-native/profiles/open-2026-10-01.log TMPDIR=~/.cache/gotmp \
//	  go test -tags measure -run TestMeasureLogCache -v ./internal/native
//
// MEASURE_LOG_COLS (comma-separated, default every column) are the
// columns read; MEASURE_LOG_DAY the log's day (default its file name's).
package native

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func heapNow() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func TestMeasureLogCache(t *testing.T) {
	src := os.Getenv("MEASURE_LOG")
	if src == "" {
		t.Skip("MEASURE_LOG is not set")
	}
	cols := []string{"*"}
	if s := os.Getenv("MEASURE_LOG_COLS"); s != "" {
		cols = strings.Split(s, ",")
	}
	day := os.Getenv("MEASURE_LOG_DAY")
	if day == "" {
		day = strings.TrimSuffix(filepath.Base(src), ".log")
		day = day[len(day)-10:]
	}
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(b), "\n")
	cut := len(lines) * 9 / 10
	path := filepath.Join(t.TempDir(), day+".log")
	if err := os.WriteFile(path, []byte(strings.Join(lines[:cut], "")), 0o644); err != nil {
		t.Fatal(err)
	}
	rest := []byte(strings.Join(lines[cut:], ""))
	nLines, mb, grownBy := len(lines)-1, len(b)>>20, len(lines)-1-cut
	b, lines = nil, nil
	r := rng(t, day, day)

	began := time.Now()
	if _, err := readLog(path, cols, "", r); err != nil {
		t.Fatal(err)
	}
	uncached := time.Since(began)

	before := heapNow()
	c := newLogCache(1 << 40)
	began = time.Now()
	if _, err := c.read(context.Background(), path, cols, "", r); err != nil {
		t.Fatal(err)
	}
	cold := time.Since(began)
	appendFile(t, path, string(rest))
	rest = nil
	began = time.Now()
	if _, err := c.read(context.Background(), path, cols, "", r); err != nil {
		t.Fatal(err)
	}
	grown := time.Since(began)
	began = time.Now()
	if _, err := c.read(context.Background(), path, cols, "views", r); err != nil {
		t.Fatal(err)
	}
	warm := time.Since(began)
	heap := int64(heapNow()) - int64(before)
	x := c.entries[path]
	rows, built := 0, 0
	for _, p := range x.parts {
		rows += p.n
		built += len(p.builders)
	}
	t.Logf("%d lines (%d MB), %d rows kept in %d families, %d family columns (cols %v): counted %.1f MB, heap %.1f MB; "+
		"readLog %v, cold %v, grown by %d lines %v, warm (views) %v",
		nLines, mb, rows, len(x.parts), built, cols, float64(x.bytes)/(1<<20), float64(heap)/(1<<20),
		uncached.Round(time.Millisecond), cold.Round(time.Millisecond), grownBy, grown.Round(time.Millisecond),
		warm.Round(time.Microsecond))
	runtime.KeepAlive(c)
}
