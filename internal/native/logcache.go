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
//     own after the cache): the first line of each id (as FirstLines
//     keeps it; the id columns' values are the ids seen, so the rule
//     spans appends), of every day, with every column the log has, so no
//     query decodes it again for a column. A key first seen in appended
//     lines is a column from then on, null for the rows before.
//   - The rows are kept a part per family, so a query of a family whose
//     rows all have a day in its range (a log holds its day's events, and
//     the odd late one) gets the part's columns as they are, not a copy;
//     a query of every family gets a batch per part. The batches handed
//     out are each query's own (its Cols its own map), their columns the
//     entry's and read-only: nothing downstream writes a column it is
//     given (rules and the identity columns make new ones).
//   - It is keyed by path and checked against the file's identity (device
//     and inode) and size: a log rotated (renamed <day>.<n>.closed) and
//     started again under its name is a new file, so a new entry; one
//     smaller than what the entry read is decoded again.
//   - Only complete lines are kept: the bytes past a log's last newline
//     (the torn line ingest is still writing) are decoded per query, as
//     readLog would, and kept once their newline arrives.
//   - Entries take at most max bytes together (counted as batchBytes
//     counts a batch), the least recently used dropped first, as the identities
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
	parts    []*logPart          // by family, in the order first seen
	nullSeen bool                // a line without an id was kept
	errs     []rowErr

	bytes   int64 // what the entry takes
	charged int64 // bytes counted in the cache's total
	used    uint64
}

// famKey is a family's value: ok false for a line without one (null).
type famKey struct {
	name string
	ok   bool
}

// logPart is the kept rows of one family, in line order.
type logPart struct {
	fam      famKey
	n        int
	builders map[string]*logCol // by key
	// The days of the rows: their bounds, and how many have none (null,
	// or one that failed to read).
	dayMin, dayMax int64
	dayNulls       int
}

func (x *logEntry) part(fam famKey) *logPart {
	for _, p := range x.parts {
		if p.fam == fam {
			return p
		}
	}
	p := &logPart{fam: fam, builders: map[string]*logCol{}}
	x.parts = append(x.parts, p)
	return p
}

// rowErr is a value of a kept row (row) that failed to read, as readLog
// fails on it only when the query reads that row: an id fails every
// query, a day the queries of its row's family, any other column those
// reading the row that ask for its key.
type rowErr struct {
	kind errKind
	part int // index in the entry's parts
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
func (c *logCache) read(path string, cols []string, family string, r Range) ([]*Batch, error) {
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
		return readLogBatches(path, cols, family, r)
	}
	done := c.flight(path)
	v, err := c.update(f, path, id, st.Size(), cols)
	done()
	if err != nil {
		return nil, err
	}
	return v.batches(path, cols, family, r)
}

// readLogBatches is readLog's batch, as a logReader gives it.
func readLogBatches(path string, cols []string, family string, r Range) ([]*Batch, error) {
	b, err := readLog(path, cols, family, r)
	if err != nil {
		return nil, err
	}
	return []*Batch{b}, nil
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
		x = &logEntry{id: id, keys: map[string]bool{}}
	}
	tail, err := x.decode(f, path, size)
	if err != nil {
		return nil, err
	}
	for _, p := range x.parts { // every column to the part's rows: views then only read them
		for _, b := range p.builders {
			b.pad(p.n)
		}
	}
	v := x.view(cols, tail)
	// The dictionaries' lookups (a map entry per value) are built again
	// only when the log grows: between growths they would take about a
	// third of the entry.
	for _, p := range x.parts {
		for _, b := range p.builders {
			b.db.ids = nil
		}
	}
	x.bytes = x.measure()
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
	if x.seen(id, ok) {
		return
	}
	if !ok {
		x.nullSeen = true
	}
	var fam famKey
	fam.name, fam.ok, _ = archive.JSONText(m["family"]) // a valid value: no error
	p := x.part(fam)
	pi := slices.Index(x.parts, p)
	row := p.n
	p.n++
	for k, raw := range m {
		b := p.builders[k]
		if b == nil {
			b = &logCol{key: k, kind: logKind(k)} // pads the rows before it with null
			p.builders[k] = b
		}
		if err := b.add(row, raw); err != nil {
			kind := errCol
			if k == "day" {
				kind = errDay
			}
			x.errs = append(x.errs, rowErr{kind: kind, part: pi, row: row, line: x.lines, key: k, err: err})
			b.pad(row + 1) // read as null; a query reading the row fails
		}
	}
	if d := p.builders["day"]; d == nil || d.n <= row || (d.c.Null != nil && d.c.Null[row]) {
		p.dayNulls++
	} else if v := d.c.Ints[row]; p.n-p.dayNulls == 1 {
		p.dayMin, p.dayMax = v, v
	} else {
		p.dayMin, p.dayMax = min(p.dayMin, v), max(p.dayMax, v)
	}
}

