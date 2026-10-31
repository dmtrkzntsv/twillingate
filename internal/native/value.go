package native

import (
	"cmp"
	"math"
	"strconv"
	"time"
)

func formatDay(d int64) string { return time.Unix(d*86400, 0).UTC().Format(time.DateOnly) }

// value is one metric's result: NULL, an integer or a float, and whether
// a sketch estimated it. form says how an integer reads: a number, a day
// (YYYY-MM-DD) or a time (RFC 3339 in UTC, its fraction only when it has
// one, trailing zeros cut).
type value struct {
	null, isInt, approx bool
	form                int
	i                   int64
	f                   float64
	text                bool // a latest's value, s
	s                   string
}

func intValue(n int64) value { return value{isInt: true, i: n} }

func (v value) String() string {
	switch {
	case v.null:
		return ""
	case v.text:
		return v.s
	case v.form == formDay:
		return formatDay(v.i)
	case v.form == formTime:
		return time.UnixMicro(v.i).UTC().Format(time.RFC3339Nano)
	case v.isInt:
		return strconv.FormatInt(v.i, 10)
	}
	return strconv.FormatFloat(v.f, 'f', -1, 64)
}

// round is v rounded to *places decimals, as DuckDB's ROUND rounds a
// DOUBLE: half away from zero, of v times 10^places; an integer, NULL, or
// places nil leave v as it is.
func (v value) round(places *int) value {
	if places == nil || v.null || v.isInt {
		return v
	}
	mod := math.Pow(10, float64(*places))
	if r := math.Round(v.f*mod) / mod; !math.IsInf(r, 0) && !math.IsNaN(r) {
		v.f = r
	}
	return v
}

func (v value) num() float64 {
	if v.isInt {
		return float64(v.i)
	}
	return v.f
}

// cmpDesc orders values descending, NULLs last.
func cmpDesc(a, b value) int {
	switch {
	case a.null || b.null:
		return cmp.Compare(boolInt(a.null), boolInt(b.null))
	case a.isInt && b.isInt:
		return cmp.Compare(b.i, a.i)
	case a.text || b.text: // latest's text values, after the numbers
		if c := cmp.Compare(boolInt(a.text), boolInt(b.text)); c != 0 {
			return c
		}
		return cmp.Compare(b.s, a.s)
	}
	return cmp.Compare(b.num(), a.num())
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// cmpDim orders dimension values ascending, NULLs last, numbers by value.
func cmpDim(typ colType, a, b dimVal) int {
	switch {
	case a.null || b.null:
		return cmp.Compare(boolInt(a.null), boolInt(b.null))
	case typ == typeInt || typ == typeBucket:
		return cmp.Compare(a.n, b.n)
	}
	return cmp.Compare(a.s, b.s)
}
