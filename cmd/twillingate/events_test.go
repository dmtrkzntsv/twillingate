package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestEventsImport: `events` alone prints its usage; `events import` on an
// install with no rows imports nothing and records it, and a second run
// says it ran.
func TestEventsImport(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"events"}, &out); code != 2 || !strings.Contains(out.String(), "usage") {
		t.Fatalf("events: exit %d, %q; want 2 and the usage", code, out.String())
	}
	out.Reset()
	if code := run([]string{"events", "import"}, &out); code != 0 || !strings.Contains(out.String(), "imported 0 of 0 rows") {
		t.Fatalf("events import: exit %d, %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"events", "import"}, &out); code != 0 || !strings.Contains(out.String(), "already imported") {
		t.Fatalf("events import again: exit %d, %q", code, out.String())
	}
}
