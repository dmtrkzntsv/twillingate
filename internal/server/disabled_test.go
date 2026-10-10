package server

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/geo"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

// unreadBody fails the test if the handler reads it: a disabled server
// refuses before it parses anything.
type unreadBody struct {
	t    *testing.T
	read bool
}

func (b *unreadBody) Read([]byte) (int, error) {
	b.read = true
	return 0, errors.New("body read")
}

// disabledServer is a server over one project (allowed origin testOrigin,
// key testKey) run with INGEST_DISABLED set to value.
func disabledServer(t *testing.T, value string) (*fakeQueue, *fakeForms, *Server) {
	t.Helper()
	cfg := configtest.Load(t, map[string]string{"INGEST_DISABLED": value})
	reg := newTestRegistry(t,
		[]manage.ProjectSpec{{Name: "App", AllowedOrigins: []string{testOrigin}}},
		map[int][2]string{0: {testKey, "web"}})
	g, _ := geo.New("cloudflare://", t.TempDir(), slog.Default())
	q, forms := &fakeQueue{}, newFakeForms()
	return q, forms, New(cfg, reg, q, g, fixedSalt{}, q, forms, slog.Default())
}

// ingestRoutes are every route that takes events or form submissions.
var ingestRoutes = []string{"/ingest/events", "/api/events", "/ingest/forms/contact?key=" + testKey}

// Every ingest route refuses with 429, "ingest is disabled" and an hour's
// Retry-After, before reading the body or resolving the key, so nothing is
// enqueued or written; an allowed Origin still gets its CORS headers so the
// browser can read the answer.
func TestIngestDisabledRefusesEveryIngestRoute(t *testing.T) {
	for _, path := range ingestRoutes {
		t.Run(path, func(t *testing.T) {
			q, forms, h := disabledServer(t, "true")
			for _, tc := range []struct {
				name, origin, key string
				cors              bool
			}{
				{"allowed origin", testOrigin, testKey, true},
				{"origin no project allows", "https://evil.example", testKey, false},
				{"no origin (a native app)", "", testKey, false},
				{"unknown key", testOrigin, "ak_nope", true},
			} {
				body := &unreadBody{t: t}
				r := httptest.NewRequest("POST", path, body)
				r.Header.Set("User-Agent", chromeUA)
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-Analytics-Key", tc.key)
				if tc.origin != "" {
					r.Header.Set("Origin", tc.origin)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusTooManyRequests {
					t.Fatalf("%s: status = %d (%q), want 429", tc.name, w.Code, w.Body.String())
				}
				if got := strings.TrimSpace(w.Body.String()); got != "ingest is disabled" {
					t.Errorf("%s: body = %q", tc.name, got)
				}
				if got := w.Header().Get("Retry-After"); got != "3600" {
					t.Errorf("%s: Retry-After = %q, want 3600", tc.name, got)
				}
				acao := w.Header().Get("Access-Control-Allow-Origin")
				if tc.cors && (acao != tc.origin || w.Header().Get("Vary") != "Origin") {
					t.Errorf("%s: CORS = %q (Vary %q), want %s", tc.name, acao, w.Header().Get("Vary"), tc.origin)
				}
				if !tc.cors && acao != "" {
					t.Errorf("%s: CORS = %q, want none", tc.name, acao)
				}
				if body.read {
					t.Errorf("%s: the body was read", tc.name)
				}
			}
			if n := len(q.events) + len(q.views) + len(q.measures) + len(q.identities); n != 0 {
				t.Errorf("%d rows enqueued by a disabled server", n)
			}
			if len(forms.calls) != 0 {
				t.Errorf("%d submissions written by a disabled server", len(forms.calls))
			}
		})
	}
}

// Preflights, the SDK scripts and the health check answer as usual: a
// browser still learns it may post (and then reads the 429), and pages
// that load the script keep working.
func TestIngestDisabledServesTheRest(t *testing.T) {
	_, _, h := disabledServer(t, "1")
	for _, path := range ingestRoutes {
		r := httptest.NewRequest("OPTIONS", path, nil)
		r.Header.Set("Origin", testOrigin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != testOrigin {
			t.Errorf("OPTIONS %s = %d, CORS %q; want 204 with CORS", path, w.Code, w.Header().Get("Access-Control-Allow-Origin"))
		}
	}
	for _, path := range []string{"/js/twillingate.js", "/js/twillingate-vitals.js", "/js/plausible-shim.js", "/healthz"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, w.Code)
		}
	}
}

// Off (the default, or set false), ingest is unchanged.
func TestIngestEnabledWhenNotDisabled(t *testing.T) {
	for _, v := range []string{"", "false", "0"} {
		q, forms, h := disabledServer(t, v)
		if w := post(h, envelopeOf(`{"name":"signup"}`), nil); w.Code != http.StatusAccepted || len(q.events) != 1 {
			t.Errorf("INGEST_DISABLED=%q: events = %d with %d enqueued, want 202 with 1", v, w.Code, len(q.events))
		}
		if w := postJSON(h, "contact", `{"key":"`+testKey+`","fields":{"email":"a@b.c"}}`, nil); w.Code != http.StatusCreated || len(forms.calls) != 1 {
			t.Errorf("INGEST_DISABLED=%q: form = %d (%q) with %d written, want 201 with 1", v, w.Code, w.Body.String(), len(forms.calls))
		}
	}
}
