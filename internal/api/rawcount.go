package api

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// rawCountTTL is how long a count of the raw events held answers limits
// before the next read starts another.
const rawCountTTL = 5 * time.Minute

// rawEventsSQL counts the raw events held, every family and project. It
// reads v_events_flat, the view of every raw row, never the events table
// itself, like every raw read (TestRawTableIsReadOnlyThroughFamilyViews).
// SQLite flattens the view, so this is the one scan of the clustered table
// a bare COUNT(*) is; summing raw_views, raw_product and raw_measures
// instead measured about 1.5x slower (three range scans comparing family).
const rawEventsSQL = `SELECT COUNT(*) FROM v_events_flat`

// rawCount caches the raw events held for limits. The count is a scan of
// every raw row (seconds on a server holding millions), so it runs at most
// once per rawCountTTL, whoever asks: the first read after boot waits for
// it (warm starts it early), concurrent readers share the count in flight,
// and a read past the TTL answers the previous count at once while a
// refresh runs. A count older than twice the TTL (nobody read for a while)
// is not answered: that read waits for the refresh like the first, so an
// answer is never more than 2 x rawCountTTL old. A failure is kept for
// the TTL like a count, so a timeout is reported once per window rather
// than per read.
type rawCount struct {
	ttl   time.Duration
	now   func() time.Time
	count func(context.Context) (int64, error)
	// fail hears each failed count, once.
	fail func(error)

	mu   sync.Mutex
	held int64
	err  error
	at   time.Time // when held and err were counted; zero before the first
	// running is closed when the count in flight ends; nil when none is.
	running chan struct{}
}

func newRawCount(count func(context.Context) (int64, error), fail func(error)) *rawCount {
	return &rawCount{ttl: rawCountTTL, now: time.Now, count: count, fail: fail}
}

// get answers the raw events held, or the error the last count ended in.
func (c *rawCount) get(ctx context.Context) (int64, error) {
	c.mu.Lock()
	age := c.now().Sub(c.at)
	if !c.at.IsZero() && age < 2*c.ttl {
		if age >= c.ttl {
			c.startLocked()
		}
		held, err := c.held, c.err
		c.mu.Unlock()
		return held, err
	}
	done := c.startLocked()
	c.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held, c.err
}

// warm starts the first count, so the first read does not wait for it.
func (c *rawCount) warm() {
	c.mu.Lock()
	c.startLocked()
	c.mu.Unlock()
}

// startLocked starts a count unless one is running, returning the channel
// closed when it ends. The count runs on its own context: a reader that
// gives up does not cancel it for the others (the read handle's deadline
// still bounds it).
func (c *rawCount) startLocked() chan struct{} {
	if c.running != nil {
		return c.running
	}
	done := make(chan struct{})
	c.running = done
	go func() {
		n, err := c.count(context.Background())
		if err != nil && c.fail != nil {
			c.fail(err)
		}
		c.mu.Lock()
		c.held, c.err, c.at, c.running = n, err, c.now(), nil
		c.mu.Unlock()
		close(done)
	}()
	return done
}

// countRawEvents counts the raw events held through the console's read
// handle, so at most one pipeline flush behind ingest.
func (h *host) countRawEvents(ctx context.Context) (int64, error) {
	res, err := h.db.Run(ctx, rawEventsSQL)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(res.Rows[0][0], 10, 64)
}

// newHostRawCount is the host's cache of countRawEvents, its failures
// logged at error.
func (h *host) newHostRawCount() *rawCount {
	return newRawCount(h.countRawEvents, func(err error) {
		h.logger.Error("limits: raw_events left out, the raw events cannot be counted", "error", err)
	})
}
