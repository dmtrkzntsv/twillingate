package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/version"
)

func TestTwillingateSDKServed(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("GET", "/js/twillingate.js", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("code = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("content-type = %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") {
		t.Errorf("cache-control = %q", cc)
	}
	body := w.Body.String()
	// The committed bundle carries __TWILLINGATE_VERSION__; the served copy
	// must have it substituted with the build version everywhere (banner
	// and twillingate.VERSION).
	if strings.Contains(body, "__TWILLINGATE_VERSION__") {
		t.Error("served bundle still contains the version placeholder")
	}
	if !strings.Contains(body, "twillingate.js "+version.Version) {
		t.Error("bundle banner does not carry the build version; rebuild with `npm run build` in sdk/")
	}
	// Behavioural markers the committed bundle must contain. The full
	// behaviour is covered by the vitest suite in sdk/; this guards against
	// an empty or stale artifact being embedded.
	for _, marker := range []string{
		"twillingate",            // the global
		"twillingate_ignore",     // opt-out
		"data-key",               // snippet-mode credential wiring
		"data-consent",           // storage consent wiring
		"data-instance",          // second tag on one page
		"sendBeacon",             // unload transport
		"pushState",              // SPA tracking
		"/ingest/events",         // the only endpoint
		"$page_view",             // web analytics
		"$screen_view",           // app analytics
		"$install_id",            // app/identity batch attribute
		"twillingate_debug",      // debug flag
		"data-twillingate-event", // tagged elements
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("twillingate.js missing %q", marker)
		}
	}
	if strings.Contains(body, "__TWILLINGATE_URL__") {
		t.Error("served bundle still contains the origin placeholder")
	}
}

// The collector bakes the origin the file was requested from into the
// served copy, so a collector answering on several hostnames serves each
// site a copy that posts back to the hostname that site used.
func TestTwillingateSDKCarriesRequestOrigin(t *testing.T) {
	_, h := testServer(t)
	serve := func(host, proto string) string {
		r := httptest.NewRequest("GET", "/js/twillingate.js", nil)
		r.Host = host
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Body.String()
	}
	for _, tc := range []struct{ host, proto, want string }{
		{"t.example.com", "https", `"https://t.example.com"`},
		{"t.example.org", "https", `"https://t.example.org"`},
		{"localhost:8080", "", `"http://localhost:8080"`},
	} {
		body := serve(tc.host, tc.proto)
		if !strings.Contains(body, tc.want) {
			t.Errorf("host %q proto %q: served bundle does not carry %s", tc.host, tc.proto, tc.want)
		}
		if strings.Contains(body, "__TWILLINGATE_URL__") {
			t.Errorf("host %q: placeholder left in the served bundle", tc.host)
		}
	}
	// A host that cannot be an origin is not written into the script.
	body := serve(`evil"host`, "https")
	if strings.Contains(body, `evil"host`) {
		t.Error("an unusable Host header reached the served script")
	}
	if !strings.Contains(body, "__TWILLINGATE_URL__") {
		t.Error("an unusable Host header should leave the placeholder, so the SDK stays dormant")
	}
}

// The legacy snippet is gone. A stale tag must 404 rather than serve a
// client whose every pageview the collector would reject.
func TestLegacyScriptRemoved(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("GET", "/js/script.js", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}
