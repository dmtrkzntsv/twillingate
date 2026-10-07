package store

import (
	"maps"
	"testing"
	"time"
)

// These pure helpers are exercised through the server and the sqlite
// store, but that coverage is not attributed to this package without
// -coverpkg, so they get their own direct tests.
func TestFormOpen(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	cases := []struct {
		name string
		f    Form
		want bool
	}{
		{"draft in window", Form{Status: FormDraft, DraftUntil: &future}, true},
		{"draft past its window", Form{Status: FormDraft, DraftUntil: &past}, false},
		{"draft expiring now", Form{Status: FormDraft, DraftUntil: &now}, false},
		{"approved, no close", Form{Status: FormApproved}, true},
		{"approved, closes later", Form{Status: FormApproved, ClosesAt: &future}, true},
		{"approved, closed", Form{Status: FormApproved, ClosesAt: &past}, false},
		{"archived", Form{Status: FormApproved, ArchivedAt: &past}, false},
	}
	for _, c := range cases {
		if got := c.f.Open(now); got != c.want {
			t.Errorf("%s: Open = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestKeepFields(t *testing.T) {
	in := map[string]string{"email": "a@b.c", "note": "hi", "extra": "x"}
	if got := KeepFields(Form{Status: FormDraft}, in); !maps.Equal(got, in) {
		t.Errorf("draft keeps everything, got %v", got)
	}
	got := KeepFields(Form{Status: FormApproved, ExpectedFields: []string{"email", "note", "absent"}}, in)
	if want := map[string]string{"email": "a@b.c", "note": "hi"}; !maps.Equal(got, want) {
		t.Errorf("approved keeps expected only: got %v, want %v", got, want)
	}
}
