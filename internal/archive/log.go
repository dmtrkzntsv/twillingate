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

// Log appends events to raw/<project>/<day>.log, one descriptor per open
// log, fsynced once per WriteEvents (the pipeline's flush).
type Log struct {
	dir   string
	mu    sync.Mutex
	files map[logKey]*os.File
	seen  map[string]time.Time
	now   func() time.Time
	// failWrite, when set, replaces the file write (tests).
	failWrite func(f *os.File, b []byte) (int, error)
}

type logKey struct {
	project int64
	day     string
}

// OpenLog opens the log under dir, cutting every open log back to its last
// newline: a line torn by a crash never reaches an append or a dump.
func OpenLog(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	logs, err := filepath.Glob(filepath.Join(dir, "*", "*.log"))
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	for _, p := range logs {
		if err := cutTornLine(p); err != nil {
			return nil, err
		}
	}
	return &Log{dir: dir, files: map[logKey]*os.File{}, seen: map[string]time.Time{}, now: time.Now}, nil
}

// cutTornLine truncates path after its last newline.
func cutTornLine(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	keep := bytes.LastIndexByte(b, '\n') + 1
	if keep == len(b) {
		return nil
	}
	if err := os.Truncate(path, int64(keep)); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	return nil
}

func (l *Log) Dir() string { return l.dir }

// WriteEvents appends each event not written in the last recentIDs to its
// project and day's log, then fsyncs every log it touched. A write that
// fails is cut back to the file's previous size and its ids are not
// remembered, so the pipeline's retry writes them again.
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
	ids := map[logKey][]string{}
	var order []logKey
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
		ids[k] = append(ids[k], e.ID)
	}
	var touched []*os.File
	for _, k := range order {
		f, err := l.file(k)
		if err != nil {
			return err
		}
		if err := l.append(f, batch[k].Bytes()); err != nil {
			return err
		}
		for _, id := range ids[k] {
			l.seen[id] = now
		}
		touched = append(touched, f)
	}
	var errs []error
	for _, f := range touched {
		if err := f.Sync(); err != nil {
			errs = append(errs, fmt.Errorf("archive: %w", err))
		}
	}
	return errors.Join(errs...)
}

// append writes b at the end of f in one write, cutting f back to its
// previous size if the write fails or falls short.
func (l *Log) append(f *os.File, b []byte) error {
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
		if terr := f.Truncate(st.Size()); terr != nil {
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
	f, err := os.OpenFile(filepath.Join(dir, k.day+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
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
	}
	for n := 1; ; n++ {
		dst := filepath.Join(dir, day+"."+strconv.Itoa(n)+".closed")
		if _, err := os.Stat(dst); errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(src, dst); err != nil {
				return "", fmt.Errorf("archive: %w", err)
			}
			return dst, nil
		}
	}
}

// Close closes every open log.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var errs []error
	for k, f := range l.files {
		errs = append(errs, f.Close())
		delete(l.files, k)
	}
	return errors.Join(errs...)
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
		"group_id": e.GroupID, "session_id": e.SessionID, "host": e.Host, "path": e.Path,
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
