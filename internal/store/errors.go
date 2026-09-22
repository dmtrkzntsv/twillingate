package store

import "errors"

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
)
