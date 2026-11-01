package monitor

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeResolver answers every name from a fixed table; a missing name is a
// not-found DNS error.
type fakeResolver map[string][]string

func (f fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ips, ok := f[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	var out []net.IPAddr
	for _, s := range ips {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out, nil
}

// loopbackClient reaches httptest servers: the guard admits loopback only.
func loopbackClient(t *testing.T) *http.Client {
	t.Helper()
	return newClient(&guard{resolver: net.DefaultResolver, allowLoopback: true}, nil)
}

func anyOrigin(string) bool { return true }

func checkURL(t *testing.T, c *http.Client, url, contains string, allowed func(string) bool) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return Check(ctx, c, url, contains, allowed)
}

func TestCheckUp(t *testing.T) {
	var ua string
	var cookies int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		cookies = len(r.Cookies())
		if r.URL.Path != "/" || r.Method != http.MethodGet {
			t.Errorf("request %s %s, want GET /", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, "hello world")
	}))
	defer srv.Close()
	res := checkURL(t, loopbackClient(t), srv.URL, "", anyOrigin)
	if !res.Up || res.Status != 200 || res.Reason != "" || res.Error != "" {
		t.Fatalf("got %+v, want up 200", res)
	}
	if res.Elapsed <= 0 {
		t.Errorf("elapsed %v, want > 0", res.Elapsed)
	}
	if !strings.HasPrefix(ua, "twillingate-monitor/") || !strings.HasSuffix(ua, "(+https://github.com/dmtrkzntsv/twillingate)") {
		t.Errorf("User-Agent %q", ua)
	}
	if cookies != 0 {
		t.Errorf("sent %d cookies", cookies)
	}
}

func TestReasonDNS(t *testing.T) {
	c := newClient(&guard{resolver: fakeResolver{}}, nil)
	res := checkURL(t, c, "https://nowhere.example", "", anyOrigin)
	if res.Up || res.Reason != "dns" || res.Status != 0 || res.Error == "" {
		t.Fatalf("got %+v, want dns", res)
	}
}

func TestReasonConnect(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	res := checkURL(t, loopbackClient(t), "http://"+addr, "", anyOrigin)
	if res.Up || res.Reason != "connect" {
		t.Fatalf("got %+v, want connect", res)
	}
}

func TestReasonTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	res := Check(ctx, loopbackClient(t), srv.URL, "", anyOrigin)
	if res.Up || res.Reason != "timeout" {
		t.Fatalf("got %+v, want timeout", res)
	}
}

func TestReasonTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Config.ErrorLog = discardLog()
	defer srv.Close()
	res := checkURL(t, loopbackClient(t), srv.URL, "", anyOrigin)
	if res.Up || res.Reason != "tls" || res.Error == "" {
		t.Fatalf("got %+v, want tls", res)
	}
}

func TestReasonHTTP(t *testing.T) {
	for _, tc := range []struct {
		status int
		reason string
	}{{404, "http_4xx"}, {499, "http_4xx"}, {500, "http_5xx"}, {503, "http_5xx"}, {304, "http_5xx"}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		res := checkURL(t, loopbackClient(t), srv.URL, "", anyOrigin)
		srv.Close()
		if res.Up || res.Status != tc.status || res.Reason != tc.reason || res.Error == "" {
			t.Errorf("status %d: got %+v, want %s", tc.status, res, tc.reason)
		}
	}
}

func TestContainsCaseSensitiveWithin1MB(t *testing.T) {
	body := strings.Repeat("x", 1<<20-len("Welcome")) + "Welcome" + "Tail"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	c := loopbackClient(t)
	if res := checkURL(t, c, srv.URL, "Welcome", anyOrigin); !res.Up {
		t.Errorf("text ending at 1 MB: got %+v, want up", res)
	}
	for _, missing := range []string{"welcome", "Tail", "absent"} {
		res := checkURL(t, c, srv.URL, missing, anyOrigin)
		if res.Up || res.Reason != "content" || res.Status != 200 || res.Error == "" {
			t.Errorf("contains %q: got %+v, want content", missing, res)
		}
	}
}

