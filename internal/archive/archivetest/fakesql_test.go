package archivetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// fixture is sqlengine's testdata file name, decoded with UseNumber.
func fixture(t *testing.T, name string) any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "sqlengine", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, b)
}

func decode(t *testing.T, b []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return v
}

// post sends body to s's /v1/query as it is, with header timeout when
// not "", and returns the status, the Retry-After and the body.
func post(t *testing.T, s *FakeSQL, body, timeout string) (int, string, []byte) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", s.Socket)
	}}}
	defer hc.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodPost, "http://fake/v1/query", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if timeout != "" {
		req.Header.Set(sqlengine.TimeoutHeader, timeout)
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Get("Retry-After"), b
}

func query(sql string, mode sqlengine.Mode) sqlengine.Request {
	return sqlengine.Request{SQL: sql, Mode: mode, Tables: sqlengine.Tables{Version: sqlengine.TablesVersion}}
}

// TestFakeSQLHealth: the health answer speaks this protocol.
func TestFakeSQLHealth(t *testing.T) {
	h, err := NewFakeSQL(t).Client(t).Health(context.Background())
	if err != nil || h.Protocol != sqlengine.Protocol || h.Engine != FakeName {
		t.Errorf("health = %+v, %v", h, err)
	}
}

// TestFakeSQLModeShapes: each helper answers the body its mode has in the
// protocol's fixtures, and the request reaches the log as it was sent.
func TestFakeSQLModeShapes(t *testing.T) {
	s := NewFakeSQL(t).
		On(ModeIs(sqlengine.ModeRows), Rows([]string{"name", "n", "day", "ratio", "id", "note"},
			[]any{"a", 3, "2026-10-24", "NaN", "6f1c0f0e-2f77-4a3b-9d0b-1f7c1a8f9a10", nil},
			[]any{"b", 1.5, "2026-10-23", "Infinity", "00000000-0000-0000-0000-000000000000", true}).Truncated()).
		On(ModeIs(sqlengine.ModePage), Page([]string{"event_name", "day"}, 2, 5, []string{"signup", "2026-10-24"}, []string{"y", "2026-10-23"})).
		On(ModeIs(sqlengine.ModeCount), Count(5, 0)).
		On(ModeIs(sqlengine.ModeShape), Shape("a", "b")).
		On(ModeIs(sqlengine.ModeText), Text([]string{"a"}, []string{"1"}, []string{""}))
	for _, c := range []struct {
		mode sqlengine.Mode
		want any
	}{
		{sqlengine.ModeRows, fixture(t, "response.json")},
		{sqlengine.ModePage, fixture(t, "response_page.json")},
		{sqlengine.ModeCount, fixture(t, "response_count.json")},
		{sqlengine.ModeShape, decode(t, []byte(`{"columns":["a","b"],"rows":[],"truncated":false}`))},
		{sqlengine.ModeText, decode(t, []byte(`{"columns":["a"],"rows":[["1"],[""]],"truncated":false}`))},
	} {
		body, _ := json.Marshal(query("SELECT 1", c.mode))
		status, _, b := post(t, s, string(body), "")
		if status != http.StatusOK || !reflect.DeepEqual(decode(t, b), c.want) {
			t.Errorf("%s: %d %s, want %v", c.mode, status, b, c.want)
		}
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "sqlengine", "testdata", "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want sqlengine.Request
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if status, _, b := post(t, s, string(b), ""); status != http.StatusOK {
		t.Fatalf("request.json: %d %s", status, b)
	}
	reqs := s.Requests()
	if got := reqs[len(reqs)-1]; !reflect.DeepEqual(got, want) {
		t.Errorf("logged %+v, want %+v", got, want)
	}
}

// TestFakeSQLErrorKinds: each kind answers its status and the error body
// the protocol's fixtures have, and the client reads it back.
func TestFakeSQLErrorKinds(t *testing.T) {
	ctx := context.Background()
	for _, k := range []sqlengine.Kind{sqlengine.KindRefused, sqlengine.KindSQL, sqlengine.KindOutOfMemory,
		sqlengine.KindOutOfMemoryLoadingLog, sqlengine.KindStale, sqlengine.KindBusy, sqlengine.KindTimeout,
		sqlengine.KindExited, sqlengine.KindInternal} {
		want := fixture(t, "error_"+string(k)+".json")
		msg := want.(map[string]any)["error"].(map[string]any)["message"].(string)
		s := NewFakeSQL(t).On(Any, Fail(k, msg))
		body, _ := json.Marshal(query("SELECT 1", sqlengine.ModeRows))
		status, _, b := post(t, s, string(body), "")
		if status != k.Status() || !reflect.DeepEqual(decode(t, b), want) {
			t.Errorf("%s: %d %s, want %d %v", k, status, b, k.Status(), want)
		}
		_, err := s.Client(t).Query(ctx, query("SELECT 1", sqlengine.ModeRows))
		var e *sqlengine.Error
		if !errors.As(err, &e) || e.Kind != k || e.Message != msg {
			t.Errorf("%s: client err = %v", k, err)
		}
	}
}

// TestFakeSQLAnswersInTurn: a script's answers go in turn, the last one
// repeating, and a later script is tried first; QueryFresh sends a stale
// answer's query again until the script answers.
func TestFakeSQLAnswersInTurn(t *testing.T) {
	ctx := context.Background()
	stale := Fail(sqlengine.KindStale, "a file in the table spec is gone")
	s := NewFakeSQL(t).
		On(Any, Shape("old")).
		On(SQLHas("events"), stale, stale, Text([]string{"n"}, []string{"3"})).
		On(And(SQLHas("events"), ModeIs(sqlengine.ModeShape)), Shape("n"))
	c := s.Client(t)
	specs := 0
	res, err := sqlengine.QueryFresh(ctx, c, query("SELECT count(*) AS n FROM events", sqlengine.ModeText), func() (sqlengine.Tables, error) {
		specs++
		return sqlengine.Tables{Version: sqlengine.TablesVersion}, nil
	})
	if err != nil || specs != sqlengine.StaleAttempts || !reflect.DeepEqual(res.Rows, [][]any{{"3"}}) {
		t.Errorf("QueryFresh = %+v, %v after %d specs, want 3 after %d", res, err, specs, sqlengine.StaleAttempts)
	}
	if res, err := c.Query(ctx, query("SELECT count(*) AS n FROM events", sqlengine.ModeText)); err != nil || res.Rows[0][0] != "3" {
		t.Errorf("the last answer does not repeat: %+v, %v", res, err)
	}
	if res, err := c.Query(ctx, query("SELECT n FROM events", sqlengine.ModeShape)); err != nil || res.Columns[0] != "n" {
		t.Errorf("the later script did not win: %+v, %v", res, err)
	}
	if res, err := c.Query(ctx, query("SELECT 1", sqlengine.ModeShape)); err != nil || res.Columns[0] != "old" {
		t.Errorf("the earlier script did not answer the rest: %+v, %v", res, err)
	}
	if n := len(s.Requests()); n != 6 {
		t.Errorf("logged %d requests, want 6", n)
	}
}

// TestFakeSQLDeadlineAndCancel: a hang answers timeout when X-Timeout-Ms
// passes, and counts a cancellation when the caller goes away first,
// with the slack keeping the engine's deadline back; busy answers 503
// with Retry-After.
func TestFakeSQLDeadlineAndCancel(t *testing.T) {
	s := NewFakeSQL(t).On(Any, Hang())
	body, _ := json.Marshal(query("SELECT 1", sqlengine.ModeRows))
	status, _, b := post(t, s, string(body), "20")
	if status != http.StatusGatewayTimeout || !strings.Contains(string(b), `"kind":"timeout"`) {
		t.Errorf("hang past the deadline = %d %s, want 504 timeout", status, b)
	}
	if status, _, _ := post(t, s, string(body), "0"); status != http.StatusBadRequest {
		t.Errorf("a timeout of 0 = %d, want 400", status)
	}

	s.SetTimeoutSlack(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.Client(t).Query(ctx, query("SELECT 1", sqlengine.ModeRows)); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the caller's own deadline", err)
	}
	for end := time.Now().Add(10 * time.Second); s.Cancelled() < 2; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("cancelled = %d, want 2", s.Cancelled())
		}
	}

	s.SetBusy(true)
	status, retry, b := post(t, s, string(body), "")
	if status != http.StatusServiceUnavailable || retry != "1" || !strings.Contains(string(b), `"kind":"busy"`) {
		t.Errorf("busy = %d %q %s", status, retry, b)
	}
}

