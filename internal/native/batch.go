// Package native reads the event files under raw/ in Go, without the
// engine's child: the Parquet files by row group, pruned on their
// statistics, into column batches.
package native

import (
	"iter"
	"maps"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// Column is one column of a batch, by the column's type:
//   - VARCHAR: Dict holds this batch's distinct values, Idx one entry per
//     row into it (-1 = null);
//   - BIGINT and DATE (days since 1970-01-01): Ints;
//   - DOUBLE: Floats;
//   - TIMESTAMP (unix microseconds, UTC): Times.
//
// Null marks the null rows of Ints, Floats and Times; nil when none is.
type Column struct {
	Dict   []string
	Idx    []int32
	Ints   []int64
	Floats []float64
	Times  []int64
	Null   []bool
}

// Batch is a run of rows, column by column. Cols is keyed by the column's
// name as the batch's file spells it: DuckDB's names ignore case (its
// union by name makes a month file's attr_plan and a log's attr_Plan one
// column of events), so batches from different files may spell one column
// differently. Look a column up with col, never by indexing Cols.
type Batch struct {
	N    int
	Cols map[string]*Column
}

// col finds the column name names: the one spelled so, else the one equal
// to it but for case (the first in name order, should there be several).
// key is the batch's own name for it; "" and nil when b has none.
func (b *Batch) col(name string) (key string, c *Column) {
	key, ok := resolveName(name, maps.Keys(b.Cols))
	if !ok {
		return "", nil
	}
	return key, b.Cols[key]
}

// resolveName is the one of names that name names, as col finds it: name
// itself, else the least of those equal to it but for case.
func resolveName(name string, names iter.Seq[string]) (string, bool) {
	var fold string
	found := false
	for n := range names {
		if n == name {
			return n, true
		}
		if strings.EqualFold(n, name) && (!found || n < fold) {
			fold, found = n, true
		}
	}
	return fold, found
}

// AttrCols is the column pattern that asks for every column whose name
// starts with "attr_": a query cannot name them up front, and files differ
// in which they hold.
const AttrCols = "attr_*"

// Range is a range of days, both ends included.
type Range struct{ From, To civil.Date }

// days is r as days since 1970-01-01.
func (r Range) days() (from, to int64) { return epochDay(r.From), epochDay(r.To) }

func epochDay(d civil.Date) int64 { return d.Time().Unix() / 86400 }

// colKind is a column's type in events.
type colKind int

const (
	kindString colKind = iota
	kindInt
	kindFloat
	kindTime
)

// eventKinds are the types of events' fixed columns that are not VARCHAR
// (archive's log fields); every other column, attr_* among them, is one.
var eventKinds = map[string]colKind{
	"v": kindInt, "project_id": kindInt, "day": kindInt, "display_width": kindInt,
	"display_height": kindInt, "consent": kindInt, "value": kindFloat, "sample_rate": kindFloat,
	"ts": kindTime, "received_at": kindTime,
}

// sessionKinds are the types of the sessions table's columns that are not
// VARCHAR (archive's sessionsSelect): those of events it shares, and these.
var sessionKinds = map[string]colKind{
	"duration_sec": kindInt, "views": kindInt, "started_at": kindTime, "ended_at": kindTime,
}

// nullColumn is a column of n nulls of name's type (in events, or in
// sessions for a column only it has), for a column a file lacks.
func nullColumn(name string, n int) *Column {
	null := make([]bool, n)
	for i := range null {
		null[i] = true
	}
	kind, ok := eventKinds[name]
	if !ok {
		kind = sessionKinds[name]
	}
	switch kind {
	case kindInt:
		return &Column{Ints: make([]int64, n), Null: null}
	case kindFloat:
		return &Column{Floats: make([]float64, n), Null: null}
	case kindTime:
		return &Column{Times: make([]int64, n), Null: null}
	}
	idx := make([]int32, n)
	for i := range idx {
		idx[i] = -1
	}
	return &Column{Idx: idx}
}

// take is a copy of c holding only the rows sel (the dictionary shared).
func (c *Column) take(sel []int32) *Column {
	return &Column{Dict: c.Dict, Idx: pick(c.Idx, sel), Ints: pick(c.Ints, sel),
		Floats: pick(c.Floats, sel), Times: pick(c.Times, sel), Null: pick(c.Null, sel)}
}

func pick[T any](s []T, sel []int32) []T {
	if s == nil {
		return nil
	}
	out := make([]T, len(sel))
	for j, i := range sel {
		out[j] = s[i]
	}
	return out
}

// dictBuilder dictionary-encodes plain string values for one batch.
type dictBuilder struct {
	ids  map[string]int32
	dict []string
}

func (d *dictBuilder) id(b []byte) int32 {
	d.index()
	if id, ok := d.ids[string(b)]; ok {
		return id
	}
	s := string(b)
	id := int32(len(d.dict))
	d.ids[s] = id
	d.dict = append(d.dict, s)
	return id
}

// index builds the lookup from each value to its index, unless built.
func (d *dictBuilder) index() {
	if d.ids == nil {
		d.ids = make(map[string]int32, len(d.dict))
		for i, s := range d.dict {
			d.ids[s] = int32(i)
		}
	}
}

// addDict adds a dictionary page's values (BYTE_ARRAY data and offsets)
// and returns each one's index in the batch's dictionary, or same (and no
// remap) when that is the value's own index. The batch's first dictionary
// is taken as the batch's dictionary as it is, without building the
// lookup (built only should a plain page or a second dictionary follow):
// a dictionary page holds each value once, as the files' writers (DuckDB,
// parquet-go) write it. Should one repeat a value anyway, the batch has two
// entries of one value: its readers resolve entries by value (group keys,
// id hashes, retention's map, the identities' lookups), so that costs
// double work, not a wrong answer. Its values are copied out of the page
// into one string, which the dictionary's entries share: a reader that
// keeps a value past the batch copies it (strings.Clone).
func (d *dictBuilder) addDict(vals []byte, offs []uint32) (remap []int32, same bool) {
	n := max(0, len(offs)-1)
	if len(d.dict) == 0 {
		d.dict, d.ids = make([]string, n), nil
		if n > 0 {
			all := string(vals[offs[0]:offs[n]])
			for j := range n {
				d.dict[j] = all[offs[j]-offs[0] : offs[j+1]-offs[0]]
			}
		}
		return nil, true
	}
	same = true
	remap = make([]int32, n)
	for j := range remap {
		remap[j] = d.id(vals[offs[j]:offs[j+1]])
		same = same && remap[j] == int32(j)
	}
	return remap, same
}
