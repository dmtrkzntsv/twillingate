package maindomain

import "testing"

func TestOf(t *testing.T) {
	for host, want := range map[string]string{
		"example.com":       "example.com",
		"shop.example.com":  "example.com",
		"a.b.example.co.uk": "example.co.uk",
		"SHOP.Example.COM.": "example.com",
		"bücher.example.de": "example.de",
		"bücher.de":         "xn--bcher-kva.de",
		"me.github.io":      "me.github.io",
		"a.me.github.io":    "me.github.io",
		"127.0.0.1":         "127.0.0.1",
		"::1":               "::1",
		"localhost":         "localhost",
		"co.uk":             "co.uk",
	} {
		if got := Of(host); got != want {
			t.Errorf("Of(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestOfOrigin(t *testing.T) {
	for origin, want := range map[string]string{
		"https://shop.example.com":    "example.com",
		"http://www.example.com:8080": "example.com",
		"https://[2001:db8::1]:8443":  "2001:db8::1",
		"":                            "",
		"not a url":                   "",
		"://bad":                      "",
	} {
		if got := OfOrigin(origin); got != want {
			t.Errorf("OfOrigin(%q) = %q, want %q", origin, got, want)
		}
	}
}
