package reporting

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingLoad returns a load func that increments calls on every
// invocation and returns v.
func countingLoad(calls *int32, v any) func() (any, error) {
	return func() (any, error) {
		atomic.AddInt32(calls, 1)
		return v, nil
	}
}

func TestCacheOrdinaryRequestReusesWithinCacheAge(t *testing.T) {
	now := time.Now()
	c := newCache(100*time.Millisecond, 10*time.Millisecond, func() time.Time { return now })
	var calls int32
	load := countingLoad(&calls, "v")

	v1, at1, err := c.get("k", false, load)
	if err != nil || v1 != "v" {
		t.Fatalf("get = %v, %v", v1, err)
	}
	now = now.Add(50 * time.Millisecond) // still younger than cacheAge (100ms)
	v2, at2, err := c.get("k", false, load)
	if err != nil || v2 != "v" || at2 != at1 {
		t.Fatalf("second get = %v %v %v, want a reused entry", v2, at2, err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (reused)", calls)
	}

	now = now.Add(60 * time.Millisecond) // now 110ms past at1: past cacheAge
	if _, _, err := c.get("k", false, load); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (expired)", calls)
	}
}

func TestCacheFreshRequestUsesTheShorterRefreshAge(t *testing.T) {
	now := time.Now()
	c := newCache(100*time.Millisecond, 10*time.Millisecond, func() time.Time { return now })
	var calls int32
	load := countingLoad(&calls, "v")

	if _, _, err := c.get("k", false, load); err != nil {
		t.Fatal(err)
	}
	now = now.Add(20 * time.Millisecond) // younger than cacheAge, older than refreshAge
	if _, _, err := c.get("k", true, load); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2: fresh must not reuse an entry older than refreshAge", calls)
	}
	// An ordinary (non-fresh) request right after still reuses it.
	if _, _, err := c.get("k", false, load); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want still 2: ordinary reuses within cacheAge", calls)
	}
}

func TestCacheZeroAgeRecomputesEveryTime(t *testing.T) {
	now := time.Now()
	c := newCache(0, 0, func() time.Time { return now })
	var calls int32
	load := countingLoad(&calls, "v")

	for i := 0; i < 3; i++ {
		if _, _, err := c.get("k", false, load); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3: CacheAge 0 never reuses", calls)
	}
	if len(c.entries) != 0 {
		t.Errorf("entries = %d, want 0: CacheAge 0 must never store, not merely never reuse", len(c.entries))
	}
}

// TestCacheZeroCacheAgeIgnoresFresh: CacheAge 0 turns the cache off for a
// fresh=true request too, even when RefreshAge is large — a fresh
// request must never end up *staler* than an ordinary one just because
// RefreshAge outlives a cache that isn't running at all.
func TestCacheZeroCacheAgeIgnoresFresh(t *testing.T) {
	now := time.Now()
	c := newCache(0, time.Hour, func() time.Time { return now })
	var calls int32
	load := countingLoad(&calls, "v")

	if _, _, err := c.get("k", false, load); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.get("k", true, load); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2: a fresh request must not reuse anything when the cache is off", calls)
	}
	if len(c.entries) != 0 {
		t.Errorf("entries = %d, want 0", len(c.entries))
	}
}

// TestCacheZeroCacheAgeStillSharesConcurrentCalls: turning the cache off
// (CacheAge 0) must not turn singleflight off too — concurrent identical
// requests still share one load, only nothing is kept afterward. Unlike
// TestCacheConcurrentMissesRunTheLoaderOnce's start/release handshake
// (which needs every caller to have already reached sf.Do before the one
// that got there first is allowed to finish), this gives the leader a
// fixed, generous head start instead: with cacheAge>0 every caller does
// a mutex-guarded lookup before Do, which incidentally paces them close
// together, but runOnce skips that lookup entirely, so a handshake tuned
// for the other path is not a given here too.
func TestCacheZeroCacheAgeStillSharesConcurrentCalls(t *testing.T) {
	c := newCache(0, 0, nil)
	var calls int32
	load := func() (any, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(100 * time.Millisecond) // ample time for every goroutine below to reach sf.Do
		return "v", nil
	}
	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, _, err := c.get("same-key", false, load); err != nil {
				t.Errorf("get: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if len(c.entries) != 0 {
		t.Errorf("entries = %d, want 0", len(c.entries))
	}
}

func TestCacheConcurrentMissesRunTheLoaderOnce(t *testing.T) {
	now := time.Now()
	c := newCache(time.Minute, time.Minute, func() time.Time { return now })
	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	load := func() (any, error) {
		atomic.AddInt32(&calls, 1)
		once.Do(func() { close(started) })
		<-release // hold every concurrent caller here so they all overlap
		return "v", nil
	}

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			v, _, err := c.get("same-key", false, load)
			if err != nil || v != "v" {
				t.Errorf("get = %v, %v", v, err)
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()

	if calls != 1 {
		t.Errorf("calls = %d, want 1: singleflight should run the loader once for 20 concurrent identical requests", calls)
	}
}

func TestCacheDifferentKeyIsAMiss(t *testing.T) {
	// Stands in for "updating a widget's source yields a miss": the
	// cache itself only knows keys, and data.go's cacheKey folds the
	// widget's source content into the key, so a changed key is exactly
	// what an updated source produces.
	now := time.Now()
	c := newCache(time.Minute, time.Minute, func() time.Time { return now })
	var calls int32
	load := countingLoad(&calls, "v")

	if _, _, err := c.get("key-before-update", false, load); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.get("key-after-update", false, load); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2: a different key must not reuse the old entry", calls)
	}
}

func TestCacheLoadFailureIsNotStored(t *testing.T) {
	now := time.Now()
	c := newCache(time.Minute, time.Minute, func() time.Time { return now })
	wantErr := errors.New("boom")
	failing := func() (any, error) { return nil, wantErr }
	if _, _, err := c.get("k", false, failing); err != wantErr {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	var calls int32
	if _, _, err := c.get("k", false, countingLoad(&calls, "v")); err != nil || calls != 1 {
		t.Fatalf("after a failed load: calls = %d, err = %v; want a fresh load to run", calls, err)
	}
}

func TestCacheSweepsExpiredEntriesOnPut(t *testing.T) {
	now := time.Now()
	c := newCache(50*time.Millisecond, 10*time.Millisecond, func() time.Time { return now })
	var calls int32
	load := countingLoad(&calls, "v")

	if _, _, err := c.get("stale", false, load); err != nil {
		t.Fatal(err)
	}
	if len(c.entries) != 1 {
		t.Fatalf("entries = %d, want 1 right after the first put", len(c.entries))
	}

	// max(cacheAge, refreshAge) is 50ms; advance past it and put a second
	// key, which should sweep the first (now stale under either age).
	now = now.Add(60 * time.Millisecond)
	if _, _, err := c.get("fresh", false, load); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.entries["stale"]; ok {
		t.Error("stale entry survived a sweep that ran 60ms after it was stored (interval 50ms)")
	}
	if _, ok := c.entries["fresh"]; !ok {
		t.Error("the entry that triggered the sweep should itself survive it")
	}
}
