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
)

// arrowSource reads with arrow-go: the parquet metadata for pruning, then
// pqarrow, one record reader per kept row group, string columns read as
// dictionaries (the pages' own dictionary when they are dictionary-encoded,
// pqarrow's memo table over the values when they are plain). One batch is
// one row group.
type arrowSource struct{ rowGroupCounter }

func NewArrowSource() Source { return &arrowSource{} }

func (s *arrowSource) Name() string { return "arrow-go" }

func (s *arrowSource) Scan(files []string, cols []string, family string, from, to int64, fn func(*Batch) error) error {
	for _, path := range files {
		if err := s.scanFile(path, cols, family, from, to, fn); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

func (s *arrowSource) scanFile(path string, cols []string, family string, from, to int64, fn func(*Batch) error) error {
	rdr, err := file.OpenParquetFile(path, true)
	if err != nil {
		return err
	}
	defer rdr.Close()
	md := rdr.MetaData()
	sch := md.Schema
	fileCols := make([]string, sch.NumColumns())
	for i := range fileCols {
		fileCols[i] = sch.Column(i).Name()
	}
	requested := expandCols(fileCols, cols)

	props := pqarrow.ArrowReadProperties{}
	for i := 0; i < md.NumRowGroups(); i++ {
		props.BatchSize = max(props.BatchSize, md.RowGroup(i).NumRows())
	}
	for i := range fileCols {
		if sch.Column(i).PhysicalType() == parquet.Types.ByteArray {
			props.SetReadDict(i, true)
		}
	}
	fr, err := pqarrow.NewFileReader(rdr, props, memory.DefaultAllocator)
	if err != nil {
		return err
	}
	famIdx, dayIdx := sch.ColumnIndexByName("family"), sch.ColumnIndexByName("day")

	for rg := 0; rg < md.NumRowGroups(); rg++ {
		rgm := md.RowGroup(rg)
		keep, all := arrowStats(rgm, famIdx, dayIdx).decide(family, from, to)
		if !keep || rgm.NumRows() == 0 {
			continue
		}
		s.n.Add(1)
		read := requested
		if !all {
			read = expandCols(nil, append(append([]string{}, requested...), "family", "day"))
		}
		var idxs []int
		var names []string // names of idxs, in order
		for _, n := range read {
			if i := sch.ColumnIndexByName(n); i >= 0 {
				idxs = append(idxs, i)
				names = append(names, n)
			}
		}
		if err := s.readGroup(fr, rg, idxs, names, read, requested, all, family, from, to, fn); err != nil {
			return err
		}
	}
	return nil
}

func (s *arrowSource) readGroup(fr *pqarrow.FileReader, rg int, idxs []int, names, read, requested []string,
	all bool, family string, from, to int64, fn func(*Batch) error) error {
	n := int(fr.ParquetReader().MetaData().RowGroup(rg).NumRows())
	if len(idxs) == 0 {
		b := &Batch{N: n, Cols: map[string]*Column{}}
		for _, name := range read {
			b.Cols[name] = nullColumn(name, n)
		}
		return fn(finish(b, requested, all, family, from, to))
	}
	rr, err := fr.GetRecordReader(context.Background(), idxs, []int{rg})
	if err != nil {
		return err
	}
	defer rr.Release()
	for rr.Next() {
		rec := rr.RecordBatch()
		b := &Batch{N: int(rec.NumRows()), Cols: make(map[string]*Column, len(read))}
		for i, name := range names {
			c, err := arrowColumn(rec.Column(i))
			if err != nil {
				return fmt.Errorf("column %s: %w", name, err)
			}
			b.Cols[name] = c
		}
		for _, name := range read {
			if b.Cols[name] == nil {
				b.Cols[name] = nullColumn(name, b.N)
			}
		}
		if err := fn(finish(b, requested, all, family, from, to)); err != nil {
			return err
		}
	}
	return rr.Err()
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
