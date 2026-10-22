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

// pqgoFile is one open file with its footer: a GroupReader's state.
type pqgoFile struct {
	path           string
	f              *os.File
	pf             *parquet.File
	leaves         map[string]int // top-level column name to leaf index
	fileCols       []string
	famIdx, dayIdx int
}

func openPQGo(path string) (*pqgoFile, error) {
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
		return nil, err
	}
	p := &pqgoFile{path: path, f: f, pf: pf, leaves: map[string]int{}, famIdx: -1, dayIdx: -1}
	for _, col := range pf.Schema().Columns() {
		if len(col) == 1 {
			lc, _ := pf.Schema().Lookup(col...)
			p.leaves[col[0]] = lc.ColumnIndex
			p.fileCols = append(p.fileCols, col[0])
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

func (p *pqgoFile) Close() error { return p.f.Close() }

func (s *pqgoSource) Groups(files []string, family string, from, to int64) ([]Group, error) {
	var out []Group
	for _, path := range files {
		p, err := openPQGo(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		md := p.pf.Metadata()
		for rg := range md.RowGroups {
			rgm := &md.RowGroups[rg]
			keep, all := pqgoStats(rgm, p.famIdx, p.dayIdx).decide(family, from, to)
			if keep && rgm.NumRows > 0 {
				out = append(out, Group{File: path, Index: rg, Rows: rgm.NumRows, All: all})
			}
		}
		if err := p.Close(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return out, nil
}

func (s *pqgoSource) NewGroupReader() *GroupReader { return &GroupReader{} }

func (s *pqgoSource) ReadGroup(r *GroupReader, g Group, cols []string, family string, from, to int64) (*Batch, error) {
	p, ok := r.state.(*pqgoFile)
	if !ok || p.path != g.File {
		if err := r.Close(); err != nil {
			return nil, err
		}
		var err error
		if p, err = openPQGo(g.File); err != nil {
			return nil, err
		}
		r.state = p
	}
	s.n.Add(1)
	requested := expandCols(p.fileCols, cols)
	read := requested
	if !g.All {
		read = expandCols(nil, append(append([]string{}, requested...), "family", "day"))
	}
	n := int(g.Rows)
	b := &Batch{N: n, Cols: make(map[string]*Column, len(read))}
	chunks := p.pf.RowGroups()[g.Index].ColumnChunks()
	for _, name := range read {
		i, ok := p.leaves[name]
		if !ok {
			b.Cols[name] = nullColumn(name, n)
			continue
		}
		c, err := readChunk(chunks[i], n)
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", name, err)
		}
		b.Cols[name] = c
	}
	return finish(b, requested, g.All, family, from, to), nil
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
