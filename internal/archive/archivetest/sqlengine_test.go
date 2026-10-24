package archivetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// sqlFixture is two projects' events over three days: the first two
// dumped and derived, the third an open log, with event and attribute
// rules on both projects.
func sqlFixture(t *testing.T) (*Files, archive.Rules) {
	t.Helper()
	f := New(t)
	var evs []store.Event
	n := 0
	user := map[string]string{"a1": "u1", "a3": "u3", "a4": "u1"}
	add := func(project int64, family store.Family, name, at, session, actor string, attrs map[string]string) {
		n++
		ts, err := time.Parse(time.RFC3339, at)
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, store.Event{ID: fmt.Sprintf("e%02d", n), ProjectID: project, Family: family, EventName: name,
			TS: ts, ReceivedAt: ts, SessionID: session, ActorID: actor, ActorKind: "install", InstallID: actor, UserID: user[actor],
			Path: "/" + name, Host: "example.test", Attributes: attrs})
	}
	for _, day := range []string{"2026-10-10", "2026-10-11"} {
		add(1, store.FamilyViews, "pageview", day+"T09:00:00Z", "s1-"+day, "a1", nil)
		add(1, store.FamilyProduct, "signup", day+"T09:05:00Z", "s1-"+day, "a1", map[string]string{"plan": "pro"})
		add(1, store.FamilyProduct, "x", day+"T10:00:00Z", "s2-"+day, "a2", map[string]string{"plan": "free", "tier": "t1"})
		add(2, store.FamilyProduct, "b", day+"T11:00:00Z", "s3-"+day, "a3", nil)
		add(2, store.FamilyProduct, "keep", day+"T11:30:00Z", "s3-"+day, "a3", map[string]string{"plan": "team"})
	}
	f.Write(t, evs...)
	f.Maintain(t, "2026-10-12")
	evs = nil
	add(1, store.FamilyProduct, "x", "2026-10-12T08:00:00Z", "s4", "a4", map[string]string{"plan": "pro"})
	add(2, store.FamilyViews, "pageview", "2026-10-12T08:30:00Z", "s5", "a5", nil)
	f.Write(t, evs...)
	rules := archive.Rules{
		Events: []store.EventRule{
			{ProjectID: 1, Family: store.FamilyProduct, Name: "x", Action: store.EventRuleRename, To: "y"},
			{ProjectID: 2, Family: store.FamilyProduct, Name: "b", Action: store.EventRuleBlock},
		},
		Attributes: []store.AttributeRule{{ProjectID: 1, Key: "plan", Action: store.EventRuleRename, To: "tier"}},
	}
	return f, rules
}

// direct answers req as today's callers do: the mode's wrap from
// enginesql, then Reader.QueryScoped over the directory's own listing.
func direct(ctx context.Context, f *Files, req sqlengine.Request, rules archive.Rules) (engine.Result, error) {
	var args []engine.Arg
	for _, n := range slices.Sorted(func(yield func(string) bool) {
		for k := range req.Args {
			if !yield(k) {
				return
			}
		}
	}) {
		args = append(args, engine.Arg{Name: n, Value: req.Args[n]})
	}
	q := archive.Scoped{Args: args, Confine: req.Confine}
	if req.Scope != nil {
		q.Scope, q.Project = true, *req.Scope
	}
	run := func(sql string, args []engine.Arg, maxRows int) (engine.Result, error) {
		r := q
		r.SQL, r.Args, r.MaxRows = sql, args, maxRows
		return f.Reader.QueryScoped(ctx, r, rules)
	}
	switch req.Mode {
	case sqlengine.ModeRows:
		return run(req.SQL, args, req.MaxRows)
	case sqlengine.ModeText:
		return run(enginesql.Text(req.SQL), args, req.MaxRows)
	case sqlengine.ModeShape:
		return run(enginesql.Shape(req.SQL), args, 0)
	}
	pg := pageOf(*req.Page)
	shape, err := run(enginesql.Shape(req.SQL), args, 0)
	if err != nil {
		return engine.Result{}, err
	}
	if req.Mode == sqlengine.ModePage {
		stmt, err := enginesql.Page(req.SQL, args, shape.Columns, pg, req.MaxRows)
		if err != nil {
			return engine.Result{}, err
		}
		return run(stmt.SQL, stmt.Args, 0)
	}
	total, matched, err := enginesql.Count(req.SQL, args, shape.Columns, pg)
	if err != nil {
		return engine.Result{}, err
	}
	out := engine.Result{Columns: []string{"total", "matched"}, Rows: [][]any{{json.Number("0"), json.Number("0")}}}
	for i, s := range []enginesql.Stmt{total, matched} {
		if s.SQL != "" {
			res, err := run(s.SQL, s.Args, 0)
			if err != nil {
				return engine.Result{}, err
			}
			out.Rows[0][i] = res.Rows[0][0]
		}
	}
	return out, nil
}

