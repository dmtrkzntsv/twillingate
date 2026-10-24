package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
