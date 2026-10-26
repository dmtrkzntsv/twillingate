package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestQueueLimit runs many holders through a queue of 2 and never sees
// more than 2 at once.
func TestQueueLimit(t *testing.T) {
	q := New(func() int { return 2 })
	var now, peak atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			release, err := q.Acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			n := now.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			now.Add(-1)
			release()
		})
	}
	wg.Wait()
	if p := peak.Load(); p != 2 {
		t.Fatalf("peak %d holders at once, want 2", p)
	}
}

// TestQueueOrder hands turns out in the order they were asked for.
func TestQueueOrder(t *testing.T) {
	q := New(func() int { return 1 })
	first, err := q.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Go(func() {
			release, err := q.Acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			release()
		})
		waitFor(t, func() bool { return q.queued() == i+1 })
	}
	first()
	wg.Wait()
	for i, v := range order {
		if v != i {
			t.Fatalf("turns went %v, want 0..4 in order", order)
		}
	}
}

// TestQueueCancel leaves the queue when ctx ends, holding nothing, and the
// next waiter still gets its turn.
func TestQueueCancel(t *testing.T) {
	q := New(func() int { return 1 })
	first, err := q.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	gone := make(chan error)
	go func() {
		_, err := q.Acquire(ctx)
		gone <- err
	}()
	waitFor(t, func() bool { return q.queued() == 1 })
	next := make(chan struct{})
	go func() {
		release, err := q.Acquire(context.Background())
		if err == nil {
			release()
		}
		close(next)
	}()
	waitFor(t, func() bool { return q.queued() == 2 })
	cancel()
	if err := <-gone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Acquire: %v, want context.Canceled", err)
	}
	first()
	first() // a second release does nothing
	select {
	case <-next:
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter behind the cancelled one never got its turn")
	}
	if q.running != 0 {
		t.Fatalf("%d still running, want 0", q.running)
	}
}

// TestQueueGrow reads the size on every turn: a queue grown while full
// lets the next waiter in.
func TestQueueGrow(t *testing.T) {
	var size atomic.Int32
	size.Store(1)
	q := New(func() int { return int(size.Load()) })
	if _, err := q.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	size.Store(2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := q.Acquire(ctx); err != nil {
		t.Fatalf("second holder of a queue grown to 2: %v", err)
	}
}

// TestQueueZeroSize counts a size below 1 as 1.
func TestQueueZeroSize(t *testing.T) {
	q := New(func() int { return 0 })
	release, err := q.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := q.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second holder of a size-0 queue: %v, want to wait", err)
	}
	release()
}

func (q *Queue) queued() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.waiting)
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}
