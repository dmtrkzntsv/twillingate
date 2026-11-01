// Package maindomain names the domain a host is registered under: the
// registrable domain of the public suffix list, in A-label form, so that
// shop.example.co.uk and example.co.uk are one name.
package maindomain

import (
	"net/netip"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// Of is host's main domain, lower case and without a trailing dot. A host
// with none (an address, a single label, a public suffix itself) is its own
// name, so every host has one.
func Of(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if _, err := netip.ParseAddr(host); err == nil {
		return host
	}
	if strings.ContainsFunc(host, func(r rune) bool { return r >= utf8.RuneSelf }) {
		a, err := idna.Lookup.ToASCII(host)
		if err != nil {
			return host
		}
		host = a
	}
	name, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return host
	}
	return name
}

// OfOrigin is the main domain of origin's host; "" when origin has no host.
func OfOrigin(origin string) string {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return Of(u.Hostname())
}
