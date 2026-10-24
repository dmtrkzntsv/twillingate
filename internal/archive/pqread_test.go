package archive

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
)

// pqTable is a Parquet file read back in Go as the goldens keep a file:
// its columns in file order, each with the type name a DESCRIBE gave it,
// and its rows in file order, each value in the form the query engine
// sent it (golden.Compact of a row is its golden): a VARCHAR as a string,
// a BIGINT or DOUBLE as a json.Number spelled as encoding/json spells
// the int64 or float64 (NaN and the infinities as the text "NaN",
// "Infinity", "-Infinity"), a DATE as YYYY-MM-DD, a TIMESTAMP as RFC 3339
// in UTC with no trailing zeros, NULL as nil.
type pqTable struct {
	names, types []string
	rows         [][]any
}

// pqTypeOf is the type name of a leaf, by its physical and logical type:
// the five a writer of the events files uses. Any other fails t.
func pqTypeOf(t testing.TB, path string, n parquet.Node) string {
	t.Helper()
	lt := "none"
	if l := n.Type().LogicalType(); l != nil {
		lt = l.String()
	}
	switch kind := n.Type().Kind().String(); kind + " " + lt {
	case "BYTE_ARRAY STRING":
		return "VARCHAR"
	case "INT64 INT(64,true)", "INT64 none":
		return "BIGINT"
	case "DOUBLE none":
		return "DOUBLE"
	case "INT32 DATE":
		return "DATE"
	case "INT64 TIMESTAMP(isAdjustedToUTC=false,unit=MICROS)":
		return "TIMESTAMP"
	default:
		t.Fatalf("%s: a leaf of type %s %s", path, kind, lt)
		return ""
	}
}

// readTable reads the Parquet file at path: every column, or only the
// named ones (in file order, the others left out), read chunk by chunk.
func readTable(t testing.TB, path string, names ...string) pqTable {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pf, err := parquet.OpenFile(fh, st.Size())
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var p pqTable
	var leaves []int // the file's leaf of each of p's columns
	for i, col := range pf.Schema().Columns() {
		if len(col) != 1 {
			t.Fatalf("%s: a nested column %v", path, col)
		}
		if len(names) > 0 && !slices.Contains(names, col[0]) {
			continue
		}
		lc, _ := pf.Schema().Lookup(col...)
		p.names = append(p.names, col[0])
		p.types = append(p.types, pqTypeOf(t, path, lc.Node))
		leaves = append(leaves, i)
	}
	for _, n := range names {
		p.col(t, n)
	}
	buf := make([]parquet.Value, 1024)
	for _, rg := range pf.RowGroups() {
		base := len(p.rows)
		for range rg.NumRows() {
			p.rows = append(p.rows, make([]any, len(p.names)))
		}
		chunks := rg.ColumnChunks()
		for c, leaf := range leaves {
			row := base
			pages := chunks[leaf].Pages()
			for {
				pg, err := pages.ReadPage()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("%s: %s: %v", path, p.names[c], err)
				}
				vr := pg.Values()
				for {
					n, err := vr.ReadValues(buf)
					for _, v := range buf[:n] {
						if row >= len(p.rows) {
							t.Fatalf("%s: %s: more values than rows", path, p.names[c])
						}
						p.rows[row][c] = p.decode(t, c, v)
						row++
					}
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Fatalf("%s: %s: %v", path, p.names[c], err)
					}
				}
				parquet.Release(pg)
			}
			pages.Close()
			if row != len(p.rows) {
				t.Fatalf("%s: %s: %d values for %d rows", path, p.names[c], row-base, len(p.rows)-base)
			}
		}
	}
	return p
}

// decode is the value v of column c, as pqTable keeps it.
func (p pqTable) decode(t testing.TB, c int, v parquet.Value) any {
	t.Helper()
	if v.IsNull() {
		return nil
	}
	switch p.types[c] {
	case "VARCHAR":
		return string(v.ByteArray())
	case "BIGINT":
		return json.Number(strconv.FormatInt(v.Int64(), 10))
	case "DOUBLE":
		return doubleValue(t, v.Double())
	case "DATE":
		return time.Unix(int64(v.Int32())*86400, 0).UTC().Format(time.DateOnly)
	case "TIMESTAMP":
		return time.UnixMicro(v.Int64()).UTC().Format(time.RFC3339Nano)
	}
	t.Fatalf("type %s", p.types[c])
	return nil
}

// doubleValue is f as the engine sent a DOUBLE.
func doubleValue(t testing.TB, f float64) any {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return json.Number(b)
}

// describe is the file's columns as a DESCRIBE printed them: name, type,
// nullable ("YES" for every column of a Parquet file).
func (p pqTable) describe() []string {
	out := make([]string, len(p.names))
	for i, n := range p.names {
		out[i] = fmt.Sprint([]any{n, p.types[i], "YES"})
	}
	return out
}

// col is the index of the column named name, failing t when there is none.
func (p pqTable) col(t testing.TB, name string) int {
	t.Helper()
	i := slices.Index(p.names, name)
	if i < 0 {
		t.Fatalf("no column %q in %v", name, p.names)
	}
	return i
}

