package api

import (
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func invalidf(format string, args ...any) error {
	return store.Refuse(manage.ErrInvalid, format, args...)
}

func notFoundf(format string, args ...any) error {
	return store.Refuse(manage.ErrNotFound, format, args...)
}
