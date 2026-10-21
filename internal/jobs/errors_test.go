package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// errBoom is the sentinel every faultyStore failure returns, so assertions
// can check the log line names the failing operation without depending on
// message wording elsewhere.
var errBoom = errors.New("boom")

// faultyStore wraps a real store.Store and makes named methods fail.
type faultyStore struct {
	store.Store
	failAlways map[string]bool
}

func newFaultyStore(st store.Store) *faultyStore {
	return &faultyStore{Store: st, failAlways: map[string]bool{}}
}

func (f *faultyStore) failing(name string) *faultyStore {
	f.failAlways[name] = true
	return f
}

func (f *faultyStore) shouldFail(name string) bool { return f.failAlways[name] }

func (f *faultyStore) IncrementalVacuum(ctx context.Context) error {
	if f.shouldFail("IncrementalVacuum") {
		return errBoom
	}
	return f.Store.IncrementalVacuum(ctx)
}

func (f *faultyStore) RecordUsageHistory(ctx context.Context, now time.Time, fileBytes map[int64]int64) error {
	if f.shouldFail("RecordUsageHistory") {
		return errBoom
	}
	return f.Store.RecordUsageHistory(ctx, now, fileBytes)
}

// setupFaulty is setup, but returns a *Runner built over a faultyStore so
// the caller can arrange targeted failures before calling RunDailyPass, plus
// a buffer capturing every log line the run produces.
func setupFaulty(t *testing.T, vars map[string]string, specs []manage.ProjectSpec) (store.Store, *faultyStore, *Runner, *bytes.Buffer) {
	t.Helper()
	cfg := configtest.Load(t, vars)
	st, _ := openStoreAt(t)
	reg := newRegistry(t, st, cfg, specs)
	fst := newFaultyStore(st)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	r := New(fst, cfg, reg, identity.NewSalter(st, now), logger, now)
	return st, fst, r, &buf
}

func logged(buf *bytes.Buffer, substr string) bool {
	return strings.Contains(buf.String(), substr)
}

// --- failures are logged and the pass goes on ---

func TestRunDailyPassLogsIncrementalVacuumFailure(t *testing.T) {
	_, fst, r, buf := setupFaulty(t, jobsVars, jobsProjectSpecs)
	fst.failing("IncrementalVacuum")
	r.RunDailyPass(context.Background())
	if !logged(buf, "incremental vacuum failed") {
		t.Errorf("log output = %q, want mention of incremental vacuum failed", buf.String())
	}
}

// The measurement is the last step: a failure is logged, and the earlier
// steps have already run.
func TestRunDailyPassLogsRecordUsageHistoryFailure(t *testing.T) {
	_, fst, r, buf := setupFaulty(t, jobsVars, jobsProjectSpecs)
	fst.failing("RecordUsageHistory")
	r.RunDailyPass(context.Background())
	if !logged(buf, "record usage history") || !logged(buf, "boom") {
		t.Errorf("log output = %q, want mention of record usage history and boom", buf.String())
	}
}

// --- runScheduled / Run: scheduler-level failures are logged, not fatal ---

// failingRotator lets a test fail Rotate and/or Current independently of
// the real identity.Salter, which always succeeds against a healthy store.
type failingRotator struct {
	rotateErr  error
	currentErr error
}

func (f *failingRotator) Rotate(context.Context) error            { return f.rotateErr }
func (f *failingRotator) Current(context.Context) (string, error) { return "", f.currentErr }

func TestRunScheduledLogsSaltRotationFailure(t *testing.T) {
	cfg := configtest.Load(t, jobsVars)
	st := openStore(t)
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	now := func() time.Time { return time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC) }
	r := New(st, cfg, reg, &failingRotator{rotateErr: errBoom}, logger, now)

	r.runScheduled(context.Background())

	if !logged(&buf, "salt rotation") || !logged(&buf, "boom") {
		t.Errorf("log output = %q, want mention of salt rotation and boom", buf.String())
	}
}

func TestRunLogsInitialSaltFailure(t *testing.T) {
	cfg := configtest.Load(t, jobsVars)
	st := openStore(t)
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	r := New(st, cfg, reg, &failingRotator{currentErr: errBoom}, logger, now)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	if !logged(&buf, "initial salt") || !logged(&buf, "boom") {
		t.Errorf("log output = %q, want mention of initial salt and boom", buf.String())
	}
}
