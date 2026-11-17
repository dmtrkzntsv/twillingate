package api

import (
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// The login page's password limiter. It counts wrong passwords only, in
// three budgets, and refuses a submission before its password is compared
// once one of them is spent:
//
//   - per source, a minute: sourceFailuresPerMinute, so a typo costs
//     nothing and a guesser gets five tries;
//   - per source, an hour: sourceFailuresPerHour, which is what holds one
//     address to a few hundred guesses a day;
//   - every source without a device cookie together, a minute:
//     strangerFailuresPerMinute, which caps a guesser with many addresses.
//
// A source is the client's address (clientip, so behind a trusted proxy
// the forwarded one), an IPv6 one cut to its /64, the size of the block a
// single subscriber is handed.
//
// The one budget every caller shares is the lockout a stranger could use
// against the owner, so two things keep it from being one. It sits ten
// times above a source's own, out of reach of a single address. And it
// does not apply to a browser that has logged in before: a successful
// login leaves a device cookie, signed under keys derived from the
// password, and a submission that carries a valid one is held to its
// source's budgets alone. Flooding the page with wrong passwords from a
// thousand addresses therefore stops browsers that have never logged in,
// while it lasts, and no one else. Clients already connected hold tokens
// and never come here.
//
// The counts live in memory: a restart forgets them, as it forgets nothing
// else about a login.
const (
	sourceFailuresPerMinute   = 5
	sourceFailuresPerHour     = 20
	strangerFailuresPerMinute = 50
	// maxLimiterSources bounds the memory a flood of addresses can take.
	// Past it, new sources share one entry (and so one budget) until the
	// hour's entries expire; the stranger budget is long spent by then.
	maxLimiterSources = 10000

	deviceCookie = "twillingate_login_device"
	deviceTTL    = 90 * 24 * time.Hour
)

// failureWindow counts failures in fixed windows of size.
type failureWindow struct {
	start time.Time
	n     int
}

func (w *failureWindow) count(now time.Time, size time.Duration) int {
	if s := now.Truncate(size); !s.Equal(w.start) {
		w.start, w.n = s, 0
	}
	return w.n
}

func (w *failureWindow) add(now time.Time, size time.Duration) {
	w.count(now, size)
	w.n++
}

// take undoes one add of the current window; one from a window gone by
// has been forgotten already.
func (w *failureWindow) take(now time.Time, size time.Duration) {
	if w.count(now, size) > 0 {
		w.n--
	}
}

type sourceFailures struct{ minute, hour failureWindow }

type loginLimiter struct {
	mu        sync.Mutex
	sources   map[string]*sourceFailures
	strangers failureWindow
	swept     time.Time // the hour sweep last ran in
}

// admit decides whether a submission may go on to the password check,
// and if so counts it as a failure at once, under the same lock: checking
// and counting apart would let a burst of concurrent guesses all pass the
// check before any is counted. A right password takes the count back
// (succeeded), as does a submission that never reaches the comparison
// (refund). A refused caller gets how long to wait, and whether its
// source's hour is what is spent; 0 admits.
func (l *loginLimiter) admit(now time.Time, source string, known bool) (wait time.Duration, hourly bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if wait, hourly := l.wait(now, source, known); wait > 0 {
		return wait, hourly
	}
	l.record(now, source, known)
	return 0, false
}

// refund takes back the failure admit counted, for a submission that
// never reached the password check.
func (l *loginLimiter) refund(now time.Time, source string, known bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := l.sources[l.key(source, known)]; s != nil {
		s.minute.take(now, time.Minute)
		s.hour.take(now, time.Hour)
	}
	if !known {
		l.strangers.take(now, time.Minute)
	}
}

// succeeded takes back the failure admit counted and forgets the source's
// others: whoever typed the password right is not guessing it.
func (l *loginLimiter) succeeded(now time.Time, source string, known bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if k := l.key(source, known); k != source {
		// Counted in the shared overflow entry, which others still count in.
		if s := l.sources[k]; s != nil {
			s.minute.take(now, time.Minute)
			s.hour.take(now, time.Hour)
		}
	} else {
		delete(l.sources, source)
	}
	if !known {
		l.strangers.take(now, time.Minute)
	}
}

// retryAfter is how long a refused caller should wait: 0 means the
// submission may go on to the password check. It counts nothing (admit).
func (l *loginLimiter) retryAfter(now time.Time, source string, known bool) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	wait, _ := l.wait(now, source, known)
	return wait
}

