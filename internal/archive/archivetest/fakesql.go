package archivetest

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// FakeName is what a FakeSQL's GET /v1/health reports.
const FakeName = "archivetest fake"

// FakeSQL is an external SQL engine for tests whose answers are scripted:
// it serves the sqlengine protocol on a Unix socket, answers each query
// with the first script whose match takes it (the latest On first), and
// records every request. A request no script takes answers internal,
// naming its mode and SQL, and fails the test when it ends, so a missing
// script fails loudly. It honours X-Timeout-Ms (plus the slack) and the
// caller going away while an answer hangs (Hang), and answers busy while
// SetBusy is on.
type FakeSQL struct {
	// Socket is the path of the socket it listens on: sqlengine.Dial it.
	Socket string

	srv       *http.Server
	busy      atomic.Bool
	slack     atomic.Int64 // added to X-Timeout-Ms, in nanoseconds
	cancelled atomic.Int64

	mu        sync.Mutex
	scripts   []*script
	requests  []sqlengine.Request
	unmatched []string
}

// Answer is what a script answers a request with: a result, an error of
// a kind, or a hang until the request's deadline or its caller goes.
type Answer struct {
	result  *sqlengine.Result
	kind    sqlengine.Kind
	message string
	hang    bool
}

// Match takes the requests a script answers.
type Match func(sqlengine.Request) bool

type script struct {
	match   Match
	answers []Answer
	served  int
	// from names the testdata file an exchange came from (Load): one
	// never asked fails the test, so a file does not outlive its test.
	from string
}

// NewFakeSQL serves a FakeSQL with no script until the test ends.
func NewFakeSQL(t testing.TB) *FakeSQL {
	t.Helper()
	s := &FakeSQL{Socket: socketPath(t)}
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
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.unmatched) > 0 {
			t.Errorf("archivetest: %d request(s) no script answered:\n%s", len(s.unmatched), strings.Join(s.unmatched, "\n"))
		}
		for _, sc := range s.scripts {
			if sc.from != "" && sc.served < len(sc.answers) {
				t.Errorf("archivetest: %s: an exchange was asked %d times, fewer than its %d answers", sc.from, sc.served, len(sc.answers))
			}
		}
	})
	return s
}

