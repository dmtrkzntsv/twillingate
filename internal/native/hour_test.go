package native

import (
	"context"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// TestHourAndWeekday: a breakdown by weekday and hour, the UTC ones of
// ts, splits the rows of the range in full: two-digit hours, weekdays
// from 1 Mon, a row's weekday its ts's.
func TestHourAndWeekday(t *testing.T) {
	f := engineFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	p := Params{Project: 1, From: date(t, engineFrom), To: date(t, today)}
	total, err := e.Answer(context.Background(), Query{Kind: "stat", Table: "views", Metrics: []Metric{{Op: "count"}}}, p, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	grid, err := e.Answer(context.Background(), Query{Kind: "breakdown", Table: "views", Dimensions: []string{"weekday", "hour"},
		Limit: 1000, Metrics: []Metric{{Op: "count"}, {Op: "max", Column: "ts"}}}, p, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	hour, day := regexp.MustCompile(`^([01][0-9]|2[0-3])$`), regexp.MustCompile(`^[1-7] (Mon|Tue|Wed|Thu|Fri|Sat|Sun)$`)
	var sum int64
	for _, r := range grid.Rows {
		if !day.MatchString(r[0]) || !hour.MatchString(r[1]) {
			t.Errorf("row %v: weekday or hour malformed", r)
		}
		n, _ := strconv.ParseInt(r[2], 10, 64)
		sum += n
		ts, err := time.Parse(time.RFC3339Nano, r[3])
		if err != nil {
			t.Fatal(err)
		}
		if got := weekdays[(int(ts.Weekday())+6)%7]; got != r[0] || ts.Format("15") != r[1] {
			t.Errorf("row %v: its latest ts %s is on %s at %s", r, r[3], got, ts.Format("15"))
		}
	}
	if strconv.FormatInt(sum, 10) != total.Rows[0][0] {
		t.Errorf("the grid holds %d rows, the range %s", sum, total.Rows[0][0])
	}
}
