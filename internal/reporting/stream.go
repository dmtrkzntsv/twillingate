package reporting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
)

// WidgetRequest is one widget of a WidgetDataStream: get_widget_data's
// input without a remote table's paging arguments, which stay on the
// single-widget route.
type WidgetRequest struct {
	WidgetID  int64
	ProjectID int64
	From, To  string
	Fresh     bool
}

// WidgetResult is one widget's answer in a WidgetDataStream: Index is
// its request's place in the call, and Data is what WidgetData answers
// for it, or Err why it has none (WidgetData's own refusal).
type WidgetResult struct {
	Index    int
	WidgetID int64
	Data     WidgetData
	Err      error
}

// WidgetDataStream loads several widgets as WidgetData loads each, each
// answer the same, and calls emit once per request as it is ready, never
// two at once: first every request refused by WidgetData's checks and
// every cache hit, then each load as it finishes. The query widgets the
// cache misses are answered together by the engine's AnswerMany, so a
// shared scan's widgets arrive when that scan finishes; every other load
// (sql, md, a query widget whose load another request is already
// running) runs on its own, concurrently. It returns after the last call
// to emit.
//
// The cache is WidgetData's: a hit is served under the same key and ages
// (Fresh asks for RefreshAge), a miss is stored per widget under its own
// key, and a load another request for the same key has in flight, a GET
// or another stream, is shared rather than run twice.
//
// Unlike WidgetData, the loads run in ctx: once ctx is done (the client
// went away) every load of the call still running stops, and a miss it
// had not finished is not cached. A GET waiting on such a load does not
// take its cancellation: it runs the load again itself.
func (s *Service) WidgetDataStream(ctx context.Context, reqs []WidgetRequest, emit func(WidgetResult)) {
	var mu sync.Mutex
	sent := make([]bool, len(reqs))
	// send emits request i's answer, once: a later one (after a panic
	// already answered it, or the sweep below) is dropped.
	send := func(i int, d WidgetData, err error) {
		mu.Lock()
		defer mu.Unlock()
		if sent[i] {
			return
		}
		sent[i] = true
		if err != nil {
			d = WidgetData{}
		}
		emit(WidgetResult{Index: i, WidgetID: reqs[i].WidgetID, Data: d, Err: err})
	}

	// A claimed miss: the flight this call leads for key (nil when the
	// source is not cacheable), and its request's prepared load.
	type miss struct {
		i int
		p prepared
		f *flight
	}
	var batch, alone, waits []miss
	var wg sync.WaitGroup
	// The last word, whatever happened before it (a panic, a request the
	// engine never answered): once every load has ended, every flight
	// this call claimed and did not settle is settled with an internal
	// error, so no GET waits on it for ever, and every request not
	// answered is answered with one.
	defer func() {
		if p := recover(); p != nil {
			logPanic(p)
		}
		wg.Wait()
		for _, ms := range [][]miss{batch, alone} {
			for _, m := range ms {
				if m.f != nil {
					s.cache.settle(m.p.key, m.f, nil, errInternal, false)
				}
			}
		}
		for i := range reqs {
			send(i, WidgetData{}, errInternal)
		}
	}()
	// guard, deferred first in a load's goroutine, turns a panic in it
	// into m's internal error, its flight settled with it.
	guard := func(m miss) {
		if p := recover(); p != nil {
			logPanic(p)
			if m.f != nil {
				s.cache.settle(m.p.key, m.f, nil, errInternal, false)
			}
			send(m.i, WidgetData{}, errInternal)
		}
	}

	for i, r := range reqs {
		in := DataRequest{WidgetID: r.WidgetID, ProjectID: r.ProjectID, From: r.From, To: r.To, Fresh: r.Fresh}
		p, err := s.prepare(ctx, in)
		switch {
		case err != nil:
			send(i, WidgetData{}, err)
			continue
		case p.final:
			send(i, p.out, nil)
			continue
		case p.key == "":
			alone = append(alone, miss{i: i, p: p})
			continue
		}
		v, at, f, leader := s.cache.claim(p.key, r.Fresh)
		switch {
		case f == nil:
			d, err := p.finish(v, at)
			send(i, d, err)
		case !leader:
			waits = append(waits, miss{i: i, p: p, f: f})
		case isQuery(p):
			batch = append(batch, miss{i: i, p: p, f: f})
		default:
			alone = append(alone, miss{i: i, p: p, f: f})
		}
	}

	// answer emits m's answer from what its load returned (and its
	// flight handed back at).
	answer := func(m miss, v any, at time.Time, err error) {
		if err != nil {
			send(m.i, WidgetData{}, wrapLoadErr(err))
			return
		}
		d, err := m.p.finish(v, at)
		send(m.i, d, err)
	}

	for _, m := range alone {
		wg.Go(func() {
			defer guard(m)
			if m.f == nil { // not cacheable: finish stamps nothing
				v, err := m.p.load(ctx)
				answer(m, v, time.Time{}, err)
				return
			}
			v, at, err := s.loadInto(ctx, m.p, m.f)
			answer(m, v, at, err)
		})
	}
	for _, m := range waits {
		wg.Go(func() {
			defer guard(miss{i: m.i, p: m.p}) // m.f is another request's flight
			v, at, err := s.wait(ctx, m.p, m.f, reqs[m.i].Fresh)
			answer(m, v, at, err)
		})
	}
	if len(batch) > 0 {
		qs := batch[0].p.src.(*querySource)
		loads := make([]queryLoad, len(batch))
		for k, m := range batch {
			loads[k] = queryLoad{content: m.p.w.Source, p: m.p.params}
		}
		qs.loadMany(ctx, loads, func(k int, res readsql.Result, err error) {
			m := batch[k]
			defer guard(m)
			var v any
			if err == nil {
				v = res
			}
			at := s.cache.settle(m.p.key, m.f, v, err, abandoned(ctx, err))
			answer(m, v, at, err)
		})
	}
}

