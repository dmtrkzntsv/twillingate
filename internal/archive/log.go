package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// lineVersion is the log line's format (the v field).
const lineVersion = 1

// recentIDs is how long an id that reached the log keeps a repeat out of it:
// the pipeline's retries and an SDK's prompt ones. A later repeat is the
// dump's to drop (DISTINCT ON id).
const recentIDs = 10 * time.Minute

const tsFormat = "2006-01-02T15:04:05Z"

// ErrLocked: another Log, in this process or another, holds the directory.
// One process at a time writes raw/: the ingest server, or the import
// command while the server is stopped.
var ErrLocked = errors.New("archive: the events directory is in use by another process")

// Log appends events to raw/<project>/<day>.log, one descriptor per open
// log, fsynced once per WriteEvents (the pipeline's flush).
type Log struct {
	dir   string
	mu    sync.Mutex
	files map[logKey]*os.File
	seen  map[string]time.Time
	now   func() time.Time
	// lock is <dir>/.lock, held with flock until Close.
	lock *os.File
	// Test hooks: when set, each replaces the file operation it names.
	failWrite    func(f *os.File, b []byte) (int, error)
	failTruncate func(f *os.File, size int64) error
	failSync     func(f *os.File) error
}

type logKey struct {
	project int64
	day     string
}

// OpenLog opens the log under dir, taking its lock (ErrLocked while another
// Log holds it) and cutting every open log back to its last newline: a line
// torn by a crash never reaches an append or a dump.
func OpenLog(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s)", ErrLocked, dir)
		}
		return nil, fmt.Errorf("archive: lock %s: %w", dir, err)
	}
	logs, err := filepath.Glob(filepath.Join(dir, "*", "*.log"))
	if err != nil {
		lock.Close()
		return nil, fmt.Errorf("archive: %w", err)
	}
	for _, p := range logs {
		if err := cutTornLine(p); err != nil {
			lock.Close()
			return nil, err
		}
	}
	return &Log{dir: dir, files: map[logKey]*os.File{}, seen: map[string]time.Time{}, now: time.Now, lock: lock}, nil
}

// cutTornLine truncates path after its last newline. It reads the file
// from the end, so a long log costs one chunk, not a full read.
func cutTornLine(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	keep, err := afterLastNewline(f, st.Size())
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	if keep == st.Size() {
		return nil
	}
	if err := f.Truncate(keep); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	return nil
}

// afterLastNewline returns the offset just past the last newline in the
// first size bytes of f, or 0 when there is none.
func afterLastNewline(f *os.File, size int64) (int64, error) {
	buf := make([]byte, 4096)
	for pos := size; pos > 0; {
		n := int64(len(buf))
		if n > pos {
			n = pos
		}
		pos -= n
		if got, err := f.ReadAt(buf[:n], pos); err != nil && !(errors.Is(err, io.EOF) && int64(got) == n) {
			return 0, err
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			return pos + int64(i) + 1, nil
		}
	}
	return 0, nil
}

func (l *Log) Dir() string { return l.dir }

