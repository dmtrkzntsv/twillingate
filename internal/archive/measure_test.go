//go:build measure

// The memory of the dump and the merge, old (the first release's queries)
// against new: go test -tags measure -run 'TestMeasure(Dump|Merge)' -v
// -timeout 2h ./internal/archive. Not part of make check. Comma-separated
// MEASURE_THREADS, MEASURE_LIMITS (MB), MEASURE_SIZES (dump log MB),
// MEASURE_VARIANTS and MEASURE_MONTH_EVENTS override the defaults. Each
// run is a fresh engine child, spill directory set as in production.
package archive

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// measureEvent is a web product event with 12 attributes on day (1-based)
// of September 2026; i makes its id.
func measureEvent(day, i int) store.Event {
	at := time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC).Add(time.Duration(i%86400) * time.Second)
	return store.Event{
		ID:        fmt.Sprintf("%08x-%04x-4%03x-a%03x-%012x", i*2654435761%0xffffffff, i%0xffff, i%0xfff, (i/7)%0xfff, i*40503),
		ProjectID: 1, Family: store.FamilyProduct, EventName: fmt.Sprintf("event_%d", i%20), TS: at, ReceivedAt: at.Add(time.Second),
		Kind: "web", ActorID: fmt.Sprintf("%016x", i*7919%100000), ActorKind: store.ActorConnection,
		SessionID: fmt.Sprintf("s%015x", i/30), Host: "www.example.com", Path: fmt.Sprintf("/products/%d/details", i%500),
		ReferrerSource: "google", Platform: "web", OS: "macOS", OSVersion: "14.5", Browser: "Chrome", BrowserVersion: "129.0",
		BrowserLocale: "en-US", Device: "desktop", Country: "DE", DisplayWidth: 1920, DisplayHeight: 1080,
		Consent: store.ConsentGiven, SampleRate: 1,
		Attributes: map[string]string{
			"plan": []string{"free", "pro", "team"}[i%3], "step": strconv.Itoa(i % 7), "cart_value": fmt.Sprintf("%d.%02d", i%300, i%100),
			"currency": "EUR", "experiment": fmt.Sprintf("exp-%d", i%13), "variant": []string{"a", "b"}[i%2],
			"item_id": fmt.Sprintf("sku-%06d", i%20000), "category": fmt.Sprintf("category-%d", i%40),
			"logged_in": []string{"true", "false"}[i%2], "source_page": fmt.Sprintf("/landing/%d", i%60),
			"order_id": fmt.Sprintf("%08X-%04X-4ABC-8DEF-%012X", i, i%0xffff, i), "coupon": []string{"", "SPRING10", "WELCOME"}[i%3] + "x",
		},
	}
}

