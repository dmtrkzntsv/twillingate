package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestKeyScannerReadsOnlyWhatWasAppended: a log only grows until it is
// rotated, so a reload reads from the first line not read yet; a torn
// last line is left for the next call; a line that is not an object is
// skipped, not fatal (the dump is the one to refuse it).
func TestKeyScannerReadsOnlyWhatWasAppended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "2026-10-12.log")
	first := `{"id":"1","attr_a":"x"}` + "\n" + `[1,2]` + "\n" + `not json` + "\n" + `{"id":"2","attr_b":"y"}` + "\n"
	appendTo(t, path, first)
	s := NewKeyScanner()
	keys, err := s.Keys(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedKeys(keys); !eq(got, []string{"attr_a", "attr_b", "id"}) {
		t.Fatalf("keys = %v", got)
	}
	appendTo(t, path, `{"id":"3","attr_c":"z"}`+"\n"+`{"id":"4","attr_d":`)
	keys, err = s.Keys(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedKeys(keys); !eq(got, []string{"attr_a", "attr_b", "attr_c", "id"}) {
		t.Fatalf("keys after an append with a torn line = %v; want attr_c and not attr_d", got)
	}
	if want := int64(len(first) + len(`{"id":"3","attr_c":"z"}`+"\n")); s.logs[path].next != want {
		t.Fatalf("next = %d, want %d: the torn line must be read again, nothing before it", s.logs[path].next, want)
	}
	appendTo(t, path, `"w"}`+"\n")
	keys, err = s.Keys(path)
	if err != nil {
		t.Fatal(err)
	}
	if !keys["attr_d"] {
		t.Fatalf("keys = %v; want attr_d once its line is complete", sortedKeys(keys))
	}
}

// TestKeyScannerStartsOverOnANewFile: a rotation renames the log, and the
// next event starts a new file at the same path, which must be read from
// its start, without the old file's keys.
func TestKeyScannerStartsOverOnANewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-10-12.log")
	appendTo(t, path, `{"id":"1","attr_a":"x","attr_long_enough_to_outgrow_the_next_file":"x"}`+"\n")
	s := NewKeyScanner()
	if _, err := s.Keys(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(dir, "2026-10-12.1.closed")); err != nil {
		t.Fatal(err)
	}
	appendTo(t, path, `{"id":"9","attr_z":"q"}`+"\n")
	keys, err := s.Keys(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedKeys(keys); !eq(got, []string{"attr_z", "id"}) {
		t.Fatalf("keys of the new file = %v; want attr_z and id only", got)
	}
	s.Keep([]string{path})
	if _, ok := s.logs[filepath.Join(dir, "2026-10-12.1.closed")]; ok {
		t.Fatal("keep left a log it was not given")
	}
}

func TestColumnsOfLeavesItsKeysAlone(t *testing.T) {
	keys := map[string]bool{"id": true, "ts": true, "attr_x": true}
	cols := columnsOf(keys)
	if len(keys) != 3 {
		t.Fatalf("columnsOf changed its keys: %v", keys)
	}
	if last := cols[len(cols)-1]; last.name != "attr_x" || len(cols) != len(fields)+1 {
		t.Fatalf("columns = %d, last %+v; want every field then attr_x", len(cols), last)
	}
}

// TestKeyScannerReturnsACopy: a caller may change the set Keys returns (the
// archive's columns read it while a second call may be reading the log);
// the scanner's own set, and so the next call, does not change.
func TestKeyScannerReturnsACopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "2026-10-12.log")
	appendTo(t, path, `{"id":"1","attr_a":"x"}`+"\n")
	s := NewKeyScanner()
	keys, err := s.Keys(path)
	if err != nil {
		t.Fatal(err)
	}
	keys["attr_gone"] = true
	delete(keys, "id")
	appendTo(t, path, `{"id":"2","attr_b":"y"}`+"\n")
	again, err := s.Keys(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedKeys(again); !eq(got, []string{"attr_a", "attr_b", "id"}) {
		t.Fatalf("keys after a caller changed its set = %v; want the log's keys only", got)
	}
}

// TestKeyScannerReadsAcrossChunks: a log read in chunks finds every key,
// including those of lines that straddle a chunk boundary or are longer
// than a chunk; next lands just after the last complete line, and a torn
// line after it is left for the next call.
func TestKeyScannerReadsAcrossChunks(t *testing.T) {
	old := keyChunk
	keyChunk = 16
	t.Cleanup(func() { keyChunk = old })
	path := filepath.Join(t.TempDir(), "2026-10-12.log")
	var b strings.Builder
	var want []string
	for i := range 20 {
		k := fmt.Sprintf("attr_%d_%s", i, strings.Repeat("x", i*3))
		want = append(want, k)
		b.WriteString(`{"id":"` + strconv.Itoa(i) + `","` + k + `":"v"}` + "\n")
	}
	complete := int64(b.Len())
	b.WriteString(`{"id":"torn","attr_torn":`)
	if err := os.WriteFile(path, []byte(b.String()), 0o640); err != nil {
		t.Fatal(err)
	}
	s := NewKeyScanner()
	keys, err := s.Keys(path)
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, "id")
	slices.Sort(want)
	if got := sortedKeys(keys); !eq(got, want) {
		t.Fatalf("keys = %v\nwant %v", got, want)
	}
	if got := s.logs[path].next; got != complete {
		t.Fatalf("next = %d, want %d: just after the last complete line", got, complete)
	}
}

// TestKeyScannerRescansAfterACutBack: a writer that cut a failed append
// back truncates the log to the line before it, and the next append can
// grow it past the offset already read, into the middle of a new line. The
// scanner must not read on from there.
func TestKeyScannerRescansAfterACutBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "2026-10-12.log")
	first := `{"id":"1","attr_a":"x"}` + "\n"
	second := `{"id":"2","attr_b":"y"}` + "\n"
	appendTo(t, path, first+second)
	s := NewKeyScanner()
	if _, err := s.Keys(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, int64(len(first))); err != nil { // the second line, cut back
		t.Fatal(err)
	}
	appendTo(t, path, `{"id":"3","attr_c":"z","attr_d":"w"}`+"\n"+`{"id":"4","attr_e":"v"}`+"\n")
	keys, err := s.Keys(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedKeys(keys); !eq(got, []string{"attr_a", "attr_c", "attr_d", "attr_e", "id"}) {
		t.Fatalf("keys = %v; want the new lines' keys and none of the cut line's", got)
	}
	if s.logs[path].next != int64(len(first))+int64(len(`{"id":"3","attr_c":"z","attr_d":"w"}`+"\n"+`{"id":"4","attr_e":"v"}`+"\n")) {
		t.Fatalf("next = %d, want the end of the file", s.logs[path].next)
	}
}
