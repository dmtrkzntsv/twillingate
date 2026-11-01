package native

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// len is how many tallies are kept.
func (m *retentionMemo) len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// countScans counts retentionCounts' scans for the test, running each
// through hook (when not nil) first.
func countScans(t *testing.T, hook func(n int64)) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	old := onRetentionScan
	onRetentionScan = func() {
		k := n.Add(1)
		if hook != nil {
			hook(k)
		}
	}
	t.Cleanup(func() { onRetentionScan = old })
	return &n
}

// The five retention widgets of a dashboard (a grid, two curves, two
// milestone rows), loading together over one range, scan once, and answer
// as each does on an engine of its own; another range, other offsets or
// another where scan again.
func TestRetentionScansOncePerLoad(t *testing.T) {
	f := retentionFixture(t)
	scans := countScans(t, nil)
	clock := func() time.Time { return date(t, today).Time().Add(13 * time.Hour) }
	e := New(Options{Dir: f.Dir, Workers: 3, MaxScans: 5, Now: clock})
	p := Params{Project: 1, From: date(t, "2026-06-15"), To: date(t, "2026-08-15")}
	queries := []Query{
		{Kind: "retention", Output: "grid"},
		{Kind: "retention", Output: "curve", IdentifiedBy: "install"},
		{Kind: "retention", Output: "curve", IdentifiedBy: "user"},
		{Kind: "retention", Output: "milestones", IdentifiedBy: "install"},
		{Kind: "retention", Output: "milestones", IdentifiedBy: "user"},
	}
	got := make([]Result, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Go(func() {
			res, err := e.Answer(context.Background(), q, p, engineRules)
			if err != nil {
				t.Errorf("%s: %v", q.Output, err)
			}
			got[i] = res
		})
	}
	wg.Wait()
	if n := scans.Load(); n != 1 {
		t.Errorf("%d scans for one load, want 1", n)
	}
	for i, q := range queries {
		alone, err := New(Options{Dir: f.Dir, Now: clock}).Answer(context.Background(), q, p, engineRules)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(got[i]) != fmt.Sprint(alone) {
			t.Errorf("%s %s: shared %v, alone %v", q.Output, q.IdentifiedBy, got[i], alone)
		}
	}
	scans.Store(0)
	for _, c := range []struct {
		q Query
		p Params
	}{
		{Query{Kind: "retention", Output: "grid", Offsets: 30}, p},
		{Query{Kind: "retention", Output: "grid", Where: []Filter{{Column: "country", Op: "eq", Values: []string{"DE"}}}}, p},
		{Query{Kind: "retention", Output: "grid"}, Params{Project: 1, From: p.From, To: p.To.AddDays(-1)}},
	} {
		if _, err := e.Answer(context.Background(), c.q, c.p, engineRules); err != nil {
			t.Fatal(err)
		}
	}
	if n := scans.Load(); n != 3 {
		t.Errorf("%d scans for three other queries, want 3", n)
	}
}

// A file that changes takes the memoised tally out of use: the next query
// scans again and counts the new event, and its tally replaces the old.
func TestRetentionMemoFollowsFiles(t *testing.T) {
	f := retentionFixture(t)
	scans := countScans(t, nil)
	e := New(Options{Dir: f.Dir, Now: func() time.Time { return date(t, today).Time().Add(13 * time.Hour) }})
	p := Params{Project: 1, From: date(t, "2026-09-01"), To: date(t, today)}
	q := Query{Kind: "retention", Output: "grid", Offsets: 7}
	before, err := e.Answer(context.Background(), q, p, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Answer(context.Background(), q, p, engineRules); err != nil {
		t.Fatal(err)
	}
	if n := scans.Load(); n != 1 {
		t.Fatalf("%d scans for the same query twice, want 1", n)
	}
	yesterday := date(t, today).AddDays(-1)
	f.Write(t, store.Event{ID: "r-new", ProjectID: 1, Family: store.FamilyViews, EventName: "pageview", ActorID: "n1",
		ActorKind: store.ActorUser, Host: "example.com", TS: yesterday.Time().Add(10 * time.Hour)})
	after, err := e.Answer(context.Background(), q, p, engineRules)
	if err != nil {
		t.Fatal(err)
	}
	if n := scans.Load(); n != 2 {
		t.Errorf("%d scans after a file changed, want 2", n)
	}
	if fmt.Sprint(after.Rows) == fmt.Sprint(before.Rows) {
		t.Errorf("the new actor's cohort of %s is missing: %v", yesterday, after.Rows)
	}
	if n := e.retMemo.len(); n != 1 {
		t.Errorf("%d tallies kept, want 1: the new one replaces the old", n)
	}
}

// A failed scan is not kept: the next query scans again.
func TestRetentionMemoKeepsNoFailure(t *testing.T) {
	f := retentionFixture(t)
	scans := countScans(t, nil)
	e := New(Options{Dir: f.Dir, MaxBytes: 1 << 10, Now: func() time.Time { return date(t, today).Time() }})
	q := Query{Kind: "retention", Output: "grid"}
	p := Params{Project: 1, From: date(t, retentionFrom), To: date(t, today)}
	for range 2 {
		if _, err := e.Answer(context.Background(), q, p, engineRules); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("err = %v, want ErrTooLarge", err)
		}
	}
	if n := scans.Load(); n != 2 {
		t.Errorf("%d scans, want 2: a failure is not kept", n)
	}
	if n := e.retMemo.len(); n != 0 {
		t.Errorf("%d tallies kept after failures", n)
	}
}

