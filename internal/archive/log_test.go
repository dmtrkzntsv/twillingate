package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// TestLineWritesTheFields: a fully populated event's line holds exactly the
// keys of fields plus its attributes, so the dump types every fixed key.
func TestLineWritesTheFields(t *testing.T) {
	v := 2.5
	e := store.Event{ID: "i", ProjectID: 1, Family: store.FamilyProduct, EventName: "n",
		TS: ts("2026-10-12T10:00:00Z"), ReceivedAt: ts("2026-10-12T10:00:01Z"), Kind: "web",
		ActorID: "a", ActorKind: store.ActorConnection, UserID: "u", GroupID: "g", SessionID: "s",
		InstallID: "in", UserName: "un", GroupName: "gn",
		Host: "h", Path: "/p", ReferrerSource: "r", UTMSource: "us", UTMMedium: "um", UTMCampaign: "uc",
		Platform: "pl", OS: "o", OSVersion: "ov", OSName: "on", Browser: "b", BrowserVersion: "bv",
		AppVersion: "av", AppLocale: "al", BrowserLocale: "bl", Device: "d", DeviceModel: "dm",
		DisplayWidth: 1, DisplayHeight: 2, Country: "c", Consent: store.ConsentGiven,
		Attributes: map[string]string{"plan": "pro"}, Value: &v, Measure: store.MeasureTime, SampleRate: 0.5}
	rv := reflect.ValueOf(e)
	for i := 0; i < rv.NumField(); i++ {
		if rv.Field(i).IsZero() {
			t.Fatalf("store.Event.%s is not set: populate every field", rv.Type().Field(i).Name)
		}
	}
	b, err := line(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var got, want []string
	for k := range m {
		got = append(got, k)
	}
	for _, f := range fields {
		want = append(want, f.name)
	}
	want = append(want, "attr_plan")
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("line keys = %v, want fields plus attr_plan: %v", got, want)
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

func ev(id string, project int64, at string) store.Event {
	return store.Event{ID: id, ProjectID: project, Family: store.FamilyProduct, EventName: "x", TS: ts(at)}
}

// TestFailedSyncRemembersNoID: a flush whose fsync failed is retried by the
// pipeline; the retry must write and sync again, not pass as already done.
func TestFailedSyncRemembersNoID(t *testing.T) {
	l := openLog(t)
	l.failSync = func(*os.File) error { return errors.New("disk on fire") }
	e := ev("a", 1, "2026-10-12T10:00:00Z")
	if err := l.WriteEvents(context.Background(), []store.Event{e}); err == nil {
		t.Fatal("WriteEvents = nil, want the fsync error")
	}
	if len(l.seen) != 0 {
		t.Fatalf("seen = %v after a failed flush, want none", l.seen)
	}
	synced := 0
	l.failSync = func(f *os.File) error { synced++; return f.Sync() }
	if err := l.WriteEvents(context.Background(), []store.Event{e}); err != nil {
		t.Fatal(err)
	}
	if synced != 1 {
		t.Fatalf("the retry synced %d files, want 1", synced)
	}
	if len(l.seen) != 1 {
		t.Fatalf("seen = %v after the retry, want the id", l.seen)
	}
	// The first attempt's bytes were appended too: a duplicate line the
	// dump's DISTINCT ON drops.
	if n := len(lines(t, filepath.Join(l.Dir(), "1", "2026-10-12.log"))); n != 2 {
		t.Fatalf("%d lines, want 2", n)
	}
}

// TestFailedSecondFileSyncsTheFirstOnRetry: a batch spanning two logs whose
// second append fails leaves the first unsynced; the retry must sync it.
func TestFailedSecondFileSyncsTheFirstOnRetry(t *testing.T) {
	l := openLog(t)
	sep := string(os.PathSeparator)
	l.failWrite = func(f *os.File, b []byte) (int, error) {
		if strings.Contains(f.Name(), sep+"2"+sep) {
			return f.Write(b[:len(b)/2])
		}
		return f.Write(b)
	}
	batch := []store.Event{ev("a", 1, "2026-10-12T10:00:00Z"), ev("b", 2, "2026-10-12T10:00:00Z")}
	if err := l.WriteEvents(context.Background(), batch); err == nil {
		t.Fatal("WriteEvents = nil, want the short write's error")
	}
	if len(l.seen) != 0 {
		t.Fatalf("seen = %v after a failed batch, want none (file 1 was never synced)", l.seen)
	}
	l.failWrite = nil
	var synced []string
	l.failSync = func(f *os.File) error { synced = append(synced, f.Name()); return f.Sync() }
	if err := l.WriteEvents(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if len(synced) != 2 {
		t.Fatalf("the retry synced %v, want both logs", synced)
	}
	if n := len(lines(t, filepath.Join(l.Dir(), "2", "2026-10-12.log"))); n != 1 {
		t.Fatalf("project 2: %d lines, want 1 (the failed write was cut back)", n)
	}
}

// TestFailedCutBackReopensAndCutsTheTail: when the cut-back itself fails the
// half line stays on disk; the descriptor is dropped, and the next append
// reopens the file and cuts the tail before writing.
func TestFailedCutBackReopensAndCutsTheTail(t *testing.T) {
	l := openLog(t)
	path := filepath.Join(l.Dir(), "1", "2026-10-12.log")
	if err := l.WriteEvents(context.Background(), []store.Event{ev("a", 1, "2026-10-12T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	l.failWrite = func(f *os.File, b []byte) (int, error) { return f.Write(b[:len(b)/2]) }
	l.failTruncate = func(*os.File, int64) error { return errors.New("cannot truncate") }
	if err := l.WriteEvents(context.Background(), []store.Event{ev("b", 1, "2026-10-12T11:00:00Z")}); err == nil {
		t.Fatal("WriteEvents = nil, want an error")
	}
	if b, _ := os.ReadFile(path); bytes.HasSuffix(b, []byte("\n")) {
		t.Fatalf("log = %q, want the half line still on disk (the cut-back failed)", b)
	}
	if len(l.files) != 0 {
		t.Fatalf("%d descriptors kept after a failed cut-back, want none", len(l.files))
	}
	l.failWrite, l.failTruncate = nil, nil
	if err := l.WriteEvents(context.Background(), []store.Event{ev("b", 1, "2026-10-12T11:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	if n := len(lines(t, path)); n != 2 { // lines fails the test on a torn line
		t.Fatalf("%d lines, want 2", n)
	}
}

// TestCutTornLineReadsOnlyTheTail covers a torn tail longer than a read
// chunk, a log with no newline at all, and a clean log.
func TestCutTornLineReadsOnlyTheTail(t *testing.T) {
	dir := t.TempDir()
	good := strings.Repeat("{\"id\":\"a\"}\n", 3)
	for name, tc := range map[string]struct{ in, want string }{
		"long tail":  {good + strings.Repeat("x", 10000), good},
		"no newline": {strings.Repeat("x", 5000), ""},
		"clean":      {good, good},
		"empty":      {"", ""},
	} {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".log")
		if err := os.WriteFile(p, []byte(tc.in), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := cutTornLine(p); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b, _ := os.ReadFile(p); string(b) != tc.want {
			t.Errorf("%s: log = %d bytes, want %d", name, len(b), len(tc.want))
		}
	}
}

// TestRotateReturnsStatErrors: a Stat error other than "not there" ends
// Rotate (and releases the lock) instead of looping on it. Needs a directory
// the process cannot search, so it is skipped as root.
func TestRotateReturnsStatErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root searches any directory")
	}
	l := openLog(t)
	if err := l.WriteEvents(context.Background(), []store.Event{ev("a", 1, "2026-10-12T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(l.Dir(), "1")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o750) })
	done := make(chan error, 1)
	go func() { _, err := l.Rotate(1, "2026-10-12"); done <- err }()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, os.ErrPermission) {
			t.Fatalf("Rotate error = %v, want a permission error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Rotate hangs on a Stat error")
	}
}

// TestOpenLogLocksTheDirectory: while one Log is open on a directory,
// opening another fails with ErrLocked (the server and another command,
// or two ingest processes, never write raw/ at once); once it is closed,
// the next one opens.
func TestOpenLogLocksTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "raw")
	l, err := OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLog(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second OpenLog: %v, want ErrLocked", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := OpenLog(dir)
	if err != nil {
		t.Fatalf("OpenLog after Close: %v", err)
	}
	again.Close()
}

// TestMoveDirRefusesWhileLocked: a directory a Log holds is not moved
// (ErrLocked, the files where they were); once the Log is closed it is,
// lock file and all, and a Log opens on the new path.
func TestMoveDirRefusesWhileLocked(t *testing.T) {
	from, to := filepath.Join(t.TempDir(), "raw"), filepath.Join(t.TempDir(), "data")
	l, err := OpenLog(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := MoveDir(from, to); !errors.Is(err, ErrLocked) {
		t.Fatalf("MoveDir while held: %v, want ErrLocked", err)
	}
	if _, err := os.Stat(to); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("moved while held: %v", err)
	}
	l.Close()
	if err := MoveDir(from, to); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(from); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s still there: %v", from, err)
	}
	l, err = OpenLog(to)
	if err != nil {
		t.Fatalf("OpenLog after the move: %v", err)
	}
	l.Close()
}
