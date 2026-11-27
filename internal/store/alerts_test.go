package store

import "testing"

func TestAlertCheckPending(t *testing.T) {
	for _, c := range []struct {
		name                 string
		requested, evaluated string
		want                 bool
	}{
		{"no request", "", "", false},
		{"no request, evaluated", "", "2026-11-01T10:00:00Z", false},
		{"requested, never evaluated", "2026-11-01T10:00:00Z", "", true},
		{"evaluated before the request", "2026-11-01T10:00:05Z", "2026-11-01T10:00:00Z", true},
		{"evaluated in the request's own second", "2026-11-01T10:00:05Z", "2026-11-01T10:00:05Z", false},
		{"evaluated after the request", "2026-11-01T10:00:05Z", "2026-11-01T10:00:09Z", false},
	} {
		a := Alert{CheckRequestedAt: c.requested, LastEvaluatedAt: c.evaluated}
		if got := a.CheckPending(); got != c.want {
			t.Errorf("%s: pending %t, want %t", c.name, got, c.want)
		}
	}
}
