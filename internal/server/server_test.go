package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/geo"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	_ "github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
)

type fakeQueue struct {
	mu         sync.Mutex
	views      []store.View
	events     []store.ProductEvent
	identities []store.Identity
}

func (f *fakeQueue) EnqueueView(v store.View) {
	f.mu.Lock()
	f.views = append(f.views, v)
	f.mu.Unlock()
}
func (f *fakeQueue) EnqueueEvent(e store.ProductEvent) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
}

// UpsertIdentities lets the queue double as the NameStore, so a test can
// assert on events and names together.
func (f *fakeQueue) UpsertIdentities(_ context.Context, ids []store.Identity) error {
	f.mu.Lock()
	f.identities = append(f.identities, ids...)
	f.mu.Unlock()
	return nil
}

type fixedSalt struct{}

func (fixedSalt) Current(context.Context) (string, error) { return "test-salt", nil }

const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

const (
	testKey    = "ak_test"
	testOrigin = "https://app.com"
)

func testServer(t *testing.T) (*fakeQueue, http.Handler) {
	q, s := newServer(t)
	return q, s
}

// newTestRegistry seeds a temp-DB registry with the given projects, in
// order, so the first spec is project 1. keys maps a project's index in
// specs to {key, label}.
func newTestRegistry(t *testing.T, projects []manage.ProjectSpec, keys map[int][2]string) *manage.Registry {
	t.Helper()
	st, err := store.Open("sqlite://" + t.TempDir() + "/reg.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	reg := manage.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := manage.NewOps(reg, st)
	var ids []int64
	for _, spec := range projects {
		p, err := ops.CreateProject(ctx, "test", spec)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	for i, kl := range keys {
		if err := st.InsertIngestKey(ctx, store.RegistryKey{
			Key: kl[0], ProjectID: ids[i], Label: kl[1]},
			store.AuditEntry{Actor: "test", Action: "key.issue", Subject: kl[1]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	return reg
}

// newServer builds a *Server over one project with one key, logging to
// slog.Default(). newLoggingServer is the same with a captured log.
func newServer(t *testing.T) (*fakeQueue, *Server) {
	t.Helper()
	return newServerWithLogger(t, slog.Default())
}

func newLoggingServer(t *testing.T) (*fakeQueue, *Server, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	q, s := newServerWithLogger(t, slog.New(slog.NewTextHandler(&buf, nil)))
	return q, s, &buf
}

func newServerWithLogger(t *testing.T, logger *slog.Logger) (*fakeQueue, *Server) {
	t.Helper()
	cfg := configtest.Load(t, nil)
	reg := newTestRegistry(t,
		[]manage.ProjectSpec{{
			Name:           "App",
			AllowedOrigins: []string{testOrigin},
		}},
		map[int][2]string{0: {testKey, "web"}})
	g, _ := geo.New("cloudflare://", t.TempDir(), slog.Default())
	q := &fakeQueue{}
	return q, New(cfg, reg, q, g, fixedSalt{}, q, logger)
}

// newTestServer is newServer for a test that only needs the server.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	_, s := newServer(t)
	return s
}

// post sends one envelope. headers may set Origin, X-Analytics-Key or a
// non-browser User-Agent; a Chrome UA and CF country are the defaults so
// web-kind enrichment behaves like a real browser request.
func post(h http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/ingest/events", strings.NewReader(body))
	r.Header.Set("User-Agent", chromeUA)
	r.Header.Set("CF-IPCountry", "DE")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeResult(t *testing.T, w *httptest.ResponseRecorder) ingestResult {
	t.Helper()
	var res ingestResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	return res
}

func envelopeOf(events string) string {
	return `{"key":"` + testKey + `","events":[` + events + `]}`
}

// --- authentication ---

func TestRejectsUnknownKey(t *testing.T) {
	_, h := testServer(t)
	w := post(h, `{"key":"nope","events":[{"name":"x"}]}`, nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestRejectsMissingKey(t *testing.T) {
	_, h := testServer(t)
	w := post(h, `{"events":[{"name":"x"}]}`, nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAcceptsKeyFromHeader(t *testing.T) {
	q, h := testServer(t)
	w := post(h, `{"events":[{"name":"subscribed","attributes":{"plan":"pro"}}]}`,
		map[string]string{"X-Analytics-Key": testKey})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	if len(q.events) != 1 || q.events[0].EventName != "subscribed" {
		t.Fatalf("queued = %+v", q.events)
	}
	if q.events[0].Attributes["plan"] != "pro" {
		t.Errorf("attributes = %v", q.events[0].Attributes)
	}
	if q.events[0].ProjectID != 1 {
		t.Errorf("project = %d; the key must resolve it", q.events[0].ProjectID)
	}
}

// TestHashInputIsTheProjectId pins the salt seam: a connection-hash actor is
// hashed with the id as a decimal string, never a name, so renaming a
// project cannot change its hashes.
func TestHashInputIsTheProjectId(t *testing.T) {
	q, h := testServer(t)
	rec := post(h, `{"events":[{"name":"$page_view","attributes":{"$path":"/"}}]}`,
		map[string]string{"Origin": testOrigin, "X-Analytics-Key": testKey})
	if rec.Code != 202 {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	if len(q.views) != 1 {
		t.Fatalf("views = %d, want 1", len(q.views))
	}
	// httptest.NewRequest sets RemoteAddr to 192.0.2.1:1234 and post adds
	// no forwarding header, so clientIP resolves to 192.0.2.1.
	want := identity.VisitorHash("test-salt", "192.0.2.1", chromeUA, "1")
	if q.views[0].ProjectID != 1 || q.views[0].ActorID != want {
		t.Fatalf("view = project %d actor %q, want project 1 actor %q", q.views[0].ProjectID, q.views[0].ActorID, want)
	}
}

func TestHeaderKeyBeatsBodyKey(t *testing.T) {
	_, h := testServer(t)
	w := post(h, `{"key":"wrong","events":[{"name":"x"}]}`,
		map[string]string{"X-Analytics-Key": testKey})
	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", w.Code)
	}
}

// --- routing ---

func TestRoutesViewsAndCustom(t *testing.T) {
	q, h := testServer(t)
	body := `{"key":"` + testKey + `","attributes":{"$platform":"iOS","$os":"ios","$app_version":"2.4.1"},
	  "events":[
	    {"name":"$page_view","attributes":{"$host":"app.com","$path":"/pricing","$utm_source":"hn","$display_width":1920,"$display_height":1080,"$locale":"de-DE"}},
	    {"name":"$screen_view","attributes":{"$screen":"/settings","$os_version":"17.2","$device_model":"iPhone15,2","$locale":"en-US","$session_id":"s1"}},
	    {"name":"subscribed","attributes":{"plan":"pro"}}
	  ]}`
	w := post(h, body, map[string]string{"Origin": testOrigin})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	if res := decodeResult(t, w); res.Accepted != 3 || res.Rejected != 0 {
		t.Errorf("result = %+v", res)
	}
	if len(q.views) != 2 {
		t.Fatalf("views = %+v", q.views)
	}
	web, app := q.views[0], q.views[1]
	if web.Kind != "web" || web.Host != "app.com" || web.Path != "/pricing" || web.UTMSource != "hn" ||
		web.Country != "DE" || web.DisplayWidth != 1920 || web.DisplayHeight != 1080 || web.Locale != "de-DE" {
		t.Errorf("web view = %+v", web)
	}
	// The environment is declared, lower-cased and never parsed: a Chrome
	// User-Agent on this request names nothing.
	if web.Platform != "ios" || web.OS != "ios" || web.Browser != "unknown" || web.BrowserVersion != "" || web.Device != "unknown" {
		t.Errorf("web environment = platform %q os %q browser %q/%q device %q", web.Platform, web.OS, web.Browser, web.BrowserVersion, web.Device)
	}
	if app.Kind != "app" || app.Path != "/settings" || app.Platform != "ios" || app.OS != "ios" || app.OSVersion != "17.2" ||
		app.AppVersion != "2.4.1" || app.DeviceModel != "iPhone15,2" || app.Locale != "en-US" ||
		app.SessionID != "s1" || app.Country != "DE" || app.Browser != "unknown" || app.Device != "unknown" {
		t.Errorf("app view = %+v", app)
	}
	if len(q.events) != 1 || q.events[0].Platform != "ios" || q.events[0].OS != "ios" || q.events[0].AppVersion != "2.4.1" {
		t.Errorf("events = %+v", q.events)
	}
}

func TestPageviewNameIsASilentAlias(t *testing.T) {
	q, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"$pageview","attributes":{"$host":"app.com","$path":"/x","$platform":"linux"}}`), nil)
	res := decodeResult(t, w)
	if res.Accepted != 1 || len(res.Warnings) != 0 {
		t.Errorf("aliases must be accepted without a warning: %+v", res)
	}
	if len(q.views) != 1 || q.views[0].Kind != "web" || q.views[0].Platform != "linux" || q.views[0].OS != "unknown" {
		t.Errorf("views = %+v ($platform must land on platform and never fill os)", q.views)
	}
}

func TestKindDeclaredValidatedAndDefaulted(t *testing.T) {
	q, h := testServer(t)
	body := `{"key":"` + testKey + `","attributes":{"$kind":"cli"},
	  "events":[
	    {"name":"$screen_view","attributes":{"$screen":"deploy"}},
	    {"name":"$page_view","attributes":{"$path":"/","$kind":"Bad Kind!"}},
	    {"name":"$page_view","attributes":{"$path":"/","$kind":""}}
	  ]}`
	// A crawler User-Agent: web-kind rows are filtered, the cli row is not.
	w := post(h, body, map[string]string{"User-Agent": "Googlebot/2.1"})
	res := decodeResult(t, w)
	if res.Accepted != 3 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Reason, `invalid $kind "Bad Kind!", using "web"`) {
		t.Errorf("warnings = %+v", res.Warnings)
	}
	// The invalid kind and the empty kind both fall back to the name's
	// default, web, and a crawler User-Agent on a web row is a bot:
	// accepted, never stored. Only the cli view reaches the queue.
	if len(q.views) != 1 {
		t.Fatalf("views = %+v", q.views)
	}
	if q.views[0].Kind != "cli" || q.views[0].Path != "deploy" || q.views[0].Browser != "unknown" {
		t.Errorf("cli view = %+v (non-web kinds are never filtered; nothing is parsed on any kind)", q.views[0])
	}
}

// A web batch that declares nothing stores unknown for os, browser and
// device: the server no longer derives any of them from the User-Agent.
// The one thing it still reads the User-Agent for is the crawler drop.
func TestEnvironmentIsDeclaredNotParsed(t *testing.T) {
	q, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"$page_view","attributes":{"$host":"app.com","$path":"/x"}}`), nil)
	if res := decodeResult(t, w); res.Accepted != 1 || len(res.Warnings) != 0 {
		t.Fatalf("result = %+v (an absent value is not a mistake to warn about)", res)
	}
	if len(q.views) != 1 {
		t.Fatalf("views = %+v", q.views)
	}
	v := q.views[0]
	if v.Platform != "unknown" || v.OS != "unknown" || v.OSName != "" || v.Browser != "unknown" || v.BrowserVersion != "" || v.Device != "unknown" {
		t.Errorf("undeclared environment = %+v, want unknown everywhere and an empty os_name", v)
	}

	q, h = testServer(t)
	post(h, envelopeOf(`{"name":"$page_view","attributes":{"$host":"app.com","$path":"/x"}}`),
		map[string]string{"User-Agent": "Googlebot/2.1"})
	if len(q.views) != 0 {
		t.Errorf("a crawler User-Agent must still drop a web view: %+v", q.views)
	}
}

// $os closes to a lower-case vocabulary. Present but unrecognised is other
// with a warning and the raw value preserved in os_name; a deliberate
// other is not warned about; an explicit $os_name always wins.
func TestOSIsValidatedAndTheNamePreserved(t *testing.T) {
	q, h := testServer(t)
	body := envelopeOf(`{"name":"$page_view","attributes":{"$path":"/","$os":"Chrome OS"}},
		{"name":"$page_view","attributes":{"$path":"/","$os":"Haiku R1"}},
		{"name":"$page_view","attributes":{"$path":"/","$os":"Haiku R1","$os_name":"Haiku R1 beta 5"}},
		{"name":"$page_view","attributes":{"$path":"/","$os":"other"}},
		{"name":"$page_view","attributes":{"$path":"/","$os":"macos","$os_name":"macOS 14.2"}},
		{"name":"signup","attributes":{"$os":"Haiku R1","$os_name":"dropped on product events"}}`)
	res := decodeResult(t, post(h, body, nil))
	if res.Accepted != 6 || res.Rejected != 0 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Warnings) != 3 {
		t.Fatalf("warnings = %+v, want one per unrecognised $os and none for a deliberate other", res.Warnings)
	}
	if res.Warnings[0].Index != 1 || res.Warnings[0].Reason != `$os "Haiku R1" is not a known value, stored as other` {
		t.Errorf("warning = %+v", res.Warnings[0])
	}
	if res.Warnings[1].Index != 2 || res.Warnings[2].Index != 5 {
		t.Errorf("warnings = %+v", res.Warnings)
	}
	want := []struct{ os, name string }{
		{"chromeos", ""}, {"other", "Haiku R1"}, {"other", "Haiku R1 beta 5"}, {"other", ""}, {"macos", "macOS 14.2"},
	}
	if len(q.views) != len(want) {
		t.Fatalf("views = %+v", q.views)
	}
	for i, w := range want {
		if q.views[i].OS != w.os || q.views[i].OSName != w.name {
			t.Errorf("view %d os = (%q, %q), want (%q, %q)", i, q.views[i].OS, q.views[i].OSName, w.os, w.name)
		}
	}
	if len(q.events) != 1 || q.events[0].OS != "other" {
		t.Errorf("product event os = %+v, want other", q.events)
	}
	if _, leaked := q.events[0].Attributes["$os_name"]; leaked {
		t.Error("$os_name reached the product event's attributes")
	}
}

// $platform is lower-cased and pattern-checked, never fills os, lands on
// product events too, and an invalid value warns and stores unknown.
func TestPlatformIsValidatedIndependentlyOfOS(t *testing.T) {
	q, h := testServer(t)
	body := envelopeOf(`{"name":"$page_view","attributes":{"$path":"/","$platform":"Electron","$os":"macos"}},
		{"name":"$page_view","attributes":{"$path":"/","$platform":"Not A Platform!"}},
		{"name":"$page_view","attributes":{"$path":"/","$platform":"ios"}},
		{"name":"signup","attributes":{"$platform":"iOS"}}`)
	res := decodeResult(t, post(h, body, nil))
	if res.Accepted != 4 || len(res.Warnings) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if res.Warnings[0].Index != 1 || res.Warnings[0].Reason != `$platform "Not A Platform!" is not a known value, stored as unknown` {
		t.Errorf("warning = %+v", res.Warnings[0])
	}
	if len(q.views) != 3 {
		t.Fatalf("views = %+v", q.views)
	}
	if q.views[0].Platform != "electron" || q.views[0].OS != "macos" {
		t.Errorf("view 0 = platform %q os %q", q.views[0].Platform, q.views[0].OS)
	}
	if q.views[1].Platform != "unknown" || q.views[1].OS != "unknown" {
		t.Errorf("view 1 = platform %q os %q, want unknown for both", q.views[1].Platform, q.views[1].OS)
	}
	if q.views[2].Platform != "ios" || q.views[2].OS != "unknown" {
		t.Errorf("view 2 = platform %q os %q: $platform must never fill os", q.views[2].Platform, q.views[2].OS)
	}
	if len(q.events) != 1 || q.events[0].Platform != "ios" {
		t.Errorf("product event = %+v, want platform ios", q.events)
	}
}

// Browser and device close on the same terms as os. Both are views-only:
// on a product event an unrecognised $browser or $device is dropped
// without any warning at all — only $os and $platform warn there.
func TestBrowserAndDeviceAreValidated(t *testing.T) {
	q, h := testServer(t)
	body := envelopeOf(`{"name":"$page_view","attributes":{"$path":"/","$browser":"Samsung Internet","$browser_version":"25","$device":"Tablet"}},
		{"name":"$page_view","attributes":{"$path":"/","$browser":"netscape","$device":"phablet"}},
		{"name":"$page_view","attributes":{"$path":"/","$browser":"other","$device":"other"}},
		{"name":"signup","attributes":{"$browser":"safari","$device":"mobile"}}`)
	res := decodeResult(t, post(h, body, nil))
	if res.Accepted != 4 || len(res.Warnings) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if res.Warnings[0].Reason != `$browser "netscape" is not a known value, stored as other` ||
		res.Warnings[1].Reason != `$device "phablet" is not a known value, stored as other` {
		t.Errorf("warnings = %+v", res.Warnings)
	}
	want := []struct{ browser, version, device string }{
		{"samsung_internet", "25", "tablet"}, {"other", "", "other"}, {"other", "", "other"},
	}
	if len(q.views) != 3 {
		t.Fatalf("views = %+v", q.views)
	}
	for i, w := range want {
		v := q.views[i]
		if v.Browser != w.browser || v.BrowserVersion != w.version || v.Device != w.device {
			t.Errorf("view %d = %q/%q %q, want %+v", i, v.Browser, v.BrowserVersion, v.Device, w)
		}
	}
	if len(q.events) != 1 || len(q.events[0].Attributes) != 0 {
		t.Errorf("product event attributes = %+v, want the reserved keys dropped", q.events)
	}
}

func TestBotFilterAppliesToWebKindOnly(t *testing.T) {
	q, h := testServer(t)
	body := envelopeOf(`{"name":"$page_view","attributes":{"$host":"app.com","$path":"/x"}},
		{"name":"$screen_view","attributes":{"$screen":"/s"}},
		{"name":"$page_view","attributes":{"$path":"/y","$kind":"app"}},
		{"name":"custom"}`)
	w := post(h, body, map[string]string{"User-Agent": "Googlebot/2.1"})
	if len(q.views) != 2 {
		t.Errorf("only web-kind rows are bot-filtered, got %+v", q.views)
	}
	for _, v := range q.views {
		if v.Kind == "web" {
			t.Errorf("web view survived the bot filter: %+v", v)
		}
	}
	if len(q.events) != 1 {
		t.Errorf("bot filter must not touch custom events: %+v", q.events)
	}
	if res := decodeResult(t, w); res.Accepted != 4 || res.Rejected != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestViewRequiresALocation(t *testing.T) {
	q, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"$page_view"},
		{"name":"$screen_view","attributes":{"$path":"/via-path"}},
		{"name":"$page_view","attributes":{"$screen":"/via-screen"}}`), nil)
	res := decodeResult(t, w)
	if res.Rejected != 1 || len(res.Errors) != 1 || res.Errors[0].Reason != "view requires $path or $screen" {
		t.Errorf("result = %+v", res)
	}
	if len(q.views) != 2 || q.views[0].Path != "/via-path" || q.views[0].Kind != "app" {
		t.Errorf("views = %+v ($path is accepted on a screen view)", q.views)
	}
	if q.views[1].Path != "/via-screen" || q.views[1].Kind != "web" {
		t.Errorf("views = %+v ($screen is accepted on a page view)", q.views)
	}
}

func TestDisplaySizeParsing(t *testing.T) {
	q, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"$page_view","attributes":{"$path":"/","$display_width":"1440","$display_height":-5}},
		{"name":"$page_view","attributes":{"$path":"/b","$display_width":"wide"}}`), nil)
	res := decodeResult(t, w)
	if res.Accepted != 2 || len(res.Warnings) != 2 {
		t.Errorf("result = %+v (want one warning per unusable value)", res)
	}
	if q.views[0].DisplayWidth != 1440 || q.views[0].DisplayHeight != 0 || q.views[1].DisplayWidth != 0 {
		t.Errorf("views = %+v", q.views)
	}
}

func TestActorKindRecorded(t *testing.T) {
	q, h := testServer(t)
	body := `{"key":"` + testKey + `","events":[
	    {"name":"$page_view","attributes":{"$path":"/","$user_id":"u1","$install_id":"i1"}},
	    {"name":"$page_view","attributes":{"$path":"/","$install_id":"i1"}},
	    {"name":"$page_view","attributes":{"$path":"/"}},
	    {"name":"x","attributes":{"$install_id":"i1"}}]}`
	post(h, body, nil)
	if len(q.views) != 3 || len(q.events) != 1 {
		t.Fatalf("views=%d events=%d", len(q.views), len(q.events))
	}
	want := []string{store.ActorUser, store.ActorInstall, store.ActorConnection}
	for i, v := range q.views {
		if v.ActorKind != want[i] {
			t.Errorf("view %d actor_kind = %q, want %q", i, v.ActorKind, want[i])
		}
	}
	if q.events[0].ActorKind != store.ActorInstall {
		t.Errorf("event actor_kind = %q", q.events[0].ActorKind)
	}
}

func TestPerEventContextOverridesBatch(t *testing.T) {
	q, h := testServer(t)
	body := `{"key":"` + testKey + `","attributes":{"$app_version":"2.4.1"},
	  "events":[{"name":"a"},{"name":"b","attributes":{"$app_version":"2.5.0"}}]}`
	post(h, body, nil)
	if len(q.events) != 2 {
		t.Fatalf("events = %+v", q.events)
	}
	if q.events[0].AppVersion != "2.4.1" || q.events[1].AppVersion != "2.5.0" {
		t.Errorf("versions = %q %q; want 2.4.1 then 2.5.0",
			q.events[0].AppVersion, q.events[1].AppVersion)
	}
}

func TestUnknownReservedNameBecomesCustomEventWithWarning(t *testing.T) {
	q, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"$pageviews"}`), nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d", w.Code)
	}
	res := decodeResult(t, w)
	if res.Accepted != 1 || res.Rejected != 0 || len(res.Warnings) != 1 {
		t.Errorf("result = %+v", res)
	}
	if len(q.events) != 1 || q.events[0].EventName != "$pageviews" {
		t.Errorf("events = %+v; an unknown reserved name must not be dropped", q.events)
	}
}

func TestUnknownReservedKeyDroppedWithWarning(t *testing.T) {
	q, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"x","attributes":{"$app_ver":"2","plan":"pro"}}`), nil)
	res := decodeResult(t, w)
	if res.Accepted != 1 || len(res.Warnings) != 1 {
		t.Errorf("result = %+v", res)
	}
	if _, ok := q.events[0].Attributes["$app_ver"]; ok {
		t.Error("unknown reserved key must not be stored")
	}
	if q.events[0].Attributes["plan"] != "pro" {
		t.Error("ordinary attributes must survive")
	}
}

