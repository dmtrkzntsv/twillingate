package native

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// colType is how the engine reads a dimension, series or filter column.
type colType int

const (
	typeString colType = iota
	typeInt            // BIGINT: Ints
	typeDay            // DATE: Ints, days since 1970-01-01, printed YYYY-MM-DD
	typeBucket         // measures' bucket, computed from value
)

// typeOf is column's type; Validate has checked that the table has it.
func typeOf(column string) colType {
	switch {
	case column == "bucket":
		return typeBucket
	case column == "day":
		return typeDay
	case plainColumns[column]:
		return typeInt
	}
	return typeString
}

// readColumn is the column a scan reads for column: value for bucket.
func readColumn(column string) string {
	if column == "bucket" {
		return "value"
	}
	return column
}

// bucketOf is measures' bucket of a value, as the reader's measures view
// computes it: ceil(ln(value)/ln(1.04)) above 1e-17, else (NULL among
// them) -1000.
func bucketOf(v float64, null bool) int64 {
	if !null && v > 1e-17 {
		return int64(math.Ceil(math.Log(v) / math.Log(1.04)))
	}
	return -1000
}

// filter is one Filter compiled: its values parsed for the column's type.
// A NULL matches no operator, as in SQL (ne and not_in leave NULLs out
// too); is_set is NOT NULL and, for text, not empty.
type filter struct {
	column string
	typ    colType
	op     string
	strs   map[string]bool
	str    string
	ints   map[int64]bool
	num    int64
}

// compileFilters parses the filters' values for their columns' types; a
// value that is not one refuses the query, naming it.
func compileFilters(where []Filter) ([]filter, error) {
	out := make([]filter, len(where))
	for i, f := range where {
		c := filter{column: f.Column, typ: typeOf(f.Column), op: f.Op}
		if c.typ == typeString {
			c.strs = make(map[string]bool, len(f.Values))
			for _, v := range f.Values {
				c.strs[v] = true
			}
			if len(f.Values) > 0 {
				c.str = f.Values[0]
			}
			out[i] = c
			continue
		}
		if f.Op == "contains" || f.Op == "prefix" {
			return nil, invalid(fmt.Sprintf("where[%d].op", i), "%s is not allowed on %s", f.Op, f.Column)
		}
		c.ints = make(map[int64]bool, len(f.Values))
		for _, v := range f.Values {
			n, err := parseValue(c.typ, v)
			if err != nil {
				return nil, invalid(fmt.Sprintf("where[%d].values", i), "%v", err)
			}
			c.ints[n] = true
			c.num = n
		}
		out[i] = c
	}
	return out, nil
}

func parseValue(t colType, v string) (int64, error) {
	if t == typeDay {
		d, err := parseDay(v)
		if err != nil {
			return 0, fmt.Errorf("%q is not a YYYY-MM-DD date", v)
		}
		return d, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not an integer", v)
	}
	return n, nil
}

// matchString says whether a non-null text value passes.
func (f *filter) matchString(s string) bool {
	switch f.op {
	case "eq":
		return s == f.str
	case "ne":
		return s != f.str
	case "in":
		return f.strs[s]
	case "not_in":
		return !f.strs[s]
	case "contains":
		return strings.Contains(s, f.str)
	case "prefix":
		return strings.HasPrefix(s, f.str)
	case "is_set":
		return s != ""
	}
	return false // gt and the like: Validate refuses them on text
}

// matchInt says whether a non-null number passes.
func (f *filter) matchInt(n int64) bool {
	switch f.op {
	case "eq":
		return n == f.num
	case "ne":
		return n != f.num
	case "in":
		return f.ints[n]
	case "not_in":
		return !f.ints[n]
	case "gt":
		return n > f.num
	case "gte":
		return n >= f.num
	case "lt":
		return n < f.num
	case "lte":
		return n <= f.num
	case "is_set":
		return true
	}
	return false
}

// apply clears keep[i] for the rows of b that do not pass.
func (f *filter) apply(b *Batch, keep []bool) error {
	_, c := b.col(readColumn(f.column))
	if c == nil && f.typ == typeBucket {
		c = nullColumn("value", b.N) // NULL values: bucket -1000
	}
	if c == nil {
		clear(keep) // all NULL: nothing passes
		return nil
	}
	switch f.typ {
	case typeString:
		if c.Idx == nil {
			return fmt.Errorf("native: column %s is not text", f.column)
		}
		pass := make([]bool, len(c.Dict))
		for j, s := range c.Dict {
			pass[j] = f.matchString(s)
		}
		for i, x := range c.Idx {
			if x < 0 || !pass[x] {
				keep[i] = false
			}
		}
	case typeBucket:
		if c.Floats == nil {
			return fmt.Errorf("native: column value is not a number")
		}
		for i, v := range c.Floats {
			if !f.matchInt(bucketOf(v, c.Null != nil && c.Null[i])) {
				keep[i] = false
			}
		}
	default:
		if c.Ints == nil {
			return fmt.Errorf("native: column %s is not a number", f.column)
		}
		for i, n := range c.Ints {
			if (c.Null != nil && c.Null[i]) || !f.matchInt(n) {
				keep[i] = false
			}
		}
	}
	return nil
}

// selectRows is the rows of b that pass every filter, appended to sel[:0].
func selectRows(b *Batch, filters []filter, sel []int32) ([]int32, error) {
	sel = sel[:0]
	if len(filters) == 0 {
		for i := range b.N {
			sel = append(sel, int32(i))
		}
		return sel, nil
	}
	keep := make([]bool, b.N)
	for i := range keep {
		keep[i] = true
	}
	for i := range filters {
		if err := filters[i].apply(b, keep); err != nil {
			return nil, err
		}
	}
	for i, k := range keep {
		if k {
			sel = append(sel, int32(i))
		}
	}
	return sel, nil
}
