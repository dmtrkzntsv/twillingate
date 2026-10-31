package native

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/parquet-go/parquet-go"
)

// The seeded days: two in August, one in September; seed runs the daily
// pass as of today, so August becomes a month file and September 5 a day
// file.
var seedDays = []string{"2026-08-10", "2026-08-20", "2026-09-05"}

const today = "2026-09-10"

func date(t testing.TB, s string) civil.Date {
	t.Helper()
	d, err := civil.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func rng(t testing.TB, from, to string) dayRange {
	return dayRange{From: date(t, from), To: date(t, to)}
}

// events is n events of each family for project on day, with fields that
// are sometimes empty (so null in the files), attributes on product and
// measures only (attr_color in September only) and values on measures.
func events(t testing.TB, project int64, day string, n int) []store.Event {
	t.Helper()
	base := date(t, day).Time()
	families := []store.Family{store.FamilyViews, store.FamilyProduct, store.FamilyMeasures}
	var out []store.Event
	for f, fam := range families {
		for i := range n {
			k := f*n + i
			e := store.Event{
				ID:        fmt.Sprintf("p%d-%s-%s-%04d", project, day, fam, i),
				ProjectID: project, Family: fam, EventName: fmt.Sprintf("ev%d", k%4),
				TS:   base.Add(time.Duration(k)*time.Second + time.Duration(k%1000)*time.Microsecond),
				Kind: "anonymous", ActorID: fmt.Sprintf("a%d", k%7), ActorKind: "install",
				Host: "example.com", Path: fmt.Sprintf("/p%d", k%5), Country: []string{"", "DE", "US"}[k%3],
				DisplayWidth: []int{0, 1280, 390}[k%3], DisplayHeight: 800 + k%2,
				Consent: []store.Consent{store.ConsentUnknown, store.ConsentGiven, store.ConsentNone}[k%3],
			}
			if k%2 == 0 {
				e.ReceivedAt = e.TS.Add(1500 * time.Microsecond)
			}
			if k%5 == 0 {
				e.UserID = fmt.Sprintf("u%d", k%11)
			}
			if fam != store.FamilyViews {
				e.Attributes = map[string]string{"plan": []string{"free", "pro", "team"}[k%3]}
				if strings.HasPrefix(day, "2026-09") && k%2 == 1 {
					e.Attributes["color"] = "red"
				}
			}
			if fam == store.FamilyMeasures {
				v := float64(k)*0.25 + 0.1
				e.Value, e.Measure, e.SampleRate = &v, "lcp", []float64{0, 0.5}[k%2]
			}
			out = append(out, e)
		}
	}
	return out
}

// seed writes n events of each family per seeded day for project 1 and a
// few on one day for project 2, then runs the daily pass as of today.
func seed(t testing.TB, n int) *archivetest.Files {
	t.Helper()
	f := archivetest.New(t)
	for _, d := range seedDays {
		f.Write(t, events(t, 1, d, n)...)
	}
	f.Write(t, events(t, 2, "2026-08-10", 3)...)
	f.Maintain(t, today)
	return f
}

func names(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Base(p)
	}
	return out
}

// projectFiles returns the Parquet files and the logs of project p that
// may hold rows in r, from archive.ListFiles(dir)[p] (which leaves out what
// a reader would count twice) and the files' day or month names, each in
// name order. A project with no files under dir, or a missing dir, has none.
func projectFiles(dir string, p int64, r dayRange) (parquet, logs []string, err error) {
	all, err := archive.ListFiles(dir)
	if err != nil {
		return nil, nil, err
	}
	parquet, logs = filesIn(all[p], r)
	return parquet, logs, nil
}

func TestProjectFilesByRange(t *testing.T) {
	f := seed(t, 4)
	f.Write(t, events(t, 1, today, 2)...) // today's log
	cases := []struct {
		r        dayRange
		pq, logs []string
	}{
		{rng(t, "2026-08-15", "2026-08-25"), []string{"2026-08.parquet"}, nil},
		{rng(t, "2026-08-31", "2026-09-05"), []string{"2026-08.parquet", "2026-09-05.parquet"}, nil},
		{rng(t, "2026-09-01", "2026-09-30"), []string{"2026-09-05.parquet"}, []string{today + ".log"}},
		{rng(t, "2026-09-06", "2026-09-09"), nil, nil},
		{rng(t, "2026-07-01", "2026-07-31"), nil, nil},
	}
	for _, c := range cases {
		pq, logs, err := projectFiles(f.Dir, 1, c.r)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(names(pq), c.pq) || !slices.Equal(names(logs), c.logs) {
			t.Errorf("%v: got %v %v, want %v %v", c.r, names(pq), names(logs), c.pq, c.logs)
		}
	}
	pq, logs, err := projectFiles(f.Dir, 9, rng(t, "2026-01-01", "2026-12-31"))
	if err != nil || pq != nil || logs != nil {
		t.Errorf("unknown project: %v %v %v", pq, logs, err)
	}
	pq, logs, err = projectFiles(filepath.Join(f.Dir, "missing"), 1, rng(t, "2026-01-01", "2026-12-31"))
	if err != nil || pq != nil || logs != nil {
		t.Errorf("missing dir: %v %v %v", pq, logs, err)
	}
}

