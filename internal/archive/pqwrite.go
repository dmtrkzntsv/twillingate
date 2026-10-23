package archive

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"unsafe"

	"github.com/parquet-go/parquet-go"
)

// colType is a column's DuckDB type, as the Parquet files spell it: the
// writer gives each the physical and logical type DuckDB's COPY does, so
// DuckDB's DESCRIBE of a file written here equals its DESCRIBE of one it
// wrote itself.
type colType uint8

const (
	colVarchar   colType = iota // BYTE_ARRAY, String logical type, dictionary encoded
	colBigint                   // INT64
	colDouble                   // DOUBLE
	colDate                     // INT32, DATE logical type: days since 1970-01-01
	colTimestamp                // INT64, TIMESTAMP(isAdjustedToUTC=false, MICROS)
)

// pqColumn is one column of a file, in the order the file lists it.
type pqColumn struct {
	Name string
	Type colType
}

// value is one cell. k is its kind, which must be its column's type (Write
// refuses another, a NULL excepted) and fits in what would be padding: s holds a VARCHAR, n a BIGINT, a DATE's days since
// the epoch, a TIMESTAMP's unix microseconds or a DOUBLE's bits
// (math.Float64bits); ok is false for NULL, so the zero value is NULL. It
// is the native batch's vectors (Dict/Idx strings, Ints, Floats, Times)
// one row at a time, in 32 bytes and no pointer but the string's: building
// a row allocates nothing, and a row read from Parquet or decoded from a
// log line fills one in place.
type value struct {
	s  string
	n  int64
	ok bool
	k  colType
}

// row is one row's cells, in its file's column order.
type row []value

func strValue(s string) value       { return value{s: s, ok: true, k: colVarchar} }
func intValue(n int64) value        { return value{n: n, ok: true, k: colBigint} }
func floatValue(f float64) value    { return value{n: int64(math.Float64bits(f)), ok: true, k: colDouble} }
func dateValue(days int32) value    { return value{n: int64(days), ok: true, k: colDate} }
func timestampValue(us int64) value { return value{n: us, ok: true, k: colTimestamp} }
func (v value) null() bool          { return !v.ok }
func (v value) str() string         { return v.s }
func (v value) int() int64          { return v.n }
func (v value) float() float64      { return math.Float64frombits(uint64(v.n)) }
func (v value) date() int32         { return int32(v.n) }
func (v value) timestamp() int64    { return v.n }

// maxGroupRows bounds a row group: a group is cut after this many rows
// whatever cut says.
const maxGroupRows = 65536

// dictMaxBytes bounds each VARCHAR column's dictionary in a row group:
// past it, the column's later pages in that group are PLAIN, as DuckDB's
// are past its own limit. Without it, a group of distinct strings (ids,
// paths) holds a dictionary entry per value, about 100 bytes each: 140 MB
// for 21 such columns of 65,536 rows. A variable so a test can lower it.
var dictMaxBytes int64 = 128 << 10

// pageBytes is parquet-go's page buffer per column (its default is 256
// KiB). A page is where it checks dictMaxBytes, so a smaller page brings
// the switch to PLAIN closer to the bound: at 256 KiB a dictionary index
// page spans a whole group and the switch never comes.
const pageBytes = 32 << 10

// pendingRows is how many rows Write converts before handing them to
// parquet-go at once (a call per row costs more than the rows).
const pendingRows = 256

// byColumnCols is the width past which a file is written by column
// (pqwide.go): parquet-go keeps buffers for every column of the open group,
// at least about 20 KB a column however few values it holds and about 150
// KB once its pages fill, so a day log with 15,000 attribute keys took
// about 2 GB. A file of up to byColumnCols columns, a typical one, is
// written by row as it always was. A variable so a test can change it.
var byColumnCols = 256

// rowWriter writes one Parquet file row by row, cutting a row group when
// cut(prev, next) says so or after maxGroupRows rows, and finishing the
// file atomically on Close: rows go to path.tmp, which Close makes 0640,
// fsyncs, renames over path and whose directory it fsyncs, so a reader
// sees the old file or the whole new one and a crash leaves at most a
// .tmp. A writer that fails, or is not to be kept, is Aborted.
//
// A file of more than byColumnCols columns is written by column instead
// (wideFile): the same encoding, its memory bounded by its values rather
// than its width.
//
// Memory: parquet-go holds the open row group until it is cut: each
// column's compressed pages, one page being filled per column (pageBytes)
// and, for a VARCHAR column, its dictionary, bounded by dictMaxBytes of
// values (about 100 bytes an entry in all). The group is written out and
// released when cut; of an earlier group only its footer entry is kept.
// Measured on 65,000 rows of 25 columns (21 VARCHARs): about 8 MB of heap
// with low-cardinality strings, about 31 MB with every string distinct, of
// which about 10 MB is the group's compressed pages. Write keeps up to
// pendingRows rows of parquet values, which point at the strings of the
// rows written.
//
// It assumes a single writer per path: two at once share path.tmp.
type rowWriter struct {
	path string
	cols []pqColumn
	cut  func(prev, next row) bool

	f   *os.File
	buf *bufio.Writer
	pw  *parquet.Writer

	prev     row
	havePrev bool
	inGroup  int // rows in the open group, pending ones included

	pending []parquet.Row // converted rows not yet given to pw
	vals    []parquet.Value
	wide    *wideFile // a file of more than byColumnCols columns, else nil
	err     error
	done    bool
}

