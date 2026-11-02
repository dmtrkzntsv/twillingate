package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

const (
	ianaBootstrap = "https://data.iana.org/rdap/dns.json"
	bootstrapTTL  = 24 * time.Hour
	// bootstrapRetry is how long a failed bootstrap is kept, so a hung or
	// failing IANA does not cost every worker its deadline.
	bootstrapRetry = 5 * time.Minute
	rdapLimit      = 4 << 20 // bytes read of a bootstrap or domain reply
)

// rdap looks up a domain's registration expiry: the registrable domain's
// TLD finds its RDAP server in the IANA bootstrap (fetched at most once a
// day), whose /domain/<name> answer carries an "expiration" event.
type rdap struct {
	client       *http.Client // the guarded client
	bootstrapURL string
	now          func() time.Time

	mu      sync.Mutex
	servers map[string][]string // TLD (or suffix) → base URLs
	fetched time.Time
	failure error // the last fetch's, kept for bootstrapRetry
	failed  time.Time
}

// errUnknown marks an answer that the expiry cannot be known: an address
// host, a name outside the ICANN part of the public suffix list (a
// private suffix such as github.io), a TLD with no RDAP server, a domain
// the server does not know, or a reply without an expiration. Any other
// error is a failed lookup, which says nothing about the domain.
var errUnknown = errors.New("domain expiry unknown")

func unknown(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errUnknown, fmt.Sprintf(format, a...))
}

// expiry is host's registrable domain's expiry date (YYYY-MM-DD, UTC).
func (r *rdap) expiry(ctx context.Context, host string) (string, error) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if _, err := netip.ParseAddr(host); err == nil {
		return "", unknown("%s is an address", host)
	}
	host, err := asciiHost(host)
	if err != nil {
		return "", unknown("%v", err)
	}
	if _, icann := publicsuffix.PublicSuffix(host); !icann {
		return "", unknown("%s is under a private or unlisted suffix", host)
	}
	name, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return "", unknown("%v", err)
	}
	bases, err := r.lookup(ctx, name)
	if err != nil {
		return "", err
	}
	var reply struct {
		Events []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
	}
	var errs []error
	for _, b := range bases {
		if err = r.getJSON(ctx, b+"domain/"+name, &reply); err == nil {
			break
		}
		errs = append(errs, err)
	}
	if err != nil {
		// Not found only when every server said so: one that failed may know it.
		if !slices.ContainsFunc(errs, func(e error) bool { return !errors.Is(e, errNotFound) }) {
			return "", unknown("%s: not found over RDAP", name)
		}
		return "", errors.Join(errs...)
	}
	for _, e := range reply.Events {
		if e.Action != "expiration" {
			continue
		}
		t, err := parseEventDate(e.Date)
		if err != nil {
			return "", fmt.Errorf("expiration date %q: %w", e.Date, err)
		}
		return t.UTC().Format(time.DateOnly), nil
	}
	return "", unknown("%s: no expiration in the RDAP reply", name)
}

// lookup is the RDAP base URLs for name's longest listed suffix, https
// ones first, each ending in "/".
func (r *rdap) lookup(ctx context.Context, name string) ([]string, error) {
	servers, err := r.bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	for s := name; s != ""; {
		if b := servers[s]; len(b) > 0 {
			return b, nil
		}
		_, rest, ok := strings.Cut(s, ".")
		if !ok {
			break
		}
		s = rest
	}
	return nil, unknown("%s: no RDAP server for its TLD", name)
}

// bootstrap is the IANA table, cached for bootstrapTTL, a failure for
// bootstrapRetry. The fetch runs outside the lock: lookups that find nothing
// cached each fetch, and none waits on another's slow one.
func (r *rdap) bootstrap(ctx context.Context) (map[string][]string, error) {
	r.mu.Lock()
	now := r.now()
	servers, fetched, failure, failed := r.servers, r.fetched, r.failure, r.failed
	r.mu.Unlock()
	if servers != nil && now.Sub(fetched) < bootstrapTTL {
		return servers, nil
	}
	if failure != nil && now.Sub(failed) < bootstrapRetry {
		return nil, failure
	}
	servers, err := r.fetchBootstrap(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.failure, r.failed = err, r.now()
		return nil, err
	}
	r.servers, r.fetched, r.failure = servers, r.now(), nil
	return servers, nil
}

func (r *rdap) fetchBootstrap(ctx context.Context) (map[string][]string, error) {
	var doc struct {
		Services [][][]string `json:"services"`
	}
	if err := r.getJSON(ctx, r.bootstrapURL, &doc); err != nil {
		return nil, fmt.Errorf("rdap bootstrap: %w", err)
	}
	servers := map[string][]string{}
	for _, svc := range doc.Services {
		if len(svc) != 2 {
			continue
		}
		var https, other []string
		for _, u := range svc[1] {
			if !strings.HasSuffix(u, "/") {
				u += "/"
			}
			if strings.HasPrefix(u, "https://") {
				https = append(https, u)
			} else if strings.HasPrefix(u, "http://") {
				other = append(other, u)
			}
		}
		bases := append(https, other...)
		for _, tld := range svc[0] {
			servers[strings.ToLower(tld)] = bases
		}
	}
	return servers, nil
}

func (r *rdap) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("GET %s: %w", url, errNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, rdapLimit)).Decode(v)
}

var errNotFound = errors.New("HTTP 404")

// parseEventDate reads an RDAP eventDate: RFC 3339, or the same without a
// zone (or a bare date), read as UTC.
func parseEventDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	var err error
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", time.DateOnly} {
		var t time.Time
		if t, err = time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, err
}
