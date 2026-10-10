package api

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCount is a count rawCount runs: it answers next (or fails with err),
// counting its calls, and blocks until release is closed when release is set.
type fakeCount struct {
	calls   atomic.Int32
	mu      sync.Mutex
	next    int64
	err     error
	release chan struct{}
}

func (f *fakeCount) count(context.Context) (int64, error) {
	f.calls.Add(1)
	f.mu.Lock()
	release := f.release
	f.mu.Unlock()
	if release != nil {
		<-release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.next, f.err
}

func (f *fakeCount) set(n int64, err error, release chan struct{}) {
	f.mu.Lock()
	f.next, f.err, f.release = n, err, release
	f.mu.Unlock()
}

// testRawCount is a rawCount over f on a clock the test moves, recording
// the failures it reports.
func testRawCount(f *fakeCount) (*rawCount, *time.Time, *atomic.Int32) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	var failures atomic.Int32
	c := newRawCount(f.count, func(error) { failures.Add(1) })
	c.now = func() time.Time { return now }
	return c, &now, &failures
}

// settle waits for the count in flight, if any.
func (c *rawCount) settle() {
	c.mu.Lock()
	done := c.running
	c.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Two reads within the TTL run one count; one past it runs the next.
func TestRawCountRunsOnceWithinTheTTL(t *testing.T) {
	f := &fakeCount{next: 42}
	c, now, _ := testRawCount(f)
	for range 3 {
		if n, err := c.get(context.Background()); n != 42 || err != nil {
			t.Fatalf("get = %d, %v; want 42", n, err)
		}
		*now = now.Add(rawCountTTL / 4)
	}
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("%d counts within the TTL, want 1", got)
	}
	*now = now.Add(rawCountTTL)
	c.get(context.Background())
	c.settle()
	if got := f.calls.Load(); got != 2 {
		t.Fatalf("%d counts after the TTL, want 2", got)
	}
}

// The first reads after boot wait for one shared count, not one each.
func TestRawCountFirstReadsShareOneCount(t *testing.T) {
	f := &fakeCount{}
	release := make(chan struct{})
	f.set(7, nil, release)
	c, _, _ := testRawCount(f)
	var wg sync.WaitGroup
	got := make([]int64, 10)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], _ = c.get(context.Background())
		}()
	}
	// Let every reader reach the count in flight before it ends.
	for f.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("%d counts for 10 concurrent first reads, want 1", n)
	}
	for i, n := range got {
		if n != 7 {
			t.Errorf("reader %d got %d, want 7", i, n)
		}
	}
}

// Past the TTL a read answers the previous count at once and refreshes in
// the background; the next read after the refresh has the new count.
func TestRawCountServesTheStaleCountWhileRefreshing(t *testing.T) {
	f := &fakeCount{next: 5}
	c, now, _ := testRawCount(f)
	if n, _ := c.get(context.Background()); n != 5 {
		t.Fatalf("first get = %d, want 5", n)
	}
	release := make(chan struct{})
	f.set(9, nil, release)
	*now = now.Add(rawCountTTL + time.Second)
	for range 3 {
		if n, err := c.get(context.Background()); n != 5 || err != nil {
			t.Fatalf("get while refreshing = %d, %v; want the stale 5", n, err)
		}
	}
	close(release)
	c.settle()
	if n, _ := c.get(context.Background()); n != 9 {
		t.Fatalf("get after the refresh = %d, want 9", n)
	}
	if got := f.calls.Load(); got != 2 {
		t.Fatalf("%d counts, want 2 (one refresh for three stale reads)", got)
	}
}

// A count older than twice the TTL is not answered: the read waits for the
// refresh, so a reader polling rarely never gets an hours-old count.
func TestRawCountWaitsOnACountTooOldToServe(t *testing.T) {
	f := &fakeCount{next: 5}
	c, now, _ := testRawCount(f)
	c.get(context.Background())
	f.set(9, nil, nil)
	*now = now.Add(2 * rawCountTTL)
	if n, _ := c.get(context.Background()); n != 9 {
		t.Fatalf("get two TTLs on = %d, want the fresh 9", n)
	}
	if got := f.calls.Load(); got != 2 {
		t.Fatalf("%d counts, want 2", got)
	}
}

// A failed count is kept for the TTL like a good one: reported once, not
// recounted or re-reported per read; past the TTL it is tried again.
func TestRawCountCachesAFailure(t *testing.T) {
	boom := errors.New("query timed out")
	f := &fakeCount{err: boom}
	c, now, failures := testRawCount(f)
	for range 3 {
		if _, err := c.get(context.Background()); !errors.Is(err, boom) {
			t.Fatalf("get = %v, want %v", err, boom)
		}
	}
	if f.calls.Load() != 1 || failures.Load() != 1 {
		t.Fatalf("counts, failures = %d, %d; want 1, 1", f.calls.Load(), failures.Load())
	}
	*now = now.Add(rawCountTTL)
	c.get(context.Background())
	c.settle()
	if f.calls.Load() != 2 || failures.Load() != 2 {
		t.Fatalf("after the TTL: counts, failures = %d, %d; want 2, 2", f.calls.Load(), failures.Load())
	}
	f.set(3, nil, nil)
	*now = now.Add(rawCountTTL)
	c.get(context.Background())
	c.settle()
	if n, err := c.get(context.Background()); n != 3 || err != nil {
		t.Fatalf("after a good count: %d, %v; want 3", n, err)
	}
}

// warm starts the first count, so the first read finds it done.
func TestRawCountWarm(t *testing.T) {
	f := &fakeCount{next: 11}
	c, _, _ := testRawCount(f)
	c.warm()
	c.settle()
	if f.calls.Load() != 1 {
		t.Fatalf("warm ran %d counts, want 1", f.calls.Load())
	}
	if n, _ := c.get(context.Background()); n != 11 || f.calls.Load() != 1 {
		t.Fatalf("get after warm = %d with %d counts; want 11 with 1", n, f.calls.Load())
	}
}

// A reader that gives up waiting for the first count gets its context's
// error; the count goes on and serves the next read.
func TestRawCountFirstReadHonoursItsContext(t *testing.T) {
	f := &fakeCount{}
	release := make(chan struct{})
	f.set(4, nil, release)
	c, _, _ := testRawCount(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("get = %v, want context.Canceled", err)
	}
	close(release)
	c.settle()
	if n, _ := c.get(context.Background()); n != 4 {
		t.Fatalf("next get = %d, want 4", n)
	}
}
