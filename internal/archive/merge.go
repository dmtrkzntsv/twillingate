package archive

import (
	"cmp"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"hash/maphash"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/deprecated"
	"github.com/parquet-go/parquet-go/encoding"
	"github.com/parquet-go/parquet-go/format"
)

// mergeFiles writes the Parquet files inputs, given oldest first (a month
// file a crashed merge left, then each day's file and its segments in
// order), as the one file out, without the engine, in two passes over the
// inputs:
//
//  1. Only the id column is read, input by input, row by row, and each
//     row whose id an earlier row had is marked in its input's drop bits:
//     an id keeps its row from the earliest input, then the earliest row.
//     Every row without an id counts as one id.
//  2. The inputs are merged by (family, ts), NULLs last, each read a row
//     group at a time; a tie goes to the earlier input, then the earlier
//     row. Marked rows are skipped. A row group is cut where the family or
//     the day changes, or after maxGroupRows rows (the writer's own bound
//     on a wide file's group comes on top).
//
// The columns are the union of the inputs': matched ignoring case, the
// first spelling and the order of first appearance kept (DuckDB's
// union_by_name), a value of an input that spells its column otherwise
// landing in the first spelling's column; then every fixed column no input
// has, in fields order, all NULL (a reader treats a missing column as NULL
// either way). A fixed column is typed as fields says, every other one is
// VARCHAR; an input whose column has another type fails the merge, naming
// it. So does an input not sorted by (family, ts), found in pass 2: the
// writer is aborted, so no file is left and nothing is renamed or deleted.
//
// Memory: in pass 1, the month's id hashes (128 bits each, from two
// maphash seeds, in a set; collected before pass 2) and one drop bit per
// input row. In pass 2, per input, its footer and the family and ts of
// its current row group; and the other columns of the runs of one family
// rows are being taken from (cursor), only those: the days of a month do
// not overlap in ts, so it is about one run at a time, a few where a
// segment or a crashed merge's month file overlaps a day. A run's values
// are held as the strings it uses and an index per row (VARCHAR) or a
// number per row; a column that is mostly NULL holds only its values and
// their rows. Then the writer's open group (rowWriter). Measured on a
// month of a million events in 32 files: about 85 MB of peak RSS above the
// process's, in about 7 s.
func mergeFiles(ctx context.Context, inputs []string, out string) error {
	m, err := openMerge(inputs)
	if err != nil {
		return err
	}
	defer m.close()
	if err := m.markRepeats(); err != nil {
		return err
	}
	// The id set is garbage now, the largest thing pass 1 held: collected
	// here, pass 2's heap goal follows its own live heap rather than twice
	// the set's.
	runtime.GC()
	return m.write(ctx, out)
}

// monthMerge is a merge's inputs, opened, and the columns of its output.
type monthMerge struct {
	inputs []*mergeInput
	cols   []pqColumn
	kept   int64 // rows pass 1 kept: pass 2 must write as many
}

// mergeInput is one input: its open file, how each of its columns lands
// in the output, and its drop bits.
type mergeInput struct {
	path     string
	f        *os.File
	pf       *parquet.File
	cols     []inColumn
	fam, ts  int // index in cols of family and ts, -1 when missing
	id       int // leaf index of id, -1 when missing
	rows     int64
	drop     []uint64
	repeats  int64   // rows marked in drop
	groupRow []int64 // each row group's first row in the file
}

// inColumn is one column of an input: its leaf, its output column and
// type, and for a TIMESTAMP the factor its values take to microseconds.
type inColumn struct {
	leaf, out int
	typ       colType
	scale     int64
}

func openMerge(inputs []string) (*monthMerge, error) {
	m := &monthMerge{}
	byName := map[string]int{} // lower-case name → output column
	for _, path := range inputs {
		in, err := openInput(path)
		if err != nil {
			m.close()
			return nil, err
		}
		m.inputs = append(m.inputs, in)
		if err := m.addColumns(in, byName); err != nil {
			m.close()
			return nil, err
		}
	}
	for _, f := range fields {
		if _, ok := byName[f.name]; !ok {
			t, _ := colTypeOf(f.typ)
			byName[f.name] = len(m.cols)
			m.cols = append(m.cols, pqColumn{Name: f.name, Type: t})
		}
	}
	return m, nil
}

