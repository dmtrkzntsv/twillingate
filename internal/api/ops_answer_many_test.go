package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// countingNative is the engine with its calls counted: each AnswerMany
// with its number of requests, and each Answer.
type countingNative struct {
	*native.Engine
	mu      sync.Mutex
	many    []int
	answers int
}

func (n *countingNative) Answer(ctx context.Context, q native.Query, p native.Params, rules archive.Rules) (native.Result, error) {
	n.mu.Lock()
	n.answers++
	n.mu.Unlock()
	return n.Engine.Answer(ctx, q, p, rules)
}

func (n *countingNative) AnswerMany(ctx context.Context, reqs []native.Request, done func(int, native.Answered)) {
	n.mu.Lock()
	n.many = append(n.many, len(reqs))
	n.mu.Unlock()
	n.Engine.AnswerMany(ctx, reqs, done)
}

// answerOnly is the engine without AnswerMany: answerAll answers one by
// one, as the tools did before.
type answerOnly struct{ *native.Engine }

func (n answerOnly) Answer(ctx context.Context, q native.Query, p native.Params, rules archive.Rules) (native.Result, error) {
	return n.Engine.Answer(ctx, q, p, rules)
}

// Each analytics tool that runs several native queries runs them as one
// AnswerMany, and answers what it answers one query at a time, byte for
// byte.
func TestAnalyticsToolsAnswerFromOneCall(t *testing.T) {
	h, _ := newTestHost(t)
	eng := h.native.(*native.Engine)
	ctx := context.Background()
	month := rangeIn{ProjectID: 1, From: "2026-08-01", To: "2026-08-31"}
	nProjects := len(h.reg.Snapshot(ctx).Projects())
	tools := []struct {
		name string
		reqs int // the requests of its one AnswerMany
		run  func() (any, error)
	}{
		{"views_overview", 2, func() (any, error) { return h.viewsOverview(ctx, overviewIn{rangeIn: month}) }},
		{"views_overview kind", 2, func() (any, error) { return h.viewsOverview(ctx, overviewIn{rangeIn: month, Kind: "app"}) }},
		{"product_events", 2, func() (any, error) { return h.productEvents(ctx, productEventsIn{rangeIn: month}) }},
		{"product_events event", 1, func() (any, error) {
			return h.productEvents(ctx, productEventsIn{rangeIn: month, Event: "signup"})
		}},
		{"retention", 2, func() (any, error) { return h.retention(ctx, retentionIn{rangeIn: month, Actor: "user"}) }},
		// One breakdown chunk and the newest arrival.
		{"usage", 2, func() (any, error) {
			return h.usage(ctx, usageIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: month.From, To: month.To}})
		}},
		// Two chunks of 333 days and the newest arrival.
		{"usage two chunks", 3, func() (any, error) {
			return h.usage(ctx, usageIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2025-09-01", To: "2026-08-31"}})
		}},
		{"limits' event count", -1, func() (any, error) { return h.countEvents(ctx) }},
	}
	for _, tl := range tools {
		t.Run(tl.name, func(t *testing.T) {
			h.native = answerOnly{eng}
			want, err := tl.run()
			if err != nil {
				t.Fatal(err)
			}
			c := &countingNative{Engine: eng}
			h.native = c
			defer func() { h.native = eng }()
			got, err := tl.run()
			if err != nil {
				t.Fatal(err)
			}
			if c.answers != 0 || len(c.many) != 1 || tl.reqs >= 0 && c.many[0] != tl.reqs {
				t.Errorf("%d Answer calls and AnswerMany calls of %v requests, want one of %d", c.answers, c.many, tl.reqs)
			}
			wb, _ := json.Marshal(want)
			gb, _ := json.Marshal(got)
			if string(gb) != string(wb) {
				t.Errorf("one call answers\n%s\nwant\n%s", gb, wb)
			}
			// The limits' count reads the 30 days to today, past the fixture.
			if tl.reqs >= 0 && !strings.Contains(string(wb), `"rows":[[`) && !strings.Contains(string(wb), `"views":`) {
				t.Errorf("an empty answer compares nothing: %s", wb)
			}
		})
	}
	if nProjects < 2 {
		t.Errorf("the fixture has %d projects: limits' event count batches nothing", nProjects)
	}
}

// failingNative fails the requests whose table is failing, in AnswerMany
// and in Answer alike, each with its own error.
type failingNative struct {
	*native.Engine
	failing map[string]error
}

func (n failingNative) Answer(ctx context.Context, q native.Query, p native.Params, rules archive.Rules) (native.Result, error) {
	if err, ok := n.failing[q.Table]; ok {
		return native.Result{}, err
	}
	return n.Engine.Answer(ctx, q, p, rules)
}

func (n failingNative) AnswerMany(ctx context.Context, reqs []native.Request, done func(int, native.Answered)) {
	for i, r := range reqs {
		res, err := n.Answer(ctx, r.Query, r.Params, r.Rules)
		done(i, native.Answered{Result: res, Err: err})
	}
}