// withTables is req with the table spec of f's files: the scoped project
// only when req has a scope, every project otherwise.
func withTables(t *testing.T, f *Files, req sqlengine.Request, rules archive.Rules) sqlengine.Request {
	t.Helper()
	var projects []int64
	if req.Scope != nil {
		projects = []int64{*req.Scope}
	}
	spec, err := archive.TableSpec(f.Dir, projects, rules)
	if err != nil {
		t.Fatal(err)
	}
	req.Tables = spec
	return req
}

func scope(id int64) *int64 { return &id }

// TestSQLEngineAnswersAsTheReader: each mode, scoped and not, confined and
// not, answers through the socket what the Reader answers directly.
func TestSQLEngineAnswersAsTheReader(t *testing.T) {
	f, rules := sqlFixture(t)
	c := f.ServeSQL(t).Client(t)
	ctx := context.Background()
	productSQL := "SELECT event_name, attr_tier, CAST(day AS VARCHAR) AS day FROM product ORDER BY id"
	for name, req := range map[string]sqlengine.Request{
		"rows, every project": {Mode: sqlengine.ModeRows,
			SQL: "SELECT project_id, family, event_name, count(*) AS n FROM events GROUP BY ALL ORDER BY ALL"},
		"rows, scoped, confined, args": {Mode: sqlengine.ModeRows, Scope: scope(1), Confine: true,
			SQL:  "SELECT event_name, count(*) AS n FROM product WHERE day BETWEEN CAST($tw_from AS DATE) AND CAST($tw_to AS DATE) GROUP BY 1 ORDER BY 1",
			Args: map[string]string{"tw_from": "2026-10-11", "tw_to": "2026-10-12"}},
		"rows, cut short": {Mode: sqlengine.ModeRows, MaxRows: 2, SQL: "SELECT id FROM events ORDER BY id"},
		"text, sessions":  {Mode: sqlengine.ModeText, Scope: scope(1), Confine: true, SQL: "SELECT * FROM sessions ORDER BY ALL"},
		"text, measures and views": {Mode: sqlengine.ModeText, Confine: true,
			SQL: "SELECT (SELECT count(*) FROM measures) AS m, (SELECT count(*) FROM views) AS v, 1.5 AS x, NULL AS nothing"},
		"text, identities": {Mode: sqlengine.ModeText, Scope: scope(2), SQL: "SELECT * FROM identities ORDER BY ALL;"},
		"shape":            {Mode: sqlengine.ModeShape, Scope: scope(1), Confine: true, SQL: "SELECT * FROM aliases"},
		"rows, aliases":    {Mode: sqlengine.ModeRows, SQL: "SELECT * FROM aliases ORDER BY ALL -- a comment"},
		"page, filtered and sorted": {Mode: sqlengine.ModePage, Scope: scope(1), Confine: true, MaxRows: 100, SQL: productSQL,
			Page: &sqlengine.Page{Filters: []sqlengine.Filter{{Column: "event_name", Op: "in", Values: []string{"y", "signup"}}},
				Sort: &sqlengine.Sort{Column: "day", Desc: true}, Limit: 3, Offset: 1}},
		"page, distinct": {Mode: sqlengine.ModePage, Scope: scope(1), MaxRows: 100, SQL: productSQL,
			Page: &sqlengine.Page{Distinct: "attr_tier"}},
		"page, past the end": {Mode: sqlengine.ModePage, Scope: scope(1), MaxRows: 100, SQL: productSQL,
			Page: &sqlengine.Page{Offset: 50}},
		"count, past the end": {Mode: sqlengine.ModeCount, Scope: scope(1), Confine: true, MaxRows: 100, SQL: productSQL,
			Page: &sqlengine.Page{Filters: []sqlengine.Filter{{Column: "event_name", Op: "=", Values: []string{"y"}}}, Offset: 50}},
		"count, distinct past the end": {Mode: sqlengine.ModeCount, Scope: scope(1), MaxRows: 100, SQL: productSQL,
			Page: &sqlengine.Page{Distinct: "event_name", Offset: 50}},
		"count, offset 0": {Mode: sqlengine.ModeCount, MaxRows: 100, SQL: productSQL,
			Page: &sqlengine.Page{Filters: []sqlengine.Filter{{Column: "event_name", Op: "=", Values: []string{"none"}}}}},
	} {
		t.Run(name, func(t *testing.T) {
			want, err := direct(ctx, f, req, rules)
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.Query(ctx, withTables(t, f, req, rules))
			if err != nil {
				t.Fatal(err)
			}
			g, _ := json.Marshal(got)
			if want.Rows == nil {
				want.Rows = [][]any{}
			}
			w, _ := json.Marshal(sqlengine.Result{Columns: want.Columns, Rows: want.Rows, Truncated: want.Truncated})
			if string(g) != string(w) {
				t.Errorf("through the socket\n%s\ndirect\n%s", g, w)
			}
			if len(want.Rows) == 0 && req.Mode != sqlengine.ModeShape && !strings.Contains(name, "past the end") {
				t.Errorf("no rows: the case tests nothing")
			}
		})
	}
}

