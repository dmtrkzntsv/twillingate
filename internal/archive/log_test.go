package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func lines(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, l := range bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n")) {
		if len(l) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatalf("line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func openLog(t *testing.T) *Log {
	t.Helper()
	l, err := OpenLog(filepath.Join(t.TempDir(), "raw"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestLineHasTheRowShape(t *testing.T) {
	v := 2.5
	e := store.Event{ID: "0af59e70-fb48-4e2b-8bc0-03e93be19ea0", ProjectID: 3, Family: store.FamilyMeasures,
		EventName: "load", TS: ts("2026-10-12T23:59:59Z"), ReceivedAt: ts("2026-10-13T00:00:04Z"),
		Kind: "web", ActorID: "a1", ActorKind: store.ActorConnection, Path: "/x", DisplayWidth: 1280,
		Consent: store.ConsentNone, Attributes: map[string]string{"plan": "pro", "a-b c": "1"},
		Value: &v, Measure: store.MeasureTime}
	b, err := line(e)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(b, []byte("\n")) || bytes.Count(b, []byte("\n")) != 1 {
		t.Fatalf("line %q: want exactly one trailing newline", b)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"v": 1.0, "id": e.ID, "project_id": 3.0, "family": "measures", "event_name": "load",
		"ts": "2026-10-12T23:59:59Z", "day": "2026-10-12", "received_at": "2026-10-13T00:00:04Z",
		"kind": "web", "actor_id": "a1", "actor_kind": "connection", "path": "/x", "display_width": 1280.0,
		"consent": 0.0, "value": 2.5, "measure": "time", "sample_rate": 1.0,
		"attr_plan": "pro", "attr_a-b c": "1",
	}
	if len(m) != len(want) {
		t.Errorf("fields = %v, want exactly %v (empty ones omitted)", m, want)
	}
	for k, w := range want {
		if m[k] != w {
			t.Errorf("%s = %#v, want %#v", k, m[k], w)
		}
	}
}

func TestWriteEventsAppendsPerProjectAndDay(t *testing.T) {
	l := openLog(t)
	evs := []store.Event{
		{ID: "a", ProjectID: 1, Family: store.FamilyViews, EventName: "$page_view", TS: ts("2026-10-12T10:00:00Z")},
		{ID: "b", ProjectID: 1, Family: store.FamilyViews, EventName: "$page_view", TS: ts("2026-10-12T11:00:00Z")},
		{ID: "c", ProjectID: 1, Family: store.FamilyProduct, EventName: "x", TS: ts("2026-10-11T23:00:00Z")},
		{ID: "d", ProjectID: 2, Family: store.FamilyProduct, EventName: "x", TS: ts("2026-10-12T00:00:00Z")},
	}
	if err := l.WriteEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
	for path, n := range map[string]int{"1/2026-10-12.log": 2, "1/2026-10-11.log": 1, "2/2026-10-12.log": 1} {
		if got := len(lines(t, filepath.Join(l.Dir(), path))); got != n {
			t.Errorf("%s: %d lines, want %d", path, got, n)
		}
	}
}

// TestWriteEventsSkipsRecentIDs: a batch the pipeline retries (or an SDK
// retry within minutes) is written once.
func TestWriteEventsSkipsRecentIDs(t *testing.T) {
	l := openLog(t)
	now := ts("2026-10-12T10:00:00Z")
	l.now = func() time.Time { return now }
	e := store.Event{ID: "a", ProjectID: 1, Family: store.FamilyProduct, EventName: "x", TS: now}
	for i := 0; i < 2; i++ {
		if err := l.WriteEvents(context.Background(), []store.Event{e, e}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(l.Dir(), "1", "2026-10-12.log")
	if n := len(lines(t, path)); n != 1 {
		t.Fatalf("%d lines, want 1", n)
	}
	now = now.Add(recentIDs + time.Second)
	if err := l.WriteEvents(context.Background(), []store.Event{e}); err != nil {
		t.Fatal(err)
	}
	if n := len(lines(t, path)); n != 2 {
		t.Fatalf("after the window: %d lines, want 2 (the dump's DISTINCT ON drops it)", n)
	}
}

// TestOpenLogCutsATornLine is Review Focus 1.
func TestOpenLogCutsATornLine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "raw")
	path := filepath.Join(dir, "1", "2026-10-12.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"id\":\"a\"}\n{\"id\":\"b\"}\n{\"id\":\"to"), 0o640); err != nil {
		t.Fatal(err)
	}
	l, err := OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	b, _ := os.ReadFile(path)
	if string(b) != "{\"id\":\"a\"}\n{\"id\":\"b\"}\n" {
		t.Fatalf("log = %q, want the torn line cut", b)
	}
	if err := l.WriteEvents(context.Background(), []store.Event{{ID: "c", ProjectID: 1, Family: store.FamilyProduct, EventName: "x", TS: ts("2026-10-12T12:00:00Z")}}); err != nil {
		t.Fatal(err)
	}
	if n := len(lines(t, path)); n != 3 {
		t.Fatalf("%d lines after an append, want 3", n)
	}
}

// TestFailedWriteLeavesNoPartialLine is Review Focus 1's other half: a
// write that fails midway is cut back, so the next append starts clean.
func TestFailedWriteLeavesNoPartialLine(t *testing.T) {
	l := openLog(t)
	e := store.Event{ID: "a", ProjectID: 1, Family: store.FamilyProduct, EventName: "x", TS: ts("2026-10-12T10:00:00Z")}
	if err := l.WriteEvents(context.Background(), []store.Event{e}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(l.Dir(), "1", "2026-10-12.log")
	before, _ := os.ReadFile(path)
	l.failWrite = func(f *os.File, b []byte) (int, error) { return f.Write(b[:len(b)/2]) }
	bad := store.Event{ID: "b", ProjectID: 1, Family: store.FamilyProduct, EventName: "x", TS: ts("2026-10-12T11:00:00Z")}
	if err := l.WriteEvents(context.Background(), []store.Event{bad}); err == nil {
		t.Fatal("WriteEvents = nil, want the short write's error")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatalf("log after a failed write = %q, want it as before", after)
	}
	l.failWrite = nil
	if err := l.WriteEvents(context.Background(), []store.Event{bad}); err != nil {
		t.Fatalf("the retry: %v (a failed id must not count as written)", err)
	}
	if n := len(lines(t, path)); n != 2 {
		t.Fatalf("%d lines after the retry, want 2", n)
	}
}

func TestRotateClosesTheDayAndANewLogStarts(t *testing.T) {
	l := openLog(t)
	e := store.Event{ID: "a", ProjectID: 1, Family: store.FamilyProduct, EventName: "x", TS: ts("2026-10-12T10:00:00Z")}
	if err := l.WriteEvents(context.Background(), []store.Event{e}); err != nil {
		t.Fatal(err)
	}
	closed, err := l.Rotate(1, "2026-10-12")
	if err != nil || !strings.HasSuffix(closed, "2026-10-12.1.closed") {
		t.Fatalf("Rotate = %q, %v", closed, err)
	}
	late := store.Event{ID: "b", ProjectID: 1, Family: store.FamilyProduct, EventName: "x", TS: ts("2026-10-12T22:00:00Z")}
	if err := l.WriteEvents(context.Background(), []store.Event{late}); err != nil {
		t.Fatal(err)
	}
	if n := len(lines(t, filepath.Join(l.Dir(), "1", "2026-10-12.log"))); n != 1 {
		t.Fatalf("new log: %d lines, want the late event alone", n)
	}
	if again, err := l.Rotate(1, "2026-10-12"); err != nil || !strings.HasSuffix(again, "2026-10-12.2.closed") {
		t.Fatalf("second Rotate = %q, %v; want the next free number", again, err)
	}
	if none, err := l.Rotate(1, "2026-10-12"); err != nil || none != "" {
		t.Fatalf("Rotate with no log = %q, %v; want \"\"", none, err)
	}
}