// A tool whose queries fail in one call fails with the first query's
// error, worded as answer words it, as it did one query at a time.
func TestAnswerAllFailsWithTheFirstError(t *testing.T) {
	h, _ := newTestHost(t)
	eng := h.native.(*native.Engine)
	ctx := context.Background()
	in := overviewIn{rangeIn: rangeIn{ProjectID: 1, From: "2026-08-01", To: "2026-08-31"}}
	h.native = failingNative{Engine: eng, failing: map[string]error{
		"views":    errors.New("the views are gone"),
		"sessions": native.ErrTooLarge,
	}}
	if _, err := h.viewsOverview(ctx, in); err == nil || err.Error() != "the views are gone" {
		t.Errorf("both failing: %v, want the views' error", err)
	}
	h.native = failingNative{Engine: eng, failing: map[string]error{"sessions": native.ErrTooLarge}}
	if _, err := h.viewsOverview(ctx, in); err == nil || !strings.Contains(err.Error(), tooLarge) {
		t.Errorf("sessions failing: %v, want %q", err, tooLarge)
	}
	h.native = nil
	if _, err := h.viewsOverview(ctx, in); err == nil || !strings.Contains(err.Error(), "cannot be queried") {
		t.Errorf("no engine: %v", err)
	}
}

// groupedNative records each AnswerMany's projects, failing the requests
// of the project fail and answering the others empty.
type groupedNative struct {
	*native.Engine
	fail  int64
	calls [][]int64
}

func (n *groupedNative) AnswerMany(ctx context.Context, reqs []native.Request, done func(int, native.Answered)) {
	var ids []int64
	for i, r := range reqs {
		if len(ids) == 0 || ids[len(ids)-1] != r.Params.Project {
			ids = append(ids, r.Params.Project)
		}
		if r.Params.Project == n.fail {
			done(i, native.Answered{Err: errors.New("the files are gone")})
			continue
		}
		done(i, native.Answered{})
	}
	n.calls = append(n.calls, ids)
}

// usage asks for at most usageProjectsPerCall projects a call, in order,
// and a failure in one call leaves the later calls unasked.
func TestUsageAsksInGroupsOfProjects(t *testing.T) {
	h, _ := newTestHost(t)
	eng := h.native.(*native.Engine)
	ctx := context.Background()
	var projects []*manage.Project
	for id := range int64(20) {
		projects = append(projects, &manage.Project{ID: id + 1})
	}
	day, _ := civil.Parse("2026-08-01")
	n := &groupedNative{Engine: eng}
	h.native = n
	if _, err := h.usageCounts(ctx, projects, true, day, day); err != nil {
		t.Fatal(err)
	}
	if want := (20 + usageProjectsPerCall - 1) / usageProjectsPerCall; len(n.calls) != want || usageProjectsPerCall != 8 {
		t.Fatalf("%d calls of %d projects at most, want %d of 8", len(n.calls), usageProjectsPerCall, want)
	}
	var seen []int64
	for _, c := range n.calls {
		if len(c) > usageProjectsPerCall {
			t.Errorf("a call of %d projects", len(c))
		}
		seen = append(seen, c...)
	}
	if len(seen) != 20 || seen[0] != 1 || seen[19] != 20 {
		t.Errorf("projects asked, in order: %v", seen)
	}

	n = &groupedNative{Engine: eng, fail: 3}
	h.native = n
	if _, err := h.usageCounts(ctx, projects, true, day, day); err == nil || err.Error() != "the files are gone" {
		t.Errorf("project 3 failing: %v", err)
	}
	if len(n.calls) != 1 {
		t.Errorf("%d calls after the first failed, want only the first", len(n.calls))
	}
}

// ctxNative records the contexts of each AnswerMany, the call's and each
// request's, and answers every request empty.
type ctxNative struct {
	*native.Engine
	call context.Context
	reqs []context.Context
}

func (n *ctxNative) AnswerMany(ctx context.Context, reqs []native.Request, done func(int, native.Answered)) {
	n.call = ctx
	for i, r := range reqs {
		n.reqs = append(n.reqs, r.Ctx)
		done(i, native.Answered{})
	}
}

// usage gives each request a CONSOLE_QUERY_TIMEOUT of its own, from the
// call's context, which has none: projects waiting for the engine's
// turns do not share one.
func TestUsageTimesEachRequest(t *testing.T) {
	h, _ := newTestHost(t)
	eng := h.native.(*native.Engine)
	ctx := context.Background()
	var projects []*manage.Project
	for id := range int64(3) {
		projects = append(projects, &manage.Project{ID: id + 1})
	}
	day, _ := civil.Parse("2026-08-01")
	n := &ctxNative{Engine: eng}
	h.native = n
	before := time.Now()
	if _, err := h.usageCounts(ctx, projects, true, day, day); err != nil {
		t.Fatal(err)
	}
	if _, ok := n.call.Deadline(); ok {
		t.Error("the call's context has a deadline of its own")
	}
	if len(n.reqs) < len(projects) {
		t.Fatalf("%d requests", len(n.reqs))
	}
	for i, rc := range n.reqs {
		dl, ok := rc.Deadline()
		if !ok || dl.Before(before.Add(h.queryTimeout)) || dl.After(time.Now().Add(h.queryTimeout)) {
			t.Errorf("request %d: deadline %v (%v), want CONSOLE_QUERY_TIMEOUT from the call", i, dl, ok)
		}
		for _, other := range n.reqs[:i] {
			if other == rc {
				t.Errorf("request %d shares a context", i)
			}
		}
		if rc.Err() == nil {
			t.Errorf("request %d: its context is not released after the call", i)
		}
	}
}