func openInput(path string) (*mergeInput, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("archive: %w", err)
	}
	pf, err := parquet.OpenFile(f, st.Size(), parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true),
		parquet.ReadBufferSize(256<<10))
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("archive: %s: %w", filepath.Base(path), err)
	}
	in := &mergeInput{path: path, f: f, pf: pf, fam: -1, ts: -1, id: -1, rows: pf.NumRows()}
	var start int64
	for _, rg := range pf.Metadata().RowGroups {
		in.groupRow = append(in.groupRow, start)
		start += rg.NumRows
	}
	// Pass 1 sizes the drop bits by the footer's count and pass 2 walks
	// the groups: the two must agree, or rows past the bits would be
	// misread.
	if start != in.rows {
		f.Close()
		return nil, fmt.Errorf("archive: %s: row groups hold %d rows, footer says %d", filepath.Base(path), start, in.rows)
	}
	in.drop = make([]uint64, (in.rows+63)/64)
	return in, nil
}

// addColumns maps in's columns onto the output's, adding the ones it is
// the first to have, and fails on a column of the wrong type.
func (m *monthMerge) addColumns(in *mergeInput, byName map[string]int) error {
	name := filepath.Base(in.path)
	seen := map[string]bool{}
	for leaf, fl := range in.pf.Schema().Fields() {
		if !fl.Leaf() || fl.Repeated() {
			return fmt.Errorf("archive: %s: column %s is not a flat column", name, visible(fl.Name()))
		}
		lower := strings.ToLower(fl.Name())
		if seen[lower] {
			return fmt.Errorf("archive: %s: two columns named %s", name, visible(fl.Name()))
		}
		seen[lower] = true
		want := colVarchar
		if i := fieldOf(lower); i >= 0 {
			want, _ = colTypeOf(fields[i].typ)
		}
		got, scale, ok := leafType(fl.Type())
		if !ok || got != want {
			return fmt.Errorf("archive: %s: column %s is %s, want %s", name, visible(fl.Name()), fl.Type(), typeNames[want])
		}
		out, ok := byName[lower]
		if !ok {
			out = len(m.cols)
			byName[lower] = out
			m.cols = append(m.cols, pqColumn{Name: fl.Name(), Type: want})
		}
		switch lower {
		case "family":
			in.fam = len(in.cols)
		case "ts":
			in.ts = len(in.cols)
		case "id":
			in.id = leaf
		}
		in.cols = append(in.cols, inColumn{leaf: leaf, out: out, typ: want, scale: scale})
	}
	return nil
}

var typeNames = map[colType]string{colVarchar: "VARCHAR", colBigint: "BIGINT", colDouble: "DOUBLE", colDate: "DATE", colTimestamp: "TIMESTAMP"}

// fieldOf is the index in fields of the fixed column named lower, -1 for
// none.
func fieldOf(lower string) int {
	for i, f := range fields {
		if f.name == lower {
			return i
		}
	}
	return -1
}

// leafType is the DuckDB type a Parquet column reads as, of the ones the
// events files hold, with a TIMESTAMP's factor to microseconds; ok false
// for any other type.
func leafType(t parquet.Type) (typ colType, scale int64, ok bool) {
	var lt format.LogicalTypeValue
	if l := t.LogicalType(); l != nil {
		lt = l.Value
	}
	ct := t.ConvertedType()
	switch t.Kind() {
	case parquet.ByteArray:
		if _, isString := lt.(*format.StringType); isString || (lt == nil && ct != nil && *ct == deprecated.UTF8) {
			return colVarchar, 0, true
		}
	case parquet.Int32:
		if _, isDate := lt.(*format.DateType); isDate {
			return colDate, 0, true
		}
	case parquet.Int64:
		switch l := lt.(type) {
		case nil:
			if ct == nil || *ct == deprecated.Int64 {
				return colBigint, 0, true
			}
		case *format.IntType:
			if l.BitWidth == 64 && l.IsSigned {
				return colBigint, 0, true
			}
		case *format.TimestampType:
			if !l.IsAdjustedToUTC {
				switch l.Unit.Value.(type) {
				case *format.MicroSeconds:
					return colTimestamp, 1, true
				case *format.MilliSeconds:
					return colTimestamp, 1000, true
				}
			}
		}
	case parquet.Double:
		if lt == nil {
			return colDouble, 0, true
		}
	}
	return 0, 0, false
}

func (m *monthMerge) close() {
	for _, in := range m.inputs {
		in.f.Close()
	}
}

