package native

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
)

// logCache keeps each open log's rows decoded between queries, so a
// dashboard's widgets decode a log once rather than once each, and a log
// that grew decodes only the lines past what it holds: logs are only
// appended to while open.
//
//   - An entry is a log's raw rows, before rules (each query applies its
//     own after the cache): the first line of each id (FirstLines, its
//     seen ids kept so the rule spans appends), of every family and day,
//     with every column the log has, so no query decodes it again for a
//     column. A key first seen in appended lines is a column from then
//     on, null for the rows before.
//   - It is keyed by path and checked against the file's identity (device
//     and inode) and size: a log rotated (renamed <day>.<n>.closed) and
//     started again under its name is a new file, so a new entry; one
//     smaller than what the entry read is decoded again.
//   - Only complete lines are kept: the bytes past a log's last newline
//     (the torn line ingest is still writing) are decoded per query, as
//     readLog would, and kept once their newline arrives.
//   - Entries take at most max bytes together (counted as batchBytes
//     counts a batch, with the seen ids and the dictionaries' lookup
//     maps), the least recently used dropped first, as the identities
//     cache drops them. A log whose entry alone is larger is read per
//     query by readLog, as before the cache, while it stays that file.
//   - Concurrent queries missing a log, or finding it grown, decode it
//     once: the others wait, then read the entry.
type logCache struct {
	mu      sync.Mutex // never held over a read
	max     int64
	bytes   int64
	tick    uint64
	entries map[string]*logEntry
	flights map[string]chan struct{} // a decode under way, closed when done
	big     map[string]fileID        // logs too large to keep, by path: readLog reads them while the file stays
}

func newLogCache(max int64) *logCache {
	return &logCache{max: max, entries: map[string]*logEntry{}, flights: map[string]chan struct{}{}, big: map[string]fileID{}}
}

// fileID tells a file from another that took its name.
type fileID struct{ dev, ino uint64 }

// onLogDecode is called as the cache decodes a log's lines from offset
// on (0: the whole log); a variable so a test can count the decodes.
var onLogDecode = func(path string, offset int64) {}

// logEntry is one log's decoded rows. Every field is changed only by the
// query holding the log's flight; a query reads a snapshot (logView), so
// appending (which writes only past the snapshot's rows) never races it.
type logEntry struct {
	id    fileID
	size  int64 // bytes decoded: through the last complete line
	lines int   // lines decoded, for errors' line numbers

	keys     map[string]bool     // keys of the lines
	names    []archive.LogColumn // the columns keys name (archive.LogColumns), nil once keys grow
	builders map[string]*logCol  // by key
	first    archive.FirstLines
	seen     int64 // bytes the seen ids take
	n        int   // rows kept
	errs     []rowErr

	bytes   int64 // what the entry takes
	charged int64 // bytes counted in the cache's total
	used    uint64
}

// rowErr is a value of a kept row (row) that failed to read, as readLog
// fails on it only when the query reads that row: an id fails every
// query, a day the queries of its row's family, any other column those
// reading the row that ask for its key.
type rowErr struct {
	kind errKind
	row  int
	line int
	key  string
	err  error
}

type errKind int

const (
	errID errKind = iota
	errDay
	errCol
)

func (x rowErr) error(path string) error {
	return fmt.Errorf("%s line %d: %s: %w", path, x.line, x.key, x.err)
}

// read is readLog(path, cols, family, r), its rows taken from the log's
// entry.
func (c *logCache) read(path string, cols []string, family string, r Range) (*Batch, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	id, ok := fileIdentity(st)
	if !ok || c.isBig(path, id) {
		return readLog(path, cols, family, r)
	}
	done := c.flight(path)
	v, err := c.update(f, path, id, st.Size(), cols)
	done()
	if err != nil {
		return nil, err
	}
	return v.batch(path, cols, family, r)
}

// isBig says whether path, while it is the file id, is too large to keep.
func (c *logCache) isBig(path string, id fileID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	big, ok := c.big[path]
	return ok && big == id
}

// flight waits while another query decodes path, then registers this
// one's; done ends it.
func (c *logCache) flight(path string) (done func()) {
	for {
		c.mu.Lock()
		ch, busy := c.flights[path]
		if !busy {
			ch = make(chan struct{})
			c.flights[path] = ch
			c.mu.Unlock()
			return func() {
				c.mu.Lock()
				delete(c.flights, path)
				c.mu.Unlock()
				close(ch)
			}
		}
		c.mu.Unlock()
		<-ch
	}
}

// update brings path's entry up to the file (f, id, its size now) with
// the columns cols, and returns a snapshot of it for cols. The caller
// holds path's flight.
func (c *logCache) update(f *os.File, path string, id fileID, size int64, cols []string) (*logView, error) {
	c.mu.Lock()
	x := c.entries[path]
	c.mu.Unlock()
	if x == nil || x.id != id || x.size > size {
		x = &logEntry{id: id, keys: map[string]bool{}, builders: map[string]*logCol{}}
	}
	tail, err := x.decode(f, path, size)
	if err != nil {
		return nil, err
	}
	x.bytes = x.measure()
	v := x.view(cols, tail)
	c.store(path, x)
	return v, nil
}