// --- per-event rejection ---

func TestPerEventRejectionLeavesBatchIntact(t *testing.T) {
	q, h := testServer(t)
	body := envelopeOf(`{"name":"good"},{"name":""},{"name":"$pageview"},
		{"name":"$screen_view"},{"name":"bad-id","id":"not-a-uuid"},{"name":"also_good"}`)
	w := post(h, body, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 even with rejects", w.Code)
	}
	res := decodeResult(t, w)
	// missing name; two views each rejected with "view requires $path or $screen"; bad id
	if res.Accepted != 2 || res.Rejected != 4 || len(res.Errors) != 4 {
		t.Errorf("result = %+v", res)
	}
	if len(q.events) != 2 {
		t.Errorf("queued %d events, want 2", len(q.events))
	}
}

func TestClientSuppliedIDIsPreserved(t *testing.T) {
	q, h := testServer(t)
	post(h, envelopeOf(`{"name":"x","id":"018f1e5a-0000-7000-8000-000000000001"}`), nil)
	if len(q.events) != 1 || q.events[0].ID != "018f1e5a-0000-7000-8000-000000000001" {
		t.Errorf("events = %+v; a client id must survive so retries dedupe", q.events)
	}
}

func TestMissingIDIsGenerated(t *testing.T) {
	q, h := testServer(t)
	post(h, envelopeOf(`{"name":"x"}`), nil)
	if len(q.events) != 1 || q.events[0].ID == "" {
		t.Errorf("events = %+v", q.events)
	}
}

