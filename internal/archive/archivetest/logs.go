//go:build measure

package archivetest

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
)

// WriteLogs writes the events of the Parquet file month as one log a day,
// pdir/<day>.log, shaped as ingest writes a line (archive.Log): empty
// values left out, ts and received_at as 2006-01-02T15:04:05Z, a day as
// 2006-01-02, v 1. It returns the number of lines. For the measurements:
// a month written by the generator (through DuckDB) becomes logs the daily
// pass dumps, merges and derives in Go.
func WriteLogs(t testing.TB, month, pdir string) int {
	t.Helper()
	f, err := os.Open(month)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pf, err := parquet.OpenFile(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	type leaf struct {
		name string
		kind string // varchar, bigint, double, date, timestamp
	}
	var leaves []leaf
	for _, path := range pf.Schema().Columns() {
		col, _ := pf.Schema().Lookup(path...)
		typ := col.Node.Type()
		var lt format.LogicalTypeValue
		if l := typ.LogicalType(); l != nil {
			lt = l.Value
		}
		kind := ""
		switch typ.Kind() {
		case parquet.ByteArray:
			kind = "varchar"
		case parquet.Int32:
			if _, ok := lt.(*format.DateType); ok {
				kind = "date"
			}
		case parquet.Int64:
			kind = "bigint"
			if ts, ok := lt.(*format.TimestampType); ok {
				if _, micros := ts.Unit.Value.(*format.MicroSeconds); !micros || ts.IsAdjustedToUTC {
					t.Fatalf("%s: column %v is %v", month, path, typ)
				}
				kind = "timestamp"
			}
		case parquet.Double:
			kind = "double"
		}
		if kind == "" {
			t.Fatalf("%s: column %v is %v", month, path, typ)
		}
		leaves = append(leaves, leaf{path[0], kind})
	}
	files := map[string]*os.File{}
	defer func() {
		for _, o := range files {
			o.Close()
		}
	}()
	epoch := time.Unix(0, 0).UTC()
	n := 0
	buf := make([]parquet.Row, 1024)
	for _, rg := range pf.RowGroups() {
		rows := rg.Rows()
		for {
			k, err := rows.ReadRows(buf)
			for _, r := range buf[:k] {
				m := map[string]any{"v": 1}
				for _, v := range r {
					if v.IsNull() {
						continue
					}
					l := leaves[v.Column()]
					switch l.kind {
					case "varchar":
						if s := v.String(); s != "" {
							m[l.name] = s
						}
					case "bigint":
						m[l.name] = v.Int64()
					case "double":
						m[l.name] = v.Double()
					case "date":
						m[l.name] = epoch.AddDate(0, 0, int(v.Int32())).Format("2006-01-02")
					case "timestamp":
						m[l.name] = time.UnixMicro(v.Int64()).UTC().Format("2006-01-02T15:04:05Z")
					}
				}
				day, _ := m["day"].(string)
				if day == "" {
					t.Fatalf("%s: a row without a day", month)
				}
				o := files[day]
				if o == nil {
					var oerr error
					if o, oerr = os.OpenFile(filepath.Join(pdir, day+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640); oerr != nil {
						t.Fatal(oerr)
					}
					files[day] = o
				}
				b, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := o.Write(append(b, '\n')); err != nil {
					t.Fatal(err)
				}
				n++
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		rows.Close()
	}
	for day, o := range files {
		if err := o.Close(); err != nil {
			t.Fatal(err)
		}
		delete(files, day)
	}
	return n
}
