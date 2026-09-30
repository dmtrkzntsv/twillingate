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
// api-docs.html is a page of the build but not of the app: APIDocs serves it.
func UI() http.Handler {
	return uiHandler(uiFS, "")
}

// APIDocs serves the build's other page, api-docs.html (Swagger UI over
// the REST API's OpenAPI document), at whatever path it is mounted on:
// /api/docs. Its scripts and styles load from /app/assets/, and it is
// cached and guarded the way the app's shell is.
func APIDocs() http.Handler {
	return uiHandler(uiFS, "api-docs.html")
}

// uiHandler serves page for every request, or with page "" the /app/ tree.
func uiHandler(files fs.ReadFileFS, page string) http.Handler {
	if _, err := fs.Stat(files, "ui/index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "this binary was built without the dashboard app: build it with `make build`", http.StatusServiceUnavailable)
		})
	}
	// The ETags of the files that keep their names across releases.
	etags := sync.OnceValue(func() map[string]string {
		tags := map[string]string{}
		for _, name := range []string{"index.html", "api-docs.html", "sw.js", "manifest.webmanifest"} {
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
		name, b := page, []byte(nil)
		if page == "" {
			var ok bool
			if name, b, ok = appFile(files, r.URL.Path); !ok {
				http.NotFound(w, r)
				return
			}
		} else {
			b = mustRead(files, "ui/"+page)
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

// appFile resolves an /app/ path to the build file that answers it: the
// file itself, or index.html for a route the app handles on the client. ok
// is false for what is not the app's to serve: components.json,
// api-docs.html (served at /api/docs) and a missing asset.
func appFile(files fs.ReadFileFS, urlPath string) (name string, b []byte, ok bool) {
	name = strings.TrimPrefix(urlPath, "/app/")
	if name == urlPath || name == "components.json" || name == "api-docs.html" {
		return "", nil, false
	}
	b, err := files.ReadFile("ui/" + name)
	switch {
	case err == nil:
		return name, b, true
	case name == "assets" || strings.HasPrefix(name, "assets/"):
		return "", nil, false
	}
	return "index.html", mustRead(files, "ui/index.html"), true
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
