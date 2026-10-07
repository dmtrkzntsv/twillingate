package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/geo"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/wire"
	"github.com/google/uuid"
)

// ---- flattening ----

func TestFlattenForm(t *testing.T) {
	v := url.Values{
		"email":       {"a@b.c"},
		"topics":      {"pricing", "support"},
		"redirect":    {"not a context key"},
		"id":          {"plain field"},
		"host":        {"plain field too"},
		"$redirect":   {"https://app.com/thanks"},
		"$id":         {"018f1e5c-0000-7000-8000-000000000000"},
		"$user_id":    {"u1"},
		"$install_id": {"i1"},
		"$host":       {"app.com"},
		"$path":       {"/contact"},
		"$session_id": {"dropped: not a form context key"},
		"$whatever":   {"dropped"},
		"":            {"no name"},
		strings.Repeat("n", wire.MaxFormFieldName):   {"kept"},
		strings.Repeat("n", wire.MaxFormFieldName+1): {"dropped"},
		strings.Repeat("é", wire.MaxFormFieldName):   {"kept: 64 characters, 128 bytes"},
	}
	fields, ctx := flattenForm(v)
	want := map[string]string{
		"email": "a@b.c", "topics": "pricing, support",
		"redirect": "not a context key", "id": "plain field", "host": "plain field too",
		strings.Repeat("n", wire.MaxFormFieldName): "kept",
		strings.Repeat("é", wire.MaxFormFieldName): "kept: 64 characters, 128 bytes",
	}
	if !maps.Equal(fields, want) {
		t.Errorf("fields = %v\nwant %v", fields, want)
	}
	wantCtx := formContext{ID: "018f1e5c-0000-7000-8000-000000000000", UserID: "u1", InstallID: "i1",
		Host: "app.com", Path: "/contact", Redirect: "https://app.com/thanks"}
	if ctx != wantCtx {
		t.Errorf("ctx = %+v\nwant %+v", ctx, wantCtx)
	}
}

// A value is cut to MaxFormValue bytes without splitting a rune; a joined
// repeat is cut after joining.
func TestFlattenFormTruncatesOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("a", wire.MaxFormValue-1) + "é" + "tail" // é is 2 bytes, straddling the limit
	fields, _ := flattenForm(url.Values{"msg": {long}, "ok": {strings.Repeat("b", wire.MaxFormValue)}})
	got := fields["msg"]
	if got != strings.Repeat("a", wire.MaxFormValue-1) || !utf8.ValidString(got) {
		t.Errorf("msg truncated to %d bytes (valid %v), want %d", len(got), utf8.ValidString(got), wire.MaxFormValue-1)
	}
	if len(fields["ok"]) != wire.MaxFormValue {
		t.Errorf("a value of exactly the limit was cut to %d", len(fields["ok"]))
	}
}

// Past MaxFormFields names the rest are dropped, in name order, so which
// survive does not depend on map iteration.
func TestFlattenFormCapsFieldCount(t *testing.T) {
	v := url.Values{}
	for i := range wire.MaxFormFields + 1 {
		v.Set(fmt.Sprintf("f%03d", i), "x")
	}
	fields, _ := flattenForm(v)
	if len(fields) != wire.MaxFormFields {
		t.Fatalf("%d fields kept, want %d", len(fields), wire.MaxFormFields)
	}
	if _, ok := fields[fmt.Sprintf("f%03d", wire.MaxFormFields)]; ok {
		t.Error("the last name in order was kept; the 101st should be dropped")
	}
}

// A field name holding a control character (U+0000 to U+001F, U+007F) is
// dropped, on both paths: a NUL would cut the SQL that reads the field
// short, and none of them names a real form control.
func TestFlattenDropsControlCharacterNames(t *testing.T) {
	bad := []string{"a\x00b", "\x00", "tab\there", "new\nline", "del\x7f", "esc\x1b"}
	v := url.Values{"ok": {"1"}}
	j := map[string]any{"ok": "1"}
	for _, n := range bad {
		v.Set(n, "x")
		j[n] = "x"
	}
	want := map[string]string{"ok": "1"}
	if fields, _ := flattenForm(v); !maps.Equal(fields, want) {
		t.Errorf("form fields = %q, want %q", fields, want)
	}
	if fields, _ := flattenJSON(j, nil); !maps.Equal(fields, want) {
		t.Errorf("json fields = %q, want %q", fields, want)
	}
}

