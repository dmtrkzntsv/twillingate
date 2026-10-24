package archive

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/encoding/thrift"
)

// mergeCase is one month's inputs, oldest first, that both merges read.
type mergeCase struct {
	name  string
	build func(t *testing.T, dir string) []string
}

// goDay dumps log as dir/name with the Go dump.
func (f fixture) goDay(t *testing.T, dir, name, log string) string {
	t.Helper()
	src, out := filepath.Join(dir, name+".dumping"), filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte(log), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := cutTornLine(src); err != nil { // as claim does
		t.Fatal(err)
	}
	if err := dumpLog(context.Background(), src, out); err != nil {
		t.Fatal(err)
	}
	os.Remove(src)
	return out
}

// oldFile copies testdata/old-files/<set>/<name>, a file the old query
// engine wrote (a release before the Go dump and merge wrote it from the
// case's logs, frozen when step 6b removed that engine), as dir/name.
// Installs still hold such files, so the Go merge and derivers read them.
func oldFile(t *testing.T, dir, set, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "old-files", set, name))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, b, 0o640); err != nil {
		t.Fatal(err)
	}
	return out
}

// sortedLog is n product events (and a view every tenth), ids prefix-i,
// from hour 0 of day on, each with an attribute that names copy.
func sortedLog(t *testing.T, day, prefix string, from, n int, copy string) string {
	t.Helper()
	base, err := time.Parse(time.RFC3339, day+"T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := from; i < from+n; i++ {
		e := withAttrs(evOf(1, fmt.Sprintf("%s-%06d", prefix, i), day+"T00:00:00Z"), fmt.Sprintf("u%d", i%97),
			map[string]string{"copy": copy, "n": strconv.Itoa(i % 13)})
		e.TS = base.Add(time.Duration(i) * time.Second)
		e.ReceivedAt = e.TS
		if i%10 == 0 {
			e.Family, e.EventName, e.Path = store.FamilyViews, "$page_view", fmt.Sprintf("/p/%d", i%31)
		}
		b.WriteString(logOf(t, e))
	}
	return b.String()
}