func TestRedirectOffOriginsNotFollowed(t *testing.T) {
	var offsite atomic.Int32
	off := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offsite.Add(1)
	}))
	defer off.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, off.URL+"/landing", http.StatusFound)
	}))
	defer site.Close()
	allowed := func(target string) bool { return strings.HasPrefix(target, site.URL) }
	res := checkURL(t, loopbackClient(t), site.URL, "", allowed)
	if res.Up || res.Reason != "redirect" {
		t.Fatalf("got %+v, want redirect", res)
	}
	want := "redirected off-site to " + off.URL + " — add it to the project's origins if it is yours"
	if res.Error != want {
		t.Errorf("error %q, want %q", res.Error, want)
	}
	if n := offsite.Load(); n != 0 {
		t.Errorf("off-site server got %d requests, want 0", n)
	}
	if res.Status != http.StatusFound {
		t.Errorf("status %d, want the redirect's %d", res.Status, http.StatusFound)
	}
	if res.Redirects != 0 || !res.Answered {
		t.Errorf("Redirects %d Answered %v, want 0 hops followed and answered", res.Redirects, res.Answered)
	}
}

func TestRedirectWithinOriginsFollowed(t *testing.T) {
	www := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "home")
	}))
	defer www.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, www.URL+"/", http.StatusMovedPermanently)
	}))
	defer site.Close()
	var seen []string
	allowed := func(target string) bool { seen = append(seen, target); return true }
	res := checkURL(t, loopbackClient(t), site.URL, "home", allowed)
	if !res.Up || res.Status != 200 {
		t.Fatalf("got %+v, want up", res)
	}
	if len(seen) != 1 || seen[0] != www.URL+"/" {
		t.Errorf("allowed asked about %v, want [%s/]", seen, www.URL)
	}
}

func TestRedirectTooMany(t *testing.T) {
	for _, tc := range []struct {
		hops int
		up   bool
	}{{5, true}, {6, false}} {
		var requested atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requested.Add(1)
			var n int
			fmt.Sscanf(r.URL.Query().Get("n"), "%d", &n)
			if n < tc.hops {
				http.Redirect(w, r, fmt.Sprintf("/?n=%d", n+1), http.StatusFound)
			}
		}))
		res := checkURL(t, loopbackClient(t), srv.URL, "", anyOrigin)
		srv.Close()
		if res.Up != tc.up {
			t.Errorf("%d hops: got %+v, want up=%v", tc.hops, res, tc.up)
		}
		if !tc.up && (res.Reason != "redirect" || res.Error != "too many redirects") {
			t.Errorf("%d hops: got %+v, want redirect/too many redirects", tc.hops, res)
		}
		if !tc.up && res.Status != http.StatusFound {
			t.Errorf("%d hops: status %d, want the last redirect's %d", tc.hops, res.Status, http.StatusFound)
		}
		// The original request and the five hops; a sixth hop is never requested.
		if got := requested.Load(); got != 6 {
			t.Errorf("%d hops: %d requests, want 6", tc.hops, got)
		}
		if res.Redirects != 5 {
			t.Errorf("%d hops: Redirects %d, want 5", tc.hops, res.Redirects)
		}
		if !res.Answered {
			t.Errorf("%d hops: not Answered", tc.hops)
		}
	}
}

func TestRedirectsCounted(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hi") }))
	defer plain.Close()
	res := checkURL(t, loopbackClient(t), plain.URL, "", anyOrigin)
	if !res.Up || res.Redirects != 0 || !res.Answered {
		t.Errorf("no redirect: got %+v, want up, 0 redirects, answered", res)
	}
	two := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("n") == "2" {
			fmt.Fprint(w, "hi")
			return
		}
		n := 0
		fmt.Sscanf(r.URL.Query().Get("n"), "%d", &n)
		http.Redirect(w, r, fmt.Sprintf("/?n=%d", n+1), http.StatusFound)
	}))
	defer two.Close()
	if res := checkURL(t, loopbackClient(t), two.URL, "", anyOrigin); !res.Up || res.Redirects != 2 {
		t.Errorf("two hops: got %+v, want up with 2 redirects", res)
	}
}

