package store

import (
	"errors"
	"fmt"
)

// Registry outcomes a caller can act on, as opposed to storage failures
// it can only report. Backends wrap these with %w so the message still
// names the row ("update project: unknown id 7: not found") while
// errors.Is answers the question the edge actually has: does the caller
// need to pick a different id or label, or did the database break?
var (
	// ErrNotFound: the project or key named by the call does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict: the key label is already taken for that project.
	ErrConflict = errors.New("already exists")
	// ErrInvalid: the request failed validation before any write.
	ErrInvalid = errors.New("invalid")
)

// Refusal is an error an edge classifies with errors.Is (against ErrInvalid,
// ErrNotFound or ErrConflict) while its text stays the sentence written for
// the caller: fmt.Errorf("%w: …") would prepend the sentinel's own words
// ("invalid: …") instead of reading as one sentence.
type Refusal struct {
	Kind error
	Msg  string
}

func (e *Refusal) Error() string { return e.Msg }
func (e *Refusal) Unwrap() error { return e.Kind }

// Refuse builds a Refusal whose message is the formatted sentence and whose
// Kind is the sentinel a caller matches with errors.Is.
func Refuse(kind error, format string, args ...any) error {
	return &Refusal{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}
