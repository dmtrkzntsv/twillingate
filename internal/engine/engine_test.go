package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// childEnv makes this test binary act as `twillingate engine` when an
// Engine spawns it, so these tests exercise a real process and pipe.
const childEnv = "ENGINE_TEST_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		if err := ServeChild(os.Args[1:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testOptions(t *testing.T) Options {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return Options{Path: self, Env: []string{childEnv + "=1"},
		AllowedDirs: []string{filepath.Join(dir, "raw")}, SpillRoot: filepath.Join(dir, "spill"),
		SlotDir: filepath.Join(dir, "slots"), MaxChildren: 1, MemoryLimit: "256MB", Threads: 1,
		IdleExit: time.Minute}
}

func newEngine(t *testing.T, o Options) *Engine {
	t.Helper()
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// eventually polls cond for up to 5s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("after 5s: %s", what)
}

func TestQuerySpawnsAndReusesAChild(t *testing.T) {
	e := newEngine(t, testOptions(t))
	if e.pid() != 0 {
		t.Fatal("a child before the first query")
	}
	res, err := e.Query(context.Background(), "SELECT 42 AS answer", 0)
	if err != nil || len(res.Rows) != 1 || fmt.Sprint(res.Rows[0][0]) != "42" {
		t.Fatalf("Query = %+v, %v", res, err)
	}
	pid := e.pid()
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil || e.pid() != pid {
		t.Fatalf("second query: err %v, pid %d → %d; want the same child", err, pid, e.pid())
	}
}

func TestQueryErrorIsTypedAndKeepsTheChild(t *testing.T) {
	e := newEngine(t, testOptions(t))
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil {
		t.Fatal(err)
	}
	pid := e.pid()
	_, err := e.Query(context.Background(), "SELECT nope", 0)
	var qe *QueryError
	if !errors.As(err, &qe) || e.pid() != pid {
		t.Fatalf("err = %v (pid %d → %d), want a *QueryError from the same child", err, pid, e.pid())
	}
}

func TestExecThenQuery(t *testing.T) {
	e := newEngine(t, testOptions(t))
	if err := e.Exec(context.Background(), "CREATE TABLE hot AS SELECT 7 AS n"); err != nil {
		t.Fatal(err)
	}
	res, err := e.Query(context.Background(), "SELECT n FROM hot", 0)
	if err != nil || fmt.Sprint(res.Rows[0][0]) != "7" {
		t.Fatalf("Query = %+v, %v", res, err)
	}
}

func TestQueryBindsNamedArguments(t *testing.T) {
	e := newEngine(t, testOptions(t))
	res, err := e.Query(context.Background(),
		"SELECT CAST($a AS BIGINT) + 1 AS n, CAST($d AS DATE) AS d", 0,
		Arg{"a", "41"}, Arg{"d", "2026-01-02"})
	if err != nil || len(res.Rows) != 1 || fmt.Sprint(res.Rows[0]) != "[42 2026-01-02]" {
		t.Fatalf("Query = %+v, %v; want [[42 2026-01-02]]", res, err)
	}
}

func TestQueryMissingArgumentIsAQueryError(t *testing.T) {
	e := newEngine(t, testOptions(t))
	_, err := e.Query(context.Background(), "SELECT CAST($a AS BIGINT)", 0)
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("err = %v, want a *QueryError", err)
	}
}

// TestChildRunsInUTC: the process's own zone (TZ) must not reach the
// SQL, or the same date would mean different instants on different hosts.
func TestChildRunsInUTC(t *testing.T) {
	o := testOptions(t)
	o.Env = append(o.Env, "TZ=America/Los_Angeles")
	e := newEngine(t, o)
	res, err := e.Query(context.Background(),
		"SELECT current_setting('TimeZone'), CAST(TIMESTAMPTZ '2026-01-02 00:00:00+00' AS VARCHAR)", 0)
	if err != nil || fmt.Sprint(res.Rows[0]) != "[UTC 2026-01-02 00:00:00+00]" {
		t.Fatalf("Query = %+v, %v; want UTC", res, err)
	}
}

