package archive

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// dumpLog writes the log at src (a claimed log, complete: claim cut any
// torn last line) as the Parquet file out, without the engine. The
// columns are columnsOf of every key of its lines: every fixed column in
// fields order, typed as fields says, then every other key as VARCHAR.
// Each line is decoded and cast as DuckDB's json_extract_string and CAST
// read it (decode.go). A line that is not a JSON object, a ts that cannot
// be cast (on any line, a repeat's too, as DuckDB's dump cast every ts) or
// a kept line's value its column's type cannot hold fails the dump, which
// then writes nothing. A cancelled ctx stops it the same way, within
// dumpCheckEvery lines.
//
// A repeated id keeps the line appended first, as SQLite keeps the first
// row it receives: a repeat need not be the same event (the server does
// not check), so a later one can differ in received_at, actor_id, country
// or attributes. Every line without an id counts as one id. The rows are
// then stably sorted by family, then ts, NULLs last (ties keep file
// order), and row groups are cut by eventsCut: where the family changes
// (a day's file holds one day, so its day never cuts), or after
// maxGroupRows rows.
//
// The log is read twice, so memory is bounded by its line count, not its
// size. Pass 1 decodes every line for its keys and holds, of each line
// with an id and of the first without one, only where it is (offset,
// length), its family and ts, and its id's hash (dumpIndex); the repeats
// are then dropped and the rest sorted. Pass 2 reads each kept line back
// by its offset, in that order, decodes it into one reused row and writes
// it. Pass 1 holds 44 bytes a line (keptLine, 24, the id's hash, 16, and
// a place in the sort that finds the repeats, 4), sized once from a count
// of the lines; pass 2 holds 24 a kept line. A 1M-line day holds about 44
// MB; the writer adds its own bound (rowWriter).
//
// Ids are compared by a 128-bit hash (two maphash seeds), as the merge
// compares them: two ids that collide would drop the later one's line,
// with a chance of about n²/2¹²⁹ for n ids. A key given twice in one
// object keeps its last value (Go's decoder); ingest never writes one.
func dumpLog(ctx context.Context, src, out string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	defer f.Close()
	name := filepath.Base(src)
	d := newDumpIndex()
	if err := d.read(ctx, f, name); err != nil {
		return err
	}
	cols := columnsOf(d.keySet())
	pq := make([]pqColumn, len(cols))
	col := make([]int, len(d.keys)) // each key's column
	for i, c := range cols {
		k := d.index[c.key]
		pq[i] = pqColumn{Name: c.name, Type: d.types[k]}
		col[k] = i
	}
	d.sort()
	// columnsOf lists the fields first, in order.
	return writeParquet(out, pq, eventsCut(fieldOf("family"), fieldOf("day")),
		func(w *rowWriter) error { return d.write(ctx, f, name, w, col) })
}

// dumpCheckEvery is how many lines a dump reads, in either pass, between
// two looks at its ctx.
const dumpCheckEvery = 4096

// dumpIndex is a log read for its dump: every key seen, in the order first
// seen (the fields first), with its type, and the kept lines.
type dumpIndex struct {
	index map[string]int
	keys  []string
	types []colType
	lines []keptLine

	families map[string]int32 // a family's place in names, interned
	names    []string
	rank     []int32 // names' places in byte order, once sorted

	// Until dropRepeats: each line's id hash (lines[i]'s at hashes[i]),
	// and the one line without an id kept (-1 for none).
	s1, s2 maphash.Seed
	hashes [][2]uint64
	nullAt int
}

// keptLine is a kept line: where it is in the log (off, and its length
// in n's low 31 bits), its family (an index into names, -1 for NULL) and
// its ts, NULL when n's high bit is set. 24 bytes.
type keptLine struct {
	off    int64
	ts     int64
	n      uint32
	family int32
}

const tsNull = 1 << 31

func (l keptLine) length() int { return int(l.n &^ tsNull) }
func (l keptLine) tsOK() bool  { return l.n&tsNull == 0 }

func newDumpIndex() *dumpIndex {
	d := &dumpIndex{index: map[string]int{}, families: map[string]int32{},
		s1: maphash.MakeSeed(), s2: maphash.MakeSeed(), nullAt: -1}
	for _, f := range fields {
		t, err := colTypeOf(f.typ)
		if err != nil {
			panic("archive: field " + f.name + ": " + err.Error())
		}
		d.add(f.name, t)
	}
	return d
}

func (d *dumpIndex) add(key string, t colType) {
	d.index[key] = len(d.keys)
	d.keys = append(d.keys, key)
	d.types = append(d.types, t)
}

