package native

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// readCached is readLog(path, cols, family, r), checked against the log
// cache's read of it: from a cold cache, from the same cache warm, and
// from a cache a query of other columns filled first. Each must give readLog's batch (its columns,
// their kinds and values) or its error.
func readCached(t *testing.T, path string, cols []string, family string, r dayRange) (*batch, error) {
	t.Helper()
	want, wantErr := readLog(path, cols, family, r)
	cold := newLogCache(1 << 30)
	other := newLogCache(1 << 30)
	_, _ = other.read(context.Background(), path, []string{"id", "event_name"}, "views", r) // only warms it
	for _, c := range []struct {
		name  string
		cache *logCache
	}{{"cold", cold}, {"warm", cold}, {"filled by other columns", other}} {
		got, err := c.cache.read(context.Background(), path, cols, family, r)
		sameBatch(t, c.name, got, err, want, wantErr)
	}
	return want, wantErr
}

// sameBatch fails unless got (or gotErr) holds want's rows (or is
// wantErr): every batch with want's columns, each of its kinds, and
// together want's rows, in any order (the cache hands a batch per
// family).
func sameBatch(t *testing.T, name string, got []*batch, gotErr error, want *batch, wantErr error) {
	t.Helper()
	if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
		t.Fatalf("%s: err %v, want %v", name, gotErr, wantErr)
	}
	if wantErr != nil {
		return
	}
	if len(got) == 0 {
		t.Fatalf("%s: no batch", name)
	}
	names := keys(want.Cols)
	var rows [][]string
	n := 0
	for _, b := range got {
		if !slices.Equal(keys(b.Cols), names) {
			t.Fatalf("%s: columns %v, want %v", name, keys(b.Cols), names)
		}
		for _, c := range names {
			if g, w := colKindOf(b.Cols[c], b.N), colKindOf(want.Cols[c], want.N); g != "null" && w != "null" && g != w {
				t.Fatalf("%s: column %s is %s, want %s", name, c, g, w)
			}
		}
		rows = formatBatch(t, b, names, rows)
		n += b.N
	}
	wantRows := formatBatch(t, want, names, nil)
	sortRows(rows)
	sortRows(wantRows)
	if n != want.N || !slices.EqualFunc(rows, wantRows, slices.Equal) {
		t.Fatalf("%s:\n got %q\nwant %q\ncols %v", name, rows, wantRows, names)
	}
}

// colKindOf is c's type, "null" when no row of n has a value (an all-null
// column's type is any).
func colKindOf(c *column, n int) string {
	for i := range n {
		switch {
		case c.Idx != nil:
			if c.Idx[i] >= 0 {
				return "string"
			}
		case c.Null != nil && c.Null[i]:
		case c.Times != nil:
			return "time"
		case c.Floats != nil:
			return "float"
		default:
			return "int"
		}
	}
	return "null"
}

