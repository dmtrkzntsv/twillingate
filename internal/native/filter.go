package native

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
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
	case column == "day", isFirstSeen(column):
		return typeDay
	case plainColumns[column]:
		return typeInt
	}
	return typeString
}

// readColumns are the columns a scan reads for column: value for bucket,
// a computed column's sources, a computed identity column's id (with
// day, which a scan always reads; the scan adds the column itself, from
// identities).
func readColumns(column string) []string {
	if c, ok := computedColumns[column]; ok {
		return c.sources
	}
	if column == "bucket" {
		return []string{"value"}
	}
	if s, ok := idColumns[column]; ok {
		return []string{s.id}
	}
	return []string{column}
}

// appendReadColumns is cols with the columns a scan reads for fs'
// columns (readColumns) appended, each that cols lacks once, in order.
func appendReadColumns(cols []string, fs []filter) []string {
	for _, f := range fs {
		for _, col := range f.columns() {
			cols = appendNew(cols, readColumns(col)...)
		}
	}
	return cols
}

// appendNew is cols with each of cs that it lacks appended, once.
func appendNew(cols []string, cs ...string) []string {
	for _, c := range cs {
		if !slices.Contains(cols, c) {
			cols = append(cols, c)
		}
	}
	return cols
}

// batchColumn is b's column for column: computed from its sources for a
// computed column, NULL values for a bucket b has no value column for, nil
// when b lacks it (every row NULL).
func batchColumn(b *batch, column string) *column {
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
	compute func(b *batch) *column
}

