package monitor

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/dmtrkzntsv/twillingate/internal/shared/version"
)

// Failure reasons, the closed vocabulary docs/twillingate.md lists.
const (
	ReasonDNS      = "dns"
	ReasonConnect  = "connect"
	ReasonTimeout  = "timeout"
	ReasonTLS      = "tls"
	ReasonHTTP4xx  = "http_4xx"
	ReasonHTTP5xx  = "http_5xx"
	ReasonRedirect = "redirect"
	ReasonContent  = "content"
	ReasonBlocked  = "blocked"
)

const (
	maxHops   = 5       // redirects followed; the 6th fails the check
	bodyLimit = 1 << 20 // bytes of the page searched for the text
	errorMax  = 200     // runes of error text kept on a row
)

var userAgent = "twillingate-monitor/" + version.Version + " (+https://github.com/dmtrkzntsv/twillingate)"

// Result is one availability check. Status is the final HTTP status, 0
// when no response arrived; Reason and Error are set only when down.
type Result struct {
	Up      bool
	Status  int
	Reason  string
	Error   string
	Elapsed time.Duration
	// Redirects is the hops followed, also on a failed check (the count
	// reached). Answered is whether any response arrived, a refused
	// redirect or a bad status included: false for a failure before one.
	Redirects int
	Answered  bool
}

// redirectError stops a redirect without following it.
type redirectError struct{ msg string }

func (e *redirectError) Error() string { return e.msg }

// Check GETs origin + "/" with c, following a redirect only when
// allowed(target) says its target is one of the project's own origins,
// and at most maxHops of them. Up means a 2xx whose first MB contains
// contains (case-sensitive; any page when contains is empty). Elapsed runs
// to the end of the body read or to the failure. ctx bounds the whole
// check.
func Check(ctx context.Context, c *http.Client, origin, contains string, allowed func(target string) bool) Result {
	start := time.Now()
	var t tally
	res := check(ctx, c, origin, contains, allowed, &t)
	res.Redirects, res.Answered = t.hops, t.answered || res.Status > 0
	res.Elapsed = time.Since(start)
	res.Error = truncate(res.Error)
	return res
}

// tally is what a check learned on the way: the hops followed and whether
// any response came.
type tally struct {
	hops      int
	answered  bool
	tlsFailed atomic.Bool // a TLS handshake ended in an error
}

func check(ctx context.Context, c *http.Client, origin, contains string, allowed func(string) bool, t *tally) Result {
	cc := *c
	cc.Jar = nil
	cc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		t.answered = true // a redirect is a response
		if len(via) > maxHops {
			return &redirectError{"too many redirects"}
		}
		if !allowed(req.URL.String()) {
			return &redirectError{"redirected off-site to " + req.URL.Scheme + "://" + req.URL.Host +
				" — add it to the project's origins if it is yours"}
		}
		t.hops = len(via)
		return nil
	}
	trace := &httptrace.ClientTrace{TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
		if err != nil {
			t.tlsFailed.Store(true)
		}
	}}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, strings.TrimSuffix(origin, "/")+"/", nil)
	if err != nil {
		return Result{Reason: ReasonConnect, Error: err.Error()}
	}
	req.Header.Set("User-Agent", userAgent)
	req.Close = true
	resp, err := cc.Do(req)
	if err != nil {
		r := failure(ctx, err)
		if t.tlsFailed.Load() && r.Reason == ReasonConnect {
			// A failure in the handshake, whatever its error type.
			r.Reason, r.Error = ReasonTLS, handshakeFailed
		}
		if resp != nil { // a refused redirect: the response is that redirect
			r.Status = resp.StatusCode
		}
		return r
	}
	defer resp.Body.Close()
	t.answered = true
	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	if err != nil {
		r := failure(ctx, err)
		r.Status = resp.StatusCode
		return r
	}
	switch s := resp.StatusCode; {
	case s >= 200 && s < 300:
	case s >= 400 && s < 500:
		return Result{Status: s, Reason: ReasonHTTP4xx, Error: "HTTP " + statusText(s)}
	default:
		return Result{Status: s, Reason: ReasonHTTP5xx, Error: "HTTP " + statusText(s)}
	}
	if contains != "" && !bytes.Contains(body, []byte(contains)) {
		return Result{Status: resp.StatusCode, Reason: ReasonContent,
			Error: "the page does not contain the expected text"}
	}
	return Result{Up: true, Status: resp.StatusCode}
}

func statusText(s int) string {
	if t := http.StatusText(s); t != "" {
		return fmt.Sprintf("%d %s", s, t)
	}
	return fmt.Sprint(s)
}

// What a check says of the network, which customers read: fixed text, since the
// raw errors name this server's side, its resolver and its address on the
// connection ("lookup x on 127.0.0.53:53", "read tcp 10.0.0.5:4242->...").
const (
	noAnswerInTime  = "no complete answer in time"
	noSuchHost      = "no such host"
	lookupFailed    = "DNS lookup failed"
	connRefused     = "connection refused"
	connFailed      = "connection failed"
	handshakeFailed = "TLS handshake failed"
)

// failure classifies a request that got no usable response. Only what describes
// the site keeps its own words: a refused redirect, a private address, and what
// TLS says of its certificate and handshake.
func failure(ctx context.Context, err error) Result {
	var (
		be  *blockedError
		re  *redirectError
		dns *net.DNSError
		ne  net.Error
		ue  *url.Error
	)
	switch {
	case errors.As(err, &be):
		return Result{Reason: ReasonBlocked, Error: be.Error()}
	case errors.As(err, &re):
		return Result{Reason: ReasonRedirect, Error: re.msg}
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
		return Result{Reason: ReasonTimeout, Error: noAnswerInTime}
	case errors.As(err, &dns):
		if dns.IsNotFound {
			return Result{Reason: ReasonDNS, Error: noSuchHost}
		}
		return Result{Reason: ReasonDNS, Error: lookupFailed}
	case isTLS(err):
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return Result{Reason: ReasonTLS, Error: err.Error()}
	case errors.As(err, &ne) && ne.Timeout():
		return Result{Reason: ReasonTimeout, Error: noAnswerInTime}
	case errors.Is(err, syscall.ECONNREFUSED):
		return Result{Reason: ReasonConnect, Error: connRefused}
	default:
		return Result{Reason: ReasonConnect, Error: connFailed}
	}
}

func isTLS(err error) bool {
	var (
		cv  *tls.CertificateVerificationError
		ua  x509.UnknownAuthorityError
		he  x509.HostnameError
		ci  x509.CertificateInvalidError
		al  tls.AlertError
		rh  tls.RecordHeaderError
		ech *tls.ECHRejectionError
	)
	return errors.As(err, &cv) || errors.As(err, &ua) || errors.As(err, &he) || errors.As(err, &ci) ||
		errors.As(err, &al) || errors.As(err, &rh) || errors.As(err, &ech)
}

// truncate keeps s within errorMax runes, on a rune boundary.
func truncate(s string) string {
	if utf8.RuneCountInString(s) <= errorMax {
		return s
	}
	r := []rune(s)
	return string(r[:errorMax-1]) + "…"
}