// keySet is the keys seen that are no field, with the fields: columnsOf's
// input.
func (d *dumpIndex) keySet() map[string]bool {
	set := make(map[string]bool, len(d.keys))
	for _, k := range d.keys[len(fields):] {
		set[k] = true
	}
	return set
}

// read is pass 1 over the log in f. Blank lines are skipped, as DuckDB's
// newline-delimited reader skips them.
func (d *dumpIndex) read(ctx context.Context, f *os.File, name string) error {
	// The index is made once at the log's line count: grown by
	// appending, it would hold up to twice its lines.
	n, err := countLines(f)
	if err != nil {
		return fmt.Errorf("archive: %s: %w", name, err)
	}
	if n > math.MaxInt32 {
		return fmt.Errorf("archive: %s: %d lines, more than a dump takes", name, n)
	}
	d.lines = make([]keptLine, 0, n)
	d.hashes = make([][2]uint64, 0, n)
	var (
		buf []byte
		off int64
	)
	br := bufio.NewReaderSize(io.NewSectionReader(f, 0, math.MaxInt64), 1<<20)
	for lineNo := 1; ; lineNo++ {
		if lineNo%dumpCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("archive: %s: %w", name, err)
			}
		}
		line, rerr := ReadLine(br, &buf)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return fmt.Errorf("archive: %s: %w", name, rerr)
		}
		if len(bytes.TrimSpace(line)) > 0 {
			if len(line) > math.MaxInt32 {
				return fmt.Errorf("archive: %s line %d: longer than 2 GB", name, lineNo)
			}
			if err := d.line(line, off); err != nil {
				return fmt.Errorf("archive: %s line %d: %w", name, lineNo, err)
			}
		}
		off += int64(len(line))
		if rerr != nil {
			d.dropRepeats()
			return nil
		}
	}
}

// line notes line's keys and, when it is the first of its id, indexes it.
func (d *dumpIndex) line(line []byte, off int64) error {
	m, object := DecodeLine(line)
	if !object {
		return errors.New("not a JSON object")
	}
	for k := range m {
		if _, seen := d.index[k]; !seen {
			d.add(k, colVarchar)
		}
	}
	id, ok, err := JSONText(m["id"])
	if err != nil {
		return fmt.Errorf("id: %w", err)
	}
	// Every line's ts is cast, a repeat's too, as DuckDB's dump cast it
	// before it kept the first line of each id.
	ts, err := castValue(m["ts"], colTimestamp)
	if err != nil {
		return fmt.Errorf("ts: %w", err)
	}
	if !ok {
		if d.nullAt >= 0 {
			return nil // every line without an id is one id: the first is kept
		}
		d.nullAt = len(d.lines)
	}
	l := keptLine{off: off, n: uint32(len(line)), family: -1}
	fam, err := castValue(m["family"], colVarchar)
	if err != nil {
		return fmt.Errorf("family: %w", err)
	}
	if !fam.null() {
		i, seen := d.families[fam.s]
		if !seen {
			i = int32(len(d.names))
			d.families[fam.s] = i
			d.names = append(d.names, fam.s)
		}
		l.family = i
	}
	l.ts = ts.n
	if ts.null() {
		l.n |= tsNull
	}
	d.lines = append(d.lines, l)
	if ok {
		d.hashes = append(d.hashes, [2]uint64{maphash.String(d.s1, id), maphash.String(d.s2, id)})
	} else {
		d.hashes = append(d.hashes, [2]uint64{})
	}
	return nil
}

// countLines is how many lines f holds, a last one without its newline
// included: a quick read, without decoding.
func countLines(f *os.File) (int, error) {
	buf := make([]byte, 1<<20)
	var (
		n    int
		off  int64
		last byte = '\n'
	)
	for {
		k, err := f.ReadAt(buf, off)
		n += bytes.Count(buf[:k], []byte{'\n'})
		if k > 0 {
			last = buf[k-1]
		}
		off += int64(k)
		if errors.Is(err, io.EOF) {
			if last != '\n' {
				n++
			}
			return n, nil
		}
		if err != nil {
			return 0, err
		}
	}
}