// countDecodes counts the cache's decodes, by path, as offsets.
func countDecodes(t *testing.T) func(path string) []int64 {
	t.Helper()
	var mu sync.Mutex
	seen := map[string][]int64{}
	old := onLogDecode
	onLogDecode = func(path string, offset int64) {
		mu.Lock()
		seen[path] = append(seen[path], offset)
		mu.Unlock()
	}
	t.Cleanup(func() { onLogDecode = old })
	return func(path string) []int64 {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(seen[path])
		seen[path] = nil
		return out
	}
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	w, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

// A log that grows between queries decodes only what it grew by; the
// torn line ingest is still writing is read per query, not kept, and
// kept once its newline comes; an id repeated in a later append is the
// first line's still.
func TestLogCacheGrows(t *testing.T) {
	decodes := countDecodes(t)
	f := archivetest.New(t)
	evs := events(t, 1, today, 3)
	f.Write(t, evs...)
	path := logOf(t, f, 1)
	c := newLogCache(1 << 30)
	cols := []string{"id", "family", "event_name", "path", "ts", "display_width", attrCols}
	day := rng(t, today, today)
	check := func(step string, wantDecodes []int64) {
		t.Helper()
		for _, fam := range []string{"", "views", "product"} {
			want, wantErr := readLog(path, cols, fam, day)
			got, err := c.read(context.Background(), path, cols, fam, day)
			sameBatch(t, step+" "+fam, got, err, want, wantErr)
		}
		if got := decodes(path); !slices.Equal(got, wantDecodes) {
			t.Errorf("%s: decodes at %v, want %v", step, got, wantDecodes)
		}
	}
	check("first", []int64{0})
	check("again", nil)

	size := fileSize(t, path)
	again := slices.Clone(evs[:2])
	for i := range again {
		again[i].EventName = "repeat" // a repeat need not be the same event
	}
	f.Write(t, again...)
	f.Write(t, events(t, 1, today, 1)[0:1]...) // ids of a second batch: new
	more := events(t, 1, today, 1)
	for i := range more {
		more[i].ID += "-more"
	}
	f.Write(t, more...)
	check("grown", []int64{size})

	size = fileSize(t, path)
	appendFile(t, path, `{"v":1,"id":"torn","family":"product","day":"`+today+`","attr_torn":"x`)
	// Read past the last line by each query (three a check), never kept.
	check("torn", []int64{size, size, size})
	check("torn again", []int64{size, size, size})
	if x := c.entries[path]; x == nil || x.size != size {
		t.Fatalf("entry %+v, want its size %d", x, size)
	}
	appendFile(t, path, `y"}`+"\n")
	check("completed", []int64{size})
	check("completed again", nil)

	// A last line that is a whole object, its newline yet to come, is a
	// row as readLog reads it; so is it once complete.
	size = fileSize(t, path)
	appendFile(t, path, `{"v":1,"id":"whole","family":"views","day":"`+today+`","path":"/w","attr_only_tail":"z","display_width":"7"}`)
	check("whole tail", []int64{size, size, size})
	appendFile(t, path, "\n")
	check("whole tail kept", []int64{size})
	if x := c.entries[path]; x == nil || x.size != fileSize(t, path) {
		t.Fatalf("entry %+v, want all of the log", x)
	}
}

// A log rotated (renamed, a new file under its name) is a new entry, even
// when the new file is larger than what the old one's entry read.
func TestLogCacheRotation(t *testing.T) {
	decodes := countDecodes(t)
	f := archivetest.New(t)
	f.Write(t, events(t, 1, today, 2)...)
	path := logOf(t, f, 1)
	c := newLogCache(1 << 30)
	cols := []string{"id", "event_name", "path"}
	day := rng(t, today, today)
	if _, err := c.read(context.Background(), path, cols, "", day); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, strings.TrimSuffix(path, ".log")+".1.closed"); err != nil {
		t.Fatal(err)
	}
	// The new log: the old one's lines with other ids and names, and more.
	next := strings.ReplaceAll(string(old), `"id":"`, `"id":"new-`)
	next = strings.ReplaceAll(next, `"event_name":"`, `"event_name":"new-`)
	if err := os.WriteFile(path, []byte(next+next), 0o644); err != nil {
		t.Fatal(err)
	}
	decodes(path)
	want, wantErr := readLog(path, cols, "", day)
	got, err := c.read(context.Background(), path, cols, "", day)
	sameBatch(t, "rotated", got, err, want, wantErr)
	if got := decodes(path); !slices.Equal(got, []int64{0}) {
		t.Errorf("decodes at %v, want the new file whole", got)
	}
	if c.size() != c.entries[path].bytes {
		t.Errorf("cache holds %d bytes, its one entry %d", c.size(), c.entries[path].bytes)
	}
}

