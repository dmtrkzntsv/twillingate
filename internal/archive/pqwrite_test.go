package archive

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
)

// allTypes is a column of each type, the rows below their values: NULLs,
// an empty string, a pre-epoch day, negative and fractional numbers.
var allTypes = []pqColumn{
	{"v", colVarchar}, {"b", colBigint}, {"d", colDouble}, {"dt", colDate}, {"ts", colTimestamp},
}

var allTypesRows = []row{
	{strValue("héllo"), intValue(-42), floatValue(1.5), dateValue(20392), timestampValue(1761955205123456)},
	{strValue(""), intValue(9007199254740993), floatValue(-0.1), dateValue(-1), timestampValue(-1)},
	{value{}, value{}, value{}, value{}, value{}},
	{strValue("it's"), intValue(0), floatValue(1e300), dateValue(0), timestampValue(0)},
}

// allTypesSQL is allTypesRows as DuckDB values.
const allTypesSQL = `SELECT * FROM (VALUES
	('héllo', -42::BIGINT, 1.5::DOUBLE, DATE '2025-10-31', TIMESTAMP '2025-11-01 00:00:05.123456'),
	('', 9007199254740993::BIGINT, -0.1::DOUBLE, DATE '1969-12-31', TIMESTAMP '1969-12-31 23:59:59.999999'),
	(NULL, NULL, NULL, NULL, NULL),
	('it''s', 0::BIGINT, 1e300::DOUBLE, DATE '1970-01-01', TIMESTAMP '1970-01-01 00:00:00')
) t(v, b, d, dt, ts)`