// dropRepeats keeps, of each id, its first line, the lines being in file
// order: the lines with an id sorted by their id's hash, then their place,
// each line whose hash the one before it has is a repeat. It frees the
// hashes.
func (d *dumpIndex) dropRepeats() {
	perm := make([]int32, 0, len(d.lines))
	for i := range d.lines {
		if i != d.nullAt {
			perm = append(perm, int32(i))
		}
	}
	h := d.hashes
	slices.SortFunc(perm, func(a, b int32) int {
		if c := cmp.Compare(h[a][0], h[b][0]); c != 0 {
			return c
		}
		if c := cmp.Compare(h[a][1], h[b][1]); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	drop := make([]uint64, (len(d.lines)+63)/64)
	for j := 1; j < len(perm); j++ {
		if h[perm[j]] == h[perm[j-1]] {
			drop[perm[j]/64] |= 1 << (perm[j] % 64)
		}
	}
	dropped := func(i int) bool { return drop[i/64]&(1<<(i%64)) != 0 }
	perm, d.hashes = nil, nil
	kept := d.lines[:0]
	for i, l := range d.lines {
		if !dropped(i) {
			kept = append(kept, l)
		}
	}
	d.lines = kept
}

// sort orders the kept lines by family, then ts, NULLs last, stably.
func (d *dumpIndex) sort() {
	order := make([]int32, len(d.names))
	for i := range order {
		order[i] = int32(i)
	}
	slices.SortFunc(order, func(a, b int32) int { return strings.Compare(d.names[a], d.names[b]) })
	d.rank = make([]int32, len(d.names))
	for r, i := range order {
		d.rank[i] = int32(r)
	}
	slices.SortStableFunc(d.lines, func(x, y keptLine) int {
		if c := d.cmpFamily(x.family, y.family); c != 0 {
			return c
		}
		switch {
		case x.tsOK() && y.tsOK():
			return cmp.Compare(x.ts, y.ts)
		case x.tsOK():
			return -1
		case y.tsOK():
			return 1
		}
		return 0
	})
}

func (d *dumpIndex) cmpFamily(x, y int32) int {
	switch {
	case x >= 0 && y >= 0:
		return cmp.Compare(d.rank[x], d.rank[y])
	case x >= 0:
		return -1
	case y >= 0:
		return 1
	}
	return 0
}

// write is pass 2: each kept line read back from f, in order, decoded
// into one row of the file's columns (col is each key's) and written.
func (d *dumpIndex) write(ctx context.Context, f *os.File, name string, w *rowWriter, col []int) error {
	buf := make(row, len(w.cols))
	var (
		line []byte
		set  []int // buf's columns the last row set
	)
	for n, l := range d.lines {
		if n%dumpCheckEvery == dumpCheckEvery-1 {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("archive: %s: %w", name, err)
			}
		}
		line = slices.Grow(line[:0], l.length())[:l.length()]
		if _, err := f.ReadAt(line, l.off); err != nil {
			return fmt.Errorf("archive: %s: %w", name, err)
		}
		m, object := DecodeLine(line)
		if !object {
			return fmt.Errorf("archive: %s at byte %d: not a JSON object", name, l.off)
		}
		for _, i := range set {
			buf[i] = value{}
		}
		set = set[:0]
		for k, raw := range m {
			key, known := d.index[k]
			if !known {
				return fmt.Errorf("archive: %s at byte %d: key %q not read before", name, l.off, visible(k))
			}
			v, err := castValue(raw, d.types[key])
			if err != nil {
				return fmt.Errorf("archive: %s at byte %d: %s: %w", name, l.off, visible(k), err)
			}
			buf[col[key]] = v
			set = append(set, col[key])
		}
		if err := w.Write(buf); err != nil {
			return err
		}
	}
	return nil
}

// castValue is raw as json_extract_string gives it, then CAST to t; a
// JSON null is NULL.
func castValue(raw json.RawMessage, t colType) (value, error) {
	s, ok, err := JSONText(raw)
	if err != nil || !ok {
		return value{}, err
	}
	switch t {
	case colVarchar:
		return strValue(s), nil
	case colBigint:
		n, err := ParseInt(s)
		return intValue(n), err
	case colDouble:
		f, err := strconv.ParseFloat(s, 64)
		return floatValue(f), err
	case colDate:
		d, err := ParseDay(s)
		return dateValue(int32(d)), err
	case colTimestamp:
		us, err := ParseTime(s)
		return timestampValue(us), err
	}
	return value{}, fmt.Errorf("unknown type %d", t)
}

// colTypeOf is a DuckDB type name of fields as the writer's colType.
func colTypeOf(typ string) (colType, error) {
	switch typ {
	case "VARCHAR":
		return colVarchar, nil
	case "BIGINT":
		return colBigint, nil
	case "DOUBLE":
		return colDouble, nil
	case "DATE":
		return colDate, nil
	case "TIMESTAMP":
		return colTimestamp, nil
	}
	return 0, fmt.Errorf("unknown type %s", typ)
}
