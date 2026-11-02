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

// A finished request with no result at or after it did not run; a result in its
// own second counts as its own.
func TestMonitorCheckNotRun(t *testing.T) {
	const req = "2026-11-01T10:00:05Z"
	for _, c := range []struct {
		name                      string
		requested, finished, last string
		want                      bool
	}{
		{"no request", "", "", "", false},
		{"no request, a scheduled result", "", "", req, false},
		{"pending", req, "", "", false},
		{"finished, never a result", req, req, "", true},
		{"finished, an older result", req, req, "2026-11-01T10:00:04Z", true},
		{"finished, in its second", req, req, req, false},
		{"finished, a later result", req, req, "2026-11-01T10:00:09Z", false},
	} {
		m := Monitor{CheckRequestedAt: c.requested, CheckFinishedAt: c.finished, Last: MonitorResult{CheckedAt: c.last}}
		if got := m.CheckNotRun(); got != c.want {
			t.Errorf("%s: not run %t, want %t", c.name, got, c.want)
		}
	}
}