func mergeCases(t *testing.T, f fixture) []mergeCase {
	dumps := dumpCases(t)
	everyDump := func(day func(t *testing.T, dir, name, log string) string) func(t *testing.T, dir string) []string {
		return func(t *testing.T, dir string) []string {
			var in []string
			for i, c := range dumps {
				in = append(in, day(t, dir, fmt.Sprintf("2026-10-%02d.parquet", i+1), c.log))
			}
			return in
		}
	}
	old := func(set string) func(t *testing.T, dir, name, log string) string {
		return func(t *testing.T, dir, name, _ string) string { return oldFile(t, dir, set, name) }
	}
	const uuid = "ABCDEF01-2345-6789-ABCD-EF0123456789"
	return []mergeCase{
		{"keys equal but for case across days", func(t *testing.T, dir string) []string {
			return []string{
				f.goDay(t, dir, "2026-09-01.parquet", logOf(t, withAttrs(evOf(1, "a", "2026-09-01T10:00:00Z"), "", map[string]string{"Plan": "A"}))),
				f.goDay(t, dir, "2026-09-02.parquet", logOf(t, withAttrs(evOf(1, "b", "2026-09-02T10:00:00Z"), "", map[string]string{"plan": "b", "PLAN_1": "c"}))),
				f.goDay(t, dir, "2026-09-03.parquet", logOf(t, withAttrs(evOf(1, "c", "2026-09-03T10:00:00Z"), "", map[string]string{"plan_1": "d", "Plan": "e", "plan": "f"}))),
			}
		}},
		{"a leftover month file", func(t *testing.T, dir string) []string {
			d1 := f.goDay(t, dir, "2026-09-01.parquet", logOf(t, withAttrs(evOf(1, "a", "2026-09-01T10:00:00Z"), "", map[string]string{"copy": "day"}),
				withAttrs(evOf(1, "b", "2026-09-01T11:00:00Z"), "", map[string]string{"copy": "day"})))
			d2 := f.goDay(t, dir, "2026-09-02.parquet", logOf(t, withAttrs(evOf(1, "c", "2026-09-02T10:00:00Z"), "", map[string]string{"copy": "day"}),
				withAttrs(evOf(1, "d", "2026-09-02T10:00:00Z"), "", map[string]string{"copy": "day"})))
			// d1 and d2 with attr_copy 'month', but for d.
			month := oldFile(t, dir, "merge-leftover-month", "2026-09.parquet")
			return []string{month, d1, d2}
		}},
		{"segments", func(t *testing.T, dir string) []string {
			day := f.goDay(t, dir, "2026-09-30.parquet", logOf(t, evOf(1, "a", "2026-09-30T10:00:00Z"), evOf(1, "b", "2026-09-30T12:00:00Z"),
				dumpView("v", "2026-09-30T11:00:00Z"))+`{"v":1,"family":"product","ts":"2026-09-30T13:00:00Z","attr_x":"null id, day"}`+"\n")
			seg1 := f.goDay(t, dir, "2026-09-30.1.parquet", logOf(t, evOf(1, "late", "2026-09-30T11:00:00Z"),
				withAttrs(evOf(1, "a", "2026-09-30T10:00:00Z"), "", map[string]string{"copy": "2"}), evOf(1, "b2", "2026-09-30T12:00:00Z"))+
				`{"v":1,"family":"product","ts":"2026-09-30T09:00:00Z","attr_x":"null id, segment"}`+"\n")
			seg2 := f.goDay(t, dir, "2026-09-30.2.parquet", logOf(t, evOf(1, "z", "2026-09-30T09:00:00Z"), dumpView("v", "2026-09-30T11:00:00Z")))
			return []string{day, seg1, seg2}
		}},
		{"duplicate ids within and across inputs", func(t *testing.T, dir string) []string {
			// a and b with attr_copy 1, then again with 2 an hour later.
			twice := oldFile(t, dir, "merge-duplicate-ids", "2026-09-01.parquet")
			d2 := f.goDay(t, dir, "2026-09-02.parquet", logOf(t, withAttrs(evOf(1, "a", "2026-09-02T10:00:00Z"), "", map[string]string{"copy": "3"}),
				withAttrs(evOf(1, "c", "2026-09-02T10:00:00Z"), "", map[string]string{"copy": "3"})))
			return []string{twice, d2}
		}},
		{"older day files without the newer columns", func(t *testing.T, dir string) []string {
			// Each a day's named event, without install_id, user_name and group_name.
			var in []string
			for _, day := range []string{"2026-10-10", "2026-10-11"} {
				in = append(in, oldFile(t, dir, "merge-older-days", day+".parquet"))
			}
			return in
		}},
		{"an older day file beside a new one", func(t *testing.T, dir string) []string {
			// p1 of u1, without install_id, user_name and group_name.
			old := oldFile(t, dir, "merge-older-beside-new", "2026-10-10.parquet")
			return []string{old, f.goDay(t, dir, "2026-10-11.parquet", logOf(t, named(evOf(1, "p2", "2026-10-11T10:00:00Z"), "u1", "Ada", "g", "Team")))}
		}},
		{"type-ambiguous VARCHAR", func(t *testing.T, dir string) []string {
			return []string{
				f.goDay(t, dir, "2026-09-01.parquet", logOf(t,
					withAttrs(evOf(1, "a", "2026-09-01T10:00:00Z"), uuid, map[string]string{"d": "2026-10-12", "t": "12:30", "n": "12"}),
					withAttrs(evOf(1, "b", "2026-09-01T11:00:00Z"), uuid, map[string]string{"d": "2026-10-13", "t": "12:31", "n": "1.50"}))),
				f.goDay(t, dir, "2026-09-02.parquet", logOf(t, withAttrs(evOf(1, "c", "2026-09-02T10:00:00Z"), "hello", map[string]string{"d": "word", "t": "word", "n": "true"}))),
			}
		}},
		{"every dump case as a day, Go-written", everyDump(f.goDay)},
		{"every dump case as a day, DuckDB-written", everyDump(old("merge-dump-cases"))},
		{"many rows", func(t *testing.T, dir string) []string {
			return []string{
				f.goDay(t, dir, "2026-09-01.parquet", sortedLog(t, "2026-09-01", "a", 0, 75000, "1")),
				f.goDay(t, dir, "2026-09-02.parquet", sortedLog(t, "2026-09-02", "b", 0, 30000, "1")),
				f.goDay(t, dir, "2026-09-02.1.parquet", sortedLog(t, "2026-09-02", "b", 29000, 4000, "2")),
				f.goDay(t, dir, "2026-09-03.parquet", sortedLog(t, "2026-09-01", "a", 74000, 3000, "3")),
			}
		}},
		{"wide", func(t *testing.T, dir string) []string {
			var in []string
			for d := 1; d <= 2; d++ {
				var evs []store.Event
				for i := 0; i < 240; i++ {
					evs = append(evs, withAttrs(evOf(1, fmt.Sprintf("w%d-%d", d, i), fmt.Sprintf("2026-09-%02dT10:%02d:%02dZ", d, i/60, i%60)), "",
						map[string]string{fmt.Sprintf("k%d", (i*7+d)%270): "x", "common": strconv.Itoa(i)}))
				}
				in = append(in, f.goDay(t, dir, fmt.Sprintf("2026-09-%02d.parquet", d), logOf(t, evs...)))
			}
			return in
		}},
	}
}