// fail counts one wrong password.
func (l *loginLimiter) fail(now time.Time, source string, known bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.record(now, source, known)
}

// wait is retryAfter with mu held, and reports whether the source's hour
// is the budget spent.
func (l *loginLimiter) wait(now time.Time, source string, known bool) (time.Duration, bool) {
	if s := l.sources[l.key(source, known)]; s != nil {
		if s.hour.count(now, time.Hour) >= sourceFailuresPerHour {
			return now.Truncate(time.Hour).Add(time.Hour).Sub(now), true
		}
		if s.minute.count(now, time.Minute) >= sourceFailuresPerMinute {
			return now.Truncate(time.Minute).Add(time.Minute).Sub(now), false
		}
	}
	if !known && l.strangers.count(now, time.Minute) >= strangerFailuresPerMinute {
		return now.Truncate(time.Minute).Add(time.Minute).Sub(now), false
	}
	return 0, false
}

// record is fail with mu held.
func (l *loginLimiter) record(now time.Time, source string, known bool) {
	if l.sources == nil {
		l.sources = map[string]*sourceFailures{}
	}
	if _, ok := l.sources[source]; !ok && len(l.sources) >= maxLimiterSources {
		l.sweep(now)
	}
	k := l.key(source, known)
	s := l.sources[k]
	if s == nil {
		s = &sourceFailures{}
		l.sources[k] = s
	}
	s.minute.add(now, time.Minute)
	s.hour.add(now, time.Hour)
	if !known {
		l.strangers.add(now, time.Minute)
	}
}

// key is the entry a source counts in: its own, or the shared overflow
// entry once the table is full and it has none. A browser with a device
// cookie always gets its own, so a flood that fills the table cannot hold
// the owner to the overflow entry's budget; only the owner has one, so
// it adds few entries. Called with mu held.
func (l *loginLimiter) key(source string, known bool) string {
	if _, ok := l.sources[source]; !ok && !known && len(l.sources) >= maxLimiterSources {
		return ""
	}
	return source
}

// sweep drops the entries whose hour has passed, once an hour: none
// expires sooner. Called with mu held.
func (l *loginLimiter) sweep(now time.Time) {
	hour := now.Truncate(time.Hour)
	if hour.Equal(l.swept) {
		return
	}
	l.swept = hour
	for k, s := range l.sources {
		if !s.hour.start.Equal(hour) {
			delete(l.sources, k)
		}
	}
}

// loginSource names who is submitting a password, for the limiter: the
// client's address, an IPv6 one as its /64.
func (s *loginServer) loginSource(r *http.Request) string {
	ip := s.proxies.From(r)
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	if a.Is6() {
		if p, err := a.Prefix(64); err == nil {
			return p.String()
		}
	}
	return a.String()
}

// knownDevice reports whether the request carries a device cookie this
// server signed under the current secrets: proof that this browser has
// logged in before.
func (s *loginServer) knownDevice(r *http.Request) bool {
	c, err := r.Cookie(deviceCookie)
	if err != nil {
		return false
	}
	var claims jwt.RegisteredClaims
	return s.keys.parse(kindDevice, c.Value, &claims, s.now, jwt.WithExpirationRequired()) == nil
}

// rememberDevice leaves the device cookie after a successful login. It is
// sent only to the login page's own form post (SameSite=Strict, its path)
// and is no credential: it lifts the shared budget for this browser and
// nothing else.
func (s *loginServer) rememberDevice(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: deviceCookie,
		Value: s.keys.sign(kindDevice, jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(s.now().Add(deviceTTL))}),
		Path:     "/oauth/authorize",
		MaxAge:   int(deviceTTL / time.Second),
		HttpOnly: true,
		// Plain http only on an insecure=1 console, which has no https to
		// require.
		Secure:   r.TLS != nil || strings.HasPrefix(s.issuer, "https://"),
		SameSite: http.SameSiteStrictMode,
	})
}
