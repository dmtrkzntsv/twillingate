package reporting

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

// uiFS is the reporting UI's build output: index.html, assets/, the
// service worker and manifest, and components.json (this package's own
// input, via Manifest). "all:" so files starting with "_" are embedded
// too, matching Vite's default asset naming. Only components.json is
// committed; `make ui` builds the rest, so a bare `go build` of a fresh
// checkout embeds no app (see UI).
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
// on every load, against an ETag of their content, so an unchanged one
// costs a 304. components.json is Manifest's input, not the app's, and a
// missing asset is a 404 rather than the shell served as a script. No
// response may be framed by another page or sniffed into another type.
// A binary built without the app (go build before make ui) answers 503
// with how to build it, rather than panicking on every request.
func UI() http.Handler {
	return uiHandler(uiFS)
}

func uiHandler(files fs.ReadFileFS) http.Handler {
	if _, err := fs.Stat(files, "ui/index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "this binary was built without the dashboard app: build it with `make build`", http.StatusServiceUnavailable)
		})
	}
	// The ETags of the files that keep their names across releases.
	etags := sync.OnceValue(func() map[string]string {
		tags := map[string]string{}
		for _, name := range []string{"index.html", "sw.js", "manifest.webmanifest"} {
			sum := sha256.Sum256(mustRead(files, "ui/"+name))
			tags[name] = `"` + hex.EncodeToString(sum[:12]) + `"`
		}
		return tags
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		name := strings.TrimPrefix(r.URL.Path, "/app/")
		if name == r.URL.Path || name == "components.json" {
			http.NotFound(w, r)
			return
		}
		b, err := files.ReadFile("ui/" + name)
		switch {
		case err == nil:
		case name == "assets" || strings.HasPrefix(name, "assets/"):
			http.NotFound(w, r)
			return
		default:
			name = "index.html"
			b = mustRead(files, "ui/index.html")
		}
		switch tag, revalidated := etags()[name]; {
		case strings.HasPrefix(name, "assets/"):
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		case revalidated:
			h.Set("Cache-Control", "no-cache")
			h.Set("ETag", tag)
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

// mustRead reads a file every build of the app has: missing means the
// build that produced this binary is broken, not a runtime condition to
// recover from.
func mustRead(files fs.ReadFileFS, name string) []byte {
	b, err := files.ReadFile(name)
	if err != nil {
		panic("reporting: " + name + ": " + err.Error())
	}
	return b
}