func TestDialFailureIsNotAnswered(t *testing.T) {
	c := newClient(&guard{resolver: fakeResolver{}}, nil)
	res := checkURL(t, c, "http://nowhere.example", "", anyOrigin)
	if res.Up || res.Answered || res.Redirects != 0 {
		t.Errorf("got %+v, want down, unanswered, 0 redirects", res)
	}
}

func TestPrivateAddressBlocked(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1",
		"192.168.1.1", "172.16.0.1", "fc00::1", "fe80::1", "0.0.0.0", "::", "224.0.0.1", "::ffff:10.0.0.1", "64:ff9b::a00:1", "255.255.255.255"} {
		c := newClient(&guard{resolver: fakeResolver{"site.example": {ip}}}, nil)
		res := checkURL(t, c, "http://site.example", "", anyOrigin)
		if res.Up || res.Reason != "blocked" {
			t.Errorf("%s: got %+v, want blocked", ip, res)
		}
	}
	// An address literal in the origin is guarded too.
	c := newClient(&guard{resolver: fakeResolver{}}, nil)
	for _, literal := range []string{"http://10.0.0.1", "http://[fd00::1%25x]", "http://[fe80::1%25eth0]"} {
		if res := checkURL(t, c, literal, "", anyOrigin); res.Reason != "blocked" {
			t.Errorf("literal %s: got %+v, want blocked", literal, res)
		}
	}
}

func TestBlockedRedirectHop(t *testing.T) {
	// The site itself is reachable (loopback allowed), but it redirects to
	// a name that resolves to a private address.
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://internal.example/", http.StatusFound)
	}))
	defer site.Close()
	r := fakeResolver{"internal.example": {"10.1.2.3"}}
	c := newClient(&guard{resolver: r, allowLoopback: true}, nil)
	if res := checkURL(t, c, site.URL, "", anyOrigin); res.Reason != "blocked" {
		t.Errorf("got %+v, want blocked", res)
	}
}

func TestSpecialPurposeAddresses(t *testing.T) {
	for _, tc := range []struct {
		ip      string
		blocked bool
	}{
		{"0.1.2.3", true}, {"10.0.0.1", true}, {"100.64.0.1", true}, {"100.127.255.254", true},
		{"127.0.0.1", true}, {"169.254.169.254", true}, {"172.16.0.1", true}, {"172.31.255.1", true},
		{"192.0.0.8", true}, {"192.0.2.1", true}, {"192.88.99.1", true}, {"192.168.1.1", true},
		{"198.18.0.1", true}, {"198.19.255.1", true}, {"198.51.100.7", true}, {"203.0.113.9", true},
		{"224.0.0.1", true}, {"239.255.255.250", true}, {"240.0.0.1", true}, {"255.255.255.255", true},
		{"::", true}, {"::1", true}, {"::ffff:10.0.0.1", true}, {"::ffff:127.0.0.1", true},
		{"::10.0.0.1", true}, {"::127.0.0.1", true},
		{"64:ff9b::a00:1", true}, {"64:ff9b::7f00:1", true}, {"64:ff9b:1::a00:1", true}, {"64:ff9b:1::5db8:d822", true},
		{"100::1", true}, {"2001::1", true}, {"2001:1ff::1", true}, {"2001:db8::1", true}, {"3fff::1", true},
		// 6to4 is deprecated: blocked whatever the embedded gateway, since the
		// host behind it (the low bits here) is not what is judged.
		{"2002:a00:1::1", true}, {"2002:c0a8:101::1", true}, {"2002:808:808::1", true}, {"2002:808:808::a00:1", true},
		// A zone hides nothing: Prefix.Contains is false for any zoned address.
		{"fd00::1%x", true}, {"fe80::1%eth0", true}, {"::1%lo", true}, {"ff02::1%eth0", true},
		{"fc00::1", true}, {"fd12:3456::1", true}, {"fe80::1", true}, {"fec0::1", true}, {"ff02::1", true},
		// public
		{"93.184.216.34", false}, {"8.8.8.8", false}, {"100.128.0.1", false}, {"172.32.0.1", false},
		{"192.0.1.1", false}, {"198.20.0.1", false}, {"223.255.255.1", false},
		{"2606:2800:220:1::1", false}, {"2001:4860:4860::8888", false}, {"2a00:1450::1", false},
		{"::ffff:8.8.8.8", false}, {"64:ff9b::808:808", false}, {"::8.8.8.8", false},
	} {
		if got := blocked(mustAddr(t, tc.ip), false); got != tc.blocked {
			t.Errorf("%s: blocked %v, want %v", tc.ip, got, tc.blocked)
		}
	}
}