// newRowWriter starts writing path (into path.tmp) with columns cols, every
// one OPTIONAL, in that order. cut may be nil: groups are then cut by size
// alone.
func newRowWriter(path string, cols []pqColumn, cut func(prev, next row) bool) (*rowWriter, error) {
	wide := len(cols) > byColumnCols
	var schema *parquet.Schema
	err := checkColumns(cols)
	if err == nil && !wide {
		schema, err = pqSchema(cols)
	}
	if err != nil {
		return nil, fmt.Errorf("archive: %s: %w", filepath.Base(path), err)
	}
	tmp := path + ".tmp"
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	w := &rowWriter{path: path, cols: cols, cut: cut, f: f, buf: bufio.NewWriterSize(f, 256<<10),
		prev: make(row, len(cols))}
	if wide {
		w.wide = newWideFile(w.buf, cols)
		return w, nil
	}
	w.pw = parquet.NewWriter(w.buf, pqOptions(schema)...)
	return w, nil
}

// pqOptions are the writer's options for schema, a by-column file's
// chunks' too.
func pqOptions(schema *parquet.Schema) []parquet.WriterOption {
	return []parquet.WriterOption{
		schema,
		parquet.Compression(&parquet.Zstd),
		parquet.DictionaryMaxBytes(dictMaxBytes),
		parquet.PageBufferSize(pageBytes),
		parquet.CreatedBy("twillingate", "", ""),
	}
}

// Write appends r, which must have one value per column, each of its
// column's type. r is not kept: the caller may reuse it.
func (w *rowWriter) Write(r row) error {
	if w.err != nil {
		return w.err
	}
	if w.done {
		return fmt.Errorf("archive: %s: write after close", filepath.Base(w.path))
	}
	if len(r) != len(w.cols) {
		w.err = fmt.Errorf("archive: %s: row of %d values, want %d", filepath.Base(w.path), len(r), len(w.cols))
		return w.err
	}
	for j, v := range r {
		if v.ok && v.k != w.cols[j].Type {
			w.err = fmt.Errorf("archive: %s: column %s: a value of the wrong kind", filepath.Base(w.path), w.cols[j].Name)
			return w.err
		}
	}
	if w.inGroup > 0 && (w.inGroup >= maxGroupRows || (w.wide != nil && w.wide.full()) ||
		(w.cut != nil && w.havePrev && w.cut(w.prev, r))) {
		if err := w.flushGroup(); err != nil {
			return err
		}
	}
	copy(w.prev, r)
	w.havePrev = true
	if w.wide != nil {
		w.wide.add(w.inGroup, r)
		w.inGroup++
		return nil
	}
	w.pending = append(w.pending, w.convert(r))
	w.inGroup++
	if len(w.pending) == pendingRows {
		return w.flushPending()
	}
	return nil
}

// convert is r as parquet values, appended to w.vals: column j's value at
// column index j, definition level 1, or a NULL at level 0.
func (w *rowWriter) convert(r row) parquet.Row {
	start := len(w.vals)
	for j, v := range r {
		w.vals = append(w.vals, pqValue(j, v))
	}
	return parquet.Row(w.vals[start:len(w.vals):len(w.vals)])
}

// pqValue is v as column j's parquet value: definition level 1, or a NULL
// at level 0. A string's bytes are the string's own, which parquet-go
// copies out without writing to them.
func pqValue(j int, v value) parquet.Value {
	switch {
	case !v.ok:
		return parquet.NullValue().Level(0, 0, j)
	case v.k == colVarchar:
		return parquet.ByteArrayValue(unsafe.Slice(unsafe.StringData(v.s), len(v.s))).Level(0, 1, j)
	case v.k == colDate:
		return parquet.Int32Value(int32(v.n)).Level(0, 1, j)
	case v.k == colDouble:
		return parquet.DoubleValue(math.Float64frombits(uint64(v.n))).Level(0, 1, j)
	}
	return parquet.Int64Value(v.n).Level(0, 1, j)
}

