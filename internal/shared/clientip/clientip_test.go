package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func request(remote string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Add(headers[i], headers[i+1])
	}
	return r
}

func mustParse(t *testing.T, list string) Proxies {
	t.Helper()
	p, err := Parse(list)
	if err != nil {
		t.Fatalf("Parse(%q): %v", list, err)
	}
	return p
}

// A forwarding header is believed only from a trusted proxy: a client that
// connects directly and types one gets its own address.
func TestFrom(t *testing.T) {
	local := Local()
	cf := mustParse(t, "local, 173.245.48.0/20")
	for _, tc := range []struct {
		name    string
		proxies Proxies
		remote  string
		headers []string
		want    string
	}{
		{"direct, no headers", local, "203.0.113.9:4000", nil, "203.0.113.9"},
		{"direct client spoofs X-Forwarded-For", local, "203.0.113.9:4000",
			[]string{"X-Forwarded-For", "1.2.3.4"}, "203.0.113.9"},
		{"direct client spoofs CF-Connecting-IP", local, "203.0.113.9:4000",
			[]string{"CF-Connecting-IP", "1.2.3.4"}, "203.0.113.9"},
		{"local proxy forwards the client", local, "127.0.0.1:4000",
			[]string{"X-Forwarded-For", "198.51.100.7"}, "198.51.100.7"},
		{"compose network proxy", local, "172.18.0.3:4000",
			[]string{"X-Forwarded-For", "198.51.100.7"}, "198.51.100.7"},
		{"client-supplied entries sit left of the proxy's", local, "127.0.0.1:4000",
			[]string{"X-Forwarded-For", "1.2.3.4, 198.51.100.7"}, "198.51.100.7"},
		{"trusted hops on the right are skipped", local, "127.0.0.1:4000",
			[]string{"X-Forwarded-For", "1.2.3.4, 198.51.100.7, 10.0.0.2"}, "198.51.100.7"},
		{"several header lines are one list", local, "127.0.0.1:4000",
			[]string{"X-Forwarded-For", "1.2.3.4", "X-Forwarded-For", "198.51.100.7"}, "198.51.100.7"},
		{"every hop trusted: the first", local, "127.0.0.1:4000",
			[]string{"X-Forwarded-For", "10.0.0.5, 10.0.0.2"}, "10.0.0.5"},
		{"garbage entries are skipped", local, "127.0.0.1:4000",
			[]string{"X-Forwarded-For", "198.51.100.7, unknown, <script>"}, "198.51.100.7"},
		{"nothing usable: the peer", local, "127.0.0.1:4000",
			[]string{"X-Forwarded-For", "unknown"}, "127.0.0.1"},
		{"CF-Connecting-IP from a trusted proxy wins", local, "127.0.0.1:4000",
			[]string{"CF-Connecting-IP", "198.51.100.7", "X-Forwarded-For", "1.2.3.4, 173.245.48.5"}, "198.51.100.7"},
		{"CF-Connecting-IP that is no address is ignored", local, "127.0.0.1:4000",
			[]string{"CF-Connecting-IP", "nope", "X-Forwarded-For", "198.51.100.7"}, "198.51.100.7"},
		{"listed public proxy is skipped in the chain", cf, "127.0.0.1:4000",
			[]string{"X-Forwarded-For", "1.2.3.4, 198.51.100.7, 173.245.48.5"}, "198.51.100.7"},
		{"listed public proxy as the peer", cf, "173.245.48.5:443",
			[]string{"CF-Connecting-IP", "198.51.100.7"}, "198.51.100.7"},
		{"IPv6 client behind a proxy", local, "[::1]:4000",
			[]string{"X-Forwarded-For", "2001:db8::7"}, "2001:db8::7"},
		{"IPv4-mapped peer is its IPv4 self", local, "[::ffff:127.0.0.1]:4000",
			[]string{"X-Forwarded-For", "198.51.100.7"}, "198.51.100.7"},
		{"trust no one", Proxies{}, "127.0.0.1:4000",
			[]string{"X-Forwarded-For", "198.51.100.7", "CF-Connecting-IP", "198.51.100.8"}, "127.0.0.1"},
		{"remote address without a port", local, "203.0.113.9", nil, "203.0.113.9"},
		{"unparseable remote address is returned as it is", local, "@", nil, "@"},
	} {
		if got := tc.proxies.From(request(tc.remote, tc.headers...)); got != tc.want {
			t.Errorf("%s: From = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestParse(t *testing.T) {
	p := mustParse(t, " 10.1.2.3 , 2001:db8::/32,192.0.2.77/24 ")
	for addr, want := range map[string]bool{
		"10.1.2.3:1": true, "10.1.2.4:1": false, "[2001:db8::9]:1": true,
		"192.0.2.1:1": true, "127.0.0.1:1": false,
	} {
		if got := p.TrustsPeer(request(addr)); got != want {
			t.Errorf("TrustsPeer(%s) = %v, want %v", addr, got, want)
		}
	}
	for _, none := range []string{"none", "NONE", "off"} {
		if p := mustParse(t, none); p.TrustsPeer(request("127.0.0.1:1")) {
			t.Errorf("%q trusts loopback", none)
		}
	}
	if p := mustParse(t, "local,203.0.113.1"); !p.TrustsPeer(request("192.168.1.1:1")) || !p.TrustsPeer(request("203.0.113.1:1")) {
		t.Error(`"local,203.0.113.1" does not trust both`)
	}
	for _, bad := range []string{"", "10.0.0.0/8,", "example.com", "10.0.0.0/33", "none,10.0.0.1"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q): no error", bad)
		}
	}
	if (Proxies{}).TrustsPeer(request("@")) {
		t.Error("an unparseable peer is trusted")
	}
}
