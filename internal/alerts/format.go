package alerts

import (
	"math"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// formatValue is an alert's number as its read words it: a count as an
// integer with thousands separators, a fraction as a percent with one
// decimal, any other number with at most two decimals (trailing zeros
// trimmed), a silence alert's age as "last event 7h ago" (whole hours,
// or minutes under one). Nil, no value, is "—".
func formatValue(p Parsed, v *float64) string {
	if v == nil {
		return "—"
	}
	switch p.Read {
	case readSilence:
		age := max(*v, 0)
		if age < 1 {
			return "last event " + strconv.Itoa(int(age*60)) + "m ago"
		}
		return "last event " + strconv.Itoa(int(age)) + "h ago"
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

// message is o's notification text for p: the value, as formatValue
// words it, and the detail, a sentence or two saying the line, the days
// read and what the value was compared with. Broken and error outcomes
// say their reason; no_data says why there was no verdict.
func message(p Parsed, o Outcome) (value, detail string) {
	value = formatValue(p, o.Value)
	switch {
	case o.Outcome == store.AlertBroken || o.Outcome == store.AlertError:
		return value, sentence(o.Reason) + "."
	case p.Read == readSilence:
		return silenceMessage(p, o)
	}
	days := onDays(o.From, o.To)
	noData := func(reason string) (string, string) {
		return value, "Not enough data " + days + " (" + reason + ")."
	}
	if o.Outcome == store.AlertNoData {
		return noData(o.Reason)
	}
	c := p.Def.Condition
	switch c.Type {
	case condChange:
		if o.Value == nil || o.Compare == nil || *o.Compare == 0 {
			return noData("comparison is 0 or empty")
		}
		pct := (*o.Value - *o.Compare) / *o.Compare * 100
		word := "Unchanged"
		if shown := formatNumber(math.Abs(pct), 1, true); shown != "0" {
			word = "Down " + shown + "%"
			if pct > 0 {
				word = "Up " + shown + "%"
			}
		}
		from, to := comparison(p, o.From, o.To)
		return value, word + " " + days + " against " + dayRange(from, to) + " (" + formatValue(p, o.Compare) + ")."
	case condThreshold:
		if c.Value == nil {
			break
		}
		word := sentence(c.Op)
		if o.Outcome != store.AlertBreached {
			word = "Not " + c.Op
		}
		return value, word + " " + formatLine(p, *c.Value) + " " + days + volumeNote(p, o.Volume) + "."
	}
	return value, sentence(days) + "."
}

// silenceMessage is message for a silence alert, read from o.Last and
// o.At: the newest matching event's time, or, with none, the hours its
// window covered.
func silenceMessage(p Parsed, o Outcome) (value, detail string) {
	alertAt := " (alert at " + strconv.Itoa(p.Def.Condition.Hours) + "h)."
	if o.Last.IsZero() {
		// The window read reaches LateDays further back, for events filed
		// before they were received; what it shows is that nothing was
		// received since the start of the day the last hours began on.
		start := civil.DateOf(o.At).AddDays(-silenceDays(p.Def.Condition.Hours)).Time()
		hours := strconv.Itoa(int(max(o.At.Sub(start).Hours(), 0)))
		value = "no event in " + hours + "h"
		detail = "No matching event in the last " + hours + "h" + alertAt
	} else {
		age := o.At.Sub(o.Last).Hours()
		value = formatValue(p, &age)
		last := o.Last.UTC().Format("2006-01-02 15:04") + " UTC"
		detail = "No matching event since " + last + alertAt
		if o.Outcome == store.AlertOK {
			detail = "Last matching event at " + last + alertAt
		}
	}
	if o.Outcome == store.AlertNoData {
		detail = "Not enough data (" + o.Reason + ")."
	}
	return value, detail
}

// onDays is the days from…to as a detail says them: "on 2026-11-23", or
// "from 2026-11-17 to 2026-11-23".
func onDays(from, to civil.Date) string {
	if from == to {
		return "on " + to.String()
	}
	return "from " + dayRange(from, to)
}

// dayRange is "2026-11-16", or "2026-11-10 to 2026-11-16".
func dayRange(from, to civil.Date) string {
	if from == to {
		return to.String()
	}
	return from.String() + " to " + to.String()
}

// volumeNote is the volume a threshold's value was read over, in
// parentheses after the line: a funnel's base step, a stat's volume
// metric; "" without one.
func volumeNote(p Parsed, volume *float64) string {
	if volume == nil {
		return ""
	}
	n := formatNumber(*volume, 2, true)
	switch {
	case p.Kind == "funnel" && p.Def.Read.From == "previous":
		return " (" + n + " at the step before)"
	case p.Kind == "funnel":
		return " (" + n + " at the first step)"
	case p.Volume >= 0:
		return " (volume " + n + ")"
	}
	return ""
}
