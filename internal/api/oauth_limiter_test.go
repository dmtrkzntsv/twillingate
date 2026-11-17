package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/shared/clientip"
	"github.com/golang-jwt/jwt/v5"
)

// submitFrom posts the login form from a connection at remote (an address,
// the port is added), with headers as name, value pairs and any cookies.
func (f *loginFixture) submitFrom(remote, request, password string, cookies []*http.Cookie, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/oauth/authorize",
		strings.NewReader(url.Values{"request": {request}, "password": {password}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = remote + ":4000"
	if strings.Contains(remote, ":") {
		req.RemoteAddr = "[" + remote + "]:4000"
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return f.serve(req)
}

func (f *loginFixture) request() string {
	f.t.Helper()
	return f.loginForm(authorizeQuery(f.register(claudeCallback), claudeCallback))
}

// fails submits n wrong passwords from remote and requires each to be
// answered as one (401), not refused.
func (f *loginFixture) fails(remote, request string, n int) {
	f.t.Helper()
	for i := range n {
		if rec := f.submitFrom(remote, request, "guess", nil); rec.Code != http.StatusUnauthorized {
			f.t.Fatalf("%s failure %d: %d, want 401", remote, i+1, rec.Code)
		}
	}
}

// A source that spends its minute is refused, the right password included
// and unchecked, until the minute ends, and no one else is: the old
// limiter's five failures locked the page for everyone.
func TestLoginLimiterPerSource(t *testing.T) {
	f := newLoginFixture(t, nil)
	request := f.request()
	f.fails("203.0.113.1", request, sourceFailuresPerMinute)
	rec := f.submitFrom("203.0.113.1", request, testPassword, nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("right password from a locked source: %d (Retry-After %q), want 429 without checking it",
			rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := f.submitFrom("203.0.113.2", request, testPassword, nil); rec.Code != http.StatusFound {
		t.Errorf("another source while the first is locked: %d, want 302", rec.Code)
	}
	f.now = f.now.Add(time.Minute)
	if rec := f.submitFrom("203.0.113.1", request, testPassword, nil); rec.Code != http.StatusFound {
		t.Errorf("next minute: %d, want 302", rec.Code)
	}
	// The login forgave the source its failures.
	f.fails("203.0.113.1", request, sourceFailuresPerMinute)
}

// The page names the budget that is spent, not the time left: an hour's
// budget spent in its last minute still says to wait for the hour.
func TestLoginLimiterNamesTheSpentBudget(t *testing.T) {
	f := newLoginFixture(t, nil)
	const rounds = sourceFailuresPerHour / sourceFailuresPerMinute
	// The last round of failures 50 s before the hour ends.
	f.now = f.now.Truncate(time.Hour).Add(time.Hour - (rounds-1)*time.Minute - 50*time.Second)
	request := f.request()
	for i := range rounds {
		if i > 0 {
			f.now = f.now.Add(time.Minute)
		}
		f.fails("203.0.113.1", request, sourceFailuresPerMinute)
	}
	f.now = f.now.Add(20 * time.Second) // 30 s before the hour ends
	rec := f.submitFrom("203.0.113.1", request, testPassword, nil)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "an hour") {
		t.Errorf("hour spent in its last minute: %d %s", rec.Code, rec.Body)
	}
}

// A right password counted in the shared overflow entry is taken back from
// it: every new source counts there.
func TestLoginLimiterSucceededInOverflow(t *testing.T) {
	var l loginLimiter
	now := loginEpoch
	for i := range maxLimiterSources {
		l.fail(now, fmt.Sprintf("s%d", i), true)
	}
	for range 2 * sourceFailuresPerMinute {
		a, wait, _ := l.admit(now, "new", false)
		if wait != 0 {
			t.Fatal("a right password in the overflow entry still counts")
		}
		// The table shrinks meanwhile: the count is still taken back from
		// the entry it went to.
		delete(l.sources, "s0")
		l.succeeded(now, "new", a)
		l.sources["s0"] = &sourceFailures{}
	}
}

// A patient guesser is held to sourceFailuresPerHour.
func TestLoginLimiterPerSourceHour(t *testing.T) {
	f := newLoginFixture(t, nil)
	f.now = f.now.Truncate(time.Hour)
	request := f.request()
	for range sourceFailuresPerHour / sourceFailuresPerMinute {
		f.fails("203.0.113.1", request, sourceFailuresPerMinute)
		f.now = f.now.Add(time.Minute)
	}
	rec := f.submitFrom("203.0.113.1", request, testPassword, nil)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "an hour") {
		t.Errorf("after the hour's failures: %d %s", rec.Code, rec.Body)
	}
	f.now = f.now.Truncate(time.Hour).Add(time.Hour)
	if rec := f.submitFrom("203.0.113.1", f.request(), testPassword, nil); rec.Code != http.StatusFound {
		t.Errorf("next hour: %d, want 302", rec.Code)
	}
}

// Many addresses spending the strangers' budget stop a browser that has
// never logged in, and not one that has: it carries the device cookie.
func TestLoginLimiterStrangersAndDeviceCookie(t *testing.T) {
	f := newLoginFixture(t, nil)
	request := f.request()

	// The owner logs in once, before the flood, and keeps the cookie.
	rec := f.submitFrom("198.51.100.1", request, testPassword, nil)
	cookies := rec.Result().Cookies()
	if rec.Code != http.StatusFound || len(cookies) != 1 {
		t.Fatalf("login: %d with %d cookies", rec.Code, len(cookies))
	}
	c := cookies[0]
	if c.Name != deviceCookie || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode ||
		c.Path != "/oauth/authorize" || c.MaxAge != int(deviceTTL/time.Second) {
		t.Errorf("device cookie = %+v", c)
	}

	for i := range strangerFailuresPerMinute / sourceFailuresPerMinute {
		f.fails(fmt.Sprintf("203.0.113.%d", i+1), request, sourceFailuresPerMinute)
	}
	if rec := f.submitFrom("192.0.2.200", request, testPassword, nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("stranger during the flood: %d, want 429", rec.Code)
	}
	// The owner, from an address the limiter has never seen.
	if rec := f.submitFrom("192.0.2.201", request, testPassword, cookies); rec.Code != http.StatusFound {
		t.Errorf("known device during the flood: %d, want 302", rec.Code)
	}
	// The cookie lifts the shared budget only: its source's own still holds.
	for i := range sourceFailuresPerMinute {
		if rec := f.submitFrom("192.0.2.202", request, "guess", cookies); rec.Code != http.StatusUnauthorized {
			t.Fatalf("known device failure %d: %d", i+1, rec.Code)
		}
	}
	if rec := f.submitFrom("192.0.2.202", request, testPassword, cookies); rec.Code != http.StatusTooManyRequests {
		t.Errorf("known device past its source's budget: %d, want 429", rec.Code)
	}
	f.now = f.now.Add(time.Minute)
	if rec := f.submitFrom("192.0.2.200", request, testPassword, nil); rec.Code != http.StatusFound {
		t.Errorf("stranger after the flood: %d, want 302", rec.Code)
	}
}

// Only a cookie this server signed, as a device cookie, under the current
// secrets and not yet expired, counts.
func TestKnownDevice(t *testing.T) {
	f := newLoginFixture(t, nil)
	other := newLoginFixture(t, func(m *config.ConsoleConfig) { m.Password = "another password" })
	exp := jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(f.now.Add(time.Hour))}
	for name, tc := range map[string]struct {
		value string
		want  bool
	}{
		"signed here":       {f.s.keys.sign(kindDevice, exp), true},
		"another kind":      {f.s.keys.sign(kindAccess, exp), false},
		"another password":  {other.s.keys.sign(kindDevice, exp), false},
		"expired":           {f.s.keys.sign(kindDevice, jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(f.now.Add(-time.Second))}), false},
		"no expiry":         {f.s.keys.sign(kindDevice, jwt.RegisteredClaims{}), false},
		"not a token":       {"1", false},
		"no cookie (empty)": {"", false},
	} {
		req := httptest.NewRequest("POST", "/oauth/authorize", nil)
		if tc.value != "" {
			req.AddCookie(&http.Cookie{Name: deviceCookie, Value: tc.value})
		}
		if got := f.s.knownDevice(req); got != tc.want {
			t.Errorf("%s: knownDevice = %v, want %v", name, got, tc.want)
		}
	}
}

// The source is the client's address as clientip reads it: a header typed
// by a direct client buys no new budget, a trusted proxy's tells its
// clients apart, and an IPv6 subscriber is one source whatever address of
// its /64 it uses.
func TestLoginSource(t *testing.T) {
	f := newLoginFixture(t, func(m *config.ConsoleConfig) { m.TrustedProxies = clientip.Local() })
	request := f.request()

	for i := range sourceFailuresPerMinute {
		rec := f.submitFrom("203.0.113.1", request, "guess", nil, "X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i+1, rec.Code)
		}
	}
	if rec := f.submitFrom("203.0.113.1", request, testPassword, nil, "X-Forwarded-For", "198.51.100.99"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("direct client with a new X-Forwarded-For: %d, want 429", rec.Code)
	}

	for range sourceFailuresPerMinute {
		f.submitFrom("127.0.0.1", request, "guess", nil, "X-Forwarded-For", "198.51.100.7")
	}
	if rec := f.submitFrom("127.0.0.1", request, testPassword, nil, "X-Forwarded-For", "198.51.100.7"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("locked client behind the proxy: %d, want 429", rec.Code)
	}
	if rec := f.submitFrom("127.0.0.1", request, testPassword, nil, "X-Forwarded-For", "198.51.100.8"); rec.Code != http.StatusFound {
		t.Errorf("another client behind the same proxy: %d, want 302", rec.Code)
	}

	for i := range sourceFailuresPerMinute {
		f.submitFrom(fmt.Sprintf("2001:db8:1:2::%d", i+1), request, "guess", nil)
	}
	if rec := f.submitFrom("2001:db8:1:2:ffff::1", request, testPassword, nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("same /64, new address: %d, want 429", rec.Code)
	}
	if rec := f.submitFrom("2001:db8:1:3::1", request, testPassword, nil); rec.Code != http.StatusFound {
		t.Errorf("another /64: %d, want 302", rec.Code)
	}
}

// A flood of addresses cannot grow the table without bound: past
// maxLimiterSources new ones share an entry, until the hour's expire. A
// browser with a device cookie is never held to that entry.
func TestLoginLimiterBounded(t *testing.T) {
	var l loginLimiter
	now := loginEpoch.Truncate(time.Hour)
	for i := range maxLimiterSources + 50 {
		l.fail(now, fmt.Sprintf("s%d", i), false)
	}
	if len(l.sources) != maxLimiterSources+1 {
		t.Fatalf("%d entries, want %d and the shared one", len(l.sources), maxLimiterSources)
	}
	// The flood spent the strangers' budget too; set it aside to see the
	// entries' own.
	l.strangers = failureWindow{}
	if l.retryAfter(now, "never seen", false) == 0 {
		t.Error("a new source past the bound is not held to the shared entry")
	}
	if l.retryAfter(now, "s0", false) != 0 {
		t.Error("a source with an entry of its own is held to the shared one")
	}
	if l.retryAfter(now, "owner", true) != 0 {
		t.Error("a known device past the bound is held to the shared entry")
	}
	now = now.Add(time.Hour)
	l.fail(now, "next hour", false)
	if len(l.sources) != 1 {
		t.Errorf("%d entries after the hour, want 1", len(l.sources))
	}
}

// Checking and counting are one step: a burst of concurrent guesses from
// one source gets sourceFailuresPerMinute through, not every one that was
// checked before the first was counted.
func TestLoginLimiterAdmitIsAtomic(t *testing.T) {
	var l loginLimiter
	now := loginEpoch
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, wait, _ := l.admit(now, "203.0.113.1", false); wait == 0 {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := admitted.Load(); n != sourceFailuresPerMinute {
		t.Errorf("%d of 100 concurrent guesses admitted, want %d", n, sourceFailuresPerMinute)
	}
}

// What admit counted is taken back when no password was compared, and
// when the right one was: neither is a guess.
func TestLoginLimiterRefunds(t *testing.T) {
	var l loginLimiter
	now := loginEpoch
	for range 2 * sourceFailuresPerMinute {
		a, wait, _ := l.admit(now, "a", false)
		if wait != 0 {
			t.Fatal("a refunded attempt still counts")
		}
		l.refund(now, a)
	}
	for i := range strangerFailuresPerMinute {
		src := fmt.Sprintf("s%d", i)
		a, wait, _ := l.admit(now, src, false)
		if wait != 0 {
			t.Fatal("a right password still spends the strangers' budget")
		}
		l.succeeded(now, src, a)
	}
	if n := l.strangers.n; n != 0 {
		t.Errorf("strangers' count %d after refunds, want 0", n)
	}
}

// Behind a local proxy that hands on what the client typed, a guesser
// cannot spend the owner's budget by naming the owner's address in
// CF-Connecting-IP: the source is the X-Forwarded-For entry the proxy
// added, the guesser's own.
func TestLoginSourceIgnoresCFConnectingIP(t *testing.T) {
	f := newLoginFixture(t, func(m *config.ConsoleConfig) { m.TrustedProxies = clientip.Local() })
	request := f.request()
	const owner, guesser = "198.51.100.50", "203.0.113.66"
	cookies := f.submitFrom("127.0.0.1", request, testPassword, nil, "X-Forwarded-For", owner).Result().Cookies()
	for i := range sourceFailuresPerMinute {
		rec := f.submitFrom("127.0.0.1", request, "guess", nil, "X-Forwarded-For", guesser, "CF-Connecting-IP", owner)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i+1, rec.Code)
		}
	}
	if rec := f.submitFrom("127.0.0.1", request, testPassword, cookies, "X-Forwarded-For", owner); rec.Code != http.StatusFound {
		t.Errorf("owner after a guesser named its address: %d, want 302", rec.Code)
	}
	if rec := f.submitFrom("127.0.0.1", request, testPassword, nil, "X-Forwarded-For", guesser, "CF-Connecting-IP", "192.0.2.9"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("guesser with a new CF-Connecting-IP: %d, want 429", rec.Code)
	}
}