// The first miss decodes every column the log has: queries of any
// columns after it decode nothing more, and a key first seen in appended
// lines is a column from then on, null for the rows before.
func TestLogCacheDecodesEveryColumnOnce(t *testing.T) {
	decodes := countDecodes(t)
	f := archivetest.New(t)
	f.Write(t, events(t, 1, today, 3)...)
	path := logOf(t, f, 1)
	c := newLogCache(1 << 30)
	day := rng(t, today, today)
	for _, s := range []struct {
		cols    []string
		decodes []int64
	}{
		{[]string{"id"}, []int64{0}},
		{[]string{"id", "path"}, nil},
		{[]string{"path"}, nil},
		{[]string{attrCols}, nil},
		{[]string{"*"}, nil},
		{[]string{"id", "path", attrCols}, nil},
	} {
		want, wantErr := readLog(path, s.cols, "views", day)
		got, err := c.read(context.Background(), path, s.cols, "views", day)
		sameBatch(t, fmt.Sprint(s.cols), got, err, want, wantErr)
		if got := decodes(path); !slices.Equal(got, s.decodes) {
			t.Errorf("%v: decodes at %v, want %v", s.cols, got, s.decodes)
		}
	}
	size := fileSize(t, path)
	appendFile(t, path, `{"v":1,"id":"new-key","family":"views","day":"`+today+`","attr_late":"l"}`+"\n")
	for _, cols := range [][]string{{"id", "attr_late"}, {attrCols}, {"*"}} {
		want, wantErr := readLog(path, cols, "views", day)
		got, err := c.read(context.Background(), path, cols, "views", day)
		sameBatch(t, fmt.Sprint("new key ", cols), got, err, want, wantErr)
	}
	if got := decodes(path); !slices.Equal(got, []int64{size}) {
		t.Errorf("new key: decodes at %v, want the appended line once", got)
	}
}

// Concurrent queries of one log decode it once; queries of other columns
// at the same time all get their own rows.
func TestLogCacheConcurrent(t *testing.T) {
	var n atomic.Int32
	old := onLogDecode
	onLogDecode = func(string, int64) { n.Add(1) }
	t.Cleanup(func() { onLogDecode = old })
	f := archivetest.New(t)
	f.Write(t, events(t, 1, today, 40)...)
	path := logOf(t, f, 1)
	day := rng(t, today, today)
	sets := [][]string{{"id", "path"}, {"id", "event_name", attrCols}, {"ts", "day", "family"}}
	wants := make([]*batch, len(sets))
	for i, cols := range sets {
		b, err := readLog(path, cols, "", day)
		if err != nil {
			t.Fatal(err)
		}
		wants[i] = b
	}
	for round, spread := range []bool{false, true} {
		c := newLogCache(1 << 30)
		n.Store(0)
		var wg sync.WaitGroup
		for g := range 16 {
			i := 0
			if spread {
				i = g % len(sets)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, err := c.read(context.Background(), path, sets[i], "", day)
				sameBatch(t, fmt.Sprint(round, sets[i]), got, err, wants[i], nil)
			}()
		}
		wg.Wait()
		if got := n.Load(); got != 1 {
			t.Errorf("16 queries at once (spread %t): %d decodes, want 1", spread, got)
		}
	}
	// And while it grows: every reader sees a log as readLog read it at
	// some point, never a row twice.
	c := newLogCache(1 << 30)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 20 {
			more := events(t, 1, today, 1)
			for j := range more {
				more[j].ID = fmt.Sprintf("%s-grow-%d", more[j].ID, i)
			}
			f.Write(t, more...)
		}
		close(stop)
	}()
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				bs, err := c.read(context.Background(), path, []string{"id", "path"}, "", day)
				if err != nil {
					t.Error(err)
					return
				}
				ids := map[string]bool{}
				for _, b := range bs {
					_, ic := b.col("id")
					for i := range b.N {
						id := ic.Dict[ic.Idx[i]]
						if ids[id] {
							t.Errorf("reader %d: id %s twice", g, id)
							return
						}
						ids[id] = true
					}
				}
			}
		}()
	}
	wg.Wait()
	want, _ := readLog(path, []string{"id", "path"}, "", day)
	got, err := c.read(context.Background(), path, []string{"id", "path"}, "", day)
	sameBatch(t, "grown while read", got, err, want, nil)
}

