package api

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSSEWriter drives the writer through a real server and reads the
// body as it arrives: headers, each event flushed on its own, comments,
// a value that does not marshal refused without a byte written.
func TestSSEWriter(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := newSSE(w, r)
		if err != nil {
			t.Errorf("newSSE: %v", err)
			return
		}
		defer s.Close()
		if err := s.Event("first", map[string]int{"n": 1}); err != nil {
			t.Errorf("Event: %v", err)
		}
		<-release
		if err := s.Event("bad", func() {}); err == nil || errors.Is(err, errSSEClosed) {
			t.Errorf("Event(func) = %v, want a marshal error", err)
		}
		if err := s.Event("bad\nname", 1); err == nil || errors.Is(err, errSSEClosed) {
			t.Errorf("Event with a line break in its name = %v, want a refusal", err)
		}
		_ = s.Comment("note")
		_ = s.Event("done", struct{}{})
	}))
	defer srv.Close()

	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	for k, want := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no"} {
		if got := res.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	br := bufio.NewReader(res.Body)
	// The first event arrives while the handler still waits: it was flushed.
	first := readBlock(t, br)
	if first != "event: first\ndata: {\"n\":1}\n" {
		t.Errorf("first event = %q", first)
	}
	close(release)
	rest, _ := io.ReadAll(br)
	if got, want := string(rest), ": note\n\nevent: done\ndata: {}\n\n"; got != want {
		t.Errorf("rest = %q, want %q", got, want)
	}
}

// readBlock reads one event: its lines up to the blank line that ends it.
func readBlock(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	var b strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v (so far %q)", err, b.String())
		}
		if line == "\n" {
			return b.String()
		}
		b.WriteString(line)
	}
}

// TestSSEWriterKeepalive: comments go out on the interval while the
// handler is quiet, and stop with Close.
func TestSSEWriterKeepalive(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := newSSE(w, r)
		if err != nil {
			return
		}
		defer s.Close()
		s.Keepalive(10 * time.Millisecond)
		<-release
	}))
	defer srv.Close()
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	br := bufio.NewReader(res.Body)
	for range 2 {
		if got := readBlock(t, br); got != ": keepalive\n" {
			t.Fatalf("block = %q, want a keepalive", got)
		}
	}
	close(release)
	rest, _ := io.ReadAll(br)
	if strings.Trim(string(rest), ": keepalive\n") != "" {
		t.Errorf("after the keepalives: %q", rest)
	}
}

// TestSSEWriterClientGone: once the client disconnects, Done closes and
// writes answer errSSEClosed.
func TestSSEWriterClientGone(t *testing.T) {
	gone := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := newSSE(w, r)
		if err != nil {
			gone <- err
			return
		}
		defer s.Close()
		s.Keepalive(5 * time.Millisecond)
		select {
		case <-s.Done():
		case <-time.After(5 * time.Second):
			gone <- errors.New("Done never closed")
			return
		}
		gone <- s.Event("late", 1)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	res.Body.Close()
	if err := <-gone; !errors.Is(err, errSSEClosed) {
		t.Errorf("write after the client left = %v, want errSSEClosed", err)
	}
}
