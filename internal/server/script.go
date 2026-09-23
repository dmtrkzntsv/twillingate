package server

import (
	"bytes"
	_ "embed"
	"net/http"
	"regexp"

	"github.com/dmtrkzntsv/twillingate/docs"
	"github.com/dmtrkzntsv/twillingate/internal/version"
)

// twillingate.js is the only served client, compiled from sdk/ (`npm run
// build` there rewrites the committed bundle).
//
//go:embed twillingate.js
var sdkScript []byte

// The committed bundle carries two placeholders so the artifact stays
// deterministic for CI's drift check. The version is substituted once at
// startup; the origin per request, with the origin the file was
// requested from, so a collector answering on several hostnames serves
// each site a copy that posts back to the hostname that site used. A
// bundle that keeps the origin placeholder (someone bundled the module)
// warns and stays dormant in the browser.
const (
	sdkVersionPlaceholder = "__TWILLINGATE_VERSION__"
	sdkOriginPlaceholder  = "__TWILLINGATE_URL__"
)

// The substituted origin lands inside a JS string literal. Only a plain
// host (with an optional port) is written; anything else leaves the
// placeholder in place, which the SDK treats as "no collector origin".
var originHost = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)

// requestOrigin is the scheme and host the client used to fetch the
// script: the proxy's forwarded scheme when there is one, else the
// connection's, and the Host header.
func requestOrigin(r *http.Request) string {
	if !originHost.MatchString(r.Host) {
		return ""
	}
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme != "https" && scheme != "http" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	return scheme + "://" + r.Host
}

func (s *Server) registerScript(mux *http.ServeMux) {
	headers := func(w http.ResponseWriter) {
		h := w.Header()
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		h.Set("Cache-Control", "public, max-age=86400")
	}
	versioned := bytes.ReplaceAll(sdkScript,
		[]byte(sdkVersionPlaceholder), []byte(version.Version))
	mux.HandleFunc("GET /js/twillingate.js", func(w http.ResponseWriter, r *http.Request) {
		headers(w)
		body := versioned
		if origin := requestOrigin(r); origin != "" {
			body = bytes.ReplaceAll(versioned, []byte(sdkOriginPlaceholder), []byte(origin))
		}
		w.Write(body)
	})
	// Helpers are served from the same table so a site loads them rather
	// than copying them into its own static assets. plausible-shim.js is
	// embedded from docs/, where the README documenting it lives.
	mux.HandleFunc("GET /js/plausible-shim.js", func(w http.ResponseWriter, _ *http.Request) {
		headers(w)
		w.Write(docs.PlausibleShim)
	})
}
