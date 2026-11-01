package store

import "testing"

func TestMonitorCheckPending(t *testing.T) {
	for _, c := range []struct {
		requested, finished string
		want                bool
	}{
		{"", "", false},
		{"2026-11-01T10:00:00Z", "", true},
		{"2026-11-01T10:00:05Z", "2026-11-01T10:00:00Z", true},
		{"2026-11-01T10:00:05Z", "2026-11-01T10:00:05Z", false},
		{"2026-11-01T10:00:05Z", "2026-11-01T10:00:09Z", false},
	} {
		m := Monitor{CheckRequestedAt: c.requested, CheckFinishedAt: c.finished}
		if got := m.CheckPending(); got != c.want {
			t.Errorf("requested %q finished %q: pending %t, want %t", c.requested, c.finished, got, c.want)
		}
	}
}
