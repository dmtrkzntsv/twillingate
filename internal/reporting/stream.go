package reporting

import (
	"context"
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
	send := func(i int, d WidgetData, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			d = WidgetData{}
		}
		emit(WidgetResult{Index: i, WidgetID: reqs[i].WidgetID, Data: d, Err: err})
	}

	// A claimed miss: the flight this call leads for key, and its
	// request's prepared load.
	type miss struct {
		i int
		p prepared
		f *flight
	}
	var batch, alone, waits []miss
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

	// settle ends m's flight with what its load returned; a load that
	// failed because ctx ended is abandoned, so nothing is cached and a
	// GET waiting on it loads for itself.
	settle := func(m miss, v any, err error) {
		at := s.cache.settle(m.p.key, m.f, v, err, err != nil && ctx.Err() != nil)
		if err != nil {
			send(m.i, WidgetData{}, wrapLoadErr(err))
			return
		}
		d, err := m.p.finish(v, at)
		send(m.i, d, err)
	}

	var wg sync.WaitGroup
	for _, m := range alone {
		wg.Go(func() {
			v, err := m.p.load(ctx)
			if m.f != nil {
				settle(m, v, err)
				return
			}
			if err != nil {
				send(m.i, WidgetData{}, wrapLoadErr(err))
				return
			}
			d, err := m.p.finish(v, time.Time{}) // not cacheable: finish stamps nothing
			send(m.i, d, err)
		})
	}
	for _, m := range waits {
		wg.Go(func() {
			v, at, err := s.wait(ctx, m.p, m.f, reqs[m.i].Fresh)
			if err != nil {
				send(m.i, WidgetData{}, wrapLoadErr(err))
				return
			}
			d, err := m.p.finish(v, at)
			send(m.i, d, err)
		})
	}
	if len(batch) > 0 {
		qs := batch[0].p.src.(*querySource)
		loads := make([]queryLoad, len(batch))
		for k, m := range batch {
			loads[k] = queryLoad{content: m.p.w.Source, p: m.p.params}
		}
		qs.loadMany(ctx, loads, func(k int, res readsql.Result, err error) {
			if err != nil {
				settle(batch[k], nil, err)
				return
			}
			settle(batch[k], res, nil)
		})
	}
	wg.Wait()
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
		v, err := p.load(ctx)
		at = s.cache.settle(p.key, next, v, err, err != nil && ctx.Err() != nil)
		if err != nil {
			return nil, time.Time{}, err
		}
		return v, at, nil
	}
}