func TestClientTimestampIsUsedAndClamped(t *testing.T) {
	q, h := testServer(t)
	// Relative to now: a fixed date would age out of the raw window and
	// turn this into a time bomb.
	recent := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	post(h, envelopeOf(`{"name":"recent","ts":"`+recent.Format(time.RFC3339)+`"},
		{"name":"ancient","ts":"2001-01-01T00:00:00Z"}`), nil)
	if len(q.events) != 2 {
		t.Fatalf("events = %+v", q.events)
	}
	if !q.events[0].TS.Equal(recent) {
		t.Errorf("in-range ts = %v, want the client value", q.events[0].TS)
	}
	// Clamped, not dropped, and never older than the global views raw
	// window (30 days by default).
	if q.events[1].TS.Before(q.events[1].ReceivedAt.Add(-31 * 24 * time.Hour)) {
		t.Errorf("clamped ts = %v, older than the raw window", q.events[1].TS)
	}
}

// TestEventAgeClampUsesGlobalRawWindow verifies the clamp follows the
// configured views raw window rather than a hard-coded default: retention
// is global, so an operator who shortens RETENTION_VIEWS_RAW_DAYS must
// also shorten how far back a clock-skewed late event can land, or it
// could target a day the daily pass already aggregated and deleted.
func TestEventAgeClampUsesGlobalRawWindow(t *testing.T) {
	cfg := configtest.Load(t, map[string]string{"RETENTION_VIEWS_RAW_DAYS": "3"})
	reg := newTestRegistry(t,
		[]manage.ProjectSpec{
			{Name: "Clamped", AllowedOrigins: []string{testOrigin}},
			{Name: "Normal", AllowedOrigins: []string{testOrigin}},
		},
		map[int][2]string{0: {"ak_clamped", "web"}, 1: {"ak_normal", "web"}})
	g, _ := geo.New("cloudflare://", t.TempDir(), slog.Default())
	q := &fakeQueue{}
	h := New(cfg, reg, q, g, fixedSalt{}, q, slog.Default())

	oldTS := time.Now().UTC().AddDate(0, 0, -10).Format(time.RFC3339)
	recentTS := time.Now().UTC().AddDate(0, 0, -1).Format(time.RFC3339)

	// A 10-day-old event must clamp to the 3-day window, not the 30-day
	// default.
	w := post(h, `{"key":"ak_clamped","events":[{"name":"$page_view","ts":"`+oldTS+`","attributes":{"$path":"/x"}}]}`, nil)
	res := decodeResult(t, w)
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Reason, "clamped") {
		t.Fatalf("warnings = %+v, want a clamp warning", res.Warnings)
	}
	if len(q.views) != 1 || q.views[0].ProjectID != 1 {
		t.Fatalf("views = %+v", q.views)
	}
	age := time.Since(q.views[0].TS)
	if age < 3*24*time.Hour || age > 3*24*time.Hour+time.Minute {
		t.Errorf("clamped view age = %v, want ~3 days (the configured raw window)", age)
	}

	// Control: the window applies to every project alike, and a timestamp
	// inside it passes through as-is.
	w = post(h, `{"key":"ak_normal","events":[{"name":"$page_view","ts":"`+recentTS+`","attributes":{"$path":"/x"}}]}`, nil)
	res = decodeResult(t, w)
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none: 1 day is within the 3-day window", res.Warnings)
	}
	if len(q.views) != 2 || q.views[1].ProjectID != 2 {
		t.Fatalf("views = %+v", q.views)
	}
	got, err := time.Parse(time.RFC3339, recentTS)
	if err != nil {
		t.Fatal(err)
	}
	if !q.views[1].TS.Equal(got) {
		t.Errorf("unclamped ts = %v, want the client value %v", q.views[1].TS, got)
	}
}

