package monitor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRDAP serves an IANA-shaped bootstrap that maps "com" and "co.uk"
// to itself, and one domain record per name.
func fakeRDAP(t *testing.T, domains map[string]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var boots atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/dns.json":
			boots.Add(1)
			fmt.Fprintf(w, `{"version":"1.0","services":[[["net","com"],["ftp://ignored.invalid/","%s/rdap"]],[["uk"],["%s/rdap/"]]]}`,
				srv.URL, srv.URL)
		case len(r.URL.Path) > len("/rdap/domain/"):
			name := r.URL.Path[len("/rdap/domain/"):]
			body, ok := domains[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/rdap+json")
			fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &boots
}

func newTestRDAP(t *testing.T, srv *httptest.Server, now func() time.Time) *rdap {
	t.Helper()
	return &rdap{client: loopbackClient(t), bootstrapURL: srv.URL + "/dns.json", now: now}
}

func TestRDAPExpiry(t *testing.T) {
	srv, boots := fakeRDAP(t, map[string]string{
		"example.com": `{"objectClassName":"domain","events":[
			{"eventAction":"registration","eventDate":"2001-01-01T00:00:00Z"},
			{"eventAction":"expiration","eventDate":"2027-03-14T04:00:00Z"}]}`,
		"example.co.uk": `{"events":[{"eventAction":"expiration","eventDate":"2028-01-02T23:30:00-02:00"}]}`,
	})
	clock := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	r := newTestRDAP(t, srv, func() time.Time { return clock })
	ctx := context.Background()
	for _, tc := range []struct{ host, want string }{
		{"shop.example.com", "2027-03-14"},
		{"example.com", "2027-03-14"},
		{"www.shop.example.co.uk", "2028-01-03"},
	} {
		got, err := r.expiry(ctx, tc.host)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q %v, want %q", tc.host, got, err, tc.want)
		}
	}
	if n := boots.Load(); n != 1 {
		t.Errorf("bootstrap fetched %d times, want 1 (cached)", n)
	}
	clock = clock.Add(25 * time.Hour)
	if _, err := r.expiry(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	if n := boots.Load(); n != 2 {
		t.Errorf("bootstrap fetched %d times after a day, want 2", n)
	}
}

func TestRDAPUnknownTLD(t *testing.T) {
	srv, _ := fakeRDAP(t, map[string]string{
		"noexpiry.com": `{"events":[{"eventAction":"registration","eventDate":"2001-01-01T00:00:00Z"}]}`,
	})
	r := newTestRDAP(t, srv, time.Now)
	ctx := context.Background()
	for _, host := range []string{"example.zz", "noexpiry.com", "missing.com", "127.0.0.1", "localhost",
		"x.github.io", "example.org"} {
		if got, err := r.expiry(ctx, host); got != "" || !errors.Is(err, errUnknown) {
			t.Errorf("%s: got %q %v, want unknown with a reason", host, got, err)
		}
	}
}

func TestRDAPFailureIsNotUnknown(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer down.Close()
	garbled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dns.json" {
			fmt.Fprintf(w, `{"services":[[["com"],["http://%s/"]]]}`, r.Host)
			return
		}
		fmt.Fprint(w, "<html>not json")
	}))
	defer garbled.Close()
	for name, boot := range map[string]string{"bootstrap down": down.URL + "/dns.json", "reply garbled": garbled.URL + "/dns.json"} {
		r := &rdap{client: loopbackClient(t), bootstrapURL: boot, now: time.Now}
		if got, err := r.expiry(context.Background(), "example.com"); got != "" || err == nil || errors.Is(err, errUnknown) {
			t.Errorf("%s: got %q %v, want a lookup failure", name, got, err)
		}
	}
}

func TestParseEventDate(t *testing.T) {
	for in, want := range map[string]string{
		"2027-03-14T04:00:00Z":      "2027-03-14T04:00:00Z",
		"2027-03-14T04:00:00.5Z":    "2027-03-14T04:00:00Z",
		"2027-03-14T23:00:00-02:00": "2027-03-15T01:00:00Z",
		"2027-03-14T23:00:00":       "2027-03-14T23:00:00Z",
		"2027-03-14T23:00:00.123":   "2027-03-14T23:00:00Z",
		"2027-03-14 23:00:00":       "2027-03-14T23:00:00Z",
		"2027-03-14":                "2027-03-14T00:00:00Z",
	} {
		got, err := parseEventDate(in)
		if err != nil || got.UTC().Truncate(time.Second).Format(time.RFC3339) != want {
			t.Errorf("%q: got %v %v, want %s", in, got, err, want)
		}
	}
	if _, err := parseEventDate("soon"); err == nil {
		t.Error("garbage parsed")
	}
}

func TestRDAPBootstrapFailureNotCached(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	srv, boots := fakeRDAP(t, map[string]string{"example.com": `{"events":[{"eventAction":"expiration","eventDate":"2027-03-14T04:00:00Z"}]}`})
	wrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "down", 503)
			return
		}
		http.Redirect(w, r, srv.URL+"/dns.json", http.StatusFound)
	}))
	defer wrap.Close()
	r := &rdap{client: loopbackClient(t), bootstrapURL: wrap.URL, now: time.Now}
	if _, err := r.expiry(context.Background(), "example.com"); err == nil {
		t.Fatal("want an error while the bootstrap is down")
	}
	fail.Store(false)
	if got, err := r.expiry(context.Background(), "example.com"); got != "2027-03-14" || err != nil {
		t.Fatalf("after recovery: %q %v", got, err)
	}
	if boots.Load() != 1 {
		t.Errorf("bootstrap fetched %d times", boots.Load())
	}
}
