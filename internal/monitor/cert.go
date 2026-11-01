package monitor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"math"
	"net"
	"net/url"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// checkCert reads origin's leaf certificate through g: its expiry and
// issuer whatever happens, and whether it verifies for the host against
// roots (nil: the system's). A handshake that fails leaves ExpiresAt
// empty and says why in Error.
func checkCert(ctx context.Context, g *guard, roots *x509.CertPool, origin string, now time.Time) store.CertState {
	st := store.CertState{CheckedAt: now.UTC().Format(time.RFC3339)}
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		st.Error = "not a URL"
		return st
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "443"
	}
	raw, err := g.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		st.Error = truncate(err.Error())
		return st
	}
	defer raw.Close()
	if dl, ok := ctx.Deadline(); ok {
		raw.SetDeadline(dl)
	}
	// Verification is done below, by hand, so an invalid certificate is
	// still read for its dates. Nothing is sent over this connection: it
	// closes right after the handshake.
	conn := tls.Client(raw, &tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	if err := conn.HandshakeContext(ctx); err != nil {
		st.Error = truncate(err.Error())
		return st
	}
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		st.Error = "no certificate"
		return st
	}
	leaf := certs[0]
	st.ExpiresAt = leaf.NotAfter.UTC().Format(time.RFC3339)
	st.Issuer = leaf.Issuer.CommonName
	if st.Issuer == "" && len(leaf.Issuer.Organization) > 0 {
		st.Issuer = leaf.Issuer.Organization[0]
	}
	st.Issuer = truncate(st.Issuer)
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: inter, CurrentTime: now})
	st.Valid = err == nil
	if err != nil {
		st.Error = truncate(err.Error())
	}
	return st
}

// daysLeft is the whole days from now until at (RFC 3339, or a bare
// YYYY-MM-DD read as its midnight UTC), rounded down; false when at is
// empty or unreadable.
func daysLeft(at string, now time.Time) (float64, bool) {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		if t, err = time.Parse(time.DateOnly, at); err != nil {
			return 0, false
		}
	}
	return math.Floor(t.Sub(now).Hours() / 24), true
}