// TestFakeSQLIgnoresUnknownFields: a request carrying fields version 1
// does not have is answered, as every engine must.
func TestFakeSQLIgnoresUnknownFields(t *testing.T) {
	s := NewFakeSQL(t).On(Any, Shape("a"))
	status, _, b := post(t, s, `{"sql":"SELECT 1 AS a","mode":"shape","tables":{"version":1,"projects":[],"rules":{"events":[],"attributes":[]}},"future":{"x":1}}`, "")
	if status != http.StatusOK {
		t.Errorf("unknown field = %d %s, want 200", status, b)
	}
}

// recorder is a testing.TB whose Errorf is kept rather than failing the
// test.
type recorder struct {
	testing.TB
	mu     sync.Mutex
	errors []string
}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// TestFakeSQLUnmatchedFailsLoudly: a request no script takes answers
// internal naming its SQL, and fails the test when it ends.
func TestFakeSQLUnmatchedFailsLoudly(t *testing.T) {
	r := &recorder{TB: t}
	t.Run("run", func(t *testing.T) {
		r.TB = t
		s := NewFakeSQL(r).On(SQLHas("SELECT known"), Shape("a"))
		_, err := s.Client(t).Query(context.Background(), query("SELECT unknown", sqlengine.ModeText))
		var e *sqlengine.Error
		if !errors.As(err, &e) || e.Kind != sqlengine.KindInternal || !strings.Contains(e.Message, "SELECT unknown") {
			t.Errorf("err = %v, want internal naming the SQL", err)
		}
	})
	if len(r.errors) != 1 || !strings.Contains(r.errors[0], "mode text: SELECT unknown") {
		t.Errorf("errors at the end = %q, want the unmatched request", r.errors)
	}
}

