package enrich

import "testing"

func TestParseUserAgent(t *testing.T) {
	cases := []struct{ ua, device, browser, version, os string }{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
			"desktop", "Chrome", "126", "Windows"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
			"desktop", "Safari", "17", "macOS"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0",
			"desktop", "Firefox", "127", "Linux"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
			"mobile", "Safari", "17", "iOS"},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36",
			"mobile", "Chrome", "126", "Android"},
		{"Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
			"tablet", "Safari", "17", "iOS"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0",
			"desktop", "Edge", "126", "Windows"},
		{"Mozilla/5.0 (Linux; Android 13; SM-G991B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Mobile Safari/537.36",
			"mobile", "Samsung Internet", "25", "Android"},
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 OPR/112.0.0.0",
			"desktop", "Opera", "112", "Linux"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/126.0.6478.54 Mobile/15E148 Safari/604.1",
			"mobile", "Chrome", "126", "iOS"},
		{"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
			"desktop", "Chrome", "126", "ChromeOS"},
		{"weird thing", "desktop", "", "", ""},
	}
	for _, c := range cases {
		d, b, v, o := ParseUserAgent(c.ua)
		if d != c.device || b != c.browser || v != c.version || o != c.os {
			t.Errorf("%q => (%s,%s,%s,%s), want (%s,%s,%s,%s)", c.ua, d, b, v, o, c.device, c.browser, c.version, c.os)
		}
	}
}

func TestNormalizeOS(t *testing.T) {
	cases := map[string]string{
		"ios": "iOS", "IOS": "iOS", "android": "Android", "macos": "macOS", "MacOS": "macOS",
		"windows": "Windows", "linux": "Linux", "chromeos": "ChromeOS",
		"iOS": "iOS", "": "", "haiku": "haiku", " ios ": "iOS",
	}
	for in, want := range cases {
		if got := NormalizeOS(in); got != want {
			t.Errorf("NormalizeOS(%q) = %q, want %q", in, got, want)
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
