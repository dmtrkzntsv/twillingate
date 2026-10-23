package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// streamAll runs WidgetDataStream and returns its results in the order
// emitted, failing the test unless each request is emitted once.
func streamAll(t *testing.T, ctx context.Context, svc *Service, reqs []WidgetRequest) []WidgetResult {
	t.Helper()
	var got []WidgetResult
	seen := map[int]bool{}
	svc.WidgetDataStream(ctx, reqs, func(r WidgetResult) {
		if seen[r.Index] {
			t.Errorf("request %d emitted twice", r.Index)
		}
		seen[r.Index] = true
		got = append(got, r)
	})
	if len(got) != len(reqs) {
		t.Fatalf("emitted %d results for %d requests", len(got), len(reqs))
	}
	return got
}

// sameAsGet fails the test unless r is what WidgetData answers for its
// request: the same JSON, or the same refusal.
func sameAsGet(t *testing.T, what string, svc *Service, req WidgetRequest, r WidgetResult) {
	t.Helper()
	want, werr := svc.WidgetData(context.Background(), DataRequest{WidgetID: req.WidgetID, ProjectID: req.ProjectID,
		From: req.From, To: req.To, Fresh: req.Fresh})
	if r.WidgetID != req.WidgetID {
		t.Errorf("%s: widget_id %d, want %d", what, r.WidgetID, req.WidgetID)
	}
	if werr != nil || r.Err != nil {
		if (werr == nil) != (r.Err == nil) || werr.Error() != r.Err.Error() ||
			errors.Is(werr, store.ErrNotFound) != errors.Is(r.Err, store.ErrNotFound) ||
			errors.Is(werr, store.ErrInvalid) != errors.Is(r.Err, store.ErrInvalid) {
			t.Errorf("%s: error %v, GET's %v", what, r.Err, werr)
		}
		return
	}
	g, _ := json.Marshal(r.Data)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("%s:\nstream %s\nGET    %s", what, g, w)
	}
}

