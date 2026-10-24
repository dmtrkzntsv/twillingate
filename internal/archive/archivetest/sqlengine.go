package archivetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// EngineName is what a SQLEngine's GET /v1/health reports.
const EngineName = "archivetest duckdb"

// SQLEngine is an external SQL engine for tests: it serves the sqlengine
// protocol on a Unix socket by running today's archive.Reader and
// enginesql wraps on the in-tree engine, over the files each request's
// table spec names (archive.ResolveTables) under its directory. Its
// answers are the ones a direct Reader.QueryScoped gives, so a test of an
// SQL path can run through the protocol and expect what it did before.
type SQLEngine struct {
	// Socket is the path of the socket it listens on: sqlengine.Dial it.
	Socket string

	dir    string
	reader *archive.Reader
	srv    *http.Server

	busy      atomic.Bool
	slack     atomic.Int64 // added to X-Timeout-Ms, in nanoseconds
	cancelled atomic.Int64
	mu        sync.Mutex
	requests  []sqlengine.Request
}

// ServeSQL serves the files under dir (a raw/ directory) on eng, which
// must be allowed to read dir, until the test ends.
func ServeSQL(t testing.TB, dir string, eng archive.Engine) *SQLEngine {
	t.Helper()
	r, err := archive.NewReader(dir, eng)
	if err != nil {
		t.Fatal(err)
	}
	s := &SQLEngine{Socket: socketPath(t), dir: dir, reader: r}
	ln, err := net.Listen("unix", s.Socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("POST /v1/query", s.query)
	s.srv = &http.Server{Handler: mux}
	done := make(chan struct{})
	go func() { defer close(done); s.srv.Serve(ln) }()
	t.Cleanup(func() {
		s.srv.Close()
		<-done
	})
	return s
}

// maxSocketPath is the longest socket path socketPath gives: sun_path
// holds 108 bytes on Linux and 104 on the BSDs, the terminating NUL
// among them.
const maxSocketPath = 103

// socketPath is a path for the test's socket under t.TempDir, or, when
// the test's name makes that too long for sun_path, under a short
// directory of the user's cache directory, removed with the test.
func socketPath(t testing.TB) string {
	t.Helper()
	if p := filepath.Join(t.TempDir(), "e.sock"); len(p) <= maxSocketPath {
		return p
	}
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "sqle-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p := filepath.Join(dir, "e.sock")
	if len(p) > maxSocketPath {
		t.Fatalf("no socket path short enough: %s", p)
	}
	return p
}

// ServeSQL serves f's files on f's engine until the test ends.
func (f *Files) ServeSQL(t testing.TB) *SQLEngine { return ServeSQL(t, f.Dir, f.Eng) }

// Client is a sqlengine client of s, closed with the test.
func (s *SQLEngine) Client(t testing.TB) *sqlengine.Client {
	c := sqlengine.Dial(s.Socket)
	t.Cleanup(c.Close)
	return c
}

// SetBusy makes every query answer 503 busy (no free slot) while on.
func (s *SQLEngine) SetBusy(on bool) { s.busy.Store(on) }

// SetTimeoutSlack adds d to each request's X-Timeout-Ms from now on, so
// a test can have the caller's own deadline pass first, every time: the
// engine then sees the caller go away rather than answering 504 itself.
func (s *SQLEngine) SetTimeoutSlack(d time.Duration) { s.slack.Store(int64(d)) }

// Cancelled is the number of queries that ended before their answer
// because the caller went away (closed the connection) or the deadline
// (X-Timeout-Ms) passed.
func (s *SQLEngine) Cancelled() int { return int(s.cancelled.Load()) }

// Requests are the query requests received so far, in order.
func (s *SQLEngine) Requests() []sqlengine.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

func (s *SQLEngine) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, sqlengine.Health{Protocol: sqlengine.Protocol, Engine: EngineName})
}

