package archive

// mergeQuery and mergeSelect, the merge's DuckDB oracle, leave with DuckDB
// in step 6.

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/parquet-go/parquet-go/encoding/thrift"
)

// mergeQuery is the merge's SELECT of files, given oldest first (a month
// file a crashed merge left, then each day's file and its segments in
// order): every id once, from the first file that holds it (a file holds
// an id once), ordered by family and ts. As in the dump, the first row
// per id is found over the ids and a row key alone, and the sort, which
// spills, carries the rows.
func mergeQuery(files []string) string { return mergeSelect(files, files) }

// mergeSelect reads files and keys each row by the position in order of
// its file's name, as read_parquet reports it (the path as given, today).
// A name that order does not hold would key the row NULL and the IN would
// drop it, then the merge would delete its day: it fails the query instead.
func mergeSelect(files, order []string) string {
	rows := "read_parquet(" + sqlList(files) + ", union_by_name=true, filename=true, file_row_number=true)"
	key := "coalesce(list_position(" + sqlList(order) + ", filename), error('archive: merge input not matched: ' || filename))" +
		" * 4294967296 + file_row_number"
	firsts := "SELECT min(k) FROM (SELECT id, " + key + " AS k FROM " + rows + ") GROUP BY id"
	return "SELECT * EXCLUDE (filename, file_row_number, k) FROM (SELECT *, " + key + " AS k FROM " + rows +
		") WHERE k IN (" + firsts + ") ORDER BY family, ts"
}

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

// duckDay dumps log as dir/name with the oracle, as a release before the
// Go dump wrote it.
func (f fixture) duckDay(t *testing.T, dir, name, log string) string {
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
	if err := f.duckDump(t, src, out); err != nil {
		t.Fatal(err)
	}
	os.Remove(src)
	return out
}