func TestFlattenJSON(t *testing.T) {
	var body struct {
		Fields     map[string]any `json:"fields"`
		Attributes map[string]any `json:"attributes"`
	}
	if err := json.Unmarshal([]byte(`{
		"fields": {"email": "a@b.c", "seats": 5, "ratio": 0.5, "agree": true, "tags": ["a"], "obj": {"x": 1},
		           "none": null, "$host": "dropped", "redirect": "a field"},
		"attributes": {"$id": "018f1e5c-0000-7000-8000-000000000000", "$user_id": 42, "$install_id": "i1",
		               "$host": "app.com", "$path": "/p", "$redirect": "https://app.com/ignored-on-json",
		               "$session_id": "dropped", "plan": "dropped: not a field"}}`), &body); err != nil {
		t.Fatal(err)
	}
	fields, ctx := flattenJSON(body.Fields, body.Attributes)
	want := map[string]string{"email": "a@b.c", "seats": "5", "ratio": "0.5", "agree": "true", "redirect": "a field"}
	if !maps.Equal(fields, want) {
		t.Errorf("fields = %v\nwant %v", fields, want)
	}
	wantCtx := formContext{ID: "018f1e5c-0000-7000-8000-000000000000", UserID: "42", InstallID: "i1", Host: "app.com", Path: "/p"}
	if ctx != wantCtx {
		t.Errorf("ctx = %+v\nwant %+v", ctx, wantCtx)
	}
}

// ---- redirect targets ----

func redirectSnapshot(t *testing.T, origins ...[]string) *manage.Snapshot {
	t.Helper()
	var specs []manage.ProjectSpec
	for i, o := range origins {
		specs = append(specs, manage.ProjectSpec{Name: "P" + strconv.Itoa(i), AllowedOrigins: o})
	}
	return newTestRegistry(t, specs, nil).Snapshot(context.Background())
}

