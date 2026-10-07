package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func sharePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// shareForm writes a multipart body with fields and files, in that order.
func shareForm(t *testing.T, fields map[string]string, files map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range files {
		fw, err := mw.CreateFormFile(k, k+".png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(v); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &b, mw.FormDataContentType()
}

// shareRange is the last 30 days to today (UTC): create refuses a range
// that ends after today, and the handler runs on the real clock.
func shareRange() (string, string) {
	now := time.Now().UTC()
	return now.AddDate(0, 0, -29).Format(time.DateOnly), now.Format(time.DateOnly)
}

// shareFields and shareFiles are a valid create request for widgetID.
func shareFields(widgetID int64) map[string]string {
	from, to := shareRange()
	return map[string]string{"widget_id": fmt.Sprint(widgetID), "project_id": "1", "from": from, "to": to}
}

func shareFiles(t *testing.T) map[string][]byte {
	return map[string][]byte{"image": sharePNG(t, 1200, 630), "image_2x": sharePNG(t, 2400, 1260)}
}

// call serves one request on h, with the fixture's token when auth is set.
func call(h http.Handler, method, target string, body io.Reader, contentType string, auth bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if auth {
		req.Header.Set("Authorization", "Bearer ar_testtoken")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// shareFixture is the full console handler with PUBLIC_URL set, on a copy
// of newTestHost's database (which has the components installed), and the
// id of a markdown widget on a user dashboard.
func shareFixture(t *testing.T) (http.Handler, int64) {
	t.Helper()
	path := t.TempDir() + "/share.db"
	data, err := os.ReadFile(hostTemplate(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHandlerFixture(t, map[string]string{"DATABASE_DSN": "sqlite://" + path, "PUBLIC_URL": "https://c.example"})
	rec := call(h, "POST", "/api/dashboards", strings.NewReader(
		`{"title":"Notes","widgets":[{"component":"markdown","title":"Read me","source":{"type":"md","content":"Hi."}}]}`),
		"application/json", true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create dashboard = %d %s", rec.Code, rec.Body.String())
	}
	var d struct {
		Widgets []struct {
			ID int64 `json:"widget_id"`
		} `json:"widgets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || len(d.Widgets) != 1 {
		t.Fatalf("create dashboard body %s: %v", rec.Body.String(), err)
	}
	return h, d.Widgets[0].ID
}

type shareJSON struct {
	ID         string  `json:"id"`
	URL        string  `json:"url"`
	ImageURL   string  `json:"image_url"`
	WidgetID   *int64  `json:"widget_id"`
	ArchiveAt  *string `json:"archive_at"`
	ArchivedAt *string `json:"archived_at"`
}

func createShare(t *testing.T, h http.Handler, widgetID int64) shareJSON {
	t.Helper()
	body, ct := shareForm(t, shareFields(widgetID), shareFiles(t))
	rec := call(h, "POST", "/api/widget-shares", body, ct, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/widget-shares = %d %s", rec.Code, rec.Body.String())
	}
	var s shareJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("create body %s: %v", rec.Body.String(), err)
	}
	return s
}

func TestCreateWidgetShareREST(t *testing.T) {
	h, wid := shareFixture(t)
	s := createShare(t, h, wid)
	if len(s.ID) != 36 || s.URL != "https://c.example/share/"+s.ID || s.ImageURL != s.URL+".png" {
		t.Errorf("created share = %+v", s)
	}
	if want := time.Now().UTC().AddDate(0, 0, 30).Format(time.DateOnly); s.ArchiveAt == nil || !strings.HasPrefix(*s.ArchiveAt, want) {
		t.Errorf("archive_at = %v, want %s (30d by default)", s.ArchiveAt, want)
	}
	if s.WidgetID == nil || *s.WidgetID != wid || s.ArchivedAt != nil {
		t.Errorf("created share = %+v, want widget %d, live", s, wid)
	}
}

func TestCreateWidgetShareRESTCaptions(t *testing.T) {
	h, wid := shareFixture(t)
	for _, c := range []struct {
		project, rng string // "" = left out
		wantP, wantR bool
	}{
		{"", "", true, true},
		{"0", "1", false, true},
		{"true", "false", true, false},
		{"false", "0", false, false},
	} {
		f := shareFields(wid)
		if c.project != "" {
			f["caption_project"] = c.project
		}
		if c.rng != "" {
			f["caption_range"] = c.rng
		}
		body, ct := shareForm(t, f, shareFiles(t))
		rec := call(h, "POST", "/api/widget-shares", body, ct, true)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%+v: %d %s", c, rec.Code, rec.Body.String())
		}
		var got struct {
			CaptionProject bool `json:"caption_project"`
			CaptionRange   bool `json:"caption_range"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.CaptionProject != c.wantP || got.CaptionRange != c.wantR {
			t.Errorf("%+v: got %+v", c, got)
		}
	}
	f := shareFields(wid)
	f["caption_range"] = "maybe"
	body, ct := shareForm(t, f, shareFiles(t))
	if rec := call(h, "POST", "/api/widget-shares", body, ct, true); rec.Code != 400 || errorCode(t, rec) != "invalid" {
		t.Errorf("caption_range=maybe: %d %s, want 400 invalid", rec.Code, rec.Body.String())
	}
}

func TestCreateWidgetShareRESTRefusals(t *testing.T) {
	h, wid := shareFixture(t)
	with := func(edit func(f map[string]string, files map[string][]byte)) (*bytes.Buffer, string) {
		f, files := shareFields(wid), shareFiles(t)
		edit(f, files)
		return shareForm(t, f, files)
	}
	big := func(n int) []byte { return bytes.Repeat([]byte{0}, n) }
	cases := []struct {
		name   string
		body   func() (io.Reader, string)
		status int
		code   string
	}{
		{"json body", func() (io.Reader, string) {
			return strings.NewReader(fmt.Sprintf(`{"widget_id":%d}`, wid)), "application/json"
		}, 400, "invalid"},
		{"no image", func() (io.Reader, string) {
			return with(func(_ map[string]string, f map[string][]byte) { delete(f, "image") })
		}, 400, "invalid"},
		{"no image_2x", func() (io.Reader, string) {
			return with(func(_ map[string]string, f map[string][]byte) { delete(f, "image_2x") })
		}, 400, "invalid"},
		{"widget_id x", func() (io.Reader, string) {
			return with(func(f map[string]string, _ map[string][]byte) { f["widget_id"] = "x" })
		}, 400, "invalid"},
		{"6 MB image", func() (io.Reader, string) {
			return with(func(_ map[string]string, f map[string][]byte) { f["image"] = big(6 << 20) })
		}, 400, "invalid"},
		{"13 MB body", func() (io.Reader, string) {
			return with(func(_ map[string]string, f map[string][]byte) { f["image"], f["image_2x"] = big(6<<20), big(7<<20) })
		}, 400, "invalid"},
		{"swapped images", func() (io.Reader, string) {
			return with(func(_ map[string]string, f map[string][]byte) { f["image"], f["image_2x"] = f["image_2x"], f["image"] })
		}, 400, "invalid"},
		{"unknown field", func() (io.Reader, string) {
			return with(func(f map[string]string, _ map[string][]byte) { f["widget"] = "1" })
		}, 400, "invalid"},
		{"unknown widget", func() (io.Reader, string) {
			return with(func(f map[string]string, _ map[string][]byte) { f["widget_id"] = "99999" })
		}, 404, "not_found"},
	}
	for _, c := range cases {
		body, ct := c.body()
		rec := call(h, "POST", "/api/widget-shares", body, ct, true)
		if rec.Code != c.status || errorCode(t, rec) != c.code {
			t.Errorf("%s: %d %s, want %d %s", c.name, rec.Code, rec.Body.String(), c.status, c.code)
		}
	}
}

func TestWidgetShareRoutes(t *testing.T) {
	h, wid := shareFixture(t)
	s := createShare(t, h, wid)
	must := func(method, target, body string, want int) []byte {
		t.Helper()
		ct := ""
		if body != "" {
			ct = "application/json"
		}
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		rec := call(h, method, target, rd, ct, true)
		if rec.Code != want {
			t.Fatalf("%s %s = %d %s, want %d", method, target, rec.Code, rec.Body.String(), want)
		}
		return rec.Body.Bytes()
	}
	list := func(query string) []shareJSON {
		t.Helper()
		var out struct {
			Shares []shareJSON `json:"shares"`
		}
		if err := json.Unmarshal(must("GET", "/api/widget-shares"+query, "", 200), &out); err != nil {
			t.Fatal(err)
		}
		return out.Shares
	}
	one := func(b []byte) shareJSON {
		t.Helper()
		var v shareJSON
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	day := func(days int) string { return time.Now().UTC().AddDate(0, 0, days).Format(time.DateOnly) }

	if got := list("?state=live"); len(got) != 1 || got[0].ID != s.ID {
		t.Fatalf("live = %+v, want the share", got)
	}
	if got := list(fmt.Sprintf("?widget_id=%d", wid+1)); len(got) != 0 {
		t.Errorf("another widget's shares = %+v, want none", got)
	}
	p := "/api/widget-shares/" + s.ID
	if u := one(must("PATCH", p, `{"archive_after":"7d"}`, 200)); u.ArchiveAt == nil || !strings.HasPrefix(*u.ArchiveAt, day(7)) {
		t.Errorf("PATCH 7d: archive_at = %v, want %s", u.ArchiveAt, day(7))
	}
	if u := one(must("PATCH", p, `{"archive_after":"project"}`, 200)); u.ArchiveAt != nil {
		t.Errorf("PATCH project: archive_at = %v, want null", *u.ArchiveAt)
	}
	if a := one(must("POST", p+"/archive", "", 200)); a.ArchivedAt == nil {
		t.Errorf("archive: %+v, want archived_at", a)
	}
	if got := list("?state=archived"); len(got) != 1 || got[0].ID != s.ID {
		t.Errorf("archived = %+v, want the share", got)
	}
	if got := list("?state=live"); len(got) != 0 {
		t.Errorf("live after archive = %+v, want none", got)
	}
	must("PATCH", p, `{"archive_after":"7d"}`, http.StatusConflict)
	r := one(must("POST", p+"/restore", `{}`, 200))
	if r.ArchivedAt != nil || r.ArchiveAt == nil || !strings.HasPrefix(*r.ArchiveAt, day(30)) {
		t.Errorf("restore: %+v, want live, archive_at %s", r, day(30))
	}
	must("PATCH", p, `{"archive_after":"2w"}`, http.StatusBadRequest)
	must("POST", "/api/widget-shares/00000000-0000-7000-8000-000000000000/archive", "", http.StatusNotFound)
}

func TestWidgetShareImageREST(t *testing.T) {
	h, wid := shareFixture(t)
	s := createShare(t, h, wid)
	img := "/api/widget-shares/" + s.ID + "/image"
	if rec := call(h, "GET", img, nil, "", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a token = %d, want 401", rec.Code)
	}
	if rec := call(h, "POST", "/api/widget-shares/"+s.ID+"/archive", strings.NewReader("{}"), "application/json", true); rec.Code != 200 {
		t.Fatalf("archive = %d %s", rec.Code, rec.Body.String())
	}
	// The public image is gone with the share; this one is not.
	if rec := call(h, "GET", "/share/"+s.ID+".png", nil, "", false); rec.Code != http.StatusNotFound {
		t.Errorf("public image of an archived share = %d, want 404", rec.Code)
	}
	rec := call(h, "GET", img, nil, "", true)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") != "private, max-age=3600" {
		t.Fatalf("archived = %d %q %q, want 200 image/png private, max-age=3600", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	}
	if cfg, err := png.DecodeConfig(rec.Body); err != nil || cfg.Width != 1200 || cfg.Height != 630 {
		t.Errorf("body is %dx%d, %v; want the 1x 1200x630 png", cfg.Width, cfg.Height, err)
	}
	if rec := call(h, "GET", "/api/widget-shares/00000000-0000-7000-8000-000000000000/image", nil, "", true); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", rec.Code)
	}
}

func TestWidgetShareToolsOverMCP(t *testing.T) {
	h, cs := newTestHost(t)
	r := newTestRegistrar(t, h)
	var d struct {
		Widgets []struct {
			ID int64 `json:"widget_id"`
		} `json:"widgets"`
	}
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Notes", "widgets": []any{map[string]any{
		"component": "markdown", "source": map[string]any{"type": "md", "content": "Hi."}}}}, &d)
	body, ct := shareForm(t, shareFields(d.Widgets[0].ID), shareFiles(t))
	req := httptest.NewRequest("POST", "/api/widget-shares", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	r.rest.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var s shareJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, tool := range tools.Tools {
		listed[tool.Name] = true
	}
	for _, name := range []string{"list_widget_shares", "update_widget_share", "archive_widget_share", "restore_widget_share"} {
		if !listed[name] {
			t.Errorf("MCP does not list %s", name)
		}
	}
	if listed["create_widget_share"] {
		t.Error("create_widget_share is REST only, yet MCP lists it")
	}

	var out struct {
		Shares []shareJSON `json:"shares"`
	}
	toolJSON(t, cs, "list_widget_shares", map[string]any{"widget_id": d.Widgets[0].ID}, &out)
	if len(out.Shares) != 1 || out.Shares[0].ID != s.ID || out.Shares[0].URL != "https://c.example/share/"+s.ID {
		t.Fatalf("list_widget_shares = %+v, want %s", out.Shares, s.ID)
	}
	var a shareJSON
	toolJSON(t, cs, "archive_widget_share", map[string]any{"id": s.ID}, &a)
	if a.ArchivedAt == nil {
		t.Errorf("archive_widget_share = %+v", a)
	}
	toolJSON(t, cs, "restore_widget_share", map[string]any{"id": s.ID, "archive_after": "90d"}, &a)
	toolJSON(t, cs, "update_widget_share", map[string]any{"id": s.ID, "archive_after": "365d"}, &a)
	if a.ArchivedAt != nil || a.ArchiveAt == nil {
		t.Errorf("after restore and update = %+v, want live with a date", a)
	}
}

func TestSharePagesArePublic(t *testing.T) {
	h, wid := shareFixture(t)
	s := createShare(t, h, wid)

	rec := call(h, "GET", "/share/"+s.ID, nil, "", false)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("GET /share/<id> without a token = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, file := range []string{s.ID + ".png", s.ID + "@2x.png"} {
		rec = call(h, "GET", "/share/"+file, nil, "", false)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
			t.Errorf("GET /share/%s without a token = %d %q", file, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if rec = call(h, "GET", "/api/widget-shares", nil, "", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/widget-shares without a token = %d, want 401", rec.Code)
	}
	for _, target := range []string{"/share/", "/share/abc", "/share/" + s.ID + "/x"} {
		rec = call(h, "GET", target, nil, "", false)
		if rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("GET %s = %d, Cache-Control %q; want 404 no-store", target, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
}
