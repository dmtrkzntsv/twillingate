package alerts

import (
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestFormatValue(t *testing.T) {
	count, fraction, number := Parsed{Read: readCount}, Parsed{Read: readFraction}, Parsed{Read: readNumber}
	silence := Parsed{Read: readSilence}
	for _, c := range []struct {
		p    Parsed
		v    *float64
		want string
	}{
		{count, ptr(1240.0), "1,240"},
		{count, ptr(812.0), "812"},
		{count, ptr(0.0), "0"},
		{count, ptr(1234567.0), "1,234,567"},
		{count, ptr(-5015.0), "-5,015"},
		{fraction, ptr(0.0213), "2.1%"},
		{fraction, ptr(0.41), "41.0%"},
		{fraction, ptr(0.0), "0.0%"},
		{fraction, ptr(1.0), "100.0%"},
		{number, ptr(1234.567), "1,234.57"},
		{number, ptr(12.5), "12.5"},
		{number, ptr(12.0), "12"},
		{number, ptr(-0.001), "0"},
		{number, ptr(-1234.5), "-1,234.5"},
		{number, nil, "—"},
		{count, nil, "—"},
		{fraction, nil, "—"},
		{Parsed{}, ptr(3.25), "3.25"}, // a definition that no longer parses
		{silence, ptr(7.08), "last event 7h ago"},
		{silence, ptr(30.9), "last event 30h ago"},
		{silence, ptr(0.42), "last event 25m ago"},
		{silence, ptr(0.0), "last event 0m ago"},
		{silence, nil, "—"},
	} {
		if got := formatValue(c.p, c.v); got != c.want {
			t.Errorf("formatValue(%v, %v) = %q, want %q", c.p.Read, deref(c.v), got, c.want)
		}
	}
}

func TestFormatLine(t *testing.T) {
	count, fraction, number := Parsed{Read: readCount}, Parsed{Read: readFraction}, Parsed{Read: readNumber}
	for _, c := range []struct {
		p    Parsed
		v    float64
		want string
	}{
		{fraction, 0.03, "3%"},
		{fraction, 0.025, "2.5%"},
		{fraction, 0.07, "7%"},
		{fraction, 0.5, "50%"},
		{count, 1000, "1,000"},
		{count, 2.5, "2.5"},
		{number, 1234.5, "1,234.5"},
		{number, 0.126, "0.13"},
	} {
		if got := formatLine(c.p, c.v); got != c.want {
			t.Errorf("formatLine(%v, %v) = %q, want %q", c.p.Read, c.v, got, c.want)
		}
	}
}

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func TestMessage(t *testing.T) {
	day := func(s string) civil.Date {
		d, err := civil.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	at := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	threshold := func(op string, v float64) store.AlertCondition {
		return store.AlertCondition{Type: "threshold", Op: op, Value: ptr(v)}
	}
	change := func(direction, against string) store.AlertCondition {
		return store.AlertCondition{Type: "change", Direction: direction, Percent: 30, Against: against}
	}
	conversion := func(cond store.AlertCondition, from string) Parsed {
		in := funnelIn(store.AlertRead{Step: "signup", From: from})
		in.Condition = cond
		return mustParse(t, in)
	}
	count := func(cond store.AlertCondition, window int) Parsed {
		in := statIn(store.AlertRead{}, cond, native.Metric{Op: "count"})
		in.WindowDays = window
		return mustParse(t, in)
	}
	rate := func(cond store.AlertCondition) Parsed {
		return mustParse(t, statIn(store.AlertRead{Metric: "avg_value", Volume: "count"}, cond,
			native.Metric{Op: "avg", Column: "value"}, native.Metric{Op: "count"}))
	}
	silence := mustParse(t, Input{Where: []native.Filter{evt("signup")}, WhereSet: true, Condition: silent6})

	d23, d24 := day("2026-11-23"), day("2026-11-24")
	for _, c := range []struct {
		name          string
		p             Parsed
		o             Outcome
		value, detail string
	}{
		{"funnel threshold breached", conversion(threshold("below", 0.03), ""),
			Outcome{Outcome: store.AlertBreached, From: d23, To: d23, Value: ptr(0.0213), Volume: ptr(1240.0)},
			"2.1%", "Below 3% on 2026-11-23 (1,240 at the first step)."},
		{"funnel threshold ok", conversion(threshold("below", 0.03), ""),
			Outcome{Outcome: store.AlertOK, From: d24, To: d24, Value: ptr(0.034), Volume: ptr(1310.0)},
			"3.4%", "Not below 3% on 2026-11-24 (1,310 at the first step)."},
		{"count threshold over a week", count(threshold("below", 1000), 7),
			Outcome{Outcome: store.AlertBreached, From: day("2026-11-17"), To: d23, Value: ptr(812.0)},
			"812", "Below 1,000 from 2026-11-17 to 2026-11-23."},
		{"count change week ago", count(change("drop", "week_ago"), 1),
			Outcome{Outcome: store.AlertBreached, From: d23, To: d23, Value: ptr(3310.0), Compare: ptr(5015.0), Volume: ptr(5015.0)},
			"3,310", "Down 34% on 2026-11-23 against 2026-11-16 (5,015)."},
		{"funnel from previous", conversion(threshold("below", 0.5), "previous"),
			Outcome{Outcome: store.AlertBreached, From: d23, To: d23, Value: ptr(0.41), Volume: ptr(220.0)},
			"41.0%", "Below 50% on 2026-11-23 (220 at the step before)."},
		{"silence breached", silence,
			Outcome{Outcome: store.AlertBreached, Value: ptr(7.08), Last: at("2026-11-24 06:10"), At: at("2026-11-24 13:15")},
			"last event 7h ago", "No matching event since 2026-11-24 06:10 UTC (alert at 6h)."},
		{"silence none in window", silence,
			Outcome{Outcome: store.AlertBreached, At: at("2026-11-24 06:20")},
			"no event in 30h", "No matching event in the last 30h (alert at 6h)."},

		// Beyond the pinned rows.
		{"silence ok", silence,
			Outcome{Outcome: store.AlertOK, Value: ptr(2.0), Last: at("2026-11-24 11:15"), At: at("2026-11-24 13:15")},
			"last event 2h ago", "Last matching event at 2026-11-24 11:15 UTC (alert at 6h)."},
		{"silence never sent", silence,
			Outcome{Outcome: store.AlertNoData, Reason: "never sent: signup", At: at("2026-11-24 13:15")},
			"no event in 37h", "Not enough data (never sent: signup)."},
		{"threshold above ok", count(threshold("above", 1000), 1),
			Outcome{Outcome: store.AlertOK, From: d23, To: d23, Value: ptr(812.0)},
			"812", "Not above 1,000 on 2026-11-23."},
		{"rate threshold", rate(threshold("above", 12.5)),
			Outcome{Outcome: store.AlertBreached, From: d23, To: d23, Value: ptr(13.25), Volume: ptr(1240.0)},
			"13.25", "Above 12.5 on 2026-11-23 (volume 1,240)."},
		{"change rise over a week, previous", count(change("rise", "previous"), 7),
			Outcome{Outcome: store.AlertBreached, From: day("2026-11-17"), To: d23, Value: ptr(1400.0), Compare: ptr(1000.0)},
			"1,400", "Up 40% from 2026-11-17 to 2026-11-23 against 2026-11-10 to 2026-11-16 (1,000)."},
		{"change either, unchanged", conversion(change("either", "previous"), ""),
			Outcome{Outcome: store.AlertOK, From: d23, To: d23, Value: ptr(0.04), Compare: ptr(0.04)},
			"4.0%", "Unchanged on 2026-11-23 against 2026-11-22 (4.0%)."},
		{"no data", conversion(threshold("below", 0.03), ""),
			Outcome{Outcome: store.AlertNoData, Reason: "below min_volume", From: d23, To: d23, Value: ptr(0.5), Volume: ptr(4.0)},
			"50.0%", "Not enough data on 2026-11-23 (below min_volume)."},
		{"change with no comparison", count(change("drop", "previous"), 1),
			Outcome{Outcome: store.AlertNoData, Reason: "comparison is 0 or empty", From: d23, To: d23, Value: ptr(5.0), Compare: ptr(0.0)},
			"5", "Not enough data on 2026-11-23 (comparison is 0 or empty)."},
		{"broken", count(threshold("below", 5), 1),
			Outcome{Outcome: store.AlertBroken, Reason: "Blocked: signup", From: d23, To: d23},
			"—", "Blocked: signup."},
		{"error", Parsed{},
			Outcome{Outcome: store.AlertError, Reason: "context deadline exceeded"},
			"—", "Context deadline exceeded."},
	} {
		value, detail := message(c.p, c.o)
		if value != c.value || detail != c.detail {
			t.Errorf("%s: message = %q, %q; want %q, %q", c.name, value, detail, c.value, c.detail)
		}
	}
}
