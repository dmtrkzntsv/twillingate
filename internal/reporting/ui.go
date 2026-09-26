package reporting

import (
	"embed"
	"io/fs"
	"net/http"
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

// UI serves the built dashboard UI: index.html, assets/, the service
// worker, manifest and icons. Task 16 mounts it under its route and adds
// the SPA fallback a client-routed app needs; here it is a plain static
// file server over the embedded build.
func UI() http.Handler {
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic("reporting: ui fs: " + err.Error())
	}
	return http.FileServerFS(sub)
}
