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

// readColumns are the columns a scan reads for column: value for bucket,
// a computed column's sources, a status column's id (with day, which a
// scan always reads; the scan adds the status itself, from identities).
func readColumns(column string) []string {
	if c, ok := computedColumns[column]; ok {
		return c.sources
	}
	if column == "bucket" {
		return []string{"value"}
	}
	if s, ok := statusColumns[column]; ok {
		return []string{s.id}
	}
	return []string{column}
}

// batchColumn is b's column for column: computed from its sources for a
// computed column, NULL values for a bucket b has no value column for, nil
// when b lacks it (every row NULL).
func batchColumn(b *Batch, column string) *Column {
	if c, ok := computedColumns[column]; ok {
		return c.compute(b)
	}
	if column == "bucket" {
		if _, c := b.col("value"); c != nil {
			return c
		}
		return nullColumn("value", b.N) // NULL values: bucket -1000
	}
	_, c := b.col(column)
	return c
}

// computedColumn is a text column computed per row from source columns,
// as the system widgets' SQL computes it.
type computedColumn struct {
	sources []string
	compute func(b *Batch) *Column
}

// computedColumns are the computed dimension and filter columns:
//   - resolution: display_width || 'x' || display_height when both are
//     above 0, else NULL;
//   - device_label: device_model when not empty, else device when not
//     empty, else NULL;
//   - platform_version: platform (NULL read as empty), a space, then
//     app_version, when app_version is not empty, else NULL;
//   - consent_state: given (consent 1), none (0), else unknown.
var computedColumns = map[string]computedColumn{
	"resolution":       {[]string{"display_width", "display_height"}, resolutionColumn},
	"device_label":     {[]string{"device_model", "device"}, deviceLabelColumn},
	"platform_version": {[]string{"platform", "app_version"}, platformVersionColumn},
	"consent_state":    {[]string{"consent"}, consentStateColumn},
}

// textDict builds a computed text column's dictionary.
type textDict struct {
	ids  map[string]int32
	dict []string
}

func (d *textDict) id(s string) int32 {
	if id, ok := d.ids[s]; ok {
		return id
	}
	if d.ids == nil {
		d.ids = map[string]int32{}
	}
	id := int32(len(d.dict))
	d.ids[s] = id
	d.dict = append(d.dict, s)
	return id
}

// textAt is c's value at row i; ok false for NULL (c nil: every row).
func textAt(c *Column, i int) (s string, ok bool) {
	if c == nil || c.Idx == nil || c.Idx[i] < 0 {
		return "", false
	}
	return c.Dict[c.Idx[i]], true
}

// intAt is c's value at row i; ok false for NULL (c nil: every row).
func intAt(c *Column, i int) (n int64, ok bool) {
	if c == nil || c.Ints == nil || (c.Null != nil && c.Null[i]) {
		return 0, false
	}
	return c.Ints[i], true
}

// textColumn is a column of b.N rows, each value(i)'s text or NULL.
func textColumn(n int, value func(i int) (string, bool)) *Column {
	var d textDict
	idx := make([]int32, n)
	for i := range idx {
		if s, ok := value(i); ok {
			idx[i] = d.id(s)
		} else {
			idx[i] = -1
		}
	}
	return &Column{Dict: d.dict, Idx: idx}
}

func resolutionColumn(b *Batch) *Column {
	_, w := b.col("display_width")
	_, h := b.col("display_height")
	type size struct{ w, h int64 }
	names := map[size]string{}
	return textColumn(b.N, func(i int) (string, bool) {
		x, ok1 := intAt(w, i)
		y, ok2 := intAt(h, i)
		if !ok1 || !ok2 || x <= 0 || y <= 0 {
			return "", false
		}
		s, ok := names[size{x, y}]
		if !ok {
			s = strconv.FormatInt(x, 10) + "x" + strconv.FormatInt(y, 10)
			names[size{x, y}] = s
		}
		return s, true
	})
}

func deviceLabelColumn(b *Batch) *Column {
	_, model := b.col("device_model")
	_, device := b.col("device")
	return textColumn(b.N, func(i int) (string, bool) {
		if s, ok := textAt(model, i); ok && s != "" {
			return s, true
		}
		if s, ok := textAt(device, i); ok && s != "" {
			return s, true
		}
		return "", false
	})
}

func platformVersionColumn(b *Batch) *Column {
	_, platform := b.col("platform")
	_, version := b.col("app_version")
	type pair struct{ p, v string }
	names := map[pair]string{}
	return textColumn(b.N, func(i int) (string, bool) {
		v, ok := textAt(version, i)
		if !ok || v == "" {
			return "", false
		}
		p, _ := textAt(platform, i)
		s, ok := names[pair{p, v}]
		if !ok {
			s = p + " " + v
			names[pair{p, v}] = s
		}
		return s, true
	})
}

func consentStateColumn(b *Batch) *Column {
	_, c := b.col("consent")
	return textColumn(b.N, func(i int) (string, bool) {
		switch n, ok := intAt(c, i); {
		case ok && n == 1:
			return "given", true
		case ok && n == 0:
			return "none", true
		}
		return "unknown", true
	})
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
// value that is not one refuses the query, naming it under path (where,
// metrics[1].where).
func compileFilters(where []Filter, path string) ([]filter, error) {
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
			return nil, invalid(fmt.Sprintf("%s[%d].op", path, i), "%s is not allowed on %s", f.Op, f.Column)
		}
		c.ints = make(map[int64]bool, len(f.Values))
		for _, v := range f.Values {
			n, err := parseValue(c.typ, v)
			if err != nil {
				return nil, invalid(fmt.Sprintf("%s[%d].values", path, i), "%v", err)
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
	c := batchColumn(b, f.column)
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

// passRows is, per row of b, whether it passes every filter, in keep
// (reused when it holds b.N rows).
func passRows(b *Batch, filters []filter, keep []bool) ([]bool, error) {
	if cap(keep) < b.N {
		keep = make([]bool, b.N)
	}
	keep = keep[:b.N]
	for i := range keep {
		keep[i] = true
	}
	for i := range filters {
		if err := filters[i].apply(b, keep); err != nil {
			return nil, err
		}
	}
	return keep, nil
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
	keep, err := passRows(b, filters, nil)
	if err != nil {
		return nil, err
	}
	for i, k := range keep {
		if k {
			sel = append(sel, int32(i))
		}
	}
	return sel, nil
}
