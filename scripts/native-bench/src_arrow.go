package main

import (
	"context"
	"fmt"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/metadata"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/apache/arrow-go/v18/parquet/schema"
)

// arrowSource reads with arrow-go: the parquet metadata for pruning, then
// pqarrow, one record reader per kept row group, string columns read as
// dictionaries (the pages' own dictionary when they are dictionary-encoded,
// pqarrow's memo table over the values when they are plain). One batch is
// one row group.
type arrowSource struct{ rowGroupCounter }

func NewArrowSource() Source { return &arrowSource{} }

func (s *arrowSource) Name() string { return "arrow-go" }

// arrowFile is one open file with its footer and record reader: a
// GroupReader's state.
type arrowFile struct {
	path     string
	rdr      *file.Reader
	fr       *pqarrow.FileReader
	sch      *schema.Schema
	fileCols []string
}

func openArrow(path string) (*arrowFile, error) {
	rdr, err := file.OpenParquetFile(path, true)
	if err != nil {
		return nil, err
	}
	md := rdr.MetaData()
	sch := md.Schema
	a := &arrowFile{path: path, rdr: rdr, sch: sch, fileCols: make([]string, sch.NumColumns())}
	for i := range a.fileCols {
		a.fileCols[i] = sch.Column(i).Name()
	}
	props := pqarrow.ArrowReadProperties{}
	for i := 0; i < md.NumRowGroups(); i++ {
		props.BatchSize = max(props.BatchSize, md.RowGroup(i).NumRows())
	}
	for i := range a.fileCols {
		if sch.Column(i).PhysicalType() == parquet.Types.ByteArray {
			props.SetReadDict(i, true)
		}
	}
	if a.fr, err = pqarrow.NewFileReader(rdr, props, memory.DefaultAllocator); err != nil {
		rdr.Close()
		return nil, err
	}
	return a, nil
}

func (a *arrowFile) Close() error { return a.rdr.Close() }