// TestFakeSQLLoad: a testdata file's exchanges answer the requests equal
// to them but for args and tables, results and errors in turn, and a
// test without a file of its own scripts nothing.
func TestFakeSQLLoad(t *testing.T) {
	dir := t.TempDir()
	file := `[
{"mode":"text","sql":"SELECT :x","scope":1,"confine":true,"max_rows":5,"answers":[{"kind":"stale","message":"gone"},{"columns":["x"],"rows":[["1"]],"truncated":true}]},
{"mode":"page","sql":"SELECT :x","page":{"filters":[],"sort":{"column":"x","desc":true},"distinct":"","limit":10,"offset":0},"scope":null,"confine":true,"max_rows":5,"answers":[{"hang":true}]}
]`
	if err := os.WriteFile(filepath.Join(dir, "TestFakeSQLLoad.json"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewFakeSQL(t).On(Any, Shape("other")).LoadTest(t, dir)
	c := s.Client(t)
	ctx := context.Background()
	one := int64(1)
	req := sqlengine.Request{SQL: "SELECT :x", Mode: sqlengine.ModeText, Scope: &one, Confine: true, MaxRows: 5, Args: map[string]string{"tw_x": "today"}}
	if _, err := c.Query(ctx, req); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("first = %v, want stale", err)
	}
	if res, err := c.Query(ctx, req); err != nil || !res.Truncated || res.Rows[0][0] != "1" {
		t.Errorf("second = %+v, %v", res, err)
	}
	req.MaxRows = 6
	if res, _ := c.Query(ctx, req); res.Columns[0] != "other" {
		t.Errorf("another max_rows matched the file: %+v", res)
	}
	page := sqlengine.Request{SQL: "SELECT :x", Mode: sqlengine.ModePage, Confine: true, MaxRows: 5,
		Page: &sqlengine.Page{Sort: &sqlengine.Sort{Column: "x", Desc: true}, Limit: 10}}
	tctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err := c.Query(tctx, page); err == nil {
		t.Error("the hang answered")
	}
	t.Run("none", func(t *testing.T) {
		if res, err := NewFakeSQL(t).LoadTest(t, dir).On(Any, Shape("x")).Client(t).Query(ctx, page); err != nil || res.Columns[0] != "x" {
			t.Errorf("a subtest without a file = %+v, %v", res, err)
		}
	})
}
