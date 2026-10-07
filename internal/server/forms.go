package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/wire"
	"github.com/google/uuid"
)

// formName bounds a form's name, which is a path segment, the suffix of
// the redirect fragments and a key in the console's URLs.
var formName = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// How a submission arrived, stored in submissions.via.
const (
	viaForm = "form" // a plain HTML form post, answered with a redirect
	viaJSON = "json" // anything else, answered with 201 {"id"}
)

// The fragments a redirect carries, followed by the form's name.
const (
	fragmentSuccess = "twillingate-form-success-"
	fragmentError   = "twillingate-form-error-"
)

// formContext is the reserved half of a submission: $ fields on the form
// path, $ attributes on JSON. Their meanings match events; Redirect is read
// on the form path only.
type formContext struct{ ID, UserID, InstallID, Host, Path, Redirect string }

// set records one context key, reporting whether k is one. Values are cut
// as event attributes are, except $redirect, which a cut would turn into
// another URL (the body limit bounds it) and $id, which must parse whole.
func (c *formContext) set(k, v string, redirect bool) bool {
	switch k {
	case "$id":
		c.ID = v
	case "$user_id":
		c.UserID = truncate(v, wire.MaxAttrValue)
	case "$install_id":
		c.InstallID = truncate(v, wire.MaxAttrValue)
	case "$host":
		c.Host = truncate(v, wire.MaxAttrValue)
	case "$path":
		c.Path = truncate(v, wire.MaxAttrValue)
	case "$redirect":
		if !redirect {
			return false
		}
		c.Redirect = v
	default:
		return false
	}
	return true
}

// flattenForm turns a plain form's values into the stored shape: every
// name not starting with $ is a field, a repeated name (a checkbox group,
// a multi-select) is joined with ", ", and the field limits apply
// (keepFields). A $ name is a context key, its first value taken, or
// dropped when it is no context key.
func flattenForm(v url.Values) (map[string]string, formContext) {
	var ctx formContext
	names := make([]string, 0, len(v))
	for k, vals := range v {
		if strings.HasPrefix(k, "$") {
			if len(vals) > 0 {
				ctx.set(k, vals[0], true)
			}
			continue
		}
		names = append(names, k)
	}
	return keepFields(names, func(k string) string { return strings.Join(v[k], ", ") }), ctx
}

// flattenJSON is flattenForm for a JSON body: fields hold strings,
// numbers or booleans, stored as their string form; an array, object or
// null drops the field, as does a $ name. attributes carry the context
// keys ($redirect is the form path's only); anything else there is
// dropped.
func flattenJSON(fields, attributes map[string]any) (map[string]string, formContext) {
	vals := make(map[string]string, len(fields))
	for k, v := range fields {
		if strings.HasPrefix(k, "$") {
			continue
		}
		if s, ok := scalar(v); ok {
			vals[k] = s
		}
	}
	names := make([]string, 0, len(vals))
	for k := range vals {
		names = append(names, k)
	}
	var ctx formContext
	for k, v := range attributes {
		if s, ok := scalar(v); ok {
			ctx.set(k, s, false)
		}
	}
	return keepFields(names, func(k string) string { return vals[k] }), ctx
}

// scalar renders a JSON string, number or boolean; anything else is not a
// value a submission keeps.
func scalar(v any) (string, bool) {
	switch v.(type) {
	case string, float64, bool:
		return stringify(v), true
	}
	return "", false
}

// keepFields applies the field limits: an empty name or one longer than
// MaxFormFieldName characters is dropped, at most MaxFormFields names are kept (the
// first in name order, so which survive never depends on map order), and
// each value is cut to MaxFormValue bytes.
func keepFields(names []string, value func(string) string) map[string]string {
	kept := names[:0]
	for _, k := range names {
		if k != "" && utf8.RuneCountInString(k) <= wire.MaxFormFieldName {
			kept = append(kept, k)
		}
	}
	sort.Strings(kept)
	if len(kept) > wire.MaxFormFields {
		kept = kept[:wire.MaxFormFields]
	}
	out := make(map[string]string, len(kept))
	for _, k := range kept {
		out[k] = cutValue(value(k))
	}
	return out
}

