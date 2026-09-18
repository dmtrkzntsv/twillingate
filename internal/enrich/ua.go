// Package enrich derives coarse device/browser/os classes and cleans
// referrers/URLs at ingest. Only substring matching on the hot path
// (spec §12a) — order of checks matters and is documented inline.
package enrich

import "strings"

var botMarkers = []string{
	"bot", "crawler", "spider", "crawling", "headless", "lighthouse",
	"slurp", "curl/", "wget/", "python-requests", "facebookexternalhit", "preview",
}

func IsBot(ua string) bool {
	if ua == "" {
		return true
	}
	l := strings.ToLower(ua)
	for _, m := range botMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// ParseUserAgent derives coarse device class, browser name, browser major
// version and OS name. Only substring matching; order matters (see inline).
func ParseUserAgent(ua string) (device, browser, browserVersion, os string) {
	// OS first — some browser checks depend on it.
	switch {
	case strings.Contains(ua, "CrOS"):
		os = "ChromeOS"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		os = "iOS"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Mac OS X"):
		os = "macOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	// Browser: check derivatives before their bases (Edge/Samsung before
	// Chrome, Chrome before Safari — every Chrome UA contains "Safari").
	// The marker that named the browser is also where its version starts.
	var marker string
	switch {
	case strings.Contains(ua, "Edg/"):
		browser, marker = "Edge", "Edg/"
	case strings.Contains(ua, "Edge/"):
		browser, marker = "Edge", "Edge/"
	case strings.Contains(ua, "SamsungBrowser/"):
		browser, marker = "Samsung Internet", "SamsungBrowser/"
	case strings.Contains(ua, "OPR/"):
		browser, marker = "Opera", "OPR/"
	case strings.Contains(ua, "Opera/"):
		browser, marker = "Opera", "Opera/"
	case strings.Contains(ua, "Firefox/"):
		browser, marker = "Firefox", "Firefox/"
	case strings.Contains(ua, "Chrome/"):
		browser, marker = "Chrome", "Chrome/"
	case strings.Contains(ua, "CriOS/"):
		browser, marker = "Chrome", "CriOS/"
	case strings.Contains(ua, "Safari/"):
		// Safari carries its version under Version/, not Safari/ (that is
		// the WebKit build number).
		browser, marker = "Safari", "Version/"
	}
	browserVersion = majorAfter(ua, marker)
	switch {
	case strings.Contains(ua, "iPad"), strings.Contains(ua, "Tablet"):
		device = "tablet"
	case strings.Contains(ua, "Mobile"), strings.Contains(ua, "iPhone"):
		device = "mobile"
	default:
		device = "desktop"
	}
	return device, browser, browserVersion, os
}

// majorAfter returns the run of digits that follows marker, or "" when the
// marker is absent or not followed by a digit.
func majorAfter(ua, marker string) string {
	if marker == "" {
		return ""
	}
	i := strings.Index(ua, marker)
	if i < 0 {
		return ""
	}
	rest := ua[i+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	return rest[:end]
}

// osNames maps the lower-cased tokens apps declare in $os to the names the
// parser produces, so a declared "ios" and a parsed iPhone land in one row.
var osNames = map[string]string{
	"ios": "iOS", "android": "Android", "macos": "macOS",
	"windows": "Windows", "linux": "Linux", "chromeos": "ChromeOS",
}

// NormalizeOS folds a declared OS token into the parser's vocabulary. An
// unknown value is stored as sent: the vocabulary is a convenience, not an
// allowlist.
func NormalizeOS(v string) string {
	v = strings.TrimSpace(v)
	if name, ok := osNames[strings.ToLower(v)]; ok {
		return name
	}
	return v
}
