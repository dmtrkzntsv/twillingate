package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

// maxAPIBody caps a REST request body; larger bodies are refused as invalid.
const maxAPIBody = 1 << 20

// restHandler adapts one operation to HTTP: decode the request into In,
// run fn as actor "api", and answer with Out as JSON at s.Status (200 when
// unset) or with the refusal mapped by writeError.
func restHandler[In, Out any](r *registrar, s spec, fn func(context.Context, In) (Out, error)) http.HandlerFunc {
	status := s.Status
	if status == 0 {
		status = http.StatusOK
	}
	return func(w http.ResponseWriter, req *http.Request) {
		var in In
		if err := decodeRequest(req, &in); err != nil {
			writeError(w, r.logger, req, err)
			return
		}
		out, err := fn(withActor(req.Context(), "api"), in)
		if err != nil {
			writeError(w, r.logger, req, err)
			return
		}
		writeJSON(w, status, out)
	}
}

// decodeRequest fills dst from the request: query parameters for GET (and
// HEAD, which ServeMux routes to GET patterns), the JSON body otherwise, then
// path wildcards over both — the path is authoritative. Anything that names
// no field is refused, so a typo fails loudly rather than being ignored.
func decodeRequest(req *http.Request, dst any) error {
	fields := jsonFields(reflect.ValueOf(dst).Elem())
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		// url.ParseQuery, not URL.Query: Query drops pairs it cannot parse,
		// which would silently remove a filter and answer unfiltered.
		query, err := url.ParseQuery(req.URL.RawQuery)
		if err != nil {
			return invalidf("malformed query string: %v", err)
		}
		for name, vals := range query {
			f, ok := fields[name]
			if !ok {
				return invalidf("unknown query parameter %q; valid: %s", name, fieldNames(fields))
			}
			if len(vals) > 1 {
				return invalidf("query parameter %q given %d times; give it once", name, len(vals))
			}
			if err := setField(f, name, vals[0]); err != nil {
				return err
			}
		}
	} else {
		if req.URL.RawQuery != "" {
			return invalidf("%s takes a JSON body, not query parameters", req.Method)
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, maxAPIBody+1))
		if err != nil {
			return invalidf("reading request body: %v", err)
		}
		if len(body) > maxAPIBody {
			return invalidf("request body exceeds %d bytes", maxAPIBody)
		}
		if len(bytes.TrimSpace(body)) > 0 {
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.DisallowUnknownFields()
			if err := dec.Decode(dst); err != nil {
				return invalidf("request body: %v", err)
			}
			if dec.Decode(&struct{}{}) != io.EOF {
				return invalidf("request body must be a single JSON object")
			}
		}
	}
	for name, f := range fields {
		if v := req.PathValue(name); v != "" {
			if err := setField(f, name, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// jsonFields maps JSON names to settable fields, promoting embedded structs
// the way encoding/json does (rangeIn inside breakdownIn).
func jsonFields(v reflect.Value) map[string]reflect.Value {
	out := map[string]reflect.Value{}
	if v.Kind() != reflect.Struct {
		return out
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.Anonymous && sf.Type.Kind() == reflect.Struct {
			for k, f := range jsonFields(v.Field(i)) {
				out[k] = f
			}
			continue
		}
		if !sf.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(sf.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		out[name] = v.Field(i)
	}
	return out
}

func fieldNames(fields map[string]reflect.Value) string {
	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func setField(f reflect.Value, name, raw string) error {
	switch f.Kind() {
	case reflect.String:
		f.SetString(raw)
	case reflect.Int:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return invalidf("%s must be an integer, got %q", name, raw)
		}
		f.SetInt(int64(n))
	case reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return invalidf("%s must be an integer, got %q", name, raw)
		}
		f.SetInt(n)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return invalidf("%s must be true or false, got %q", name, raw)
		}
		f.SetBool(b)
	default:
		return invalidf("%s cannot be set from the URL; send it in a JSON body", name)
	}
	return nil
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError maps a refusal to its status by errors.Is. Untyped errors are
// logged (with the request that triggered them, so a 500 in journalctl
// names a method and path rather than just an error string) and answered
// generically: their text may carry internals.
func writeError(w http.ResponseWriter, logger *slog.Logger, req *http.Request, err error) {
	status, code, msg := http.StatusInternalServerError, "internal", "internal error"
	switch {
	case errors.Is(err, manage.ErrInvalid):
		status, code, msg = http.StatusBadRequest, "invalid", err.Error()
	case errors.Is(err, manage.ErrNotFound):
		status, code, msg = http.StatusNotFound, "not_found", err.Error()
	case errors.Is(err, manage.ErrConflict):
		status, code, msg = http.StatusConflict, "conflict", err.Error()
	default:
		logger.Error("api request failed", "method", req.Method, "path", req.URL.Path, "error", err)
	}
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg}})
}
