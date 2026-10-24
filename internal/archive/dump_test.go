package archive

// logKeys, the old dump's key scan, stays for the tests of the SQL the
// Reader runs (derive_test.go's logRel).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/golden"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// logKeys returns every key of a log's lines, for logSelect (dumpLog
// collects them as it reads). A line that is not a JSON object fails it,
// as DuckDB would.
func logKeys(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	defer f.Close()
	keys := map[string]bool{}
	dec := json.NewDecoder(bufio.NewReader(f))
	for {
		var m map[string]json.RawMessage
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			return keys, nil
		} else if err != nil {
			return nil, fmt.Errorf("archive: %s: %w", filepath.Base(path), err)
		}
		if m == nil {
			return nil, fmt.Errorf("archive: %s: a line is not an object", filepath.Base(path))
		}
		for k := range m {
			keys[k] = true
		}
	}
}

// logOf is evs as a log's content, one line each.
func logOf(t *testing.T, evs ...store.Event) string {
	t.Helper()
	var b strings.Builder
	for _, e := range evs {
		l, err := line(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(l)
	}
	return b.String()
}

// dumpView is evOf as a page view.
func dumpView(id, at string) store.Event {
	e := evOf(1, id, at)
	e.Family, e.EventName = store.FamilyViews, "$page_view"
	return e
}

// measureOf is evOf as a measure of value.
func measureOf(id, at string, value float64) store.Event {
	e := evOf(1, id, at)
	e.Family, e.EventName, e.Measure, e.SampleRate = store.FamilyMeasures, "$web_vitals", "lcp", 0.5
	e.Value = &value
	return e
}

// dumpCase is one log the dump tests read. allowed names the ids whose
// column attr_n holds a JSON number, the one allowed difference from the
// golden (frozen from the dump the old query engine ran): the Go dump
// keeps its text as written (as native's live log read does), the old
// dump gave its own form (1.50 → "1.5"), kept in was as the golden
// keeps it. Ingest writes every VARCHAR as a string, so no real line has
// one.
type dumpCase struct {
	name, log string
	allowed   map[string]string // id → the Go dump's attr_n
	was       map[string]string // id → the old dump's attr_n, as the golden keeps it
}

// dumpCases are the logs of every dump test in maintain_test.go, and
// lines the ingest never writes that the decoder must still read as
// DuckDB did.
func dumpCases(t *testing.T) []dumpCase {
	view := dumpView("v", "2026-10-11T09:00:00Z")
	torn := logOf(t, evOf(1, "a", "2026-10-11T10:00:00Z"), evOf(1, "b", "2026-10-11T11:00:00Z")) + `{"v":1,"id":"torn","project_id":1,"fam`
	const uuid = "ABCDEF01-2345-6789-ABCD-EF0123456789"
	cases := []dumpCase{
		{name: "closed days", log: logOf(t, evOf(1, "a", "2026-10-11T10:00:00Z"), evOf(1, "b", "2026-10-11T11:00:00Z"))},
		{name: "family and ts order", log: logOf(t, evOf(1, "late", "2026-10-11T23:00:00Z"), view,
			evOf(1, "early", "2026-10-11T01:00:00Z"), evOf(1, "mid", "2026-10-11T12:00:00Z"))},
		{name: "duplicate ids", log: logOf(t, evOf(1, "a", "2026-10-11T10:00:00Z"), evOf(1, "a", "2026-10-11T10:00:00Z"))},
		{name: "torn last line", log: torn},
		{name: "one line", log: logOf(t, evOf(1, "a", "2026-10-11T10:00:00Z"))},
		{name: "types day 1", log: logOf(t,
			withAttrs(evOf(1, "a", "2026-09-01T10:00:00Z"), uuid, map[string]string{"d": "2026-10-12", "t": "12:30"}),
			withAttrs(evOf(1, "b", "2026-09-01T11:00:00Z"), uuid, map[string]string{"d": "2026-10-13", "t": "12:31"}))},
		{name: "types day 2", log: logOf(t, withAttrs(evOf(1, "c", "2026-09-02T10:00:00Z"), "hello", map[string]string{"d": "word", "t": "word"}))},
		{name: "keys equal but for case", log: logOf(t,
			withAttrs(evOf(1, "a", "2026-10-11T10:00:00Z"), "", map[string]string{"Plan": "A", "plan": "b"}),
			withAttrs(evOf(1, "b", "2026-10-11T11:00:00Z"), "", map[string]string{"plan": "c", "plan_1": "z"}))},
		{name: "any key", log: logOf(t, withAttrs(evOf(1, "a", "2026-10-11T10:00:00Z"), "", map[string]string{"a-b c": "1", `q"u'o`: "2",
			"x/y~z": "3", "a$.[0]": "4", "nul\x00k": "5", `nul\u0000k`: "6", "t\tb\nc\x01\x7f": "7"}))},
		{name: "merge copies", log: logOf(t, withAttrs(evOf(1, "a", "2026-09-30T10:00:00Z"), "", map[string]string{"copy": "1"}),
			withAttrs(evOf(1, "a", "2026-09-30T10:00:00Z"), "", map[string]string{"copy": "2"}))},
		{name: "families and measures", log: logOf(t, measureOf("m2", "2026-10-11T12:00:00Z", 2512.25), dumpView("v2", "2026-10-11T08:00:00Z"),
			evOf(1, "p", "2026-10-11T08:00:00Z"), measureOf("m1", "2026-10-11T07:00:00Z", -0.1), dumpView("v1", "2026-10-11T08:00:00Z"),
			dumpView("v3", "2026-10-11T08:00:00Z"))},
		{name: "hand-written", log: strings.Join([]string{
			`{"v":1,"family":"product","ts":"2026-10-11T10:00:00Z","attr_x":"first null id"}`,
			`{"v":1,"family":"product","ts":"2026-10-11T09:00:00Z","attr_x":"second null id"}`,
			`{"v":1,"id":null,"family":"views","attr_x":"third null id"}`,
			`{"v":1,"id":"no-family","ts":"2026-10-11T08:00:00Z"}`,
			`{"v":1,"id":"no-ts","family":"product","day":"2026-10-11"}`,
			`{"v":1,"id":"neither"}`,
			``,
			`{"v":1.0,"id":"casts","family":"product","ts":"2026-10-11 10:00:00.5","received_at":"2026-10-11T12:00:00.123456Z",` +
				`"display_width":"1280.5","display_height":-7,"consent":"1","value":"0.25","sample_rate":1e-3,"day":"2026-10-11"}`,
			`{"v":1,"id":"empty","family":"","ts":"2026-10-11T10:00:00Z","attr_x":"","user_id":""}`,
			`{"v":1,"id":"é ","family":"product","ts":"2026-10-11T10:00:00Z","attr_x":"é \"\\"}`,
			`{"v":1,"id":"rounded","family":"product","ts":"2026-10-11T10:00:00Z","display_width":"-2.5","consent":"1280.6"}`,
			``}, "\n")},
		{name: "a JSON number in a VARCHAR", log: strings.Join([]string{
			`{"v":1,"id":"n1","family":"product","ts":"2026-10-11T10:00:00Z","attr_n":1.50}`,
			`{"v":1,"id":"n2","family":"product","ts":"2026-10-11T10:00:01Z","attr_n":1e3}`,
			`{"v":1,"id":"n3","family":"product","ts":"2026-10-11T10:00:02Z","attr_n":true}`,
			`{"v":1,"id":"n4","family":"product","ts":"2026-10-11T10:00:03Z","attr_n":"1.50"}`,
			""}, "\n"),
			allowed: map[string]string{"n1": "1.50", "n2": "1e3"}, was: map[string]string{"n1": "[1.5]", "n2": "[1000.0]"}},
	}
	return cases
}

// bigDumpLog is TestDumpKeepsAnIDsFirstLine's log, about 45 MB: three
// copies of 6,000 ids, each line padded with 2,400 bytes.
func bigDumpLog(t *testing.T) string {
	t.Helper()
	pad := strings.Repeat("x", 2400)
	var b strings.Builder
	for copy := 1; copy <= 3; copy++ {
		for i := 0; i < 6000; i++ {
			b.WriteString(logOf(t, withAttrs(evOf(1, fmt.Sprintf("id-%05d", i), "2026-10-11T10:00:00Z"), "",
				map[string]string{"copy": strconv.Itoa(copy), "pad": pad})))
		}
	}
	return b.String()
}

// TestGoDumpMatchesDuckDB: for every log of the dump tests, and lines the
// ingest never writes, the Go dump's file, read back, is its golden (the
// file the old query engine's COPY of the dump's SELECT wrote): the same
// columns of the same types in the same order, and the same rows. The old
// dump left the order of rows equal in (family, ts) unspecified, so the
// rows are compared as a multiset and the Go file's own order is checked:
// (family, ts) non-decreasing, NULLs last. The one allowed difference is
// dumpCase's: a JSON number in a VARCHAR column keeps its text.
func TestGoDumpMatchesDuckDB(t *testing.T) {
	f := newFixture(t)
	cases := append(dumpCases(t), dumpCase{name: "45 MB of repeated ids", log: bigDumpLog(t)})
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := f.project1(fmt.Sprintf("2026-10-%02d.parquet.dumping", i+1))
			f.writeFile(t, filepath.Base(src), c.log)
			if err := cutTornLine(src); err != nil { // as claim does
				t.Fatal(err)
			}
			goFile := f.project1(fmt.Sprintf("go-%d.parquet", i))
			if err := dumpLog(context.Background(), src, goFile); err != nil {
				t.Fatal(err)
			}
			p := readTable(t, goFile)
			orderedByFamilyAndTS(t, p)
			// The allowed ids' attr_n: the JSON text as written, then NULL
			// in the rows the golden holds, which keeps the old form apart.
			if len(c.allowed) > 0 {
				id, n := p.col(t, "id"), p.col(t, "attr_n")
				for _, r := range p.rows {
					text, ok := c.allowed[fmt.Sprint(r[id])]
					if !ok {
						continue
					}
					if got := fmt.Sprint([]any{r[n]}); got != "["+text+"]" || got == c.was[fmt.Sprint(r[id])] {
						t.Errorf("%s: attr_n = %v, want the JSON text %s, not the golden's %s", r[id], got, text, c.was[fmt.Sprint(r[id])])
					}
					r[n] = nil
				}
			}
			type dumpAnswer struct {
				File    fileAnswer        `json:"file"`
				Allowed map[string]string `json:"allowed_attr_n,omitempty"`
			}
			got := dumpAnswer{File: answerOf(t, p, false), Allowed: c.was}
			want := golden.Get(t, golden.For(t, *updateGolden), c.name, got)
			sameAnswer(t, "Go dump", got.File, want.File)
			if !maps.Equal(got.Allowed, want.Allowed) {
				t.Errorf("allowed attr_n %v, the golden's %v", got.Allowed, want.Allowed)
			}
		})
	}
}