func (s *arrowSource) Groups(files []string, family string, from, to int64) ([]Group, error) {
	var out []Group
	for _, path := range files {
		rdr, err := file.OpenParquetFile(path, true)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		md := rdr.MetaData()
		famIdx, dayIdx := md.Schema.ColumnIndexByName("family"), md.Schema.ColumnIndexByName("day")
		for rg := 0; rg < md.NumRowGroups(); rg++ {
			rgm := md.RowGroup(rg)
			keep, all := arrowStats(rgm, famIdx, dayIdx).decide(family, from, to)
			if keep && rgm.NumRows() > 0 {
				out = append(out, Group{File: path, Index: rg, Rows: rgm.NumRows(), All: all})
			}
		}
		if err := rdr.Close(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return out, nil
}

func (s *arrowSource) NewGroupReader() *GroupReader { return &GroupReader{} }

func (s *arrowSource) ReadGroup(r *GroupReader, g Group, cols []string, family string, from, to int64) (*Batch, error) {
	a, ok := r.state.(*arrowFile)
	if !ok || a.path != g.File {
		if err := r.Close(); err != nil {
			return nil, err
		}
		var err error
		if a, err = openArrow(g.File); err != nil {
			return nil, err
		}
		r.state = a
	}
	s.n.Add(1)
	requested := expandCols(a.fileCols, cols)
	read := requested
	if !g.All {
		read = expandCols(nil, append(append([]string{}, requested...), "family", "day"))
	}
	var idxs []int
	var names []string // names of idxs, in order
	for _, n := range read {
		if i := a.sch.ColumnIndexByName(n); i >= 0 {
			idxs = append(idxs, i)
			names = append(names, n)
		}
	}
	b, err := readArrowGroup(a.fr, g, idxs, names, read)
	if err != nil {
		return nil, err
	}
	return finish(b, requested, g.All, family, from, to), nil
}

// readArrowGroup reads one row group as one record: the reader's batch size
// is the file's largest row group, so a group never splits.
func readArrowGroup(fr *pqarrow.FileReader, g Group, idxs []int, names, read []string) (*Batch, error) {
	n := int(g.Rows)
	b := &Batch{N: n, Cols: make(map[string]*Column, len(read))}
	if len(idxs) > 0 {
		rr, err := fr.GetRecordReader(context.Background(), idxs, []int{g.Index})
		if err != nil {
			return nil, err
		}
		defer rr.Release()
		if !rr.Next() {
			if err := rr.Err(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("no record for %d rows", n)
		}
		rec := rr.RecordBatch()
		if int(rec.NumRows()) != n {
			return nil, fmt.Errorf("record of %d rows, want %d", rec.NumRows(), n)
		}
		for i, name := range names {
			c, err := arrowColumn(rec.Column(i))
			if err != nil {
				return nil, fmt.Errorf("column %s: %w", name, err)
			}
			b.Cols[name] = c
		}
	}
	for _, name := range read {
		if b.Cols[name] == nil {
			b.Cols[name] = nullColumn(name, n)
		}
	}
	return b, nil
}

func arrowStats(rgm *metadata.RowGroupMetaData, famIdx, dayIdx int) groupStats {
	var g groupStats
	stats := func(i int) metadata.TypedStatistics {
		if i < 0 {
			return nil
		}
		cc, err := rgm.ColumnChunk(i)
		if err != nil {
			return nil
		}
		if ok, err := cc.StatsSet(); !ok || err != nil {
			return nil
		}
		st, err := cc.Statistics()
		if err != nil || st == nil || !st.HasMinMax() {
			return nil
		}
		return st
	}
	if st, ok := stats(famIdx).(*metadata.ByteArrayStatistics); ok {
		g.famOK, g.famMin, g.famMax = true, string(st.Min()), string(st.Max())
	}
	if st, ok := stats(dayIdx).(*metadata.Int32Statistics); ok {
		g.dayOK, g.dayMin, g.dayMax = true, int64(st.Min()), int64(st.Max())
	}
	return g
}

// arrowColumn converts one record column. Strings arrive as dictionary
// arrays; their dictionary strings point into Go-allocated buffers that the
// Go allocator never reuses, so they are not copied.
func arrowColumn(a arrow.Array) (*Column, error) {
	n := a.Len()
	switch a := a.(type) {
	case *array.Dictionary:
		c := &Column{Idx: make([]int32, n)}
		switch d := a.Dictionary().(type) {
		case *array.String:
			c.Dict = make([]string, d.Len())
			for i := range c.Dict {
				c.Dict[i] = d.Value(i)
			}
		case *array.Binary:
			c.Dict = make([]string, d.Len())
			for i := range c.Dict {
				c.Dict[i] = d.ValueString(i)
			}
		default:
			return nil, fmt.Errorf("dictionary of %s", d.DataType())
		}
		idx, ok := a.Indices().(*array.Int32)
		if !ok {
			for i := 0; i < n; i++ {
				c.Idx[i] = int32(a.GetValueIndex(i))
			}
		} else {
			copy(c.Idx, idx.Int32Values())
		}
		if a.NullN() > 0 {
			for i := 0; i < n; i++ {
				if a.IsNull(i) {
					c.Idx[i] = -1
				}
			}
		}
		return c, nil
	case *array.String:
		var d dictBuilder
		c := &Column{Idx: make([]int32, n)}
		for i := 0; i < n; i++ {
			if a.IsNull(i) {
				c.Idx[i] = -1
			} else {
				c.Idx[i] = d.id([]byte(a.Value(i)))
			}
		}
		c.Dict = d.dict
		return c, nil
	case *array.Date32:
		c := &Column{Ints: make([]int64, n)}
		for i, v := range a.Date32Values() {
			c.Ints[i] = int64(v)
		}
		ints(c, a)
		return c, nil
	case *array.Int32:
		c := &Column{Ints: make([]int64, n)}
		for i, v := range a.Int32Values() {
			c.Ints[i] = int64(v)
		}
		ints(c, a)
		return c, nil
	case *array.Int64:
		c := &Column{Ints: make([]int64, n)}
		copy(c.Ints, a.Int64Values())
		ints(c, a)
		return c, nil
	}
	return nil, fmt.Errorf("unsupported type %s", a.DataType())
}

func ints(c *Column, a arrow.Array) {
	if a.NullN() == 0 {
		return
	}
	c.Null = make([]bool, a.Len())
	for i := range c.Null {
		c.Null[i] = a.IsNull(i)
	}
}
