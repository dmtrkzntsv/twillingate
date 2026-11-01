package sqlbind

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// TestClassify: each failure of the engine comes to its own case, in the
// order the callers relied on: a deadline before anything the engine
// said, a caller that went away before the engine's kinds.
func TestClassify(t *testing.T) {
	engine := func(k sqlengine.Kind, msg string) error {
		return fmt.Errorf("wrapped: %w", &sqlengine.Error{Status: k.Status(), Kind: k, Message: msg})
	}
	live := context.Background()
	late, cancelLate := context.WithDeadline(live, time.Now().Add(-time.Second))
	defer cancelLate()
	gone, cancelGone := context.WithCancel(live)
	cancelGone()
	const d = 30 * time.Second
	for _, c := range []struct {
		ctx  context.Context
		err  error
		want Failure
		msg  string
	}{
		{late, engine(sqlengine.KindSQL, "x"), FailDeadline, Timeout(d)},
		{live, fmt.Errorf("w: %w", context.DeadlineExceeded), FailDeadline, Timeout(d)},
		{gone, engine(sqlengine.KindSQL, "x"), FailOther, ""},
		{live, engine(sqlengine.KindTimeout, "x"), FailTimeout, Timeout(d)},
		{live, engine(sqlengine.KindOutOfMemoryLoadingLog, "x"), FailLogsOutOfMemory, LogsOutOfMemory},
		{live, engine(sqlengine.KindOutOfMemory, "x"), FailOutOfMemory, OutOfMemory},
		{live, engine(sqlengine.KindRefused, "reads past"), FailRefused, "reads past"},
		{live, engine(sqlengine.KindSQL, "Parser Error"), FailSQL, "Parser Error"},
		{live, engine(sqlengine.KindExited, "x"), FailExited, EngineExited},
		{live, engine(sqlengine.KindBusy, "x"), FailBusy, Busy},
		{live, engine(sqlengine.KindStale, "x"), FailStale, Stale},
		{live, fmt.Errorf("w: %w", sqlengine.ErrTooLarge), FailTooLarge, TooLarge},
		{live, engine(sqlengine.KindInternal, "x"), FailEngine, EngineFailed},
		{live, fmt.Errorf("w: %w", sqlengine.ErrUnavailable), FailEngine, EngineFailed},
		{live, errors.New("elsewhere"), FailOther, ""},
	} {
		if f, msg := Classify(c.ctx, c.err, d); f != c.want || msg != c.msg {
			t.Errorf("Classify(%v) = %d %q, want %d %q", c.err, f, msg, c.want, c.msg)
		}
	}
}
