package sqlengine

import (
	"context"
	"errors"
	"testing"
	"time"
)

// staleQuerier answers stale to its first n queries, then a row, and
// records the table spec of each.
type staleQuerier struct {
	n     int
	specs []Tables
}

func (s *staleQuerier) Query(_ context.Context, req Request) (Result, error) {
	s.specs = append(s.specs, req.Tables)
	if len(s.specs) <= s.n {
		return Result{}, &Error{Status: 409, Kind: KindStale, Message: "a file in the table spec is gone"}
	}
	return Result{Columns: []string{"v"}, Rows: [][]any{{"1"}}}, nil
}

// TestQueryFreshRebuildsTheSpec: a stale answer builds the spec again and
// resends it, up to StaleAttempts in all, then the stale answer stands.
func TestQueryFreshRebuildsTheSpec(t *testing.T) {
	builds := 0
	spec := func() (Tables, error) {
		builds++
		return Tables{Version: TablesVersion, Projects: []Project{{ID: int64(builds)}}}, nil
	}
	q := &staleQuerier{n: 1}
	res, err := QueryFresh(context.Background(), q, Request{SQL: "x"}, spec)
	if err != nil || len(res.Rows) != 1 || builds != 2 || q.specs[1].Projects[0].ID != 2 {
		t.Errorf("one stale answer: %+v, %v after %d builds; want the second, fresh spec answered", res, err, builds)
	}

	builds = 0
	q = &staleQuerier{n: 99}
	start := time.Now()
	_, err = QueryFresh(context.Background(), q, Request{SQL: "x"}, spec)
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindStale || len(q.specs) != StaleAttempts || builds != StaleAttempts {
		t.Errorf("always stale: %v after %d queries; want stale after %d", err, len(q.specs), StaleAttempts)
	}
	if waited := time.Since(start); waited < 150*time.Millisecond {
		t.Errorf("waited %v, want 50 ms then 100 ms between the attempts", waited)
	}
}

// TestQueryFreshStopsWithTheContext: a context that ends during the wait
// ends the retries, and an answer other than stale is never retried.
func TestQueryFreshStopsWithTheContext(t *testing.T) {
	spec := func() (Tables, error) { return Tables{Version: TablesVersion}, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	q := &staleQuerier{n: 99}
	if _, err := QueryFresh(ctx, q, Request{}, spec); err == nil || len(q.specs) != 1 {
		t.Errorf("%d queries, err %v; want one before the deadline", len(q.specs), err)
	}
	other := errors.New("boom")
	n := 0
	if _, err := QueryFresh(context.Background(), querierFunc(func() error { n++; return other }), Request{}, spec); !errors.Is(err, other) || n != 1 {
		t.Errorf("%d queries, err %v; want one", n, err)
	}
	if _, err := QueryFresh(context.Background(), q, Request{}, func() (Tables, error) { return Tables{}, other }); !errors.Is(err, other) {
		t.Errorf("a spec that fails: %v", err)
	}
}

type querierFunc func() error

func (f querierFunc) Query(context.Context, Request) (Result, error) { return Result{}, f() }
