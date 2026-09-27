package reporting

import (
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// entry is one cached value: what a load returned, and when.
type entry struct {
	value    any
	cachedAt time.Time
}

// loadResult is what a cache miss's singleflight call returns, so every
// caller sharing that one call — not just the one that happened to
// trigger it — gets back the same cachedAt.
type loadResult struct {
	value    any
	cachedAt time.Time
}

// cache is the two-age cache a Service's widget reads share (Options.
// CacheAge/RefreshAge, REPORTING_CACHE_SECONDS/REPORTING_REFRESH_SECONDS):
// an entry younger than cacheAge is served as-is; a "fresh" request
// instead requires one younger than the shorter refreshAge. cacheAge 0
// turns the cache off outright (get's own comment) rather than merely
// never reusing an entry. Concurrent identical requests share one load
// either way (singleflight); when the cache is on, a put also sweeps
// entries older than max(cacheAge, refreshAge) — stale under either age,
// so never worth keeping — once that long has passed since the last
// sweep, so the map does not grow without bound.
type cache struct {
	mu                   sync.Mutex
	entries              map[string]entry
	sf                   singleflight.Group
	cacheAge, refreshAge time.Duration
	lastSweep            time.Time
	now                  func() time.Time // stands in for time.Now in tests; nil means time.Now
}

// newCache builds a cache sized by cacheAge and refreshAge; now stands in
// for time.Now in tests.
func newCache(cacheAge, refreshAge time.Duration, now func() time.Time) *cache {
	return &cache{entries: make(map[string]entry), cacheAge: cacheAge, refreshAge: refreshAge, now: now}
}

// clock is in UTC, since cached_at and refresh_after are sent as they
// are stamped and every other time the API returns is UTC.
func (c *cache) clock() time.Time {
	if c.now != nil {
		return c.now().UTC()
	}
	return time.Now().UTC()
}

// get returns key's cached value when it is younger than refreshAge
// (fresh) or cacheAge (an ordinary request); otherwise it runs load, once
// across concurrent callers sharing key, and stores the result.
//
// cacheAge 0 stores nothing: a stored entry would let a fresh request
// (refreshAge may still be > 0) reuse a value an ordinary one never
// could, making "fresh" the staler of the two.
func (c *cache) get(key string, fresh bool, load func() (any, error)) (any, time.Time, error) {
	if c.cacheAge <= 0 {
		return c.runOnce(key, load)
	}
	age := c.cacheAge
	if fresh {
		age = c.refreshAge
	}
	if v, at, ok := c.lookup(key, age); ok {
		return v, at, nil
	}
	v, err, _ := c.sf.Do(key, func() (any, error) {
		// Re-checked here, inside the singleflight call: the outer lookup
		// above and this one can straddle a different call for the same
		// key that started after the outer one missed but already
		// finished (and stored its result) before this goroutine reached
		// sf.Do — singleflight only shares a call still in flight, so
		// without this check that now-fresh entry would be loaded again.
		if v, at, ok := c.lookup(key, age); ok {
			return loadResult{value: v, cachedAt: at}, nil
		}
		val, err := load()
		if err != nil {
			return nil, err
		}
		at := c.clock()
		c.put(key, val, at)
		return loadResult{value: val, cachedAt: at}, nil
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	lr := v.(loadResult)
	return lr.value, lr.cachedAt, nil
}

// runOnce runs load through singleflight without ever touching the
// entries map: cacheAge 0's "off, not just always-stale" path (get's own
// comment). Concurrent identical requests still share one call.
func (c *cache) runOnce(key string, load func() (any, error)) (any, time.Time, error) {
	v, err, _ := c.sf.Do(key, func() (any, error) {
		val, err := load()
		if err != nil {
			return nil, err
		}
		return loadResult{value: val, cachedAt: c.clock()}, nil
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	lr := v.(loadResult)
	return lr.value, lr.cachedAt, nil
}

// lookup reports key's entry, if one exists and is younger than age.
func (c *cache) lookup(key string, age time.Duration) (any, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || c.clock().Sub(e.cachedAt) >= age {
		return nil, time.Time{}, false
	}
	return e.value, e.cachedAt, true
}

// put stores value under key at "at", then — once max(cacheAge,
// refreshAge) has passed since the last sweep — drops every entry at
// least that old, under either age.
func (c *cache) put(key string, value any, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = entry{value: value, cachedAt: at}
	interval := max(c.cacheAge, c.refreshAge)
	if interval <= 0 || at.Sub(c.lastSweep) < interval {
		return
	}
	for k, e := range c.entries {
		if at.Sub(e.cachedAt) >= interval {
			delete(c.entries, k)
		}
	}
	c.lastSweep = at
}
