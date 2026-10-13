package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"
)

// DefaultIdleExit is how long a child waits without a request before it
// exits and gives its memory back (D8: "a few idle minutes").
const DefaultIdleExit = 3 * time.Minute

var (
	// ErrSpawn: the child could not be started or did not get ready.
	ErrSpawn = errors.New("engine: the child did not start")
	// ErrChildExited: the child died with the request in flight (killed for
	// memory, crashed); the next request starts another.
	ErrChildExited = errors.New("engine: the child exited")
	// ErrClosed: the engine was closed.
	ErrClosed = errors.New("engine: closed")
)

// QueryError is SQL the child ran and DuckDB refused; the child is fine.
type QueryError struct{ Msg string }

func (e *QueryError) Error() string { return e.Msg }

// Result is a query's rows. Numbers are json.Number; dates, times and
// UUIDs are strings; NULL is nil.
type Result struct {
	Columns   []string
	Rows      [][]any
	Truncated bool // maxRows cut the result short
}

// Options configure the children an Engine spawns. AllowedDirs must not
// hold the SQLite file or anything else widget SQL must not reach: SQL in
// the child can read and write there (app.EngineOptions allows raw/ only).
// Besides AllowedDirs the child can write its own spill directory (DuckDB's
// temp_directory, under SpillRoot, removed when the child exits).
type Options struct {
	Path        string
	Args        []string
	Env         []string
	AllowedDirs []string
	SpillRoot   string
	SlotDir     string
	MaxChildren int
	MemoryLimit string
	Threads     int
	Parallel    int
	IdleExit    time.Duration
}

// Engine runs requests on one child at a time, spawned on the first
// request, kept while requests come, and told to exit after IdleExit
// without one.
type Engine struct {
	o         Options
	spawnGate chan struct{} // capacity 1: one spawn at a time, and a waiter can give up
	mu        sync.Mutex    // guards everything below and each child's mutable fields
	next      uint64
	child     *child
	closed    bool
}

type child struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	enc     *json.Encoder
	pending map[uint64]chan reply
	busy    int
	idle    *time.Timer
	slot    *slot
	spill   string
	done    chan struct{} // closed after exit, once the slot and spill are released
}

// New fills the defaults and makes the directories absolute: the child
// resolves paths from its own working directory, DuckDB's allow list
// needs them whole.
func New(o Options) (*Engine, error) {
	if o.Threads <= 0 {
		// GOMAXPROCS follows the cgroup CPU quota (Go 1.25), NumCPU does not:
		// a container or a systemd CPUQuota must not be oversubscribed.
		o.Threads = runtime.GOMAXPROCS(0)
	}
	if o.Parallel <= 0 {
		o.Parallel = 1
		if o.Threads > 2 {
			o.Parallel = 2
		}
	}
	if o.MaxChildren < 1 {
		o.MaxChildren = 1
	}
	if o.MemoryLimit == "" {
		o.MemoryLimit = DefaultMemoryLimit
	}
	if o.IdleExit <= 0 {
		o.IdleExit = DefaultIdleExit
	}
	o.AllowedDirs = slices.Clone(o.AllowedDirs)
	for i, d := range o.AllowedDirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			return nil, fmt.Errorf("engine: %w", err)
		}
		o.AllowedDirs[i] = abs
	}
	for _, p := range []*string{&o.SpillRoot, &o.SlotDir} {
		abs, err := filepath.Abs(*p)
		if err != nil {
			return nil, fmt.Errorf("engine: %w", err)
		}
		*p = abs
	}
	return &Engine{o: o, spawnGate: make(chan struct{}, 1)}, nil
}

// Query runs sql and returns at most maxRows rows (all when maxRows ≤ 0).
func (e *Engine) Query(ctx context.Context, sql string, maxRows int) (Result, error) {
	rep, err := e.do(ctx, request{SQL: sql, MaxRows: maxRows})
	if err != nil {
		return Result{}, err
	}
	return Result{Columns: rep.Columns, Rows: rep.Rows, Truncated: rep.Truncated}, nil
}

// Exec runs sql for its effect on the child, such as a table later
// queries read; the effect lasts as long as the child.
func (e *Engine) Exec(ctx context.Context, sql string) error {
	_, err := e.do(ctx, request{SQL: sql, Exec: true})
	return err
}

func (e *Engine) do(ctx context.Context, r request) (reply, error) {
	c, err := e.start(ctx)
	if err != nil {
		return reply{}, err
	}
	ch := make(chan reply, 1)
	e.mu.Lock()
	e.next++
	r.ID = e.next
	c.pending[r.ID] = ch
	err = c.enc.Encode(r)
	e.mu.Unlock()
	defer e.finish(c, r.ID)
	if err != nil {
		return reply{}, fmt.Errorf("%w: %v", ErrChildExited, err)
	}
	select {
	case rep := <-ch:
		return answer(rep)
	case <-c.done:
		select {
		case rep := <-ch:
			return answer(rep)
		default:
			return reply{}, ErrChildExited
		}
	case <-ctx.Done():
		e.mu.Lock()
		_ = c.enc.Encode(request{ID: r.ID, Cancel: true})
		e.mu.Unlock()
		return reply{}, ctx.Err()
	}
}

