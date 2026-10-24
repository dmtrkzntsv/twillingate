package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

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
func openSQL(ctx context.Context, socket string, logger *slog.Logger) (*sqlengine.Client, error) {
	if socket == "" {
		logger.Info("no SQL engine configured (SQL_ENGINE_SOCKET): SQL widgets and the query tool's sql are refused")
		return nil, nil
	}
	c := sqlengine.Dial(socket)
	hctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	h, err := c.Health(hctx)
	switch {
	case err != nil:
		logger.Warn("the SQL engine does not answer; SQL fails until it does", "socket", socket, "error", err.Error())
	case h.Protocol != sqlengine.Protocol:
		c.Close()
		return nil, fmt.Errorf("%w: %s at %s speaks protocol %d, this release %d (SQL_ENGINE_SOCKET)",
			errSQLEngineProtocol, h.Engine, socket, h.Protocol, sqlengine.Protocol)
	default:
		logger.Info("SQL engine", "socket", socket, "engine", h.Engine, "protocol", h.Protocol)
	}
	return c, nil
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
	client *sqlengine.Client
	err    error
}

// newLazySQL is lazySQL at socket, nil when socket is "" (no engine).
func newLazySQL(socket string, logger *slog.Logger) *lazySQL {
	if socket == "" {
		return nil
	}
	return &lazySQL{socket: socket, logger: logger}
}

func (l *lazySQL) Query(ctx context.Context, req sqlengine.Request) (sqlengine.Result, error) {
	l.once.Do(func() { l.client, l.err = openSQL(ctx, l.socket, l.logger) })
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
