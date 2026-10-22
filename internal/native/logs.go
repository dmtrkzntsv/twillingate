package native

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// readLog reads an open log (archive's NDJSON lines, one event each) into
// one batch of the columns cols holding only the rows of family (any, when
// "") with day in r: the rows the reader's events view holds for the log
// (archive's logSelect, tolerant), as the Parquet path would yield them.
//
//   - Every line is decoded, but only the keys cols may need are parsed.
//   - A line that is not a JSON object (the torn last line of a log ingest
//     is still writing, garbage) is skipped.
//   - Of lines with one id, the first is kept, before family and day are
//     looked at: a repeat need not be the same event.
//   - The columns are named as archive.LogColumns names them for the keys
//     of the log's complete lines: a key equal to an earlier one but for
//     case reads as <key>_<n>, so a name in cols matches that column. A
//     pattern in cols (AttrCols) expands to the log's columns with its
//     prefix; a plain name the log lacks reads all null.
//   - A key a line leaves out reads null, at the column's type; a value
//     that does not cast to the column's type fails the read, as DuckDB's
//     CAST fails the query.
func readLog(path string, cols []string, family string, r Range) (*Batch, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	from, to := r.days()
	var (
		keys     = map[string]bool{}     // keys of the complete lines
		builders = map[string]*logCol{}  // by key; nil for a key cols cannot need
		seen     = map[string]struct{}{} // ids read
		nullSeen bool                    // a line without an id read
		n        int                     // rows kept
		buf      []byte
	)
	br := bufio.NewReaderSize(f, 1<<20)
	for lineNo := 1; ; lineNo++ {
		line, rerr := readLine(br, &buf)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return nil, fmt.Errorf("%s: %w", path, rerr)
		}
		if len(line) == 0 && rerr != nil {
			break
		}
		complete := line[len(line)-1] == '\n'
		var m map[string]json.RawMessage
		if json.Unmarshal(line, &m) != nil || m == nil {
			// DuckDB reads JSON that is no object (an array, a number) as a
			// line without keys: its id is null, so it takes the place of
			// the first line without one.
			if bytes.Equal(bytes.TrimSpace(line), []byte("null")) || !json.Valid(line) {
				if rerr != nil {
					break
				}
				continue
			}
			m = map[string]json.RawMessage{}
		}
		if complete {
			for k := range m {
				keys[k] = true
			}
		}
		id, ok, err := text(m["id"])
		if err != nil {
			return nil, fmt.Errorf("%s line %d: id: %w", path, lineNo, err)
		}
		if !ok {
			if nullSeen {
				continue
			}
			nullSeen = true
		} else if _, dup := seen[id]; dup {
			continue
		} else {
			seen[id] = struct{}{}
		}
		if family != "" {
			if fam, ok, _ := text(m["family"]); !ok || fam != family {
				continue
			}
		}
		ds, ok, err := text(m["day"])
		if err != nil {
			return nil, fmt.Errorf("%s line %d: day: %w", path, lineNo, err)
		}
		if !ok {
			continue
		}
		day, err := parseDay(ds)
		if err != nil {
			return nil, fmt.Errorf("%s line %d: day: %w", path, lineNo, err)
		}
		if day < from || day > to {
			continue
		}
		for k, raw := range m {
			b, known := builders[k]
			if !known {
				if wanted(k, cols) {
					b = &logCol{key: k, kind: logKind(k)}
				}
				builders[k] = b
			}
			if b == nil {
				continue
			}
			if err := b.add(n, raw); err != nil {
				return nil, fmt.Errorf("%s line %d: %s: %w", path, lineNo, k, err)
			}
		}
		n++
		if rerr != nil {
			break
		}
	}
	lc := archive.LogColumns(keys)
	names := make([]string, len(lc))
	keyOf := make(map[string]string, len(lc))
	for i, c := range lc {
		names[i] = c.Name
		keyOf[c.Name] = c.Key
	}
	requested := expandCols(names, cols)
	out := &Batch{N: n, Cols: make(map[string]*Column, len(requested))}
	for _, name := range requested {
		if k, ok := keyOf[name]; ok && builders[k] != nil {
			out.Cols[name] = builders[k].finish(n)
			continue
		}
		out.Cols[name] = nullColumn(name, n)
	}
	return out, nil
}