// TestIdleChildExits: an idle child leaves, freeing its slot and its spill
// directory, and the next query starts another.
func TestIdleChildExits(t *testing.T) {
	o := testOptions(t)
	o.IdleExit = 200 * time.Millisecond
	e := newEngine(t, o)
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil {
		t.Fatal(err)
	}
	pid := e.pid()
	eventually(t, "the idle child gone", func() bool { return e.pid() == 0 })
	eventually(t, "the slot free", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		s, err := acquireSlot(ctx, o.SlotDir, 1)
		if err == nil {
			s.release()
		}
		return err == nil
	})
	eventually(t, "the spill directory removed", func() bool {
		entries, err := os.ReadDir(o.SpillRoot)
		return err == nil && len(entries) == 0
	})
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil || e.pid() == 0 || e.pid() == pid {
		t.Fatalf("after idle: err %v, pid %d (was %d); want a new child", err, e.pid(), pid)
	}
}

// TestIdleTimeCountsFromTheLastQuery: the idle period starts when the last
// request finishes, so a query running longer than IdleExit is never cut
// off, and the child stays for IdleExit after it.
func TestIdleTimeCountsFromTheLastQuery(t *testing.T) {
	o := testOptions(t)
	// Short beside the query (about 1.3 s on a CI runner, 3 s on a laptop),
	// so the query outlasts it many times over on any machine.
	o.IdleExit = 100 * time.Millisecond
	e := newEngine(t, o)
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil {
		t.Fatal(err)
	}
	pid := e.pid()
	start := time.Now()
	res, err := e.Query(context.Background(), "SELECT count(*) FROM range(400000000) t(i) WHERE i % 7 = 3", 0)
	took := time.Since(start)
	if err != nil || len(res.Rows) != 1 {
		t.Fatalf("long query: %+v, %v", res, err)
	}
	if took < 2*o.IdleExit {
		t.Fatalf("the long query took %v, under twice IdleExit (%v): lengthen it, or this test proves nothing", took, o.IdleExit)
	}
	if e.pid() != pid {
		t.Fatalf("pid %d → %d: the child was replaced during a query longer than IdleExit", pid, e.pid())
	}
	time.Sleep(o.IdleExit / 3)
	if e.pid() != pid {
		t.Fatalf("child gone %v after the last query, before IdleExit (%v)", o.IdleExit/3, o.IdleExit)
	}
	eventually(t, "the idle child gone after IdleExit", func() bool { return e.pid() == 0 })
}

// TestCrashedChildIsReplaced is Review Focus 2.
func TestCrashedChildIsReplaced(t *testing.T) {
	e := newEngine(t, testOptions(t))
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil {
		t.Fatal(err)
	}
	pid := e.pid()
	errc := make(chan error, 1)
	go func() {
		_, err := e.Query(context.Background(), slowSQL, 0)
		errc <- err
	}()
	time.Sleep(300 * time.Millisecond)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, ErrChildExited) {
			t.Fatalf("in-flight query = %v, want ErrChildExited", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight query still waiting 5s after its child was killed")
	}
	eventually(t, "the dead child cleared", func() bool { return e.pid() == 0 })
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil || e.pid() == pid {
		t.Fatalf("after the crash: err %v, pid %d (was %d); want a new child", err, e.pid(), pid)
	}
}

// TestCancelInterruptsTheQuery is Review Focus 3: the caller gets its
// context's error, and the child, which runs one query at a time here,
// answers the next query at once.
func TestCancelInterruptsTheQuery(t *testing.T) {
	e := newEngine(t, testOptions(t))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := e.Query(ctx, slowSQL, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	start := time.Now()
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("next query: %v after %v; the slow one was not interrupted", err, time.Since(start))
	}
}

