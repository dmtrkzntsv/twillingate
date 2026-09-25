// Package pipeline decouples HTTP ingestion from storage: a bounded
// channel absorbs bursts; a single worker writes batches (spec §5.3).
// Availability over completeness: enqueue never blocks, flush failures
// are retried then dropped.
package pipeline

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

type Sink interface {
	WriteEvents(ctx context.Context, evs []store.Event) error
}

var retryDelays = []time.Duration{time.Second, 5 * time.Second, 25 * time.Second}

type Buffer struct {
	cfg     config.BufferConfig
	sink    Sink
	logger  *slog.Logger
	ch      chan store.Event
	dropped atomic.Uint64
}

func New(cfg config.BufferConfig, sink Sink, logger *slog.Logger) *Buffer {
	return &Buffer{cfg: cfg, sink: sink, logger: logger, ch: make(chan store.Event, cfg.Capacity)}
}

func (b *Buffer) Enqueue(e store.Event) { b.enqueue(e) }
func (b *Buffer) Dropped() uint64       { return b.dropped.Load() }

func (b *Buffer) enqueue(e store.Event) {
	for {
		select {
		case b.ch <- e:
			return
		default:
			// Full: drop the oldest to make room, count it, retry.
			select {
			case <-b.ch:
				b.dropped.Add(1)
			default:
			}
		}
	}
}

func (b *Buffer) Run(ctx context.Context) {
	ticker := time.NewTicker(b.cfg.FlushInterval)
	defer ticker.Stop()
	var batch []store.Event
	// One dispatch used by both the steady-state receive and the shutdown
	// drain, so a new item kind cannot be handled in one and missed in the
	// other.
	take := func(e store.Event) {
		batch = append(batch, e)
	}
	flush := func(ctx context.Context) {
		if len(batch) > 0 {
			b.write(ctx, func(c context.Context) error { return b.sink.WriteEvents(c, batch) }, len(batch), "events")
			batch = nil
		}
	}
	for {
		select {
		case <-ctx.Done():
			// Drain whatever is still queued, then final flush.
			for {
				select {
				case e := <-b.ch:
					take(e)
					continue
				default:
				}
				break
			}
			flush(ctx)
			return
		case e := <-b.ch:
			take(e)
			if len(batch) >= b.cfg.FlushMaxEvents {
				flush(ctx)
			}
		case <-ticker.C:
			flush(ctx)
		}
	}
}

// write retries per spec §5.3 (3 attempts, then drop with an error log).
// The sink call itself always uses context.Background(): shutdown must
// still be able to flush. The backoff sleep between attempts, however, is
// cancellation-aware — if ctx is done (e.g. the process is shutting down)
// the sleep is aborted immediately and remaining attempts fire back-to-back
// without delay, so a failing flush can't stall shutdown past the retry
// writes themselves. Normal (non-shutdown) operation keeps the full
// 1s/5s/25s backoff.
func (b *Buffer) write(ctx context.Context, fn func(context.Context) error, n int, kind string) {
	var err error
	for attempt := 0; attempt <= len(retryDelays); attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(retryDelays[attempt-1]):
			case <-ctx.Done():
			}
		}
		if err = fn(context.Background()); err == nil {
			return
		}
	}
	b.logger.Error("pipeline: batch dropped after retries", "kind", kind, "count", n, "error", err)
}