func TestRedirectTarget(t *testing.T) {
	// Project 1 allows app.com, 2 only a bare "*", 3 nothing.
	snap := redirectSnapshot(t, []string{"https://app.com"}, []string{"*"}, nil)
	const frag = "twillingate-form-success-contact"
	for _, c := range []struct {
		name       string
		project    int64
		reqOrigin  string
		candidates []string
		want       string
	}{
		{"$redirect first", 1, "", []string{"https://app.com/thanks", "https://app.com/return", "https://app.com/page"}, "https://app.com/thanks#" + frag},
		{"return_url when $redirect is foreign", 1, "", []string{"https://evil.com/x", "https://app.com/return", "https://app.com/page"}, "https://app.com/return#" + frag},
		{"return_url when $redirect is absent", 1, "", []string{"", "https://app.com/return", "https://app.com/page"}, "https://app.com/return#" + frag},
		{"Referer last", 1, "", []string{"", "", "https://app.com/page?x=1"}, "https://app.com/page?x=1#" + frag},
		{"javascript: is never a target", 1, "", []string{"javascript:alert(1)", "", "https://app.com/"}, "https://app.com/#" + frag},
		{"a relative URL is never a target", 1, "", []string{"/thanks", "", ""}, ""},
		{"a foreign origin is never a target", 1, "", []string{"https://evil.com/", "", ""}, ""},
		{"an existing fragment is replaced", 1, "", []string{"https://app.com/thanks#top"}, "https://app.com/thanks#" + frag},
		{"bare * with the request's own Origin", 2, "https://site.com", []string{"https://site.com/thanks"}, "https://site.com/thanks#" + frag},
		{"bare * with another Origin", 2, "https://other.com", []string{"https://site.com/thanks"}, ""},
		{"bare * with no Origin", 2, "", []string{"https://site.com/thanks"}, ""},
		{"empty allowed_origins", 3, "https://app.com", []string{"https://app.com/thanks"}, ""},
		{"no candidates", 1, "", nil, ""},
	} {
		if got := redirectTarget(snap, c.project, c.reqOrigin, frag, c.candidates...); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// ---- the handler ----

// fakeForms is the FormStore: it keeps forms and submissions in maps, so a
// test can assert on what the handler handed the store.
type fakeForms struct {
	mu     sync.Mutex
	forms  map[string]store.Form
	subs   map[string]store.NewSubmission
	calls  []store.NewSubmission
	closed bool  // every new submission is refused with ErrFormClosed
	err    error // every call fails with err
	visit  *store.Visit
	visits [][2]string // (actor kind, actor id) per SessionVisit call
}

func newFakeForms() *fakeForms {
	return &fakeForms{forms: map[string]store.Form{}, subs: map[string]store.NewSubmission{}}
}

func (f *fakeForms) WriteSubmission(_ context.Context, n store.NewSubmission) (store.Form, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, n)
	if f.err != nil {
		return store.Form{}, false, f.err
	}
	sub := n.Submission
	form, ok := f.forms[sub.Form]
	if !ok {
		du := n.DraftUntil
		form = store.Form{ProjectID: sub.ProjectID, Name: sub.Form, Status: store.FormDraft, DraftUntil: &du}
	}
	if _, dup := f.subs[sub.ID]; dup {
		return form, false, nil
	}
	if f.closed {
		return form, false, fmt.Errorf("%w: form %q: it is closed", store.ErrFormClosed, sub.Form)
	}
	f.forms[sub.Form] = form
	f.subs[sub.ID] = n
	return form, true, nil
}

func (f *fakeForms) SessionVisit(_ context.Context, _ int64, actorKind, actorID string, _ time.Time) (*store.Visit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.visits = append(f.visits, [2]string{actorKind, actorID})
	return f.visit, nil
}

// formServer is a server over one project (allowed origin testOrigin, key
// testKey) whose FormStore is forms, logging to logger.
func formServer(t *testing.T, forms *fakeForms, logger *slog.Logger) *Server {
	t.Helper()
	return formServerAllowing(t, forms, logger, testOrigin)
}

// formServerAllowing is formServer with the project's allowed_origins set
// to origins.
func formServerAllowing(t *testing.T, forms *fakeForms, logger *slog.Logger, origins ...string) *Server {
	t.Helper()
	cfg := configtest.Load(t, nil)
	reg := newTestRegistry(t,
		[]manage.ProjectSpec{{Name: "App", AllowedOrigins: origins}},
		map[int][2]string{0: {testKey, "web"}})
	g, _ := geo.New("cloudflare://", t.TempDir(), slog.Default())
	q := &fakeQueue{}
	return New(cfg, reg, q, g, fixedSalt{}, q, forms, logger)
}

// postForm sends one request to /ingest/forms/<name><query> with a Chrome
// UA and a CF country; headers override or add.
func postForm(h http.Handler, name, query, contentType string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/ingest/forms/"+name+query, body)
	r.Header.Set("User-Agent", chromeUA)
	r.Header.Set("CF-IPCountry", "DE")
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const urlencoded = "application/x-www-form-urlencoded"

func postURLEncoded(h http.Handler, name string, v url.Values, headers map[string]string) *httptest.ResponseRecorder {
	return postForm(h, name, "?key="+testKey, urlencoded, strings.NewReader(v.Encode()), headers)
}

func postJSON(h http.Handler, name, body string, headers map[string]string) *httptest.ResponseRecorder {
	return postForm(h, name, "", "text/plain;charset=UTF-8", strings.NewReader(body), headers)
}

func wantRedirect(t *testing.T, w *httptest.ResponseRecorder, location string) {
	t.Helper()
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != location {
		t.Fatalf("got %d Location %q (body %q), want 303 to %q", w.Code, w.Header().Get("Location"), w.Body.String(), location)
	}
}

func wantPlain(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code || w.Header().Get("Location") != "" {
		t.Fatalf("got %d Location %q (body %q), want a plain %d", w.Code, w.Header().Get("Location"), w.Body.String(), code)
	}
}

func TestFormURLEncodedRedirectsWithSuccess(t *testing.T) {
	forms := newFakeForms()
	h := formServer(t, forms, slog.Default())
	w := postURLEncoded(h, "contact", url.Values{
		"email": {"a@b.c"}, "topics": {"x", "y"}, "$redirect": {"https://app.com/thanks#top"},
	}, map[string]string{"Origin": testOrigin, "Referer": "https://app.com/contact"})
	wantRedirect(t, w, "https://app.com/thanks#twillingate-form-success-contact")
	if len(forms.calls) != 1 {
		t.Fatalf("store called %d times, want 1", len(forms.calls))
	}
	sub := forms.calls[0].Submission
	if sub.Form != "contact" || sub.Via != "form" || sub.ProjectID != 1 ||
		!maps.Equal(sub.Fields, map[string]string{"email": "a@b.c", "topics": "x, y"}) {
		t.Errorf("submission = %+v", sub)
	}
	if _, err := uuid.Parse(sub.ID); err != nil {
		t.Errorf("server-made id %q is not a UUID", sub.ID)
	}
	// host and path come from the Referer when no context key names them.
	if sub.Host != "app.com" || sub.Path != "/contact" {
		t.Errorf("host/path = %q %q, want app.com /contact from the Referer", sub.Host, sub.Path)
	}
}

func TestFormMultipartIgnoresFileParts(t *testing.T) {
	forms := newFakeForms()
	h := formServer(t, forms, slog.Default())
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("email", "a@b.c")
	mw.WriteField("$redirect", "https://app.com/thanks")
	fw, _ := mw.CreateFormFile("cv", "cv.pdf")
	fw.Write([]byte("%PDF-1.4 secret"))
	fw, _ = mw.CreateFormFile("empty", "") // a file input left empty still carries filename=""
	fw.Write(nil)
	mw.Close()
	w := postForm(h, "apply", "?key="+testKey, mw.FormDataContentType(), &buf, nil)
	wantRedirect(t, w, "https://app.com/thanks#twillingate-form-success-apply")
	if got := forms.calls[0].Submission.Fields; !maps.Equal(got, map[string]string{"email": "a@b.c"}) {
		t.Errorf("fields = %v, want only email", got)
	}
}

// The key also works as the X-Analytics-Key header on the form path.
func TestFormKeyFromHeader(t *testing.T) {
	h := formServer(t, newFakeForms(), slog.Default())
	w := postForm(h, "contact", "", urlencoded, strings.NewReader("email=a&%24redirect=https%3A%2F%2Fapp.com%2F"),
		map[string]string{"X-Analytics-Key": testKey})
	wantRedirect(t, w, "https://app.com/#twillingate-form-success-contact")
}

func TestFormRefusals(t *testing.T) {
	forms := newFakeForms()
	h := formServer(t, forms, slog.Default())
	body := url.Values{"email": {"a"}, "$redirect": {"https://app.com/thanks"}}.Encode()
	for _, c := range []struct {
		name, form, query, contentType, body string
		headers                              map[string]string
		code                                 int
	}{
		{"bad key", "contact", "?key=nope", urlencoded, body, nil, http.StatusUnauthorized},
		{"no key", "contact", "", urlencoded, body, nil, http.StatusUnauthorized},
		{"bad key on JSON", "contact", "", "text/plain", `{"key":"nope","fields":{"a":"b"}}`, nil, http.StatusUnauthorized},
		{"disallowed Origin", "contact", "?key=" + testKey, urlencoded, body, map[string]string{"Origin": "https://evil.com"}, http.StatusForbidden},
		{"Origin null", "contact", "?key=" + testKey, urlencoded, body, map[string]string{"Origin": "null"}, http.StatusForbidden},
		{"Origin null on JSON", "contact", "", "text/plain", `{"key":"` + testKey + `","fields":{"a":"b"}}`, map[string]string{"Origin": "null"}, http.StatusForbidden},
		{"upper-case name", "Contact", "?key=" + testKey, urlencoded, body, nil, http.StatusBadRequest},
		{"name too long", strings.Repeat("a", 65), "?key=" + testKey, urlencoded, body, nil, http.StatusBadRequest},
		{"name with a dot", "con.tact", "?key=" + testKey, urlencoded, body, nil, http.StatusBadRequest},
		{"malformed JSON", "contact", "", "application/json", `{"key":`, map[string]string{"X-Analytics-Key": testKey}, http.StatusBadRequest},
		{"bad id", "contact", "", "application/json", `{"key":"` + testKey + `","id":"nope"}`, nil, http.StatusBadRequest},
	} {
		w := postForm(h, c.form, c.query, c.contentType, strings.NewReader(c.body), c.headers)
		if w.Code != c.code || w.Header().Get("Location") != "" {
			t.Errorf("%s: got %d Location %q, want a plain %d", c.name, w.Code, w.Header().Get("Location"), c.code)
		}
	}
	if len(forms.calls) != 0 {
		t.Errorf("store called %d times for refused requests", len(forms.calls))
	}
}

func TestFormJSON(t *testing.T) {
	forms := newFakeForms()
	h := formServer(t, forms, slog.Default())
	w := postJSON(h, "contact", `{"key":"`+testKey+`","fields":{"email":"a@b.c","seats":5},
		"attributes":{"$host":"app.com","$path":"/pricing","$install_id":"i1"}}`, map[string]string{"Origin": testOrigin})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body %q, want 201", w.Code, w.Body.String())
	}
	if w.Header().Get("Access-Control-Allow-Origin") != testOrigin {
		t.Errorf("ACAO = %q, want the allowed origin echoed", w.Header().Get("Access-Control-Allow-Origin"))
	}
	var out struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	sub := forms.calls[0].Submission
	if out.ID == "" || out.ID != sub.ID || sub.Via != "json" || sub.Host != "app.com" || sub.Path != "/pricing" ||
		sub.ActorKind != store.ActorInstall || sub.ActorID != "i1" ||
		!maps.Equal(sub.Fields, map[string]string{"email": "a@b.c", "seats": "5"}) {
		t.Errorf("answer %q, submission %+v", out.ID, sub)
	}
}

// A retried id answers 201 with the same id and is stored once; the id
// may come as the top-level id or the $id attribute.
func TestFormJSONRetryStoresOnce(t *testing.T) {
	forms := newFakeForms()
	h := formServer(t, forms, slog.Default())
	const id = "018f1e5c-0000-7000-8000-000000000001"
	for _, body := range []string{
		`{"key":"` + testKey + `","id":"` + id + `","fields":{"a":"b"}}`,
		`{"key":"` + testKey + `","fields":{"a":"b"},"attributes":{"$id":"` + id + `"}}`,
	} {
		w := postJSON(h, "contact", body, nil)
		if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), id) {
			t.Fatalf("got %d %q, want 201 with id %s", w.Code, w.Body.String(), id)
		}
	}
	if len(forms.calls) != 2 || len(forms.subs) != 1 {
		t.Errorf("calls %d, stored %d; want 2 calls, 1 stored", len(forms.calls), len(forms.subs))
	}
}

