package native

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// LatestColumns are the columns latest may read, beside attr_<key> and
// def_ ones: the event tables' own and the computed ones not read from
// identities.
var LatestColumns = slices.Sorted(func(yield func(string) bool) {
	for _, c := range Columns {
		if _, ok := idColumns[c]; !ok && !yield(c) {
			return
		}
	}
	for c := range maps.Keys(computedTables) {
		if _, ok := idColumns[c]; !ok && !yield(c) {
			return
		}
	}
})

// latestCol is a latest metric's column, over the rows of a mask.
type latestCol struct {
	name string
	mask int
}

// numeric says whether l's column holds numbers, which compare and
// answer as numbers.
func (l latestCol) numeric() bool { return columnType(l.name) == "number" }

// latestPart is one latest column's partials per cell: the ts of the
// latest row with a value, and that value; set says the cell has one.
type latestPart struct {
	ts  []int64
	val []string
	set []bool
	num bool // the values are numbers: a tie compares them so
}

func (lp *latestPart) grow(cells int) {
	lp.ts, lp.val, lp.set = growTo(lp.ts, cells), growTo(lp.val, cells), growTo(lp.set, cells)
}

// later says whether (ts, v) is after (ts0, v0): by time, a tie by the
// greater value (as numbers when num), so workers and merges agree
// whatever their order.
func later(ts int64, v string, ts0 int64, v0 string, num bool) bool {
	if ts != ts0 {
		return ts > ts0
	}
	return cmpLatest(v, v0, num) > 0
}

// cmpLatest compares two latest values, as numbers when num.
func cmpLatest(a, b string, num bool) int {
	if num {
		x, errA := strconv.ParseFloat(a, 64)
		y, errB := strconv.ParseFloat(b, 64)
		if errA == nil && errB == nil {
			return cmp.Compare(x, y)
		}
	}
	return cmp.Compare(a, b)
}

// put keeps (ts, v) in cell x when it is later than what x holds, a copy
// of v (it may share a file dictionary's backing string), and returns
// the bytes the cell grew by.
func (lp *latestPart) put(x int, ts int64, v string) int64 {
	if lp.set[x] && !later(ts, v, lp.ts[x], lp.val[x], lp.num) {
		return 0
	}
	grew := int64(len(v) - len(lp.val[x]))
	lp.ts[x], lp.val[x], lp.set[x] = ts, strings.Clone(v), true
	return grew
}

// latestText is c's value at row i as the answer prints it: text as is, a
// number in decimal, a day as YYYY-MM-DD; ok false for NULL.
func latestText(c *Column, i int, typ colType) (string, bool) {
	if c == nil || (c.Null != nil && c.Null[i]) {
		return "", false
	}
	switch {
	case c.Idx != nil:
		return textAt(c, i)
	case c.Ints != nil && typ == typeDay:
		return formatDay(c.Ints[i]), true
	case c.Ints != nil:
		return strconv.FormatInt(c.Ints[i], 10), true
	case c.Floats != nil:
		return strconv.FormatFloat(c.Floats[i], 'f', -1, 64), true
	}
	return "", false
}

// addLatest adds the rows sel of b (each in cell(k, i), gids[k] < 0 past
// the caps) to p's latest partials.
func (a *agg) addLatest(p *part, b *Batch, sel, gids []int32, pass [][]bool, cell func(int, int32) int) {
	if len(a.latests) == 0 {
		return
	}
	_, ts := b.col("ts")
	if ts == nil || ts.Times == nil {
		return // no row has a time to be the latest by
	}
	for j, l := range a.latests {
		c := batchColumn(b, l.name)
		if c == nil {
			continue
		}
		typ := typeOf(l.name)
		lp := &p.latest[j]
		keep := pass[l.mask]
		for k, i := range sel {
			if gids[k] < 0 || (keep != nil && !keep[i]) || (ts.Null != nil && ts.Null[i]) {
				continue
			}
			if v, ok := latestText(c, int(i), typ); ok {
				p.grew += lp.put(cell(k, i), ts.Times[i], v)
			}
		}
	}
}

// mergeLatest folds src's cells into dst's.
func mergeLatest(dst, src *latestPart, cells int) {
	for x := range min(len(src.set), cells) {
		if src.set[x] && (!dst.set[x] || later(src.ts[x], src.val[x], dst.ts[x], dst.val[x], dst.num)) {
			dst.ts[x], dst.val[x], dst.set[x] = src.ts[x], src.val[x], true // src's own copy
		}
	}
}

// latestOf is a latest metric's value over gs (on day, unless it is <
// 0): the latest of their cells, NULL over none.
func (m *merged) latestOf(mt metricSpec, gs []int32, day int) value {
	lp := &m.latest[mt.latest]
	found := false
	var ts int64
	var v string
	for _, g := range gs {
		m.cellsOf(g, day, func(x int) {
			if lp.set[x] && (!found || later(lp.ts[x], lp.val[x], ts, v, lp.num)) {
				ts, v, found = lp.ts[x], lp.val[x], true
			}
		})
	}
	if !found {
		return value{null: true}
	}
	if lp.num { // a number, which ranks as one
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return intValue(n)
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return value{f: f}
		}
	}
	return value{text: true, s: v}
}