// TestSlotsBoundChildrenAcrossEngines: two engines on one slot directory
// with one slot run one child between them.
func TestSlotsBoundChildrenAcrossEngines(t *testing.T) {
	o := testOptions(t)
	a, b := newEngine(t, o), newEngine(t, o)
	if _, err := a.Query(context.Background(), "SELECT 1", 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := b.Query(ctx, "SELECT 1", 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second engine = %v, want the deadline while the only slot is held", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Query(context.Background(), "SELECT 1", 0); err != nil {
		t.Fatalf("after the first engine closed: %v", err)
	}
}

func TestBadMemoryLimitFailsTheSpawnAndFreesTheSlot(t *testing.T) {
	o := testOptions(t)
	o.MemoryLimit = "lots"
	e := newEngine(t, o)
	if _, err := e.Query(context.Background(), "SELECT 1", 0); !errors.Is(err, ErrSpawn) {
		t.Fatalf("err = %v, want ErrSpawn", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := acquireSlot(ctx, o.SlotDir, 1)
	if err != nil {
		t.Fatalf("slot still held after a failed spawn: %v", err)
	}
	s.release()
}

func TestClosedEngineRefuses(t *testing.T) {
	e := newEngine(t, testOptions(t))
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Query(context.Background(), "SELECT 1", 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

// TestRequestAfterACrashIsNotRefused: a request that starts once the dead
// child is no longer the engine's child must get a new one, not
// ErrChildExited. The window between the child's death and its removal is
// hard to hit deterministically; this documents the intent and catches the
// gross case.
func TestRequestAfterACrashIsNotRefused(t *testing.T) {
	e := newEngine(t, testOptions(t))
	if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(e.pid(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		gone := e.pid() == 0
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := e.Query(ctx, "SELECT 1", 0)
		cancel()
		if err == nil {
			return
		}
		if gone || !errors.Is(err, ErrChildExited) {
			t.Fatalf("Query = %v (child already cleared: %v); want a new child or the dead one's ErrChildExited only before it was cleared", err, gone)
		}
	}
	t.Fatal("no query succeeded within 5s of the crash")
}

// TestEndedContextSpawnsNothing: a context that has ended starts no child.
func TestEndedContextSpawnsNothing(t *testing.T) {
	e := newEngine(t, testOptions(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.Query(ctx, "SELECT 1", 0)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrSpawn) {
		t.Fatalf("err = %v, want context.Canceled and not ErrSpawn", err)
	}
	if e.pid() != 0 {
		t.Fatalf("pid %d, want no child", e.pid())
	}
}

func TestCloseFailsInFlightRequestsWithErrClosed(t *testing.T) {
	e := newEngine(t, testOptions(t))
	errc := make(chan error, 1)
	go func() {
		_, err := e.Query(context.Background(), slowSQL, 0)
		errc <- err
	}()
	time.Sleep(300 * time.Millisecond)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; !errors.Is(err, ErrClosed) {
		t.Fatalf("in-flight query at Close = %v, want ErrClosed", err)
	}
}

// TestIdleChildYieldsItsSlotToAWaiter: an idle child gives its slot up
// when another engine waits for one, rather than holding it for IdleExit.
func TestIdleChildYieldsItsSlotToAWaiter(t *testing.T) {
	o := testOptions(t) // MaxChildren 1, IdleExit 1 minute
	a, b := newEngine(t, o), newEngine(t, o)
	if _, err := a.Query(context.Background(), "SELECT 1", 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := b.Query(ctx, "SELECT 1", 0); err != nil {
		t.Fatalf("waiting engine: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the waiter got a slot after %v; the idle child held it", took)
	}
	eventually(t, "the idle child retired", func() bool { return a.pid() == 0 })
}

// TestStaleIdleCallbackNeitherRetiresNorRearms: a callback armed before a
// request ran (its generation is old) must leave the child alone, even
// with its deadline long past; one of the current generation retires it.
func TestStaleIdleCallbackNeitherRetiresNorRearms(t *testing.T) {
	e := newEngine(t, testOptions(t))
	query := func() {
		t.Helper()
		if _, err := e.Query(context.Background(), "SELECT 1", 0); err != nil {
			t.Fatal(err)
		}
	}
	query()
	e.mu.Lock()
	c, stale := e.child, e.child.gen
	e.mu.Unlock()
	query() // a request in the gap: the callback armed at `stale` is now old

	past := time.Now().Add(-time.Second)
	e.idleCheck(c, past, stale) // deadline passed: would retire
	if e.pid() == 0 {
		t.Fatal("a stale callback retired the child")
	}
	future := time.Now().Add(time.Hour)
	e.mu.Lock()
	timer := c.idle
	e.mu.Unlock()
	e.idleCheck(c, future, stale) // deadline ahead: would re-arm
	e.mu.Lock()
	rearmed := c.idle != timer
	cur := c.gen
	e.mu.Unlock()
	if rearmed {
		t.Fatal("a stale callback replaced the child's idle timer")
	}
	e.idleCheck(c, past, cur)
	if e.pid() != 0 {
		t.Fatal("a current callback past its deadline did not retire the child")
	}
}