// cutValue truncates s to MaxFormValue bytes without splitting a rune.
func cutValue(s string) string {
	if len(s) <= wire.MaxFormValue {
		return s
	}
	n := wire.MaxFormValue
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// redirectTarget returns the first allowed of candidates with its
// fragment replaced by fragment, or "" when none is allowed. What allowed
// means is manage's (Snapshot.RedirectAllowed): an absolute http(s) URL
// whose origin passes allowed_origins, and through a bare "*" only when it
// is the request's own Origin.
func redirectTarget(snap *manage.Snapshot, projectID int64, reqOrigin, fragment string, candidates ...string) string {
	for _, c := range candidates {
		if c == "" || !snap.RedirectAllowed(projectID, c, reqOrigin) {
			continue
		}
		u, err := url.Parse(c)
		if err != nil {
			continue
		}
		u.Fragment, u.RawFragment = fragment, ""
		return u.String()
	}
	return ""
}

// formBody is a decoded request body.
type formBody struct {
	key    string // JSON only: the body's key
	id     string // JSON only: the top-level id
	fields map[string]string
	ctx    formContext
}

// parseFormBody decodes raw by its style: urlencoded or multipart on the
// form path (file parts skipped unread), JSON otherwise.
func parseFormBody(mediaType string, params map[string]string, raw []byte) (formBody, error) {
	switch mediaType {
	case "application/x-www-form-urlencoded":
		v, err := url.ParseQuery(string(raw))
		if err != nil {
			return formBody{}, err
		}
		fields, ctx := flattenForm(v)
		return formBody{fields: fields, ctx: ctx}, nil
	case "multipart/form-data":
		v, err := multipartValues(raw, params["boundary"])
		if err != nil {
			return formBody{}, err
		}
		fields, ctx := flattenForm(v)
		return formBody{fields: fields, ctx: ctx}, nil
	}
	var jb struct {
		Key        string         `json:"key"`
		ID         string         `json:"id"`
		Fields     map[string]any `json:"fields"`
		Attributes map[string]any `json:"attributes"`
	}
	if err := json.Unmarshal(raw, &jb); err != nil {
		return formBody{}, err
	}
	fields, ctx := flattenJSON(jb.Fields, jb.Attributes)
	return formBody{key: jb.Key, id: jb.ID, fields: fields, ctx: ctx}, nil
}

// multipartValues reads a multipart body's ordinary parts. A file part
// (any part whose disposition names a filename, an empty one included) is
// skipped unread: files are never stored, though they count toward the
// body limit.
func multipartValues(raw []byte, boundary string) (url.Values, error) {
	if boundary == "" {
		return nil, errors.New("multipart body without a boundary")
	}
	mr := multipart.NewReader(bytes.NewReader(raw), boundary)
	v := url.Values{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return v, nil
		}
		if err != nil {
			return nil, err
		}
		if _, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition")); err == nil {
			if _, file := params["filename"]; file {
				continue
			}
		}
		name := part.FormName()
		if name == "" {
			continue
		}
		b, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}
		v.Add(name, string(b))
	}
}

// pageOf is the submission's host and path: the $host and $path context
// keys, each falling back to the Referer's.
func pageOf(ctx formContext, referer string) (host, path string) {
	host, path = ctx.Host, ctx.Path
	if host != "" && path != "" {
		return host, path
	}
	if u, err := url.Parse(referer); err == nil && u.Host != "" {
		if host == "" {
			host = truncate(u.Host, wire.MaxAttrValue)
		}
		if path == "" {
			path = truncate(u.Path, wire.MaxAttrValue)
		}
	}
	return host, path
}

