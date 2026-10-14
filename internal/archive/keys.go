package archive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// keyScanner remembers, per log, the keys of the lines read so far and
// where the first unread line starts, so a reader's rebuild reads only
// what was appended since: a log only grows until it is rotated, and a
// rotated log is a new path.
type keyScanner struct {
	mu   sync.Mutex
	logs map[string]*scannedLog
}

type scannedLog struct {
	info os.FileInfo // the file read, to tell a new file at the same path
	next int64       // offset of the first line not read yet
	keys map[string]bool
}

func newKeyScanner() *keyScanner { return &keyScanner{logs: map[string]*scannedLog{}} }

// keys returns the keys of path's complete lines: up to its last newline,
// so a line ingest is still writing is read on a later call. A file that
// is not the one read before at path (os.SameFile), or is shorter than
// what was read, is read from its start. A line that is not a JSON object
// is skipped: the dump, not a reader, refuses it. The map is the
// scanner's own: callers must not modify it.
func (s *keyScanner) keys(path string) (map[string]bool, error) {
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
	if sl == nil || !os.SameFile(sl.info, st) || st.Size() < sl.next {
		sl = &scannedLog{keys: map[string]bool{}}
		s.logs[path] = sl
	}
	sl.info = st
	if st.Size() <= sl.next {
		return sl.keys, nil
	}
	buf := make([]byte, st.Size()-sl.next)
	n, err := f.ReadAt(buf, sl.next)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("archive: %w", err)
	}
	buf = buf[:n]
	end := bytes.LastIndexByte(buf, '\n') + 1
	for _, line := range bytes.Split(buf[:end], []byte{'\n'}) {
		var m map[string]json.RawMessage
		if len(line) == 0 || json.Unmarshal(line, &m) != nil || m == nil {
			continue
		}
		for k := range m {
			sl.keys[k] = true
		}
	}
	sl.next += int64(end)
	return sl.keys, nil
}

// keep forgets every log but paths: the ones dumped or removed since.
func (s *keyScanner) keep(paths []string) {
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