// TestWidgetDataStreamEqualsWidgetData: every built-in dashboard's
// widgets, streamed as one request for every preset, each answer the same
// JSON as its GET, the cache off so both load.
func TestWidgetDataStreamEqualsWidgetData(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	now := time.Now()
	svc := New(f.st, f.files, Options{Native: native.New(native.Options{Dir: f.files.Dir}), Now: func() time.Time { return now }})
	ds, err := svc.Dashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range ds.Dashboards {
		if d.Owner != store.OwnerSystem {
			continue
		}
		detail, err := svc.Dashboard(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, preset := range systemPresets {
			from, to := presetDates(preset, f.today)
			reqs := make([]WidgetRequest, len(detail.Widgets))
			for i, w := range detail.Widgets {
				reqs[i] = WidgetRequest{WidgetID: w.ID, ProjectID: f.project, From: from, To: to}
			}
			for _, r := range streamAll(t, ctx, svc, reqs) {
				sameAsGet(t, d.Title+"/"+detail.Widgets[r.Index].Name+" "+preset, svc, reqs[r.Index], r)
				n++
			}
		}
	}
	if n < 100 {
		t.Fatalf("compared %d widgets", n)
	}
}

// gateNative is the engine with AnswerMany held until open is closed,
// and every AnswerMany's requests recorded.
type gateNative struct {
	*native.Engine
	open    chan struct{}
	started chan struct{} // closed when the first AnswerMany starts
	once    sync.Once
	mu      sync.Mutex
	reqs    []native.Request
}

func newGate(e *native.Engine) *gateNative {
	return &gateNative{Engine: e, open: make(chan struct{}), started: make(chan struct{})}
}

func (g *gateNative) AnswerMany(ctx context.Context, reqs []native.Request, done func(int, native.Answered)) {
	g.mu.Lock()
	g.reqs = append(g.reqs, reqs...)
	g.mu.Unlock()
	g.once.Do(func() { close(g.started) })
	select {
	case <-g.open:
	case <-ctx.Done():
	}
	g.Engine.AnswerMany(ctx, reqs, done)
}

// TestWidgetDataStreamHitsFirst: a cached widget is emitted while the
// engine has not started on the misses.
func TestWidgetDataStreamHitsFirst(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	gate := newGate(native.New(native.Options{Dir: f.files.Dir}))
	svc := New(f.st, f.files, Options{Native: gate, CacheAge: time.Minute, RefreshAge: time.Second})
	from, to := presetDates("7d", f.today)
	visitors := systemWidgetOf(t, svc, 1, "visitors")
	views := systemWidgetOf(t, svc, 1, "views")
	if _, err := svc.WidgetData(ctx, DataRequest{WidgetID: visitors, ProjectID: f.project, From: from, To: to}); err != nil {
		t.Fatal(err)
	}
	reqs := []WidgetRequest{
		{WidgetID: views, ProjectID: f.project, From: from, To: to},
		{WidgetID: visitors, ProjectID: f.project, From: from, To: to},
	}
	first := make(chan WidgetResult, 2)
	done := make(chan struct{})
	go func() {
		svc.WidgetDataStream(ctx, reqs, func(r WidgetResult) { first <- r })
		close(done)
	}()
	select {
	case r := <-first:
		if r.WidgetID != visitors || r.Err != nil {
			t.Fatalf("first emit %+v, want the cached widget %d", r, visitors)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cache hit was not emitted while the engine was held")
	}
	<-gate.started
	close(gate.open)
	<-done
	if r := <-first; r.WidgetID != views || r.Err != nil {
		t.Fatalf("second emit %+v", r)
	}
	if len(gate.reqs) != 1 {
		t.Fatalf("AnswerMany got %d requests, want only the miss", len(gate.reqs))
	}
}

// TestWidgetDataStreamErrors: a missing, an archived and a widget missing
// its project fail only their own result, with GET's refusal; the rest
// answer.
func TestWidgetDataStreamErrors(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	now := time.Now()
	svc := New(f.st, f.files, Options{Native: native.New(native.Options{Dir: f.files.Dir}), Now: func() time.Time { return now }})
	from, to := presetDates("7d", f.today)
	d, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	visitors := systemWidgetOf(t, svc, 1, "visitors")
	cp, err := svc.CopyWidget(ctx, "test", CopyWidget{ID: visitors, DashboardID: d.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ArchiveWidget(ctx, "test", cp.ID); err != nil {
		t.Fatal(err)
	}
	reqs := []WidgetRequest{
		{WidgetID: visitors, ProjectID: f.project, From: from, To: to},
		{WidgetID: 999999, ProjectID: f.project, From: from, To: to},
		{WidgetID: cp.ID, ProjectID: f.project, From: from, To: to},
		{WidgetID: visitors, From: from, To: to},
		{WidgetID: visitors, ProjectID: f.project, From: to, To: from},
	}
	got := streamAll(t, ctx, svc, reqs)
	wantNotFound := map[int]bool{1: true, 2: true}
	for _, r := range got {
		sameAsGet(t, fmt.Sprintf("request %d", r.Index), svc, reqs[r.Index], r)
		switch {
		case r.Index == 0:
			if r.Err != nil {
				t.Errorf("the good widget failed: %v", r.Err)
			}
		case wantNotFound[r.Index]:
			if !errors.Is(r.Err, store.ErrNotFound) {
				t.Errorf("request %d: %v, want not found", r.Index, r.Err)
			}
		default:
			if !errors.Is(r.Err, store.ErrInvalid) {
				t.Errorf("request %d: %v, want invalid", r.Index, r.Err)
			}
		}
	}
}

// blockNative answers nothing until each request's context ends, and
// records those contexts; Answer, a GET's load, is the engine's.
type blockNative struct {
	*native.Engine
	mu      sync.Mutex
	ctxs    []context.Context
	started chan struct{}
	once    sync.Once
}

func (b *blockNative) AnswerMany(ctx context.Context, reqs []native.Request, done func(int, native.Answered)) {
	b.mu.Lock()
	for _, r := range reqs {
		b.ctxs = append(b.ctxs, r.Ctx)
	}
	b.mu.Unlock()
	b.once.Do(func() { close(b.started) })
	for i, r := range reqs {
		<-r.Ctx.Done()
		done(i, native.Answered{Err: r.Ctx.Err()})
	}
}

// TestWidgetDataStreamCancels: once the stream's context ends, every
// engine request of it is cancelled, nothing it had not finished is
// cached, and a GET that joined one of its loads loads for itself rather
// than failing with the stream's cancellation.
func TestWidgetDataStreamCancels(t *testing.T) {
	f := newSystemFixture(t)
	nat := &blockNative{Engine: native.New(native.Options{Dir: f.files.Dir}), started: make(chan struct{})}
	svc := New(f.st, f.files, Options{Native: nat, CacheAge: time.Minute, RefreshAge: time.Second})
	from, to := presetDates("7d", f.today)
	d, err := svc.Dashboard(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	var reqs []WidgetRequest
	var visitors WidgetRequest
	for _, w := range d.Widgets {
		r := WidgetRequest{WidgetID: w.ID, ProjectID: f.project, From: from, To: to}
		if w.Name == "visitors" {
			visitors = r
		}
		reqs = append(reqs, r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	streamed := make(chan []WidgetResult)
	go func() {
		var got []WidgetResult
		svc.WidgetDataStream(ctx, reqs, func(r WidgetResult) { got = append(got, r) })
		streamed <- got
	}()
	<-nat.started

	// A GET of a widget the stream is loading joins that load.
	get := make(chan error, 1)
	go func() {
		got, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: visitors.WidgetID,
			ProjectID: visitors.ProjectID, From: visitors.From, To: visitors.To})
		if res, ok := got.Data.(readsql.Result); err == nil && (!ok || len(res.Rows) == 0) {
			err = errors.New("no rows")
		}
		get <- err
	}()
	waitFor(t, "the GET to join the stream's load", func() bool {
		svc.cache.mu.Lock()
		defer svc.cache.mu.Unlock()
		for _, f := range svc.cache.flights {
			if f.waiters > 0 {
				return true
			}
		}
		return false
	})
	cancel()
	select {
	case got := <-streamed:
		if len(got) != len(reqs) {
			t.Errorf("emitted %d of %d", len(got), len(reqs))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not stop once its context ended")
	}
	nat.mu.Lock()
	ctxs := nat.ctxs
	nat.mu.Unlock()
	if len(ctxs) < 5 {
		t.Fatalf("the engine got %d requests", len(ctxs))
	}
	for i, c := range ctxs {
		if c.Err() == nil {
			t.Errorf("engine request %d still running", i)
		}
	}
	if err := <-get; err != nil {
		t.Errorf("the GET sharing the cancelled load: %v", err)
	}
	svc.cache.mu.Lock()
	n := len(svc.cache.entries)
	svc.cache.mu.Unlock()
	if n != 1 { // the GET's own load only
		t.Errorf("%d cache entries, want 1", n)
	}
}

// TestWidgetDataStreamIdenticalWidgets is Review Focus 2: two widgets
// with the same content on one dashboard each get the answer, and each
// is cached under its own id.
func TestWidgetDataStreamIdenticalWidgets(t *testing.T) {
	ctx := context.Background()
	f := newSystemFixture(t)
	now := time.Now()
	svc := New(f.st, f.files, Options{Native: native.New(native.Options{Dir: f.files.Dir}),
		CacheAge: time.Minute, RefreshAge: time.Second, Now: func() time.Time { return now }})
	d, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "Twins"})
	if err != nil {
		t.Fatal(err)
	}
	from, to := presetDates("30d", f.today)
	var reqs []WidgetRequest
	for _, name := range []string{"visitors", "visitors", "top-pages", "top-pages"} {
		cp, err := svc.CopyWidget(ctx, "test", CopyWidget{ID: systemWidgetOf(t, svc, 1, name), DashboardID: d.ID})
		if err != nil {
			t.Fatal(err)
		}
		reqs = append(reqs, WidgetRequest{WidgetID: cp.ID, ProjectID: f.project, From: from, To: to})
	}
	got := streamAll(t, ctx, svc, reqs)
	svc.cache.mu.Lock()
	keys := make([]string, 0, len(svc.cache.entries))
	for k := range svc.cache.entries {
		keys = append(keys, k)
	}
	svc.cache.mu.Unlock()
	if len(keys) != 4 {
		t.Fatalf("cache keys %v, want one per widget", keys)
	}
	for _, r := range reqs {
		if !hasPrefixed(keys, r.WidgetID) {
			t.Errorf("widget %d has no cache entry: %v", r.WidgetID, keys)
		}
	}
	// The GETs now hit the entries the stream stored; and the twins agree.
	for _, r := range got {
		if r.Err != nil {
			t.Fatalf("widget %d: %v", r.WidgetID, r.Err)
		}
		sameAsGet(t, "twin", svc, reqs[r.Index], r)
	}
	byIndex := map[int]WidgetResult{}
	for _, r := range got {
		byIndex[r.Index] = r
	}
	for _, pair := range [][2]int{{0, 1}, {2, 3}} {
		a, _ := json.Marshal(byIndex[pair[0]].Data.Data)
		b, _ := json.Marshal(byIndex[pair[1]].Data.Data)
		if string(a) != string(b) {
			t.Errorf("twins %v differ:\n%s\n%s", pair, a, b)
		}
	}
}

func hasPrefixed(keys []string, id int64) bool {
	for _, k := range keys {
		if strings.HasPrefix(k, fmt.Sprintf("%d:", id)) {
			return true
		}
	}
	return false
}

// waitFor polls cond until it holds, failing the test after 10 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// panicManyNative is the engine with AnswerMany panicking (a bug); a
// GET's Answer is the engine's.
type panicManyNative struct{ *native.Engine }

func (panicManyNative) AnswerMany(context.Context, []native.Request, func(int, native.Answered)) {
	panic("boom in AnswerMany")
}

// panicNative's every answer panics, and it has no AnswerMany, so the
// stream loads each query widget alone.
type panicNative struct{}

func (panicNative) Answer(context.Context, native.Query, native.Params, archive.Rules) (native.Result, error) {
	panic("boom in Answer")
}

// TestWidgetDataStreamPanics: a panic loading widgets fails each of them
// with an internal error, the stream still answers every request, the
// process goes on, and no flight is left open: a later GET loads.
func TestWidgetDataStreamPanics(t *testing.T) {
	f := newSystemFixture(t)
	from, to := presetDates("7d", f.today)
	for name, nat := range map[string]Native{
		"AnswerMany": panicManyNative{native.New(native.Options{Dir: f.files.Dir})},
		"Answer":     panicNative{},
	} {
		t.Run(name, func(t *testing.T) {
			svc := New(f.st, f.files, Options{Native: nat, CacheAge: time.Minute, RefreshAge: time.Second})
			d, err := svc.Dashboard(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			var reqs []WidgetRequest
			queries, query := 0, int64(0)
			for _, w := range d.Widgets {
				reqs = append(reqs, WidgetRequest{WidgetID: w.ID, ProjectID: f.project, From: from, To: to})
				if w.Source.Type == "query" {
					queries++
					query = w.ID
				}
			}
			internal := 0
			for _, r := range streamAll(t, context.Background(), svc, reqs) {
				switch {
				case errors.Is(r.Err, errInternal):
					internal++
				case r.Err != nil:
					t.Errorf("widget %d: %v, want the internal error", r.WidgetID, r.Err)
				}
			}
			if internal != queries || queries == 0 {
				t.Errorf("%d internal errors for %d query widgets", internal, queries)
			}
			svc.cache.mu.Lock()
			open := len(svc.cache.flights)
			svc.cache.mu.Unlock()
			if open != 0 {
				t.Errorf("%d flights left open", open)
			}
			if name != "AnswerMany" {
				return // a GET would answer through the panicking Answer
			}
			got := make(chan error, 1)
			go func() {
				_, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: query,
					ProjectID: f.project, From: from, To: to})
				got <- err
			}()
			select {
			case err := <-got:
				if err != nil {
					t.Errorf("GET after the panic: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("GET after the panic is still waiting")
			}
		})
	}
}
