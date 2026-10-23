package archive

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// A log's lines (line), read back one value at a time as DuckDB reads
// them in logSelect: each value as json_extract_string gives it, then
// CAST to its column's type. The native engine reads the logs with these,
// and so does the dump.

// ReadLine reads one line, its newline included, reusing *buf for a line
// longer than br's buffer. The line is valid until the next call. At the
// end of the file it returns what is left (a last line without its
// newline, or nothing) with io.EOF.
func ReadLine(br *bufio.Reader, buf *[]byte) ([]byte, error) {
	line, err := br.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		return line, err
	}
	*buf = append((*buf)[:0], line...)
	for errors.Is(err, bufio.ErrBufferFull) {
		line, err = br.ReadSlice('\n')
		*buf = append(*buf, line...)
	}
	return *buf, err
}

// DecodeLine decodes one line into its keys' raw values; object says
// whether it is a JSON object. When it is not, a tolerant reader (the
// reader's events view) goes on as DuckDB does: m is nil for a line it
// skips (JSON null, or no JSON at all: the torn last line of a log ingest
// is still writing, garbage), and empty for any other JSON (an array, a
// number), which DuckDB reads as a line without keys, its id null.
func DecodeLine(line []byte) (m map[string]json.RawMessage, object bool) {
	if json.Unmarshal(line, &m) == nil && m != nil {
		return m, true
	}
	if bytes.Equal(bytes.TrimSpace(line), []byte("null")) || !json.Valid(line) {
		return nil, false
	}
	return map[string]json.RawMessage{}, false
}

// FirstLines keeps the first line of each id, in the order they are
// read: a repeat need not be the same event. Every line without an id
// counts as one id, so only the first of them is kept. The zero value is
// ready to use.
type FirstLines struct {
	seen     map[string]struct{}
	nullSeen bool // a line without an id was kept
}

// Keep says whether the line with id (ok false for none) is the first of
// its id, and notes it.
func (f *FirstLines) Keep(id string, ok bool) bool {
	if !ok {
		if f.nullSeen {
			return false
		}
		f.nullSeen = true
		return true
	}
	if f.seen == nil {
		f.seen = map[string]struct{}{}
	} else if _, dup := f.seen[id]; dup {
		return false
	}
	f.seen[id] = struct{}{}
	return true
}

// Seen says whether Keep would refuse the line with id (ok false for
// none), without noting it.
func (f *FirstLines) Seen(id string, ok bool) bool {
	if !ok {
		return f.nullSeen
	}
	_, dup := f.seen[id]
	return dup
}

// JSONText is a JSON value as DuckDB's json_extract_string gives it: a
// string's content, any other value's JSON text; ok false for a null or
// missing value.
func JSONText(raw json.RawMessage) (string, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", false, nil
	}
	if raw[0] != '"' {
		return string(raw), true, nil
	}
	// A string without an escape, in valid UTF-8, is its bytes between the
	// quotes: raw is one valid JSON value, so it holds no other quote and no
	// control character. Unmarshal reads the rest (it also replaces invalid
	// UTF-8 with U+FFFD).
	if n := len(raw); n >= 2 && raw[n-1] == '"' && bytes.IndexByte(raw[1:n-1], '\\') < 0 && utf8.Valid(raw[1:n-1]) {
		return string(raw[1 : n-1]), true, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false, err
	}
	return s, true, nil
}

// ParseDay reads a DATE, YYYY-MM-DD, as days since 1970-01-01.
func ParseDay(s string) (int64, error) {
	d, err := civil.Parse(s)
	if err != nil {
		return 0, err
	}
	return d.Time().Unix() / 86400, nil
}

// timeLayouts are the forms a log's timestamps take: line writes the
// first; the others are what DuckDB's CAST AS TIMESTAMP also reads.
var timeLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999", "2006-01-02"}

// ParseTime reads a TIMESTAMP as unix microseconds.
func ParseTime(s string) (int64, error) {
	var first error
	for _, layout := range timeLayouts {
		t, err := time.Parse(layout, s)
		if err == nil {
			return t.UnixMicro(), nil
		}
		if first == nil {
			first = err
		}
	}
	return 0, first
}

// ParseInt reads a BIGINT as DuckDB casts text to one: a whole number, or
// a decimal one rounded.
func ParseInt(s string) (int64, error) {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) >= 1<<63 {
		return 0, fmt.Errorf("%q is no BIGINT", s)
	}
	return int64(math.Round(f)), nil
}
