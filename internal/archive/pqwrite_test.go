package archive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

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

func writeRows(t *testing.T, path string, cols []pqColumn, cut func(prev, next row) bool, rows ...row) {
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
	if got := describe(goFile); !eq(got, []string{"[v VARCHAR YES]", "[b BIGINT YES]", "[d DOUBLE YES]", "[dt DATE YES]", "[ts TIMESTAMP YES]"}) {
		t.Errorf("DESCRIBE = %v", got)
	}
	read := func(p string) []string {
		return f.values(t, "SELECT v, b, d, dt::VARCHAR, ts::VARCHAR, epoch_us(ts) FROM read_parquet("+quote(p)+")")
	}
	if got, want := read(goFile), read(duckFile); !eq(got, want) {
		t.Errorf("Go file reads %v, DuckDB's %v", got, want)
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
	cut := func(prev, next row) bool { return prev[2].int()/10 != next[2].int()/10 }
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
	byFamily := func(prev, next row) bool { return prev[0] != next[0] }
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
// dictMaxBytes goes on in PLAIN pages, and DuckDB reads both kinds back.
func TestWriterFallsBackToPlain(t *testing.T) {
	f := newFixture(t)
	defer SetDictMaxBytes(1 << 10)()
	dir := filepath.Join(f.log.Dir(), "w")
	os.MkdirAll(dir, 0o750)
	p := filepath.Join(dir, "plain.parquet")
	var rows []row
	for i := range 20000 {
		rows = append(rows, row{strValue(fmt.Sprintf("path-%05d", i)), intValue(int64(i))})
	}
	writeRows(t, p, []pqColumn{{"path", colVarchar}, {"n", colBigint}}, nil, rows...)
	md := openMeta(t, p).Metadata().RowGroups[0].Columns[0].MetaData
	if got := fmt.Sprint(md.Encoding); !strings.Contains(got, "PLAIN") || !strings.Contains(got, "RLE_DICTIONARY") {
		t.Errorf("encodings = %s, want PLAIN and RLE_DICTIONARY pages", got)
	}
	got := f.values(t, "SELECT count(DISTINCT path), min(path), max(path), count(*) FILTER (path = 'path-' || lpad(n::VARCHAR, 5, '0')) FROM read_parquet("+quote(p)+")")
	if !eq(got, []string{"[20000 path-00000 path-19999 20000]"}) {
		t.Errorf("read back %v", got)
	}
}