// On answers the requests match takes with answers, in turn: the first
// such request gets the first answer, and the last answer repeats. A
// later On is tried before an earlier one.
func (s *FakeSQL) On(match Match, answers ...Answer) *FakeSQL {
	if len(answers) == 0 {
		panic("archivetest: On without an answer")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scripts = append(s.scripts, &script{match: match, answers: answers})
	return s
}

// Client is a sqlengine client of s, closed with the test.
func (s *FakeSQL) Client(t testing.TB) *sqlengine.Client {
	c := sqlengine.Dial(s.Socket)
	t.Cleanup(c.Close)
	return c
}

// SetBusy makes every query answer 503 busy (no free slot) while on.
func (s *FakeSQL) SetBusy(on bool) { s.busy.Store(on) }

// SetTimeoutSlack adds d to each request's X-Timeout-Ms from now on, so
// a test can have the caller's own deadline pass first, every time: the
// engine then sees the caller go away rather than answering 504 itself.
func (s *FakeSQL) SetTimeoutSlack(d time.Duration) { s.slack.Store(int64(d)) }

// Cancelled is the number of queries that ended before their answer
// because the caller went away or the deadline (X-Timeout-Ms) passed.
func (s *FakeSQL) Cancelled() int { return int(s.cancelled.Load()) }

// Requests are the query requests received so far, in order.
func (s *FakeSQL) Requests() []sqlengine.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// Rows answers columns and rows as they are; a cell is nil, a bool, a
// string or a number.
func Rows(columns []string, rows ...[]any) Answer {
	if columns == nil {
		columns = []string{}
	}
	if rows == nil {
		rows = [][]any{}
	}
	return Answer{result: &sqlengine.Result{Columns: columns, Rows: rows}}
}

// Text answers columns and rows of text, as modes text and page do.
func Text(columns []string, rows ...[]string) Answer {
	out := make([][]any, len(rows))
	for i, r := range rows {
		out[i] = make([]any, len(r))
		for j, c := range r {
			out[i][j] = c
		}
	}
	return Rows(columns, out...)
}

// Shape answers the columns and no row, as mode shape does.
func Shape(columns ...string) Answer { return Rows(columns) }

// Page answers a page's rows of text with its two counts appended to
// each, as mode page does: columns are the query's, without the counts.
func Page(columns []string, matched, total int, rows ...[]string) Answer {
	cols := append(slices.Clone(columns), "__tw_matched", "__tw_total")
	out := make([][]any, len(rows))
	for i, r := range rows {
		for _, c := range r {
			out[i] = append(out[i], c)
		}
		out[i] = append(out[i], json.Number(strconv.Itoa(matched)), json.Number(strconv.Itoa(total)))
	}
	return Rows(cols, out...)
}

// Count answers the counts of a page with no row, as mode count does.
func Count(total, matched int) Answer {
	return Rows([]string{"total", "matched"}, []any{json.Number(strconv.Itoa(total)), json.Number(strconv.Itoa(matched))})
}

// Fail answers an error of kind with message.
func Fail(kind sqlengine.Kind, message string) Answer { return Answer{kind: kind, message: message} }

// Hang answers nothing until the request's deadline passes, timeout, or
// its caller goes away.
func Hang() Answer { return Answer{hang: true} }

// Truncated is a with its result marked truncated.
func (a Answer) Truncated() Answer {
	if a.result != nil {
		r := *a.result
		r.Truncated = true
		a.result = &r
	}
	return a
}

// Any takes every request.
func Any(sqlengine.Request) bool { return true }

// ModeIs takes the requests of mode m.
func ModeIs(m sqlengine.Mode) Match { return func(r sqlengine.Request) bool { return r.Mode == m } }

// SQLHas takes the requests whose SQL contains sub.
func SQLHas(sub string) Match {
	return func(r sqlengine.Request) bool { return strings.Contains(r.SQL, sub) }
}

// And takes the requests every one of ms takes.
func And(ms ...Match) Match {
	return func(r sqlengine.Request) bool {
		for _, m := range ms {
			if !m(r) {
				return false
			}
		}
		return true
	}
}

// Exchange is one scripted answer in a testdata file (Load): the request
// it takes, matched on everything but its args and tables, and the
// answers in turn.
type Exchange struct {
	Mode    sqlengine.Mode   `json:"mode"`
	SQL     string           `json:"sql"`
	Page    *sqlengine.Page  `json:"page,omitempty"`
	Scope   *int64           `json:"scope"`
	Confine bool             `json:"confine"`
	MaxRows int              `json:"max_rows"`
	Answers []ExchangeAnswer `json:"answers"`
}

// ExchangeAnswer is a result, with Kind set an error, or with Hang a
// hang (Hang).
type ExchangeAnswer struct {
	Hang      bool           `json:"hang,omitempty"`
	Columns   []string       `json:"columns,omitempty"`
	Rows      [][]any        `json:"rows,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
	Kind      sqlengine.Kind `json:"kind,omitempty"`
	Message   string         `json:"message,omitempty"`
}

// Load scripts the exchanges in the JSON file at path (a list of
// Exchange): each takes the requests equal to it but for args and tables,
// and fails the test when it ends unless every one of its answers was
// given.
// The args carry dates relative to today, so a test that cares about them
// reads Requests.
func (s *FakeSQL) Load(t testing.TB, path string) *FakeSQL {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var xs []Exchange
	if err := dec.Decode(&xs); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	for _, x := range xs {
		answers := make([]Answer, len(x.Answers))
		for i, a := range x.Answers {
			if a.Hang {
				answers[i] = Hang()
				continue
			}
			if a.Kind != "" {
				answers[i] = Fail(a.Kind, a.Message)
				continue
			}
			answers[i] = Rows(a.Columns, a.Rows...)
			if a.Truncated {
				answers[i] = answers[i].Truncated()
			}
		}
		s.On(x.match(), answers...)
		s.mu.Lock()
		s.scripts[len(s.scripts)-1].from = fmt.Sprintf("%s: mode %s: %s", path, x.Mode, x.SQL)
		s.mu.Unlock()
	}
	return s
}

// LoadTest scripts the exchanges of t's own file under dir, when there is
// one: its name is t.Name() with "/" as "." and every other character
// outside [A-Za-z0-9_.-] as "_", then ".json".
func (s *FakeSQL) LoadTest(t testing.TB, dir string) *FakeSQL {
	t.Helper()
	name := strings.Map(func(r rune) rune {
		switch {
		case r == '/':
			return '.'
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			return r
		}
		return '_'
	}, t.Name())
	path := filepath.Join(dir, name+".json")
	if _, err := os.Stat(path); err != nil {
		return s
	}
	return s.Load(t, path)
}

func (x Exchange) match() Match {
	page := pageKey(x.Page)
	return func(r sqlengine.Request) bool {
		return r.Mode == x.Mode && r.SQL == x.SQL && r.Confine == x.Confine && r.MaxRows == x.MaxRows &&
			((r.Scope == nil) == (x.Scope == nil)) && (r.Scope == nil || *r.Scope == *x.Scope) &&
			pageKey(r.Page) == page
	}
}

// pageKey is p in a form two equal pages share, nil and empty lists alike.
func pageKey(p *sqlengine.Page) string {
	if p == nil {
		return ""
	}
	q := *p
	if len(q.Filters) == 0 {
		q.Filters = nil
	}
	for i := range q.Filters {
		if len(q.Filters[i].Values) == 0 {
			q.Filters[i].Values = nil
		}
	}
	b, _ := json.Marshal(q)
	return string(b)
}

func (s *FakeSQL) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, sqlengine.Health{Protocol: sqlengine.Protocol, Engine: FakeName})
}

func (s *FakeSQL) query(w http.ResponseWriter, hr *http.Request) {
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
	a, ok := s.answer(req)
	switch {
	case !ok:
		writeError(w, sqlengine.KindInternal, fmt.Sprintf("archivetest: no script answers mode %s: %s", req.Mode, req.SQL))
	case a.hang:
		<-ctx.Done()
		s.cancelled.Add(1)
		if hr.Context().Err() != nil {
			writeError(w, sqlengine.KindInternal, ctx.Err().Error()) // nobody reads it
			return
		}
		writeError(w, sqlengine.KindTimeout, "the query exceeded "+sqlengine.TimeoutHeader)
	case a.kind != "":
		writeError(w, a.kind, a.message)
	default:
		writeJSON(w, http.StatusOK, a.result)
	}
}

// answer is the answer of the first script that takes req, latest first.
func (s *FakeSQL) answer(req sqlengine.Request) (Answer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.scripts) - 1; i >= 0; i-- {
		sc := s.scripts[i]
		if !sc.match(req) {
			continue
		}
		a := sc.answers[min(sc.served, len(sc.answers)-1)]
		sc.served++
		return a, true
	}
	s.unmatched = append(s.unmatched, fmt.Sprintf("  mode %s: %s", req.Mode, req.SQL))
	return Answer{}, false
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

func writeError(w http.ResponseWriter, kind sqlengine.Kind, msg string) {
	writeJSON(w, kind.Status(), sqlengine.ErrorBody{Error: sqlengine.ErrorDetail{Kind: kind, Message: msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
