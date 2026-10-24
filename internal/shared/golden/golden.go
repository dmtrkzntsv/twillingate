// Package golden keeps a test's expected answers in a file beside it:
// testdata/<test>.golden.json, one JSON object of answers by key, the
// keys sorted and the values indented, so a change to an answer reads as
// a diff. A test asks for each answer by key, handing in the answer of
// the code under test; with update that answer is written (or the file's
// own, when the test finds it equivalent: Previous), and without it the
// file's is returned for the test to compare the two.
package golden

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Set is one test's goldens.
type Set struct {
	path   string
	update bool

	mu     sync.Mutex
	values map[string]json.RawMessage
	prev   map[string]json.RawMessage // the file as this run found it
	asked  map[string]bool            // the keys this run asked for
	kept   map[string]bool            // the keys this run kept, with update
}

// sets holds each file's Set, so the subtests of a test share one.
var sets struct {
	sync.Mutex
	m map[string]*Set
}

// For returns the Set of t's top-level test: testdata/<Test>.golden.json,
// relative to the package's directory, as go test runs it. The first call
// for a file reads it (missing is empty); a call from a top-level test
// also checks, when the test ends and passed in a run of every test, that
// it asked for every key the file holds, so no answer outlives its case.
func For(t testing.TB, update bool) *Set {
	t.Helper()
	top, _, _ := strings.Cut(t.Name(), "/")
	return Open(t, filepath.Join("testdata", top+".golden.json"), update)
}

// Open returns the Set kept in path, as For does.
func Open(t testing.TB, path string, update bool) *Set {
	t.Helper()
	sets.Lock()
	defer sets.Unlock()
	if s := sets.m[path]; s != nil {
		return s
	}
	s := &Set{path: path, update: update, values: map[string]json.RawMessage{}, prev: map[string]json.RawMessage{},
		asked: map[string]bool{}, kept: map[string]bool{}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		t.Fatalf("golden: %v", err)
	default:
		if err := json.Unmarshal(b, &s.prev); err != nil {
			t.Fatalf("golden: %s: %v", path, err)
		}
	}
	if !update || filtered() {
		// With update and every case running, the file is written anew,
		// so an answer no case asks for any more goes.
		maps.Copy(s.values, s.prev)
	}
	if sets.m == nil {
		sets.m = map[string]*Set{}
	}
	sets.m[path] = s
	if !strings.Contains(t.Name(), "/") {
		t.Cleanup(func() { s.checkAsked(t) })
	}
	return s
}

// checkAsked fails t if the file holds a key no call asked for, unless the
// run left cases out (-run, -short, the race suite's skips) or t failed
// already.
func (s *Set) checkAsked(t testing.TB) {
	t.Helper()
	if s.update || t.Failed() || filtered() || testing.Short() || raceEnabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var stale []string
	for k := range s.values {
		if !s.asked[k] {
			stale = append(stale, k)
		}
	}
	if len(stale) > 0 {
		slices.Sort(stale)
		t.Errorf("golden: %s holds answers no case asks for: %q; delete them, or the file, and rerun with -update-golden", s.path, stale)
	}
}

// filtered says whether the run picks its tests (-run): then a Set sees
// only some of its keys asked for.
func filtered() bool {
	f := flag.Lookup("test.run")
	return f != nil && f.Value.String() != ""
}

// Get returns the answer kept under key, decoded into a T. With the Set's
// update on, it first keeps got (the code under test's answer) there and
// writes the file (asking for a key twice must give the same answer), so
// it returns got as read back from JSON.
func Get[T any](t testing.TB, s *Set, key string, got T) T {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked[key] = true
	if s.update {
		b, err := Compact(got)
		if err != nil {
			t.Fatalf("golden: %s: %v", key, err)
		}
		if old := s.values[key]; s.kept[key] && !bytes.Equal(old, b) {
			t.Fatalf("golden: %s asked twice with different answers:\n%s\n%s", key, old, b)
		}
		s.values[key], s.kept[key] = b, true
		if err := s.write(); err != nil {
			t.Fatalf("golden: %v", err)
		}
	}
	raw, ok := s.values[key]
	if !ok {
		t.Fatalf("golden: %s has no answer %q; run the test with -update-golden to keep the code's", s.path, key)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("golden: %s: %q: %v", s.path, key, err)
	}
	return out
}