// orderedByFamilyAndTS fails unless p's rows, in file order, are
// non-decreasing in family, then ts, NULLs last.
func orderedByFamilyAndTS(t *testing.T, p pqTable) {
	t.Helper()
	rows := p.pick(t, "family", "ts")
	key := func(r []any) (string, int64, bool, bool) {
		fam, famOK := r[0].(string)
		var ts int64 = math.MaxInt64
		tsOK := r[1] != nil
		if tsOK {
			if _, err := fmt.Sscan(fmt.Sprint(epochUS(t, r[1])), &ts); err != nil {
				t.Fatalf("ts %v: %v", r[1], err)
			}
		}
		return fam, ts, famOK, tsOK
	}
	for i := 1; i < len(rows); i++ {
		pf, pt, pfOK, ptOK := key(rows[i-1])
		nf, nt, nfOK, ntOK := key(rows[i])
		famCmp := 0
		switch {
		case pfOK && nfOK:
			famCmp = strings.Compare(pf, nf)
		case pfOK:
			famCmp = -1
		case nfOK:
			famCmp = 1
		}
		if famCmp > 0 || (famCmp == 0 && ((!ptOK && ntOK) || (ptOK && ntOK && pt > nt))) {
			t.Fatalf("row %d (%v) after row %d (%v): not in (family, ts) order", i, rows[i], i-1, rows[i-1])
		}
	}
}