// TestSQLEngineRefusesAsTheReader: what the Reader or enginesql refuses
// comes back 400 with the kind and message today's callers tell apart,
// and a request the engine cannot run is refused before it runs.
func TestSQLEngineRefusesAsTheReader(t *testing.T) {
	f, rules := sqlFixture(t)
	s := f.ServeSQL(t)
	c := s.Client(t)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		req  sqlengine.Request
		kind sqlengine.Kind
	}{
		"confine": {sqlengine.Request{Mode: sqlengine.ModeRows, Confine: true, SQL: "SELECT * FROM read_parquet('x.parquet')"}, sqlengine.KindRefused},
		"confine, main.events": {sqlengine.Request{Mode: sqlengine.ModeText, Scope: scope(1), Confine: true,
			SQL: "SELECT count(*) FROM main.events"}, sqlengine.KindRefused},
		"sql":         {sqlengine.Request{Mode: sqlengine.ModeRows, SQL: "SELECT nope FROM events"}, sqlengine.KindSQL},
		"missing arg": {sqlengine.Request{Mode: sqlengine.ModeRows, SQL: "SELECT $tw_from"}, sqlengine.KindSQL},
		"a write":     {sqlengine.Request{Mode: sqlengine.ModeRows, SQL: "DROP TABLE events"}, sqlengine.KindSQL},
		"page, unknown column": {sqlengine.Request{Mode: sqlengine.ModePage, MaxRows: 10, SQL: "SELECT 1 AS a",
			Page: &sqlengine.Page{Sort: &sqlengine.Sort{Column: "b"}}}, sqlengine.KindRefused},
		"page, over the cap": {sqlengine.Request{Mode: sqlengine.ModePage, MaxRows: 10, SQL: "SELECT 1 AS a",
			Page: &sqlengine.Page{Limit: 11}}, sqlengine.KindRefused},
		"count, page's own parameter": {sqlengine.Request{Mode: sqlengine.ModeCount, SQL: "SELECT $tw_p1 AS a",
			Args: map[string]string{"tw_p1": "x"}, Page: &sqlengine.Page{}}, sqlengine.KindRefused},
	} {
		t.Run(name, func(t *testing.T) {
			_, derr := direct(ctx, f, tc.req, rules)
			if derr == nil {
				t.Fatal("the direct query did not fail")
			}
			_, err := c.Query(ctx, withTables(t, f, tc.req, rules))
			var e *sqlengine.Error
			if !errors.As(err, &e) || e.Status != http.StatusBadRequest || e.Kind != tc.kind {
				t.Fatalf("error = %v, want 400 %s", err, tc.kind)
			}
			want := derr.Error()
			var qe *engine.QueryError
			if errors.As(derr, &qe) {
				want = qe.Msg
			}
			if want = strings.TrimPrefix(want, enginesql.ErrRefused.Error()+": "); e.Message != want {
				t.Errorf("message = %q, want %q", e.Message, want)
			}
		})
	}

	spec, err := archive.TableSpec(f.Dir, nil, rules)
	if err != nil {
		t.Fatal(err)
	}
	escape := spec
	escape.Projects = slices.Clone(spec.Projects)
	escape.Projects[0].Logs = []string{"../../etc/passwd"}
	for name, req := range map[string]sqlengine.Request{
		"a path out of raw/": {Mode: sqlengine.ModeRows, SQL: "SELECT 1", Tables: escape},
		"another version":    {Mode: sqlengine.ModeRows, SQL: "SELECT 1", Tables: sqlengine.Tables{Version: 2}},
		"unknown mode":       {Mode: "pivot", SQL: "SELECT 1", Tables: spec},
		"page without page":  {Mode: sqlengine.ModePage, SQL: "SELECT 1", Tables: spec},
	} {
		_, err := c.Query(ctx, req)
		var e *sqlengine.Error
		if !errors.As(err, &e) || e.Status != http.StatusBadRequest || e.Kind != sqlengine.KindRefused {
			t.Errorf("%s: error = %v, want 400 refused", name, err)
		}
	}

	// A file the spec names that is not there fails at once: the list is
	// the caller's, so the reader does not retry it.
	missing := spec
	missing.Projects = slices.Clone(spec.Projects)
	missing.Projects[0].Parquet = append(slices.Clone(spec.Projects[0].Parquet), "1/2020-01.parquet")
	start := time.Now()
	_, err = c.Query(ctx, sqlengine.Request{Mode: sqlengine.ModeRows, SQL: "SELECT 1", Tables: missing})
	var e *sqlengine.Error
	if !errors.As(err, &e) || e.Status != http.StatusInternalServerError || e.Kind != sqlengine.KindInternal || !strings.Contains(e.Message, "2020-01.parquet") {
		t.Errorf("a missing file: error = %v, want 500 internal naming it", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("a missing file took %s: retried", took)
	}

	s.SetBusy(true)
	_, err = c.Query(ctx, sqlengine.Request{Mode: sqlengine.ModeRows, SQL: "SELECT 1", Tables: spec})
	if !errors.As(err, &e) || e.Status != http.StatusServiceUnavailable || e.Kind != sqlengine.KindBusy || e.RetryAfter != time.Second {
		t.Errorf("busy: error = %v, want 503 busy after 1s", err)
	}
	s.SetBusy(false)
	if _, err := c.Query(ctx, sqlengine.Request{Mode: sqlengine.ModeRows, SQL: "SELECT 1", Tables: spec}); err != nil {
		t.Errorf("no longer busy: %v", err)
	}

	h, err := c.Health(ctx)
	if err != nil || h != (sqlengine.Health{Protocol: sqlengine.Protocol, Engine: EngineName}) {
		t.Errorf("health = %+v, %v", h, err)
	}
	if got := len(s.Requests()); got < 10 {
		t.Errorf("requests recorded = %d", got)
	}
}