// handleForm is POST /ingest/forms/{name}: a plain HTML form post
// (urlencoded or multipart), answered with a 303 back to the site, or a
// JSON body, answered with 201 {"id"}. The checks run in order: the name,
// the key (plain 401), the Origin (plain 403), the body limit, the form
// being open (the store's), and the redirect target. A refusal after the
// key and Origin redirects with the error fragment when a target is
// allowed, else answers its plain status. Field values are never logged.
func (s *Server) handleForm(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !formName.MatchString(name) {
		http.Error(w, "bad form name", http.StatusBadRequest)
		return
	}
	mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	via := viaJSON
	if mediaType == "application/x-www-form-urlencoded" || mediaType == "multipart/form-data" {
		via = viaForm
	}
	ctx := r.Context()
	snap := s.reg.Snapshot(ctx)
	reqOrigin := r.Header.Get("Origin")

	var p *manage.Project
	var label string
	// authorize resolves the key and checks the Origin: the two plain
	// refusals, since without a project nothing can vouch for a target.
	authorize := func(key string) bool {
		var ok bool
		if p, label, ok = snap.ProjectByKey(key); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
		if reqOrigin != "" && !s.originAllowed(w, r, p.ID) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return false
		}
		return true
	}
	// refuse answers a refusal after authorize: the error fragment on the
	// form path when one of candidates is allowed, else the plain status.
	refuse := func(code int, msg string, candidates ...string) {
		if via == viaForm && p != nil {
			if target := redirectTarget(snap, p.ID, reqOrigin, fragmentError+name, candidates...); target != "" {
				http.Redirect(w, r, target, http.StatusSeeOther)
				return
			}
		}
		http.Error(w, msg, code)
	}

	// The form path's key is in the action URL (or the header), so it is
	// checked before the body is read; JSON may carry its key in the body.
	key := r.Header.Get("X-Analytics-Key")
	if key == "" {
		key = r.URL.Query().Get("key")
	}
	if (via == viaForm || key != "") && !authorize(key) {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, wire.MaxFormBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			// Nothing of the form is read yet: only the Referer can be a target.
			refuse(http.StatusRequestEntityTooLarge, "form too large", r.Referer())
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	body, err := parseFormBody(mediaType, params, raw)
	if err != nil {
		refuse(http.StatusBadRequest, "bad request", r.Referer())
		return
	}
	if p == nil && !authorize(body.key) {
		return
	}
	fc := body.ctx

	id := body.id
	if id == "" {
		id = fc.ID
	}
	if id == "" {
		id = newID()
	} else if _, err := uuid.Parse(id); err != nil {
		refuse(http.StatusBadRequest, "id is not a valid UUID", fc.Redirect, r.Referer())
		return
	}

	salt, err := s.salt.Current(ctx)
	if err != nil {
		s.logger.Error("salt unavailable", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	received := time.Now().UTC()
	ip := clientIP(r)
	actor, actorKind, user, _ := resolveIdentity(resolved{UserID: fc.UserID, InstallID: fc.InstallID},
		salt, ip, r.UserAgent(), strconv.FormatInt(p.ID, 10))
	host, path := pageOf(fc, r.Referer())
	visit, err := s.forms.SessionVisit(ctx, p.ID, actorKind, actor, received)
	if err != nil {
		// The visit is context, not the submission: store it without one.
		s.logger.Warn("form session visit failed", "project", p.ID, "form", name, "error", err)
		visit = nil
	}
	form, inserted, err := s.forms.WriteSubmission(ctx, store.NewSubmission{
		Submission: store.Submission{
			ProjectID: p.ID, ID: id, Form: name, ReceivedAt: received, Fields: body.fields,
			ActorKind: actorKind, ActorID: actor, Host: host, Path: path, Via: via, Visit: visit,
		},
		DraftUntil: received.AddDate(0, 0, s.cfg.Forms.DraftDays),
		// Written by the store only when the form is approved.
		Event: store.Event{
			ID: id, ProjectID: p.ID, Family: store.FamilyProduct, EventName: store.FormSubmitEvent,
			TS: received, ReceivedAt: received,
			ActorID: actor, ActorKind: actorKind, UserID: user,
			Host: host, Path: path, Country: s.geo.Country(r, ip),
			Attributes: map[string]string{"form": name},
		},
	})
	switch {
	case errors.Is(err, store.ErrFormClosed):
		s.counters.record(label, 0, 1)
		refuse(http.StatusConflict, "form closed", fc.Redirect, form.ReturnURL, r.Referer())
		return
	case err != nil:
		s.logger.Error("form submission failed", "project", p.ID, "form", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if inserted {
		s.counters.record(label, 1, 0)
		if actorKind != store.ActorConnection {
			s.noteIDs(p.ID, actorKind)
		}
	}

	if via == viaJSON {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(map[string]string{"id": id}); err != nil {
			s.logger.Error("write response", "error", err)
		}
		return
	}
	target := redirectTarget(snap, p.ID, reqOrigin, fragmentSuccess+name, fc.Redirect, form.ReturnURL, r.Referer())
	if target == "" {
		// Stored all the same: only the way back is missing.
		http.Error(w, "form has no return URL", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