func TestFileOverlaps(t *testing.T) {
	r := rng(t, "2026-08-15", "2026-08-25")
	for name, want := range map[string]bool{
		"2026-08.parquet": true, "2026-07.parquet": false, "2026-08-15.parquet": true,
		"2026-08-25.2.parquet": true, "2026-08-26.parquet": false, "2026-08-14.log": false,
		"2026-08-20.3.closed": true, "2026-08-20.parquet.dumping": true, "2026-08-26.1.parquet.dumping": false,
		"odd": true, // a name it cannot read is kept: the rows are filtered anyway
	} {
		if got := overlaps(name, r); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}

// nullCell stands for NULL in formatted rows.
const nullCell = "\x00NULL"

// formatBatch appends b's rows as cells of cols, formatted as cell does.
func formatBatch(t *testing.T, b *batch, cols []string, out [][]string) [][]string {
	t.Helper()
	for i := range b.N {
		row := make([]string, len(cols))
		for j, name := range cols {
			c := b.Cols[name]
			switch {
			case c == nil:
				row[j] = nullCell
			case c.Idx != nil:
				row[j] = nullCell
				if c.Idx[i] >= 0 {
					row[j] = c.Dict[c.Idx[i]]
				}
			case c.Null != nil && c.Null[i]:
				row[j] = nullCell
			case c.Times != nil:
				row[j] = time.UnixMicro(c.Times[i]).UTC().Format(microFormat)
			case c.Floats != nil:
				row[j] = strconv.FormatFloat(c.Floats[i], 'g', -1, 64)
			case name == "day":
				row[j] = civil.DateOf(time.Unix(c.Ints[i]*86400, 0)).String()
			case c.Ints != nil:
				row[j] = strconv.FormatInt(c.Ints[i], 10)
			default:
				t.Fatalf("column %s: no values", name)
			}
		}
		out = append(out, row)
	}
	return out
}

const microFormat = "2006-01-02T15:04:05.000000Z"

// readAll reads every group with one reader per worker.
func readAll(t *testing.T, groups []group, workers int, cols []string, family string, r dayRange) []*batch {
	t.Helper()
	readers := make([]groupReader, workers)
	var mu sync.Mutex
	var out []*batch
	err := scanParallel(context.Background(), groups, workers, func(w int, g group) error {
		b, err := readers[w].read(g, cols, family, r)
		if err != nil {
			return err
		}
		mu.Lock()
		out = append(out, b)
		mu.Unlock()
		return nil
	})
	for i := range readers {
		if cerr := readers[i].close(); cerr != nil {
			t.Fatal(cerr)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sortByFirst(rows [][]string) {
	slices.SortFunc(rows, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
}

// matchGolden reads every row of project 1 in its Parquet files (two,
// and no log) in every column of events, and compares them with the
// golden. It returns the files.
func matchGolden(t *testing.T, f *archivetest.Files) []string {
	t.Helper()
	const q = "SELECT * FROM events WHERE project_id = 1 ORDER BY id"
	cols := eventsColumns(t, f.Dir, 1, archive.Rules{})
	for _, want := range []string{"ts", "received_at", "day", "value", "sample_rate", "display_width", "consent", "attr_plan", "attr_color"} {
		if !slices.Contains(cols, want) {
			t.Fatalf("events lacks %s: %v", want, cols)
		}
	}

	full := rng(t, "2026-08-01", "2026-09-30")
	pq, logs, err := projectFiles(f.Dir, 1, full)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 0 || len(pq) != 2 {
		t.Fatalf("files %v, logs %v", pq, logs)
	}
	groups, err := parquetGroups(pq, "", full)
	if err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for _, b := range readAll(t, groups, 2, cols, "", full) {
		got = formatBatch(t, b, cols, got)
	}
	want := wantRows(t, q, archive.Rules{}, cols, got)
	if len(got) != len(want) {
		t.Fatalf("%d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Fatalf("row %d:\n got %q\nwant %q\ncols %v", i, got[i], want[i], cols)
		}
	}
	return pq
}

func TestParquetRowsMatchGolden(t *testing.T) {
	f := seed(t, 40)
	pq := matchGolden(t, f)

	// One family and one day, the attributes by pattern: the rows SQL
	// filters the same way, and every attr_ column a file holds.
	day := rng(t, "2026-09-05", "2026-09-05")
	groups, err := parquetGroups(pq, "product", day)
	if err != nil {
		t.Fatal(err)
	}
	sub := []string{"id", "attr_*"}
	var gotSub [][]string
	for _, b := range readAll(t, groups, 1, sub, "product", day) {
		if b.Cols["attr_color"] == nil || b.Cols["attr_plan"] == nil || b.Cols["family"] != nil {
			t.Fatalf("attr_* read %v", keys(b.Cols))
		}
		gotSub = formatBatch(t, b, []string{"id", "attr_color", "attr_plan"}, gotSub)
	}
	const q = "SELECT id, attr_color, attr_plan FROM events WHERE project_id = 1 " +
		"AND family = 'product' AND day = DATE '2026-09-05' ORDER BY id"
	want := wantRows(t, q, archive.Rules{}, []string{"id", "attr_color", "attr_plan"}, gotSub)
	if len(gotSub) != len(want) || len(gotSub) == 0 {
		t.Fatalf("%d rows, want %d", len(gotSub), len(want))
	}
	for i, row := range want {
		if !slices.Equal(row, gotSub[i]) {
			t.Fatalf("row %d: got %q, want %q", i, gotSub[i], row)
		}
	}
}

func keys(m map[string]*column) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// rewrite rewrites a Parquet file the pass wrote (in (family, ts) order)
// with row groups of groupRows rows, its schema and rows as they are.
func rewrite(t *testing.T, path string, groupRows int) {
	t.Helper()
	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pf, err := parquet.OpenFile(in, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	tmp := path + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	w := parquet.NewWriter(out, pf.Schema(), parquet.MaxRowsPerRowGroup(int64(groupRows)))
	for _, rg := range pf.RowGroups() {
		rows := rg.Rows()
		_, err := parquet.CopyRows(w, rows)
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func TestParquetPrunesByDay(t *testing.T) {
	f := seed(t, 1500) // 9000 rows in August
	month := filepath.Join(f.Dir, "1", "2026-08.parquet")
	rewrite(t, month, 2048) // small row groups, as the old engine's smallest
	matchGolden(t, f)       // every column over many groups and pages
	all, err := parquetGroups([]string{month}, "", rng(t, "2026-08-01", "2026-08-31"))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 4 {
		t.Fatalf("%d groups, want many", len(all))
	}
	for _, g := range all {
		if !g.all {
			t.Errorf("group %d: whole month range, not all", g.index)
		}
	}
	day := rng(t, "2026-08-10", "2026-08-10") // the last group holds only August 20
	kept, err := parquetGroups([]string{month}, "", day)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) == 0 || len(kept) >= len(all) {
		t.Fatalf("one day kept %d of %d groups", len(kept), len(all))
	}
	n := 0
	for _, b := range readAll(t, kept, 3, []string{"id", "day"}, "", day) {
		n += b.N
		for i := range b.N {
			if got := civil.DateOf(time.Unix(b.Cols["day"].Ints[i]*86400, 0)).String(); got != "2026-08-10" {
				t.Fatalf("row of %s", got)
			}
		}
	}
	if n != 4500 {
		t.Errorf("%d rows, want 4500", n)
	}
	fam, err := parquetGroups([]string{month}, "views", day)
	if err != nil {
		t.Fatal(err)
	}
	if len(fam) == 0 || len(fam) >= len(kept) {
		t.Errorf("views on one day kept %d groups of %d", len(fam), len(kept))
	}
	none, err := parquetGroups([]string{month}, "zzz", day)
	if err != nil || len(none) != 0 {
		t.Errorf("unknown family: %v %v", none, err)
	}
}

// TestParquetGroupSpanningDays: a month file's group holds every day of
// its family while it is small (the pass cuts at a day only past
// dayCutRows rows), so a range that starts or ends inside the group keeps
// it, does not take it whole, and reads only its own days' rows.
func TestParquetGroupSpanningDays(t *testing.T) {
	f := seed(t, 100) // August: 100 rows a family on the 10th and the 20th
	month := filepath.Join(f.Dir, "1", "2026-08.parquet")
	whole, err := parquetGroups([]string{month}, "views", rng(t, "2026-08-01", "2026-08-31"))
	if err != nil {
		t.Fatal(err)
	}
	if len(whole) != 1 || !whole[0].all || whole[0].rows != 200 {
		t.Fatalf("views over the month: %+v, want one group of both days, taken whole", whole)
	}
	for _, c := range []struct{ from, to, day string }{
		{"2026-08-15", "2026-08-25", "2026-08-20"}, // starts inside the group
		{"2026-08-01", "2026-08-15", "2026-08-10"}, // ends inside it
		{"2026-08-20", "2026-08-20", "2026-08-20"}, // its last day alone
	} {
		r := rng(t, c.from, c.to)
		kept, err := parquetGroups([]string{month}, "views", r)
		if err != nil {
			t.Fatal(err)
		}
		if len(kept) != 1 || kept[0].all {
			t.Fatalf("%s to %s: %+v, want the group kept, not whole", c.from, c.to, kept)
		}
		n := 0
		for _, b := range readAll(t, kept, 2, []string{"id", "day", "family"}, "views", r) {
			n += b.N
			for i := range b.N {
				if got := civil.DateOf(time.Unix(b.Cols["day"].Ints[i]*86400, 0)).String(); got != c.day {
					t.Fatalf("%s to %s: a row of %s", c.from, c.to, got)
				}
			}
		}
		if n != 100 {
			t.Errorf("%s to %s: %d rows, want 100", c.from, c.to, n)
		}
	}
	matchGolden(t, f)
}

func TestParquetMissingColumns(t *testing.T) {
	f := seed(t, 2)
	day := filepath.Join(f.Dir, "1", "2026-09-05.parquet")
	r := rng(t, "2026-09-05", "2026-09-05")
	groups, err := parquetGroups([]string{day}, "views", r)
	if err != nil || len(groups) == 0 {
		t.Fatal(groups, err)
	}
	var g groupReader
	defer g.close()
	b, err := g.read(groups[0], []string{"id", "attr_nope", "value", "ts", "day", "display_width", "attr_*"}, "views", r)
	if err != nil {
		t.Fatal(err)
	}
	if b.N != 2 {
		t.Fatalf("%d rows", b.N)
	}
	if c := b.Cols["attr_nope"]; c == nil || c.Idx[0] != -1 {
		t.Errorf("attr_nope: %+v", c)
	}
	for _, name := range []string{"value", "display_width"} { // null on views' first row
		if c := b.Cols[name]; c == nil || len(c.Null) != 2 || !c.Null[0] {
			t.Errorf("%s: %+v", name, c)
		}
	}
	for name, typ := range map[string]string{"value": "f", "ts": "t", "day": "i"} {
		c := b.Cols[name]
		if (typ == "f") != (c.Floats != nil) || (typ == "t") != (c.Times != nil) || (typ == "i") != (c.Ints != nil) {
			t.Errorf("%s: wrong kind %+v", name, c)
		}
	}
	// A column no file has and the schema does not know reads as strings.
	n := nullColumn("zz", 2)
	if n.Idx == nil {
		t.Errorf("nullColumn(zz): %+v", n)
	}
	for _, name := range []string{"ts", "value", "consent"} {
		n := nullColumn(name, 1)
		if !n.Null[0] {
			t.Errorf("nullColumn(%s): %+v", name, n)
		}
	}
	if _, err := g.read(group{file: filepath.Join(f.Dir, "nope.parquet")}, []string{"id"}, "", r); err == nil {
		t.Error("read of a missing file: no error")
	}
	if _, err := parquetGroups([]string{filepath.Join(f.Dir, "nope.parquet")}, "", r); err == nil {
		t.Error("groups of a missing file: no error")
	}
	bad := filepath.Join(t.TempDir(), "bad.parquet")
	if err := os.WriteFile(bad, []byte("not parquet"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := parquetGroups([]string{bad}, "", r); err == nil {
		t.Error("groups of a broken file: no error")
	}
}

func TestScanParallel(t *testing.T) {
	groups := make([]group, 50)
	for i := range groups {
		groups[i] = group{file: "f", index: i, rows: 1}
	}
	var seen atomic.Int64
	if err := scanParallel(context.Background(), groups, 4, func(w int, g group) error {
		if w < 0 || w >= 4 {
			t.Errorf("worker %d", w)
		}
		seen.Add(int64(g.index) + 1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen.Load() != 50*51/2 {
		t.Errorf("sum %d", seen.Load())
	}
	boom := errors.New("boom")
	err := scanParallel(context.Background(), groups, 3, func(w int, g group) error {
		if g.index == 7 {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Errorf("err %v, want boom", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int64
	err = scanParallel(ctx, groups, 2, func(w int, g group) error {
		if calls.Add(1) == 3 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls.Load() >= 50 {
		t.Errorf("cancelled: err %v after %d calls", err, calls.Load())
	}
	if err := scanParallel(context.Background(), nil, 4, func(int, group) error { return boom }); err != nil {
		t.Errorf("no groups: %v", err)
	}
}

func TestDecideCountsNulls(t *testing.T) {
	s := groupStats{famOK: true, famMin: "views", famMax: "views", dayOK: true, dayMin: 10, dayMax: 12}
	for _, c := range []struct {
		famNulls, dayNulls int64
		family             string
		all                bool
	}{
		{0, 0, "views", true}, {0, 0, "", true},
		{3, 0, "views", false}, // a null family is no views row
		{3, 0, "", true},       // nor filtered when any family will do
		{0, 2, "", false},      // a null day is in no range
	} {
		s.famNulls, s.dayNulls = c.famNulls, c.dayNulls
		if keep, all := s.decide(c.family, 10, 12); !keep || all != c.all {
			t.Errorf("%+v: keep %v all %v", c, keep, all)
		}
	}
}

func TestDictDedupes(t *testing.T) {
	var db dictBuilder
	vals, offs := byteArray("a", "b")
	remap, same := db.addDict(vals, offs)
	clear(vals) // the page's buffer, reused: the dictionary keeps its own copy
	if !same || remap != nil || !slices.Equal(db.dict, []string{"a", "b"}) || db.ids != nil {
		t.Errorf("first: %v %v %v, lookup %v", remap, same, db.dict, db.ids)
	}
	if id := db.id([]byte("c")); id != 2 { // a plain page's value
		t.Errorf("c: %d", id)
	}
	remap, same = db.addDict(byteArray("c", "a", "d", "d")) // a second dictionary page
	if same || !slices.Equal(remap, []int32{2, 0, 3, 3}) || !slices.Equal(db.dict, []string{"a", "b", "c", "d"}) {
		t.Errorf("second: %v %v %v", remap, same, db.dict)
	}
}

// byteArray is vals as a page's BYTE_ARRAY values and offsets.
func byteArray(vals ...string) ([]byte, []uint32) {
	var data []byte
	offs := []uint32{0}
	for _, v := range vals {
		data = append(data, v...)
		offs = append(offs, uint32(len(data)))
	}
	return data, offs
}

// parquetGroupsOf leaves out a row group none of whose families is asked
// for (the measures a retention does not count), and keeps every row of
// the families asked for.
func TestParquetGroupsOfFamilies(t *testing.T) {
	f := seed(t, 1500)
	month := filepath.Join(f.Dir, "1", "2026-08.parquet")
	rewrite(t, month, 2048) // by family: some groups hold measures alone
	r := rng(t, "2026-08-01", "2026-08-31")
	all, err := parquetGroups([]string{month}, "", r)
	if err != nil {
		t.Fatal(err)
	}
	kept, err := parquetGroupsOf([]string{month}, retentionFamilies, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) == 0 || len(kept) >= len(all) {
		t.Fatalf("kept %d of %d groups, want the measures-only ones left out", len(kept), len(all))
	}
	count := func(groups []group) map[string]int {
		n := map[string]int{}
		for _, b := range readAll(t, groups, 3, []string{"family"}, "", r) {
			_, fam := b.col("family")
			for i := range b.N {
				n[fam.Dict[fam.Idx[i]]]++
			}
		}
		return n
	}
	want, got := count(all), count(kept)
	for _, fam := range retentionFamilies {
		if got[fam] != want[fam] || want[fam] == 0 {
			t.Errorf("%s: %d rows kept of %d", fam, got[fam], want[fam])
		}
	}
	if got["measures"] >= want["measures"] {
		t.Errorf("measures: %d rows kept of %d, want fewer", got["measures"], want["measures"])
	}
}