// computedColumns are the computed dimension and filter columns:
//   - resolution: display_width || 'x' || display_height when both are
//     above 0, else NULL;
//   - device_label: device_model when not empty, else device when not
//     empty, else NULL;
//   - platform_version: platform (NULL read as empty), a space, then
//     app_version, when app_version is not empty, else NULL;
//   - consent_state: given (consent 1), none (0), else unknown;
//   - vital: upper(substr(event_name, 2)) for the vitals (vitals), else
//     NULL;
//   - vital_rating: a vital's sample rated against its thresholds by its
//     bucket's value (bucketValue): good up to the first, needs
//     improvement up to the second, else poor; NULL for the others.
var computedColumns = map[string]computedColumn{
	"vital":            {[]string{"event_name"}, vitalColumn},
	"vital_rating":     {[]string{"event_name", "value"}, vitalRatingColumn},
	"resolution":       {[]string{"display_width", "display_height"}, resolutionColumn},
	"device_label":     {[]string{"device_model", "device"}, deviceLabelColumn},
	"platform_version": {[]string{"platform", "app_version"}, platformVersionColumn},
	"consent_state":    {[]string{"consent"}, consentStateColumn},
	"hour":             {[]string{"ts"}, hourColumn},
	"weekday":          {[]string{"ts"}, weekdayColumn},
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
func textAt(c *column, i int) (s string, ok bool) {
	if c == nil || c.Idx == nil || c.Idx[i] < 0 {
		return "", false
	}
	return c.Dict[c.Idx[i]], true
}

// intAt is c's value at row i; ok false for NULL (c nil: every row).
func intAt(c *column, i int) (n int64, ok bool) {
	if c == nil || c.Ints == nil || (c.Null != nil && c.Null[i]) {
		return 0, false
	}
	return c.Ints[i], true
}

// textColumn is a column of b.N rows, each value(i)'s text or NULL.
func textColumn(n int, value func(i int) (string, bool)) *column {
	var d textDict
	idx := make([]int32, n)
	for i := range idx {
		if s, ok := value(i); ok {
			idx[i] = d.id(s)
		} else {
			idx[i] = -1
		}
	}
	return &column{Dict: d.dict, Idx: idx}
}

func resolutionColumn(b *batch) *column {
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

func deviceLabelColumn(b *batch) *column {
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

func platformVersionColumn(b *batch) *column {
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

func consentStateColumn(b *batch) *column {
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

// hourColumn is the UTC hour of ts, 00 to 23, two digits so the hours
// sort as text; NULL without a ts.
func hourColumn(b *batch) *column {
	_, c := b.col("ts")
	return textColumn(b.N, func(i int) (string, bool) {
		if c == nil || c.Times == nil || (c.Null != nil && c.Null[i]) {
			return "", false
		}
		return fmt.Sprintf("%02d", time.UnixMicro(c.Times[i]).UTC().Hour()), true
	})
}

// weekdays are weekday's values, Monday first: the day's number from 1
// so they sort as text, and its name.
var weekdays = [7]string{"1 Mon", "2 Tue", "3 Wed", "4 Thu", "5 Fri", "6 Sat", "7 Sun"}

// weekdayColumn is the UTC weekday of ts (weekdays); NULL without a ts.
func weekdayColumn(b *batch) *column {
	_, c := b.col("ts")
	return textColumn(b.N, func(i int) (string, bool) {
		if c == nil || c.Times == nil || (c.Null != nil && c.Null[i]) {
			return "", false
		}
		return weekdays[(int(time.UnixMicro(c.Times[i]).UTC().Weekday())+6)%7], true
	})
}

// vitalThreshold is a vital's name (vital's value) and the two values a
// sample's bucket value is rated good and needs improvement up to, as
// vitals/ratings.sql has them.
type vitalThreshold struct {
	name       string
	good, poor float64
}

// vitals are the web vitals by event_name.
var vitals = map[string]vitalThreshold{
	"$lcp":  {"LCP", 2500, 4000},
	"$inp":  {"INP", 200, 500},
	"$cls":  {"CLS", 0.1, 0.25},
	"$fcp":  {"FCP", 1800, 3000},
	"$ttfb": {"TTFB", 800, 1800},
}

// vitalOf is the vital of b's event_name dictionary entries (nil when b
// has no event_name): per entry, its threshold, or nil.
func vitalOf(b *batch) (*column, []*vitalThreshold) {
	_, c := b.col("event_name")
	if c == nil || c.Idx == nil {
		return nil, nil
	}
	ts := make([]*vitalThreshold, len(c.Dict))
	for j, s := range c.Dict {
		if v, ok := vitals[s]; ok {
			ts[j] = &v
		}
	}
	return c, ts
}

func vitalColumn(b *batch) *column {
	names, ts := vitalOf(b)
	return textColumn(b.N, func(i int) (string, bool) {
		if names == nil || names.Idx[i] < 0 || ts[names.Idx[i]] == nil {
			return "", false
		}
		return ts[names.Idx[i]].name, true
	})
}

func vitalRatingColumn(b *batch) *column {
	names, ts := vitalOf(b)
	_, val := b.col("value")
	return textColumn(b.N, func(i int) (string, bool) {
		if names == nil || names.Idx[i] < 0 || ts[names.Idx[i]] == nil {
			return "", false
		}
		t := ts[names.Idx[i]]
		v, null := 0.0, true
		switch {
		case val == nil || (val.Null != nil && val.Null[i]):
		case val.Floats != nil:
			v, null = val.Floats[i], false
		case val.Ints != nil:
			v, null = float64(val.Ints[i]), false
		}
		switch x := bucketValue(bucketOf(v, null)); {
		case x <= t.good:
			return "good", true
		case x <= t.poor:
			return "needs improvement", true
		}
		return "poor", true
	})
}

// bucketOf is measures' bucket of a value, as the measures table's bucket
// column holds it: ceil(ln(value)/ln(1.04)) above 1e-17, else (NULL among
// them) -1000.
func bucketOf(v float64, null bool) int64 {
	if !null && v > 1e-17 {
		return int64(math.Ceil(math.Log(v) / math.Log(1.04)))
	}
	return -1000
}

// filter is one Filter compiled: its values parsed for the column's type.
// A NULL matches no operator, as in SQL (ne and not_in leave NULLs out
// too); is_set is NOT NULL and, for text, not empty; any_set is one is_set
// per column in anyOf, of which a row passes one.
type filter struct {
	column string
	anyOf  []filter
	// ever and never's: its where, its days and the ids found (filled
	// by collectEver before the scan).
	inner []filter
	over  string
	ids   *idSet
	typ   colType
	op    string
	strs  map[string]bool
	str   string
	ints  map[int64]bool
	num   int64
}

// compileFilters parses the filters' values for their columns' types; a
// value that is not one refuses the query, naming it under path (where,
// metrics[1].where).
func compileFilters(where []Filter, path string) ([]filter, error) {
	out := make([]filter, len(where))
	for i, f := range where {
		if f.Op == "ever" || f.Op == "never" {
			inner, err := compileFilters(f.Where, fmt.Sprintf("%s[%d].where", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = filter{op: f.Op, column: everColumn(f.By), typ: typeString, inner: inner, over: f.Over, ids: &idSet{}}
			continue
		}
		if f.Op == "any_of" {
			anyOf, err := compileFilters(f.Any, fmt.Sprintf("%s[%d].any", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = filter{op: f.Op, anyOf: anyOf}
			continue
		}
		if f.Op == "any_set" {
			each := make([]Filter, len(f.Columns))
			for j, col := range f.Columns {
				each[j] = Filter{Column: col, Op: "is_set"}
			}
			anyOf, err := compileFilters(each, fmt.Sprintf("%s[%d].columns", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = filter{op: f.Op, anyOf: anyOf}
			continue
		}
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
		if f.Op == "contains" || f.Op == "prefix" || f.Op == "not_prefix" {
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
		d, err := archive.ParseDay(v)
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
	case "not_prefix":
		return !strings.HasPrefix(s, f.str)
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

// columns are the filter's columns: any_set's or any_of's, each once,
// else its one.
func (f *filter) columns() []string {
	if f.anyOf == nil {
		return []string{f.column}
	}
	var out []string
	for i := range f.anyOf {
		for _, c := range f.anyOf[i].columns() {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return out
}

// apply clears keep[i] for the rows of b that do not pass.
func (f *filter) apply(b *batch, keep []bool) error {
	if f.anyOf != nil {
		return f.applyAny(b, keep)
	}
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
		if f.ids != nil { // ever: the ids found; never: the others; an empty id neither
			for j, s := range c.Dict {
				pass[j] = s != "" && f.ids.has(s) == (f.op == "ever")
			}
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

// applyAny clears keep[i] for the rows of b where none of any_set's
// columns is set, or none of any_of's filters passes.
func (f *filter) applyAny(b *batch, keep []bool) error {
	set := make([]bool, b.N)
	one := make([]bool, b.N)
	for j := range f.anyOf {
		for i := range one {
			one[i] = true
		}
		if err := f.anyOf[j].apply(b, one); err != nil {
			return err
		}
		for i, ok := range one {
			set[i] = set[i] || ok
		}
	}
	for i, ok := range set {
		if !ok {
			keep[i] = false
		}
	}
	return nil
}

// passRows is, per row of b, whether it passes every filter, in keep
// (reused when it holds b.N rows).
func passRows(b *batch, filters []filter, keep []bool) ([]bool, error) {
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
func selectRows(b *batch, filters []filter, sel []int32) ([]int32, error) {
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
