package sqlengine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// serve starts h on a Unix socket in a directory under ~/.cache (a path
// short enough for sun_path) and returns a client for it.
func serve(t *testing.T, h http.Handler) *Client {
	t.Helper()
	base := filepath.Join(os.Getenv("HOME"), ".cache")
	dir, err := os.MkdirTemp(base, "sqlengine-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "e.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	c := Dial(sock)
	t.Cleanup(func() { c.Close(); srv.Close() })
	return c
}

func TestHealth(t *testing.T) {
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/health" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"protocol":1,"engine":"fake 0.1"}`))
	}))
	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h.Protocol != Protocol || h.Engine != "fake 0.1" {
		t.Errorf("health = %+v", h)
	}
}

func TestHealthError(t *testing.T) {
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	_, err := c.Health(context.Background())
	var e *Error
	if !errors.As(err, &e) || e.Status != 500 || e.Kind != KindInternal || e.Message != "nope" {
		t.Errorf("err = %v", err)
	}
}

func TestQuerySuccess(t *testing.T) {
	want := fixture(t, "request.json")
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/query" || r.Header.Get("Content-Type") != "application/json" {
			http.NotFound(w, r)
			return
		}
		var got json.RawMessage
		json.NewDecoder(r.Body).Decode(&got)
		if string(got) != string(want) {
			http.Error(w, "body differs: "+string(got), http.StatusBadRequest)
			return
		}
		if r.Header.Get(TimeoutHeader) != "" {
			http.Error(w, "unexpected timeout header", http.StatusBadRequest)
			return
		}
		w.Write(fixture(t, "response.json"))
	}))
	var req Request
	if err := json.Unmarshal(want, &req); err != nil {
		t.Fatal(err)
	}
	res, err := c.Query(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Columns) != 6 || len(res.Rows) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if n, ok := res.Rows[0][1].(json.Number); !ok || n.String() != "3" {
		t.Errorf("number cell = %#v", res.Rows[0][1])
	}
	if res.Rows[0][3] != "NaN" || res.Rows[1][3] != "Infinity" || res.Rows[0][5] != nil || res.Rows[1][5] != true {
		t.Errorf("cells = %#v %#v", res.Rows[0], res.Rows[1])
	}
}

func TestQueryErrors(t *testing.T) {
	cases := []struct {
		kind   Kind
		status int
	}{
		{KindRefused, 400}, {KindSQL, 400}, {KindStale, 409},
		{KindOutOfMemory, 500}, {KindOutOfMemoryLoadingLog, 500}, {KindInternal, 500},
		{KindBusy, 503}, {KindTimeout, 504}, {KindExited, 500},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			body := fixture(t, "error_"+string(tc.kind)+".json")
			c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status == 503 {
					w.Header().Set("Retry-After", "7")
				}
				w.WriteHeader(tc.status)
				w.Write(body)
			}))
			_, err := c.Query(context.Background(), Request{SQL: "x"})
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v", err)
			}
			if e.Status != tc.status || e.Kind != tc.kind || e.Message == "" || tc.kind.Status() != tc.status {
				t.Errorf("error = %+v", e)
			}
			wantRetry := time.Duration(0)
			if tc.status == 503 {
				wantRetry = 7 * time.Second
			}
			if e.RetryAfter != wantRetry {
				t.Errorf("RetryAfter = %v, want %v", e.RetryAfter, wantRetry)
			}
			if errors.Is(err, ErrUnavailable) {
				t.Error("an engine's answer is not ErrUnavailable")
			}
		})
	}
}

func TestRetryAfterUnparseable(t *testing.T) {
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "soon")
		w.WriteHeader(503)
		w.Write(fixture(t, "error_busy.json"))
	}))
	_, err := c.Query(context.Background(), Request{})
	var e *Error
	if !errors.As(err, &e) || e.RetryAfter != 0 || e.Kind != KindBusy {
		t.Errorf("err = %v", err)
	}
}

func TestErrorWithoutJSONBody(t *testing.T) {
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", 502)
	}))
	_, err := c.Query(context.Background(), Request{})
	var e *Error
	if !errors.As(err, &e) || e.Status != 502 || e.Kind != KindInternal || e.Message != "bad gateway" {
		t.Errorf("err = %v", err)
	}
	if e.Error() == "" {
		t.Error("empty Error string")
	}
}

func TestMalformedSuccessBody(t *testing.T) {
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	if _, err := c.Query(context.Background(), Request{}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("query err = %v", err)
	}
	if _, err := c.Health(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("health err = %v", err)
	}
}

func TestDeadlineHeader(t *testing.T) {
	got := make(chan string, 1)
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get(TimeoutHeader)
		w.Write([]byte(`{"columns":[],"rows":[],"truncated":false}`))
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.Query(ctx, Request{}); err != nil {
		t.Fatal(err)
	}
	ms, err := strconv.Atoi(<-got)
	if err != nil || ms < 25000 || ms > 30001 {
		t.Errorf("%s = %d, %v; want about 30000", TimeoutHeader, ms, err)
	}
}

func TestCancellationReachesServer(t *testing.T) {
	started := make(chan struct{})
	ended := make(chan error, 1)
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // the server notices a closed connection once the body is read
		close(started)
		<-r.Context().Done()
		ended <- r.Context().Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := c.Query(ctx, Request{}); errc <- err }()
	<-started
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnavailable) {
		t.Errorf("client err = %v", err)
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the server never saw the request end")
	}
}

func TestDeadlineExceeded(t *testing.T) {
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Query(ctx, Request{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}

func TestUnreachableSocket(t *testing.T) {
	dir, err := os.MkdirTemp(filepath.Join(os.Getenv("HOME"), ".cache"), "sqlengine-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	c := Dial(filepath.Join(dir, "missing.sock"))
	defer c.Close()
	if _, err := c.Health(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("health err = %v", err)
	}
	if _, err := c.Query(context.Background(), Request{}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("query err = %v", err)
	}
}

func TestConnectionDroppedMidFlight(t *testing.T) {
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	if _, err := c.Query(context.Background(), Request{}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

// TestTimeoutWithoutJSONBody: a 504 whose body names no kind is a timeout.
func TestTimeoutWithoutJSONBody(t *testing.T) {
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
	}))
	_, err := c.Query(context.Background(), Request{})
	var e *Error
	if !errors.As(err, &e) || e.Status != 504 || e.Kind != KindTimeout || e.Message != "gateway timeout" {
		t.Errorf("err = %v", err)
	}
}