// markRepeats is pass 1: it reads the id column of every input, in input
// order, and marks each row whose id an earlier row had.
//
// Ids are compared by a 128-bit hash: two ids that collide would drop the
// later one's row. For n ids the chance of any collision is about
// n²/2¹²⁹, below 1e-20 at the 10⁶–10⁷ ids a month holds.
func (m *monthMerge) markRepeats() error {
	var total int64
	for _, in := range m.inputs {
		total += in.rows
	}
	s1, s2 := maphash.MakeSeed(), maphash.MakeSeed()
	seen := make(map[[2]uint64]struct{}, total)
	nullSeen := false
	for _, in := range m.inputs {
		var row int64
		mark := func(b []byte, ok bool) {
			first := false
			if !ok {
				first, nullSeen = !nullSeen, true
			} else {
				h := [2]uint64{maphash.Bytes(s1, b), maphash.Bytes(s2, b)}
				if _, dup := seen[h]; !dup {
					seen[h] = struct{}{}
					first = true
				}
			}
			if !first {
				in.drop[row/64] |= 1 << (row % 64)
				in.repeats++
			}
			row++
		}
		for g, rg := range in.pf.RowGroups() {
			if in.id < 0 {
				for range rg.NumRows() {
					mark(nil, false)
				}
				continue
			}
			n, err := eachByteArray(rg.ColumnChunks()[in.id], mark)
			if err == nil && int64(n) != rg.NumRows() {
				err = fmt.Errorf("read %d rows, want %d", n, rg.NumRows())
			}
			if err != nil {
				return fmt.Errorf("archive: %s row group %d column id: %w", filepath.Base(in.path), g, err)
			}
		}
		m.kept += in.rows - in.repeats
	}
	return nil
}

// dropped reports whether row (of the file) was marked by pass 1.
func (in *mergeInput) dropped(row int64) bool { return in.drop[row/64]&(1<<(row%64)) != 0 }

// write is pass 2: it merges the inputs into out.
func (m *monthMerge) write(ctx context.Context, out string) error {
	fam, day := m.column("family"), m.column("day")
	w, err := newRowWriter(out, m.cols, func(prev, next row) bool { return prev[fam] != next[fam] || prev[day] != next[day] })
	if err != nil {
		return err
	}
	if err := m.writeRows(ctx, w); err != nil {
		w.Abort()
		return err
	}
	return w.Close()
}

// column is the output column named name (a field, so always there).
func (m *monthMerge) column(name string) int {
	for i, c := range m.cols {
		if strings.EqualFold(c.Name, name) {
			return i
		}
	}
	panic("archive: merge without column " + name)
}

func (m *monthMerge) writeRows(ctx context.Context, w *rowWriter) error {
	h := &cursorHeap{}
	for i, in := range m.inputs {
		c := &cursor{in: in, order: i, g: -1, r: -1}
		if err := c.advance(); err != nil {
			return err
		}
		if !c.done {
			h.items = append(h.items, c)
		}
	}
	heap.Init(h)
	buf := make(row, len(m.cols))
	var set []int // the columns of buf the last row set
	var written int64
	for h.Len() > 0 {
		c := h.items[0]
		if err := c.load(); err != nil {
			return err
		}
		for _, o := range set {
			buf[o] = value{}
		}
		set = c.fill(buf, set[:0])
		if err := w.Write(buf); err != nil {
			return err
		}
		written++
		if written%maxGroupRows == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if err := c.advance(); err != nil {
			return err
		}
		if c.done {
			heap.Pop(h)
		} else {
			heap.Fix(h, 0)
		}
	}
	// Every row pass 1 kept must be written: a kept row pass 2 did not
	// write (a group read short, a row skipped as marked though pass 1
	// counted it) would be dropped without a word. The check catches a
	// drift in the count of rows; it cannot tell a bitset misaligned with
	// the rows it marks, which drops as many rows as it keeps: the parity
	// suite (TestGoMergeMatchesDuckDB) guards that.
	if written != m.kept {
		return fmt.Errorf("archive: merge wrote %d rows, want %d", written, m.kept)
	}
	return nil
}

// cursor reads one input in pass 2: its current row group and row, the
// group's family and ts, and the values of the run of rows of one family
// its row is in, once its rows are taken. A group DuckDB wrote holds a
// whole day, every family; held whole, every input's would be held at
// once, since the merge takes every day's first family before any day's
// next. A run of one family is taken whole before the next input's, the
// days' ts not overlapping, so about one run is held at a time.
type cursor struct {
	in    *mergeInput
	order int // the input's position: ties go to the earlier
	g, r  int // row group, and row within it (-1 before the first)
	n     int // rows in group g
	done  bool

	fam, ts *vector // group g's family and ts, for the order

	// The run held, rows lo to hi of group g (vals nil when none is).
	lo, hi   int
	vals     []*vector // every column, family and ts included
	dense    []int     // the columns held a value per row
	start    []int32   // the mostly-NULL columns' cells of row lo+r are sparse[start[r]:start[r+1]] …
	sparse   []int32
	sparseAt []int32 // … each cell's index into its column's values

	prevFam, prevTS value
	havePrevs       bool
}