// Previous returns the answer the file held under key when the run began,
// decoded into a T, and whether it held one. With update, a test that
// finds it equivalent to its own answer (equal but for an order or a
// rounding the comparison allows) hands it to Get instead, so the file
// changes only where an answer does.
func Previous[T any](t testing.TB, s *Set, key string) (T, bool) {
	t.Helper()
	s.mu.Lock()
	raw, ok := s.prev[key]
	s.mu.Unlock()
	var out T
	if !ok {
		return out, false
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("golden: %s: %q: %v", s.path, key, err)
	}
	return out, true
}

// write writes every answer, keys sorted, each value indented but for an
// array of numbers, strings and nulls, which stays on its line (a row).
func (s *Set) write() error {
	var b bytes.Buffer
	b.WriteString("{")
	for i, k := range slices.Sorted(maps.Keys(s.values)) {
		if i > 0 {
			b.WriteString(",")
		}
		key, err := Compact(k)
		if err != nil {
			return err
		}
		var v any
		dec := json.NewDecoder(bytes.NewReader(s.values[k]))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		fmt.Fprintf(&b, "\n  %s: ", key)
		if err := indent(&b, v, "  "); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	b.WriteString("\n}\n")
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path, b.Bytes(), 0o644)
}

// indent writes v at the given indent: an object one key a line, sorted;
// an array of objects or arrays one element a line; any other array, and
// a scalar, compact.
func indent(b *bytes.Buffer, v any, at string) error {
	compact := func(v any) error {
		c, err := Compact(v)
		b.Write(c)
		return err
	}
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteString("{")
		for i, k := range slices.Sorted(maps.Keys(x)) {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString("\n" + at + "  ")
			if err := compact(k); err != nil {
				return err
			}
			b.WriteString(": ")
			if err := indent(b, x[k], at+"  "); err != nil {
				return err
			}
		}
		b.WriteString("\n" + at + "}")
		return nil
	case []any:
		nested := slices.ContainsFunc(x, func(e any) bool {
			switch e.(type) {
			case map[string]any, []any:
				return true
			}
			return false
		})
		if !nested || len(x) == 0 {
			return compact(x)
		}
		b.WriteString("[")
		for i, e := range x {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString("\n" + at + "  ")
			if err := indent(b, e, at+"  "); err != nil {
				return err
			}
		}
		b.WriteString("\n" + at + "]")
		return nil
	}
	return compact(v)
}

// Compact is v as one line of JSON, as the files keep a row: no HTML
// escaping, no trailing newline.
func Compact(v any) ([]byte, error) {
	var w bytes.Buffer
	e := json.NewEncoder(&w)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(w.Bytes(), []byte("\n")), nil
}

// Same says whether a and b are equal as JSON: what Get keeps of each.
func Same(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// Diff is where a and b part as indented JSON: the first line that
// differs, with its line number, or "" when they are the same.
func Diff(a, b any) string {
	x, err1 := json.MarshalIndent(a, "", " ")
	y, err2 := json.MarshalIndent(b, "", " ")
	if err := errors.Join(err1, err2); err != nil {
		return err.Error()
	}
	xs, ys := strings.Split(string(x), "\n"), strings.Split(string(y), "\n")
	for i := range max(len(xs), len(ys)) {
		var p, q string
		if i < len(xs) {
			p = xs[i]
		}
		if i < len(ys) {
			q = ys[i]
		}
		if p != q || i >= len(xs) || i >= len(ys) {
			return fmt.Sprintf("line %d: %s\n  want: %s", i+1, strings.TrimSpace(p), strings.TrimSpace(q))
		}
	}
	return ""
}