// store caches x as path's entry, replacing what it had, and drops the
// least recently used entries while the cache holds more than max. An
// entry larger than max alone is not cached, and its file is marked big.
func (c *logCache) store(path string, x *logEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.entries[path]; old != nil {
		c.bytes -= old.charged
		delete(c.entries, path)
	}
	if x.bytes > c.max {
		c.big[path] = x.id
		return
	}
	delete(c.big, path)
	c.tick++
	x.used, x.charged = c.tick, x.bytes
	c.entries[path] = x
	c.bytes += x.bytes
	for c.bytes > c.max {
		var oldest string
		var at uint64
		first := true
		for p, y := range c.entries {
			if first || y.used < at {
				oldest, at, first = p, y.used, false
			}
		}
		c.bytes -= c.entries[oldest].charged
		delete(c.entries, oldest)
	}
}

// size is the bytes the cached entries take.
func (c *logCache) size() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// decode reads f's complete lines past x.size, up to size, into x, and
// returns the bytes past the last of them (a line not yet complete).
func (x *logEntry) decode(f *os.File, path string, size int64) ([]byte, error) {
	if x.size == size {
		return nil, nil
	}
	onLogDecode(path, x.size)
	sr := io.NewSectionReader(f, x.size, size-x.size)
	br := bufio.NewReaderSize(sr, int(min(1<<20, max(4096, size-x.size))))
	var buf []byte
	for {
		line, rerr := archive.ReadLine(br, &buf)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return nil, fmt.Errorf("%s: %w", path, rerr)
		}
		if len(line) == 0 {
			return nil, nil
		}
		if line[len(line)-1] != '\n' { // only at the end
			return slices.Clone(line), nil
		}
		x.size += int64(len(line))
		x.lines++
		x.add(line)
		if rerr != nil {
			return nil, nil
		}
	}
}

// add decodes one complete line into x, as readLog reads it: a line that
// is not a JSON object is skipped (any other JSON, which has no id, takes
// the place of the first line without one), a repeat of an id kept
// before is skipped.
func (x *logEntry) add(line []byte) {
	m, _ := archive.DecodeLine(line)
	if m == nil {
		return
	}
	for k := range m {
		if !x.keys[k] {
			x.keys[k] = true
			x.names = nil
		}
	}
	id, ok, err := archive.JSONText(m["id"])
	if err != nil {
		x.errs = append(x.errs, rowErr{kind: errID, row: -1, line: x.lines, key: "id", err: err})
		return
	}
	if !x.first.Keep(id, ok) {
		return
	}
	if ok {
		x.seen += int64(len(id)) + 48
	}
	row := x.n
	x.n++
	for k, raw := range m {
		b := x.builders[k]
		if b == nil {
			b = &logCol{key: k, kind: logKind(k)} // pads the rows before it with null
			x.builders[k] = b
		}
		if err := b.add(row, raw); err != nil {
			kind := errCol
			if k == "day" {
				kind = errDay
			}
			x.errs = append(x.errs, rowErr{kind: kind, row: row, line: x.lines, key: k, err: err})
			b.pad(row + 1) // read as null; a query reading the row fails
		}
	}
}

// measure is about what x takes: its columns as batchBytes counts them,
// each dictionary value's lookup entry, the seen ids and the keys.
func (x *logEntry) measure() int64 {
	n := x.seen + int64(len(x.errs))*64
	for k, b := range x.builders {
		n += int64(len(k)) + 32
		c := &b.c
		n += int64(cap(c.Idx))*4 + int64(cap(c.Ints)+cap(c.Floats)+cap(c.Times))*8 + int64(cap(c.Null))
		for _, s := range b.db.dict {
			n += int64(len(s)) + 16 + 40
		}
	}
	return n
}

// logView is a snapshot of an entry for one query: the rows it held then,
// and the columns the query reads, each cut to those rows.
type logView struct {
	n      int
	names  []archive.LogColumn
	cols   map[string]*Column // by key: the family, the day and those cols may need that the entry has
	errs   []rowErr
	tailOK bool // the tail is a JSON object whose id no kept line has
	tailM  map[string]json.RawMessage
	line   int // the tail's line number
	idErr  error
}

// view is x's snapshot for cols, with tail the bytes past its last line.
func (x *logEntry) view(cols []string, tail []byte) *logView {
	if x.names == nil {
		x.names = archive.LogColumns(x.keys)
	}
	v := &logView{n: x.n, names: x.names, cols: map[string]*Column{}, errs: x.errs[:len(x.errs):len(x.errs)], line: x.lines + 1}
	for k, b := range x.builders {
		if k == "family" || k == "day" || wanted(k, cols) {
			v.cols[k] = b.finish(x.n)
		}
	}
	if len(tail) > 0 {
		if m, _ := archive.DecodeLine(tail); m != nil {
			id, ok, err := archive.JSONText(m["id"])
			switch {
			case err != nil:
				v.idErr = err
			case !x.first.Seen(id, ok):
				v.tailOK, v.tailM = true, m
			}
		}
	}
	return v
}