// A closed form answers the error fragment on the form path, reading the
// return_url off the form WriteSubmission hands back, and 409 on JSON.
func TestFormClosed(t *testing.T) {
	forms := newFakeForms()
	forms.closed = true
	forms.forms["contact"] = store.Form{ProjectID: 1, Name: "contact", Status: store.FormApproved, ReturnURL: "https://app.com/back"}
	h := formServer(t, forms, slog.Default())
	w := postURLEncoded(h, "contact", url.Values{"email": {"a"}}, map[string]string{"Referer": "https://app.com/contact"})
	wantRedirect(t, w, "https://app.com/back#twillingate-form-error-contact")
	// No allowed target at all: the plain refusal.
	w = postURLEncoded(h, "contact2", url.Values{"email": {"a"}}, nil)
	wantPlain(t, w, http.StatusConflict)
	w = postJSON(h, "contact", `{"key":"`+testKey+`","fields":{"a":"b"}}`, nil)
	wantPlain(t, w, http.StatusConflict)
}

// With no allowed target the submission is still stored and the answer is
// a plain 400 saying so.
func TestFormWithoutReturnURL(t *testing.T) {
	forms := newFakeForms()
	h := formServer(t, forms, slog.Default())
	w := postURLEncoded(h, "contact", url.Values{"email": {"a"}, "$redirect": {"https://evil.com/"}},
		map[string]string{"Referer": "https://evil.com/page"})
	wantPlain(t, w, http.StatusBadRequest)
	if !strings.Contains(w.Body.String(), "form has no return URL") {
		t.Errorf("body = %q", w.Body.String())
	}
	if len(forms.subs) != 1 {
		t.Errorf("stored %d submissions, want 1 (it is stored before the redirect is chosen)", len(forms.subs))
	}
}

