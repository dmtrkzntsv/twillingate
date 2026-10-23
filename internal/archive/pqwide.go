package archive

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/encoding/thrift"
	"github.com/parquet-go/parquet-go/format"
)

// byColumnCells bounds the values a by-column group holds, about 36 bytes
// each: past it, the group is cut. A variable so a test can lower it.
var byColumnCells = 1 << 19

// byColumnRows is how many of a column's rows go to parquet-go per call:
// a page is cut only between calls, so it stays near pageBytes.
const byColumnRows = 512

// wideFile writes a file of many columns one column at a time, so its
// memory is its values', not its width's. Each row group's non-NULL values
// are held by column; when the group is cut, each column in turn is
// written as the single column of a small Parquet file by a parquet-go
// writer of its type (the same options as a narrow file's), and that
// file's column chunk, its pages verbatim, is copied into this one, its
// metadata (statistics among it) moved to the chunk's new offset. Close
// writes the footer: the schema, each chunk's metadata, the column orders.
// The files have no page index, which neither DuckDB nor the native
// reader needs.
//
// It holds the open group's values (byColumnCells at most, so a group of
// many values is cut early), one writer per column type with a column
// chunk's pages, and every chunk's metadata until Close, a few hundred
// bytes a column per group. Measured on a day of 65,536 rows, 40 fixed
// columns and 5 attributes a row: 2,000 columns peaked at 81 MB of heap
// (by row, about 310 MB at 20,000 rows), 15,000 at 125 MB (by row, about
// 2 GB); the files are within a few percent of the by-row ones' size.
type wideFile struct {
	out   *bufio.Writer
	off   int64 // bytes written to out
	cols  []pqColumn
	group []colValues // the open group's values, by column
	cells int

	chunk     [colTimestamp + 1]*chunkWriter // by type, made when first used
	rowGroups []format.RowGroup
	numRows   int64
	scratch   []parquet.Value
	err       error
}

// colValues is one column's values in the open group: the rows (in the
// group) that are not NULL, and their values.
type colValues struct {
	rows []int32
	vals []value
}

// chunkWriter writes single-column files of one type into buf.
type chunkWriter struct {
	buf bytes.Buffer
	pw  *parquet.Writer
	// What the type's empty file says: its root and leaf schema elements,
	// the leaf's column order (zero when the file has none), its version
	// and creator.
	root, leaf format.SchemaElement
	order      format.ColumnOrder
	hasOrder   bool
	version    int32
	createdBy  string
}

func newWideFile(out *bufio.Writer, cols []pqColumn) *wideFile {
	return &wideFile{out: out, cols: cols, group: make([]colValues, len(cols))}
}

// full reports whether the open group holds byColumnCells values.
func (f *wideFile) full() bool { return f.cells >= byColumnCells }

// add keeps row i of the open group's non-NULL values.
func (f *wideFile) add(i int, r row) {
	for j, v := range r {
		if v.ok {
			c := &f.group[j]
			c.rows = append(c.rows, int32(i))
			c.vals = append(c.vals, v)
			f.cells++
		}
	}
}

func (f *wideFile) write(b []byte) error {
	if f.off == 0 {
		if _, err := f.out.WriteString("PAR1"); err != nil {
			return err
		}
		f.off = 4
	}
	n, err := f.out.Write(b)
	f.off += int64(n)
	return err
}

// writeGroup writes the open group of n rows, column by column.
func (f *wideFile) writeGroup(n int) error {
	if f.err != nil {
		return f.err
	}
	if n == 0 {
		return nil
	}
	rg := format.RowGroup{Columns: make([]format.ColumnChunk, len(f.cols)), NumRows: int64(n), Ordinal: int16(len(f.rowGroups))}
	for j, col := range f.cols {
		cw, err := f.chunkWriter(col.Type)
		if err != nil {
			return f.fail(err)
		}
		cc, chunk, from, err := f.encode(cw, &f.group[j], n)
		if err != nil {
			return f.fail(fmt.Errorf("column %s: %w", col.Name, err))
		}
		start := max(f.off, 4) // write puts the magic first
		if err := f.write(chunk); err != nil {
			return f.fail(err)
		}
		moveChunk(&cc, start-from, start, col.Name)
		if j == 0 {
			rg.FileOffset = start
		}
		rg.TotalByteSize += cc.MetaData.TotalUncompressedSize
		rg.TotalCompressedSize += cc.MetaData.TotalCompressedSize
		rg.Columns[j] = cc
		c := &f.group[j]
		clear(c.vals) // the strings are not kept past their group
		c.rows, c.vals = c.rows[:0], c.vals[:0]
	}
	f.rowGroups = append(f.rowGroups, rg)
	f.numRows += int64(n)
	f.cells = 0
	return nil
}

func (f *wideFile) fail(err error) error {
	f.err = err
	return err
}