// TestGoMergeMatchesDuckDB: for each month's inputs, the Go merge's file,
// read back, is its golden (the file the old query engine's COPY of
// mergeQuery wrote): its columns, of the same types in the same order,
// then every fixed column no input had, all NULL in every row; and the
// same rows, compared as a multiset. The Go file's own order is (family,
// ts) non-decreasing, NULLs last.
func TestGoMergeMatchesDuckDB(t *testing.T) {
	f := newFixture(t)
	for i, c := range mergeCases(t, f) {
		t.Run(c.name, func(t *testing.T) {
			dir := f.project1(fmt.Sprintf("case-%d", i))
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			inputs := c.build(t, dir)
			goFile := filepath.Join(dir, "go.parquet")
			if err := mergeFiles(context.Background(), inputs, goFile); err != nil {
				t.Fatal(err)
			}
			p := readTable(t, goFile)
			orderedByFamilyAndTS(t, p)
			// The fixed columns no input had come last, all NULL.
			had := map[string]bool{}
			for _, in := range inputs {
				for _, col := range openMeta(t, in).Schema().Columns() {
					had[strings.ToLower(col[0])] = true
				}
			}
			var added []string
			for j := len(p.names) - 1; j >= 0 && !had[strings.ToLower(p.names[j])]; j-- {
				name := p.names[j]
				if k := fieldOf(name); k < 0 || fields[k].typ != p.types[j] {
					t.Fatalf("added column %s %s: not a fixed column of its type", name, p.types[j])
				}
				for _, r := range p.rows {
					if r[j] != nil {
						t.Fatalf("added column %s: a row not NULL", name)
					}
				}
				added = append(added, name)
			}
			for _, n := range p.names[:len(p.names)-len(added)] {
				if !had[strings.ToLower(n)] {
					t.Fatalf("column %s no input had, before an input's", n)
				}
			}
			fileFrozen(t, c.name, "Go merge", p.without(added...), false)
			t.Logf("%d inputs, columns %d (+%d added), rows %d", len(inputs), len(p.names)-len(added), len(added), len(p.rows))
		})
	}
}

// TestMergeKeepsTiesInInputOrder: rows equal in family and ts come out by
// input, then by row, and a row group is cut where the family changes,
// where the day does once the group holds dayCutRows rows, or at
// maxGroupRows.
func TestMergeKeepsTiesInInputOrder(t *testing.T) {
	f := newFixture(t)
	dir := f.project1("")
	d1 := f.goDay(t, dir, "2026-09-01.parquet", logOf(t, evOf(1, "p-1a", "2026-09-01T10:00:00Z"), evOf(1, "p-1b", "2026-09-01T10:00:00Z"),
		dumpView("v-1", "2026-09-01T10:00:00Z")))
	seg := f.goDay(t, dir, "2026-09-01.1.parquet", logOf(t, evOf(1, "p-sa", "2026-09-01T10:00:00Z"), evOf(1, "p-2", "2026-09-02T10:00:00Z"),
		dumpView("v-s", "2026-09-01T10:00:00Z")))
	out := f.project1("2026-09.parquet")
	if err := mergeFiles(context.Background(), []string{d1, seg}, out); err != nil {
		t.Fatal(err)
	}
	got := texts(readTable(t, out, "id").pick(t, "id"))
	if want := []string{"[p-1a]", "[p-1b]", "[p-sa]", "[p-2]", "[v-1]", "[v-s]"}; !eq(got, want) {
		t.Fatalf("rows = %v, want %v: ties by input, then row", got, want)
	}
	if got := groupRows(t, out); fmt.Sprint(got) != "[4 2]" {
		t.Fatalf("row groups = %v, want [4 2]: cut at the family, not at a day under dayCutRows", got)
	}
	setDayCutRows(t, 3)
	if err := mergeFiles(context.Background(), []string{d1, seg}, out); err != nil {
		t.Fatal(err)
	}
	if got := groupRows(t, out); fmt.Sprint(got) != "[3 1 2]" {
		t.Fatalf("row groups = %v, want [3 1 2]: past 3 rows, cut at the day, then at the family", got)
	}
	setDayCutRows(t, 32768)
	big := f.goDay(t, dir, "2026-09-03.parquet", sortedLog(t, "2026-09-03", "a", 1, 80000, "1")) // ids a-…0 are views
	if err := mergeFiles(context.Background(), []string{big}, out); err != nil {
		t.Fatal(err)
	}
	if got := groupRows(t, out); fmt.Sprint(got) != "[65536 6464 8000]" {
		t.Fatalf("row groups = %v, want [65536 6464 8000]", got)
	}
}

