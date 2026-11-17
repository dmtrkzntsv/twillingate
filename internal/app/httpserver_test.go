package app

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// serveWith runs newHTTPServer over h on a loopback port, with the read
// timeout shrunk to d, and returns its address.
func serveWith(t *testing.T, d time.Duration, h http.Handler) string {
	t.Helper()
	old := readTimeout
	readTimeout = d
	t.Cleanup(func() { readTimeout = old })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newHTTPServer(ln.Addr().String(), h)
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(10 * time.Second))
	return c
}

// A client that sends its headers and then trickles (here: stops) the body
// does not hold the handler: the read fails once the request's time is up.
func TestSlowBodyIsCutOff(t *testing.T) {
	type result struct {
		err     error
		elapsed time.Duration
	}
	got := make(chan result, 1)
	addr := serveWith(t, 200*time.Millisecond, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		_, err := io.ReadAll(r.Body)
		got <- result{err, time.Since(start)}
		http.Error(w, "body", http.StatusBadRequest)
	}))
	c := dial(t, addr)
	fmt.Fprint(c, "POST /ingest/events HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\npartial")
	select {
	case r := <-got:
		if r.err == nil {
			t.Fatal("the stalled body was read without error")
		}
		if r.elapsed > 5*time.Second {
			t.Errorf("the read failed after %v, want about the read timeout", r.elapsed)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("the handler is still waiting for the body")
	}
}

// A body that arrives in time is read whole, and the deadline it was read
// under does not outlive it: a response that streams after the body (the
// MCP endpoint answers a POST with server-sent events) keeps going past
// the read timeout, its context alive. So does a stream with no body.
func TestStreamsOutliveTheReadTimeout(t *testing.T) {
	const timeout = 150 * time.Millisecond
	addr := serveWith(t, timeout, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: got %q\n\n", body)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return // cut: the client never sees the second event
		case <-time.After(4 * timeout):
		}
		fmt.Fprint(w, "data: still here\n\n")
	}))
	for name, req := range map[string]string{
		"POST with a body": "POST /mcp HTTP/1.1\r\nHost: x\r\nContent-Length: 5\r\n\r\nhello",
		"chunked POST":     "POST /mcp HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n",
		"GET":              "GET /api/stream HTTP/1.1\r\nHost: x\r\n\r\n",
	} {
		c := dial(t, addr)
		fmt.Fprint(c, req)
		res, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != http.StatusOK || !strings.Contains(string(b), "still here") {
			t.Errorf("%s: status %d, err %v, body %q: the stream was cut", name, res.StatusCode, err, b)
		}
		if name != "GET" && !strings.Contains(string(b), `got "hello"`) {
			t.Errorf("%s: body %q", name, b)
		}
	}
}

// The server bounds what else a slow client can hold open.
func TestServerLimits(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.IdleTimeout <= 0 || srv.MaxHeaderBytes <= 0 {
		t.Errorf("ReadHeaderTimeout %v, ReadTimeout %v, IdleTimeout %v, MaxHeaderBytes %d: all must be set",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.IdleTimeout, srv.MaxHeaderBytes)
	}
	// It bounds the whole response, so it would cut the event streams.
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout %v: must stay unset", srv.WriteTimeout)
	}
}