// A query waiting for another's scan gives up when its own context is
// done; one whose leader gave up scans again on its own.
func TestRetentionMemoWaiters(t *testing.T) {
	f := retentionFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	scans := countScans(t, func(n int64) {
		if n == 1 {
			close(started)
			<-release
		}
	})
	e := New(Options{Dir: f.Dir, MaxScans: 4, Now: func() time.Time { return date(t, today).Time() }})
	p := Params{Project: 1, From: date(t, "2026-07-01"), To: date(t, "2026-08-01")}
	q := Query{Kind: "retention", Output: "curve", IdentifiedBy: "install"}

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leader := make(chan error, 1)
	go func() {
		_, err := e.Answer(leaderCtx, q, p, engineRules)
		leader <- err
	}()
	<-started

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	go func() {
		_, err := e.Answer(waiterCtx, q, p, engineRules)
		waiter <- err
	}()
	patient := make(chan error, 1)
	var res Result
	go func() {
		var err error
		res, err = e.Answer(context.Background(), q, p, engineRules)
		patient <- err
	}()
	time.Sleep(50 * time.Millisecond) // both join the leader's scan
	cancelWaiter()
	select {
	case err := <-waiter:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("waiter: %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a waiter whose context is done still waits")
	}
	cancelLeader()
	close(release)
	if err := <-leader; !errors.Is(err, context.Canceled) {
		t.Errorf("leader: %v, want context.Canceled", err)
	}
	if err := <-patient; err != nil {
		t.Fatalf("patient waiter: %v, want its own scan's answer", err)
	}
	if len(res.Rows) == 0 {
		t.Error("patient waiter: no rows")
	}
	if n := scans.Load(); n != 2 {
		t.Errorf("%d scans, want 2: the leader's, given up, and the waiter's own", n)
	}
}

// The memo holds at most retentionMemoEntries tallies and
// retentionMemoBytes, dropping the oldest, and none past its age.
func TestRetentionMemoBounded(t *testing.T) {
	var m retentionMemo
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	small := &retentionTally{counts: map[retentionKey]int64{{}: 1}}
	for i := range retentionMemoEntries + 5 {
		k := strconv.Itoa(i)
		m.put(k, k, small, now.Add(time.Duration(i)*time.Millisecond))
	}
	if n := m.len(); n != retentionMemoEntries {
		t.Errorf("%d tallies kept, want %d", n, retentionMemoEntries)
	}
	if m.get("0", now) != nil || m.get(strconv.Itoa(retentionMemoEntries+4), now) == nil {
		t.Error("the oldest tally kept, or the newest dropped")
	}
	big := &retentionTally{counts: map[retentionKey]int64{}}
	for i := range retentionMemoBytes/retentionKeyBytes - 2 {
		big.counts[retentionKey{cohort: int64(i)}] = 1
	}
	m.put("big", "big", big, now.Add(time.Second))
	if m.bytes > retentionMemoBytes {
		t.Errorf("%d bytes kept, over %d", m.bytes, retentionMemoBytes)
	}
	if m.get("big", now.Add(time.Second)) == nil {
		t.Error("the big tally is not kept")
	}
	huge := &retentionTally{counts: map[retentionKey]int64{}}
	for i := range retentionMemoBytes/retentionKeyBytes + 1 {
		huge.counts[retentionKey{cohort: int64(i)}] = 1
	}
	m.put("huge", "huge", huge, now.Add(time.Second))
	if m.get("huge", now.Add(time.Second)) != nil {
		t.Error("a tally over the bytes allowed is kept")
	}
	if m.get("big", now.Add(time.Second+retentionMemoAge)) != nil {
		t.Error("a tally past its age is used")
	}
}
