package api

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// eventCountTTL is how long a count of the last 30 days' events answers
// limits before the next read starts another.
const eventCountTTL = 5 * time.Minute

// eventCount caches the last 30 days' events for limits. The count reads
// a month of files (seconds on a busy server), so it runs at most once per
// eventCountTTL, whoever asks: the first read after boot waits for
// it (warm starts it early), concurrent readers share the count in flight,
// and a read past the TTL answers the previous count at once while a
// refresh runs. A count older than twice the TTL (nobody read for a while)
// is not answered: that read waits for the refresh like the first, so an
// answer is never more than 2 x eventCountTTL old. A failure is kept for
// the TTL like a count, so a timeout is reported once per window rather
// than per read.
type eventCount struct {
	ttl   time.Duration
	now   func() time.Time
	count func(context.Context) (int64, error)
	// fail hears each failed count, once.
	fail func(error)

	mu  sync.Mutex
	n   int64
	err error
	at  time.Time // when n and err were counted; zero before the first
	// running is closed when the count in flight ends; nil when none is.
	running chan struct{}
}

func newEventCount(count func(context.Context) (int64, error), fail func(error)) *eventCount {
	return &eventCount{ttl: eventCountTTL, now: time.Now, count: count, fail: fail}
}

// get answers the events counted, or the error the last count ended in.
func (c *eventCount) get(ctx context.Context) (int64, error) {
	c.mu.Lock()
	age := c.now().Sub(c.at)
	if !c.at.IsZero() && age < 2*c.ttl {
		if age >= c.ttl {
			c.startLocked()
		}
		n, err := c.n, c.err
		c.mu.Unlock()
		return n, err
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
	return c.n, c.err
}

// warm starts the first count, so the first read does not wait for it.
func (c *eventCount) warm() {
	c.mu.Lock()
	c.startLocked()
	c.mu.Unlock()
}

// startLocked starts a count unless one is running, returning the channel
// closed when it ends. The count runs on its own context: a reader that
// gives up does not cancel it for the others (CONSOLE_QUERY_TIMEOUT still
// bounds it).
func (c *eventCount) startLocked() chan struct{} {
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
		c.n, c.err, c.at, c.running = n, err, c.now(), nil
		c.mu.Unlock()
		close(done)
	}()
	return done
}

// countEvents counts the events of the 30 days ending today (UTC) in the
// event files, of every family and of every project that has files,
// rules applied: a native count per project from that day on, summed,
// under one CONSOLE_QUERY_TIMEOUT. limits reports the window as of its
// read: only a count started before midnight and read after it is a day
// behind its window, for at most 2 x eventCountTTL.
func (h *host) countEvents(ctx context.Context) (int64, error) {
	if h.native == nil {
		return 0, invalidf("the event files cannot be queried by this process")
	}
	all, err := archive.ListFiles(h.eventsDir)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, h.queryTimeout)
	defer cancel()
	from := civil.Today(time.Now()).AddDays(-29)
	// No upper bound, as the day filter had none.
	to, _ := civil.Parse("9999-12-31")
	var reqs []native.Request
	for _, id := range slices.Sorted(maps.Keys(all)) {
		reqs = append(reqs, native.Request{Query: native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{{Op: "count"}}},
			Params: native.Params{Project: id, From: from, To: to}})
	}
	// One call: a scan per project, side by side.
	var n int64
	for _, a := range h.answerAll(ctx, reqs) {
		if a.Err != nil {
			return 0, a.Err
		}
		if len(a.Result.Rows) == 1 {
			n += cellInt(a.Result.Rows[0][0])
		}
	}
	return n, nil
}

// newHostEventCount is the host's cache of countEvents, its failures
// logged at error.
func (h *host) newHostEventCount() *eventCount {
	return newEventCount(h.countEvents, func(err error) {
		h.logger.Error("limits: events_30d left out, the events cannot be counted", "error", err)
	})
}