// The cache holds at most max bytes: a log whose entry alone is larger
// is read per query by readLog (no decode of the cache's), the least
// recently used entry goes first.
func TestLogCacheBound(t *testing.T) {
	decodes := countDecodes(t)
	f := archivetest.New(t)
	f.Write(t, events(t, 1, today, 30)...)
	f.Write(t, events(t, 2, today, 30)...)
	a, b := logOf(t, f, 1), logOf(t, f, 2)
	cols := []string{"id", "path", "event_name"}
	day := rng(t, today, today)
	probe := newLogCache(1 << 30)
	if _, err := probe.read(context.Background(), a, cols, "", day); err != nil {
		t.Fatal(err)
	}
	one := probe.size()
	decodes(a)
	if one <= 0 {
		t.Fatalf("an entry of %d bytes", one)
	}

	small := newLogCache(one - 1)
	for i := range 3 {
		want, _ := readLog(a, cols, "", day)
		got, err := small.read(context.Background(), a, cols, "", day)
		sameBatch(t, fmt.Sprint("too large ", i), got, err, want, nil)
	}
	if got := decodes(a); !slices.Equal(got, []int64{0}) {
		t.Errorf("too large: decodes at %v, want one, then readLog's", got)
	}
	if small.size() != 0 || len(small.entries) != 0 {
		t.Errorf("too large: the cache holds %d bytes", small.size())
	}

	// Room for one entry: the second log's pushes the first out.
	lru := newLogCache(one + one/2)
	for _, p := range []string{a, b, a} {
		if _, err := lru.read(context.Background(), p, cols, "", day); err != nil {
			t.Fatal(err)
		}
		if lru.size() > lru.max {
			t.Fatalf("the cache holds %d bytes of %d", lru.size(), lru.max)
		}
	}
	if got := decodes(a); !slices.Equal(got, []int64{0, 0}) {
		t.Errorf("evicted: decodes of the first at %v, want it decoded again", got)
	}
	if len(lru.entries) != 1 || lru.entries[a] == nil {
		t.Errorf("entries %v, want the first log's alone", keys2(lru.entries))
	}
}

func keys2[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// A value that does not read fails a query only when it reads the row,
// as readLog fails: a bad day the queries of its family, a bad value
// those of its family and day that ask for its column.
func TestLogCacheErrorsAsReadLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), today+".log")
	lines := []string{
		`{"id":"a","day":"` + today + `","family":"views","display_width":"10"}`,
		`{"id":"b","day":"` + today + `","family":"product","display_width":"wide"}`,
		`{"id":"c","day":"2026-09-01","family":"views","display_width":"wide"}`,
		`{"id":"d","day":"10.09.2026","family":"measures"}`,
		`{"id":"a","day":"` + today + `","family":"views","display_width":"tall"}`, // a repeat: never read
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	day := rng(t, today, today)
	for _, fam := range []string{"", "views", "product", "measures"} {
		for _, cols := range [][]string{{"id"}, {"id", "display_width"}, {"*"}} {
			for _, r := range []dayRange{day, rng(t, "2026-09-01", today)} {
				readCached(t, path, cols, fam, r)
			}
		}
	}
}