// WriteEvents appends each event not written in the last recentIDs to its
// project and day's log, then fsyncs every log it touched. Ids are
// remembered only once every touched log has synced: on any error none is,
// so the pipeline's retry writes the batch again (a line may then appear
// twice, which the dump's DISTINCT ON drops) and syncs it again. A write
// that fails is cut back to the file's previous size.
func (l *Log) WriteEvents(ctx context.Context, evs []store.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for id, at := range l.seen {
		if now.Sub(at) > recentIDs {
			delete(l.seen, id)
		}
	}
	batch := map[logKey]*bytes.Buffer{}
	var order []logKey
	var written []string
	inBatch := map[string]bool{}
	for _, e := range evs {
		if _, ok := l.seen[e.ID]; ok || inBatch[e.ID] {
			continue
		}
		inBatch[e.ID] = true
		b, err := line(e)
		if err != nil {
			return fmt.Errorf("archive: event %s: %w", e.ID, err)
		}
		k := logKey{e.ProjectID, Day(e.TS)}
		if batch[k] == nil {
			batch[k] = &bytes.Buffer{}
			order = append(order, k)
		}
		batch[k].Write(b)
		written = append(written, e.ID)
	}
	var touched []*os.File
	for _, k := range order {
		f, err := l.file(k)
		if err != nil {
			return err
		}
		if err := l.append(k, f, batch[k].Bytes()); err != nil {
			return err
		}
		touched = append(touched, f)
	}
	var errs []error
	for _, f := range touched {
		sync := f.Sync
		if l.failSync != nil {
			sync = func() error { return l.failSync(f) }
		}
		if err := sync(); err != nil {
			errs = append(errs, fmt.Errorf("archive: %w", err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	for _, id := range written {
		l.seen[id] = now
	}
	return nil
}

// append writes b at the end of f in one write, cutting f back to its
// previous size if the write fails or falls short. If the cut-back fails
// too, f is closed and forgotten: the next append reopens the file and
// cuts its torn tail first, so no line is ever appended after one.
func (l *Log) append(k logKey, f *os.File, b []byte) error {
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	write := func(f *os.File, b []byte) (int, error) { return f.Write(b) }
	if l.failWrite != nil {
		write = l.failWrite
	}
	n, err := write(f, b)
	if err == nil && n < len(b) {
		err = io.ErrShortWrite
	}
	if err != nil {
		cut := f.Truncate
		if l.failTruncate != nil {
			cut = func(size int64) error { return l.failTruncate(f, size) }
		}
		if terr := cut(st.Size()); terr != nil {
			delete(l.files, k)
			f.Close()
			return fmt.Errorf("archive: %w (and cutting it back: %v)", err, terr)
		}
		return fmt.Errorf("archive: %w", err)
	}
	return nil
}

// file returns the open log for k, opening it O_APPEND.
func (l *Log) file(k logKey) (*os.File, error) {
	if f := l.files[k]; f != nil {
		return f, nil
	}
	dir := filepath.Join(l.dir, strconv.FormatInt(k.project, 10))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	path := filepath.Join(dir, k.day+".log")
	if err := cutTornLine(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	l.files[k] = f
	return f, nil
}

// Rotate closes the project's log for day and renames it <day>.<n>.closed
// with the next free n, ready for its dump; a later event for the day
// starts a new log. It returns the closed file's path, or "" when the day
// has no log.
func (l *Log) Rotate(project int64, day string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := logKey{project, day}
	if f := l.files[k]; f != nil {
		delete(l.files, k)
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("archive: %w", err)
		}
	}
	dir := filepath.Join(l.dir, strconv.FormatInt(project, 10))
	src := filepath.Join(dir, day+".log")
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	dst, err := nextClosed(dir, day)
	if err != nil {
		return "", err
	}
	if err := os.Rename(src, dst); err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	return dst, nil
}

// nextClosed is dir's first free <day>.<n>.closed, n from 1. The caller
// holds l.mu, so no other rotation takes the name between
// the look and the rename.
func nextClosed(dir, day string) (string, error) {
	for n := 1; ; n++ {
		dst := filepath.Join(dir, day+"."+strconv.Itoa(n)+".closed")
		_, err := os.Stat(dst)
		if err == nil {
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("archive: %w", err)
		}
		return dst, nil
	}
}

// Close closes every open log and releases the directory's lock.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var errs []error
	for k, f := range l.files {
		errs = append(errs, f.Close())
		delete(l.files, k)
	}
	if l.lock != nil {
		errs = append(errs, l.lock.Close())
		l.lock = nil
	}
	return errors.Join(errs...)
}

// fields are the log line's fixed keys, in the row's column order, with the
// DuckDB type the dump reads each as; line writes no other key but
// attr_<key>, which the dump reads as VARCHAR (TestLineWritesTheFields).
var fields = []struct{ name, typ string }{
	{"v", "BIGINT"}, {"id", "VARCHAR"}, {"project_id", "BIGINT"}, {"family", "VARCHAR"},
	{"event_name", "VARCHAR"}, {"ts", "TIMESTAMP"}, {"day", "DATE"}, {"received_at", "TIMESTAMP"},
	{"kind", "VARCHAR"}, {"actor_id", "VARCHAR"}, {"actor_kind", "VARCHAR"}, {"user_id", "VARCHAR"},
	{"group_id", "VARCHAR"}, {"session_id", "VARCHAR"}, {"install_id", "VARCHAR"}, {"user_name", "VARCHAR"},
	{"group_name", "VARCHAR"}, {"host", "VARCHAR"}, {"path", "VARCHAR"}, {"referrer_source", "VARCHAR"},
	{"utm_source", "VARCHAR"}, {"utm_medium", "VARCHAR"},
	{"utm_campaign", "VARCHAR"}, {"platform", "VARCHAR"}, {"os", "VARCHAR"}, {"os_version", "VARCHAR"},
	{"os_name", "VARCHAR"}, {"browser", "VARCHAR"}, {"browser_version", "VARCHAR"},
	{"browser_locale", "VARCHAR"}, {"app_version", "VARCHAR"}, {"app_locale", "VARCHAR"},
	{"device", "VARCHAR"}, {"device_model", "VARCHAR"}, {"display_width", "BIGINT"},
	{"display_height", "BIGINT"}, {"country", "VARCHAR"}, {"consent", "BIGINT"}, {"value", "DOUBLE"},
	{"measure", "VARCHAR"}, {"sample_rate", "DOUBLE"},
}

// line is e as one NDJSON line shaped like its Parquet row (raw-first Q10):
// the events table's column names, attributes flat as attr_<key> (the key
// as stored, unsanitized), empty fields left out, v the format.
func line(e store.Event) ([]byte, error) {
	m := map[string]any{
		"v": lineVersion, "id": e.ID, "project_id": e.ProjectID, "family": string(e.Family),
		"event_name": e.EventName, "ts": e.TS.UTC().Format(tsFormat), "day": Day(e.TS),
	}
	if !e.ReceivedAt.IsZero() {
		m["received_at"] = e.ReceivedAt.UTC().Format(tsFormat)
	}
	for k, v := range map[string]string{
		"kind": e.Kind, "actor_id": e.ActorID, "actor_kind": e.ActorKind, "user_id": e.UserID,
		"group_id": e.GroupID, "session_id": e.SessionID, "install_id": e.InstallID,
		"user_name": e.UserName, "group_name": e.GroupName, "host": e.Host, "path": e.Path,
		"referrer_source": e.ReferrerSource, "utm_source": e.UTMSource, "utm_medium": e.UTMMedium,
		"utm_campaign": e.UTMCampaign, "platform": e.Platform, "os": e.OS, "os_version": e.OSVersion,
		"os_name": e.OSName, "browser": e.Browser, "browser_version": e.BrowserVersion,
		"browser_locale": e.BrowserLocale, "app_version": e.AppVersion, "app_locale": e.AppLocale,
		"device": e.Device, "device_model": e.DeviceModel, "country": e.Country, "measure": e.Measure,
	} {
		if v != "" {
			m[k] = v
		}
	}
	if e.DisplayWidth != 0 {
		m["display_width"] = e.DisplayWidth
	}
	if e.DisplayHeight != 0 {
		m["display_height"] = e.DisplayHeight
	}
	switch e.Consent {
	case store.ConsentGiven:
		m["consent"] = 1
	case store.ConsentNone:
		m["consent"] = 0
	}
	if e.Value != nil {
		m["value"] = *e.Value
	}
	rate := e.SampleRate
	if rate == 0 {
		rate = 1
	}
	m["sample_rate"] = rate
	for k, v := range e.Attributes {
		if !strings.HasPrefix(k, "$") {
			m["attr_"+k] = v
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// forget closes and drops a project's open logs (RemoveProject).
func (l *Log) forget(project int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, f := range l.files {
		if k.project == project {
			f.Close()
			delete(l.files, k)
		}
	}
}