func TestPublicAddressPassesGuard(t *testing.T) {
	if blocked(mustAddr(t, "127.0.0.1"), true) {
		t.Error("loopback blocked with allowLoopback")
	}
	if !blocked(mustAddr(t, "10.0.0.1"), true) {
		t.Error("private allowed with allowLoopback")
	}
}

func TestErrorTruncated(t *testing.T) {
	long := strings.Repeat("é", 300)
	if got := truncate(long); len([]rune(got)) > 200 || !strings.HasPrefix(long, strings.TrimSuffix(got, "…")) {
		t.Errorf("truncate: %d runes", len([]rune(got)))
	}
}

func TestTLSConfigNotShared(t *testing.T) {
	// The transport keeps its own TLS config with verification on.
	c := loopbackClient(t)
	tr := c.Transport.(*http.Transport)
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("verification off")
	}
	if !tr.DisableKeepAlives || tr.Proxy != nil {
		t.Error("keep-alives on or a proxy set")
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestControlRefusesPrivate(t *testing.T) {
	g := &guard{}
	if err := g.control("tcp4", "10.0.0.1:80", nil); err == nil {
		t.Error("10.0.0.1 passed control")
	}
	if err := g.control("tcp4", "93.184.216.34:443", nil); err != nil {
		t.Errorf("public address refused: %v", err)
	}
	if err := g.control("tcp4", "garbage", nil); err == nil {
		t.Error("unparsable address passed control")
	}
}

func TestAttemptDeadline(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		remaining time.Duration
		left      int
		want      time.Duration
	}{
		{10 * time.Second, 1, 10 * time.Second}, // the last address takes what is left
		{10 * time.Second, 2, 5 * time.Second},
		{9 * time.Second, 3, 3 * time.Second},
		{10 * time.Second, 10, time.Second}, // an even share, however many
		{2 * time.Second, 2, time.Second},   // a short budget is not eaten by the first address
		{-time.Second, 2, -time.Second},     // already past: nothing to share
	} {
		if got := attemptDeadline(now, now.Add(tc.remaining), tc.left); !got.Equal(now.Add(tc.want)) {
			t.Errorf("%v over %d: %v, want %v", tc.remaining, tc.left, got.Sub(now), tc.want)
		}
	}
}

// deadFirst resolves site.example to two public addresses: the first never
// answers, as a blackholed one does, and the second reaches serverAddr.
func deadFirst(serverAddr string, budget time.Duration) *guard {
	return &guard{
		resolver: fakeResolver{"site.example": {"93.184.216.34", "93.184.216.35"}},
		budget:   budget,
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "93.184.216.34:") {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			var d net.Dialer
			return d.DialContext(ctx, network, serverAddr)
		},
	}
}

