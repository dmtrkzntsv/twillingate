package monitor

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

const (
	dialBudget = 10 * time.Second // a dial's whole limit when its context sets none
	minAttempt = 2 * time.Second  // one address's least share of the limit
)

// resolver is the slice of *net.Resolver the guard uses; tests answer
// names from a table.
type resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// guard dials only public addresses, so a monitor (or a redirect hop, or
// an RDAP server) can never make this machine reach its own network. The
// name is resolved here and the resolved address dialed, with the check
// repeated on the socket's address in Control, so a name that resolves
// differently on a second lookup (DNS rebinding) cannot get past it.
type guard struct {
	resolver resolver
	// allowLoopback admits 127.0.0.0/8 and ::1, nothing else; tests set
	// it to reach httptest servers.
	allowLoopback bool
	// budget is how long a dial whose context carries no deadline may take
	// over all its addresses (0: dialBudget). dial connects to one resolved
	// address (nil: a net.Dialer with control). Test seams.
	budget time.Duration
	dial   func(ctx context.Context, network, addr string) (net.Conn, error)
}

// blockedError is a dial refused because every address the name
// resolved to is private.
type blockedError struct{ host string }

func (e *blockedError) Error() string {
	return fmt.Sprintf("%s resolves to a private address", e.host)
}

// denied lists the special-purpose ranges (IANA's IPv4 and IPv6
// special-purpose registries) a site cannot legitimately be on. Loopback
// is judged apart, for the test seam.
var denied = prefixes(
	// IPv4
	"0.0.0.0/8",       // "this network", unspecified
	"10.0.0.0/8",      // private
	"100.64.0.0/10",   // CGNAT
	"169.254.0.0/16",  // link-local
	"172.16.0.0/12",   // private
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // TEST-NET-1
	"192.88.99.0/24",  // 6to4 relay anycast
	"192.168.0.0/16",  // private
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // TEST-NET-2
	"203.0.113.0/24",  // TEST-NET-3
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reserved, broadcast included
	// IPv6
	"::/128",         // unspecified
	"64:ff9b:1::/48", // local-use NAT64: where the IPv4 sits is the operator's choice
	"2002::/16",      // 6to4 (deprecated): the address names a gateway, not the host behind it
	"100::/64",       // discard
	"2001::/23",      // IETF protocol assignments (Teredo included)
	"2001:db8::/32",  // documentation
	"3fff::/20",      // documentation
	"fc00::/7",       // unique local
	"fe80::/10",      // link-local
	"fec0::/10",      // site-local (deprecated)
	"ff00::/8",       // multicast
)

var (
	nat64    = netip.MustParsePrefix("64:ff9b::/96")
	v4compat = netip.MustParsePrefix("::/96")
)

func prefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// embedded4 is the IPv4 address an IPv6 address carries by a fixed rule:
// well-known NAT64 (64:ff9b::/96) or IPv4-compatible (::/96).
func embedded4(a netip.Addr) (netip.Addr, bool) {
	if nat64.Contains(a) || v4compat.Contains(a) {
		b := a.As16()
		return netip.AddrFrom4([4]byte(b[12:16])), true
	}
	return netip.Addr{}, false
}

// blocked reports whether a may not be dialed: loopback (unless
// allowLoopback) or any range in denied. A zone is ignored (Prefix.Contains
// is false for any zoned address, so it would hide the address from the
// ranges), and an IPv4 address embedded in an IPv6 one (IPv4-mapped, NAT64,
// IPv4-compatible) is judged as itself.
func blocked(a netip.Addr, allowLoopback bool) bool {
	if !a.IsValid() {
		return true
	}
	a = a.WithZone("").Unmap()
	if a.IsLoopback() {
		return !allowLoopback
	}
	if a.Is6() {
		if v4, ok := embedded4(a); ok {
			return blocked(v4, false)
		}
	}
	for _, p := range denied {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// attemptDeadline is the deadline of one dial with left addresses still to
// try, the current one among them: its share of the time remaining, but
// at least minAttempt while that much remains, as the standard library's
// dialer shares it. One dead address so leaves time for the next.
func attemptDeadline(now, deadline time.Time, left int) time.Time {
	remaining := deadline.Sub(now)
	if remaining <= 0 || left <= 1 {
		return deadline
	}
	share := remaining / time.Duration(left)
	if share < minAttempt {
		share = min(remaining, minAttempt)
	}
	return now.Add(share)
}

// DialContext resolves addr's host, drops every private address and dials
// the rest in turn, each with its share of the time. That time is ctx's
// deadline, or the budget when it has none: http.Transport detaches its
// dials from the request's deadline. When nothing public is left the error
// is a *blockedError.
func (g *guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{a}
	} else {
		res, err := g.resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, r := range res {
			if a, ok := netip.AddrFromSlice(r.IP); ok {
				ips = append(ips, a)
			}
		}
	}
	var public []netip.Addr
	for _, a := range ips {
		if !blocked(a, g.allowLoopback) {
			public = append(public, a.WithZone("").Unmap())
		}
	}
	if len(public) == 0 {
		if len(ips) == 0 {
			return nil, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
		}
		return nil, &blockedError{host: host}
	}
	connect := g.dial
	if connect == nil {
		connect = (&net.Dialer{Control: g.control}).DialContext
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(cmp.Or(g.budget, dialBudget))
	}
	var errs []error
	for i, a := range public {
		attempt, cancel := context.WithDeadline(ctx, attemptDeadline(time.Now(), deadline, len(public)-i))
		c, err := connect(attempt, network, net.JoinHostPort(a.String(), port))
		cancel()
		if err == nil {
			return c, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}

// control refuses the socket's own address once more, right before the
// connect: the last word, whatever produced the address.
func (g *guard) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	if blocked(ap.Addr(), g.allowLoopback) {
		return &blockedError{host: ap.Addr().String()}
	}
	return nil
}

// newClient is the client every check shares: the guard dials, no proxy
// (a proxy would dial for us, past the guard), no cookie jar, no
// keep-alives, verification on against roots (nil: the system's).
// Check sets its own redirect policy on a copy.
func newClient(g *guard, roots *x509.CertPool) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext:            g.DialContext,
		TLSClientConfig:        &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}}
}
