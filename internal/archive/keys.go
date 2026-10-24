package archive

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"sync"
)

// keyChunk is how many bytes Keys reads of a log at a time: a first read of
// a day's log costs this much memory, not the log's size. A variable so a
// test can make it small enough to split lines.
var keyChunk = 1 << 20

// KeyScanner remembers, per log, the keys of the lines read so far and
// where the first unread line starts, so a reader's rebuild reads only
// what was appended since: a log only grows until it is rotated, and a
// rotated log is a new path. It is the one reader of a log's keys (the
// native engine's attribute rules), safe for concurrent use; its lock is
// held while it reads.
type KeyScanner struct {
	mu   sync.Mutex
	logs map[string]*scannedLog
}

type scannedLog struct {
	info os.FileInfo // the file read, to tell a new file at the same path
	next int64       // offset of the first line not read yet
	keys map[string]bool
}

// NewKeyScanner returns a scanner that has read no log.
func NewKeyScanner() *KeyScanner { return &KeyScanner{logs: map[string]*scannedLog{}} }

// Keys returns the keys of path's complete lines: up to its last newline,
// so a line ingest is still writing is read on a later call. A file that
// is not the one read before at path (os.SameFile), or is shorter than
// what was read, or does not end a line where the read stopped (a writer's
// cut-back), is read from its start. A line that is not a JSON object
// is skipped: the dump, not a reader, refuses it. The returned set is a
// new copy: changing it does not change what the scanner holds.
func (s *KeyScanner) Keys(path string) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	sl := s.logs[path]
	if sl == nil || !os.SameFile(sl.info, st) || st.Size() < sl.next || !endsLine(f, sl.next) {
		sl = &scannedLog{keys: map[string]bool{}}
		s.logs[path] = sl
	}
	sl.info = st
	if st.Size() > sl.next {
		if err := sl.read(f, st.Size()); err != nil {
			return nil, err
		}
	}
	return maps.Clone(sl.keys), nil
}

// endsLine reports whether the byte before off is a newline (true for off
// 0): the writer cuts a failed append back, and the file can then grow past
// the offset read on into the middle of a new line, so an offset is trusted
// only where a line ends. An unreadable byte counts as no.
func endsLine(f *os.File, off int64) bool {
	if off == 0 {
		return true
	}
	var b [1]byte
	n, err := f.ReadAt(b[:], off-1)
	return n == 1 && b[0] == '\n' && (err == nil || errors.Is(err, io.EOF))
}

// read adds the keys of the complete lines of f from sl.next up to size,
// in chunks of keyChunk bytes; a line longer than a chunk is still read
// whole. sl.next moves past each complete line and no further: a torn
// last line is read again by the next call.
func (sl *scannedLog) read(f *os.File, size int64) error {
	r := bufio.NewReaderSize(io.NewSectionReader(f, sl.next, size-sl.next), keyChunk)
	for {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("archive: %w", err)
		}
		sl.next += int64(len(line))
		var m map[string]json.RawMessage
		if json.Unmarshal(line, &m) != nil || m == nil {
			continue
		}
		for k := range m {
			sl.keys[k] = true
		}
	}
}

// Keep forgets every log but paths: the ones dumped or removed since.
func (s *KeyScanner) Keep(paths []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := map[string]bool{}
	for _, p := range paths {
		live[p] = true
	}
	for p := range s.logs {
		if !live[p] {
			delete(s.logs, p)
		}
	}
}
