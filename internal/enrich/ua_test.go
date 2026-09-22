package enrich

import (
	"strings"
	"testing"
)

func TestNormalizeOS(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"ios": {"ios", true}, "iOS": {"ios", true}, " macOS ": {"macos", true},
		"Chrome OS": {"chromeos", true}, "chrome-os": {"chromeos", true},
		"HarmonyOS": {"harmonyos", true}, "PlayStation": {"playstation", true},
		"other":   {"other", true},   // sent deliberately: legitimate, never warned
		"unknown": {"unknown", true}, // same
		"":        {"unknown", true}, // absent: nothing to validate
		"   ":     {"unknown", true},
		"haiku":   {"other", false}, // present but outside the vocabulary
		"iOS 17":  {"other", false},
		"ubuntu":  {"other", false}, // distributions are not OSes
	}
	for in, c := range cases {
		got, ok := NormalizeOS(in)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizeOS(%q) = (%q, %v), want (%q, %v)", in, got, ok, c.want, c.ok)
		}
	}
}

func TestNormalizeBrowser(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"Chrome": {"chrome", true}, "safari": {"safari", true}, "Edge": {"edge", true},
		"Samsung Internet": {"samsung_internet", true}, "samsung-internet": {"samsung_internet", true},
		"brave": {"brave", true}, "DuckDuckGo": {"duckduckgo", true},
		"other": {"other", true}, "": {"unknown", true},
		"netscape": {"other", false}, "Chrome 126": {"other", false},
	}
	for in, c := range cases {
		got, ok := NormalizeBrowser(in)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizeBrowser(%q) = (%q, %v), want (%q, %v)", in, got, ok, c.want, c.ok)
		}
	}
}

func TestNormalizeDevice(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"desktop": {"desktop", true}, "Mobile": {"mobile", true}, "tablet": {"tablet", true},
		"wearable": {"wearable", true}, "XR": {"xr", true},
		"other": {"other", true}, "": {"unknown", true},
		"phablet": {"other", false}, "tv": {"other", false},
	}
	for in, c := range cases {
		got, ok := NormalizeDevice(in)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizeDevice(%q) = (%q, %v), want (%q, %v)", in, got, ok, c.want, c.ok)
		}
	}
}

// $platform is open but bounded: the same shape as $kind, applied after
// lower-casing so a client sending iOS records ios rather than being
// dropped. There is no other: every well-formed token is a value, so the
// only failure is "no usable value", which unknown says.
func TestNormalizePlatform(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"web": {"web", true}, "iOS": {"ios", true}, " Electron ": {"electron", true},
		"quest_2": {"quest_2", true}, "unknown": {"unknown", true},
		"":              {"unknown", true},
		"Bad Platform!": {"unknown", false}, "9lives": {"unknown", false},
		"averyveryverylongplatformname": {"unknown", false},
	}
	for in, c := range cases {
		got, ok := NormalizePlatform(in)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizePlatform(%q) = (%q, %v), want (%q, %v)", in, got, ok, c.want, c.ok)
		}
	}
}

// Every vocabulary is lower-case, closed under its own validator, and ends
// with the two floors — other and unknown are different answers and both
// must be sendable.
func TestVocabulariesAreLowerCaseAndClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		values    []string
		normalize func(string) (string, bool)
	}{
		"os":      {OSValues, NormalizeOS},
		"browser": {BrowserValues, NormalizeBrowser},
		"device":  {DeviceValues, NormalizeDevice},
	} {
		if n := len(tc.values); n < 2 || tc.values[n-2] != "other" || tc.values[n-1] != "unknown" {
			t.Errorf("%s: vocabulary must end with other, unknown: %v", name, tc.values)
		}
		for _, v := range tc.values {
			if v != strings.ToLower(v) || strings.ContainsAny(v, " -") {
				t.Errorf("%s: %q is not a lower-case token", name, v)
			}
			if got, ok := tc.normalize(v); got != v || !ok {
				t.Errorf("%s: %q does not round-trip: (%q, %v)", name, v, got, ok)
			}
		}
	}
}

func TestIsBot(t *testing.T) {
	bots := []string{
		"", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"Mozilla/5.0 (compatible; bingbot/2.0)", "curl/8.5.0", "Wget/1.21",
		"python-requests/2.32", "Mozilla/5.0 (X11; Linux x86_64) HeadlessChrome/126.0.0.0",
		"Screaming Frog SEO Spider/19.0", "facebookexternalhit/1.1",
	}
	for _, ua := range bots {
		if !IsBot(ua) {
			t.Errorf("IsBot(%q) = false, want true", ua)
		}
	}
	humans := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
	}
	for _, ua := range humans {
		if IsBot(ua) {
			t.Errorf("IsBot(%q) = true, want false", ua)
		}
	}
}
