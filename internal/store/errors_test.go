package store

import (
	"errors"
	"testing"
)

// TestRefuse exercises Refuse/Refusal directly in this package: callers
// (internal/api's invalidf/notFoundf) exercise the same code, but that
// coverage isn't attributed back to internal/store without -coverpkg, so
// the vocabulary needs its own direct test.
func TestRefuse(t *testing.T) {
	err := Refuse(ErrInvalid, "name must not be %q", "")
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("Refuse(ErrInvalid, ...) is not ErrInvalid: %v", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("Refuse(ErrInvalid, ...) unexpectedly Is ErrNotFound: %v", err)
	}
	// The message is the formatted sentence alone: fmt.Errorf("%w: …")
	// would have prepended ErrInvalid's own text ("invalid: ...").
	if got, want := err.Error(), `name must not be ""`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatal("Refuse did not return a *Refusal")
	}
	if r.Unwrap() != ErrInvalid {
		t.Errorf("Unwrap() = %v, want ErrInvalid", r.Unwrap())
	}
}