// An engine's queries of a log decode it once while it stays as it was:
// a dashboard's widgets read its rows from the cache.
func TestEngineDecodesALogOnce(t *testing.T) {
	decodes := countDecodes(t)
	f := archivetest.New(t)
	f.Write(t, events(t, 1, today, 20)...)
	path := logOf(t, f, 1)
	e := New(Options{Dir: f.Dir})
	p := Params{Project: 1, From: date(t, today), To: date(t, today)}
	qs := []Query{
		{Kind: "stat", Table: "views", Metrics: count()},
		{Kind: "breakdown", Table: "views", Dimensions: []string{"path"}, Metrics: count()},
		{Kind: "stat", Table: "product", Metrics: count()},
	}
	var first []Result
	for round := range 2 {
		for i, q := range qs {
			res, err := e.Answer(context.Background(), q, p, archive.Rules{})
			if err != nil {
				t.Fatal(err)
			}
			if round == 0 {
				first = append(first, res)
			} else if !slices.EqualFunc(res.Rows, first[i].Rows, slices.Equal) {
				t.Errorf("query %d: %v, then %v", i, first[i].Rows, res.Rows)
			}
		}
	}
	if got := decodes(path); !slices.Equal(got, []int64{0}) {
		t.Errorf("decodes at %v, want one", got)
	}
	late := events(t, 1, today, 1)
	for i := range late {
		late[i].ID += "-late"
	}
	if !slices.ContainsFunc(late, func(ev store.Event) bool { return ev.Family == store.FamilyViews }) {
		t.Fatal("no view among the late events")
	}
	f.Write(t, late...)
	res, err := e.Answer(context.Background(), qs[0], p, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows[0][0] == first[0].Rows[0][0] {
		t.Errorf("the grown log's count %v did not change", res.Rows)
	}
	if got := decodes(path); len(got) != 1 || got[0] == 0 {
		t.Errorf("grown: decodes at %v, want one past the old end", got)
	}
}

// entrySum is a checksum of every vector of x's parts: their values,
// nulls and dictionaries, to the capacity (a write past a batch's rows
// counts too).
func entrySum(x *logEntry) uint64 {
	h := fnv.New64a()
	var b [8]byte
	put := func(v uint64) {
		binary.LittleEndian.PutUint64(b[:], v)
		h.Write(b[:])
	}
	for _, p := range x.parts {
		for _, k := range keys2(p.builders) {
			c := p.builders[k].c
			h.Write([]byte(k))
			for _, v := range c.Idx[:cap(c.Idx)] {
				put(uint64(v))
			}
			for _, v := range c.Ints[:cap(c.Ints)] {
				put(uint64(v))
			}
			for _, v := range c.Times[:cap(c.Times)] {
				put(uint64(v))
			}
			for _, v := range c.Floats[:cap(c.Floats)] {
				put(math.Float64bits(v))
			}
			for _, v := range c.Null[:cap(c.Null)] {
				if v {
					put(1)
				} else {
					put(0)
				}
			}
			d := p.builders[k].db.dict
			for _, s := range d[:cap(d)] {
				h.Write([]byte(s))
				h.Write([]byte{0})
			}
		}
	}
	return h.Sum64()
}

// The batches the cache hands out are read-only: queries that apply
// rules (an event renamed and one blocked, an attribute renamed) and add
// the identity columns, of one family and of all, over its day and a
// range it only partly fills, leave the entry's vectors as they were.
func TestLogCacheBatchesAreNotWritten(t *testing.T) {
	f := archivetest.New(t)
	evs := events(t, 1, today, 12)
	for i := range evs {
		if i%3 == 0 {
			evs[i].UserID, evs[i].UserName = fmt.Sprintf("u%d", i%4), "name"
		}
		if evs[i].Attributes != nil {
			evs[i].Attributes["colour"] = "blue"
		}
	}
	late := events(t, 1, "2026-09-09", 2) // late events: a part with two days
	f.Write(t, evs...)
	f.Write(t, late...)
	path := logOf(t, f, 1)
	// The clock a day on, so a retention counts the log's day.
	e := New(Options{Dir: f.Dir, Now: func() time.Time { return date(t, today).Time().Add(36 * time.Hour) }})
	rules := archive.Rules{
		Events: []store.EventRule{
			evRule(1, store.FamilyProduct, "ev1", store.EventRuleRename, "signup"),
			evRule(1, store.FamilyProduct, "ev2", store.EventRuleBlock, ""),
			evRule(1, store.FamilyViews, "ev0", store.EventRuleRename, "landing"),
		},
		Attributes: []store.AttributeRule{attrRule(1, "colour", store.EventRuleRename, "color")},
	}
	qs := []Query{
		{Kind: "breakdown", Table: "product", Dimensions: []string{"event_name", "attr_color"}, Metrics: count()},
		{Kind: "breakdown", Table: "views", Dimensions: []string{"event_name", "user_status"}, Metrics: count()},
		{Kind: "breakdown", Table: "events", Dimensions: []string{"user_label", "family"}, Metrics: count()},
		{Kind: "trend", Table: "product", Metrics: count()},
		{Kind: "retention", Output: "grid"},
		{Kind: "attributes", Table: "product", Output: "values", Dimensions: []string{"event_name"},
			Metrics: []Metric{{Op: "count"}, {Op: "actors"}}},
	}
	// Fill the cache, then check every query against it.
	if _, err := e.Answer(context.Background(), qs[0], Params{Project: 1, From: date(t, today), To: date(t, today)}, rules); err != nil {
		t.Fatal(err)
	}
	x := e.logs.entries[path]
	if x == nil || len(x.parts) != 3 {
		t.Fatalf("entry %+v, want three families", x)
	}
	before := entrySum(x)
	for _, from := range []string{today, "2026-09-09"} {
		for _, q := range qs {
			res, err := e.Answer(context.Background(), q, Params{Project: 1, From: date(t, from), To: date(t, today)}, rules)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Rows) == 0 && q.Kind != "retention" {
				t.Errorf("%s %s from %s: no rows", q.Kind, q.Table, from)
			}
			if got := entrySum(x); got != before {
				t.Fatalf("%s %s from %s wrote the entry's vectors", q.Kind, q.Table, from)
			}
		}
	}
	// The renamed attribute and event read as the rules say, as they
	// would without the cache.
	res, err := e.Answer(context.Background(), qs[0], Params{Project: 1, From: date(t, today), To: date(t, today)}, rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range res.Rows {
		if row[0] == "ev1" || row[0] == "ev2" {
			t.Errorf("rules not applied: %v", row)
		}
	}
	if e.logs.entries[path] != x || entrySum(x) != before {
		t.Error("the entry changed")
	}
}

// A query waiting for another's decode sizes the log once it holds the
// flight: a waiter that opened the log before it grew takes the grown
// entry as it is, rather than as a file cut to the size it saw.
func TestLogCacheSizesUnderTheFlight(t *testing.T) {
	decodes := countDecodes(t)
	f := archivetest.New(t)
	f.Write(t, events(t, 1, today, 5)...)
	path := logOf(t, f, 1)
	c := newLogCache(1 << 30)
	cols := []string{"id", "path"}
	day := rng(t, today, today)
	if _, err := c.read(context.Background(), path, cols, "", day); err != nil {
		t.Fatal(err)
	}
	first := fileSize(t, path)
	decodes(path)

	waiting := make(chan struct{})
	old := onFlightWait
	onFlightWait = func(string) { close(waiting); onFlightWait = func(string) {} }
	t.Cleanup(func() { onFlightWait = old })
	done, err := c.flight(context.Background(), path) // a query decoding
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []*batch, 1)
	go func() { // a query that sees the log at its first size, then waits
		bs, err := c.read(context.Background(), path, cols, "", day)
		if err != nil {
			t.Error(err)
		}
		got <- bs
	}()
	<-waiting
	more := events(t, 1, today, 3)
	for i := range more {
		more[i].ID += "-more"
	}
	f.Write(t, more...)
	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.update(src, path, cols); err != nil { // the decoding query, past the first size
		t.Fatal(err)
	}
	src.Close()
	done()
	want, err := readLog(path, cols, "", day)
	if err != nil {
		t.Fatal(err)
	}
	sameBatch(t, "waiter", <-got, nil, want, nil)
	if got := decodes(path); !slices.Equal(got, []int64{first}) {
		t.Errorf("decodes at %v, want the growth once (from %d), never the whole log again", got, first)
	}

	// A waiter whose context ends gives up.
	done, _ = c.flight(context.Background(), path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.read(ctx, path, cols, "", day); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want Canceled", err)
	}
	done()
}