// batch is readLog's batch of the view's rows: cols of the rows of family
// (any, when "") with day in r.
func (v *logView) batch(path string, cols []string, family string, r Range) (*Batch, error) {
	from, to := r.days()
	fam, day := v.cols["family"], v.cols["day"]
	var famOK []bool // by family index
	if family != "" && fam != nil {
		famOK = make([]bool, len(fam.Dict))
		for i, s := range fam.Dict {
			famOK[i] = s == family
		}
	}
	ofFamily := func(i int) bool {
		if family == "" {
			return true
		}
		if fam == nil {
			return false
		}
		f := fam.Idx[i]
		return f >= 0 && famOK[f]
	}
	inRange := func(i int) bool {
		if day == nil || (day.Null != nil && day.Null[i]) {
			return false
		}
		d := day.Ints[i]
		return d >= from && d <= to
	}
	// The errors readLog would meet, the first in line order.
	for _, x := range v.errs {
		switch {
		case x.kind == errID,
			x.kind == errDay && ofFamily(x.row),
			x.kind == errCol && ofFamily(x.row) && inRange(x.row) && wanted(x.key, cols):
			return nil, x.error(path)
		}
	}
	sel := make([]int32, 0, v.n)
	for i := range v.n {
		if ofFamily(i) && inRange(i) {
			sel = append(sel, int32(i))
		}
	}
	names := make([]string, len(v.names))
	keyOf := make(map[string]string, len(v.names))
	for i, c := range v.names {
		names[i] = c.Name
		keyOf[c.Name] = c.Key
	}
	requested := expandCols(names, cols)
	n := len(sel)
	out := &Batch{N: n, Cols: make(map[string]*Column, len(requested))}
	for _, name := range requested {
		k, ok := keyOf[name]
		c := v.cols[k]
		switch {
		case !ok || c == nil:
			out.Cols[name] = nullColumn(name, n)
		case n == v.n:
			out.Cols[name] = c
		default:
			out.Cols[name] = c.take(sel)
		}
	}
	if v.idErr != nil {
		return nil, fmt.Errorf("%s line %d: id: %w", path, v.line, v.idErr)
	}
	if v.tailOK {
		if err := v.addTail(out, path, cols, requested, keyOf, family, from, to); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// addTail adds the view's tail, a JSON object whose id no kept line has,
// to out as readLog reads such a last line: a row when of family with day
// in [from, to], its keys named as the complete lines name them (a key
// only it has is not read).
func (v *logView) addTail(out *Batch, path string, cols, requested []string, keyOf map[string]string, family string, from, to int64) error {
	m := v.tailM
	if family != "" {
		if fam, ok, _ := archive.JSONText(m["family"]); !ok || fam != family {
			return nil
		}
	}
	ds, ok, err := archive.JSONText(m["day"])
	if err != nil {
		return fmt.Errorf("%s line %d: day: %w", path, v.line, err)
	}
	if !ok {
		return nil
	}
	d, err := archive.ParseDay(ds)
	if err != nil {
		return fmt.Errorf("%s line %d: day: %w", path, v.line, err)
	}
	if d < from || d > to {
		return nil
	}
	// Every key cols may need is cast, named or not, as readLog casts it.
	for k, raw := range m {
		if wanted(k, cols) {
			if err := (&logCol{key: k, kind: logKind(k)}).add(0, raw); err != nil {
				return fmt.Errorf("%s line %d: %s: %w", path, v.line, k, err)
			}
		}
	}
	n := out.N
	for _, name := range requested {
		k, named := keyOf[name]
		raw, has := m[k]
		has = named && has
		var c *Column
		if named {
			c = v.cols[k]
		}
		switch {
		case c == nil && !has:
			out.Cols[name] = nullColumn(name, n+1)
		case c == nil: // only the tail has the key: readLog's builder starts at it
			b := &logCol{key: k, kind: logKind(k)}
			if err := b.add(n, raw); err != nil {
				return fmt.Errorf("%s line %d: %s: %w", path, v.line, k, err)
			}
			out.Cols[name] = b.finish(n + 1)
		default:
			src := out.Cols[name]
			if n == v.n {
				src = src.take(allRows(n)) // the entry's own: copied before it grows
			}
			// The dictionary is the entry's: clipped, it is copied as it grows.
			b := &logCol{key: k, kind: logKind(k), c: *src, db: dictBuilder{dict: slices.Clip(src.Dict)}, n: n}
			if has {
				if err := b.add(n, raw); err != nil {
					return fmt.Errorf("%s line %d: %s: %w", path, v.line, k, err)
				}
			}
			out.Cols[name] = b.finish(n + 1)
		}
	}
	out.N = n + 1
	return nil
}

// allRows is the selection of rows 0 to n-1.
func allRows(n int) []int32 {
	sel := make([]int32, n)
	for i := range sel {
		sel[i] = int32(i)
	}
	return sel
}
