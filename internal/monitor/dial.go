package monitor

import (
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
}

// blockedError is a dial refused because every address the name
// resolved to is private.
type blockedError struct{ host string }

func (e *blockedError) Error() string {
	return fmt.Sprintf("%s resolves to a private address", e.host)
}

var (
	cgnat     = netip.MustParsePrefix("100.64.0.0/10")
	thisNet   = netip.MustParsePrefix("0.0.0.0/8")
	broadcast = netip.MustParseAddr("255.255.255.255")
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
)

// blocked reports whether a may not be dialed: loopback (unless
// allowLoopback), private (RFC 1918, fc00::/7), link-local, CGNAT,
// unspecified, "this network", broadcast or multicast. An IPv4 address
// embedded as IPv4-mapped or in the NAT64 prefix is judged as itself.
func blocked(a netip.Addr, allowLoopback bool) bool {
	a = a.Unmap()
	if nat64.Contains(a) {
		b := a.As16()
		a = netip.AddrFrom4([4]byte(b[12:16]))
	}
	if a.IsLoopback() {
		return !allowLoopback
	}
	return !a.IsValid() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() ||
		cgnat.Contains(a) || thisNet.Contains(a) || a == broadcast
}

// DialContext resolves addr's host, drops every private address and dials
// the rest in turn. When nothing public is left the error is a
// *blockedError.
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
	d := &net.Dialer{Timeout: 10 * time.Second, Control: g.control}
	var errs []error
	tried := false
	for _, a := range ips {
		if blocked(a, g.allowLoopback) {
			continue
		}
		tried = true
		c, err := d.DialContext(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	if !tried {
		if len(ips) == 0 {
			return nil, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
		}
		return nil, &blockedError{host: host}
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