// http.Transport detaches its dial from the request's deadline, so this
// path is the one that needs the guard's own budget.
func TestCheckSurvivesADeadAddress(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer srv.Close()
	c := newClient(deadFirst(srv.Listener.Addr().String(), 4*time.Second), nil)
	if res := checkURL(t, c, "http://site.example", "ok", anyOrigin); !res.Up {
		t.Errorf("got %+v, want up through the second address", res)
	}
}

// silentListener accepts and never writes; closed reports each accepted
// connection the client closed (its read ended).
func silentListener(t *testing.T) (addr string, closed chan struct{}) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	closed = make(chan struct{}, 8)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(io.Discard, c) // returns when the client closes
				closed <- struct{}{}
			}()
		}
	}()
	return l.Addr().String(), closed
}

func requireClosed(t *testing.T, closed chan struct{}, within time.Duration) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(within):
		t.Fatalf("the client left the connection open for %v", within)
	}
}

// A host that takes the connection and says nothing ends the check as a
// timeout, and the client closes the socket: nothing is left to leak.
func TestSilentHostIsClosedByTheClient(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			addr, closed := silentListener(t)
			g := &guard{resolver: net.DefaultResolver, allowLoopback: true, budget: time.Second}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // the dial is detached from it anyway
			defer cancel()
			start := time.Now()
			res := Check(ctx, newClient(g, nil), scheme+"://"+addr, "", anyOrigin)
			if res.Up || res.Reason != "timeout" {
				t.Fatalf("got %+v, want timeout", res)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("took %v, want about the 1 s budget", d)
			}
			requireClosed(t, closed, 3*time.Second)
		})
	}
}

func TestRDAPClientKeepsItsOwnLimit(t *testing.T) {
	r := newRunner(&fakeStore{}, allowAll{}, &fakeLog{}, 60*time.Second, quietLogger(), options{timeout: time.Second})
	tr := r.rdap.client.Transport.(*http.Transport)
	if tr.ResponseHeaderTimeout != rdapTimeout || tr.TLSHandshakeTimeout != rdapTimeout {
		t.Errorf("rdap transport header %v handshake %v, want %v", tr.ResponseHeaderTimeout, tr.TLSHandshakeTimeout, rdapTimeout)
	}
	tr = r.client.Transport.(*http.Transport)
	if tr.ResponseHeaderTimeout != time.Second || tr.TLSHandshakeTimeout != time.Second {
		t.Errorf("check transport header %v handshake %v, want 1s", tr.ResponseHeaderTimeout, tr.TLSHandshakeTimeout)
	}
}

func TestSilentRDAPHostIsClosed(t *testing.T) {
	addr, closed := silentListener(t)
	g := &guard{resolver: net.DefaultResolver, allowLoopback: true, budget: time.Second}
	r := &rdap{client: newClient(g, nil)}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/", nil)
	if _, err := r.client.Do(req); err == nil {
		t.Fatal("a silent host answered")
	}
	requireClosed(t, closed, 3*time.Second)
}

// Errors from the TLS stage, the server's alert or a version the client
// refuses, are tls, not connect.
func TestReasonTLSHandshakeFailures(t *testing.T) {
	alert := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	alert.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return nil, errors.New("no")
	}}
	old := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	old.TLS = &tls.Config{MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}
	for name, srv := range map[string]*httptest.Server{"alert": alert, "old version": old} {
		srv.Config.ErrorLog = discardLog()
		srv.StartTLS()
		res := checkURL(t, loopbackClient(t), srv.URL, "", anyOrigin)
		srv.Close()
		if res.Up || res.Reason != "tls" {
			t.Errorf("%s: got %+v, want tls", name, res)
		}
	}
}

// At a short budget the first, dead address must not take all of it.
func TestShortBudgetLeavesTimeForTheNextAddress(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer srv.Close()
	c := newClient(deadFirst(srv.Listener.Addr().String(), 2*time.Second), nil)
	if res := checkURL(t, c, "http://site.example", "ok", anyOrigin); !res.Up {
		t.Errorf("got %+v, want up through the second address", res)
	}
}
