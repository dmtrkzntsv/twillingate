package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
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