// A log cut in place and grown again past what the entry read is decoded
// again whole.
func TestLogCacheCutAndRegrown(t *testing.T) {
	decodes := countDecodes(t)
	f := archivetest.New(t)
	f.Write(t, events(t, 1, today, 5)...)
	path := logOf(t, f, 1)
	c := newLogCache(1 << 30)
	cols := []string{"id", "event_name"}
	day := rng(t, today, today)
	if _, err := c.read(context.Background(), path, cols, "", day); err != nil {
		t.Fatal(err)
	}
	size := fileSize(t, path)
	if err := os.Truncate(path, size-7); err != nil { // mid-line
		t.Fatal(err)
	}
	appendFile(t, path, "\n"+`{"v":1,"id":"after-cut","family":"views","day":"`+today+`","event_name":"x"}`+"\n")
	if fileSize(t, path) <= size {
		t.Fatal("not regrown past the entry")
	}
	decodes(path)
	want, wantErr := readLog(path, cols, "", day)
	got, err := c.read(context.Background(), path, cols, "", day)
	sameBatch(t, "cut", got, err, want, wantErr)
	if got := decodes(path); !slices.Equal(got, []int64{0}) {
		t.Errorf("decodes at %v, want the whole log again", got)
	}
}

// A decode stops as soon as the entry would pass the bound, not after
// the whole log; the log is then read per query, and so is one that grows
// past the bound.
func TestLogCacheDecodeStopsAtTheBound(t *testing.T) {
	f := archivetest.New(t)
	f.Write(t, events(t, 1, today, 200)...)
	path := logOf(t, f, 1)
	size := fileSize(t, path)
	probe := newLogCache(1 << 30)
	day := rng(t, today, today)
	if _, err := probe.read(context.Background(), path, []string{"id"}, "", day); err != nil {
		t.Fatal(err)
	}
	full := probe.size()

	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	x := &logEntry{keys: map[string]bool{}}
	if _, err := x.decode(src, path, size, full/10, nil); !errors.Is(err, errTooBig) {
		t.Fatalf("err = %v, want errTooBig", err)
	}
	if x.size > size/4 {
		t.Errorf("stopped at byte %d of %d with a tenth of the entry's bytes allowed", x.size, size)
	}

	cols := []string{"id", "path"}
	c := newLogCache(full / 10)
	want, _ := readLog(path, cols, "", day)
	got, err := c.read(context.Background(), path, cols, "", day)
	sameBatch(t, "too large", got, err, want, nil)
	if c.size() != 0 || !c.isBigNow(path) {
		t.Errorf("cache holds %d bytes, big %v", c.size(), c.big)
	}

	// Growth past the bound: kept, then dropped and read per query.
	g := archivetest.New(t)
	g.Write(t, events(t, 1, today, 20)...)
	gp := logOf(t, g, 1)
	c = newLogCache(full / 2)
	if _, err := c.read(context.Background(), gp, cols, "", day); err != nil || len(c.entries) != 1 {
		t.Fatalf("not kept: %v, %d entries", err, len(c.entries))
	}
	more := events(t, 1, today, 300)
	for i := range more {
		more[i].ID += "-more"
	}
	g.Write(t, more...)
	want, _ = readLog(gp, cols, "", day)
	got, err = c.read(context.Background(), gp, cols, "", day)
	sameBatch(t, "grown too large", got, err, want, nil)
	if len(c.entries) != 0 || c.size() != 0 || !c.isBigNow(gp) {
		t.Errorf("grown too large: %d entries of %d bytes, big %v", len(c.entries), c.size(), c.big)
	}
}