func TestOversizedBatchIsRejected(t *testing.T) {
	_, h := testServer(t)
	var b strings.Builder
	b.WriteString(`{"key":"` + testKey + `","events":[`)
	for i := 0; i < maxBatchEvents+1; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"name":"x"}`)
	}
	b.WriteString(`]}`)
	if w := post(h, b.String(), nil); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", w.Code)
	}
}

func TestBodyLimit(t *testing.T) {
	_, h := testServer(t)
	big := strings.Repeat("a", maxBody+1024)
	if w := post(h, `{"key":"`+testKey+`","events":[{"name":"`+big+`"}]}`, nil); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an oversized body", w.Code)
	}
}

func TestMalformedBodyIsRejected(t *testing.T) {
	_, h := testServer(t)
	if w := post(h, `not json`, nil); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- origin ---

func TestOriginPresentMustMatch(t *testing.T) {
	_, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"x"}`), map[string]string{"Origin": "https://evil.example"})
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestOriginAbsentIsAccepted(t *testing.T) {
	_, h := testServer(t)
	if w := post(h, envelopeOf(`{"name":"x"}`), nil); w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202 for a native app with no Origin", w.Code)
	}
}

// --- ids are stored as sent ---

func TestIdsAreStoredAsSent(t *testing.T) {
	q, h := testServer(t)
	post(h, `{"key":"`+testKey+`","attributes":{"$user_id":"u1","$group_id":"org9","$user_name":"Ada","$group_name":"Acme","$install_id":"i1"},
	  "events":[{"name":"$screen_view","attributes":{"$screen":"/x"}}]}`, nil)

	if len(q.views) != 1 {
		t.Fatalf("views = %+v", q.views)
	}
	v := q.views[0]
	if v.UserID != "u1" || v.ActorID != "u1" || v.ActorKind != store.ActorUser {
		t.Errorf("view identity = actor %q/%s user %q; want raw u1 as the user actor", v.ActorID, v.ActorKind, v.UserID)
	}
	if v.GroupID != "org9" {
		t.Errorf("group_id = %q; groups are stored raw", v.GroupID)
	}
	if len(q.identities) != 2 {
		t.Fatalf("identities = %+v; want the user and the group name", q.identities)
	}
	var user, group bool
	for _, id := range q.identities {
		switch {
		case id.Kind == store.KindUser && id.ID == "u1" && id.Name == "Ada" && id.ProjectID == 1:
			user = true
		case id.Kind == store.KindGroup && id.ID == "org9" && id.Name == "Acme" && id.ProjectID == 1:
			group = true
		}
	}
	if !user || !group {
		t.Errorf("identities = %+v; want Ada for u1 and Acme for org9", q.identities)
	}
}

