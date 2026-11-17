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
// headers are not read. From a trusted proxy: the right-most
// X-Forwarded-For address that is not itself a trusted proxy, which is the
// one address in that header a client cannot choose, since each proxy
// appends the peer it saw. An entry may carry a port; one that is no
// address is skipped; with nothing usable, the connection's address.
//
// CF-Connecting-IP is never read. A proxy passes on headers it does not
// know, so behind a Caddy or nginx that the internet reaches directly a
// client could type one and pick its address, its visitor hash and its
// login budget, the owner's included. Cloudflare appends the visitor to
// X-Forwarded-For as well, whether it connects straight here or through
// cloudflared, so that header alone already names the client.
func (p Proxies) From(r *http.Request) string {
	peer, ok := peerAddr(r)
	if !ok {
		return r.RemoteAddr
	}
	if !p.Trusts(peer) {
		return peer.String()
	}
	// Several X-Forwarded-For lines are one list, in order.
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	var outermost netip.Addr
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := hopAddr(hops[i])
		if !ok {
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

// hopAddr reads one X-Forwarded-For entry: an address, or one with a port
// ("203.0.113.5:4321", "[2001:db8::1]:443"), which some proxies append.
func hopAddr(hop string) (netip.Addr, bool) {
	hop = strings.TrimSpace(hop)
	if a, err := netip.ParseAddr(hop); err == nil {
		return a, true
	}
	if ap, err := netip.ParseAddrPort(hop); err == nil {
		return ap.Addr(), true
	}
	if a, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(hop, "["), "]")); err == nil {
		return a, true
	}
	return netip.Addr{}, false
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