// An oversize body is never read into a submission: on the form path the
// error fragment goes to the Referer when it is allowed, else 413; JSON
// is always 413.
func TestFormBodyTooLarge(t *testing.T) {
	forms := newFakeForms()
	h := formServer(t, forms, slog.Default())
	big := "msg=" + strings.Repeat("x", wire.MaxFormBody) + "&%24redirect=https%3A%2F%2Fapp.com%2Fthanks"
	w := postForm(h, "contact", "?key="+testKey, urlencoded, strings.NewReader(big), map[string]string{"Referer": "https://app.com/contact"})
	wantRedirect(t, w, "https://app.com/contact#twillingate-form-error-contact")
	w = postForm(h, "contact", "?key="+testKey, urlencoded, strings.NewReader(big), nil)
	wantPlain(t, w, http.StatusRequestEntityTooLarge)
	w = postJSON(h, "contact", `{"key":"`+testKey+`","fields":{"msg":"`+strings.Repeat("x", wire.MaxFormBody)+`"}}`, nil)
	wantPlain(t, w, http.StatusRequestEntityTooLarge)
	// A file part counts toward the limit though it is discarded.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("email", "a@b.c")
	fw, _ := mw.CreateFormFile("cv", "cv.pdf")
	fw.Write(bytes.Repeat([]byte("x"), wire.MaxFormBody))
	mw.Close()
	w = postForm(h, "contact", "?key="+testKey, mw.FormDataContentType(), &buf, nil)
	wantPlain(t, w, http.StatusRequestEntityTooLarge)
	if len(forms.calls) != 0 {
		t.Errorf("store called %d times for oversize bodies", len(forms.calls))
	}
}