// advance moves c to its next row pass 1 kept, reading the next row
// group's keys when the current one is used up and letting go of a run
// it has passed, and checks every row it passes is in (family, ts) order
// after the one before.
func (c *cursor) advance() error {
	for {
		c.r++
		if c.vals != nil && c.r >= c.hi {
			c.vals, c.dense, c.start, c.sparse, c.sparseAt = nil, nil, nil, nil, nil
		}
		for c.r >= c.n {
			c.g++
			if c.g >= len(c.in.groupRow) {
				c.done = true
				c.fam, c.ts = nil, nil
				return nil
			}
			if err := c.readKeys(); err != nil {
				return err
			}
			c.r = 0
		}
		f, t := c.key()
		if c.havePrevs && compareKeys(c.prevFam, c.prevTS, f, t) > 0 {
			return fmt.Errorf("archive: %s: not sorted by family and ts at row %d", filepath.Base(c.in.path), c.in.groupRow[c.g]+int64(c.r))
		}
		c.prevFam, c.prevTS, c.havePrevs = f, t, true
		if !c.in.dropped(c.in.groupRow[c.g] + int64(c.r)) {
			return nil
		}
	}
}

// readKeys reads group g's family and ts.
func (c *cursor) readKeys() error {
	c.n = int(c.in.pf.RowGroups()[c.g].NumRows())
	var err error
	if c.fam, err = c.readColumn(c.in.fam, 0, c.n, false); err != nil {
		return err
	}
	c.ts, err = c.readColumn(c.in.ts, 0, c.n, false)
	return err
}

// readColumn reads rows from to to of column i (of in.cols; -1 for one the
// input lacks, read as nil) of group g, a mostly-NULL column compactly
// when compact.
func (c *cursor) readColumn(i, from, to int, compact bool) (*vector, error) {
	if i < 0 {
		return nil, nil
	}
	ic := c.in.cols[i]
	cc := c.in.pf.RowGroups()[c.g].ColumnChunks()[ic.leaf]
	compact = compact && mostlyNull(&c.in.pf.Metadata().RowGroups[c.g].Columns[ic.leaf], c.n)
	v, err := readVector(cc, ic, c.n, from, to, compact)
	if err != nil {
		return nil, fmt.Errorf("archive: %s row group %d column %s: %w", filepath.Base(c.in.path), c.g, visible(c.in.pf.Schema().Fields()[ic.leaf].Name()), err)
	}
	return v, nil
}

// mostlyNull reports whether the chunk's statistics say at most an eighth
// of its n rows hold a value.
func mostlyNull(cc *format.ColumnChunk, n int) bool {
	st := &cc.MetaData.Statistics
	if st.NullCount == 0 && st.MinValue == nil && st.Min == nil {
		return false // no statistics
	}
	return (int64(n)-st.NullCount)*8 <= int64(n)
}

// key is the family and ts of row r.
func (c *cursor) key() (value, value) { return c.fam.at(c.r), c.ts.at(c.r) }

// load reads the values of the run row r is in, from r on, when not held.
func (c *cursor) load() error {
	if c.vals != nil {
		return nil
	}
	c.lo, c.hi = c.r, c.r+1
	for f := c.fam.at(c.r); c.hi < c.n && c.fam.at(c.hi) == f; c.hi++ {
	}
	c.vals = make([]*vector, len(c.in.cols))
	cells := 0
	for i := range c.in.cols {
		v := c.fam
		switch i {
		case c.in.fam:
		case c.in.ts:
			v = c.ts
		default:
			var err error
			if v, err = c.readColumn(i, c.lo, c.hi, true); err != nil {
				return err
			}
		}
		c.vals[i] = v
		if v.pos != nil {
			cells += len(v.pos)
		} else {
			c.dense = append(c.dense, i)
		}
	}
	if cells == 0 {
		return nil
	}
	// The mostly-NULL columns' cells by row, a counting sort.
	rows := c.hi - c.lo
	c.start = make([]int32, rows+1)
	for _, v := range c.vals {
		for _, p := range v.pos {
			c.start[p+1]++
		}
	}
	for r := range rows {
		c.start[r+1] += c.start[r]
	}
	c.sparse, c.sparseAt = make([]int32, cells), make([]int32, cells)
	next := append([]int32(nil), c.start[:rows]...)
	for i, v := range c.vals {
		for k, p := range v.pos {
			c.sparse[next[p]], c.sparseAt[next[p]] = int32(i), int32(k)
			next[p]++
		}
	}
	return nil
}