func (w *rowWriter) flushPending() error {
	if len(w.pending) == 0 {
		return nil
	}
	if _, err := w.pw.WriteRows(w.pending); err != nil {
		w.err = fmt.Errorf("archive: %s: %w", filepath.Base(w.path), err)
		return w.err
	}
	w.pending = w.pending[:0]
	// w.vals is reused only once pw has copied the values out.
	w.vals = w.vals[:0]
	return nil
}

// flushGroup writes out the open row group.
func (w *rowWriter) flushGroup() error {
	if err := w.flushPending(); err != nil {
		return err
	}
	var err error
	if w.wide != nil {
		err = w.wide.writeGroup(w.inGroup)
	} else {
		err = w.pw.Flush()
	}
	if err != nil {
		w.err = fmt.Errorf("archive: %s: %w", filepath.Base(w.path), err)
		return w.err
	}
	w.inGroup = 0
	return nil
}

// Close finishes the file and puts it in place: on a failure before the
// rename nothing is renamed and the .tmp is removed. A failure to fsync
// the directory after the rename leaves the new file in place, whole: only
// its durability across a crash is in question. A file of no rows has the
// full schema and no row groups.
func (w *rowWriter) Close() error {
	if w.done {
		return w.err
	}
	err := w.finish()
	w.done = true
	if err != nil {
		w.f.Close()
		os.Remove(w.path + ".tmp")
		w.err = err
	}
	return err
}

func (w *rowWriter) finish() error {
	if w.err != nil {
		return w.err
	}
	tmp := w.path + ".tmp"
	if err := w.flushGroup(); err != nil {
		return err
	}
	var err error
	if w.wide != nil {
		err = w.wide.close()
	} else {
		err = w.pw.Close()
	}
	if err != nil {
		return fmt.Errorf("archive: %s: %w", filepath.Base(w.path), err)
	}
	if err := w.buf.Flush(); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	// The mode is set again: OpenFile's is under the process's umask.
	if err := w.f.Chmod(0o640); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	if err := os.Rename(tmp, w.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("archive: %w", err)
	}
	return syncFile(filepath.Dir(w.path))
}

// Abort drops the file being written: the .tmp is removed and path left as
// it was. It is a no-op after Close.
func (w *rowWriter) Abort() {
	if w.done {
		return
	}
	w.done = true
	w.f.Close()
	os.Remove(w.path + ".tmp")
}

// checkColumns refuses a name given twice and an unknown type.
func checkColumns(cols []pqColumn) error {
	seen := make(map[string]bool, len(cols))
	for _, c := range cols {
		if seen[c.Name] {
			return fmt.Errorf("column %s twice", c.Name)
		}
		seen[c.Name] = true
		if c.Type > colTimestamp {
			return fmt.Errorf("column %s: unknown type %d", c.Name, c.Type)
		}
	}
	return nil
}

// pqSchema is cols as a flat Parquet schema of OPTIONAL leaves, in cols'
// order (parquet.Group would sort them by name), each zstd compressed and
// VARCHARs dictionary encoded, as DuckDB writes them.
func pqSchema(cols []pqColumn) (*parquet.Schema, error) {
	fields := make([]parquet.Field, len(cols))
	for i, c := range cols {
		var leaf parquet.Node
		switch c.Type {
		case colVarchar:
			leaf = parquet.Encoded(parquet.String(), &parquet.RLEDictionary)
		case colBigint:
			leaf = parquet.Leaf(parquet.Int64Type)
		case colDouble:
			leaf = parquet.Leaf(parquet.DoubleType)
		case colDate:
			leaf = parquet.Date()
		case colTimestamp:
			leaf = parquet.TimestampAdjusted(parquet.Microsecond, false)
		default:
			return nil, fmt.Errorf("column %s: unknown type %d", c.Name, c.Type)
		}
		fields[i] = pqField{Node: parquet.Compressed(parquet.Optional(leaf), &parquet.Zstd), name: c.Name}
	}
	return parquet.NewSchema("twillingate", pqGroup{Node: parquet.Group{}, fields: fields}), nil
}

// pqGroup is a group node whose fields keep their order.
type pqGroup struct {
	parquet.Node
	fields []parquet.Field
}

func (g pqGroup) Fields() []parquet.Field { return g.fields }

// pqField is a named leaf of a pqGroup. Value is parquet-go's reflection
// hook, which the writer never calls: rows are written as parquet.Row.
type pqField struct {
	parquet.Node
	name string
}

func (f pqField) Name() string                           { return f.name }
func (f pqField) Value(base reflect.Value) reflect.Value { return base }
