package sqlbind

import (
	"context"
	"errors"
	"time"

	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// Failure is what a failed query on the SQL engine comes to, as every
// caller that runs a person's SQL on it tells the cases apart: the query
// tool and widgets word each their own way.
type Failure uint8

const (
	// FailOther is no failure of the engine's: the caller went away
	// (whatever the engine said last), or an error from elsewhere. The
	// caller keeps err as it is.
	FailOther Failure = iota
	// FailDeadline is ctx's deadline, or an error that wraps it.
	FailDeadline
	// FailTimeout is the engine's own timeout (KindTimeout).
	FailTimeout
	FailLogsOutOfMemory
	FailOutOfMemory
	// FailRefused is SQL or a request the engine refused (KindRefused).
	FailRefused
	// FailSQL is SQL that failed to parse or run (KindSQL).
	FailSQL
	FailExited
	FailBusy
	FailStale
	// FailTooLarge is an answer past the client's cap.
	FailTooLarge
	// FailEngine is an engine that failed with no word on the SQL, or
	// could not be reached.
	FailEngine
)

// Classify is what err, from a query on the engine run under ctx (whose
// deadline is timeout, CONSOLE_QUERY_TIMEOUT), comes to, and its text:
// the engine's own message for FailRefused and FailSQL, the advice that
// follows the failure for the others but FailOther, which has none. A
// deadline (ctx's or err's) wins over anything the engine said, and a
// caller that went away over the rest.
func Classify(ctx context.Context, err error, timeout time.Duration) (Failure, string) {
	var se *sqlengine.Error
	isKind := func(k sqlengine.Kind) bool { return errors.As(err, &se) && se.Kind == k }
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return FailDeadline, Timeout(timeout)
	case ctx.Err() != nil:
		return FailOther, ""
	case isKind(sqlengine.KindTimeout):
		return FailTimeout, Timeout(timeout)
	case isKind(sqlengine.KindOutOfMemoryLoadingLog):
		return FailLogsOutOfMemory, LogsOutOfMemory
	case isKind(sqlengine.KindOutOfMemory):
		return FailOutOfMemory, OutOfMemory
	case isKind(sqlengine.KindRefused):
		return FailRefused, se.Message
	case isKind(sqlengine.KindSQL):
		return FailSQL, se.Message
	case isKind(sqlengine.KindExited):
		return FailExited, EngineExited
	case isKind(sqlengine.KindBusy):
		return FailBusy, Busy
	case isKind(sqlengine.KindStale):
		return FailStale, Stale
	case errors.Is(err, sqlengine.ErrTooLarge):
		return FailTooLarge, TooLarge
	case errors.As(err, &se), errors.Is(err, sqlengine.ErrUnavailable):
		return FailEngine, EngineFailed
	}
	return FailOther, ""
}
