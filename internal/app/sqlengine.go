package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/queue"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// healthTimeout bounds the start-up call to the engine's health route.
const healthTimeout = 5 * time.Second

// errSQLEngineProtocol fails a start whose engine speaks another version
// of the protocol than this release.
var errSQLEngineProtocol = errors.New("app: the SQL engine speaks another protocol")

// openSQL is the client of the external SQL engine at socket
// (SQL_ENGINE_SOCKET), nil when socket is "" (no engine: SQL is refused).
// It asks the engine's health first: an engine that answers with another
// protocol version fails the start, since every query would fail; one that
// does not answer is logged, and each query tries it again, so an engine
// started after twillingate, or restarted, needs no restart of ours. The
// caller closes the client.
func openSQL(ctx context.Context, socket string, logger *slog.Logger) (*engineSQL, error) {
	if socket == "" {
		logger.Info("no SQL engine configured (SQL_ENGINE_SOCKET): SQL widgets and the query tool's sql are refused")
		return nil, nil
	}
	e := newEngineSQL(sqlengine.Dial(socket), logger)
	h, err := e.health(ctx)
	switch {
	case err != nil:
		logger.Warn("the SQL engine does not answer; SQL fails until it does", "socket", socket, "error", err.Error())
	case h.Protocol != sqlengine.Protocol:
		e.Close()
		return nil, fmt.Errorf("%w: %s at %s speaks protocol %d, this release %d (SQL_ENGINE_SOCKET)",
			errSQLEngineProtocol, h.Engine, socket, h.Protocol, sqlengine.Protocol)
	default:
		logger.Info("SQL engine", "socket", socket, "engine", h.Engine, "protocol", h.Protocol, "slots", e.slots.Load())
	}
	return e, nil
}

// engineSQL is the engine's client behind a queue as wide as the engine's
// slots (sqlengine.Health.Slots): the engine answers a query past them
// busy, so the queries twillingate sends together, a dashboard's sql
// widgets loading at once, wait their turn here instead
// (reporting.Queued), at most queueWait. The width is learnt from the
// engine's health, at start or, while the engine has not answered it,
// before the next query; until then, or when the engine does not say, it
// is 1. An engine whose slots change is learnt again on twillingate's
// restart.
type engineSQL struct {
	*sqlengine.Client
	slots atomic.Int64 // the engine's Health.Slots; 0 until known
	known atomic.Bool  // the engine's health answered
	queue *queue.Queue
	wait  time.Duration // queueWait; a test shortens it
}

func newEngineSQL(c *sqlengine.Client, logger *slog.Logger) *engineSQL {
	e := &engineSQL{Client: c, wait: queueWait}
	e.queue = queue.New(func() int { return int(e.slots.Load()) })
	return e
}

// health asks the engine's health under healthTimeout and, when it
// answers with this protocol, keeps its slots.
func (e *engineSQL) health(ctx context.Context) (sqlengine.Health, error) {
	hctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	h, err := e.Health(hctx)
	if err == nil && h.Protocol == sqlengine.Protocol {
		e.slots.Store(int64(max(h.Slots, 1)))
		e.known.Store(true)
		e.queue.Wake() // the queries already waiting may fit now
	}
	return h, err
}

// queueWait bounds the wait for a turn on the engine: past it, the query
// is answered busy, as the engine would, rather than waiting on.
const queueWait = 30 * time.Second

// errQueueWait is the engine's busy answer to a query that waited
// queueWait without a turn.
var errQueueWait = &sqlengine.Error{Status: sqlengine.KindBusy.Status(), Kind: sqlengine.KindBusy,
	Message: "no turn on the SQL engine within " + queueWait.String()}

// Acquire waits for a turn on the engine (reporting.Queued), at most
// queueWait.
func (e *engineSQL) Acquire(ctx context.Context) (func(), error) {
	wctx, cancel := context.WithTimeout(ctx, e.wait)
	defer cancel()
	release, err := e.queue.Acquire(wctx)
	if err != nil && ctx.Err() == nil {
		return nil, errQueueWait
	}
	return release, err
}

// Query asks the engine's health first while it has not answered, then
// sends req.
func (e *engineSQL) Query(ctx context.Context, req sqlengine.Request) (sqlengine.Result, error) {
	if !e.known.Load() {
		_, _ = e.health(ctx) // a failure is the query's to report
	}
	return e.Client.Query(ctx, req)
}

// lazySQL is the engine at socket opened (openSQL, health included) on
// its first query rather than at start: a command whose migrator finds no
// sql system widget to check (`twillingate project …`, an ingest-only
// serve) never calls the engine. A protocol mismatch fails that query and
// every one after; an engine that does not answer is logged and tried
// again by each query, as openSQL's client is.
type lazySQL struct {
	socket string
	logger *slog.Logger
	once   sync.Once
	client *engineSQL
	err    error
}

// newLazySQL is lazySQL at socket, nil when socket is "" (no engine).
func newLazySQL(socket string, logger *slog.Logger) *lazySQL {
	if socket == "" {
		return nil
	}
	return &lazySQL{socket: socket, logger: logger}
}

func (l *lazySQL) open(ctx context.Context) {
	l.once.Do(func() { l.client, l.err = openSQL(ctx, l.socket, l.logger) })
}

// Acquire opens the engine and waits for a turn on it
// (reporting.Queued); an engine that fails to open leaves the error to
// Query.
func (l *lazySQL) Acquire(ctx context.Context) (func(), error) {
	l.open(ctx)
	if l.err != nil {
		return func() {}, nil
	}
	return l.client.Acquire(ctx)
}

func (l *lazySQL) Query(ctx context.Context, req sqlengine.Request) (sqlengine.Result, error) {
	l.open(ctx)
	if l.err != nil {
		return sqlengine.Result{}, l.err
	}
	return l.client.Query(ctx, req)
}

// Close drops the client's idle connections, if it was opened.
func (l *lazySQL) Close() {
	if l.client != nil {
		l.client.Close()
	}
}