func answer(rep reply) (reply, error) {
	if rep.Err != "" {
		return reply{}, &QueryError{Msg: rep.Err}
	}
	return rep, nil
}

// start returns the running child with one more request counted on it,
// spawning one when there is none.
func (e *Engine) start(ctx context.Context) (*child, error) {
	select {
	case e.spawnGate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-e.spawnGate }()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, ErrClosed
	}
	if c := e.child; c != nil {
		c.busy++
		if c.idle != nil {
			c.idle.Stop()
			c.idle = nil
		}
		e.mu.Unlock()
		return c, nil
	}
	e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := e.spawn(ctx)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		c.stdin.Close()
		return nil, ErrClosed
	}
	e.child = c
	c.busy++
	return c, nil
}

// finish forgets a request and, when it was the child's last, arms the
// idle timer.
func (e *Engine) finish(c *child, id uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(c.pending, id)
	c.busy--
	if c.busy == 0 && e.child == c {
		c.idle = time.AfterFunc(e.o.IdleExit, func() { e.retire(c) })
	}
}

// retire tells an idle child to exit by closing its stdin, unless a
// request arrived since the timer fired.
func (e *Engine) retire(c *child) {
	e.mu.Lock()
	if e.child != c || c.busy > 0 {
		e.mu.Unlock()
		return
	}
	e.child = nil
	e.mu.Unlock()
	c.stdin.Close()
}

// spawn takes a slot, starts the child and waits for its ready line.
func (e *Engine) spawn(ctx context.Context) (*child, error) {
	sl, err := acquireSlot(ctx, e.o.SlotDir, e.o.MaxChildren)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			sl.release()
		}
	}()
	for _, d := range append([]string{e.o.SpillRoot}, e.o.AllowedDirs...) {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSpawn, err)
		}
	}
	spill, err := os.MkdirTemp(e.o.SpillRoot, "child-")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSpawn, err)
	}
	defer func() {
		if !ok {
			os.RemoveAll(spill)
		}
	}()
	args := append(slices.Clone(e.o.Args),
		"-threads", strconv.Itoa(e.o.Threads), "-memory", e.o.MemoryLimit,
		"-spill", spill, "-parallel", strconv.Itoa(e.o.Parallel))
	for _, d := range e.o.AllowedDirs {
		args = append(args, "-allow", d)
	}
	cmd := exec.Command(e.o.Path, args...)
	cmd.Env = append(os.Environ(), e.o.Env...)
	cmd.Stderr = os.Stderr // a panic in the child reaches the server's log
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSpawn, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSpawn, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSpawn, err)
	}
	dec := json.NewDecoder(stdout)
	dec.UseNumber()
	var first reply
	readErr := make(chan error, 1)
	go func() { readErr <- dec.Decode(&first) }()
	select {
	case err = <-readErr:
		if err == nil && !first.Ready {
			err = errors.New(first.Err)
		}
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, fmt.Errorf("%w: %w", ErrSpawn, err)
	}
	ok = true
	c := &child{cmd: cmd, stdin: stdin, enc: json.NewEncoder(stdin), pending: map[uint64]chan reply{},
		slot: sl, spill: spill, done: make(chan struct{})}
	go e.read(c, dec)
	return c, nil
}

// read delivers replies until the child's stdout ends, then reaps the
// child and frees what it held.
func (e *Engine) read(c *child, dec *json.Decoder) {
	for {
		var rep reply
		if err := dec.Decode(&rep); err != nil {
			break
		}
		e.mu.Lock()
		ch := c.pending[rep.ID]
		e.mu.Unlock()
		if ch != nil {
			ch <- rep
		}
	}
	// Drop the dead child first: a request arriving while it is reaped
	// (a big spill directory takes a while) must get a new one.
	e.mu.Lock()
	if e.child == c {
		e.child = nil
	}
	if c.idle != nil {
		c.idle.Stop()
	}
	e.mu.Unlock()
	c.stdin.Close()
	c.cmd.Wait()
	c.slot.release()
	os.RemoveAll(c.spill)
	close(c.done)
}

// Close tells the child to exit, kills it after 5s, and refuses later
// requests.
func (e *Engine) Close() error {
	e.mu.Lock()
	e.closed = true
	c := e.child
	e.child = nil
	e.mu.Unlock()
	if c == nil {
		return nil
	}
	c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		c.cmd.Process.Kill()
		<-c.done
	}
	return nil
}

// pid is the running child's process id, 0 when there is none (tests).
func (e *Engine) pid() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.child == nil {
		return 0
	}
	return e.child.cmd.Process.Pid
}
