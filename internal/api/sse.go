package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// sseKeepalive is how often a Server-Sent Events stream sends a keepalive
// comment, so a proxy or a browser does not take a quiet stream for a dead
// connection.
const sseKeepalive = 15 * time.Second

// errSSEClosed is what a write answers once the stream is over: the client
// went away, or an earlier write failed.
var errSSEClosed = errors.New("api: event stream closed")

// sseWriter writes one Server-Sent Events response: named events with JSON
// data, each flushed as it is written, and keepalive comments. Writes are
// serialised, so the keepalive loop and the handler may both write. It
// knows nothing of what the events carry.
type sseWriter struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
}

// newSSE starts an event stream on w: the headers (text/event-stream, no
// caching, no proxy buffering), a 200 flushed at once, and no write
// deadline, since the stream lasts as long as its handler wants. It fails
// when the response cannot be flushed.
func newSSE(w http.ResponseWriter, r *http.Request) (*sseWriter, error) {
	rc := http.NewResponseController(w)
	// A server write timeout would cut a long stream; it ends when its
	// handler is done or its client leaves instead.
	_ = rc.SetWriteDeadline(time.Time{})
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-cache")
	hd.Set("X-Accel-Buffering", "no") // nginx: do not buffer this response
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return nil, fmt.Errorf("api: event stream: %w", err)
	}
	ctx, cancel := context.WithCancel(r.Context())
	return &sseWriter{w: w, rc: rc, ctx: ctx, cancel: cancel}, nil
}

// Done is closed once the stream is over: the client went away, a write
// failed, or Close was called.
func (s *sseWriter) Done() <-chan struct{} { return s.ctx.Done() }

// Close ends the stream's keepalive loop; nothing more is written.
func (s *sseWriter) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
}

// Event writes one event, `event: name` and `data: ` data as JSON, and
// flushes it. A value that does not marshal is refused before anything is
// written, so the stream stays whole; a failed write answers errSSEClosed
// and ends the stream.
func (s *sseWriter) Event(name string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	msg := make([]byte, 0, len("event: \ndata: \n\n")+len(name)+len(b))
	msg = append(msg, "event: "...)
	msg = append(msg, name...)
	msg = append(msg, "\ndata: "...)
	msg = append(msg, b...)
	msg = append(msg, '\n', '\n')
	return s.write(msg)
}

// Comment writes `: text`, which a client skips.
func (s *sseWriter) Comment(text string) error {
	return s.write([]byte(": " + text + "\n\n"))
}

func (s *sseWriter) write(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return errSSEClosed
	}
	if _, err := s.w.Write(b); err != nil {
		s.fail()
		return errSSEClosed
	}
	if err := s.rc.Flush(); err != nil {
		s.fail()
		return errSSEClosed
	}
	return nil
}

func (s *sseWriter) fail() {
	s.closed = true
	s.cancel()
}

// Keepalive writes `: keepalive` every interval (sseKeepalive when it is
// not positive) until the stream is over. It returns at once; the loop
// runs until Done.
func (s *sseWriter) Keepalive(every time.Duration) {
	if every <= 0 {
		every = sseKeepalive
	}
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-tick.C:
				if s.Comment("keepalive") != nil {
					return
				}
			}
		}
	}()
}
