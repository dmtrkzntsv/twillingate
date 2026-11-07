package inbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/notify"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// DeliveryStore is the slice of the store the Dispatcher uses.
type DeliveryStore interface {
	PendingDeliveries(ctx context.Context, now time.Time, limit int) ([]store.Notification, error)
	SetDelivery(ctx context.Context, id int64, delivery, errText string, attempts int, next *time.Time) error
}

const (
	// dispatchEvery is how often Run looks for pending deliveries.
	dispatchEvery = 5 * time.Second
	// dispatchBatch is how many rows one pass takes at most.
	dispatchBatch = 50
	// sendTimeout bounds one send.
	sendTimeout = 30 * time.Second
	// staleAfter: a row never tried and older than this (the process was
	// down, or NOTIFY_URL was just set) is skipped, not sent late.
	staleAfter = time.Hour
	// giveUpAfter: a temporary failure of a row older than this fails it.
	giveUpAfter = 6 * time.Hour
)

// retryAfter is the wait before the next attempt after the n-th temporary
// failure (1-based): 1m, 5m, 15m, then every hour.
func retryAfter(n int) time.Duration {
	switch n {
	case 1:
		return time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 15 * time.Minute
	}
	return time.Hour
}

// Dispatcher sends pending notifications to the one channel in NOTIFY_URL.
// The message is the one the inbox shows, its link absolute.
type Dispatcher struct {
	st         DeliveryStore
	sender     notify.Sender
	consoleURL string
	logger     *slog.Logger
}

// NewDispatcher returns a Dispatcher sending through s; a nil s means no
// channel: every pending row becomes none and stays in the inbox only.
func NewDispatcher(st DeliveryStore, s notify.Sender, consoleURL string, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{st: st, sender: s, consoleURL: consoleURL, logger: logger}
}

// Run delivers every 5s until ctx ends.
func (d *Dispatcher) Run(ctx context.Context) {
	tk := time.NewTicker(dispatchEvery)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tk.C:
			d.Deliver(ctx, now)
		}
	}
}

// Deliver is one pass over up to 50 rows due at now, oldest first.
func (d *Dispatcher) Deliver(ctx context.Context, now time.Time) {
	rows, err := d.st.PendingDeliveries(ctx, now, dispatchBatch)
	if err != nil {
		if ctx.Err() == nil {
			d.logger.Error("notifications: list pending deliveries", "err", err)
		}
		return
	}
	for _, n := range rows {
		if ctx.Err() != nil {
			return
		}
		d.deliver(ctx, now, n)
	}
}

func (d *Dispatcher) deliver(ctx context.Context, now time.Time, n store.Notification) {
	switch {
	case d.sender == nil:
		d.set(ctx, n.ID, store.DeliveryNone, "", n.Attempts, nil)
		return
	case n.Attempts == 0 && now.Sub(n.CreatedAt) > staleAfter:
		d.set(ctx, n.ID, store.DeliverySkipped, "", 0, nil)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	err := d.sender.Send(sctx, Absolute(Render(n), d.consoleURL))
	cancel()
	attempts := n.Attempts + 1
	switch {
	case err == nil:
		d.set(ctx, n.ID, store.DeliverySent, "", attempts, nil)
	case ctx.Err() != nil:
		// Shutting down: the row stays pending, untouched, for the next start.
	case errors.Is(err, notify.ErrTemporary) && now.Sub(n.CreatedAt) <= giveUpAfter:
		next := now.Add(retryAfter(attempts))
		d.logger.Warn("notifications: send failed, will retry", "id", n.ID, "service", d.sender.Service(),
			"attempts", attempts, "next", next, "err", err)
		d.set(ctx, n.ID, store.DeliveryPending, err.Error(), attempts, &next)
	default:
		// ErrPermanent, a temporary failure past giveUpAfter, or an error
		// a Sender did not classify: retrying is not worth it.
		d.logger.Error("notifications: send failed", "id", n.ID, "service", d.sender.Service(),
			"attempts", attempts, "err", err)
		d.set(ctx, n.ID, store.DeliveryFailed, err.Error(), attempts, nil)
	}
}

func (d *Dispatcher) set(ctx context.Context, id int64, delivery, errText string, attempts int, next *time.Time) {
	if err := d.st.SetDelivery(ctx, id, delivery, errText, attempts, next); err != nil && ctx.Err() == nil {
		d.logger.Error("notifications: record delivery", "id", id, "delivery", delivery, "err", err)
	}
}