// slowSQL runs for minutes unless it is cancelled.
const slowSQL = "SELECT count(*) FROM range(100000000000) r(i) WHERE hash(i) % 7 = 3"

// TestSQLEngineCancels: a caller that cancels or runs past its deadline
// closes the connection, and the engine sees it and stops the query; the
// engine's own deadline (X-Timeout-Ms) ends a query the caller still
// waits on.
func TestSQLEngineCancels(t *testing.T) {
	f := New(t)
	s := f.ServeSQL(t)
	c := s.Client(t)
	spec, err := archive.TableSpec(f.Dir, nil, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	req := sqlengine.Request{Mode: sqlengine.ModeRows, SQL: slowSQL, Tables: spec}
	waitCancelled := func(n int) {
		t.Helper()
		for end := time.Now().Add(30 * time.Second); s.Cancelled() < n; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("the engine saw %d cancellations, want %d", s.Cancelled(), n)
			}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	if _, err := c.Query(ctx, req); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: error = %v, want context.Canceled", err)
	}
	waitCancelled(1)

	// The caller's deadline: the engine's own (X-Timeout-Ms, the same
	// time) is held back, so the caller always gives up first and the
	// engine sees it go, not its own 504 racing the caller's.
	s.SetTimeoutSlack(time.Minute)
	ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := c.Query(ctx, req); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("deadline: error = %v, want context.DeadlineExceeded", err)
	}
	waitCancelled(2)
	s.SetTimeoutSlack(0)

	// The header alone, from a caller with no deadline of its own.
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", s.Socket)
	}}}
	defer hc.CloseIdleConnections()
	body, _ := json.Marshal(req)
	hr, _ := http.NewRequest(http.MethodPost, "http://sqlengine/v1/query", strings.NewReader(string(body)))
	hr.Header.Set(sqlengine.TimeoutHeader, "300")
	start := time.Now()
	resp, err := hc.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var eb sqlengine.ErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&eb); err != nil || resp.StatusCode != http.StatusGatewayTimeout || eb.Error.Kind != sqlengine.KindTimeout {
		t.Errorf("X-Timeout-Ms: %d %+v, %v; want 504 timeout", resp.StatusCode, eb, err)
	}
	waitCancelled(3)
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("X-Timeout-Ms 300 took %s", took)
	}

	// The child serves the next query.
	if res, err := c.Query(context.Background(), sqlengine.Request{Mode: sqlengine.ModeRows, SQL: "SELECT 42 AS n", Tables: spec}); err != nil || fmt.Sprint(res.Rows) != "[[42]]" {
		t.Errorf("after the cancellations: %v, %v", res, err)
	}
}

