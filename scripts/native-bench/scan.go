package main

import (
	"strings"
	"sync/atomic"
)

// What the two Parquet sources share: the row-group counter, the column
// list a file answers, the pruning decision and the row filter.

type rowGroupCounter struct{ n atomic.Int64 }

func (c *rowGroupCounter) openedRowGroups() int { return int(c.n.Load()) }

// OpenedRowGroups is how many row groups src has read (not pruned) since it
// was made; -1 for a source that does not count.
func OpenedRowGroups(src Source) int {
	if c, ok := src.(interface{ openedRowGroups() int }); ok {
		return c.openedRowGroups()
	}
	return -1
}

// expandCols resolves the requested names against one file's columns: a
// "prefix*" pattern becomes every file column with that prefix, a plain name
// stays even when the file lacks it (it then reads all null).
func expandCols(fileCols []string, want []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, w := range want {
		if prefix, ok := strings.CutSuffix(w, "*"); ok {
			for _, c := range fileCols {
				if strings.HasPrefix(c, prefix) {
					add(c)
				}
			}
			continue
		}
		add(w)
	}
	return out
}

// groupStats is what a row group's statistics say about family and day;
// ok false when a column has none.
type groupStats struct {
	famOK          bool
	famMin, famMax string
	dayOK          bool
	dayMin, dayMax int64
}

// decide says whether a row group may hold matching rows (keep) and whether
// every row in it matches (all), so the row filter can be skipped.
func (s groupStats) decide(family string, from, to int64) (keep, all bool) {
	if s.famOK && (family < s.famMin || family > s.famMax) {
		return false, false
	}
	if s.dayOK && (s.dayMax < from || s.dayMin > to) {
		return false, false
	}
	all = s.famOK && s.famMin == family && s.famMax == family &&
		s.dayOK && s.dayMin >= from && s.dayMax <= to
	return true, all
}

// nullColumn is a column of n nulls, for a column a file lacks: an integer
// one for day (the only integer column a query names), a string one else.
func nullColumn(name string, n int) *Column {
	if name == "day" {
		null := make([]bool, n)
		for i := range null {
			null[i] = true
		}
		return &Column{Ints: make([]int64, n), Null: null}
	}
	idx := make([]int32, n)
	for i := range idx {
		idx[i] = -1
	}
	return &Column{Idx: idx}
}

// filterBatch keeps the rows whose family is family and day in [from, to],
// compacting every column of b in place of a copy only when a row goes.
func filterBatch(b *Batch, fam, day *Column, family string, from, to int64) *Batch {
	match := make([]bool, len(fam.Dict))
	for i, s := range fam.Dict {
		match[i] = s == family
	}
	sel := make([]int32, 0, b.N)
	for i := 0; i < b.N; i++ {
		f := fam.Idx[i]
		if f < 0 || !match[f] {
			continue
		}
		if day.Null != nil && day.Null[i] {
			continue
		}
		if d := day.Ints[i]; d < from || d > to {
			continue
		}
		sel = append(sel, int32(i))
	}
	if len(sel) == b.N {
		return b
	}
	out := &Batch{N: len(sel), Cols: make(map[string]*Column, len(b.Cols))}
	for name, c := range b.Cols {
		nc := &Column{Dict: c.Dict}
		if c.Idx != nil {
			nc.Idx = make([]int32, len(sel))
			for j, i := range sel {
				nc.Idx[j] = c.Idx[i]
			}
		}
		if c.Ints != nil {
			nc.Ints = make([]int64, len(sel))
			for j, i := range sel {
				nc.Ints[j] = c.Ints[i]
			}
		}
		if c.Null != nil {
			nc.Null = make([]bool, len(sel))
			for j, i := range sel {
				nc.Null[j] = c.Null[i]
			}
		}
		out.Cols[name] = nc
	}
	return out
}

// dictBuilder dictionary-encodes plain string values for one batch.
type dictBuilder struct {
	ids  map[string]int32
	dict []string
}

func (d *dictBuilder) id(b []byte) int32 {
	if d.ids == nil {
		d.ids = map[string]int32{}
		for i, s := range d.dict {
			d.ids[s] = int32(i)
		}
	}
	if id, ok := d.ids[string(b)]; ok {
		return id
	}
	s := string(b)
	id := int32(len(d.dict))
	d.ids[s] = id
	d.dict = append(d.dict, s)
	return id
}

// finish runs the batch through the filter when needed and drops the
// columns read only for it.
func finish(b *Batch, requested []string, all bool, family string, from, to int64) *Batch {
	if !all {
		b = filterBatch(b, b.Cols["family"], b.Cols["day"], family, from, to)
	}
	if len(b.Cols) != len(requested) {
		keep := make(map[string]bool, len(requested))
		for _, n := range requested {
			keep[n] = true
		}
		for n := range b.Cols {
			if !keep[n] {
				delete(b.Cols, n)
			}
		}
	}
	return b
}
