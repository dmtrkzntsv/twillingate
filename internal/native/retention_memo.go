package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
)

// The retention memo's bounds: how many answers it keeps, their bytes
// together, and how long one is kept. A dashboard's retention widgets
// (a grid, two curves, two milestone rows over one range) load together,
// so a short memo is enough for them to share one scan.
const (
	retentionMemoEntries = 16
	retentionMemoBytes   = 8 << 20
	retentionMemoAge     = time.Minute
)

// retentionKeyBytes is about what one count costs in a memoised map.
const retentionKeyBytes = 64

// retentionTally is one retention scan's counts (countRetention's) and the
// day they count up to, leaving it out. A memoised tally is shared: it is
// read only.
type retentionTally struct {
	today  int64 // days since 1970-01-01
	counts map[retentionKey]int64
}

func (t *retentionTally) bytes() int64 { return int64(len(t.counts)) * retentionKeyBytes }

// retentionMemo keeps recent retention tallies, so queries that differ in
// their output alone (grid, curve, milestones) scan once. Concurrent ones
// missing it share one scan (singleflight); a failed scan is not kept. A
// tally is keyed by everything it depends on: the project, the range, the
// offsets, the where, the rules, today and the project's files (names,
// sizes, modification times), so a file that changes, appears or goes
// takes the old tally out of use; the next tally of the same query
// replaces it.
type retentionMemo struct {
	sf singleflight.Group

	mu      sync.Mutex
	bytes   int64
	entries map[string]*memoEntry
}

type memoEntry struct {
	query string // the key without the files
	at    time.Time
	tally *retentionTally
}

// get is the tally kept under key, unless older than retentionMemoAge at now.
func (m *retentionMemo) get(key string, now time.Time) *retentionTally {
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.entries[key]
	if x == nil || now.Sub(x.at) >= retentionMemoAge {
		return nil
	}
	return x.tally
}

// put keeps t under key: it drops the same query's tallies of other files
// and those past their age, then the oldest while over the bounds. A tally
// bigger than the bytes allowed is not kept.
func (m *retentionMemo) put(query, key string, t *retentionTally, now time.Time) {
	n := t.bytes()
	if n > retentionMemoBytes {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = map[string]*memoEntry{}
	}
	for k, x := range m.entries {
		if k == key || x.query == query || now.Sub(x.at) >= retentionMemoAge {
			m.drop(k)
		}
	}
	for len(m.entries) >= retentionMemoEntries || m.bytes+n > retentionMemoBytes {
		oldest := ""
		for k, x := range m.entries {
			if oldest == "" || x.at.Before(m.entries[oldest].at) {
				oldest = k
			}
		}
		m.drop(oldest)
	}
	m.entries[key] = &memoEntry{query: query, at: now, tally: t}
	m.bytes += n
}

func (m *retentionMemo) drop(k string) {
	m.bytes -= m.entries[k].tally.bytes()
	delete(m.entries, k)
}

// onRetentionScan is called as retentionCounts scans rather than taking a
// tally from the memo or a scan under way; a variable so a test can count
// the scans.
var onRetentionScan = func() {}

// retentionCounts is the tally of spec over the query's project, where and
// rules: from the memo, from a scan another query has under way (waiting
// for it until ctx is done), or scanned. When the scan it waited for ends
// with that query's own ctx error while ctx is not done, it tries again.
func (s scan) retentionCounts(ctx context.Context, spec retentionSpec) (*retentionTally, error) {
	all, err := archive.ListFiles(s.e.dir)
	if err != nil {
		return nil, err
	}
	files := all[s.project]
	query, key, err := s.retentionMemoKey(spec, files)
	if err != nil {
		return nil, err
	}
	m := &s.e.retMemo
	for {
		if t := m.get(key, s.e.now()); t != nil {
			return t, nil
		}
		ch := m.sf.DoChan(key, func() (any, error) {
			if t := m.get(key, s.e.now()); t != nil { // kept meanwhile
				return t, nil
			}
			onRetentionScan()
			actors, err := s.retentionActors(ctx, spec, files, s.e.charge())
			if err != nil {
				return nil, err
			}
			t := &retentionTally{today: epochDay(spec.today), counts: countRetention(actors, spec.offsets)}
			m.put(query, key, t, s.e.now())
			return t, nil
		})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-ch:
			if r.Err != nil {
				if (errors.Is(r.Err, context.Canceled) || errors.Is(r.Err, context.DeadlineExceeded)) && ctx.Err() == nil {
					continue // the scan was another query's, which gave up
				}
				return nil, r.Err
			}
			return r.Val.(*retentionTally), nil
		}
	}
}

// retentionMemoKey is the memo's key for spec over files: query, what the
// tally depends on but the files, and key, query with the files' names,
// sizes and modification times.
func (s scan) retentionMemoKey(spec retentionSpec, files archive.Files) (query, key string, err error) {
	where, err := json.Marshal(s.q.Where)
	if err != nil {
		return "", "", err
	}
	query = fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%d\x00%s\x00%+v", s.project, spec.from, spec.to, spec.today, spec.offsets, where, s.rules)
	paths := append(append([]string{}, files.Parquet...), files.Logs...)
	for _, p := range []string{files.Identities, files.Aliases} {
		if p != "" {
			paths = append(paths, p)
		}
	}
	sig, err := filesSignature(paths)
	if err != nil {
		return "", "", err
	}
	var sb strings.Builder
	sb.WriteString(query)
	sb.WriteString("\x00")
	sb.WriteString(sig)
	return query, sb.String(), nil
}
