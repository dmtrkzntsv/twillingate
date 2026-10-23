package reporting

import (
	"errors"
	"sync"
	"time"
)

// entry is one cached value: what a load returned, and when.
type entry struct {
	value    any
	cachedAt time.Time
}

// flight is one load in progress for a key: every caller asking for
// that key meanwhile waits on done and shares its value, cachedAt and
// error, whether it came through get (a widget GET) or claim (the
// stream). abandoned is set when the load stopped because its own
// caller's context ended (the stream's client went away): the waiters
// then load again rather than take that caller's cancellation as their
// own.
type flight struct {
	done      chan struct{}
	value     any
	cachedAt  time.Time
	err       error
	abandoned bool
}

// cache is the two-age cache a Service's widget reads share (Options.
// CacheAge/RefreshAge, REPORTING_CACHE_MAX_AGE/REPORTING_REFRESH_MAX_AGE):
// an entry younger than cacheAge is served as-is; a "fresh" request
// instead requires one younger than the shorter refreshAge. cacheAge 0
// turns the cache off outright (get's own comment) rather than merely
// never reusing an entry. Concurrent identical requests share one load
// either way (flights); when the cache is on, a put also sweeps
// entries older than max(cacheAge, refreshAge) — stale under either age,
// so never worth keeping — once that long has passed since the last
// sweep, so the map does not grow without bound.
type cache struct {
	mu                   sync.Mutex
	entries              map[string]entry
	flights              map[string]*flight
	cacheAge, refreshAge time.Duration
	lastSweep            time.Time
	now                  func() time.Time // stands in for time.Now in tests; nil means time.Now
}

// newCache builds a cache sized by cacheAge and refreshAge; now stands in
// for time.Now in tests.
func newCache(cacheAge, refreshAge time.Duration, now func() time.Time) *cache {
	return &cache{entries: make(map[string]entry), flights: make(map[string]*flight),
		cacheAge: cacheAge, refreshAge: refreshAge, now: now}
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
// could, making "fresh" the staler of the two. Concurrent identical
// requests still share one load.
func (c *cache) get(key string, fresh bool, load func() (any, error)) (any, time.Time, error) {
	for {
		v, at, f, leader := c.claim(key, fresh)
		switch {
		case f == nil:
			return v, at, nil
		case !leader:
			<-f.done
			if f.abandoned {
				continue
			}
			return f.value, f.cachedAt, f.err
		}
		return c.lead(key, f, load)
	}
}

// lead runs load as f's leader and settles f with what it returns; a
// panic in load settles f with an error first, so its waiters do not
// wait forever, and goes on.
func (c *cache) lead(key string, f *flight, load func() (any, error)) (v any, at time.Time, err error) {
	settled := false
	defer func() {
		if !settled {
			c.settle(key, f, nil, errors.New("reporting: widget load panicked"), false)
		}
	}()
	v, err = load()
	at = c.settle(key, f, v, err, false)
	settled = true
	if err != nil {
		return nil, time.Time{}, err
	}
	return v, at, nil
}

// claim is get's first step, under one lock: key's entry when it is
// young enough (f nil), or the load in flight for key to wait on
// (leader false), or a new flight this caller must settle (leader true).
// The lookup and the flight check are one critical section, so a load
// that finished and stored its value is never loaded again by a caller
// that missed just before it.
func (c *cache) claim(key string, fresh bool) (v any, at time.Time, f *flight, leader bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cacheAge > 0 {
		age := c.cacheAge
		if fresh {
			age = c.refreshAge
		}
		if e, ok := c.entries[key]; ok && c.clock().Sub(e.cachedAt) < age {
			return e.value, e.cachedAt, nil, false
		}
	}
	if f, ok := c.flights[key]; ok {
		return nil, time.Time{}, f, false
	}
	f = &flight{done: make(chan struct{})}
	c.flights[key] = f
	return nil, time.Time{}, f, true
}

// settle ends f, claimed for key: a value is stored (cache on) and
// handed to every waiter, stamped now, which it returns; an error is
// handed to them and not stored; abandoned stores nothing and sends the
// waiters back to load for themselves.
func (c *cache) settle(key string, f *flight, v any, err error, abandoned bool) time.Time {
	at := c.clock()
	c.mu.Lock()
	if c.flights[key] == f {
		delete(c.flights, key)
	}
	if err == nil && !abandoned && c.cacheAge > 0 {
		c.putLocked(key, v, at)
	}
	c.mu.Unlock()
	f.value, f.cachedAt, f.err, f.abandoned = v, at, err, abandoned
	close(f.done)
	return at
}

// put stores value under key at "at", then — once max(cacheAge,
// refreshAge) has passed since the last sweep — drops every entry at
// least that old, under either age. c.mu is held.
func (c *cache) putLocked(key string, value any, at time.Time) {
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
