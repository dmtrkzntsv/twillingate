package sqlengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// TimeoutHeader carries the request's remaining time in milliseconds.
const TimeoutHeader = "X-Timeout-Ms"

// ErrUnavailable marks a failure to reach the engine or to read its
// answer: the socket is missing, refused or closed mid-flight. Match it
// with errors.Is.
var ErrUnavailable = errors.New("sqlengine: engine unavailable")

// Error is a non-2xx answer from the engine.
type Error struct {
	Status     int
	Kind       Kind
	Message    string
	RetryAfter time.Duration // from Retry-After on a 503; 0 when absent
}

func (e *Error) Error() string {
	return fmt.Sprintf("sqlengine: %d %s: %s", e.Status, e.Kind, e.Message)
}

// Client talks to one engine over its Unix socket.
type Client struct {
	hc *http.Client
}

// Dial returns a Client for the engine listening on socket. It does not
// connect; each request does.
func Dial(socket string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}
	return &Client{hc: &http.Client{Transport: tr}}
}

// Close drops the idle connections.
func (c *Client) Close() { c.hc.CloseIdleConnections() }

// Health calls GET /v1/health. The caller compares Health.Protocol with
// Protocol.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	body, err := c.do(ctx, http.MethodGet, "/v1/health", nil)
	if err != nil {
		return h, err
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return h, fmt.Errorf("%w: health body: %v", ErrUnavailable, err)
	}
	return h, nil
}

// Query calls POST /v1/query. The context's deadline becomes the
// X-Timeout-Ms header, and cancelling it closes the connection.
func (c *Client) Query(ctx context.Context, req Request) (Result, error) {
	var r Result
	payload, err := json.Marshal(req)
	if err != nil {
		return r, fmt.Errorf("sqlengine: encode request: %w", err)
	}
	body, err := c.do(ctx, http.MethodPost, "/v1/query", payload)
	if err != nil {
		return r, err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&r); err != nil {
		return Result{}, fmt.Errorf("%w: result body: %v", ErrUnavailable, err)
	}
	return r, nil
}

func (c *Client) do(ctx context.Context, method, path string, payload []byte) ([]byte, error) {
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	hr, err := http.NewRequestWithContext(ctx, method, "http://sqlengine"+path, rd)
	if err != nil {
		return nil, fmt.Errorf("sqlengine: build request: %w", err)
	}
	if payload != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	if dl, ok := ctx.Deadline(); ok {
		ms := time.Until(dl).Milliseconds() + 1 // round up
		if ms < 1 {
			ms = 1
		}
		hr.Header.Set(TimeoutHeader, strconv.FormatInt(ms, 10))
	}
	resp, err := c.hc.Do(hr)
	if err != nil {
		return nil, transportErr(ctx, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, transportErr(ctx, err)
	}
	if resp.StatusCode/100 == 2 {
		return data, nil
	}
	e := &Error{Status: resp.StatusCode, Kind: KindInternal}
	var eb ErrorBody
	if json.Unmarshal(data, &eb) == nil && eb.Error.Kind != "" {
		e.Kind, e.Message = eb.Error.Kind, eb.Error.Message
	} else {
		e.Message = strings.TrimSpace(string(data))
	}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			e.RetryAfter = time.Duration(n) * time.Second
		}
	}
	return nil, e
}

// transportErr keeps the caller's own cancellation or deadline as it is
// and marks every other failure ErrUnavailable.
func transportErr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("sqlengine: %w", cerr)
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}
