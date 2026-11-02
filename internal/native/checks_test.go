package native

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// checksFixture holds, on two days, 2 product events and 3 checks rows
// (two down: timeout and status 500), as the log and, after Maintain, as
// Parquet.
func checksFixture(t *testing.T, maintain bool) *Engine {
	t.Helper()
	f := archivetest.New(t)
	base := date(t, "2026-09-08").Time()
	var evs []store.Event
	for k := range 2 {
		evs = append(evs, store.Event{ID: fmt.Sprintf("p%d", k), ProjectID: 1, Family: store.FamilyProduct, EventName: "buy",
			TS: base.Add(time.Duration(k) * time.Hour), ActorID: "a", Attributes: map[string]string{"plan": "pro"}})
	}
	for k, c := range []struct{ up, reason string }{{"1", ""}, {"0", "timeout"}, {"0", "status"}} {
		v := float64(100 * (k + 1))
		attrs := map[string]string{"up": c.up}
		if c.reason != "" {
			attrs["reason"] = c.reason
		}
		evs = append(evs, store.Event{ID: fmt.Sprintf("c%d", k), ProjectID: 1, Family: store.FamilyChecks, EventName: "uptime",
			TS: base.Add(time.Duration(k) * 24 * time.Hour / 2), Value: &v, Attributes: attrs})
	}
	for _, e := range evs {
		f.Write(t, e)
	}
	if maintain {
		f.Maintain(t, today)
	}
	return New(Options{Dir: f.Dir, Workers: 2})
}

func checksRows(t *testing.T, e *Engine, q Query) [][]string {
	t.Helper()
	res, err := e.Answer(context.Background(), q, Params{Project: 1, From: date(t, "2026-09-01"), To: date(t, today)}, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	return res.Rows
}

// TestEventsTableLeavesChecksOut: events reads every family but checks,
// from the log and from Parquet; checks reads only itself.
func TestEventsTableLeavesChecksOut(t *testing.T) {
	for _, maintain := range []bool{false, true} {
		t.Run(fmt.Sprintf("parquet=%v", maintain), func(t *testing.T) {
			e := checksFixture(t, maintain)
			count := func(table string) string {
				return checksRows(t, e, Query{Kind: "stat", Table: table, Metrics: []Metric{{Op: "count"}}})[0][0]
			}
			if got := count("events"); got != "2" {
				t.Errorf("events count = %s, want 2", got)
			}
			if got := count("checks"); got != "3" {
				t.Errorf("checks count = %s, want 3", got)
			}
			rows := checksRows(t, e, Query{Kind: "breakdown", Table: "checks", Dimensions: []string{"attr_reason"}, Limit: 10,
				Where:   []Filter{{Column: "attr_up", Op: "eq", Values: []string{"0"}}},
				Metrics: []Metric{{Op: "count"}}})
			var reasons []string
			for _, r := range rows {
				reasons = append(reasons, r[0])
			}
			slices.Sort(reasons)
			if !slices.Equal(reasons, []string{"status", "timeout"}) {
				t.Errorf("down reasons = %v", reasons)
			}
			// Uptime: up checks over all, as a ratio.
			rows = checksRows(t, e, Query{Kind: "stat", Table: "checks", Metrics: []Metric{{Op: "ratio", As: "up",
				Num: &Metric{Op: "count", Where: []Filter{{Column: "attr_up", Op: "eq", Values: []string{"1"}}}}, Den: &Metric{Op: "count"}}}})
			if f, _ := strconv.ParseFloat(rows[0][0], 64); f < 0.33 || f > 0.34 {
				t.Errorf("up ratio = %s, want 1/3", rows[0][0])
			}
			// By hour and weekday, checks like the other event tables.
			if rows := checksRows(t, e, Query{Kind: "breakdown", Table: "checks", Dimensions: []string{"hour"}, Limit: 10,
				Metrics: []Metric{{Op: "count"}}}); len(rows) == 0 {
				t.Error("no rows by hour")
			}
		})
	}
}

// TestChecksNotInAttributeKeys: the keys of events leave checks' out, and
// checks has its own.
func TestChecksNotInAttributeKeys(t *testing.T) {
	for _, maintain := range []bool{false, true} {
		e := checksFixture(t, maintain)
		keys := func(table string) []string {
			var out []string
			for _, r := range checksRows(t, e, Query{Kind: "attributes", Table: table, Output: "keys", Limit: 50}) {
				out = append(out, r[0])
			}
			slices.Sort(out)
			return out
		}
		if got := keys("events"); !slices.Equal(got, []string{"plan"}) {
			t.Errorf("parquet=%v: events keys = %v, want [plan]", maintain, got)
		}
		if got := keys("checks"); !slices.Equal(got, []string{"reason", "up"}) {
			t.Errorf("parquet=%v: checks keys = %v, want [reason up]", maintain, got)
		}
	}
}
