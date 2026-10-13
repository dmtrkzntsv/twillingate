//go:build measure

// The dump's memory, old (inferred types) against new (typed): go test
// -tags measure -run TestMeasureDump -v ./internal/archive. Not part of
// make check. MEASURE_SIZES (MB, comma-separated) and MEASURE_LIMITS
// override the defaults.
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

// measureEvent is a web product event with 12 attributes.
func measureEvent(i int) store.Event {
	at := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC).Add(time.Duration(i%86400) * time.Second)
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

// writeMeasureLog writes events to path until it holds mb megabytes.
func writeMeasureLog(t *testing.T, path string, mb int) int {
	t.Helper()
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	w := bufio.NewWriter(fh)
	n, size := 0, 0
	for size < mb<<20 {
		b, err := line(measureEvent(n))
		if err != nil {
			t.Fatal(err)
		}
		w.Write(b)
		size += len(b)
		n++
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return n
}

func measureInts(env, def string) []int {
	s := os.Getenv(env)
	if s == "" {
		s = def
	}
	var out []int
	for _, p := range strings.Split(s, ",") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

func TestMeasureDump(t *testing.T) {
	data := t.TempDir()
	raw := filepath.Join(data, "raw")
	os.MkdirAll(raw, 0o750)
	self, _ := os.Executable()
	variants := map[string]func(src string) (string, error){
		"old": func(src string) (string, error) {
			return "SELECT DISTINCT ON (id) * FROM read_ndjson(" + quote(src) + ", sample_size=-1) ORDER BY family, ts", nil
		},
		"new": dumpQuery,
	}
	for _, limit := range measureInts("MEASURE_LIMITS", "256,512") {
		for _, mb := range measureInts("MEASURE_SIZES", "30,61,92,122,153,184") {
			src := filepath.Join(raw, fmt.Sprintf("%d.log", mb))
			if _, err := os.Stat(src); err != nil {
				writeMeasureLog(t, src, mb)
			}
			for _, name := range strings.Split(cmp.Or(os.Getenv("MEASURE_VARIANTS"), "old,new"), ",") {
				eng, err := engine.New(engine.Options{Path: self, Env: []string{childEnv + "=1"},
					AllowedDirs: []string{raw}, SpillRoot: filepath.Join(data, "spill"), SlotDir: filepath.Join(data, "slots"),
					MaxChildren: 1, MemoryLimit: fmt.Sprintf("%dMB", limit), Threads: 1})
				if err != nil {
					t.Fatal(err)
				}
				q, err := variants[name](src)
				if err != nil {
					t.Fatal(err)
				}
				out := filepath.Join(raw, "out.parquet")
				start := time.Now()
				err = eng.Exec(context.Background(), "COPY ("+q+") TO "+quote(out)+" (FORMAT parquet, COMPRESSION zstd)")
				res := "OK"
				if err != nil {
					res = "FAIL " + strings.SplitN(err.Error(), "\n", 2)[0]
				}
				t.Logf("limit=%dMB log=%dMB %s: %s (%s)", limit, mb, name, res, time.Since(start).Round(10*time.Millisecond))
				eng.Close()
				os.Remove(out)
			}
		}
	}
}
