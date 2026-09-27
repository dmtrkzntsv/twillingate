package reporting

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
)

func serveUI(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	UI().ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
	return rec
}

// builtAsset names one file the web build put under ui/assets, whatever
// its content hash is this build.
func builtAsset(t *testing.T) string {
	t.Helper()
	names, err := fs.Glob(uiFS, "ui/assets/*.js")
	if err != nil || len(names) == 0 {
		t.Fatalf("no built asset under ui/assets: %v", err)
	}
	return path.Base(names[0])
}

// TestUIServesTheShellForEveryRoute: the app routes on the client, so a
// reload of any /app/ path gets index.html, never a 404.
func TestUIServesTheShellForEveryRoute(t *testing.T) {
	index, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/app/", "/app/dashboards/3", "/app/dashboards/3?project=1&range=7d", "/app/callback?code=x", "/app/index.html"} {
		rec := serveUI(t, target)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", target, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s Content-Type = %q, want text/html", target, ct)
		}
		if rec.Body.String() != string(index) {
			t.Errorf("GET %s did not answer index.html", target)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", target, cc)
		}
	}
}

// TestUICachesHashedAssetsForever: a file under assets/ is named by its
// content, so it never changes; the shell, the worker and the manifest
// keep their names across releases and are revalidated on every load.
func TestUICachesHashedAssetsForever(t *testing.T) {
	asset := builtAsset(t)
	rec := serveUI(t, "/app/assets/"+asset)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET asset = %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("asset Cache-Control = %q", cc)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("asset Content-Type = %q", ct)
	}
	for target, ct := range map[string]string{
		"/app/sw.js":                "text/javascript",
		"/app/manifest.webmanifest": "application/manifest+json",
	} {
		rec := serveUI(t, target)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", target, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", target, cc)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, ct) {
			t.Errorf("GET %s Content-Type = %q, want %s", target, got, ct)
		}
	}
	if rec := serveUI(t, "/app/icon-192.png"); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("GET icon = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// TestUIHidesItsBuildInputs: components.json is the Go side's input, not
// part of the app; and a missing asset is a 404, not the shell served as
// a script.
func TestUIHidesItsBuildInputs(t *testing.T) {
	for _, target := range []string{"/app/components.json", "/app/assets/missing-0000.js", "/app/assets/"} {
		if rec := serveUI(t, target); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
	}
}