func TestInstallIdThenConnectionHash(t *testing.T) {
	q, h := testServer(t)
	post(h, `{"key":"`+testKey+`","attributes":{"$install_id":"i1"},
	  "events":[{"name":"a"}]}`, nil)
	post(h, envelopeOf(`{"name":"b"}`), nil)

	if q.events[0].ActorID != "i1" || q.events[0].ActorKind != store.ActorInstall {
		t.Errorf("actor with install only = %q/%s, want i1/install", q.events[0].ActorID, q.events[0].ActorKind)
	}
	want := identity.VisitorHash("test-salt", "192.0.2.1", chromeUA, "1")
	if q.events[1].ActorID != want || q.events[1].ActorKind != store.ActorConnection {
		t.Errorf("actor with no identifier = %q/%s, want the connection hash %q", q.events[1].ActorID, q.events[1].ActorKind, want)
	}
}

func TestUserNameNeedsAUserId(t *testing.T) {
	q, h := testServer(t)
	post(h, `{"key":"`+testKey+`","attributes":{"$user_name":"Ada","$group_id":"g","$group_name":"G"},
	  "events":[{"name":"a"}]}`, nil)
	if len(q.identities) != 1 || q.identities[0].Kind != store.KindGroup {
		t.Errorf("identities = %+v; a name without an id names nothing", q.identities)
	}
}

