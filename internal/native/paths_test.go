package native

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TestPaths: on one day of the sessions fixture, the flows of its
// sessions as the sessions table cuts them (a declared id, 30 minutes of
// silence, one partition per kind and actor), step by step.
func TestPaths(t *testing.T) {
	f := sessionsFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	p := Params{Project: sessionsProject, From: date(t, "2026-08-30"), To: date(t, "2026-08-30")}
	ask := func(depth int) map[string]string {
		res, err := e.Answer(context.Background(), Query{Kind: "paths", Depth: depth, Limit: 1000}, p, engineRules)
		if err != nil {
			t.Fatal(err)
		}
		if res.Columns[0] != "source" || res.Columns[2] != "value" {
			t.Fatalf("columns %v", res.Columns)
		}
		out := map[string]string{}
		for _, r := range res.Rows {
			out[r[0]+" > "+r[1]] = r[2]
		}
		return out
	}
	want := map[string]string{
		"1 /a > 2 /b": "1", "1 /d > 2 /e": "1", // x0: /c alone, 30m0.2s after /b
		"1 /s1a > 2 /s1b": "1", "1 /u1 > 2 /u2": "1", "1 /d2 > 2 /d2b": "1", // x1's declared and gap sessions
		"1 /first > 2 /second": "1", // x4: one second, ordered
	}
	got := ask(2)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("flow %s = %q, want %s", k, got[k], v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected flow %s = %s", k, got[k])
		}
	}
	if three := ask(3); len(three) < len(got) {
		t.Errorf("depth 3 has %d flows, fewer than depth 2's %d", len(three), len(got))
	}
	for _, tc := range []struct {
		q    Query
		path string
	}{
		{Query{Kind: "paths", Depth: 7}, "depth"},
		{Query{Kind: "paths", Table: "measures"}, "table"},
		{Query{Kind: "paths", Dimensions: []string{"path", "host"}}, "dimensions"},
		{Query{Kind: "paths", Metrics: []Metric{{Op: "count"}}}, "metrics"},
		{Query{Kind: "stat", Table: "views", Depth: 3, Metrics: []Metric{{Op: "count"}}}, "depth"},
		{Query{Kind: "funnel", Depth: 3, Steps: []Step{{Label: "a", Where: []Filter{{Column: "path", Op: "is_set"}}}, {Label: "b", Where: []Filter{{Column: "host", Op: "is_set"}}}}}, "depth"},
		{Query{Kind: "retention", Output: "grid", Depth: 3}, "depth"},
	} {
		if err := tc.q.Validate(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.path) {
			t.Errorf("%+v: %v, want a refusal at %s", tc.q, err, tc.path)
		}
	}
}

// TestPathsCutOverEveryView: a view where leaves out still cuts, and
// joins, sessions: /a, a /b 25 minutes on, a /c 25 minutes after that
// are one session, so without /b the path is /a then /c.
func TestPathsCutOverEveryView(t *testing.T) {
	f := archivetest.New(t)
	base := date(t, "2026-08-30").Time().Add(10 * time.Hour)
	var evs []store.Event
	for i, path := range []string{"/a", "/b", "/c"} {
		evs = append(evs, store.Event{ID: fmt.Sprintf("v%d", i), ProjectID: 5, Family: store.FamilyViews, EventName: "pageview",
			TS: base.Add(time.Duration(i) * 25 * time.Minute), Kind: "web", ActorID: "y", ActorKind: "install", Host: "example.com", Path: path})
	}
	f.Write(t, evs...)
	e := New(Options{Dir: f.Dir, Workers: 2})
	res, err := e.Answer(context.Background(), Query{Kind: "paths", Where: []Filter{{Column: "path", Op: "ne", Values: []string{"/b"}}}},
		Params{Project: 5, From: date(t, "2026-08-30"), To: date(t, "2026-08-30")}, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "1 /a" || res.Rows[0][1] != "2 /c" || res.Rows[0][2] != "1" {
		t.Errorf("paths without /b = %v, want one flow 1 /a > 2 /c", res.Rows)
	}
}
