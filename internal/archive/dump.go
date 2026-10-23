package archive

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// dumpLog writes the log at src (a claimed log, complete: claim cut any
// torn last line) as the Parquet file out, without the engine. The
// columns are columnsOf of every key of its lines: every fixed
// column in fields order, typed as fields says, then every other key as
// VARCHAR. Each line is decoded and cast as DuckDB's json_extract_string
// and CAST read it (decode.go); a line that is not a JSON object, or a
// value its column's type cannot hold, fails the dump and writes nothing.
//
// A repeated id keeps the line appended first, as SQLite keeps the first
// row it receives: a repeat need not be the same event (the server does
// not check), so a later one can differ in received_at, actor_id, country
// or attributes. Every line without an id counts as one id. The rows are
// then stably sorted by family, then ts, NULLs last (ties keep file
// order), and a row group is cut where the family or the day changes, or
// after maxGroupRows rows.
//
// The log is read once: a key's type does not depend on where its column
// lands (a fixed key has its own, every other is VARCHAR), so each kept
// line becomes a row indexed by the order its keys were first seen, and
// the rows are put in column order as they are written. Memory: the day's
// kept lines as rows (a value per key, 32 bytes, plus the strings); each
// line's parsed map is dropped once its row is built. A day is at most a
// few tens of thousands of lines.
func dumpLog(src, out string) error {
	d := newLogDump()
	if err := d.read(src); err != nil {
		return err
	}
	cols := columnsOf(d.keySet())
	pq := make([]pqColumn, len(cols))
	from := make([]int, len(cols)) // column i's place in d's rows
	for i, c := range cols {
		k := d.index[c.key]
		pq[i] = pqColumn{Name: c.name, Type: d.types[k]}
		from[i] = k
	}
	// d indexes the fields first, in order, as columnsOf lists them.
	fam, ts, day := fieldIndex("family"), fieldIndex("ts"), fieldIndex("day")
	slices.SortStableFunc(d.rows, func(x, y row) int {
		if c := nullsLast(x[fam], y[fam], func(a, b value) int { return strings.Compare(a.s, b.s) }); c != 0 {
			return c
		}
		return nullsLast(x[ts], y[ts], func(a, b value) int { return cmp.Compare(a.n, b.n) })
	})
	w, err := newRowWriter(out, pq, func(prev, next row) bool { return prev[fam] != next[fam] || prev[day] != next[day] })
	if err != nil {
		return err
	}
	buf := make(row, len(cols))
	for _, r := range d.rows {
		for i, k := range from {
			buf[i] = value{}
			if k < len(r) {
				buf[i] = r[k]
			}
		}
		if err := w.Write(buf); err != nil {
			w.Abort()
			return err
		}
	}
	return w.Close()
}

// logDump is a log being read for its dump: every key seen, in the order
// first seen (the fields first), with its type, and the kept lines as rows
// over those keys; a row is as long as the keys were when its line was
// read.
type logDump struct {
	index map[string]int
	keys  []string
	types []colType
	rows  []row
	first FirstLines
}

func newLogDump() *logDump {
	d := &logDump{index: map[string]int{}}
	for _, f := range fields {
		t, err := colTypeOf(f.typ)
		if err != nil {
			panic("archive: field " + f.name + ": " + err.Error())
		}
		d.add(f.name, t)
	}
	return d
}

func (d *logDump) add(key string, t colType) int {
	d.index[key] = len(d.keys)
	d.keys = append(d.keys, key)
	d.types = append(d.types, t)
	return len(d.keys) - 1
}

// keySet is the keys seen that are no field, with the fields: columnsOf's
// input.
func (d *logDump) keySet() map[string]bool {
	set := make(map[string]bool, len(d.keys))
	for _, k := range d.keys[len(fields):] {
		set[k] = true
	}
	return set
}

// read reads every line of the log at src. Blank lines are skipped, as
// DuckDB's newline-delimited reader skips them.
func (d *logDump) read(src string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	defer f.Close()
	name := filepath.Base(src)
	var buf []byte
	br := bufio.NewReaderSize(f, 1<<20)
	for lineNo := 1; ; lineNo++ {
		line, rerr := ReadLine(br, &buf)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return fmt.Errorf("archive: %s: %w", name, rerr)
		}
		if len(bytes.TrimSpace(line)) > 0 {
			if err := d.line(line); err != nil {
				return fmt.Errorf("archive: %s line %d: %w", name, lineNo, err)
			}
		}
		if rerr != nil {
			return nil
		}
	}
}

// line notes line's keys and, when it is the first of its id, keeps it as
// a row.
func (d *logDump) line(line []byte) error {
	m, object := DecodeLine(line)
	if !object {
		return errors.New("not a JSON object")
	}
	for k := range m {
		if _, seen := d.index[k]; !seen {
			d.add(k, colVarchar)
		}
	}
	id, ok, err := JSONText(m["id"])
	if err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if !d.first.Keep(id, ok) {
		return nil
	}
	r := make(row, len(d.keys))
	for k, raw := range m {
		i := d.index[k]
		v, err := castValue(raw, d.types[i])
		if err != nil {
			return fmt.Errorf("%s: %w", visible(k), err)
		}
		r[i] = v
	}
	d.rows = append(d.rows, r)
	return nil
}

// castValue is raw as json_extract_string gives it, then CAST to t; a
// JSON null is NULL.
func castValue(raw json.RawMessage, t colType) (value, error) {
	s, ok, err := JSONText(raw)
	if err != nil || !ok {
		return value{}, err
	}
	switch t {
	case colVarchar:
		return strValue(s), nil
	case colBigint:
		n, err := ParseInt(s)
		return intValue(n), err
	case colDouble:
		f, err := strconv.ParseFloat(s, 64)
		return floatValue(f), err
	case colDate:
		d, err := ParseDay(s)
		return dateValue(int32(d)), err
	case colTimestamp:
		us, err := ParseTime(s)
		return timestampValue(us), err
	}
	return value{}, fmt.Errorf("unknown type %d", t)
}

// colTypeOf is a DuckDB type name of fields as the writer's colType.
func colTypeOf(typ string) (colType, error) {
	switch typ {
	case "VARCHAR":
		return colVarchar, nil
	case "BIGINT":
		return colBigint, nil
	case "DOUBLE":
		return colDouble, nil
	case "DATE":
		return colDate, nil
	case "TIMESTAMP":
		return colTimestamp, nil
	}
	return 0, fmt.Errorf("unknown type %s", typ)
}

// fieldIndex is the position of the fixed column name in fields, which is
// its column in every dump.
func fieldIndex(name string) int {
	for i, f := range fields {
		if f.name == name {
			return i
		}
	}
	panic("archive: no field " + name)
}

// nullsLast orders two values with by, a NULL after every value.
func nullsLast(x, y value, by func(a, b value) int) int {
	switch {
	case x.ok && y.ok:
		return by(x, y)
	case x.ok:
		return -1
	case y.ok:
		return 1
	}
	return 0
}