func writeRows(t *testing.T, path string, cols []pqColumn, cut func(prev, next row, rows int) bool, rows ...row) {
	t.Helper()
	w, err := newRowWriter(path, cols, cut)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if err := w.Write(r); err != nil {
			w.Abort()
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func openMeta(t *testing.T, path string) *parquet.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	st, _ := f.Stat()
	pf, err := parquet.OpenFile(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	return pf
}

// leafFacts is what DuckDB reads from a leaf: its physical and logical
// type, repetition, and the chunks' codec and encodings.
func leafFacts(pf *parquet.File) []string {
	var out []string
	for i, col := range pf.Schema().Columns() {
		lc, _ := pf.Schema().Lookup(col...)
		n := lc.Node
		lt := "none"
		if l := n.Type().LogicalType(); l != nil {
			lt = l.String()
		}
		facts := fmt.Sprintf("%s %s %s optional=%t", col[0], n.Type().Kind(), lt, n.Optional())
		if len(pf.Metadata().RowGroups) > 0 {
			md := pf.Metadata().RowGroups[0].Columns[i].MetaData
			dict := false
			for _, e := range md.Encoding {
				dict = dict || e == format.RLEDictionary || e == format.PlainDictionary
			}
			facts += fmt.Sprintf(" %s dict=%t", md.Codec, dict)
		}
		out = append(out, facts)
	}
	return out
}

// TestWriterTypesMatchDuckDB: a file of every type reads back in DuckDB
// with the types and values of the file DuckDB COPYs from the same values,
// and its leaves are typed as DuckDB types them (native's reader goes by
// those: pqwrite_native_test.go reads the values back through it).
func TestWriterTypesMatchDuckDB(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	dir := filepath.Join(f.log.Dir(), "w")
	os.MkdirAll(dir, 0o750)
	goFile, duckFile := filepath.Join(dir, "go.parquet"), filepath.Join(dir, "duck.parquet")
	writeRows(t, goFile, allTypes, nil, allTypesRows...)
	if err := f.eng.Exec(ctx, "COPY ("+allTypesSQL+") TO "+quote(duckFile)+" (FORMAT parquet, COMPRESSION zstd)"); err != nil {
		t.Fatal(err)
	}
	describe := func(p string) []string {
		return f.values(t, "SELECT column_name, column_type, \"null\" FROM (DESCRIBE SELECT * FROM read_parquet("+quote(p)+"))")
	}
	if got, want := describe(goFile), describe(duckFile); !eq(got, want) {
		t.Errorf("DESCRIBE of the Go file = %v, of DuckDB's %v", got, want)
	}
	// DuckDB's file, frozen: its DESCRIBE, its rows as read below, and its
	// leaves' types (the first five facts: the dictionary may differ).
	type typesAnswer struct {
		Describe, Rows, Leaves []string
	}
	leaves := func(facts []string) []string {
		out := make([]string, len(facts))
		for i, l := range facts {
			out[i] = strings.Join(strings.Fields(l)[:5], " ")
		}
		return out
	}
	if got := describe(goFile); !eq(got, []string{"[v VARCHAR YES]", "[b BIGINT YES]", "[d DOUBLE YES]", "[dt DATE YES]", "[ts TIMESTAMP YES]"}) {
		t.Errorf("DESCRIBE = %v", got)
	}
	read := func(p string) []string {
		return f.values(t, "SELECT v, b, d, dt::VARCHAR, ts::VARCHAR, epoch_us(ts) FROM read_parquet("+quote(p)+")")
	}
	if got, want := read(goFile), read(duckFile); !eq(got, want) {
		t.Errorf("Go file reads %v, DuckDB's %v", got, want)
	}
	golden := frozen(t, "all types", typesAnswer{Describe: describe(duckFile), Rows: read(duckFile),
		Leaves: leaves(leafFacts(openMeta(t, duckFile)))})
	if got := describe(goFile); !eq(got, golden.Describe) {
		t.Errorf("DESCRIBE of the Go file = %v, the golden's %v", got, golden.Describe)
	}
	if got := read(goFile); !eq(got, golden.Rows) {
		t.Errorf("Go file reads %v, the golden's %v", got, golden.Rows)
	}
	if got := leaves(leafFacts(openMeta(t, goFile))); !eq(got, golden.Leaves) {
		t.Errorf("Go leaves %v, the golden's %v", got, golden.Leaves)
	}
	// The leaves, as parquet-go reads them, equal DuckDB's but for where
	// the dictionary is used: always on a VARCHAR here, where DuckDB judges
	// by the values (and dictionary-encodes numbers too at times), which
	// changes no type DuckDB or native reads.
	gf, df := leafFacts(openMeta(t, goFile)), leafFacts(openMeta(t, duckFile))
	want := []string{
		"v BYTE_ARRAY STRING optional=true ZSTD dict=true",
		"b INT64 INT(64,true) optional=true ZSTD dict=false",
		"d DOUBLE none optional=true ZSTD dict=false",
		"dt INT32 DATE optional=true ZSTD dict=false",
		"ts INT64 TIMESTAMP(isAdjustedToUTC=false,unit=MICROS) optional=true ZSTD dict=false",
	}
	if !eq(gf, want) {
		t.Errorf("Go leaves:\n%s\nwant\n%s", strings.Join(gf, "\n"), strings.Join(want, "\n"))
	}
	for j := range gf {
		if g, d := strings.Fields(gf[j]), strings.Fields(df[j]); strings.Join(g[:5], " ") != strings.Join(d[:5], " ") {
			t.Errorf("Go leaf %s, DuckDB's %s", gf[j], df[j])
		}
	}
	// The accessors read back what the constructors put in.
	r := allTypesRows[0]
	if r[0].str() != "héllo" || r[1].int() != -42 || r[2].float() != 1.5 || r[3].date() != 20392 ||
		r[4].timestamp() != 1761955205123456 || r[0].null() || !allTypesRows[2][0].null() {
		t.Errorf("accessors = %v", r)
	}
}

// TestWriterKeepsColumnOrder: columns are in the order given, not by name.
func TestWriterKeepsColumnOrder(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.log.Dir(), "w")
	os.MkdirAll(dir, 0o750)
	p := filepath.Join(dir, "order.parquet")
	cols := []pqColumn{{"zeta", colBigint}, {"alpha", colVarchar}, {"Mid", colDate}, {"attr_b", colVarchar}, {"attr_a", colVarchar}}
	writeRows(t, p, cols, nil, row{intValue(1), strValue("a"), dateValue(1), strValue("b"), value{}})
	got := f.values(t, "SELECT column_name FROM (DESCRIBE SELECT * FROM read_parquet("+quote(p)+"))")
	if !eq(got, []string{"[zeta]", "[alpha]", "[Mid]", "[attr_b]", "[attr_a]"}) {
		t.Errorf("columns = %v", got)
	}
	if got := f.values(t, "SELECT * FROM read_parquet("+quote(p)+")"); !eq(got, []string{"[1 a 1970-01-02 b <nil>]"}) {
		t.Errorf("rows = %v", got)
	}
	if _, err := newRowWriter(filepath.Join(dir, "dup.parquet"), []pqColumn{{"a", colBigint}, {"a", colVarchar}}, nil); err == nil {
		t.Error("a column twice is accepted")
	}
	if _, err := newRowWriter(filepath.Join(dir, "bad.parquet"), []pqColumn{{"a", colType(99)}}, nil); err == nil {
		t.Error("an unknown type is accepted")
	}
	if exists(filepath.Join(dir, "dup.parquet.tmp")) || exists(filepath.Join(dir, "bad.parquet.tmp")) {
		t.Error("a refused schema leaves a .tmp")
	}
}

// statsCols are the columns the native reader prunes by, and a number.
var statsCols = []pqColumn{{"family", colVarchar}, {"day", colDate}, {"n", colBigint}}

// TestWriterStatsAreExact: every group has exact bounds, long strings
// untruncated, and null counts, an all-NULL group's too.
func TestWriterStatsAreExact(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "stats.parquet")
	long := strings.Repeat("x", 5000)
	cut := func(prev, next row, _ int) bool { return prev[2].int()/10 != next[2].int()/10 }
	writeRows(t, p, statsCols, cut,
		// group 0: long strings, a NULL day
		row{strValue(long + "b"), dateValue(20000), intValue(0)},
		row{strValue(long + "a"), value{}, intValue(1)},
		row{strValue(long + "c"), dateValue(19999), intValue(2)},
		// group 1: family all NULL
		row{value{}, dateValue(20001), intValue(10)},
		row{value{}, dateValue(20001), intValue(11)},
		// group 2: day all NULL, one family
		row{strValue("views"), value{}, intValue(20)},
	)
	pf := openMeta(t, p)
	type stat struct {
		min, max string
		nulls    int64
	}
	str := func(b []byte, typ colType) string {
		if b == nil {
			return "-"
		}
		if typ == colVarchar {
			return string(b)
		}
		return fmt.Sprint(b)
	}
	got := map[[2]int]stat{}
	for g, rg := range pf.Metadata().RowGroups {
		for c := range statsCols {
			st := rg.Columns[c].MetaData.Statistics
			got[[2]int{g, c}] = stat{str(st.MinValue, statsCols[c].Type), str(st.MaxValue, statsCols[c].Type), st.NullCount}
		}
	}
	le := func(days int32) string {
		return str([]byte{byte(days), byte(days >> 8), byte(days >> 16), byte(days >> 24)}, colDate)
	}
	le64 := func(n int64) string {
		b := make([]byte, 8)
		for i := range b {
			b[i] = byte(n >> (8 * i))
		}
		return str(b, colBigint)
	}
	want := map[[2]int]stat{
		{0, 0}: {long + "a", long + "c", 0}, {0, 1}: {le(19999), le(20000), 1}, {0, 2}: {le64(0), le64(2), 0},
		{1, 0}: {"-", "-", 2}, {1, 1}: {le(20001), le(20001), 0}, {1, 2}: {le64(10), le64(11), 0},
		{2, 0}: {"views", "views", 0}, {2, 1}: {"-", "-", 1}, {2, 2}: {le64(20), le64(20), 0},
	}
	if len(pf.Metadata().RowGroups) != 3 {
		t.Fatalf("row groups = %d, want 3", len(pf.Metadata().RowGroups))
	}
	for k, w := range want {
		if g := got[k]; g != w {
			t.Errorf("group %d column %s: min %.12s max %.12s nulls %d, want min %.12s max %.12s nulls %d",
				k[0], statsCols[k[1]].Name, g.min, g.max, g.nulls, w.min, w.max, w.nulls)
		}
	}
}