// TestDumpFailsWhereDuckDBDid: a line that is not a JSON object, or a value
// its column cannot hold, fails the Go dump as it failed the old one (the
// golden), leaving no file and no .tmp.
func TestDumpFailsWhereDuckDBDid(t *testing.T) {
	f := newFixture(t)
	for i, log := range []string{
		"[1]\n",
		`{"id":"a"}` + "\n" + "garbage\n",
		`{"id":"a","ts":"yesterday"}` + "\n",
		`{"id":"a","display_width":"wide"}` + "\n",
		`{"id":"a","day":"10.11.2026"}` + "\n",
		`{"id":"a","value":"x"}` + "\n",
		`{"id":"a","consent":true}` + "\n",
		`{"id":"a"}` + "\n" + `{"id":"a","ts":"yesterday"}` + "\n", // a repeat's ts is cast too
		`{"v":1}` + "\n" + `{"v":2,"ts":"yesterday"}` + "\n",       // and a second line without an id
	} {
		src := f.project1(fmt.Sprintf("2026-10-%02d.parquet.dumping", i+1))
		f.writeFile(t, filepath.Base(src), log)
		out := strings.TrimSuffix(src, ".dumping")
		if failed := frozen(t, log, dumpLog(context.Background(), src, out) != nil); !failed {
			t.Errorf("%q: the Go dump went through", log)
		}
		if exists(out) || exists(out+".tmp") {
			t.Errorf("%q: the failed dump left a file", log)
		}
	}
}