// TestErrorKinds: the errors the Reader returns map to the kinds and
// statuses twillingate's callers tell apart today.
func TestErrorKinds(t *testing.T) {
	bg := context.Background()
	oom := &engine.QueryError{Msg: "Out of Memory Error: failed to allocate"}
	gone, cancel := context.WithCancel(bg)
	cancel()
	expired, cancel2 := context.WithDeadline(bg, time.Now().Add(-time.Second))
	defer cancel2()
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		err    error
		status int
		kind   sqlengine.Kind
		msg    string
	}{
		{"query error", bg, &engine.QueryError{Msg: "Binder Error: no column"}, 400, sqlengine.KindSQL, "Binder Error: no column"},
		{"refused", bg, fmt.Errorf("%w: only SELECT", enginesql.ErrRefused), 400, sqlengine.KindRefused, "only SELECT"},
		{"bad tables", bg, fmt.Errorf("%w: x", archive.ErrTables), 400, sqlengine.KindRefused, "archive: bad table spec: x"},
		{"out of memory", bg, oom, 500, sqlengine.KindOutOfMemory, oom.Msg},
		{"out of memory loading logs", bg, fmt.Errorf("%w: %w", archive.ErrLoadingLogs, oom), 500, sqlengine.KindOutOfMemoryLoadingLog, oom.Msg},
		{"loading logs, no memory issue", bg, fmt.Errorf("%w: %w", archive.ErrLoadingLogs, &engine.QueryError{Msg: "IO Error"}), 400, sqlengine.KindSQL, "IO Error"},
		{"child exited", bg, fmt.Errorf("%w: killed", engine.ErrChildExited), 500, sqlengine.KindExited, "engine: the child exited: killed"},
		{"spawn", bg, engine.ErrSpawn, 500, sqlengine.KindInternal, engine.ErrSpawn.Error()},
		{"deadline", expired, fmt.Errorf("engine: %w", context.DeadlineExceeded), 504, sqlengine.KindTimeout, "the query exceeded X-Timeout-Ms"},
		{"caller gone", gone, context.Canceled, 500, sqlengine.KindInternal, "context canceled"},
	} {
		kind, msg := errorOf(tc.ctx, tc.err)
		if kind.Status() != tc.status || kind != tc.kind || msg != tc.msg {
			t.Errorf("%s: %d %s %q, want %d %s %q", tc.name, kind.Status(), kind, msg, tc.status, tc.kind, tc.msg)
		}
	}
}