// setDayCutRows sets dayCutRows for the test, restored after it.
func setDayCutRows(t *testing.T, n int) {
	old := dayCutRows
	dayCutRows = n
	t.Cleanup(func() { dayCutRows = old })
}

// groupRows is each row group's row count.
func groupRows(t *testing.T, path string) []int64 {
	t.Helper()
	var out []int64
	for _, rg := range openMeta(t, path).Metadata().RowGroups {
		out = append(out, rg.NumRows)
	}
	return out
}

// TestWriterCutsGroups: a group ends where cut says, and after 65,536 rows.
func TestWriterCutsGroups(t *testing.T) {
	dir := t.TempDir()
	byFamily := func(prev, next row, _ int) bool { return prev[0] != next[0] }
	p := filepath.Join(dir, "cut.parquet")
	rows := []row{}
	for _, fam := range []string{"product", "product", "views", "views", "views", "measures"} {
		rows = append(rows, row{strValue(fam), dateValue(1), intValue(1)})
	}
	writeRows(t, p, statsCols, byFamily, rows...)
	if got := fmt.Sprint(groupRows(t, p)); got != "[2 3 1]" {
		t.Errorf("groups by family = %s", got)
	}

	big := filepath.Join(dir, "big.parquet")
	w, err := newRowWriter(big, statsCols, byFamily)
	if err != nil {
		t.Fatal(err)
	}
	r := row{strValue("views"), dateValue(1), value{}}
	n := 2*maxGroupRows + 5
	for i := range n {
		r[2] = intValue(int64(i))
		if i == n-2 {
			r[0] = strValue("x") // a cut 3 rows into the last group
		}
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(groupRows(t, big)); got != fmt.Sprint([]int{maxGroupRows, maxGroupRows, 3, 2}) {
		t.Errorf("groups by size = %s", got)
	}
}

// TestEmptyFileHasSchema: a file of no rows has its columns, typed, and no
// row groups.
func TestEmptyFileHasSchema(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.log.Dir(), "w")
	os.MkdirAll(dir, 0o750)
	p := filepath.Join(dir, "empty.parquet")
	writeRows(t, p, allTypes, nil)
	if got := groupRows(t, p); len(got) != 0 {
		t.Errorf("row groups = %v", got)
	}
	got := f.values(t, "SELECT column_name, column_type FROM (DESCRIBE SELECT * FROM read_parquet("+quote(p)+"))")
	if !eq(got, []string{"[v VARCHAR]", "[b BIGINT]", "[d DOUBLE]", "[dt DATE]", "[ts TIMESTAMP]"}) {
		t.Errorf("DESCRIBE = %v", got)
	}
	if got := f.values(t, "SELECT count(*) FROM read_parquet("+quote(p)+")"); !eq(got, []string{"[0]"}) {
		t.Errorf("count = %v", got)
	}
}