func TestFirstIdsAreLoggedOncePerKind(t *testing.T) {
	_, s, buf := newLoggingServer(t)
	// A rejected view carries $user_id but stores nothing, so it must not
	// trip the log: the first store, not the first sighting, is what counts.
	post(s, `{"key":"`+testKey+`","attributes":{"$user_id":"u1"},"events":[{"name":"$page_view"}]}`, nil)
	if buf.Len() != 0 {
		t.Fatalf("rejected batch logged something: %s", buf.String())
	}
	userBatch := `{"key":"` + testKey + `","attributes":{"$user_id":"u1"},"events":[{"name":"a"}]}`
	post(s, userBatch, nil)
	post(s, userBatch, nil)
	post(s, `{"key":"`+testKey+`","attributes":{"$install_id":"i1"},"events":[{"name":"b"}]}`, nil)
	post(s, envelopeOf(`{"name":"c"}`), nil)

	out := buf.String()
	if n := strings.Count(out, "project receives ids"); n != 2 {
		t.Fatalf("log has %d 'project receives ids' lines, want 2 (one per kind):\n%s", n, out)
	}
	for _, want := range []string{"project=1 kind=user", "project=1 kind=install"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "kind=connection") {
		t.Errorf("a connection-hash actor must not be logged as an id:\n%s", out)
	}
}

