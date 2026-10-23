package archive

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/encoding/thrift"
	"github.com/parquet-go/parquet-go/format"
)

// byColumnBytes bounds what a by-column group holds of its values: past
// it, the group is cut. Its arenas can take up to about twice that, as
// slices grow by doubling. A variable so a test can lower it.
var byColumnBytes = 32 << 20

// byColumnRows is how many of a column's rows go to parquet-go per call:
// a page is cut only between calls, so it stays near pageBytes.
const byColumnRows = 512

// wideFile writes a file of many columns one column at a time, so its
// memory is bounded by what it holds of the open group's values rather
// than by its width times its pages. Each row group's non-NULL values are
// copied into an arena per column; when the group is cut, each column in
// turn is written as the single column of a small Parquet file by a
// parquet-go writer of its type (the same options as a narrow file's), and
// that file's column chunk, its pages verbatim, is copied into this one,
// its metadata (statistics among it) moved to the chunk's new offset. A
// row group's metadata is thrift-encoded as soon as it is written, and
// Close writes the footer around those: the schema, the row groups, the
// column orders. The files have no page index, which neither DuckDB nor
// the native reader needs.
//
// It holds the open group's values (byColumnBytes, about twice that in
// capacity at most), one writer per column type with a column chunk's
// pages, and each written group's encoded metadata, about 100 bytes a
// column. Groups are cut as a narrow file's are (maxGroupRows and the
// caller's cut), and by byColumnBytes.
type wideFile struct {
	out   *bufio.Writer
	off   int64 // bytes written to out
	cols  []pqColumn
	group []colValues // the open group's values, by column
	held  int         // bytes in the group's arenas

	chunk     [colTimestamp + 1]*chunkWriter // by type, made when first used
	rowGroups [][]byte                       // each written group's RowGroup, thrift-encoded
	numRows   int64
	scratch   []parquet.Value
	err       error
}

// colValues is one column's non-NULL values in the open group, copied
// into an arena: each is its row's distance from the previous value's
// (the first's from row -1) as a uvarint, then a VARCHAR's length as a
// uvarint and its bytes, a DATE's 4 bytes or another type's 8.
type colValues struct {
	arena []byte
	last  int // the row of the last value, -1 for none
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
	f := &wideFile{out: out, cols: cols, group: make([]colValues, len(cols))}
	for j := range f.group {
		f.group[j].last = -1
	}
	return f
}

// full reports whether the open group holds byColumnBytes of values.
func (f *wideFile) full() bool { return f.held >= byColumnBytes }

