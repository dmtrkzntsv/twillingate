package reporting

import (
	"errors"
	"net/http"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// HTTPRefusal is how the REST routes answer one kind of refusal: the
// API's (api's writeError) and reporting dev's (writeAPIErr) alike.
type HTTPRefusal struct {
	Kind   error // the store's sentinel, matched with errors.Is
	Status int
	Code   string
}

// httpRefusals are the refusal kinds in the order they are checked: an
// error that matches more than one gets the first. Anything else is a
// 500 "internal", which each server words its own way.
var httpRefusals = []HTTPRefusal{
	{store.ErrInvalid, http.StatusBadRequest, "invalid"},
	{store.ErrNotFound, http.StatusNotFound, "not_found"},
	{store.ErrConflict, http.StatusConflict, "conflict"},
	{store.ErrBusy, http.StatusTooManyRequests, "busy"},
	{store.ErrTimeout, http.StatusGatewayTimeout, "timeout"},
}

// HTTPRefusalOf is the first of httpRefusals err is; false for none.
func HTTPRefusalOf(err error) (HTTPRefusal, bool) {
	for _, r := range httpRefusals {
		if errors.Is(err, r.Kind) {
			return r, true
		}
	}
	return HTTPRefusal{}, false
}