// readLine reads one line, its newline included, reusing *buf for a line
// longer than br's buffer. The line is valid until the next call. At the
// end of the file it returns what is left (a last line without its
// newline, or nothing) with io.EOF.
func readLine(br *bufio.Reader, buf *[]byte) ([]byte, error) {
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

// wanted says whether key's column may be one cols asks for: its name is
// the key (NUL spelled \u0000, as archive names it) or that and _<n>.
func wanted(key string, cols []string) bool {
	name := strings.ReplaceAll(key, "\x00", `\u0000`)
	for _, c := range cols {
		if prefix, ok := strings.CutSuffix(c, "*"); ok {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		} else if c == name || strings.HasPrefix(c, name+"_") {
			return true
		}
	}
	return false
}

// logKind is the type of key's column: a fixed field's (eventKinds; the
// key is the column's name), VARCHAR for every other key.
func logKind(key string) colKind {
	if key == "day" {
		return kindDate
	}
	return eventKinds[key]
}

// kindDate is DATE, read from YYYY-MM-DD, into Ints as days since
// 1970-01-01; only a log's reader tells it from BIGINT.
const kindDate colKind = -1

// text is a JSON value as DuckDB's json_extract_string gives it: a
// string's content, any other value's JSON text; ok false for a null or
// missing value.
func text(raw json.RawMessage) (string, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", false, nil
	}
	if raw[0] != '"' {
		return string(raw), true, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false, err
	}
	return s, true, nil
}

func parseDay(s string) (int64, error) {
	d, err := civil.Parse(s)
	if err != nil {
		return 0, err
	}
	return epochDay(d), nil
}

// timeLayouts are the forms a log's timestamps take: line writes the
// first; the others are what DuckDB's CAST AS TIMESTAMP also reads.
var timeLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999", "2006-01-02"}

func parseTime(s string) (int64, error) {
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

// parseInt reads a BIGINT as DuckDB casts text to one: a whole number, or
// a decimal one rounded.
func parseInt(s string) (int64, error) {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) >= 1<<63 {
		return 0, fmt.Errorf("%q is no BIGINT", s)
	}
	return int64(math.Round(f)), nil
}

// logCol builds one column of a log's batch: rows come in order, a row
// without the key leaves a gap that the next add or finish fills with null.
type logCol struct {
	key  string
	kind colKind
	c    Column
	db   dictBuilder
	n    int // rows filled
}

func (b *logCol) pad(to int) {
	for b.n < to {
		switch b.kind {
		case kindString:
			b.c.Idx = append(b.c.Idx, -1)
		case kindFloat:
			b.c.Floats = append(b.c.Floats, 0)
			b.null(true)
		case kindTime:
			b.c.Times = append(b.c.Times, 0)
			b.null(true)
		default:
			b.c.Ints = append(b.c.Ints, 0)
			b.null(true)
		}
		b.n++
	}
}

// null records whether the row just appended is null: Null is made the
// first time one is, falses for the rows before it.
func (b *logCol) null(isNull bool) {
	if isNull && b.c.Null == nil {
		b.c.Null = make([]bool, b.n)
	}
	if b.c.Null != nil {
		b.c.Null = append(b.c.Null, isNull)
	}
}

// add sets row's value from raw, after filling the rows before it.
func (b *logCol) add(row int, raw json.RawMessage) error {
	b.pad(row)
	s, ok, err := text(raw)
	if err != nil {
		return err
	}
	if !ok {
		b.pad(row + 1)
		return nil
	}
	switch b.kind {
	case kindString:
		b.c.Idx = append(b.c.Idx, b.db.id([]byte(s)))
	case kindFloat:
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		b.c.Floats = append(b.c.Floats, v)
		b.null(false)
	case kindTime:
		v, err := parseTime(s)
		if err != nil {
			return err
		}
		b.c.Times = append(b.c.Times, v)
		b.null(false)
	case kindDate:
		v, err := parseDay(s)
		if err != nil {
			return err
		}
		b.c.Ints = append(b.c.Ints, v)
		b.null(false)
	default:
		v, err := parseInt(s)
		if err != nil {
			return err
		}
		b.c.Ints = append(b.c.Ints, v)
		b.null(false)
	}
	b.n++
	return nil
}

// finish fills the rows up to n and returns the column. A builder is
// made by a kept row's add, so the column has at least one row.
func (b *logCol) finish(n int) *Column {
	b.pad(n)
	c := b.c
	c.Dict = b.db.dict
	return &c
}
