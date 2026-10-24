package sqlengine

import (
	"context"
	"errors"
	"time"
)

// Querier sends a query to an engine; *Client is one.
type Querier interface {
	Query(ctx context.Context, req Request) (Result, error)
}

// StaleAttempts is how many times QueryFresh sends a query whose table
// spec went stale.
const StaleAttempts = 3

// staleBackoff is the wait before QueryFresh's second attempt, doubled
// before each one after; a variable so a test can shorten it.
var staleBackoff = 50 * time.Millisecond

// QueryFresh sends req on q with the table spec tables builds. An answer
// of KindStale says a file the spec named was gone by the time the engine
// read it: the daily pass dumps a day's log to Parquet and deletes it,
// merges a month's days and deletes them, and drops what retention
// expired, while the spec was on its way. QueryFresh then builds the spec
// afresh and sends it again, after 50 ms and then 100 ms, at most
// StaleAttempts times in all and while ctx lasts. It returns the last
// answer: a stale one when every attempt was, or the error tables
// returned.
func QueryFresh(ctx context.Context, q Querier, req Request, tables func() (Tables, error)) (Result, error) {
	backoff := staleBackoff
	for attempt := 1; ; attempt++ {
		t, err := tables()
		if err != nil {
			return Result{}, err
		}
		req.Tables = t
		res, err := q.Query(ctx, req)
		var e *Error
		if err == nil || !errors.As(err, &e) || e.Kind != KindStale || attempt == StaleAttempts {
			return res, err
		}
		select {
		case <-ctx.Done():
			return res, err
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}
