package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
)

// pqgoSource reads with parquet-go at the page level: row-group statistics
// from the footer for pruning, then each kept column chunk's pages. A
// dictionary-encoded page hands over its dictionary and its indexes as they
// are; a plain page's strings go into a per-batch dictionary. One batch is
// one row group.
type pqgoSource struct{ rowGroupCounter }

func NewPQGoSource() Source { return &pqgoSource{} }

func (s *pqgoSource) Name() string { return "parquet-go" }

func (s *pqgoSource) Scan(files []string, cols []string, family string, from, to int64, fn func(*Batch) error) error {
	for _, path := range files {
		if err := s.scanFile(path, cols, family, from, to, fn); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

func (s *pqgoSource) scanFile(path string, cols []string, family string, from, to int64, fn func(*Batch) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	pf, err := parquet.OpenFile(f, st.Size(), parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true),
		parquet.ReadBufferSize(1<<20))
	if err != nil {
		return err
	}
	leaves := map[string]int{}
	var fileCols []string
	for _, path := range pf.Schema().Columns() {
		if len(path) == 1 {
			lc, _ := pf.Schema().Lookup(path...)
			leaves[path[0]] = lc.ColumnIndex
			fileCols = append(fileCols, path[0])
		}
	}
	requested := expandCols(fileCols, cols)
	famIdx, dayIdx := -1, -1
	if i, ok := leaves["family"]; ok {
		famIdx = i
	}
	if i, ok := leaves["day"]; ok {
		dayIdx = i
	}

	md := pf.Metadata()
	groups := pf.RowGroups()
	for rg := range md.RowGroups {
		rgm := &md.RowGroups[rg]
		keep, all := pqgoStats(rgm, famIdx, dayIdx).decide(family, from, to)
		if !keep || rgm.NumRows == 0 {
			continue
		}
		s.n.Add(1)
		read := requested
		if !all {
			read = expandCols(nil, append(append([]string{}, requested...), "family", "day"))
		}
		n := int(rgm.NumRows)
		b := &Batch{N: n, Cols: make(map[string]*Column, len(read))}
		chunks := groups[rg].ColumnChunks()
		for _, name := range read {
			i, ok := leaves[name]
			if !ok {
				b.Cols[name] = nullColumn(name, n)
				continue
			}
			c, err := readChunk(chunks[i], n)
			if err != nil {
				return fmt.Errorf("row group %d column %s: %w", rg, name, err)
			}
			b.Cols[name] = c
		}
		if err := fn(finish(b, requested, all, family, from, to)); err != nil {
			return err
		}
	}
	return nil
}

func pqgoStats(rgm *format.RowGroup, famIdx, dayIdx int) groupStats {
	var g groupStats
	minMax := func(i int) (lo, hi []byte, ok bool) {
		if i < 0 || i >= len(rgm.Columns) {
			return nil, nil, false
		}
		st := &rgm.Columns[i].MetaData.Statistics
		if st.MinValue != nil && st.MaxValue != nil {
			return st.MinValue, st.MaxValue, true
		}
		if st.Min != nil && st.Max != nil {
			return st.Min, st.Max, true
		}
		return nil, nil, false
	}
	if lo, hi, ok := minMax(famIdx); ok {
		g.famOK, g.famMin, g.famMax = true, string(lo), string(hi)
	}
	if lo, hi, ok := minMax(dayIdx); ok && len(lo) == 4 && len(hi) == 4 {
		g.dayOK = true
		g.dayMin = int64(int32(binary.LittleEndian.Uint32(lo)))
		g.dayMax = int64(int32(binary.LittleEndian.Uint32(hi)))
	}
	return g
}

// readChunk reads one column chunk of n rows. Pages are released as they
// are consumed, so everything kept is copied out of them: indexes into the
// column's own slice, dictionary and plain strings into Go strings.
func readChunk(cc parquet.ColumnChunk, n int) (*Column, error) {
	kind := cc.Type().Kind()
	c := &Column{}
	switch kind {
	case parquet.ByteArray:
		c.Idx = make([]int32, 0, n)
	case parquet.Int32, parquet.Int64:
		c.Ints = make([]int64, 0, n)
	default:
		return nil, fmt.Errorf("unsupported type %s", cc.Type())
	}
	var (
		db       dictBuilder
		lastDict parquet.Dictionary
		base     int32 // where lastDict's entries start in db.dict
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
		switch kind {
		case parquet.ByteArray:
			if d := p.Dictionary(); d != nil {
				if d != lastDict {
					// A new dictionary: append its values (a chunk has one, so
					// this happens once per batch in practice).
					dv := d.Page().Data()
					vals, offs := dv.ByteArray()
					base = int32(len(db.dict))
					for j := 0; j+1 < len(offs); j++ {
						db.dict = append(db.dict, string(vals[offs[j]:offs[j+1]]))
					}
					db.ids = nil
					lastDict = d
				}
				idx := data.Int32()
				if def == nil && base == 0 {
					c.Idx = append(c.Idx, idx...)
				} else {
					k := 0
					for r := 0; r < rows; r++ {
						if def != nil && def[r] == 0 {
							c.Idx = append(c.Idx, -1)
						} else {
							c.Idx = append(c.Idx, base+idx[k])
							k++
						}
					}
				}
			} else {
				vals, offs := data.ByteArray()
				k := 0
				for r := 0; r < rows; r++ {
					if def != nil && def[r] == 0 {
						c.Idx = append(c.Idx, -1)
						continue
					}
					c.Idx = append(c.Idx, db.id(vals[offs[k]:offs[k+1]]))
					k++
				}
			}
		case parquet.Int32, parquet.Int64:
			// DuckDB dictionary-encodes integers too: then data holds
			// indexes into the dictionary page's values.
			vals := data
			var idx []int32
			if d := p.Dictionary(); d != nil {
				idx = data.Int32()
				vals = d.Page().Data()
			}
			var get func(k int) int64
			if kind == parquet.Int32 {
				v := vals.Int32()
				get = func(k int) int64 { return int64(v[k]) }
			} else {
				v := vals.Int64()
				get = func(k int) int64 { return v[k] }
			}
			if idx != nil {
				at := get
				get = func(k int) int64 { return at(int(idx[k])) }
			}
			k := 0
			for r := 0; r < rows; r++ {
				if def != nil && def[r] == 0 {
					if c.Null == nil {
						c.Null = make([]bool, len(c.Ints), n)
					}
					c.Ints = append(c.Ints, 0)
					c.Null = append(c.Null, true)
					continue
				}
				c.Ints = append(c.Ints, get(k))
				if c.Null != nil {
					c.Null = append(c.Null, false)
				}
				k++
			}
		}
		parquet.Release(p)
	}
	if kind == parquet.ByteArray {
		c.Dict = db.dict
		if len(c.Idx) != n {
			return nil, fmt.Errorf("read %d rows, want %d", len(c.Idx), n)
		}
	} else if len(c.Ints) != n {
		return nil, fmt.Errorf("read %d rows, want %d", len(c.Ints), n)
	}
	return c, nil
}
