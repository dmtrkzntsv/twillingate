// Package enrich keeps the two things the server still derives from a
// request — whether the User-Agent belongs to a bot, and a referrer's
// source — and validates the environment a client declares about itself.
// The User-Agent is no longer a source of OS, browser or device class:
// the client detects those (sdk/src/detect.ts) and the server only checks
// a declared value against a closed vocabulary.
package enrich

import (
	"regexp"
	"strings"
)

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

// Vocabularies. A value earns a place when it is a distinct product target
// — something a team would ship, test or drop support for separately —
// and is either detectable from a browser or declarable by a native
// client. Each list ends with the two floors, which must stay distinct:
// other means a real value outside the list, unknown means no information
// at all. Collapsing them would make a server-relayed event
// indistinguishable from a genuine FreeBSD.
var (
	OSValues = []string{
		"windows", "macos", "linux", "bsd", "chromeos",
		"ios", "ipados", "android", "fireos", "harmonyos", "kaios",
		"tvos", "watchos", "visionos", "tizen", "webos",
		"playstation", "xbox", "nintendo",
		"other", "unknown",
	}
	BrowserValues = []string{
		"chrome", "safari", "firefox", "edge", "opera", "samsung_internet",
		"brave", "vivaldi", "duckduckgo", "yandex",
		"other", "unknown",
	}
	DeviceValues = []string{
		"desktop", "mobile", "tablet", "wearable", "xr",
		"other", "unknown",
	}
)

var (
	osSet      = set(OSValues)
	browserSet = set(BrowserValues)
	deviceSet  = set(DeviceValues)
)

func set(values []string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

// NormalizeOS validates a declared $os against OSValues. Absent stores
// unknown; present but unrecognised stores other and reports ok=false so
// the caller can warn and preserve the raw value in os_name. A client
// sending other itself is recognised (ok=true): that is a legitimate
// value, not a mistake to warn about.
func NormalizeOS(v string) (string, bool) { return closed(v, osSet, "") }

// NormalizeBrowser is NormalizeOS for $browser. Spaces and dashes fold to
// underscores so "Samsung Internet" is samsung_internet.
func NormalizeBrowser(v string) (string, bool) { return closed(v, browserSet, "_") }

// NormalizeDevice is NormalizeOS for $device.
func NormalizeDevice(v string) (string, bool) { return closed(v, deviceSet, "") }

// platformPattern bounds a declared $platform to the same shape as $kind.
var platformPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)

// NormalizePlatform validates a declared $platform: trim, lower-case, then
// the same shape as $kind. The vocabulary is open, so every well-formed
// token is accepted as sent; the only failure is "no usable value", which
// unknown says. Absent is unknown too, and is not a mistake to warn about.
func NormalizePlatform(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "unknown", true
	}
	if platformPattern.MatchString(v) {
		return v, true
	}
	return "unknown", false
}

// closed trims, lower-cases, replaces spaces and dashes with sep ("Chrome
// OS" → chromeos, "Samsung Internet" → samsung_internet), then matches
// vocab. It never rejects: an unrecognised value is stored as other so a
// client shipping a value this server has not learned yet is not handed
// a 4xx, which the retry rules classify as a poison batch to drop.
func closed(v string, vocab map[string]bool, sep string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "unknown", true
	}
	folded := strings.NewReplacer(" ", sep, "-", sep).Replace(strings.ToLower(v))
	if vocab[folded] {
		return folded, true
	}
	return "other", false
}
