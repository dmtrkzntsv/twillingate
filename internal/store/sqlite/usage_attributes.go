package sqlite

import (
	"context"
	"database/sql"
	"sync"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// declarableValues pairs each reserved key a project may declare
// (store.DeclarableAttributes) with the event's column value; empty when
// the event did not carry it.
func declarableValues(e store.Event) [9]struct{ key, value string } {
	return [9]struct{ key, value string }{
		{"$host", e.Host}, {"$path", e.Path}, {"$referrer", e.ReferrerSource},
		{"$utm_source", e.UTMSource}, {"$utm_medium", e.UTMMedium}, {"$utm_campaign", e.UTMCampaign},
		{"$os_version", e.OSVersion}, {"$browser_version", e.BrowserVersion}, {"$device_model", e.DeviceModel},
	}
}

type receivedKey struct {
	project int64
	day     string
	key     string
}

// receivedKeys collects the (project, day, key) triples a batch's inserted
// product and measure rows carried.
type receivedKeys map[receivedKey]struct{}

func (c receivedKeys) add(e store.Event, day string) {
	if e.Family == store.FamilyViews {
		return
	}
	for k := range e.Attributes {
		c[receivedKey{e.ProjectID, day, k}] = struct{}{}
	}
	for _, kv := range declarableValues(e) {
		if kv.value != "" {
			c[receivedKey{e.ProjectID, day, kv.key}] = struct{}{}
		}
	}
}

// receivedSeen remembers the triples ingest has already written to
// usage_attributes, so a batch whose keys are all known writes nothing
// more: ingest records that a key arrived, and the daily pass counts it.
// It holds the newest day a batch carried and the day before; older days
// leave it as the newest day moves on, and a late event for one falls back
// to INSERT OR IGNORE, which writes nothing when the row exists. Empty
// after a restart, when each key costs one ignored insert per day.
//
// gen is the generation a batch lists its unseen triples at, and a batch
// remembers only at the current one. Nothing moves usage rows any more (a
// rename keeps only its rule), so it stays at zero until the table goes.
type receivedSeen struct {
	mu     sync.Mutex
	keys   map[receivedKey]struct{}
	newest string
	gen    uint64
}

// unseen lists the batch's triples not yet remembered, and the generation
// to remember them at.
func (s *receivedSeen) unseen(batch receivedKeys) ([]receivedKey, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []receivedKey
	for k := range batch {
		if _, ok := s.keys[k]; !ok {
			out = append(out, k)
		}
	}
	return out, s.gen
}

// remember records the triples a committed batch wrote, then drops the
// days before the newest day's eve. A batch listed before a forget
// (gen behind) remembers nothing: its rows may have been moved.
func (s *receivedSeen) remember(written []receivedKey, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen {
		return
	}
	if s.keys == nil {
		s.keys = map[receivedKey]struct{}{}
	}
	moved := false
	for _, k := range written {
		if k.day > s.newest {
			s.newest, moved = k.day, true
		}
	}
	floor := s.newest
	if d, err := civil.Parse(s.newest); err == nil {
		floor = d.AddDays(-1).String()
	}
	if moved {
		for k := range s.keys {
			if k.day < floor {
				delete(s.keys, k)
			}
		}
	}
	for _, k := range written {
		if k.day >= floor {
			s.keys[k] = struct{}{}
		}
	}
}

// writeReceived inserts the triples ingest has not seen yet, with events 0
// until the daily pass counts their day; a row already there (a restart,
// or a late event for an older day) is left as it is.
func writeReceived(ctx context.Context, tx *sql.Tx, keys []receivedKey) error {
	if len(keys) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO usage_attributes (project_id, day, attr_key, events)
		VALUES (?, ?, ?, 0)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, k := range keys {
		if _, err := stmt.ExecContext(ctx, k.project, k.day, k.key); err != nil {
			return err
		}
	}
	return nil
}