// seen says whether a kept line has id (ok false for none): the first
// line of each id is kept, every line without one counting as one id.
// The kept ids are the id columns' values (a row's id is added to its
// part's as the row is), so their dictionaries are the set.
func (x *logEntry) seen(id string, ok bool) bool {
	if !ok {
		return x.nullSeen
	}
	for _, p := range x.parts {
		if b := p.builders["id"]; b != nil {
			b.db.index()
			if _, dup := b.db.ids[id]; dup {
				return true
			}
		}
	}
	return false
}

// measure is about what x takes: its columns as batchBytes counts them,
// the dictionaries' lookups when built, and the keys.
func (x *logEntry) measure() int64 {
	n := int64(len(x.errs)) * 64
	for _, p := range x.parts {
		for k, b := range p.builders {
			n += int64(len(k)) + 32
			c := &b.c
			n += int64(cap(c.Idx))*4 + int64(cap(c.Ints)+cap(c.Floats)+cap(c.Times))*8 + int64(cap(c.Null))
			for _, s := range b.db.dict {
				n += int64(len(s)) + 16
			}
			if b.db.ids != nil {
				n += int64(len(b.db.dict)) * 40
			}
		}
	}
	return n
}

// logView is a snapshot of an entry for one query: the rows it held then,
// and the columns the query reads, each cut to those rows.
type logView struct {
	names  []archive.LogColumn
	parts  []partView
	errs   []rowErr
	tailOK bool // the tail is a JSON object whose id no kept line has
	tailM  map[string]json.RawMessage
	line   int // the tail's line number
	idErr  error
}

// partView is a part's snapshot.
type partView struct {
	fam            famKey
	n              int
	cols           map[string]*Column // by key: the family, the day and those cols may need that the part has
	dayMin, dayMax int64
	dayNulls       int
}

// view is x's snapshot for cols, with tail the bytes past its last line.
func (x *logEntry) view(cols []string, tail []byte) *logView {
	if x.names == nil {
		x.names = archive.LogColumns(x.keys)
	}
	v := &logView{names: x.names, errs: x.errs[:len(x.errs):len(x.errs)], line: x.lines + 1}
	for _, p := range x.parts {
		pv := partView{fam: p.fam, n: p.n, cols: map[string]*Column{}, dayMin: p.dayMin, dayMax: p.dayMax, dayNulls: p.dayNulls}
		for k, b := range p.builders {
			if k == "family" || k == "day" || wanted(k, cols) {
				pv.cols[k] = b.finish(p.n)
			}
		}
		v.parts = append(v.parts, pv)
	}
	if len(tail) > 0 {
		if m, _ := archive.DecodeLine(tail); m != nil {
			id, ok, err := archive.JSONText(m["id"])
			switch {
			case err != nil:
				v.idErr = err
			case !x.seen(id, ok):
				v.tailOK, v.tailM = true, m
			}
		}
	}
	return v
}