// add copies row i of the open group's non-NULL values.
func (f *wideFile) add(i int, r row) {
	for j, v := range r {
		if !v.ok {
			continue
		}
		c := &f.group[j]
		n := len(c.arena)
		c.arena = binary.AppendUvarint(c.arena, uint64(i-c.last))
		c.last = i
		switch v.k {
		case colVarchar:
			c.arena = binary.AppendUvarint(c.arena, uint64(len(v.s)))
			c.arena = append(c.arena, v.s...)
		case colDate:
			c.arena = binary.LittleEndian.AppendUint32(c.arena, uint32(v.n))
		default:
			c.arena = binary.LittleEndian.AppendUint64(c.arena, uint64(v.n))
		}
		f.held += len(c.arena) - n
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
	if len(f.rowGroups) == parquet.MaxRowGroups {
		return f.fail(parquet.ErrTooManyRowGroups)
	}
	// No sorting columns, written as an empty list as parquet-go writes it.
	rg := format.RowGroup{Columns: make([]format.ColumnChunk, len(f.cols)), NumRows: int64(n), Ordinal: int16(len(f.rowGroups)),
		SortingColumns: []format.SortingColumn{}}
	for j, col := range f.cols {
		cw, err := f.chunkWriter(col.Type)
		if err != nil {
			return f.fail(err)
		}
		cc, chunk, from, err := f.encode(cw, col.Type, &f.group[j], n)
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
		c.arena, c.last = c.arena[:0], -1
	}
	b, err := thrift.Marshal(new(thrift.CompactProtocol), &rg)
	if err != nil {
		return f.fail(err)
	}
	f.rowGroups = append(f.rowGroups, b)
	f.numRows += int64(n)
	f.held = 0
	return nil
}

func (f *wideFile) fail(err error) error {
	f.err = err
	return err
}

// encode writes column c's n rows, of type t, as a single-column file and
// returns its column chunk's metadata, its bytes (in cw's buffer, valid
// until its next use) and their offset in that file.
func (f *wideFile) encode(cw *chunkWriter, t colType, c *colValues, n int) (format.ColumnChunk, []byte, int64, error) {
	var none format.ColumnChunk
	cw.buf.Reset()
	cw.pw.Reset(&cw.buf)
	col := cw.pw.ColumnWriters()[0]
	a, pos, next := c.arena, 0, -1 // next: the row of the value at pos
	advance := func() {
		if pos < len(a) {
			d, k := binary.Uvarint(a[pos:])
			pos += k
			next += int(d)
		} else {
			next = n
		}
	}
	advance()
	for start := 0; start < n; start += byColumnRows {
		vals := f.scratch[:0]
		for r := start; r < min(start+byColumnRows, n); r++ {
			if r != next {
				vals = append(vals, parquet.NullValue().Level(0, 0, 0))
				continue
			}
			var v parquet.Value
			switch t {
			case colVarchar:
				l, k := binary.Uvarint(a[pos:])
				pos += k
				v = parquet.ByteArrayValue(a[pos : pos+int(l)])
				pos += int(l)
			case colDate:
				v = parquet.Int32Value(int32(binary.LittleEndian.Uint32(a[pos:])))
				pos += 4
			case colDouble:
				v = parquet.DoubleValue(math.Float64frombits(binary.LittleEndian.Uint64(a[pos:])))
				pos += 8
			default:
				v = parquet.Int64Value(int64(binary.LittleEndian.Uint64(a[pos:])))
				pos += 8
			}
			vals = append(vals, v.Level(0, 1, 0))
			advance()
		}
		f.scratch = vals
		if _, err := col.WriteRowValues(vals); err != nil {
			return none, nil, 0, err
		}
	}
	if next != n {
		return none, nil, 0, errors.New("values past the group's rows")
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

// footerOf decodes a Parquet file's footer from a copy of it, so nothing
// decoded points into b.
func footerOf(b []byte) (*format.FileMetaData, error) {
	if len(b) < 12 || string(b[len(b)-4:]) != "PAR1" {
		return nil, errors.New("not a Parquet file")
	}
	n := int(binary.LittleEndian.Uint32(b[len(b)-8:]))
	if n > len(b)-12 {
		return nil, errors.New("a footer longer than its file")
	}
	md := new(format.FileMetaData)
	if err := thrift.Unmarshal(new(thrift.CompactProtocol), bytes.Clone(b[len(b)-8-n:len(b)-8]), md); err != nil {
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

// footerHead and footerTail are FileMetaData's fields before and after its
// row groups (field 4), which close splices in already encoded.
type footerHead struct {
	Version int32                              `thrift:"1,required"`
	Schema  thrift.Slice[format.SchemaElement] `thrift:"2,required"`
	NumRows int64                              `thrift:"3,required"`
}

type footerTail struct {
	KeyValueMetadata thrift.Slice[format.KeyValue]    `thrift:"5,optional"`
	CreatedBy        string                           `thrift:"6,optional"`
	ColumnOrders     thrift.Slice[format.ColumnOrder] `thrift:"7,optional"`
}

// close writes the footer, its length and the magic. A file of no rows
// has every column in its schema and no row groups.
func (f *wideFile) close() error {
	if f.err != nil {
		return f.err
	}
	head := footerHead{NumRows: f.numRows, Schema: make([]format.SchemaElement, 1, len(f.cols)+1)}
	head.Schema[0] = format.SchemaElement{Name: "twillingate", NumChildren: thrift.New(int32(len(f.cols)))}
	tail := footerTail{KeyValueMetadata: []format.KeyValue{}} // empty, as parquet-go writes it
	orders := make([]format.ColumnOrder, 0, len(f.cols))
	for i, col := range f.cols {
		cw, err := f.chunkWriter(col.Type)
		if err != nil {
			return err
		}
		if i == 0 {
			head.Schema[0] = cw.root
			head.Schema[0].Name = "twillingate"
			head.Schema[0].NumChildren = thrift.New(int32(len(f.cols)))
			head.Version, tail.CreatedBy = cw.version, cw.createdBy
		}
		el := cw.leaf
		el.Name = col.Name
		head.Schema = append(head.Schema, el)
		if cw.hasOrder {
			orders = append(orders, cw.order)
		}
	}
	if len(orders) == len(f.cols) && len(orders) > 0 {
		tail.ColumnOrders = orders
	}
	parts, err := spliceFooter(head, f.rowGroups, tail)
	if err != nil {
		return err
	}
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	for _, p := range parts {
		if err := f.write(p); err != nil {
			return err
		}
	}
	var end [8]byte
	binary.LittleEndian.PutUint32(end[:4], uint32(n))
	copy(end[4:], "PAR1")
	return f.write(end[:])
}

// spliceFooter is the compact-protocol FileMetaData of head, the encoded
// row groups and tail, in parts to write one after another (the row
// groups are not copied into one buffer). A struct's fields are written as deltas of their
// ids, each ending in a stop byte, and a struct's encoding does not depend
// on where it sits: so head's encoding without its stop, field 4's header
// (delta 1, a list), the list's header (of structs) and its elements, then
// tail's encoding with its first field's delta taken from 4, not 0.
func spliceFooter(head footerHead, rowGroups [][]byte, tail footerTail) ([][]byte, error) {
	p := new(thrift.CompactProtocol)
	h, err := thrift.Marshal(p, &head)
	if err != nil {
		return nil, err
	}
	t, err := thrift.Marshal(p, &tail)
	if err != nil {
		return nil, err
	}
	if len(h) == 0 || h[len(h)-1] != 0 || len(t) == 0 {
		return nil, errors.New("an unexpected footer encoding")
	}
	const list, structType = 0x9, 0xc
	out := append(h[:len(h)-1:len(h)-1], 1<<4|list)
	if n := len(rowGroups); n < 15 {
		out = append(out, byte(n)<<4|structType)
	} else {
		out = binary.AppendUvarint(append(out, 0xf0|structType), uint64(n))
	}
	if id := int(t[0] >> 4); t[0] != 0 {
		if id <= 4 {
			return nil, errors.New("an unexpected footer tail")
		}
		t[0] = byte(id-4)<<4 | t[0]&0xf
	}
	parts := make([][]byte, 0, len(rowGroups)+2)
	parts = append(parts, out)
	parts = append(parts, rowGroups...)
	return append(parts, t), nil
}
