package alerts

import (
	"strconv"
	"strings"
)

// formatValue is an alert's number as its read words it: a count as an
// integer with thousands separators, a fraction as a percent with one
// decimal, any other number with at most two decimals (trailing zeros
// trimmed). Nil, no value, is "—".
func formatValue(p Parsed, v *float64) string {
	if v == nil {
		return "—"
	}
	switch p.Read {
	case readCount:
		return formatNumber(*v, 0, false)
	case readFraction:
		return formatNumber(*v*100, 1, false) + "%"
	}
	return formatNumber(*v, 2, true)
}

// formatLine is a threshold's value in the read's unit, trailing zeros
// trimmed: a fraction as a percent (0.03 is 3%), anything else as a
// number with at most two decimals.
func formatLine(p Parsed, v float64) string {
	if p.Read == readFraction {
		return formatNumber(v*100, 2, true) + "%"
	}
	return formatNumber(v, 2, true)
}

// formatNumber is v with decimals places, its trailing zeros trimmed when
// trim, and thousands separators in its integer part. A value that
// rounds to zero is never "-0".
func formatNumber(v float64, decimals int, trim bool) string {
	s := strconv.FormatFloat(v, 'f', decimals, 64)
	if trim && strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	sign := ""
	if rest, ok := strings.CutPrefix(s, "-"); ok {
		s = rest
		if strings.Trim(s, "0.") != "" {
			sign = "-"
		}
	}
	whole, frac, hasFrac := strings.Cut(s, ".")
	if len(whole) > 3 {
		var b strings.Builder
		lead := len(whole) % 3
		if lead > 0 {
			b.WriteString(whole[:lead])
		}
		for i := lead; i < len(whole); i += 3 {
			if b.Len() > 0 {
				b.WriteByte(',')
			}
			b.WriteString(whole[i : i+3])
		}
		whole = b.String()
	}
	if hasFrac {
		return sign + whole + "." + frac
	}
	return sign + whole
}
