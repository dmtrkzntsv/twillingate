package native

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// readCached is readLog(path, cols, family, r), checked against the log
// cache's read of it: from a cold cache, from the same cache warm, and
// from a cache a query of other columns filled first. Each must give readLog's batch (its columns,
// their kinds and values) or its error.
func readCached(t *testing.T, path string, cols []string, family string, r Range) (*Batch, error) {
	t.Helper()
	want, wantErr := readLog(path, cols, family, r)
	cold := newLogCache(1 << 30)
	other := newLogCache(1 << 30)
	_, _ = other.read(path, []string{"id", "event_name"}, "views", r) // only warms it
	for _, c := range []struct {
		name  string
		cache *logCache
	}{{"cold", cold}, {"warm", cold}, {"filled by other columns", other}} {
		got, err := c.cache.read(path, cols, family, r)
		sameBatch(t, c.name, got, err, want, wantErr)
	}
	return want, wantErr
}

// sameBatch fails unless got (or gotErr) is want (or wantErr).
func sameBatch(t *testing.T, name string, got *Batch, gotErr error, want *Batch, wantErr error) {
	t.Helper()
	if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
		t.Fatalf("%s: err %v, want %v", name, gotErr, wantErr)
	}
	if wantErr != nil {
		return
	}
	if got.N != want.N || !slices.Equal(keys(got.Cols), keys(want.Cols)) {
		t.Fatalf("%s: %d rows %v, want %d rows %v", name, got.N, keys(got.Cols), want.N, keys(want.Cols))
	}
	names := keys(want.Cols)
	for _, n := range names {
		if g, w := colKindOf(got.Cols[n], got.N), colKindOf(want.Cols[n], want.N); g != w {
			t.Fatalf("%s: column %s is %s, want %s", name, n, g, w)
		}
	}
	g, w := formatBatch(t, got, names, nil), formatBatch(t, want, names, nil)
	if !slices.EqualFunc(g, w, slices.Equal) {
		t.Fatalf("%s:\n got %q\nwant %q\ncols %v", name, g, w, names)
	}
}

// colKindOf is c's type, "null" when no row of n has a value (an all-null
// column's type is any).
func colKindOf(c *Column, n int) string {
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
	cols := []string{"id", "family", "event_name", "path", "ts", "display_width", AttrCols}
	day := rng(t, today, today)
	check := func(step string, wantDecodes []int64) {
		t.Helper()
		for _, fam := range []string{"", "views", "product"} {
			want, wantErr := readLog(path, cols, fam, day)
			got, err := c.read(path, cols, fam, day)
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
	if _, err := c.read(path, cols, "", day); err != nil {
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
	got, err := c.read(path, cols, "", day)
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
		{[]string{AttrCols}, nil},
		{[]string{"*"}, nil},
		{[]string{"id", "path", AttrCols}, nil},
	} {
		want, wantErr := readLog(path, s.cols, "views", day)
		got, err := c.read(path, s.cols, "views", day)
		sameBatch(t, fmt.Sprint(s.cols), got, err, want, wantErr)
		if got := decodes(path); !slices.Equal(got, s.decodes) {
			t.Errorf("%v: decodes at %v, want %v", s.cols, got, s.decodes)
		}
	}
	size := fileSize(t, path)
	appendFile(t, path, `{"v":1,"id":"new-key","family":"views","day":"`+today+`","attr_late":"l"}`+"\n")
	for _, cols := range [][]string{{"id", "attr_late"}, {AttrCols}, {"*"}} {
		want, wantErr := readLog(path, cols, "views", day)
		got, err := c.read(path, cols, "views", day)
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
	sets := [][]string{{"id", "path"}, {"id", "event_name", AttrCols}, {"ts", "day", "family"}}
	wants := make([]*Batch, len(sets))
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
				got, err := c.read(path, sets[i], "", day)
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
				b, err := c.read(path, []string{"id", "path"}, "", day)
				if err != nil {
					t.Error(err)
					return
				}
				ids := map[string]bool{}
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
		}()
	}
	wg.Wait()
	want, _ := readLog(path, []string{"id", "path"}, "", day)
	got, err := c.read(path, []string{"id", "path"}, "", day)
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
	if _, err := probe.read(a, cols, "", day); err != nil {
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
		got, err := small.read(a, cols, "", day)
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
		if _, err := lru.read(p, cols, "", day); err != nil {
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
			for _, r := range []Range{day, rng(t, "2026-09-01", today)} {
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