// The handler fills the event the store writes on an approved form: the
// submission's id, instant and actor, family product, $form_submit, the
// page, the country and only the form's name as attribute.
func TestFormEventPrefilled(t *testing.T) {
	forms := newFakeForms()
	forms.forms["contact"] = store.Form{ProjectID: 1, Name: "contact", Status: store.FormApproved}
	forms.visit = &store.Visit{LandingPath: "/", Views: 3}
	h := formServer(t, forms, slog.Default())
	before := time.Now().UTC()
	w := postURLEncoded(h, "contact", url.Values{
		"email": {"a"}, "$user_id": {"u1"}, "$host": {"app.com"}, "$path": {"/contact"},
		"$id": {"018f1e5c-0000-7000-8000-000000000002"}, "$redirect": {"https://app.com/thanks"},
	}, map[string]string{"Referer": "https://other.app.com/ignored"})
	wantRedirect(t, w, "https://app.com/thanks#twillingate-form-success-contact")
	n := forms.calls[0]
	sub, ev := n.Submission, n.Event
	if sub.ID != "018f1e5c-0000-7000-8000-000000000002" || sub.ActorKind != store.ActorUser || sub.ActorID != "u1" ||
		sub.Host != "app.com" || sub.Path != "/contact" || sub.Visit == nil || sub.Visit.Views != 3 {
		t.Errorf("submission = %+v", sub)
	}
	if ev.ID != sub.ID || ev.ProjectID != 1 || ev.Family != store.FamilyProduct || ev.EventName != store.FormSubmitEvent ||
		!ev.TS.Equal(sub.ReceivedAt) || !ev.ReceivedAt.Equal(sub.ReceivedAt) || sub.ReceivedAt.Before(before) ||
		ev.ActorKind != store.ActorUser || ev.ActorID != "u1" || ev.UserID != "u1" ||
		ev.Host != "app.com" || ev.Path != "/contact" || ev.Country != "DE" ||
		!maps.Equal(ev.Attributes, map[string]string{"form": "contact"}) {
		t.Errorf("event = %+v", ev)
	}
	if d := n.DraftUntil.Sub(sub.ReceivedAt); d != 7*24*time.Hour {
		t.Errorf("draft_until is %v after received, want FORMS_DRAFT_DAYS (7 days)", d)
	}
	if len(forms.visits) != 1 || forms.visits[0] != [2]string{store.ActorUser, "u1"} {
		t.Errorf("SessionVisit calls = %v", forms.visits)
	}
}