// TestMergeHoldsFewFooters: a merge of more inputs than maxFooters holds
// at most maxFooters of their footers decoded at once, decodes a footer
// let go again when its input is read, and still writes every row once.
func TestMergeHoldsFewFooters(t *testing.T) {
	f := newFixture(t)
	dir := f.project1("")
	var inputs []string
	for d := 1; d <= 3*maxFooters; d++ {
		day := fmt.Sprintf("2026-09-%02d", d)
		inputs = append(inputs, f.goDay(t, dir, day+".parquet", sortedLog(t, day, fmt.Sprintf("d%02d", d), 0, 40, day)))
	}
	// A repeat of the first day's ids, as a late segment: dropped.
	inputs = append(inputs, f.goDay(t, dir, "2026-09-01.1.parquet", sortedLog(t, "2026-09-01", "d01", 0, 40, "again")))
	m, err := openMerge(inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	if err := m.markRepeats(); err != nil {
		t.Fatal(err)
	}
	out := f.project1("2026-09.parquet")
	if err := m.write(context.Background(), out); err != nil {
		t.Fatal(err)
	}
	if m.footers.most > maxFooters {
		t.Fatalf("held %d footers at once, want at most %d", m.footers.most, maxFooters)
	}
	// Pass 1 decodes each once; pass 2 again for each family (views, then
	// product) of each input.
	if m.footers.decoded <= len(inputs)*2 {
		t.Fatalf("decoded %d footers for %d inputs: pass 2 kept them", m.footers.decoded, len(inputs))
	}
	ids, again := map[any]bool{}, 0
	rows := readTable(t, out, "id", "attr_copy").pick(t, "id", "attr_copy")
	for _, r := range rows {
		ids[r[0]] = true
		if r[1] == "again" {
			again++
		}
	}
	got := []string{fmt.Sprintf("[%d %d %d]", len(rows), len(ids), again)}
	if want := []string{fmt.Sprintf("[%d %d 0]", 40*3*maxFooters, 40*3*maxFooters)}; !eq(got, want) {
		t.Fatalf("rows, ids, repeats = %v, want %v", got, want)
	}
}

// TestMergeKeepsTheFirstSpelling is Review Focus 1: attr_Plan one day and
// attr_plan the next are one column of the month, the first spelling, with
// both days' values.
func TestMergeKeepsTheFirstSpelling(t *testing.T) {
	f := newFixture(t)
	f.write(t, withAttrs(evOf(1, "a", "2026-09-01T10:00:00Z"), "", map[string]string{"Plan": "A"}),
		withAttrs(evOf(1, "b", "2026-09-02T10:00:00Z"), "", map[string]string{"plan": "b"}))
	f.maintain(t, "2026-09-03")
	f.maintain(t, "2026-10-05")
	month := f.project1("2026-09.parquet")
	p := readTable(t, month)
	got := slices.DeleteFunc(slices.Clone(p.names), func(n string) bool { return strings.ToLower(n) != "attr_plan" })
	if !eq(got, []string{"attr_Plan"}) {
		t.Fatalf("columns = %v, want attr_Plan alone", got)
	}
	if got := sortedTexts(p.pick(t, "id", "attr_Plan")); !eq(got, []string{"[a A]", "[b b]"}) {
		t.Fatalf("rows = %v", got)
	}
}

// TestMergeRefusesAnUnsortedInput: an input not in (family, ts) order fails
// the merge, naming it; the month stays as its day files, no month file
// and no .tmp left.
func TestMergeRefusesAnUnsortedInput(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-09-01T10:00:00Z"), evOf(1, "b", "2026-09-01T11:00:00Z"), evOf(1, "c", "2026-09-02T10:00:00Z"))
	f.maintain(t, "2026-09-03")
	day := f.project1("2026-09-01.parquet")
	p := readTable(t, day)
	slices.Reverse(p.rows) // a at 10:00, b at 11:00: now ts descending
	writeTable(t, day+".x", p)
	if err := os.Rename(day+".x", day); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.log.Dir(), "1")
	names, err := list(dir)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := f.a.merge(context.Background(), dir, names, "2026-09")
	if err == nil || merged || !strings.Contains(err.Error(), "2026-09-01.parquet: not sorted by family and ts") {
		t.Fatalf("merge = %v, %v; want the unsorted day refused, by name", merged, err)
	}
	if got := f.files(t); !eq(got, []string{"2026-09-01.parquet", "2026-09-02.parquet"}) {
		t.Fatalf("files = %v, want the day files alone", got)
	}
}

