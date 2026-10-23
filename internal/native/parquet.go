package native

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/encoding"
	"github.com/parquet-go/parquet-go/format"
)

// group is one row group of one file that survived pruning by statistics.
type group struct {
	file  string
	index int
	rows  int64
	all   bool  // statistics prove every row matches the family and the range
	day   int64 // a gap day to derive sessions of (index gapIndex)
}

// pqFile is one open file with its footer.
type pqFile struct {
	path           string
	f              *os.File
	pf             *parquet.File
	leaves         map[string]int // top-level column name to leaf index
	cols           []string       // top-level column names, in file order
	famIdx, dayIdx int            // leaf indexes of family and day, -1 when missing
}

func openFile(path string) (*pqFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	pf, err := parquet.OpenFile(f, st.Size(), parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true),
		parquet.ReadBufferSize(1<<20))
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p := &pqFile{path: path, f: f, pf: pf, leaves: map[string]int{}, famIdx: -1, dayIdx: -1}
	for _, col := range pf.Schema().Columns() {
		if len(col) == 1 {
			lc, _ := pf.Schema().Lookup(col...)
			p.leaves[col[0]] = lc.ColumnIndex
			p.cols = append(p.cols, col[0])
		}
	}
	if i, ok := p.leaves["family"]; ok {
		p.famIdx = i
	}
	if i, ok := p.leaves["day"]; ok {
		p.dayIdx = i
	}
	return p, nil
}

func (p *pqFile) close() error { return p.f.Close() }

// parquetGroups lists, in file order, the row groups of files whose
// statistics allow rows of family (any, when "") with day in r. It reads
// the footers only, no pages.
func parquetGroups(files []string, family string, r Range) ([]group, error) {
	from, to := r.days()
	return listGroups(files, func(st groupStats) (bool, bool) { return st.decide(family, from, to) })
}

// parquetGroupsOf is parquetGroups for rows of any of families, which the
// reader does not filter by: a group's all says only that every row's day
// is in r.
func parquetGroupsOf(files []string, families []string, r Range) ([]group, error) {
	from, to := r.days()
	return listGroups(files, func(st groupStats) (bool, bool) {
		if !st.mayHold(families) {
			return false, false
		}
		return st.decide("", from, to)
	})
}