// fill puts row r's values into buf, in their output columns, and returns
// set with each column it set appended.
func (c *cursor) fill(buf row, set []int) []int {
	for _, i := range c.dense {
		if v := c.vals[i].at(c.r); v.ok {
			o := c.in.cols[i].out
			buf[o] = v
			set = append(set, o)
		}
	}
	if c.start != nil {
		r := c.r - c.lo
		for k := c.start[r]; k < c.start[r+1]; k++ {
			i := c.sparse[k]
			o := c.in.cols[i].out
			buf[o] = c.vals[i].value(int(c.sparseAt[k]))
			set = append(set, o)
		}
	}
	return set
}

// compareKeys orders (family, ts) pairs, NULLs last in each.
func compareKeys(f1, t1, f2, t2 value) int {
	if c := nullsLast(f1, f2, func(a, b value) int { return strings.Compare(a.s, b.s) }); c != 0 {
		return c
	}
	return nullsLast(t1, t2, func(a, b value) int { return cmp.Compare(a.n, b.n) })
}

// cursorHeap orders the cursors by their row's (family, ts), then by
// input.
type cursorHeap struct{ items []*cursor }

func (h *cursorHeap) Len() int { return len(h.items) }
func (h *cursorHeap) Less(i, j int) bool {
	a, b := h.items[i], h.items[j]
	af, at := a.key()
	bf, bt := b.key()
	if c := compareKeys(af, at, bf, bt); c != 0 {
		return c < 0
	}
	return a.order < b.order
}
func (h *cursorHeap) Swap(i, j int) { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *cursorHeap) Push(x any)    { h.items = append(h.items, x.(*cursor)) }
func (h *cursorHeap) Pop() any {
	x := h.items[len(h.items)-1]
	h.items = h.items[:len(h.items)-1]
	return x
}

// vector is rows from on of one column chunk, of type typ: a VARCHAR as
// an index per value into dict, every other type as a number per value
// (value's n). A dense vector has a value per row, NULL as index -1 or
// null[r]; a compact one (pos not nil) has only the non-NULL values,
// pos[k] the row of the k-th, counted from from.
type vector struct {
	typ  colType
	from int
	dict []string
	idx  []int32
	nums []int64
	null []bool // dense, not VARCHAR: nil when no row is NULL
	pos  []int32
}

// at is row r's value (of the group, from on), of a dense vector; nil
// reads NULL.
func (v *vector) at(r int) value {
	if v == nil {
		return value{}
	}
	r -= v.from
	if v.typ == colVarchar {
		if v.idx[r] < 0 {
			return value{}
		}
		return strValue(v.dict[v.idx[r]])
	}
	if v.null != nil && v.null[r] {
		return value{}
	}
	return value{n: v.nums[r], ok: true, k: v.typ}
}

// value is the k-th value held: of a compact vector, its k-th non-NULL
// value.
func (v *vector) value(k int) value {
	if v.typ == colVarchar {
		return strValue(v.dict[v.idx[k]])
	}
	return value{n: v.nums[k], ok: true, k: v.typ}
}

