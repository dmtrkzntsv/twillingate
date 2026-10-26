package reporting

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/queue"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// queuedEngine is an engine of one slot with its queue (Queued): each
// query takes d, and it notes whether two ever ran at once.
type queuedEngine struct {
	*queue.Queue
	d          time.Duration
	now        atomic.Int32
	overlapped atomic.Bool
}

func (q *queuedEngine) Query(ctx context.Context, _ sqlengine.Request) (sqlengine.Result, error) {
	if q.now.Add(1) > 1 {
		q.overlapped.Store(true)
	}
	defer q.now.Add(-1)
	select {
	case <-time.After(q.d):
		return sqlengine.Result{Columns: []string{"n"}, Rows: [][]any{{"1"}}}, nil
	case <-ctx.Done():
		return sqlengine.Result{}, ctx.Err()
	}
}

// TestAskWaitsItsTurn: sql widgets loading together on an engine of one
// slot run one after another, and every one answers, though together
// they take longer than one query's timeout: the wait is no part of it.
func TestAskWaitsItsTurn(t *testing.T) {
	q := &queuedEngine{Queue: queue.New(func() int { return 1 }), d: 100 * time.Millisecond}
	e := newSQLEngine(q, Options{EventsDir: t.TempDir(), QueryTimeout: 250 * time.Millisecond, QueryMaxRows: 10})
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if _, err := e.ask(context.Background(), boundSQL{sql: "SELECT 1 AS n"}, sqlengine.ModeText, 10, nil); err != nil {
				t.Errorf("ask = %v, want an answer after its turn", err)
			}
		})
	}
	wg.Wait()
	if q.overlapped.Load() {
		t.Error("two queries ran on the engine at once, want one at a time")
	}
}

// TestAskLeavesTheQueue: a caller that goes away while waiting its turn
// gets its context's error and never reaches the engine.
func TestAskLeavesTheQueue(t *testing.T) {
	q := &queuedEngine{Queue: queue.New(func() int { return 1 }), d: time.Second}
	e := newSQLEngine(q, Options{EventsDir: t.TempDir(), QueryTimeout: 5 * time.Second, QueryMaxRows: 10})
	release, err := q.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.ask(ctx, boundSQL{sql: "SELECT 1 AS n"}, sqlengine.ModeText, 10, nil); err == nil {
		t.Fatal("ask with its caller gone = an answer, want its context's error")
	}
	if q.now.Load() != 0 || q.overlapped.Load() {
		t.Error("the query reached the engine")
	}
}
