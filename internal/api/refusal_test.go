package api

import (
	"context"
	"errors"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

func TestRefusalKeepsMessage(t *testing.T) {
	err := invalidf("from and to must be YYYY-MM-DD, got %q and %q", "x", "y")
	if !errors.Is(err, manage.ErrInvalid) || err.Error() != `from and to must be YYYY-MM-DD, got "x" and "y"` {
		t.Errorf("invalidf = %v (Is invalid: %v)", err, errors.Is(err, manage.ErrInvalid))
	}
	nf := notFoundf("unknown project %q", "zzz")
	if !errors.Is(nf, manage.ErrNotFound) || errors.Is(nf, manage.ErrInvalid) || nf.Error() != `unknown project "zzz"` {
		t.Errorf("notFoundf = %v", nf)
	}
}

func TestOperationRefusalsAreTyped(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := context.Background()
	cases := map[string]struct {
		err  error
		kind error
	}{
		"bad day":         {h.checkRange(ctx, rangeIn{Project: "blog", From: "yesterday", To: "2026-08-21"}), manage.ErrInvalid},
		"unknown project": {h.checkRange(ctx, rangeIn{Project: "nope", From: "2026-08-20", To: "2026-08-21"}), manage.ErrNotFound},
	}
	for name, c := range cases {
		if !errors.Is(c.err, c.kind) {
			t.Errorf("%s: %v is not %v", name, c.err, c.kind)
		}
	}
}
