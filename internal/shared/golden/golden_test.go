package golden

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeT records what a Set reports instead of failing the test.
type fakeT struct {
	testing.TB
	name     string
	failed   bool
	msgs     []string
	cleanups []func()
}

func (f *fakeT) Helper()      {}
func (f *fakeT) Name() string { return f.name }
func (f *fakeT) Failed() bool { return f.failed }
func (f *fakeT) Cleanup(fn func()) {
	f.cleanups = append(f.cleanups, fn)
}
func (f *fakeT) Errorf(format string, args ...any) {
	f.failed = true
	f.msgs = append(f.msgs, format)
}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.Errorf(format, args...)
	panic(f)
}
func (f *fakeT) end() {
	for _, fn := range f.cleanups {
		fn()
	}
}

// try runs fn, recovering a fakeT's Fatalf.
func try(f *fakeT, fn func()) {
	defer func() {
		if r := recover(); r != nil && r != any(f) {
			panic(r)
		}
	}()
	fn()
}

type answer struct {
	Rows [][]string `json:"rows"`
	N    float64    `json:"n"`
}

// TestGetKeepsAndReads: with update the oracle's answers are written,
// keys sorted and indented; without, they are read back as given; a key
// never kept fails, and so does a file holding a key nobody asks for.
func TestGetKeepsAndReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "testdata", "TestX.golden.json")
	one := answer{Rows: [][]string{{"a", ""}, {"b", "1.5"}}, N: 0.1}

	w := &fakeT{name: "TestX"}
	s := Open(w, path, true)
	if got := Get(w, s, "b one", one); !Same(got, one) {
		t.Fatalf("update returned %+v", got)
	}
	Get(w, s, "a two", []int{2})
	Get(w, s, "a two", []int{2}) // the same answer twice is fine
	try(w, func() { Get(w, s, "a two", []int{3}) })
	if !w.failed {
		t.Error("two answers under one key were kept")
	}
	Get(w, s, "c <&>", map[string]any{"empty": map[string]any{}, "none": []any{}, "objs": []any{map[string]any{"k": "<v>"}}})
	want := `{
  "a two": [2],
  "b one": {
    "n": 0.1,
    "rows": [
      ["a",""],
      ["b","1.5"]
    ]
  },
  "c <&>": {
    "empty": {},
    "none": [],
    "objs": [
      {
        "k": "<v>"
      }
    ]
  }
}
`
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Errorf("file:\n%s\nwant\n%s", b, want)
	}

	sets.Lock()
	delete(sets.m, path)
	sets.Unlock()
	r := &fakeT{name: "TestX"}
	s = Open(r, path, false)
	if Open(r, path, false) != s {
		t.Error("a second Open made a second Set")
	}
	if got := Get(r, s, "b one", answer{}); !Same(got, one) {
		t.Errorf("read %+v, want %+v", got, one)
	}
	try(r, func() { Get(r, s, "missing", 0) })
	if !r.failed {
		t.Error("a missing key read")
	}
	r.failed = false
	if got := Get(r, s, "c <&>", map[string]any(nil)); got["objs"].([]any)[0].(map[string]any)["k"] != "<v>" {
		t.Errorf("read %v", got)
	}
	r.end() // "a two" was never asked for
	if !r.failed && !raceEnabled {
		t.Error("a stale key went unreported")
	}
	if Same(1, 2) || !Same([]string{"x"}, []string{"x"}) || Same(func() {}, 1) {
		t.Error("Same")
	}
	if d := Diff([]string{"a", "b"}, []string{"a", "c"}); d != "line 3: \"b\"\n  want: \"c\"" {
		t.Errorf("Diff = %q", d)
	}
	if Diff([]int{1}, []int{1}) != "" || Diff([]int{1}, []int{1, 2}) == "" || Diff(func() {}, 1) == "" {
		t.Error("Diff")
	}
}

// TestForNamesTheFileByTheTopTest: a subtest shares its test's file.
func TestForNamesTheFileByTheTopTest(t *testing.T) {
	sub := &fakeT{name: "TestTop/case"}
	s := For(sub, false)
	if s.path != filepath.Join("testdata", "TestTop.golden.json") || len(sub.cleanups) != 0 {
		t.Errorf("path %s, %d cleanups", s.path, len(sub.cleanups))
	}
}

// TestOpenRefusesABadFile: a file that is not a JSON object fails.
func TestOpenRefusesABadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.golden.json")
	if err := os.WriteFile(path, []byte("[1]"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeT{name: "TestBad"}
	try(f, func() { Open(f, path, false) })
	if !f.failed {
		t.Error("a bad file read")
	}
}