// readVector reads rows from to to of chunk cc, of n rows, of the input
// column ic, compactly when compact. The whole chunk is decoded, its
// pages released as read, so the strings kept are copied out: of a
// dictionary (a chunk's pages share one) only the entries the rows kept
// use, each once; of a PLAIN page the rows kept.
func readVector(cc parquet.ColumnChunk, ic inColumn, n, from, to int, compact bool) (*vector, error) {
	v := &vector{typ: ic.typ, from: from}
	switch {
	case compact:
		v.pos = make([]int32, 0, 16)
	case v.typ == colVarchar:
		v.idx = make([]int32, 0, to-from)
	default:
		v.nums = make([]int64, 0, to-from)
	}
	kind := cc.Type().Kind()
	var lastDict parquet.Dictionary
	var remap []int32 // lastDict's entries' indexes in v.dict, -1 until used
	row := 0
	err := eachPage(cc, func(p parquet.Page) error {
		def := p.DefinitionLevels()
		rows := int(p.NumRows())
		if row+rows <= from || row >= to {
			row += rows
			return nil
		}
		data := p.Data()
		// get appends the k-th value of the page to v.
		var get func(k int)
		if d := p.Dictionary(); d != nil {
			idx := data.Int32()
			dv := d.Page().Data()
			if v.typ == colVarchar {
				vals, offs := dv.ByteArray()
				if d != lastDict {
					lastDict, remap = d, make([]int32, len(offs)-1)
					for j := range remap {
						remap[j] = -1
					}
				}
				get = func(k int) {
					j := idx[k]
					if remap[j] < 0 {
						remap[j] = int32(len(v.dict))
						v.dict = append(v.dict, string(vals[offs[j]:offs[j+1]]))
					}
					v.idx = append(v.idx, remap[j])
				}
			} else {
				num := numbersOf(kind, &dv, ic.scale)
				get = func(k int) { v.nums = append(v.nums, num(int(idx[k]))) }
			}
		} else if v.typ == colVarchar {
			vals, offs := data.ByteArray()
			get = func(k int) {
				v.idx = append(v.idx, int32(len(v.dict)))
				v.dict = append(v.dict, string(vals[offs[k]:offs[k+1]]))
			}
		} else {
			num := numbersOf(kind, &data, ic.scale)
			get = func(k int) { v.nums = append(v.nums, num(k)) }
		}
		k := 0
		for r := range rows {
			null := def != nil && def[r] == 0
			if row >= from && row < to {
				switch {
				case null:
					if !compact {
						v.addNull(row-from, to-from)
					}
				default:
					if compact {
						v.pos = append(v.pos, int32(row-from))
					} else if v.null != nil {
						v.null = append(v.null, false)
					}
					get(k)
				}
			}
			if !null {
				k++
			}
			row++
		}
		return nil
	})
	if err == nil && row != n {
		err = fmt.Errorf("read %d rows, want %d", row, n)
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// addNull appends a NULL as row (counted from from) of a dense vector of
// rows rows.
func (v *vector) addNull(row, rows int) {
	if v.typ == colVarchar {
		v.idx = append(v.idx, -1)
		return
	}
	if v.null == nil {
		v.null = make([]bool, row, rows)
	}
	v.null = append(v.null, true)
	v.nums = append(v.nums, 0)
}

// numbersOf reads the k-th of a page's values as value's n: an INT32 or
// INT64 as it is, a TIMESTAMP times scale, a DOUBLE as its bits.
func numbersOf(kind parquet.Kind, vals *encoding.Values, scale int64) func(k int) int64 {
	switch kind {
	case parquet.Int32:
		v := vals.Int32()
		return func(k int) int64 { return int64(v[k]) }
	case parquet.Double:
		v := vals.Double()
		return func(k int) int64 { return floatValue(v[k]).n }
	}
	v := vals.Int64()
	if scale > 1 {
		return func(k int) int64 { return v[k] * scale }
	}
	return func(k int) int64 { return v[k] }
}

// eachByteArray calls fn for each row of a BYTE_ARRAY chunk with its
// bytes (only valid during the call) and false for a NULL, and returns
// how many rows it read.
func eachByteArray(cc parquet.ColumnChunk, fn func(b []byte, ok bool)) (int, error) {
	n := 0
	err := eachPage(cc, func(p parquet.Page) error {
		def := p.DefinitionLevels()
		data := p.Data()
		var vals []byte
		var offs []uint32
		var idx []int32
		if d := p.Dictionary(); d != nil {
			dv := d.Page().Data()
			vals, offs = dv.ByteArray()
			idx = data.Int32()
		} else {
			vals, offs = data.ByteArray()
		}
		rows := int(p.NumRows())
		k := 0
		for r := range rows {
			if def != nil && def[r] == 0 {
				fn(nil, false)
				continue
			}
			at := k
			if idx != nil {
				at = int(idx[k])
			}
			fn(vals[offs[at]:offs[at+1]], true)
			k++
		}
		n += rows
		return nil
	})
	return n, err
}

// eachPage calls fn for each page of cc in order, releasing it after.
func eachPage(cc parquet.ColumnChunk, fn func(p parquet.Page) error) error {
	pages := cc.Pages()
	defer pages.Close()
	for {
		p, err := pages.ReadPage()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		err = fn(p)
		parquet.Release(p)
		if err != nil {
			return err
		}
	}
}