// A name arrives only beside a row that is stored: a rejected view and a
// bot-filtered view carry no identity into the identities table.
func TestNamesFollowStoredRowsOnly(t *testing.T) {
	q, h := testServer(t)
	// no $path and no $screen: the view is rejected
	post(h, `{"key":"`+testKey+`","attributes":{"$user_id":"u1","$user_name":"Ada"},
	  "events":[{"name":"$page_view"}]}`, nil)
	if len(q.views) != 0 || len(q.identities) != 0 {
		t.Fatalf("rejected view stored views=%d identities=%+v", len(q.views), q.identities)
	}
	// a crawler: accepted and silently dropped, names included
	post(h, `{"key":"`+testKey+`","attributes":{"$user_id":"u1","$user_name":"Ada"},
	  "events":[{"name":"$page_view","attributes":{"$path":"/"}}]}`,
		map[string]string{"User-Agent": "Googlebot/2.1 (+http://www.google.com/bot.html)"})
	if len(q.views) != 0 || len(q.identities) != 0 {
		t.Fatalf("bot view stored views=%d identities=%+v", len(q.views), q.identities)
	}
	// a stored view carries the name
	post(h, `{"key":"`+testKey+`","attributes":{"$user_id":"u1","$user_name":"Ada"},
	  "events":[{"name":"$page_view","attributes":{"$path":"/"}}]}`, nil)
	if len(q.views) != 1 || len(q.identities) != 1 || q.identities[0].Name != "Ada" {
		t.Fatalf("stored view: views=%d identities=%+v", len(q.views), q.identities)
	}
}