// A JS-free form post from the browser that sent the visit's views gets
// the same connection actor as those views.
func TestFormConnectionActorMatchesViews(t *testing.T) {
	forms := newFakeForms()
	cfg := configtest.Load(t, nil)
	reg := newTestRegistry(t, []manage.ProjectSpec{{Name: "App", AllowedOrigins: []string{testOrigin}}},
		map[int][2]string{0: {testKey, "web"}})
	g, _ := geo.New("cloudflare://", t.TempDir(), slog.Default())
	q := &fakeQueue{}
	h := New(cfg, reg, q, g, fixedSalt{}, q, forms, slog.Default())
	post(h, envelopeOf(`{"name":"$page_view","attributes":{"$path":"/"}}`), nil)
	postURLEncoded(h, "contact", url.Values{"email": {"a"}, "$redirect": {"https://app.com/"}}, nil)
	if len(q.views) != 1 || len(forms.calls) != 1 {
		t.Fatalf("views %d, submissions %d", len(q.views), len(forms.calls))
	}
	sub := forms.calls[0].Submission
	if sub.ActorKind != store.ActorConnection || sub.ActorID != q.views[0].ActorID {
		t.Errorf("submission actor %s/%s, view actor %s/%s", sub.ActorKind, sub.ActorID, q.views[0].ActorKind, q.views[0].ActorID)
	}
}

// A store failure is a 500, and the log names neither a field's value nor
// its name.
func TestFormStoreErrorLogsNoValues(t *testing.T) {
	forms := newFakeForms()
	forms.err = errors.New("disk full")
	var buf bytes.Buffer
	h := formServer(t, forms, slog.New(slog.NewTextHandler(&buf, nil)))
	w := postURLEncoded(h, "contact", url.Values{"secret_field": {"hunter2-value"}, "$redirect": {"https://app.com/"}}, nil)
	wantPlain(t, w, http.StatusInternalServerError)
	if !strings.Contains(buf.String(), "disk full") {
		t.Errorf("log %q does not report the error", buf.String())
	}
	if strings.Contains(buf.String(), "hunter2-value") || strings.Contains(buf.String(), "secret_field") {
		t.Errorf("log carries a field: %q", buf.String())
	}
}

func TestFormPreflight(t *testing.T) {
	h := formServer(t, newFakeForms(), slog.Default())
	r := httptest.NewRequest("OPTIONS", "/ingest/forms/contact", nil)
	r.Header.Set("Origin", testOrigin)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != testOrigin {
		t.Errorf("preflight = %d ACAO %q", w.Code, w.Header().Get("Access-Control-Allow-Origin"))
	}
}

