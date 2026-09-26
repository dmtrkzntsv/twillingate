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
// instead requires one younger than the shorter refreshAge. Concurrent
// misses on the same key run the loader once (singleflight); a put
// sweeps entries older than max(cacheAge, refreshAge) — stale under
// either age, so never worth keeping — once that long has passed since
// the last sweep, so the map does not grow without bound.
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

func (c *cache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// get returns key's cached value when it is younger than refreshAge
// (fresh) or cacheAge (an ordinary request); otherwise it runs load —
// once, even against concurrent callers sharing key — stores the result
// and returns that instead. cacheAge (or refreshAge) 0 never reuses an
// entry: every call recomputes.
func (c *cache) get(key string, fresh bool, load func() (any, error)) (any, time.Time, error) {
	age := c.cacheAge
	if fresh {
		age = c.refreshAge
	}
	if v, at, ok := c.lookup(key, age); ok {
		return v, at, nil
	}
	v, err, _ := c.sf.Do(key, func() (any, error) {
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