func TestBatchNamesAreDedupedAcrossEvents(t *testing.T) {
	q, h := testServer(t)
	post(h, `{"key":"`+testKey+`","attributes":{"$user_id":"u1","$user_name":"Ada","$group_id":"g","$group_name":"G"},
	  "events":[{"name":"a"},{"name":"b"},{"name":"c"}]}`, nil)
	if len(q.identities) != 2 {
		t.Errorf("identities = %+v; want one user and one group, not one pair per event", q.identities)
	}
}

// --- removed endpoints, CORS, health ---

func TestOldEndpointsAreGone(t *testing.T) {
	_, h := testServer(t)
	for _, path := range []string{"/api/hit", "/api/event"} {
		r := httptest.NewRequest("POST", path, strings.NewReader("{}"))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, w.Code)
		}
	}
}

// TestLegacyIngestPathIsAnAlias: /api/events, the path before the API
// surface took /api/, still ingests and answers preflights exactly like
// /ingest/events, so cached SDKs and shipped native apps keep working.
func TestLegacyIngestPathIsAnAlias(t *testing.T) {
	q, h := testServer(t)
	r := httptest.NewRequest("POST", "/api/events",
		strings.NewReader(`{"events":[{"name":"subscribed"}]}`))
	r.Header.Set("X-Analytics-Key", testKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted || len(q.events) != 1 || q.events[0].EventName != "subscribed" {
		t.Fatalf("POST /api/events = %d %s, queued %+v", w.Code, w.Body.String(), q.events)
	}

	pre := httptest.NewRequest("OPTIONS", "/api/events", nil)
	pre.Header.Set("Origin", testOrigin)
	pre.Header.Set("Access-Control-Request-Method", "POST")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, pre)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != testOrigin {
		t.Fatalf("OPTIONS /api/events = %d %v", w.Code, w.Header())
	}
}

func TestPreflight(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("OPTIONS", "/ingest/events", nil)
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != testOrigin {
		t.Fatalf("preflight allowed origin: %d %v", w.Code, w.Header())
	}
	if !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "X-Analytics-Key") {
		t.Errorf("preflight must allow the key header: %q", w.Header().Get("Access-Control-Allow-Headers"))
	}
	r2 := httptest.NewRequest("OPTIONS", "/ingest/events", nil)
	r2.Header.Set("Origin", "https://evil.com")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("preflight must not allow unknown origins")
	}
}

func TestHealthz(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("healthz = %d", w.Code)
	}
}

// TestMountOnSharedMux verifies Mount registers the ingest surface's routes
// on a mux it doesn't own, alongside another surface's routes, without
// swallowing unmatched paths (no catch-all at "/").
func TestMountOnSharedMux(t *testing.T) {
	s := newTestServer(t)
	mux := http.NewServeMux()
	s.Mount(mux)
	mux.HandleFunc("GET /api/projects", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) })
	for target, want := range map[string]int{"/healthz": 200, "/js/twillingate.js": 200, "/api/projects": 299, "/nope": 404} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", target, rec.Code, want)
		}
	}
}

func TestKeyCountersRecordPerLabel(t *testing.T) {
	q, h := testServer(t)
	_ = q
	post(h, envelopeOf(`{"name":"a"},{"name":""}`), nil)
	srv, ok := h.(*Server)
	if !ok {
		t.Fatal("handler is not *Server")
	}
	counts := srv.Counters().Drain()
	if got := counts["web"]; got != [2]int{1, 1} {
		t.Errorf("counters[web] = %v, want [1 1]", got)
	}
	if len(srv.Counters().Drain()) != 0 {
		t.Error("Drain must reset the counters")
	}
}