// errInternal is a widget's answer when loading it panicked (a bug) or
// nothing answered it: an untyped error, which the API answers as
// "internal" and logs.
var errInternal = errors.New("reporting: internal error loading the widget")

// logPanic logs a recovered panic with its stack.
func logPanic(p any) {
	slog.Error("reporting: panic loading a widget", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
}

// abandoned says whether a load's err is ctx ending (the client went
// away) rather than an answer: nothing is cached, and a GET waiting on
// the load runs it again.
func abandoned(ctx context.Context, err error) bool { return err != nil && ctx.Err() != nil }

// loadInto runs p's load in ctx as f's leader and settles f with it: a
// load that failed because ctx ended is abandoned. A panic in the load
// settles f with an internal error first, so no waiter waits for ever,
// and goes on.
func (s *Service) loadInto(ctx context.Context, p prepared, f *flight) (v any, at time.Time, err error) {
	defer func() {
		if r := recover(); r != nil {
			s.cache.settle(p.key, f, nil, errInternal, false)
			panic(r)
		}
	}()
	v, err = p.load(ctx)
	at = s.cache.settle(p.key, f, v, err, abandoned(ctx, err))
	return v, at, err
}

// isQuery says whether p loads through the query source as a whole
// result, which loadMany can batch.
func isQuery(p prepared) bool {
	_, ok := p.src.(*querySource)
	return ok && !remoteTable(p.w)
}

// wait is the value of the load f, another request's, hands over, or
// once that load was abandoned, a load of p's own through the cache, in
// ctx (so another waiter may share it in turn).
func (s *Service) wait(ctx context.Context, p prepared, f *flight, fresh bool) (any, time.Time, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, time.Time{}, ctx.Err()
		case <-f.done:
		}
		if !f.abandoned {
			return f.value, f.cachedAt, f.err
		}
		v, at, next, leader := s.cache.claim(p.key, fresh)
		switch {
		case next == nil:
			return v, at, nil
		case !leader:
			f = next
			continue
		}
		return s.loadInto(ctx, p, next)
	}
}