// duckCopy writes sql's rows as out through DuckDB.
func (f fixture) duckCopy(t *testing.T, out, sql string) string {
	t.Helper()
	if err := f.eng.Exec(context.Background(), "COPY ("+sql+") TO "+quote(out)+" (FORMAT parquet, COMPRESSION zstd)"); err != nil {
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
			month := f.duckCopy(t, filepath.Join(dir, "2026-09.parquet"), "SELECT * REPLACE ('month' AS attr_copy) FROM read_parquet("+
				sqlList([]string{d1, d2})+", union_by_name=true) WHERE id <> 'd' ORDER BY family, ts")
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
			d1 := f.goDay(t, dir, "src.parquet", logOf(t, withAttrs(evOf(1, "a", "2026-09-01T10:00:00Z"), "", map[string]string{"copy": "1"}),
				withAttrs(evOf(1, "b", "2026-09-01T11:00:00Z"), "", map[string]string{"copy": "1"})))
			twice := f.duckCopy(t, filepath.Join(dir, "2026-09-01.parquet"), "SELECT * FROM (SELECT * FROM read_parquet("+quote(d1)+
				") UNION ALL SELECT * REPLACE ('2' AS attr_copy, ts + INTERVAL 1 HOUR AS ts) FROM read_parquet("+quote(d1)+")) ORDER BY family, ts")
			d2 := f.goDay(t, dir, "2026-09-02.parquet", logOf(t, withAttrs(evOf(1, "a", "2026-09-02T10:00:00Z"), "", map[string]string{"copy": "3"}),
				withAttrs(evOf(1, "c", "2026-09-02T10:00:00Z"), "", map[string]string{"copy": "3"})))
			return []string{twice, d2}
		}},
		{"older day files without the newer columns", func(t *testing.T, dir string) []string {
			var in []string
			for i, day := range []string{"2026-10-10", "2026-10-11"} {
				src := f.goDay(t, dir, fmt.Sprintf("new-%d.parquet", i), logOf(t, named(evOf(1, fmt.Sprintf("p%d", i), day+"T10:00:00Z"), "u1", "Ada", "g", "Team")))
				in = append(in, f.duckCopy(t, filepath.Join(dir, day+".parquet"), "SELECT * EXCLUDE (install_id, user_name, group_name) FROM read_parquet("+quote(src)+")"))
			}
			return in
		}},
		{"an older day file beside a new one", func(t *testing.T, dir string) []string {
			src := f.goDay(t, dir, "new.parquet", logOf(t, named(evOf(1, "p1", "2026-10-10T10:00:00Z"), "u1", "", "", "")))
			old := f.duckCopy(t, filepath.Join(dir, "2026-10-10.parquet"), "SELECT * EXCLUDE (install_id, user_name, group_name) FROM read_parquet("+quote(src)+")")
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
		{"every dump case as a day, DuckDB-written", everyDump(f.duckDay)},
		{"many rows", func(t *testing.T, dir string) []string {
			return []string{
				f.goDay(t, dir, "2026-09-01.parquet", sortedLog(t, "2026-09-01", "a", 0, 75000, "1")),
				f.duckDay(t, dir, "2026-09-02.parquet", sortedLog(t, "2026-09-02", "b", 0, 30000, "1")),
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

// TestGoMergeMatchesDuckDB: for each month's inputs, the Go merge's file
// equals the one DuckDB's COPY of mergeQuery wrote, read through DuckDB:
// DuckDB's columns, of the same types in the same order, then every fixed
// column no input had, all NULL in every row; and the same rows, compared
// as a multiset and in (family, ts, id) order. The Go file's own order is
// (family, ts) non-decreasing, NULLs last.
func TestGoMergeMatchesDuckDB(t *testing.T) {
	f := newFixtureWith(t, 4, "512MB")
	for i, c := range mergeCases(t, f) {
		t.Run(c.name, func(t *testing.T) {
			dir := f.project1(fmt.Sprintf("case-%d", i))
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			inputs := c.build(t, dir)
			duck, goFile := filepath.Join(dir, "duck.parquet"), filepath.Join(dir, "go.parquet")
			f.duckCopy(t, duck, mergeQuery(inputs))
			if err := mergeFiles(context.Background(), inputs, goFile); err != nil {
				t.Fatal(err)
			}
			got, want := f.describe(t, goFile), f.describe(t, duck)
			if len(got) < len(want) || !eq(got[:len(want)], want) {
				t.Fatalf("Go file's columns\n%v\nwant DuckDB's first\n%v", got, want)
			}
			var added []string
			for _, d := range got[len(want):] {
				var name, typ string
				fmt.Sscan(strings.Trim(d, "[]"), &name, &typ)
				if i := fieldOf(name); i < 0 || fields[i].typ != typ {
					t.Fatalf("added column %s: not a fixed column of its type", d)
				}
				for _, w := range want {
					if strings.HasPrefix(w, "["+name+" ") {
						t.Fatalf("added column %s DuckDB has", name)
					}
				}
				added = append(added, ident(name))
				if n := f.values(t, "SELECT count(*) FROM read_parquet("+quote(goFile)+") WHERE "+ident(name)+" IS NOT NULL"); !eq(n, []string{"[0]"}) {
					t.Errorf("added column %s: %v rows not NULL", name, n)
				}
			}
			goRel := "read_parquet(" + quote(goFile) + ")"
			if len(added) > 0 {
				goRel = "(SELECT * EXCLUDE (" + strings.Join(added, ", ") + ") FROM " + goRel + ")"
			}
			duckRel := "read_parquet(" + quote(duck) + ")"
			if len(want) > 200 {
				// EXCEPT ALL over hundreds of columns runs DuckDB out of
				// memory: each row is compared as its text instead.
				asText := func(rel string) string {
					return "(SELECT r::VARCHAR AS row, family, ts, id FROM " + rel + " r)"
				}
				goRel, duckRel = asText(goRel), asText(duckRel)
			}
			f.sameRows(t, goRel, duckRel)
			order := " ORDER BY family NULLS LAST, ts NULLS LAST, id NULLS LAST"
			if g, d := f.values(t, "SELECT * FROM "+goRel+order), f.values(t, "SELECT * FROM "+duckRel+order); !eq(g, d) {
				t.Errorf("rows in (family, ts, id) order differ")
			}
			f.orderedByFamilyAndTS(t, goFile)
			t.Logf("%d inputs, columns %d (+%d added), rows %v", len(inputs), len(want), len(added), f.values(t, "SELECT count(*) FROM "+duckRel))
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
	got := f.values(t, "SELECT id FROM read_parquet("+quote(out)+", file_row_number=true) ORDER BY file_row_number")
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
	got := f.values(t, "SELECT count(*), count(DISTINCT id), count(*) FILTER (WHERE attr_copy = 'again') FROM read_parquet("+quote(out)+")")
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
	got := f.values(t, `SELECT column_name FROM (DESCRIBE SELECT * FROM read_parquet(`+quote(month)+`)) WHERE lower(column_name) = 'attr_plan'`)
	if !eq(got, []string{"[attr_Plan]"}) {
		t.Fatalf("columns = %v, want attr_Plan alone", got)
	}
	if got := f.values(t, `SELECT id, "attr_Plan" FROM read_parquet(`+quote(month)+`) ORDER BY id`); !eq(got, []string{"[a A]", "[b b]"}) {
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
	f.duckCopy(t, day+".x", "SELECT * FROM read_parquet("+quote(day)+") ORDER BY ts DESC")
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
	for _, c := range []struct{ sel, want string }{
		{"*, 7::BIGINT AS attr_n", "column attr_n is INT(64,true), want VARCHAR"},
		{"* REPLACE (CAST(display_width AS INTEGER) AS display_width)", "column display_width is INT(32,true), want BIGINT"},
		{"* REPLACE (CAST(ts AS TIMESTAMPTZ) AS ts)", "column ts is TIMESTAMP(isAdjustedToUTC=true,unit=MICROS), want TIMESTAMP"},
		{"* REPLACE (CAST(value AS FLOAT) AS value)", "column value is FLOAT, want DOUBLE"},
	} {
		odd := f.duckCopy(t, f.project1("odd.parquet"), "SELECT "+c.sel+" FROM read_parquet("+quote(day)+")")
		err := mergeFiles(context.Background(), []string{f.project1("2026-09-01.parquet"), odd}, f.project1("2026-09.parquet"))
		if err == nil || !strings.Contains(err.Error(), "odd.parquet: "+c.want) {
			t.Errorf("%s: err = %v, want %q", c.sel, err, c.want)
		}
		if exists(f.project1("2026-09.parquet")) || exists(f.project1("2026-09.parquet.tmp")) {
			t.Fatalf("%s: a refused merge left a file", c.sel)
		}
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