// isBigNow says whether path is marked too large to keep.
func (c *logCache) isBigNow(path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.big[path]
	return ok
}

// Storing an entry drops those of logs no longer there (dumped, rotated).
func TestLogCachePrunesGoneLogs(t *testing.T) {
	f := archivetest.New(t)
	f.Write(t, events(t, 1, today, 3)...)
	f.Write(t, events(t, 2, today, 3)...)
	a, b := logOf(t, f, 1), logOf(t, f, 2)
	c := newLogCache(1 << 30)
	day := rng(t, today, today)
	if _, err := c.read(context.Background(), a, []string{"id"}, "", day); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(a, strings.TrimSuffix(a, ".log")+".1.closed"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.read(context.Background(), b, []string{"id"}, "", day); err != nil {
		t.Fatal(err)
	}
	if got := keys2(c.entries); !slices.Equal(got, []string{b}) || c.size() != c.entries[b].bytes {
		t.Errorf("entries %v (%d bytes), want only %s", got, c.size(), b)
	}
}

// A decode counts against the cache as it grows, with the other decodes
// under way: one that does not fit beside them is read per query, without
// marking the log too large, and is kept once there is room.
func TestLogCacheDecodesShareTheBound(t *testing.T) {
	f := archivetest.New(t)
	f.Write(t, events(t, 1, today, 30)...)
	path := logOf(t, f, 1)
	cols := []string{"id", "path"}
	day := rng(t, today, today)
	probe := newLogCache(1 << 30)
	if _, err := probe.read(context.Background(), path, cols, "", day); err != nil {
		t.Fatal(err)
	}
	one := probe.size()

	c := newLogCache(2 * one)
	other := &reservation{c: c, path: "elsewhere"} // another decode under way
	if !other.hold(one + one/2) {
		t.Fatal("the other decode got no room in an empty cache")
	}
	if (&reservation{c: c, path: "third"}).hold(one) {
		t.Error("a third decode got room past the bound")
	}
	want, _ := readLog(path, cols, "", day)
	got, err := c.read(context.Background(), path, cols, "", day)
	sameBatch(t, "no room", got, err, want, nil)
	if c.size() != 0 || len(c.entries) != 0 || c.isBigNow(path) {
		t.Errorf("no room: %d entries of %d bytes, big %v", len(c.entries), c.size(), c.big)
	}
	if c.inflight != one+one/2 {
		t.Errorf("in flight %d, want the other decode's %d", c.inflight, one+one/2)
	}
	other.release()
	if c.inflight != 0 {
		t.Errorf("in flight %d once released", c.inflight)
	}
	got, err = c.read(context.Background(), path, cols, "", day)
	sameBatch(t, "room", got, err, want, nil)
	if len(c.entries) != 1 || c.size() > c.max {
		t.Errorf("room: %d entries of %d bytes, of %d", len(c.entries), c.size(), c.max)
	}
}

// fakeClock is a clock a test moves.
type fakeClock struct{ ns atomic.Int64 }

func (f *fakeClock) now() time.Time          { return time.Unix(0, f.ns.Load()) }
func (f *fakeClock) advance(d time.Duration) { f.ns.Add(int64(d)) }

// An entry unused for idleAfter is dropped by the sweep, and so is one
// whose log rotated, without another store; the sweep runs by itself
// while there are entries, and stops once there are none.
func TestLogCacheDropsIdleEntries(t *testing.T) {
	f := archivetest.New(t)
	f.Write(t, events(t, 1, today, 3)...)
	f.Write(t, events(t, 2, today, 3)...)
	a, b := logOf(t, f, 1), logOf(t, f, 2)
	cols := []string{"id"}
	day := rng(t, today, today)
	clock := &fakeClock{}
	clock.ns.Store(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC).UnixNano())
	c := newLogCache(1 << 30)
	c.now = clock.now
	c.sweepEvery = time.Hour // swept by hand below
	read := func(p string) {
		t.Helper()
		if _, err := c.read(context.Background(), p, cols, "", day); err != nil {
			t.Fatal(err)
		}
	}
	held := func() []string {
		c.mu.Lock()
		defer c.mu.Unlock()
		return keys2(c.entries)
	}
	read(a)
	read(b)
	clock.advance(4 * time.Minute)
	read(a) // used again
	c.sweep()
	if got := held(); len(got) != 2 {
		t.Fatalf("after 4 minutes: %v, want both", got)
	}
	clock.advance(2 * time.Minute)
	c.sweep()
	if got := held(); !slices.Equal(got, []string{a}) {
		t.Errorf("after 6 minutes: %v, want the one used at 4 alone", got)
	}
	if err := os.Rename(a, strings.TrimSuffix(a, ".log")+".1.closed"); err != nil {
		t.Fatal(err)
	}
	c.sweep()
	if got := held(); len(got) != 0 || c.size() != 0 {
		t.Errorf("rotated: %v of %d bytes, want none", got, c.size())
	}

	// By itself: every entry idle at once, the sweep drops them and stops.
	c = newLogCache(1 << 30)
	c.idleAfter, c.sweepEvery = 0, time.Millisecond
	read = func(p string) {
		t.Helper()
		if _, err := c.read(context.Background(), p, cols, "", day); err != nil {
			t.Fatal(err)
		}
	}
	read(b)
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		n, timer := len(c.entries), c.sweeping
		c.mu.Unlock()
		if n == 0 && timer == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d entries, sweep %v after 10 s", n, timer != nil)
		}
		time.Sleep(time.Millisecond)
	}
}
