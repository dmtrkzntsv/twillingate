package monitor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func discardLog() *log.Logger { return log.New(io.Discard, "", 0) }

func TestCertState(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Config.ErrorLog = discardLog()
	defer srv.Close()
	leaf := srv.Certificate()
	g := &guard{resolver: net.DefaultResolver, allowLoopback: true}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	// Self-signed: read, but not verified.
	st := checkCert(ctx, g, nil, srv.URL, now)
	if st.Valid || st.Error == "" {
		t.Errorf("self-signed: got %+v, want invalid with an error", st)
	}
	if want := leaf.NotAfter.UTC().Format(time.RFC3339); st.ExpiresAt != want {
		t.Errorf("expires %q, want %q", st.ExpiresAt, want)
	}
	if st.CheckedAt != "2026-11-01T12:00:00Z" {
		t.Errorf("checked_at %q", st.CheckedAt)
	}

	// Trusted root: verified.
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	st = checkCert(ctx, g, pool, srv.URL, now)
	if !st.Valid || st.Error != "" || st.ExpiresAt == "" {
		t.Errorf("trusted: got %+v, want valid", st)
	}
	if st.Issuer != leaf.Issuer.CommonName && st.Issuer != firstOrg(leaf) {
		t.Errorf("issuer %q", st.Issuer)
	}

	// Nothing listening: no expiry, an error.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	st = checkCert(ctx, g, nil, "https://"+addr, now)
	if st.Valid || st.Error == "" || st.ExpiresAt != "" {
		t.Errorf("refused: got %+v", st)
	}

	// A private address is refused by the same guard.
	st = checkCert(ctx, &guard{resolver: fakeResolver{"x.example": {"10.0.0.1"}}}, nil, "https://x.example", now)
	if st.Valid || st.Error == "" || st.ExpiresAt != "" {
		t.Errorf("blocked: got %+v", st)
	}
}

func firstOrg(c *x509.Certificate) string {
	if len(c.Issuer.Organization) > 0 {
		return c.Issuer.Organization[0]
	}
	return ""
}

func TestDaysLeft(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		at   string
		want float64
	}{
		{"2026-11-11T12:00:00Z", 10},
		{"2026-11-11T11:59:59Z", 9},
		{"2026-11-11", 10}, // a bare date counts calendar days in UTC
		{"2026-11-01", 0},  // its own day, not yet past
		{"2026-10-31", -1},
		{"2026-10-30T12:00:00Z", -2},
	} {
		got, ok := daysLeft(tc.at, now)
		if !ok || got != tc.want {
			t.Errorf("%s: got %v %v, want %v", tc.at, got, ok, tc.want)
		}
	}
	if _, ok := daysLeft("", now); ok {
		t.Error("empty: want unknown")
	}
}

func TestCertSurvivesADeadAddress(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Config.ErrorLog = discardLog()
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	st := checkCert(ctx, deadFirst(srv.Listener.Addr().String(), 0), nil, "https://site.example", time.Now())
	if st.ExpiresAt == "" {
		t.Errorf("got %+v, want the certificate read through the second address", st)
	}
}

// A bare date counts the same at any hour, as the console's daysUntilDate
// does, so the Availability tile and Settings agree.
func TestDaysLeftDateIsCalendarDaysInUTC(t *testing.T) {
	for _, at := range []string{"2026-11-01T00:00:00Z", "2026-11-01T10:00:00Z", "2026-11-01T23:59:59Z", "2026-11-01T22:00:00-01:00"} {
		now, err := time.Parse(time.RFC3339, at)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := daysLeft("2027-03-14", now); !ok || got != 133 {
			t.Errorf("at %s: %v %v, want 133", at, got, ok)
		}
	}
}

// An internationalized host goes to DNS, SNI and verification as its
// A-label; one that will not convert is a failed read.
func TestCertInternationalizedHost(t *testing.T) {
	var sni atomic.Value
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.TLS = &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		sni.Store(h.ServerName)
		return nil, nil
	}}
	srv.Config.ErrorLog = discardLog()
	srv.StartTLS()
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	g := &guard{resolver: fakeResolver{"xn--bcher-kva.de": {"127.0.0.1"}}, allowLoopback: true}
	ctx := context.Background()
	st := checkCert(ctx, g, nil, "https://bücher.de:"+port, time.Now())
	if st.ExpiresAt == "" {
		t.Fatalf("got %+v, want the certificate read", st)
	}
	if got := sni.Load(); got != "xn--bcher-kva.de" {
		t.Errorf("SNI %v, want the A-label", got)
	}
	if !strings.Contains(st.Error, "xn--bcher-kva.de") { // the test certificate is for example.com
		t.Errorf("verification error %q, want it to name the A-label", st.Error)
	}
	st = checkCert(ctx, g, nil, "https://bü_cher.de:"+port, time.Now())
	if st.ExpiresAt != "" || st.Valid || st.Error == "" {
		t.Errorf("unconvertible host: got %+v, want a failed read", st)
	}
}
