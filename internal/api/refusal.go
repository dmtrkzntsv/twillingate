package api

import (
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

// refusal is an operation error an edge can classify with errors.Is while
// its text stays the sentence written for a model to recover from: wrapping
// with fmt.Errorf("%w: …") would prepend the sentinel's own text.
type refusal struct {
	kind error
	msg  string
}

func (e *refusal) Error() string { return e.msg }
func (e *refusal) Unwrap() error { return e.kind }

func invalidf(format string, args ...any) error {
	return &refusal{kind: manage.ErrInvalid, msg: fmt.Sprintf(format, args...)}
}

func notFoundf(format string, args ...any) error {
	return &refusal{kind: manage.ErrNotFound, msg: fmt.Sprintf(format, args...)}
}