// writeMeasureLog writes events of day to path, ids from first, until it
// holds mb megabytes (or n events, when n > 0). It returns the next id.
func writeMeasureLog(t *testing.T, path string, day, first, mb, n int) int {
	t.Helper()
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	w := bufio.NewWriter(fh)
	i, size := first, 0
	for (n > 0 && i-first < n) || (n == 0 && size < mb<<20) {
		b, err := line(measureEvent(day, i))
		if err != nil {
			t.Fatal(err)
		}
		w.Write(b)
		size += len(b)
		i++
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return i
}

func measureInts(env, def string) []int {
	s := cmp.Or(os.Getenv(env), def)
	var out []int
	for _, p := range strings.Split(s, ",") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

type measureDir struct{ data, raw string }

func newMeasureDir(t *testing.T) measureDir {
	d := measureDir{data: t.TempDir()}
	d.raw = filepath.Join(d.data, "raw")
	os.MkdirAll(d.raw, 0o750)
	return d
}

// exec runs a COPY of query into a scratch file in a fresh child; "" means
// it went through.
func (d measureDir) exec(t *testing.T, limit, threads int, query string) (string, time.Duration) {
	t.Helper()
	self, _ := os.Executable()
	eng, err := engine.New(engine.Options{Path: self, Env: []string{childEnv + "=1"},
		AllowedDirs: []string{d.raw}, SpillRoot: filepath.Join(d.data, "spill"), SlotDir: filepath.Join(d.data, "slots"),
		MaxChildren: 1, MemoryLimit: fmt.Sprintf("%dMB", limit), Threads: threads})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	out := filepath.Join(d.raw, "out.parquet")
	defer os.Remove(out)
	start := time.Now()
	err = eng.Exec(context.Background(), "COPY ("+query+") TO "+quote(out)+" (FORMAT parquet, COMPRESSION zstd)")
	if err != nil {
		return strings.SplitN(err.Error(), "\n", 2)[0], time.Since(start)
	}
	return "", time.Since(start)
}

// largest logs, per cell, the largest size that went through, with every
// smaller size through too ("-" for none).
type largest map[string]int

func (l largest) table(t *testing.T, what string, variants []string) {
	t.Logf("largest %s that went through (MB, all smaller sizes too):", what)
	for _, threads := range measureInts("MEASURE_THREADS", "1,2,4") {
		for _, limit := range measureInts("MEASURE_LIMITS", "256,512") {
			row := fmt.Sprintf("threads=%d limit=%dMB", threads, limit)
			for _, v := range variants {
				row += fmt.Sprintf("  %s=%d", v, l[fmt.Sprint(threads, limit, v)])
			}
			t.Log(row)
		}
	}
}

func TestMeasureDump(t *testing.T) {
	d := newMeasureDir(t)
	variants := map[string]func(src string) (string, error){
		"old": func(src string) (string, error) {
			return "SELECT DISTINCT ON (id) * FROM read_ndjson(" + quote(src) + ", sample_size=-1) ORDER BY family, ts", nil
		},
		"new": dumpQuery,
	}
	names := strings.Split(cmp.Or(os.Getenv("MEASURE_VARIANTS"), "old,new"), ",")
	best := largest{}
	for _, threads := range measureInts("MEASURE_THREADS", "1,2,4") {
		for _, limit := range measureInts("MEASURE_LIMITS", "256,512") {
			for _, name := range names {
				failed := false
				for _, mb := range measureInts("MEASURE_SIZES", "15,30,46,61,76,92,122,153,184,214,245") {
					src := filepath.Join(d.raw, fmt.Sprintf("%d.log", mb))
					if _, err := os.Stat(src); err != nil {
						writeMeasureLog(t, src, 11, 0, mb, 0)
					}
					q, err := variants[name](src)
					if err != nil {
						t.Fatal(err)
					}
					res, took := d.exec(t, limit, threads, q)
					t.Logf("dump threads=%d limit=%dMB log=%dMB %s: %s (%s)", threads, limit, mb, name, cmp.Or(res, "OK"), took.Round(10*time.Millisecond))
					if res != "" {
						failed = true
					} else if !failed {
						best[fmt.Sprint(threads, limit, name)] = mb
					}
				}
			}
		}
	}
	best.table(t, "log", names)
}

// TestMeasureMerge merges a month of MEASURE_MONTH_EVENTS events (default
// 1M, the cap) over 30 day files, each dumped by the new dump.
func TestMeasureMerge(t *testing.T) {
	d := newMeasureDir(t)
	total := measureInts("MEASURE_MONTH_EVENTS", "1000000")[0]
	var files []string
	next := 0
	var logBytes int64
	for day := 1; day <= 30; day++ {
		src := filepath.Join(d.raw, fmt.Sprintf("2026-09-%02d.log", day))
		next = writeMeasureLog(t, src, day, next, 0, total/30)
		if st, err := os.Stat(src); err == nil {
			logBytes += st.Size()
		}
		q, err := dumpQuery(src)
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(d.raw, fmt.Sprintf("2026-09-%02d.parquet", day))
		self, _ := os.Executable()
		eng, err := engine.New(engine.Options{Path: self, Env: []string{childEnv + "=1"},
			AllowedDirs: []string{d.raw}, SpillRoot: filepath.Join(d.data, "spill"), SlotDir: filepath.Join(d.data, "slots"),
			MaxChildren: 1, MemoryLimit: "2GB", Threads: 4})
		if err != nil {
			t.Fatal(err)
		}
		if err := eng.Exec(context.Background(), "COPY ("+q+") TO "+quote(out)+" (FORMAT parquet, COMPRESSION zstd)"); err != nil {
			t.Fatal(err)
		}
		eng.Close()
		os.Remove(src)
		files = append(files, out)
	}
	var parquetBytes int64
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			parquetBytes += st.Size()
		}
	}
	t.Logf("month: %d events, %d MB of logs, %d MB of day files", next, logBytes>>20, parquetBytes>>20)
	list := make([]string, len(files))
	for i, f := range files {
		list[i] = quote(f)
	}
	variants := map[string]string{
		"old": "SELECT DISTINCT ON (id) * FROM read_parquet([" + strings.Join(list, ", ") + "], union_by_name=true) ORDER BY family, ts",
		"new": mergeQuery(files),
	}
	names := strings.Split(cmp.Or(os.Getenv("MEASURE_VARIANTS"), "old,new"), ",")
	for _, threads := range measureInts("MEASURE_THREADS", "1,2,4") {
		for _, limit := range measureInts("MEASURE_LIMITS", "256,512") {
			for _, name := range names {
				res, took := d.exec(t, limit, threads, variants[name])
				t.Logf("merge threads=%d limit=%dMB events=%d %s: %s (%s)", threads, limit, next, name, cmp.Or(res, "OK"), took.Round(10*time.Millisecond))
			}
		}
	}
}