// TestMergeRefusesAColumnOfAnotherType: a column whose type is not the
// month's (a fixed column's own, VARCHAR for any other) fails the merge,
// naming the input and the column.
func TestMergeRefusesAColumnOfAnotherType(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-09-01T10:00:00Z"), evOf(1, "b", "2026-09-02T10:00:00Z"))
	f.maintain(t, "2026-09-03")
	day := f.project1("2026-09-02.parquet")
	b := readTable(t, day, "id", "family", "ts", "day").pick(t, "id", "family", "ts", "day")[0]
	ts, err := time.Parse(time.RFC3339Nano, b[2].(string))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		col  string
		node parquet.Node
		v    parquet.Value
		want string
	}{
		{"attr_n", parquet.Int(64), parquet.Int64Value(7), "column attr_n is INT(64,true), want VARCHAR"},
		{"display_width", parquet.Int(32), parquet.Int32Value(1280), "column display_width is INT(32,true), want BIGINT"},
		{"ts", parquet.TimestampAdjusted(parquet.Microsecond, true), parquet.Int64Value(ts.UnixMicro()),
			"column ts is TIMESTAMP(isAdjustedToUTC=true,unit=MICROS), want TIMESTAMP"},
		{"value", parquet.Leaf(parquet.FloatType), parquet.FloatValue(0.5), "column value is FLOAT, want DOUBLE"},
	} {
		// b's id, family, ts and day, and the column at its odd type.
		group := parquet.Group{"id": parquet.Optional(parquet.String()), "family": parquet.Optional(parquet.String()),
			"ts": parquet.Optional(parquet.TimestampAdjusted(parquet.Microsecond, false)), "day": parquet.Optional(parquet.Date())}
		group[c.col] = parquet.Optional(c.node)
		vals := map[string]parquet.Value{"id": parquet.ByteArrayValue([]byte("b")), "family": parquet.ByteArrayValue([]byte(b[1].(string))),
			"ts": parquet.Int64Value(ts.UnixMicro()), "day": parquet.Int32Value(int32(ts.Unix() / 86400)), c.col: c.v}
		odd := f.project1("odd.parquet")
		writeOdd(t, odd, parquet.NewSchema("odd", group), vals)
		err := mergeFiles(context.Background(), []string{f.project1("2026-09-01.parquet"), odd}, f.project1("2026-09.parquet"))
		if err == nil || !strings.Contains(err.Error(), "odd.parquet: "+c.want) {
			t.Errorf("%s: err = %v, want %q", c.col, err, c.want)
		}
		if exists(f.project1("2026-09.parquet")) || exists(f.project1("2026-09.parquet.tmp")) {
			t.Fatalf("%s: a refused merge left a file", c.col)
		}
	}
}