// TestWriterFileIsAtomicAnd0640: the file appears whole on Close, 0640,
// with no .tmp left; until then, and after an Abort or a failure, the old
// one stays.
func TestWriterFileIsAtomicAnd0640(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.parquet")
	if err := os.WriteFile(p, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p+".tmp", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := func() bool { b, _ := os.ReadFile(p); return string(b) == "old" }

	w, err := newRowWriter(p, statsCols, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(row{strValue("views"), dateValue(1), intValue(1)}); err != nil {
		t.Fatal(err)
	}
	if !old() || !exists(p+".tmp") {
		t.Error("before Close the old file is gone or there is no .tmp")
	}
	w.Abort()
	w.Abort()
	if !old() || exists(p+".tmp") {
		t.Error("Abort replaced the file or left the .tmp")
	}

	// A bad row fails the writer: Close fails and keeps the old file.
	w, err = newRowWriter(p, statsCols, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(row{strValue("views")}); err == nil {
		t.Error("a short row is accepted")
	}
	if err := w.Write(row{strValue("views"), dateValue(1), intValue(1)}); err == nil {
		t.Error("a write after a failure is accepted")
	}
	if err := w.Close(); err == nil || !old() || exists(p+".tmp") {
		t.Errorf("Close after a failure = %v; old kept %t, .tmp left %t", err, old(), exists(p+".tmp"))
	}

	// Under a umask that would make it 0600, the file is still 0640.
	defer syscall.Umask(syscall.Umask(0o077))
	w, err = newRowWriter(p, statsCols, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(row{strValue("views"), dateValue(1), intValue(1)}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o640 || old() || exists(p+".tmp") {
		t.Errorf("after Close: mode %v, old kept %t, .tmp left %t", st.Mode().Perm(), old(), exists(p+".tmp"))
	}
	if got := groupRows(t, p); fmt.Sprint(got) != "[1]" {
		t.Errorf("row groups = %v", got)
	}
	if err := w.Close(); err != nil {
		t.Errorf("a second Close = %v", err)
	}
	w.Abort() // a no-op after Close
	if !exists(p) {
		t.Error("Abort after Close removed the file")
	}
	if err := w.Write(row{strValue("views"), dateValue(1), intValue(1)}); err == nil {
		t.Error("a write after Close is accepted")
	}

	// A directory that is gone fails the start.
	if _, err := newRowWriter(filepath.Join(dir, "missing", "a.parquet"), statsCols, nil); err == nil {
		t.Error("a missing directory is accepted")
	}
}

// TestWriterFallsBackToPlain: a VARCHAR column whose dictionary outgrows
// dictMaxBytes goes on in PLAIN pages, DuckDB reads both kinds back, and
// the chunk's statistics stay exact across them: the minimum is in the
// first (dictionary) page, a long maximum and NULLs in PLAIN pages after
// the switch.
func TestWriterFallsBackToPlain(t *testing.T) {
	f := newFixture(t)
	defer SetDictMaxBytes(1 << 10)()
	dir := filepath.Join(f.log.Dir(), "w")
	os.MkdirAll(dir, 0o750)
	p := filepath.Join(dir, "plain.parquet")
	const n = 20000
	long := "path-~" + strings.Repeat("z", 3000) // sorts after every path-NNNNN
	var rows []row
	nulls := 0
	for i := range n {
		v := strValue(fmt.Sprintf("path-%05d", i))
		switch {
		case i == n-1:
			v = strValue(long)
		case i%7 == 3:
			v, nulls = value{}, nulls+1
		}
		rows = append(rows, row{v, intValue(int64(i))})
	}
	writeRows(t, p, []pqColumn{{"path", colVarchar}, {"n", colBigint}}, nil, rows...)
	pf := openMeta(t, p)
	md := pf.Metadata().RowGroups[0].Columns[0].MetaData
	if got := fmt.Sprint(md.Encoding); !strings.Contains(got, "PLAIN") || !strings.Contains(got, "RLE_DICTIONARY") {
		t.Errorf("encodings = %s, want PLAIN and RLE_DICTIONARY pages", got)
	}
	st := md.Statistics
	if string(st.MinValue) != "path-00000" || string(st.MaxValue) != long || st.NullCount != int64(nulls) {
		t.Errorf("stats: min %.12q max %.12q (%d bytes) nulls %d, want path-00000, the %d-byte max, %d",
			st.MinValue, st.MaxValue, len(st.MaxValue), st.NullCount, len(long), nulls)
	}
	// The pages: dictionary ones, then PLAIN ones holding NULLs too, the
	// last one the long maximum.
	pages := pf.RowGroups()[0].ColumnChunks()[0].Pages()
	defer pages.Close()
	var kinds []bool // dictionary-encoded, per page
	plainNulls, lastPlain := int64(0), false
	for {
		pg, err := pages.ReadPage()
		if err != nil {
			break
		}
		dict := pg.Dictionary() != nil
		kinds = append(kinds, dict)
		if !dict {
			plainNulls += pg.NumNulls()
			_, hi, _ := pg.Bounds()
			lastPlain = string(hi.ByteArray()) == long
		}
		parquet.Release(pg)
	}
	if len(kinds) < 3 || !kinds[0] || kinds[len(kinds)-1] || plainNulls == 0 || !lastPlain {
		t.Errorf("pages (dictionary-encoded) %v, NULLs in PLAIN pages %d, max in the last PLAIN page %t", kinds, plainNulls, lastPlain)
	}
	got := f.values(t, "SELECT count(DISTINCT path), min(path), length(max(path)), count(*) - count(path), "+
		"count(*) FILTER (path = 'path-' || lpad(n::VARCHAR, 5, '0')) FROM read_parquet("+quote(p)+")")
	if want := fmt.Sprintf("[%d path-00000 %d %d %d]", n-nulls, len(long), nulls, n-nulls-1); !eq(got, []string{want}) {
		t.Errorf("read back %v, want %s", got, want)
	}
}

// TestWriterRefusesTheWrongKind: a value of another kind than its column
// fails the writer, naming the column; a NULL fits any column. A value
// stays 32 bytes, its kind in what would be padding.
func TestWriterRefusesTheWrongKind(t *testing.T) {
	if n := unsafe.Sizeof(value{}); n != 32 {
		t.Errorf("a value is %d bytes, want 32", n)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "kind.parquet")
	w, err := newRowWriter(p, statsCols, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(row{value{}, value{}, value{}}); err != nil {
		t.Errorf("NULLs refused: %v", err)
	}
	err = w.Write(row{strValue("views"), dateValue(1), timestampValue(5)})
	if err == nil || !strings.Contains(err.Error(), "column n") {
		t.Errorf("a TIMESTAMP in a BIGINT column: %v", err)
	}
	if err := w.Write(row{strValue("views"), dateValue(1), intValue(1)}); err == nil {
		t.Error("the error is not sticky")
	}
	if err := w.Close(); err == nil || exists(p) || exists(p+".tmp") {
		t.Errorf("Close = %v; file %t, .tmp %t", err, exists(p), exists(p+".tmp"))
	}
	for i, r := range []row{
		{intValue(1), value{}, value{}},
		{value{}, intValue(1), value{}},
		{value{}, value{}, floatValue(1)},
		{value{}, value{}, strValue("1")},
	} {
		w, err := newRowWriter(p, statsCols, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Write(r); err == nil {
			t.Errorf("%d: a value of the wrong kind is accepted", i)
		}
		w.Abort()
	}
}

// wideCols is a dumped day's columns with attrs attribute keys: 40 fixed
// ones (an id, family, day, ts, project_id and 35 low-cardinality
// strings), then attr_k0 … attr_k<attrs-1>.
func wideCols(attrs int) []pqColumn {
	cols := []pqColumn{{"id", colVarchar}, {"family", colVarchar}, {"day", colDate}, {"ts", colTimestamp}, {"project_id", colBigint}}
	for i := 5; i < 40; i++ {
		cols = append(cols, pqColumn{fmt.Sprint("f", i), colVarchar})
	}
	for i := range attrs {
		cols = append(cols, pqColumn{fmt.Sprint("attr_k", i), colVarchar})
	}
	return cols
}

// writeWide writes rows rows of wideCols(attrs) to path, each row with 5
// attributes set, spread over the keys, and reports the peak heap in use
// above what it was before, sampled every 256 rows and after Close. every,
// when not 0, cuts a group every that many rows (as a day's families and
// a month's days do). set counts the values each attribute column got, lo
// and hi are attr_k5's least and greatest.
func writeWide(t testing.TB, path string, attrs, rows, every int) (peak uint64, set []int, lo, hi string) {
	cols := wideCols(attrs)
	small := make([]string, 100)
	for i := range small {
		small[i] = fmt.Sprint("x", i)
	}
	set = make([]int, attrs)
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base := ms.HeapInuse
	sample := func() {
		runtime.ReadMemStats(&ms)
		if ms.HeapInuse > base {
			peak = max(peak, ms.HeapInuse-base)
		}
	}
	var cut func(prev, next row, rows int) bool
	if every > 0 {
		cut = func(prev, next row, _ int) bool { return prev[4].int()/int64(every) != next[4].int()/int64(every) }
	}
	w, err := newRowWriter(path, cols, cut)
	if err != nil {
		t.Fatal(err)
	}
	r := make(row, len(cols))
	for i := range rows {
		clear(r)
		r[0] = strValue(fmt.Sprintf("%032x", i*7919))
		r[1] = strValue("views")
		r[2] = dateValue(20000)
		r[3] = timestampValue(int64(i) * 1000)
		r[4] = intValue(int64(i))
		for j := 5; j < 40; j++ {
			r[j] = strValue(small[(i+j)%30])
		}
		for k := range min(5, attrs) {
			a := (i*5 + k*7919) % attrs
			if !r[40+a].ok {
				set[a]++
			}
			v := small[i%100]
			if a == 5 && attrs > 5 {
				if lo == "" || v < lo {
					lo = v
				}
				hi = max(hi, v)
			}
			r[40+a] = strValue(v)
		}
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
		if i%256 == 0 {
			sample()
		}
	}
	sample()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	sample()
	return peak, set, lo, hi
}

// TestWideFileMatchesNarrow: the same rows written by row and by column
// (byColumnCols lowered) read back the same through DuckDB, with the same
// types, row groups and statistics: every type, NULLs, a long string, an
// all-NULL group, a dictionary that falls back to PLAIN, groups cut by
// the cut rule and by byColumnBytes; and an empty file.
func TestWideFileMatchesNarrow(t *testing.T) {
	f := newFixture(t)
	defer SetDictMaxBytes(1 << 10)()
	dir := filepath.Join(f.log.Dir(), "w")
	os.MkdirAll(dir, 0o750)
	cols := append([]pqColumn{{"family", colVarchar}}, allTypes...)
	long := strings.Repeat("y", 4000)
	var rows []row
	for i := range 3000 {
		fam := strValue("product")
		if i >= 2000 {
			fam = strValue("views")
		}
		v := strValue(fmt.Sprintf("path-%05d", i))
		switch {
		case i == 1999:
			v = strValue(long)
		case i >= 1500 && i < 2000:
			v = value{}
		}
		r := row{fam, v, intValue(int64(i - 1000)), floatValue(float64(i) / 8), dateValue(int32(20000 + i/1000)), timestampValue(int64(i) * 1_000_003)}
		if i%5 == 0 {
			r[2], r[3], r[4], r[5] = value{}, value{}, value{}, value{}
		}
		rows = append(rows, r)
	}
	byFamily := func(prev, next row, _ int) bool { return prev[0] != next[0] }
	narrow, wide := filepath.Join(dir, "narrow.parquet"), filepath.Join(dir, "wide.parquet")
	writeRows(t, narrow, cols, byFamily, rows...)
	func() {
		defer setByColumn(0, 32<<10)()
		writeRows(t, wide, cols, byFamily, rows...)
	}()
	ask := func(sql, p string) []string {
		return f.values(t, strings.ReplaceAll(sql, "$f", "read_parquet("+quote(p)+")"))
	}
	for _, q := range []string{
		"SELECT column_name, column_type FROM (DESCRIBE SELECT * FROM $f)",
		"SELECT family, v, b, d, dt::VARCHAR, epoch_us(ts) FROM $f",
		"SELECT count(*), count(v), min(v), length(max(v)), sum(b), sum(d), min(dt)::VARCHAR, max(ts)::VARCHAR FROM $f",
	} {
		if got, want := ask(q, wide), ask(q, narrow); !eq(got, want) {
			t.Errorf("%s: by column %.300v, by row %.300v", q, got, want)
		}
	}
	// The groups: by row, cut by family (2000, 1000); by column, also each
	// time 32 KiB of values are held, so the product rows take several.
	if got := fmt.Sprint(groupRows(t, narrow)); got != "[2000 1000]" {
		t.Errorf("by-row groups %s", got)
	}
	g := groupRows(t, wide)
	var sum int64
	product := -1
	for i, n := range g {
		if sum += n; sum == 2000 {
			product = i + 1
		}
	}
	if sum != 3000 || product < 3 || product == len(g) {
		t.Errorf("by-column groups %v: the product rows in %d of them", g, product)
	}
	// Each column's statistics over the file: nulls summed, bounds folded.
	stats := func(p string) []string {
		pf := openMeta(t, p)
		var out []string
		for c := range cols {
			var nulls int64
			var lo, hi []byte
			for _, rg := range pf.Metadata().RowGroups {
				st := rg.Columns[c].MetaData.Statistics
				nulls += st.NullCount
				if st.MinValue == nil {
					continue
				}
				if lo == nil || cmpStat(cols[c].Type, st.MinValue, lo) < 0 {
					lo = st.MinValue
				}
				if hi == nil || cmpStat(cols[c].Type, st.MaxValue, hi) > 0 {
					hi = st.MaxValue
				}
			}
			out = append(out, fmt.Sprintf("%s nulls=%d min=%.20x max=%.20x/%d", cols[c].Name, nulls, lo, hi, len(hi)))
		}
		return out
	}
	if got, want := stats(wide), stats(narrow); !eq(got, want) {
		t.Errorf("statistics by column %v, by row %v", got, want)
	}
	if md := openMeta(t, wide).Metadata(); len(md.ColumnOrders) != len(cols) {
		t.Errorf("%d column orders", len(md.ColumnOrders))
	}

	empty := filepath.Join(dir, "empty.parquet")
	func() {
		defer setByColumn(0, byColumnBytes)()
		writeRows(t, empty, cols, nil)
	}()
	if got, want := ask("SELECT column_name, column_type FROM (DESCRIBE SELECT * FROM $f)", empty), ask("SELECT column_name, column_type FROM (DESCRIBE SELECT * FROM $f)", narrow); !eq(got, want) {
		t.Errorf("empty by column: %v", got)
	}
	if got := ask("SELECT count(*) FROM $f", empty); !eq(got, []string{"[0]"}) || len(groupRows(t, empty)) != 0 {
		t.Errorf("empty by column holds %v", got)
	}
}

// footerDiff lists where a and b differ, by field path.
func footerDiff(path string, a, b reflect.Value) []string {
	if reflect.DeepEqual(a.Interface(), b.Interface()) {
		return nil
	}
	switch a.Kind() {
	case reflect.Struct:
		var out []string
		for i := range a.NumField() {
			if a.Type().Field(i).IsExported() {
				out = append(out, footerDiff(path+"."+a.Type().Field(i).Name, a.Field(i), b.Field(i))...)
			}
		}
		return out
	case reflect.Slice:
		if a.Len() == b.Len() && a.IsNil() == b.IsNil() {
			var out []string
			for i := range a.Len() {
				out = append(out, footerDiff(fmt.Sprintf("%s[%d]", path, i), a.Index(i), b.Index(i))...)
			}
			return out
		}
	}
	return []string{fmt.Sprintf("%s: by row %v, by column %v", path, a.Interface(), b.Interface())}
}

// cmpStat compares two statistics bounds of a column of type t.
func cmpStat(t colType, a, b []byte) int {
	switch t {
	case colVarchar:
		return strings.Compare(string(a), string(b))
	case colDouble:
		return cmpNum(math.Float64frombits(binary.LittleEndian.Uint64(a)), math.Float64frombits(binary.LittleEndian.Uint64(b)))
	case colDate:
		return cmpNum(int32(binary.LittleEndian.Uint32(a)), int32(binary.LittleEndian.Uint32(b)))
	}
	return cmpNum(int64(binary.LittleEndian.Uint64(a)), int64(binary.LittleEndian.Uint64(b)))
}

func cmpNum[T int32 | int64 | float64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// setByColumn sets byColumnCols and byColumnBytes; the returned func
// restores them.
func setByColumn(cols, held int) func() {
	oldCols, oldHeld := byColumnCols, byColumnBytes
	byColumnCols, byColumnBytes = cols, held
	return func() { byColumnCols, byColumnBytes = oldCols, oldHeld }
}

// TestWideFooterMatchesNarrow: the footers of the same rows written by row
// and by column decode equal but for where each chunk sits and the page
// index the by-row file has, a chunk all NULL in one group among them.
func TestWideFooterMatchesNarrow(t *testing.T) {
	dir := t.TempDir()
	cols := append([]pqColumn{{"family", colVarchar}, {"nulls", colVarchar}, {"nullb", colBigint}}, allTypes...)
	var rows []row
	for i := range 3000 {
		fam := strValue("product")
		if i >= 2000 {
			fam = strValue("views")
		}
		r := row{fam, {}, {}, {}, {}, {}, {}, {}}
		if i < 2000 { // v all NULL in the second group, b in the first
			r[3] = strValue(fmt.Sprint("p", i))
		} else {
			r[4], r[5] = intValue(int64(i)), floatValue(float64(i))
		}
		r[6], r[7] = dateValue(int32(i/100)), timestampValue(int64(i))
		rows = append(rows, r)
	}
	// By family and every 90 rows: 34 groups, so the list's header takes
	// its long form (past 14 elements).
	cut := func(prev, next row, _ int) bool {
		return prev[0] != next[0] || prev[7].timestamp()/90 != next[7].timestamp()/90
	}
	narrow, wide := filepath.Join(dir, "narrow.parquet"), filepath.Join(dir, "wide.parquet")
	writeRows(t, narrow, cols, cut, rows...)
	func() {
		defer setByColumn(0, byColumnBytes)()
		writeRows(t, wide, cols, cut, rows...)
	}()
	footer := func(p string) *format.FileMetaData {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		md, err := footerOf(b)
		if err != nil {
			t.Fatal(err)
		}
		for i := range md.RowGroups {
			rg := &md.RowGroups[i]
			rg.FileOffset = 0
			for j := range rg.Columns {
				c := &rg.Columns[j]
				c.FileOffset, c.MetaData.DataPageOffset, c.MetaData.DictionaryPageOffset = 0, 0, 0
				c.OffsetIndexOffset, c.OffsetIndexLength, c.ColumnIndexOffset, c.ColumnIndexLength = 0, 0, 0, 0
			}
		}
		return md
	}
	n, w := footer(narrow), footer(wide)
	if !reflect.DeepEqual(n, w) {
		for _, d := range footerDiff("", reflect.ValueOf(n).Elem(), reflect.ValueOf(w).Elem()) {
			t.Errorf("footers differ at %s", d)
		}
	}
	if len(w.RowGroups) < 30 {
		t.Errorf("%d row groups, want 30 or more", len(w.RowGroups))
	}
	// v is all NULL in the views groups, b in the product ones.
	for i, rg := range w.RowGroups {
		null := 4
		if string(rg.Columns[0].MetaData.Statistics.MinValue) == "views" {
			null = 3
		}
		if st := rg.Columns[null].MetaData.Statistics; st.NullCount != rg.NumRows || st.MinValue != nil {
			t.Errorf("group %d: column %s has %d nulls of %d rows", i, cols[null].Name, st.NullCount, rg.NumRows)
		}
	}
}

// TestWideFileRefusesTooManyRowGroups: past parquet-go's limit of row
// groups, the by-column writer refuses as parquet-go does.
func TestWideFileRefusesTooManyRowGroups(t *testing.T) {
	f := newWideFile(bufio.NewWriter(io.Discard), statsCols)
	f.rowGroups = make([][]byte, parquet.MaxRowGroups)
	f.add(0, row{strValue("views"), dateValue(1), intValue(1)})
	if err := f.writeGroup(1); !errors.Is(err, parquet.ErrTooManyRowGroups) {
		t.Errorf("writeGroup = %v", err)
	}
	if err := f.writeGroup(1); !errors.Is(err, parquet.ErrTooManyRowGroups) {
		t.Errorf("a second writeGroup = %v", err)
	}
}

// TestWriterBoundsWideFiles: a day of 2,000 columns, nearly all sparse
// attributes, is written by column in under 150 MB of heap (about 310 MB
// by row), in 1 group and in 100, and DuckDB reads it back whole, with
// exact statistics.
func TestWriterBoundsWideFiles(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.log.Dir(), "w")
	os.MkdirAll(dir, 0o750)
	for _, every := range []int{0, 200} {
		t.Run(fmt.Sprint("every ", every), func(t *testing.T) { checkWide(t, f, filepath.Join(dir, fmt.Sprint("wide", every, ".parquet")), every) })
	}
}

func checkWide(t *testing.T, f fixture, p string, every int) {
	const attrs, rows = 1960, 20000
	peak, set, lo5, hi5 := writeWide(t, p, attrs, rows, every)
	st, _ := os.Stat(p)
	groups := len(groupRows(t, p))
	t.Logf("peak heap %.1f MB, %d groups, file %.2f MB", float64(peak)/1e6, groups, float64(st.Size())/1e6)
	if peak > 150e6 {
		t.Errorf("peak heap %.1f MB, want under 150 MB", float64(peak)/1e6)
	}
	if want := max(1, rows/max(every, rows)); every > 0 && groups != rows/every || every == 0 && groups != want {
		t.Errorf("%d groups", groups)
	}
	got := f.values(t, "SELECT count(*), count(DISTINCT id), count(attr_k0), count(attr_k977), count(attr_k1959), min(attr_k5), max(attr_k5) FROM read_parquet("+quote(p)+")")
	if want := fmt.Sprintf("[%d %d %d %d %d %s %s]", rows, rows, set[0], set[977], set[1959], lo5, hi5); !eq(got, []string{want}) {
		t.Errorf("read back %v, want %s", got, want)
	}
	if got := f.values(t, "SELECT count(*) FROM (DESCRIBE SELECT * FROM read_parquet("+quote(p)+"))"); !eq(got, []string{"[2000]"}) {
		t.Errorf("columns %v", got)
	}
	// attr_k5's values: its null count and bounds in the statistics.
	pf := openMeta(t, p)
	var nulls int64
	lo, hi := "", ""
	for _, rg := range pf.Metadata().RowGroups {
		st := rg.Columns[45].MetaData.Statistics
		nulls += st.NullCount
		if st.MinValue != nil && (lo == "" || string(st.MinValue) < lo) {
			lo = string(st.MinValue)
		}
		hi = max(hi, string(st.MaxValue))
	}
	if nulls != int64(rows-set[5]) || lo != lo5 || hi != hi5 {
		t.Errorf("attr_k5 statistics: %d nulls, %s to %s; want %d, %s to %s", nulls, lo, hi, rows-set[5], lo5, hi5)
	}
}
