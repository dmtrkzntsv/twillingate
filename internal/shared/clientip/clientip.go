// Package clientip works out which address a request came from when
// reverse proxies stand in front of the server. A forwarding header is a
// claim anyone can type, so it is read only when the connection itself
// comes from a proxy the operator trusts; from anyone else the address is
// the connection's own.
package clientip

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Proxies is the set of addresses whose forwarding headers are believed.
// The zero value trusts no one.
type Proxies struct {
	prefixes []netip.Prefix
}

// Local is the default set: loopback, the private ranges (RFC 1918 and
// IPv6 unique local) and link-local, where a reverse proxy on the same
// host, in the same compose network or on the same LAN connects from. A
// client on the public internet is never in it.
func Local() Proxies {
	return Proxies{prefixes: []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("fc00::/7"),
		netip.MustParsePrefix("fe80::/10"),
	}}
}

// Parse reads a comma-separated list of addresses and CIDR prefixes
// ("10.0.0.0/8, 2001:db8::1"). "none" (or "off") trusts no one; "local"
// stands for Local's ranges and may be mixed with others. An empty list
// is refused: the caller decides what unset means.
func Parse(list string) (Proxies, error) {
	var p Proxies
	fields := strings.Split(list, ",")
	for _, f := range fields {
		f = strings.TrimSpace(f)
		switch strings.ToLower(f) {
		case "":
			return Proxies{}, fmt.Errorf("an empty entry in %q", list)
		case "none", "off":
			if len(fields) > 1 {
				return Proxies{}, fmt.Errorf("%q stands alone, not in a list", f)
			}
			return Proxies{}, nil
		case "local":
			p.prefixes = append(p.prefixes, Local().prefixes...)
			continue
		}
		if pre, err := netip.ParsePrefix(f); err == nil {
			p.prefixes = append(p.prefixes, pre.Masked())
			continue
		}
		addr, err := netip.ParseAddr(f)
		if err != nil {
			return Proxies{}, fmt.Errorf("%q is not an IP address or a CIDR prefix", f)
		}
		addr = addr.Unmap()
		p.prefixes = append(p.prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return p, nil
}

// Trusts reports whether addr is one of the proxies.
func (p Proxies) Trusts(addr netip.Addr) bool {
	addr = addr.Unmap().WithZone("")
	for _, pre := range p.prefixes {
		if pre.Contains(addr) {
			return true
		}
	}
	return false
}

// TrustsPeer reports whether the request's connection comes from one of
// the proxies, which is when its other forwarding headers
// (X-Forwarded-Proto) may be believed.
func (p Proxies) TrustsPeer(r *http.Request) bool {
	peer, ok := peerAddr(r)
	return ok && p.Trusts(peer)
}

// From is the address the request came from, as text.
//
// A connection from anyone but a trusted proxy is its own address, and its
// headers are not read. From a trusted proxy: CF-Connecting-IP when it
// holds an address (Cloudflare overwrites whatever the client sent, so it
// is sound as long as the proxy is reachable only through Cloudflare; a
// proxy that is not must strip it), else the right-most X-Forwarded-For
// address that is not itself a trusted proxy, which is the one address in
// that header a client cannot choose, since each proxy appends the peer
// it saw. Entries that are not addresses are skipped; with nothing
// usable, the connection's address.
func (p Proxies) From(r *http.Request) string {
	peer, ok := peerAddr(r)
	if !ok {
		return r.RemoteAddr
	}
	if !p.Trusts(peer) {
		return peer.String()
	}
	if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); err == nil {
		return a.Unmap().String()
	}
	// Several X-Forwarded-For lines are one list, in order.
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	var outermost netip.Addr
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			continue
		}
		a = a.Unmap().WithZone("")
		if !p.Trusts(a) {
			return a.String()
		}
		outermost = a
	}
	// Every hop is a trusted proxy (a request from inside the network):
	// the first of them is where it began.
	if outermost.IsValid() {
		return outermost.String()
	}
	return peer.String()
}

// peerAddr is the address of the connection itself.
func peerAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap().WithZone(""), true
}
