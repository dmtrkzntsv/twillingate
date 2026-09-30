package reporting

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"testing/fstest"
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

// TestUIRevalidatesWithETags: the files served no-cache carry an ETag of
// their content, so a reload that finds them unchanged gets a 304; the SPA
// fallback carries index.html's own.
func TestUIRevalidatesWithETags(t *testing.T) {
	index := serveUI(t, "/app/").Header().Get("ETag")
	if index == "" {
		t.Fatal("index.html has no ETag")
	}
	if got := serveUI(t, "/app/dashboards/3").Header().Get("ETag"); got != index {
		t.Errorf("SPA route ETag = %q, want index.html's %q", got, index)
	}
	for _, target := range []string{"/app/", "/app/dashboards/3", "/app/sw.js", "/app/manifest.webmanifest"} {
		tag := serveUI(t, target).Header().Get("ETag")
		if !strings.HasPrefix(tag, `"`) || !strings.HasSuffix(tag, `"`) || len(tag) < 10 {
			t.Errorf("GET %s ETag = %q, want a quoted content hash", target, tag)
			continue
		}
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("If-None-Match", tag)
		rec := httptest.NewRecorder()
		UI().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Errorf("GET %s with its ETag = %d, want 304", target, rec.Code)
		}
	}
	if serveUI(t, "/app/sw.js").Header().Get("ETag") == index {
		t.Error("sw.js and index.html share an ETag")
	}
}

// TestUIRefusesFramingAndSniffing: no /app/ response may be framed by
// another page, or read as a type other than the one it is served as.
func TestUIRefusesFramingAndSniffing(t *testing.T) {
	for _, target := range []string{"/app/", "/app/dashboards/3", "/app/assets/" + builtAsset(t), "/app/sw.js", "/app/components.json"} {
		h := serveUI(t, target).Header()
		if h.Get("X-Frame-Options") != "DENY" || h.Get("Content-Security-Policy") != "frame-ancestors 'none'" {
			t.Errorf("GET %s: X-Frame-Options %q, Content-Security-Policy %q", target,
				h.Get("X-Frame-Options"), h.Get("Content-Security-Policy"))
		}
		if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s: X-Content-Type-Options %q, want nosniff", target, got)
		}
	}
}

// The service worker precaches the shell's scripts and styles at install, so
// an offline launch can render; a build that forgot to stamp the list, or a
// bundle rebuilt without re-stamping, would ship a worker missing them. The
// API docs page's chunk (Swagger UI) is the exception, and must stay one:
// the dashboards never load it (web/scripts/stamp-sw.ts).
func TestServiceWorkerPrecachesEveryAsset(t *testing.T) {
	sw, err := fs.ReadFile(uiFS, "ui/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	assets, err := fs.ReadDir(uiFS, "ui/assets")
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) == 0 {
		t.Fatal("no built assets")
	}
	for _, a := range assets {
		listed := strings.Contains(string(sw), `"/app/assets/`+a.Name()+`"`)
		switch docs := strings.HasPrefix(a.Name(), "ApiDocs-"); {
		case docs && listed:
			t.Errorf("sw.js precaches the API docs chunk /app/assets/%s", a.Name())
		case !docs && !listed:
			t.Errorf("sw.js does not precache /app/assets/%s", a.Name())
		}
	}
	if !strings.Contains(string(sw), "const CACHE = 'twillingate-app-") {
		t.Error("sw.js cache name is not under the twillingate-app- prefix")
	}
}

func TestUIWithoutTheAppSaysHowToBuildIt(t *testing.T) {
	files := fstest.MapFS{"ui/components.json": {Data: []byte(`{"components":[]}`)}}
	rec := httptest.NewRecorder()
	uiHandler(files).ServeHTTP(rec, httptest.NewRequest("GET", "/app/dashboards/3", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "make build") {
		t.Fatalf("got %d %q, want 503 naming make build", rec.Code, rec.Body.String())
	}
}