// encode writes column c's n rows as a single-column file and returns its
// column chunk's metadata, its bytes (in cw's buffer, valid until its next
// use) and their offset in that file.
func (f *wideFile) encode(cw *chunkWriter, c *colValues, n int) (format.ColumnChunk, []byte, int64, error) {
	var none format.ColumnChunk
	cw.buf.Reset()
	cw.pw.Reset(&cw.buf)
	col := cw.pw.ColumnWriters()[0]
	k := 0
	for start := 0; start < n; start += byColumnRows {
		vals := f.scratch[:0]
		for r := start; r < min(start+byColumnRows, n); r++ {
			if k < len(c.rows) && int(c.rows[k]) == r {
				vals = append(vals, pqValue(0, c.vals[k]))
				k++
			} else {
				vals = append(vals, parquet.NullValue().Level(0, 0, 0))
			}
		}
		f.scratch = vals
		if _, err := col.WriteRowValues(vals); err != nil {
			return none, nil, 0, err
		}
	}
	if err := cw.pw.Close(); err != nil {
		return none, nil, 0, err
	}
	b := cw.buf.Bytes()
	md, err := footerOf(b)
	if err != nil {
		return none, nil, 0, err
	}
	if len(md.RowGroups) != 1 || len(md.RowGroups[0].Columns) != 1 || md.RowGroups[0].NumRows != int64(n) {
		return none, nil, 0, errors.New("the column's file is not one chunk of its rows")
	}
	cc := md.RowGroups[0].Columns[0]
	m := &cc.MetaData
	from := m.DataPageOffset
	if m.DictionaryPageOffset > 0 && m.DictionaryPageOffset < from {
		from = m.DictionaryPageOffset
	}
	to := from + m.TotalCompressedSize
	if from < 4 || to > int64(len(b)) {
		return none, nil, 0, errors.New("the column's chunk is out of its file")
	}
	// The decoded metadata aliases b, which is reused: what is kept of it
	// is copied.
	st := &m.Statistics
	st.Min, st.Max = bytes.Clone(st.Min), bytes.Clone(st.Max)
	st.MinValue, st.MaxValue = bytes.Clone(st.MinValue), bytes.Clone(st.MaxValue)
	return cc, b[from:to], from, nil
}

// moveChunk moves cc's offsets by delta, to the chunk's place at in this
// file, names its column and drops what pointed into its single-column
// file: its page index, its file path.
func moveChunk(cc *format.ColumnChunk, delta, at int64, name string) {
	m := &cc.MetaData
	m.DataPageOffset += delta
	if m.DictionaryPageOffset > 0 {
		m.DictionaryPageOffset += delta
	}
	if m.IndexPageOffset > 0 {
		m.IndexPageOffset += delta
	}
	m.PathInSchema = []string{name}
	cc.FilePath = ""
	cc.FileOffset = at
	cc.OffsetIndexOffset, cc.OffsetIndexLength = 0, 0
	cc.ColumnIndexOffset, cc.ColumnIndexLength = 0, 0
}

// footerOf decodes a Parquet file's footer.
func footerOf(b []byte) (*format.FileMetaData, error) {
	if len(b) < 12 || string(b[len(b)-4:]) != "PAR1" {
		return nil, errors.New("not a Parquet file")
	}
	n := int(binary.LittleEndian.Uint32(b[len(b)-8:]))
	if n > len(b)-12 {
		return nil, errors.New("a footer longer than its file")
	}
	md := new(format.FileMetaData)
	if err := thrift.Unmarshal(new(thrift.CompactProtocol), b[len(b)-8-n:len(b)-8], md); err != nil {
		return nil, err
	}
	return md, nil
}

// chunkWriter is t's writer, made on first use from the narrow writer's
// schema and options; its empty file gives the schema elements and column
// order the footer uses for every column of t.
func (f *wideFile) chunkWriter(t colType) (*chunkWriter, error) {
	if cw := f.chunk[t]; cw != nil {
		return cw, nil
	}
	schema, err := pqSchema([]pqColumn{{"c", t}})
	if err != nil {
		return nil, err
	}
	cw := &chunkWriter{}
	cw.pw = parquet.NewWriter(&cw.buf, pqOptions(schema)...)
	if err := cw.pw.Close(); err != nil {
		return nil, err
	}
	md, err := footerOf(cw.buf.Bytes())
	if err != nil {
		return nil, err
	}
	if len(md.Schema) != 2 {
		return nil, fmt.Errorf("a %d-element schema for one column", len(md.Schema))
	}
	cw.root, cw.leaf = md.Schema[0], md.Schema[1]
	cw.root.Name, cw.leaf.Name = "", ""
	if len(md.ColumnOrders) == 1 {
		cw.order, cw.hasOrder = md.ColumnOrders[0], true
	}
	cw.version, cw.createdBy = md.Version, strings.Clone(md.CreatedBy)
	f.chunk[t] = cw
	return cw, nil
}

// close writes the open group's end: the footer, its length and the magic.
// A file of no rows has every column in its schema and no row groups.
func (f *wideFile) close() error {
	if f.err != nil {
		return f.err
	}
	md := format.FileMetaData{NumRows: f.numRows, RowGroups: f.rowGroups}
	md.Schema = make([]format.SchemaElement, 1, len(f.cols)+1)
	orders := make([]format.ColumnOrder, 0, len(f.cols))
	for i, col := range f.cols {
		cw, err := f.chunkWriter(col.Type)
		if err != nil {
			return err
		}
		if i == 0 {
			md.Schema[0] = cw.root
			md.Schema[0].Name = "twillingate"
			md.Schema[0].NumChildren = thrift.New(int32(len(f.cols)))
			md.Version, md.CreatedBy = cw.version, cw.createdBy
		}
		el := cw.leaf
		el.Name = col.Name
		md.Schema = append(md.Schema, el)
		if cw.hasOrder {
			orders = append(orders, cw.order)
		}
	}
	if len(orders) == len(f.cols) {
		md.ColumnOrders = orders
	}
	if len(f.cols) == 0 {
		md.Schema[0] = format.SchemaElement{Name: "twillingate", NumChildren: thrift.New(int32(0))}
	}
	footer, err := thrift.Marshal(new(thrift.CompactProtocol), &md)
	if err != nil {
		return err
	}
	var tail [8]byte
	binary.LittleEndian.PutUint32(tail[:4], uint32(len(footer)))
	copy(tail[4:], "PAR1")
	if err := f.write(footer); err != nil {
		return err
	}
	return f.write(tail[:])
}
