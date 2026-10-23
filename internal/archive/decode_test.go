package archive

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestCastsMatchDuckDB: each JSON value of a log line reads as the dump's
// logSelect reads it (json_extract_string, then CAST to the column's
// type): the same value at each type, or an error where the CAST fails
// the query.
func TestCastsMatchDuckDB(t *testing.T) {
	f := newFixture(t)
	values := []string{`"abc"`, `""`, `5`, `-7`, `"12"`, `"1280.6"`, `"1280.5"`, `"-2.5"`, `1.50`, `1e3`, `true`,
		`{"k":1}`, `[1,2]`, `"é\u2028"`, `"x"`, `"1e300"`, `1e300`, `"0.25"`,
		`"2026-09-10T12:00:00Z"`, `"2026-09-10T12:00:01.123456Z"`, `"2026-09-10"`, `"1969-12-31"`, `"10.09.2026"`, `"nope"`}
	cast := map[string]func(s string) (any, error){
		"attr_v":        func(s string) (any, error) { return s, nil },
		"display_width": func(s string) (any, error) { return ParseInt(s) },
		"received_at":   func(s string) (any, error) { return ParseTime(s) },
		"day":           func(s string) (any, error) { return ParseDay(s) },
		"value":         func(s string) (any, error) { return strconv.ParseFloat(s, 64) },
	}
	expr := map[string]string{"attr_v": "attr_v", "display_width": "display_width", "received_at": "epoch_us(received_at)",
		"day": "day - DATE '1970-01-01'", "value": "value"}
	// Where the native reader and DuckDB part, on values line never
	// writes (a string for every VARCHAR, YYYY-MM-DD for day): DuckDB
	// gives a JSON number's text in its own form (1.5, 1000.0), and reads a
	// DATE from a timestamp's text.
	known := map[string]bool{
		`{"id":"a","attr_v":1.50}`:                       true,
		`{"id":"a","attr_v":1e3}`:                        true,
		`{"id":"a","day":"2026-09-10T12:00:00Z"}`:        true,
		`{"id":"a","day":"2026-09-10T12:00:01.123456Z"}`: true,
	}
	for _, v := range values {
		for col, to := range cast {
			line := `{"id":"a","` + col + `":` + v + `}`
			if known[line] {
				continue
			}
			f.writeFile(t, "2026-09-10.log", line+"\n")
			var duck any
			res, err := f.eng.Query(context.Background(), "SELECT "+expr[col]+" FROM "+f.logRel(t, "2026-09-10"), 0)
			if err == nil {
				duck = res.Rows[0][0]
			}
			m, _ := DecodeLine([]byte(line))
			s, ok, gerr := JSONText(m[col])
			if gerr != nil || !ok {
				t.Fatalf("%s: %v %v", line, ok, gerr)
			}
			goVal, gerr := to(s)
			if (err != nil) != (gerr != nil) || (err == nil && fmt.Sprint(duck) != fmt.Sprint(goVal)) {
				t.Errorf("%s: DuckDB %v (%v), Go %v (%v)", line, duck, err, goVal, gerr)
			}
		}
	}
	for _, raw := range []string{``, `null`, ` null `} {
		if _, ok, err := JSONText(json.RawMessage(raw)); ok || err != nil {
			t.Errorf("%q: ok %v, err %v; want NULL", raw, ok, err)
		}
	}
}

// TestJSONTextReadsStringsAsUnmarshalDoes: the shortcut for a string
// without an escape gives what json.Unmarshal gives, invalid UTF-8
// included.
func TestJSONTextReadsStringsAsUnmarshalDoes(t *testing.T) {
	for _, raw := range []string{`""`, `"abc"`, `"é"`, "\"a\xffb\"", `"a\"b"`, `"\u00e9\n"`, ` "x" `} {
		var want string
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := JSONText(json.RawMessage(raw)); got != want || !ok || err != nil {
			t.Errorf("%q: %q %v %v, want %q", raw, got, ok, err, want)
		}
	}
}

// TestDecodeLine: an object decodes to its keys; null and what is not
// JSON are skipped (nil); other JSON reads as a line without keys.
func TestDecodeLine(t *testing.T) {
	for line, want := range map[string]string{
		`{"id":"a","n":1}` + "\n": "object 2",
		`{}`:                      "object 0",
		`[1,2]` + "\n":            "keyless",
		`5`:                       "keyless",
		`"s"`:                     "keyless",
		`null` + "\n":             "skipped",
		"\n":                      "skipped",
		`not json {`:              "skipped",
		`{"id":"torn","a":"x`:     "skipped",
	} {
		m, object := DecodeLine([]byte(line))
		got := "skipped"
		switch {
		case object:
			got = fmt.Sprint("object ", len(m))
		case m != nil:
			got = "keyless"
			if len(m) != 0 {
				got = "keyless with keys"
			}
		}
		if got != want {
			t.Errorf("%q: %s, want %s", line, got, want)
		}
	}
}

// TestFirstLinesKeepsTheFirstOfEachID: a repeated id is dropped, and the
// lines without one count as one id.
func TestFirstLinesKeepsTheFirstOfEachID(t *testing.T) {
	var f FirstLines
	var got []bool
	for _, id := range []struct {
		s  string
		ok bool
	}{{"a", true}, {"b", true}, {"a", true}, {"", false}, {"", false}, {"", true}, {"", true}, {"b", true}} {
		got = append(got, f.Keep(id.s, id.ok))
	}
	if want := []bool{true, true, false, true, false, true, false, false}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("kept %v, want %v", got, want)
	}
}

// TestReadLineReadsLongAndLastLines: a line longer than the buffer comes
// whole, and a last line without its newline comes with io.EOF.
func TestReadLineReadsLongAndLastLines(t *testing.T) {
	long := strings.Repeat("x", 100) + "\n"
	br := bufio.NewReaderSize(strings.NewReader("ab\n"+long+"tail"), 16)
	var buf []byte
	var got []string
	for {
		line, err := ReadLine(br, &buf)
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		if len(line) > 0 {
			got = append(got, string(line))
		}
		if err != nil {
			break
		}
	}
	if want := []string{"ab\n", long, "tail"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("lines %q, want %q", got, want)
	}
}

// logLine is one kept line of a log, its values as JSONText reads them.
type logLine map[string]*string

// readLogLines reads path's lines as the native reader reads them: the
// first line of each id, objects only, each value as text.
func readLogLines(t *testing.T, path string) []logLine {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	br := bufio.NewReader(fh)
	var (
		buf   []byte
		first FirstLines
		out   []logLine
	)
	for {
		line, rerr := ReadLine(br, &buf)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			t.Fatal(rerr)
		}
		if len(line) == 0 {
			break
		}
		if m, _ := DecodeLine(line); m != nil {
			l := logLine{}
			for k, raw := range m {
				s, ok, err := JSONText(raw)
				if err != nil {
					t.Fatal(err)
				}
				if ok {
					l[k] = &s
				}
			}
			id := l["id"]
			if first.Keep(l.str("id"), id != nil) {
				out = append(out, l)
			}
		}
		if rerr != nil {
			break
		}
	}
	return out
}

// str is key's value, "" for NULL.
func (l logLine) str(key string) string {
	if p := l[key]; p != nil {
		return *p
	}
	return ""
}

// ts is the line's ts, ok false for NULL.
func (l logLine) ts(t *testing.T) (int64, bool) {
	t.Helper()
	if l["ts"] == nil {
		return 0, false
	}
	us, err := ParseTime(*l["ts"])
	if err != nil {
		t.Fatal(err)
	}
	return us, true
}