// batches is readLog's rows of the view, cols of the rows of family (any,
// when "") with day in r, a batch per part (each of a part's rows, or a
// copy of those in r) and one for the tail, at least one.
func (v *logView) batches(path string, cols []string, family string, r Range) ([]*Batch, error) {
	from, to := r.days()
	ofFamily := func(p int) bool {
		return family == "" || v.parts[p].fam == famKey{family, true}
	}
	inRange := func(p, i int) bool {
		day := v.parts[p].cols["day"]
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
			x.kind == errDay && ofFamily(x.part),
			x.kind == errCol && ofFamily(x.part) && inRange(x.part, x.row) && wanted(x.key, cols):
			return nil, x.error(path)
		}
	}
	names := make([]string, len(v.names))
	keyOf := make(map[string]string, len(v.names))
	for i, c := range v.names {
		names[i] = c.Name
		keyOf[c.Name] = c.Key
	}
	requested := expandCols(names, cols)
	var out []*Batch
	for pi, p := range v.parts {
		if !ofFamily(pi) || p.n == p.dayNulls || p.dayMax < from || p.dayMin > to {
			continue
		}
		var sel []int32
		if p.dayNulls > 0 || p.dayMin < from || p.dayMax > to {
			sel = make([]int32, 0, p.n)
			for i := range p.n {
				if inRange(pi, i) {
					sel = append(sel, int32(i))
				}
			}
		}
		n := p.n
		if sel != nil {
			n = len(sel)
		}
		b := &Batch{N: n, Cols: make(map[string]*Column, len(requested))}
		for _, name := range requested {
			k, ok := keyOf[name]
			c := p.cols[k]
			switch {
			case !ok || c == nil:
				b.Cols[name] = nullColumn(name, n)
			case sel == nil:
				b.Cols[name] = c
			default:
				b.Cols[name] = c.take(sel)
			}
		}
		out = append(out, b)
	}
	if v.idErr != nil {
		return nil, fmt.Errorf("%s line %d: id: %w", path, v.line, v.idErr)
	}
	if v.tailOK {
		b, err := v.tailBatch(path, cols, requested, keyOf, family, from, to)
		if err != nil {
			return nil, err
		}
		if b != nil {
			out = append(out, b)
		}
	}
	if len(out) == 0 {
		b := &Batch{Cols: make(map[string]*Column, len(requested))}
		for _, name := range requested {
			b.Cols[name] = nullColumn(name, 0)
		}
		out = append(out, b)
	}
	return out, nil
}

// tailBatch is the view's tail, a JSON object whose id no kept line has,
// as readLog reads such a last line: a row when of family with day in
// [from, to] (nil when not), its keys named as the complete lines name
// them (a key only it has is not read).
func (v *logView) tailBatch(path string, cols, requested []string, keyOf map[string]string, family string, from, to int64) (*Batch, error) {
	m := v.tailM
	if family != "" {
		if fam, ok, _ := archive.JSONText(m["family"]); !ok || fam != family {
			return nil, nil
		}
	}
	ds, ok, err := archive.JSONText(m["day"])
	if err != nil {
		return nil, fmt.Errorf("%s line %d: day: %w", path, v.line, err)
	}
	if !ok {
		return nil, nil
	}
	d, err := archive.ParseDay(ds)
	if err != nil {
		return nil, fmt.Errorf("%s line %d: day: %w", path, v.line, err)
	}
	if d < from || d > to {
		return nil, nil
	}
	// Every key cols may need is cast, named or not, as readLog casts it.
	vals := map[string]*Column{}
	for k, raw := range m {
		if !wanted(k, cols) {
			continue
		}
		b := &logCol{key: k, kind: logKind(k)}
		if err := b.add(0, raw); err != nil {
			return nil, fmt.Errorf("%s line %d: %s: %w", path, v.line, k, err)
		}
		vals[k] = b.finish(1)
	}
	out := &Batch{N: 1, Cols: make(map[string]*Column, len(requested))}
	for _, name := range requested {
		if k, ok := keyOf[name]; ok && vals[k] != nil {
			out.Cols[name] = vals[k]
		} else {
			out.Cols[name] = nullColumn(name, 1)
		}
	}
	return out, nil
}
