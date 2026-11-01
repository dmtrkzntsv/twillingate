package monitor

import (
	"context"
	"fmt"
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
	}{{10, true}, {11, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if res := checkURL(t, c, "http://10.0.0.1", "", anyOrigin); res.Reason != "blocked" {
		t.Errorf("literal: got %+v, want blocked", res)
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
		{"2002:a00:1::1", true}, {"2002:c0a8:101::1", true},
		{"fc00::1", true}, {"fd12:3456::1", true}, {"fe80::1", true}, {"fec0::1", true}, {"ff02::1", true},
		// public
		{"93.184.216.34", false}, {"8.8.8.8", false}, {"100.128.0.1", false}, {"172.32.0.1", false},
		{"192.0.1.1", false}, {"198.20.0.1", false}, {"223.255.255.1", false},
		{"2606:2800:220:1::1", false}, {"2001:4860:4860::8888", false}, {"2a00:1450::1", false},
		{"::ffff:8.8.8.8", false}, {"64:ff9b::808:808", false}, {"2002:808:808::1", false}, {"::8.8.8.8", false},
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