// TestDumpKeepsTiesInFileOrder: rows equal in family and ts keep the order
// of their lines (a stable sort), and a row group is cut where the family
// changes, and where the day does (a late event's) only once the group
// holds dayCutRows rows.
func TestDumpKeepsTiesInFileOrder(t *testing.T) {
	f := newFixture(t)
	late := evOf(1, "p-late-day", "2026-10-11T10:00:00Z")
	late.TS = ts("2026-10-12T00:30:00Z") // a late event of the next day's file name: day follows ts
	f.writeFile(t, "2026-10-11.parquet.dumping", logOf(t,
		dumpView("v-b", "2026-10-11T10:00:00Z"), evOf(1, "p-z", "2026-10-11T10:00:00Z"), dumpView("v-a", "2026-10-11T10:00:00Z"),
		evOf(1, "p-a", "2026-10-11T10:00:00Z"), late, evOf(1, "p-m", "2026-10-11T10:00:00Z")))
	out := f.project1("2026-10-11.parquet")
	if err := dumpLog(context.Background(), out+".dumping", out); err != nil {
		t.Fatal(err)
	}
	got := texts(readTable(t, out, "id").pick(t, "id"))
	if want := []string{"[p-z]", "[p-a]", "[p-m]", "[p-late-day]", "[v-b]", "[v-a]"}; !eq(got, want) {
		t.Fatalf("rows = %v, want %v: ties in file order", got, want)
	}
	if got := groupRows(t, out); fmt.Sprint(got) != "[4 2]" {
		t.Fatalf("row groups = %v, want [4 2]: cut at the family only", got)
	}
	setDayCutRows(t, 2)
	os.Remove(out)
	f.writeFile(t, "2026-10-11.parquet.dumping", logOf(t,
		dumpView("v-b", "2026-10-11T10:00:00Z"), evOf(1, "p-z", "2026-10-11T10:00:00Z"), dumpView("v-a", "2026-10-11T10:00:00Z"),
		evOf(1, "p-a", "2026-10-11T10:00:00Z"), late, evOf(1, "p-m", "2026-10-11T10:00:00Z")))
	if err := dumpLog(context.Background(), out+".dumping", out); err != nil {
		t.Fatal(err)
	}
	if got := groupRows(t, out); fmt.Sprint(got) != "[3 1 2]" {
		t.Fatalf("row groups = %v, want [3 1 2]: past 2 rows, cut at the day, then at the family", got)
	}
}

// TestDumpSendsTheEngineNothing: claiming and dumping a log, the 45 MB one
// included, runs in Go (the Archive holds no engine).
func TestDumpSendsTheEngineNothing(t *testing.T) {
	f := newFixture(t)
	a := f.a
	f.writeFile(t, "2026-10-11.1.closed", bigDumpLog(t))
	dir := filepath.Join(f.log.Dir(), "1")
	if err := a.claim(dir, "2026-10-11.1.closed", "2026-10-11"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := a.dump(context.Background(), dir, "2026-10-11.parquet.dumping"); err != nil {
		t.Fatal(err)
	}
	t.Logf("dumped 45 MB in %s", time.Since(start).Round(time.Millisecond))
	if got := f.files(t); !eq(got, []string{"2026-10-11.parquet"}) {
		t.Fatalf("files = %v, want the day file alone", got)
	}
	if st, err := os.Stat(f.project1("2026-10-11.parquet")); err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("day file: %v, %v; want 0640", st, err)
	}
	if n, ids := f.rows(t, "2026-10-11.parquet"); n != 6000 || ids != 6000 {
		t.Fatalf("day file: %d rows, %d ids; want 6000 and 6000", n, ids)
	}
}

// manyKeysLog is lines product events, each with keys attributes of
// its own: lines × keys distinct keys in all.
func manyKeysLog(t *testing.T, lines, keys int) string {
	t.Helper()
	var b strings.Builder
	for i := 0; i < lines; i++ {
		attrs := map[string]string{}
		for k := 0; k < keys; k++ {
			attrs[fmt.Sprintf("k%05d_%d", i, k)] = "v"
		}
		b.WriteString(logOf(t, withAttrs(evOf(1, fmt.Sprintf("id-%05d", i), "2026-10-11T10:00:00Z"), "", attrs)))
	}
	return b.String()
}

