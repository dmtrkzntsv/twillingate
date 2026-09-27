package reporting

import (
	"bytes"
	"embed"
	"net/http"
	"path"
	"strings"
	"time"
)

// uiFS is the reporting UI's build output: index.html, assets/, the
// service worker and manifest, and components.json (this package's own
// input, via Manifest). "all:" so files starting with "_" are embedded
// too, matching Vite's default asset naming.
//
//go:embed all:ui
var uiFS embed.FS

// Manifest returns the UI build's component manifest (ui/components.json),
// ParseManifest's input.
func Manifest() []byte {
	b, err := uiFS.ReadFile("ui/components.json")
	if err != nil {
		// Embedded at build time: missing means the build that produced
		// this binary is broken, not a runtime condition to recover from.
		panic("reporting: ui/components.json: " + err.Error())
	}
	return b
}

// UI serves the built dashboard UI under /app/, the path its bundle and
// manifest are built for: a file of the build at its own path, and
// index.html for any other path, since the app routes on the client
// (/app/dashboards/3, /app/callback). Files under assets/ are named by
// their content and cached for a year; the shell, the service worker and
// the manifest keep their names across releases, so they are revalidated
// on every load. components.json is Manifest's input, not the app's, and
// a missing asset is a 404 rather than the shell served as a script.
func UI() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/app/")
		if name == r.URL.Path || name == "components.json" {
			http.NotFound(w, r)
			return
		}
		b, err := uiFS.ReadFile("ui/" + name)
		switch {
		case err == nil:
		case name == "assets" || strings.HasPrefix(name, "assets/"):
			http.NotFound(w, r)
			return
		default:
			name = "index.html"
			b = uiIndex()
		}
		h := w.Header()
		switch {
		case strings.HasPrefix(name, "assets/"):
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		case name == "index.html" || name == "sw.js" || name == "manifest.webmanifest":
			h.Set("Cache-Control", "no-cache")
		}
		if ct, ok := uiTypes[path.Ext(name)]; ok {
			h.Set("Content-Type", ct)
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
	})
}

// uiTypes are the content types Go's own table may lack or get wrong for
// the build's files; ServeContent infers the rest from the extension.
var uiTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".webmanifest": "application/manifest+json",
}

// uiIndex is the app's shell, embedded at build time like components.json.
func uiIndex() []byte {
	b, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		panic("reporting: ui/index.html: " + err.Error())
	}
	return b
}