// listGroups lists, in file order, the row groups of files with rows that
// decide keeps, with its all.
func listGroups(files []string, decide func(groupStats) (keep, all bool)) ([]group, error) {
	var out []group
	for _, path := range files {
		p, err := openFile(path)
		if err != nil {
			return nil, err
		}
		md := p.pf.Metadata()
		for i := range md.RowGroups {
			rg := &md.RowGroups[i]
			keep, all := decide(statsOf(rg, p.famIdx, p.dayIdx))
			if keep && rg.NumRows > 0 {
				out = append(out, group{file: path, index: i, rows: rg.NumRows, all: all})
			}
		}
		if err := p.close(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return out, nil
}

// statsOf is what row group rg's statistics say about family and day.
func statsOf(rg *format.RowGroup, famIdx, dayIdx int) groupStats {
	var g groupStats
	// The null count comes with the bounds (a writer SHOULD always write
	// it, DuckDB does); without bounds it is not read.
	minMax := func(i int) (lo, hi []byte, nulls int64, ok bool) {
		if i < 0 || i >= len(rg.Columns) {
			return nil, nil, 0, false
		}
		st := &rg.Columns[i].MetaData.Statistics
		if st.MinValue != nil && st.MaxValue != nil {
			return st.MinValue, st.MaxValue, st.NullCount, true
		}
		if st.Min != nil && st.Max != nil {
			return st.Min, st.Max, st.NullCount, true
		}
		return nil, nil, 0, false
	}
	if lo, hi, nulls, ok := minMax(famIdx); ok {
		g.famOK, g.famMin, g.famMax, g.famNulls = true, string(lo), string(hi), nulls
	}
	if lo, hi, nulls, ok := minMax(dayIdx); ok && len(lo) == 4 && len(hi) == 4 {
		g.dayOK, g.dayNulls = true, nulls
		g.dayMin = int64(int32(binary.LittleEndian.Uint32(lo)))
		g.dayMax = int64(int32(binary.LittleEndian.Uint32(hi)))
	}
	return g
}

// groupReader reads row groups for one worker, keeping the last file it
// opened open, so consecutive groups of one file reuse it. The zero value
// is ready; close releases it.
type groupReader struct{ f *pqFile }

func (g *groupReader) close() error {
	if g.f == nil {
		return nil
	}
	err := g.f.close()
	g.f = nil
	return err
}

// read reads row group gr into a batch of the columns cols holding only
// the rows of family (any, when "") with day in r. A name in cols ending
// in "*" (AttrCols) is a prefix pattern, expanded to the file's columns
// with that prefix, so batches differ in which of those they hold; a plain
// name the file lacks reads all null.
func (g *groupReader) read(gr group, cols []string, family string, r Range) (*Batch, error) {
	if g.f == nil || g.f.path != gr.file {
		if err := g.close(); err != nil {
			return nil, err
		}
		f, err := openFile(gr.file)
		if err != nil {
			return nil, err
		}
		g.f = f
	}
	p := g.f
	requested := expandCols(p.cols, cols)
	read := requested
	if !gr.all {
		extra := []string{"day"}
		if family != "" {
			extra = append(extra, "family")
		}
		read = expandCols(nil, append(append([]string{}, requested...), extra...))
	}
	n := int(gr.rows)
	b := &Batch{N: n, Cols: make(map[string]*Column, len(read))}
	chunks := p.pf.RowGroups()[gr.index].ColumnChunks()
	for _, name := range read {
		i, ok := p.leaves[name]
		if !ok {
			b.Cols[name] = nullColumn(name, n)
			continue
		}
		c, err := readChunk(chunks[i], name, n)
		if err != nil {
			return nil, fmt.Errorf("%s row group %d column %s: %w", gr.file, gr.index, name, err)
		}
		b.Cols[name] = c
	}
	from, to := r.days()
	return finish(b, requested, gr.all, family, from, to), nil
}

// timeScale is how column cc's INT64 values become unix microseconds:
// multiply by mul, divide by div; ok false when the column is no
// timestamp. The logical type says so and gives the unit; without one, the
// events schema's TIMESTAMP columns are taken as DuckDB writes them, in
// microseconds.
func timeScale(cc parquet.ColumnChunk, name string) (mul, div int64, ok bool) {
	if lt := cc.Type().LogicalType(); lt != nil {
		if ts, isTS := lt.Value.(*format.TimestampType); isTS {
			switch d := ts.Unit.Value.Duration(); {
			case d > time.Microsecond:
				return int64(d / time.Microsecond), 1, true
			case d < time.Microsecond:
				return 1, int64(time.Microsecond / d), true
			}
			return 1, 1, true
		}
	}
	if eventKinds[name] == kindTime {
		return 1, 1, true
	}
	return 0, 0, false
}

// readChunk reads one column chunk of n rows. Pages are released as they
// are consumed, so everything kept is copied out of them: indexes and
// numbers into the column's own slices, strings into Go strings. A
// dictionary-encoded string page's dictionary joins the batch's, its
// indexes kept as they are when that is the dictionary's first (no values
// before it, none repeated); a plain page's strings join it one by one, so
// the batch's dictionary holds each value once.
func readChunk(cc parquet.ColumnChunk, name string, n int) (*Column, error) {
	kind := cc.Type().Kind()
	c := &Column{}
	mul, div, isTime := int64(1), int64(1), false
	switch kind {
	case parquet.ByteArray:
		c.Idx = make([]int32, 0, n)
	case parquet.Int32:
		c.Ints = make([]int64, 0, n)
	case parquet.Int64:
		if mul, div, isTime = timeScale(cc, name); isTime {
			c.Times = make([]int64, 0, n)
		} else {
			c.Ints = make([]int64, 0, n)
		}
	case parquet.Float, parquet.Double:
		c.Floats = make([]float64, 0, n)
	default:
		return nil, fmt.Errorf("unsupported type %s", cc.Type())
	}
	var (
		db       dictBuilder
		lastDict parquet.Dictionary
		remap    []int32 // lastDict's entries' indexes in db.dict
		same     bool    // remap is the identity
	)
	pages := cc.Pages()
	defer pages.Close()
	for {
		p, err := pages.ReadPage()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		def := p.DefinitionLevels() // nil for a required column
		rows := int(p.NumRows())
		data := p.Data()
		if kind == parquet.ByteArray {
			if d := p.Dictionary(); d != nil {
				if d != lastDict {
					// A new dictionary (a chunk has one, so this happens once
					// per batch in practice): its values join the batch's,
					// each once.
					dv := d.Page().Data()
					remap, same = db.addDict(dv.ByteArray())
					lastDict = d
				}
				idx := data.Int32()
				if def == nil && same {
					c.Idx = append(c.Idx, idx...)
				} else {
					k := 0
					for r := range rows {
						if def != nil && def[r] == 0 {
							c.Idx = append(c.Idx, -1)
						} else {
							c.Idx = append(c.Idx, remap[idx[k]])
							k++
						}
					}
				}
			} else {
				vals, offs := data.ByteArray()
				k := 0
				for r := range rows {
					if def != nil && def[r] == 0 {
						c.Idx = append(c.Idx, -1)
						continue
					}
					c.Idx = append(c.Idx, db.id(vals[offs[k]:offs[k+1]]))
					k++
				}
			}
			parquet.Release(p)
			continue
		}
		// DuckDB dictionary-encodes numbers too: then data holds indexes
		// into the dictionary page's values.
		vals := data
		var idx []int32
		if d := p.Dictionary(); d != nil {
			idx = data.Int32()
			vals = d.Page().Data()
		}
		switch {
		case c.Floats != nil:
			get := floatsOf(kind, vals)
			c.Floats, c.Null = appendValues(c.Floats, c.Null, def, idx, rows, n, get)
		case isTime:
			get := intsOf(kind, vals)
			scaled := func(k int) int64 { return get(k) * mul / div }
			c.Times, c.Null = appendValues(c.Times, c.Null, def, idx, rows, n, scaled)
		default:
			c.Ints, c.Null = appendValues(c.Ints, c.Null, def, idx, rows, n, intsOf(kind, vals))
		}
		parquet.Release(p)
	}
	got := len(c.Idx) + len(c.Ints) + len(c.Floats) + len(c.Times)
	if got != n {
		return nil, fmt.Errorf("read %d rows, want %d", got, n)
	}
	c.Dict = db.dict
	if c.Idx == nil {
		c.Dict = nil
	}
	return c, nil
}

func intsOf(kind parquet.Kind, vals encoding.Values) func(k int) int64 {
	if kind == parquet.Int32 {
		v := vals.Int32()
		return func(k int) int64 { return int64(v[k]) }
	}
	v := vals.Int64()
	return func(k int) int64 { return v[k] }
}

func floatsOf(kind parquet.Kind, vals encoding.Values) func(k int) float64 {
	if kind == parquet.Float {
		v := vals.Float()
		return func(k int) float64 { return float64(v[k]) }
	}
	v := vals.Double()
	return func(k int) float64 { return v[k] }
}

// appendValues appends one page's rows to out and null: def marks the null
// rows (nil when none can be), idx the dictionary index of each non-null
// value (nil when the page is plain), get the k-th value. null is made the
// first time a null shows up, falses for the rows before it; n is the
// chunk's row count, its capacity.
func appendValues[T int64 | float64](out []T, null []bool, def []byte, idx []int32, rows, n int, get func(k int) T) ([]T, []bool) {
	k := 0
	for r := range rows {
		if def != nil && def[r] == 0 {
			if null == nil {
				null = make([]bool, len(out), n)
			}
			var zero T
			out = append(out, zero)
			null = append(null, true)
			continue
		}
		at := k
		if idx != nil {
			at = int(idx[k])
		}
		out = append(out, get(at))
		if null != nil {
			null = append(null, false)
		}
		k++
	}
	return out, null
}