// $form_submit is the server's: a client sending it to /ingest/events is
// rejected per event, whatever family it declares, so it cannot forge a
// conversion.
func TestClientFormSubmitRejected(t *testing.T) {
	q, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"$form_submit","attributes":{"form":"contact"}},
		{"name":"$form_submit","family":"product"},
		{"name":"$form_submit","family":"measures","value":1,"measure":"number"},
		{"name":"ok"}`), nil)
	res := decodeResult(t, w)
	if res.Accepted != 1 || res.Rejected != 3 || len(q.events) != 1 || len(q.measures) != 0 {
		t.Fatalf("result %+v, events %d, measures %d; want only the ordinary event stored", res, len(q.events), len(q.measures))
	}
	for _, e := range res.Errors {
		if !strings.Contains(e.Reason, "$form_submit") {
			t.Errorf("reason %q does not name $form_submit", e.Reason)
		}
	}
}

// JSON takes its key from ?key= as well as the header and the body.
func TestFormJSONKeyFromQuery(t *testing.T) {
	forms := newFakeForms()
	h := formServer(t, forms, slog.Default())
	w := postForm(h, "contact", "?key="+testKey, "text/plain", strings.NewReader(`{"fields":{"a":"b"}}`), nil)
	if w.Code != http.StatusCreated || len(forms.subs) != 1 {
		t.Fatalf("got %d %q, stored %d; want 201 and one stored", w.Code, w.Body.String(), len(forms.subs))
	}
}

// A $id that is not a UUID is refused before anything is stored: the error
// fragment on $redirect when it is allowed, else a plain 400.
func TestFormBadIDRedirectsWithError(t *testing.T) {
	forms := newFakeForms()
	h := formServer(t, forms, slog.Default())
	w := postURLEncoded(h, "contact", url.Values{"$id": {"nope"}, "$redirect": {"https://app.com/thanks"}}, nil)
	wantRedirect(t, w, "https://app.com/thanks#twillingate-form-error-contact")
	w = postURLEncoded(h, "contact", url.Values{"$id": {"nope"}}, nil)
	wantPlain(t, w, http.StatusBadRequest)
	if len(forms.calls) != 0 {
		t.Errorf("store called %d times for a bad id", len(forms.calls))
	}
}

// On a project allowing a bare "*", $redirect is a target only when it is
// on the request's own Origin, so "*" never makes an open redirect.
func TestFormBareStarRedirectsOnlyToTheRequestOrigin(t *testing.T) {
	forms := newFakeForms()
	h := formServerAllowing(t, forms, slog.Default(), "*")
	v := url.Values{"email": {"a"}, "$redirect": {"https://site.com/thanks"}}
	w := postURLEncoded(h, "contact", v, map[string]string{"Origin": "https://site.com"})
	wantRedirect(t, w, "https://site.com/thanks#twillingate-form-success-contact")
	w = postURLEncoded(h, "contact", v, map[string]string{"Origin": "https://other.com"})
	wantPlain(t, w, http.StatusBadRequest)
	if len(forms.subs) != 2 {
		t.Errorf("stored %d, want both stored (the target only decides the answer)", len(forms.subs))
	}
}

// A closed form's error fragment goes to $redirect first, before the
// form's return_url, in the same order as a success.
func TestFormClosedPrefersRedirect(t *testing.T) {
	forms := newFakeForms()
	forms.closed = true
	forms.forms["contact"] = store.Form{ProjectID: 1, Name: "contact", Status: store.FormApproved, ReturnURL: "https://app.com/back"}
	h := formServer(t, forms, slog.Default())
	w := postURLEncoded(h, "contact", url.Values{"email": {"a"}, "$redirect": {"https://app.com/thanks"}},
		map[string]string{"Referer": "https://app.com/contact"})
	wantRedirect(t, w, "https://app.com/thanks#twillingate-form-error-contact")
}
