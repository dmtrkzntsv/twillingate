// Package native reads the event files under raw/ in Go, without the
// engine's child: the Parquet files by row group, pruned on their
// statistics, into column batches.
package native

import "github.com/dmtrkzntsv/twillingate/internal/shared/civil"

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

// Batch is a run of rows, column by column.
type Batch struct {
	N    int
	Cols map[string]*Column
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

// nullColumn is a column of n nulls of name's type, for a column a file
// lacks.
func nullColumn(name string, n int) *Column {
	null := make([]bool, n)
	for i := range null {
		null[i] = true
	}
	switch eventKinds[name] {
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
	if d.ids == nil {
		d.ids = make(map[string]int32, len(d.dict))
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

// addDict adds a dictionary page's values (BYTE_ARRAY data and offsets)
// and returns each one's index in the batch's dictionary; same when that
// is the value's own index (the first dictionary of a batch, no value
// repeated), so the page's indexes stand as they are.
func (d *dictBuilder) addDict(vals []byte, offs []uint32) (remap []int32, same bool) {
	same = len(d.dict) == 0
	remap = make([]int32, max(0, len(offs)-1))
	for j := range remap {
		remap[j] = d.id(vals[offs[j]:offs[j+1]])
		same = same && remap[j] == int32(j)
	}
	return remap, same
}