// pick is the rows' values of the named columns, in that order.
func (p pqTable) pick(t testing.TB, names ...string) [][]any {
	t.Helper()
	idx := make([]int, len(names))
	for i, n := range names {
		idx[i] = p.col(t, n)
	}
	out := make([][]any, len(p.rows))
	for r, row := range p.rows {
		out[r] = make([]any, len(idx))
		for i, j := range idx {
			out[r][i] = row[j]
		}
	}
	return out
}

// without is p with the named columns left out.
func (p pqTable) without(names ...string) pqTable {
	var q pqTable
	var keep []int
	for i, n := range p.names {
		if !slices.Contains(names, n) {
			keep = append(keep, i)
			q.names, q.types = append(q.names, n), append(q.types, p.types[i])
		}
	}
	for _, row := range p.rows {
		r := make([]any, len(keep))
		for i, j := range keep {
			r[i] = row[j]
		}
		q.rows = append(q.rows, r)
	}
	return q
}

// where is p with only the rows keep says yes to.
func (p pqTable) where(keep func(row []any) bool) pqTable {
	q := pqTable{names: p.names, types: p.types}
	for _, r := range p.rows {
		if keep(r) {
			q.rows = append(q.rows, r)
		}
	}
	return q
}

// texts is each row as fmt.Sprint prints it.
func texts(rows [][]any) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = fmt.Sprint(r)
	}
	return out
}

// tsText is a TIMESTAMP value as a cast to VARCHAR printed it:
// "YYYY-MM-DD HH:MM:SS", then the fraction of a second without trailing
// zeros; nil stays nil.
func tsText(t testing.TB, v any) any {
	t.Helper()
	if v == nil {
		return nil
	}
	ts, err := time.Parse(time.RFC3339Nano, v.(string))
	if err != nil {
		t.Fatal(err)
	}
	return ts.UTC().Format("2006-01-02 15:04:05.999999")
}

// epochUS is a TIMESTAMP value as its microseconds since the epoch, a
// json.Number; nil stays nil.
func epochUS(t testing.TB, v any) any {
	t.Helper()
	if v == nil {
		return nil
	}
	ts, err := time.Parse(time.RFC3339Nano, v.(string))
	if err != nil {
		t.Fatal(err)
	}
	return json.Number(strconv.FormatInt(ts.UnixMicro(), 10))
}

// writeTable writes p as path with the Go writer: how a test makes a file
// of chosen columns, rows or order.
func writeTable(t testing.TB, path string, p pqTable) {
	t.Helper()
	cols := make([]pqColumn, len(p.names))
	for i, n := range p.names {
		typ, err := colTypeOf(p.types[i])
		if err != nil {
			t.Fatal(err)
		}
		cols[i] = pqColumn{Name: n, Type: typ}
	}
	w, err := newRowWriter(path, cols, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range p.rows {
		vals := make(row, len(r))
		for i, v := range r {
			vals[i] = valueOf(t, cols[i].Type, v)
		}
		if err := w.Write(vals); err != nil {
			w.Abort()
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// valueOf is v, as pqTable keeps it, as the writer's value of type typ.
func valueOf(t testing.TB, typ colType, v any) value {
	t.Helper()
	if v == nil {
		return value{}
	}
	s := fmt.Sprint(v)
	switch typ {
	case colVarchar:
		return strValue(s)
	case colBigint:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return intValue(n)
	case colDouble:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatal(err)
		}
		return floatValue(f)
	case colDate:
		d, err := time.Parse(time.DateOnly, s)
		if err != nil {
			t.Fatal(err)
		}
		return dateValue(int32(d.Unix() / 86400))
	case colTimestamp:
		ts, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return timestampValue(ts.UnixMicro())
	}
	t.Fatalf("type %d", typ)
	return value{}
}

// sortedTexts is texts, sorted.
func sortedTexts(rows [][]any) []string {
	out := texts(rows)
	slices.Sort(out)
	return out
}

// inOrder fails t unless p's rows, in file order, are non-decreasing in
// the named columns, each compared by its type, NULLs last.
func inOrder(t testing.TB, p pqTable, names ...string) {
	t.Helper()
	types := make([]string, len(names))
	for i, n := range names {
		types[i] = p.types[p.col(t, n)]
	}
	cmpValue := func(typ string, a, b any) int {
		switch {
		case a == nil && b == nil:
			return 0
		case a == nil:
			return 1
		case b == nil:
			return -1
		}
		switch typ {
		case "BIGINT", "DOUBLE":
			x, _ := strconv.ParseFloat(fmt.Sprint(a), 64)
			y, _ := strconv.ParseFloat(fmt.Sprint(b), 64)
			return cmp.Compare(x, y)
		case "TIMESTAMP":
			x, _ := strconv.ParseInt(fmt.Sprint(epochUS(t, a)), 10, 64)
			y, _ := strconv.ParseInt(fmt.Sprint(epochUS(t, b)), 10, 64)
			return cmp.Compare(x, y)
		}
		return strings.Compare(a.(string), b.(string))
	}
	rows := p.pick(t, names...)
	for i := 1; i < len(rows); i++ {
		for j, typ := range types {
			if c := cmpValue(typ, rows[i-1][j], rows[i][j]); c > 0 {
				t.Fatalf("row %d %v after row %d %v: not in %v order", i, rows[i], i-1, rows[i-1], names)
			} else if c < 0 {
				break
			}
		}
	}
}