func (s *SQLEngine) query(w http.ResponseWriter, hr *http.Request) {
	ctx := hr.Context()
	// Unknown fields are ignored, as every engine must: version 1 grows
	// by adding fields.
	var req sqlengine.Request
	if err := json.NewDecoder(hr.Body).Decode(&req); err != nil {
		writeError(w, sqlengine.KindRefused, "the request body: "+err.Error())
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if v := hr.Header.Get(sqlengine.TimeoutHeader); v != "" {
		ms, err := strconv.ParseInt(v, 10, 64)
		if err != nil || ms < 1 {
			writeError(w, sqlengine.KindRefused, sqlengine.TimeoutHeader+" must be a positive number of milliseconds")
			return
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond+time.Duration(s.slack.Load()))
		defer cancel()
	}
	if s.busy.Load() {
		w.Header().Set("Retry-After", "1")
		writeError(w, sqlengine.KindBusy, "no free slot; retry later")
		return
	}
	res, err := s.answer(ctx, req)
	if ctx.Err() != nil {
		s.cancelled.Add(1)
	}
	if err != nil {
		kind, msg := errorOf(ctx, err)
		writeError(w, kind, msg)
		return
	}
	out := sqlengine.Result{Columns: res.Columns, Rows: res.Rows, Truncated: res.Truncated}
	if out.Columns == nil {
		out.Columns = []string{}
	}
	if out.Rows == nil {
		out.Rows = [][]any{}
	}
	writeJSON(w, http.StatusOK, out)
}

// errRequest is a request the engine cannot run as sent.
var errRequest = errors.New("archivetest: bad request")

// answer answers req as the callers of the Reader did before the protocol:
// the mode's wrap from enginesql around the SQL, then QueryFiles over the
// spec's files with req's scope, confine, args and row cap.
func (s *SQLEngine) answer(ctx context.Context, req sqlengine.Request) (engine.Result, error) {
	files, rules, err := archive.ResolveTables(s.dir, req.Tables)
	if err != nil {
		return engine.Result{}, err
	}
	names := make([]string, 0, len(req.Args))
	for n := range req.Args {
		names = append(names, n)
	}
	slices.Sort(names)
	args := make([]engine.Arg, len(names))
	for i, n := range names {
		args[i] = engine.Arg{Name: n, Value: req.Args[n]}
	}
	q := archive.Scoped{SQL: req.SQL, Args: args, MaxRows: req.MaxRows, Confine: req.Confine}
	if req.Scope != nil {
		q.Scope, q.Project = true, *req.Scope
	}
	do := func(sql string, args []engine.Arg, maxRows int) (engine.Result, error) {
		r := q
		r.SQL, r.Args, r.MaxRows = sql, args, maxRows
		return s.reader.QueryFiles(ctx, r, files, rules)
	}
	switch req.Mode {
	case sqlengine.ModeRows:
		return do(req.SQL, args, req.MaxRows)
	case sqlengine.ModeText:
		return do(enginesql.Text(req.SQL), args, req.MaxRows)
	case sqlengine.ModeShape:
		return do(enginesql.Shape(req.SQL), args, 0)
	case sqlengine.ModePage, sqlengine.ModeCount:
	default:
		return engine.Result{}, fmt.Errorf("%w: unknown mode %q", errRequest, req.Mode)
	}
	if req.Page == nil {
		return engine.Result{}, fmt.Errorf("%w: mode %s needs page", errRequest, req.Mode)
	}
	pg := pageOf(*req.Page)
	shape, err := do(enginesql.Shape(req.SQL), args, 0)
	if err != nil {
		return engine.Result{}, err
	}
	if req.Mode == sqlengine.ModePage {
		stmt, err := enginesql.Page(req.SQL, args, shape.Columns, pg, req.MaxRows)
		if err != nil {
			return engine.Result{}, err
		}
		return do(stmt.SQL, stmt.Args, 0)
	}
	total, matched, err := enginesql.Count(req.SQL, args, shape.Columns, pg)
	if err != nil {
		return engine.Result{}, err
	}
	out := engine.Result{Columns: []string{"total", "matched"}, Rows: [][]any{{json.Number("0"), json.Number("0")}}}
	for i, stmt := range []enginesql.Stmt{total, matched} {
		if stmt.SQL == "" {
			continue // offset 0: an empty page means nothing matched
		}
		res, err := do(stmt.SQL, stmt.Args, 0)
		if err != nil {
			return engine.Result{}, err
		}
		if len(res.Rows) != 1 || len(res.Rows[0]) != 1 {
			return engine.Result{}, errors.New("archivetest: a page count returned no single cell")
		}
		out.Rows[0][i] = res.Rows[0][0]
	}
	return out, nil
}

// pageOf is the protocol's page as enginesql takes it.
func pageOf(p sqlengine.Page) readsql.Page {
	out := readsql.Page{Distinct: p.Distinct, Limit: p.Limit, Offset: p.Offset}
	for _, f := range p.Filters {
		out.Filters = append(out.Filters, readsql.Filter{Column: f.Column, Op: f.Op, Values: f.Values})
	}
	if p.Sort != nil {
		out.Sort = &readsql.Sort{Column: p.Sort.Column, Desc: p.Sort.Desc}
	}
	return out
}

// errorOf is the kind and message an error answers (its status is
// Kind.Status), in the order reporting's filesErr and api's filesError
// tell them apart today:
//   - the deadline (X-Timeout-Ms) passed: timeout;
//   - the caller went away: internal (nobody reads the answer);
//   - a file the spec names that is gone, before or while it is read
//     (archive.Vanished): stale, so the caller sends a fresh spec;
//   - running out of memory loading the open logs:
//     out_of_memory_loading_logs; running out of memory otherwise:
//     out_of_memory;
//   - a refusal (enginesql.ErrRefused: the parameter scanner, Confine, a
//     page twillingate cannot build), a table spec ResolveTables refuses
//     and a request the engine cannot run: refused, the message without
//     the sentinel's "refused: ";
//   - SQL DuckDB refused (engine.QueryError): sql, DuckDB's message as it
//     is;
//   - a child that died with the query in flight: exited;
//   - anything else, a child that did not start among it: internal.
func errorOf(ctx context.Context, err error) (sqlengine.Kind, string) {
	var qe *engine.QueryError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) && errors.Is(err, context.DeadlineExceeded):
		return sqlengine.KindTimeout, "the query exceeded " + sqlengine.TimeoutHeader
	case ctx.Err() != nil && errors.Is(err, context.Canceled):
		return sqlengine.KindInternal, err.Error()
	case archive.Vanished(err):
		return sqlengine.KindStale, "a file in the table spec is gone: " + err.Error()
	case errors.Is(err, archive.ErrLoadingLogs) && errors.As(err, &qe) && enginesql.IsOutOfMemory(qe.Msg):
		return sqlengine.KindOutOfMemoryLoadingLog, qe.Msg
	case errors.As(err, &qe) && enginesql.IsOutOfMemory(qe.Msg):
		return sqlengine.KindOutOfMemory, qe.Msg
	case errors.Is(err, enginesql.ErrRefused):
		return sqlengine.KindRefused, strings.TrimPrefix(err.Error(), enginesql.ErrRefused.Error()+": ")
	case errors.Is(err, archive.ErrTables), errors.Is(err, errRequest):
		return sqlengine.KindRefused, err.Error()
	case errors.As(err, &qe):
		return sqlengine.KindSQL, qe.Msg
	case errors.Is(err, engine.ErrChildExited):
		return sqlengine.KindExited, err.Error()
	}
	return sqlengine.KindInternal, err.Error()
}

func writeError(w http.ResponseWriter, kind sqlengine.Kind, msg string) {
	writeJSON(w, kind.Status(), sqlengine.ErrorBody{Error: sqlengine.ErrorDetail{Kind: kind, Message: msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
