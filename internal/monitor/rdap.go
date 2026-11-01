package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

const (
	ianaBootstrap = "https://data.iana.org/rdap/dns.json"
	bootstrapTTL  = 24 * time.Hour
	rdapLimit     = 4 << 20 // bytes read of a bootstrap or domain reply
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
}

// expiry is host's registrable domain's expiry date (YYYY-MM-DD, UTC). An
// error means unknown: no RDAP for the TLD, no expiration in the reply,
// or a failure on the way.
func (r *rdap) expiry(ctx context.Context, host string) (string, error) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if _, err := netip.ParseAddr(host); err == nil {
		return "", errors.New("an address has no domain")
	}
	name, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return "", err
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
		return "", errors.Join(errs...)
	}
	for _, e := range reply.Events {
		if e.Action != "expiration" {
			continue
		}
		t, err := time.Parse(time.RFC3339, e.Date)
		if err != nil {
			return "", fmt.Errorf("expiration date %q: %w", e.Date, err)
		}
		return t.UTC().Format(time.DateOnly), nil
	}
	return "", fmt.Errorf("%s: no expiration in the RDAP reply", name)
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
	return nil, fmt.Errorf("%s: no RDAP server for its TLD", name)
}

// bootstrap is the IANA table, cached for bootstrapTTL. A failed fetch is
// not cached, so the next lookup tries again.
func (r *rdap) bootstrap(ctx context.Context) (map[string][]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.servers != nil && r.now().Sub(r.fetched) < bootstrapTTL {
		return r.servers, nil
	}
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
	r.servers, r.fetched = servers, r.now()
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
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, rdapLimit)).Decode(v)
}
