package reporting

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// shareCache keeps the public /share/ routes off the store. The store has
// one connection, shared with ingest flushes and the daily pass, and
// /share/ answers anyone: without it, a hot or hammered link queues behind
// ingest and waits out every long transaction.
//
// Rows are kept shareRowAge, and an unknown id shareMissAge, so a flood
// of made-up ids reaches the store once per id per 10 s. Liveness is not
// cached: it is worked out from the cached row against the clock on every
// request, so a share whose archive_at passes goes down on time. This
// Service's own writes (archive, restore, update) drop the row at once.
// Writes from elsewhere, another process (the CLI's project delete) or
// the daily pass deleting a project, are seen when the row expires: for
// up to shareRowAge a deleted share may still be served.
//
// Images never change for an id and size, so they are kept until evicted:
// least recently used first, once they hold more than shareImageBytes.
// One whose share has gone is never served, since the row is read first.
type shareCache struct {
	now func() time.Time

	mu        sync.Mutex
	rows      map[string]shareRowEntry
	lastSweep time.Time
	gen       uint64 // bumped by every invalidate, so a load that raced one is not kept

	imgs     map[shareImageKey]*list.Element
	lru      *list.List // of *shareImageEntry, most recently used at the front
	imgBytes int
	maxBytes int

	sf singleflight.Group
}

const (
	shareRowAge     = 60 * time.Second
	shareMissAge    = 10 * time.Second
	shareImageBytes = 64 << 20
)

type shareRowEntry struct {
	row     store.WidgetShare
	missing bool // the store answered ErrNotFound
	at      time.Time
}

type shareImageKey struct {
	id   string
	twoX bool
}

type shareImageEntry struct {
	key shareImageKey
	b   []byte
}

func newShareCache(now func() time.Time, maxBytes int) *shareCache {
	return &shareCache{
		now: now, rows: map[string]shareRowEntry{},
		imgs: map[shareImageKey]*list.Element{}, lru: list.New(), maxBytes: maxBytes,
	}
}

// row is the share id from the cache, or from load when it is not there
// or has expired. An unknown id is ErrNotFound, cached or not; any other
// error from load is returned and not kept.
func (c *shareCache) row(ctx context.Context, id string, load func(context.Context, string) (store.WidgetShare, error)) (store.WidgetShare, error) {
	c.mu.Lock()
	e, ok := c.rows[id]
	now := c.now()
	if ok {
		age := shareRowAge
		if e.missing {
			age = shareMissAge
		}
		if now.Sub(e.at) < age {
			c.mu.Unlock()
			if e.missing {
				return store.WidgetShare{}, store.Refuse(store.ErrNotFound, "widget share %s: not found", id)
			}
			return e.row, nil
		}
	}
	gen := c.gen
	c.mu.Unlock()

	// The generation is in the key, so a request after an invalidate never
	// joins a load that started before it.
	v, err, _ := c.sf.Do(fmt.Sprintf("row/%d/%s", gen, id), func() (any, error) {
		r, err := load(ctx, id)
		missing := errors.Is(err, store.ErrNotFound)
		if err != nil && !missing {
			return nil, err
		}
		c.mu.Lock()
		if c.gen == gen {
			c.sweepRows(now)
			c.rows[id] = shareRowEntry{row: r, missing: missing, at: now}
		}
		c.mu.Unlock()
		return r, err
	})
	if err != nil {
		return store.WidgetShare{}, err
	}
	return v.(store.WidgetShare), nil
}

// sweepRows drops expired rows, at most once per shareMissAge, so a flood
// of made-up ids does not keep them all. Called with mu held.
func (c *shareCache) sweepRows(now time.Time) {
	if now.Sub(c.lastSweep) < shareMissAge {
		return
	}
	c.lastSweep = now
	for id, e := range c.rows {
		if now.Sub(e.at) >= shareRowAge || (e.missing && now.Sub(e.at) >= shareMissAge) {
			delete(c.rows, id)
		}
	}
}

// image is a share's 1x or 2x PNG from the cache, or from load.
func (c *shareCache) image(ctx context.Context, id string, twoX bool, load func(context.Context, string, bool) ([]byte, error)) ([]byte, error) {
	k := shareImageKey{id, twoX}
	c.mu.Lock()
	if el, ok := c.imgs[k]; ok {
		c.lru.MoveToFront(el)
		b := el.Value.(*shareImageEntry).b
		c.mu.Unlock()
		return b, nil
	}
	c.mu.Unlock()

	sfKey := "1x/" + id
	if twoX {
		sfKey = "2x/" + id
	}
	v, err, _ := c.sf.Do(sfKey, func() (any, error) {
		b, err := load(ctx, id, twoX)
		if err != nil {
			return nil, err
		}
		c.putImage(k, b)
		return b, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

func (c *shareCache) putImage(k shareImageKey, b []byte) {
	if len(b) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.imgs[k]; ok {
		return
	}
	c.imgs[k] = c.lru.PushFront(&shareImageEntry{key: k, b: b})
	c.imgBytes += len(b)
	for c.imgBytes > c.maxBytes {
		el := c.lru.Back()
		e := el.Value.(*shareImageEntry)
		c.lru.Remove(el)
		delete(c.imgs, e.key)
		c.imgBytes -= len(e.b)
	}
}

// invalidate drops id's row, so the next request reads the store.
func (c *shareCache) invalidate(id string) {
	c.mu.Lock()
	delete(c.rows, id)
	c.gen++
	c.mu.Unlock()
}