// heapAfterGC is the live heap after a collection.
func heapAfterGC() uint64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// TestDumpHoldsALineAsWhereItIs: pass 1 holds a kept line as where it is
// in the log, whatever keys it brings: a log whose every line has keys of
// its own (5,000 lines, 3 keys each: 15,000 keys) costs the index its
// keys and a few dozen bytes a line, not lines times keys (dense rows
// would be 5,000 × 15,041 × 32 bytes, 2.4 GB). Such a log dumps; the
// writer's buffers per column are logged, not bounded.
func TestDumpHoldsALineAsWhereItIs(t *testing.T) {
	f := newFixture(t)
	f.writeFile(t, "2026-10-11.parquet.dumping", manyKeysLog(t, 5000, 3))
	src := f.project1("2026-10-11.parquet.dumping")
	before := heapAfterGC()
	d := newDumpIndex()
	fh, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if err := d.read(context.Background(), fh, filepath.Base(src)); err != nil {
		t.Fatal(err)
	}
	held := int64(heapAfterGC()) - int64(before)
	runtime.KeepAlive(d)
	t.Logf("read: %d keys, %d lines, %.1f MB held", len(d.keys), len(d.lines), float64(held)/(1<<20))
	if len(d.keys) != len(fields)+15000 || len(d.lines) != 5000 {
		t.Fatalf("%d keys, %d rows; want %d and 5000", len(d.keys), len(d.lines), len(fields)+15000)
	}
	if held > 32<<20 {
		t.Fatalf("the read holds %d MB for 5,000 lines, want under 32 MB", held>>20)
	}
	// The writer holds buffers per column (concern for the pass's budget):
	// the dump itself runs on a tenth of the keys, so make check stays light.
	small := f.project1("2026-10-12.parquet.dumping")
	f.writeFile(t, filepath.Base(small), manyKeysLog(t, 500, 3))
	var peak atomic.Uint64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc)
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	base := heapAfterGC()
	start := time.Now()
	err = dumpLog(context.Background(), small, f.project1("2026-10-12.parquet"))
	close(stop)
	<-sampled
	if err != nil {
		t.Fatal(err)
	}
	grew := int64(peak.Load()) - int64(base)
	t.Logf("dump of 500 lines, %d columns: %s, heap peak %.0f MB above the start (%.0f KB a column)", len(fields)+1500,
		time.Since(start).Round(time.Millisecond), float64(grew)/(1<<20), float64(grew)/1024/float64(len(fields)+1500))
	back := readTable(t, f.project1("2026-10-12.parquet"), "attr_k00250_1").pick(t, "attr_k00250_1")
	if got := fmt.Sprint(len(back), len(slices.DeleteFunc(back, func(r []any) bool { return r[0] == nil }))); got != "500 1" {
		t.Fatalf("rows = %v", got)
	}
}

// TestDumpStopsWhenCancelled: a dump whose ctx is cancelled, before
// either pass or during the second, writes no target and no .tmp, and the
// pass keeps the claimed log for the next one.
func TestDumpStopsWhenCancelled(t *testing.T) {
	f := newFixture(t)
	f.writeFile(t, "2026-10-11.parquet.dumping", manyKeysLog(t, 3*dumpCheckEvery, 1))
	src := f.project1("2026-10-11.parquet.dumping")
	out := f.project1("2026-10-11.parquet")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := filepath.Join(f.log.Dir(), "1")
	if err := f.a.dump(ctx, dir, filepath.Base(src)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !exists(src) || exists(out) || exists(out+".tmp") {
		t.Fatalf("files = %v, want the claimed log alone", f.files(t))
	}
	// Cancelled in pass 2: after pass 1's index is sorted.
	d := newDumpIndex()
	fh, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if err := d.read(context.Background(), fh, "log"); err != nil {
		t.Fatal(err)
	}
	cols := columnsOf(d.keySet())
	pq := make([]pqColumn, len(cols))
	col := make([]int, len(d.keys))
	for i, c := range cols {
		pq[i] = pqColumn{Name: c.name, Type: d.types[d.index[c.key]]}
		col[d.index[c.key]] = i
	}
	d.sort()
	w, err := newRowWriter(out, pq, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.write(ctx, fh, "log", w, col); !errors.Is(err, context.Canceled) {
		t.Fatalf("pass 2: err = %v, want context.Canceled", err)
	}
	w.Abort()
	if exists(out) || exists(out+".tmp") {
		t.Fatalf("files = %v after pass 2 stopped, want the claimed log alone", f.files(t))
	}
}