// writeOdd writes one row of vals, by column name, as path in schema.
func writeOdd(t *testing.T, path string, schema *parquet.Schema, vals map[string]parquet.Value) {
	t.Helper()
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	w := parquet.NewWriter(fh, schema)
	row := make(parquet.Row, len(schema.Columns()))
	for i, col := range schema.Columns() {
		row[i] = vals[col[0]].Level(0, 1, i)
	}
	if _, err := w.WriteRows([]parquet.Row{row}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestMergeFailsOnAnUnmatchedInput: a merge never drops a row silently. An
// input it cannot read fails it, renaming and deleting nothing, and pass 2
// must write every row pass 1 kept: a row neither written nor marked fails
// the merge and leaves no file.
func TestMergeFailsOnAnUnmatchedInput(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-09-01T10:00:00Z"), evOf(1, "b", "2026-09-01T11:00:00Z"), evOf(1, "c", "2026-09-02T10:00:00Z"))
	f.maintain(t, "2026-09-03")
	d1, d2 := f.project1("2026-09-01.parquet"), f.project1("2026-09-02.parquet")
	out := f.project1("2026-09.parquet")

	m, err := openMerge([]string{d1, d2})
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	if err := m.markRepeats(); err != nil {
		t.Fatal(err)
	}
	m.inputs[0].drop[0] |= 1 << 1 // b marked, but not counted
	if err := m.write(context.Background(), out); err == nil || !strings.Contains(err.Error(), "archive: merge wrote 2 rows, want 3") {
		t.Fatalf("err = %v, want the uncounted row refused", err)
	}
	if exists(out) || exists(out+".tmp") {
		t.Fatal("the failed merge left a file")
	}

	b, err := os.ReadFile(d2)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d2, b[:len(b)/2], 0o640); err != nil { // torn: no footer
		t.Fatal(err)
	}
	dir := filepath.Join(f.log.Dir(), "1")
	names, _ := list(dir)
	if merged, err := f.a.merge(context.Background(), dir, names, "2026-09"); err == nil || merged || !strings.Contains(err.Error(), "2026-09-02.parquet") {
		t.Fatalf("merge = %v, %v; want the torn day refused, by name", merged, err)
	}
	if got := f.files(t); !eq(got, []string{"2026-09-01.parquet", "2026-09-02.parquet"}) {
		t.Fatalf("files = %v, want the day files alone", got)
	}
}

// TestMergeSendsTheEngineNothing: a month's merge runs in Go (the Archive
// holds no engine).
func TestMergeSendsTheEngineNothing(t *testing.T) {
	f := newFixture(t)
	a := f.a
	dir := f.project1("")
	f.goDay(t, dir, "2026-09-01.parquet", sortedLog(t, "2026-09-01", "a", 0, 5000, "1"))
	f.goDay(t, dir, "2026-09-01.1.parquet", sortedLog(t, "2026-09-01", "a", 4000, 2000, "2"))
	f.goDay(t, dir, "2026-09-02.parquet", sortedLog(t, "2026-09-02", "b", 0, 5000, "1"))
	names, err := list(filepath.Clean(dir))
	if err != nil {
		t.Fatal(err)
	}
	merged, err := a.merge(context.Background(), filepath.Clean(dir), names, "2026-09")
	if err != nil || !merged {
		t.Fatalf("merge = %v, %v", merged, err)
	}
	if got := f.files(t); !eq(got, []string{"2026-09.parquet"}) {
		t.Fatalf("files = %v, want the month file alone", got)
	}
	if n, ids := f.rows(t, "2026-09.parquet"); n != 11000 || ids != 11000 {
		t.Fatalf("month file: %d rows, %d ids; want 11000 and 11000", n, ids)
	}
}

// TestMergeRefusesAFooterThatMiscounts: a footer whose row count is not
// the sum of its row groups' fails the merge, naming the file, before
// pass 1 sizes its drop bits by that count.
func TestMergeRefusesAFooterThatMiscounts(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-09-01T10:00:00Z"), evOf(1, "b", "2026-09-01T11:00:00Z"))
	f.maintain(t, "2026-09-02")
	day := f.project1("2026-09-01.parquet")
	b, err := os.ReadFile(day)
	if err != nil {
		t.Fatal(err)
	}
	md, err := footerOf(b)
	if err != nil {
		t.Fatal(err)
	}
	md.NumRows++
	foot, err := thrift.Marshal(new(thrift.CompactProtocol), md)
	if err != nil {
		t.Fatal(err)
	}
	n := int(binary.LittleEndian.Uint32(b[len(b)-8:]))
	out := append(append([]byte{}, b[:len(b)-8-n]...), foot...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(foot)))
	if err := os.WriteFile(day, append(out, "PAR1"...), 0o640); err != nil {
		t.Fatal(err)
	}
	err = mergeFiles(context.Background(), []string{day}, f.project1("2026-09.parquet"))
	if err == nil || !strings.Contains(err.Error(), "archive: 2026-09-01.parquet: row groups hold 2 rows, footer says 3") {
		t.Fatalf("err = %v, want the miscounting footer refused", err)
	}
	if exists(f.project1("2026-09.parquet")) {
		t.Fatal("a refused merge left a file")
	}
}
