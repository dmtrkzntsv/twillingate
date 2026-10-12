package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
// An attribute rename moves usage rows to another key, so it forgets the
// project's triples (forget). gen counts the forgets: a batch remembers
// only if none happened since it listed its unseen triples, since a rename
// can commit between the batch's commit and its remember.
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

// forget drops the project's triples, after a rename moved its usage rows
// (spec D7): the next batch that carries one of its keys inserts the row
// again, or finds it there.
func (s *receivedSeen) forget(projectID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	for k := range s.keys {
		if k.project == projectID {
			delete(s.keys, k)
		}
	}
}

// writeReceived inserts the triples ingest has not seen yet, with events 0
// until the daily pass counts their day; a row already there (a restart,
// or a late event for an older day) is left as it is. Each key's catalog
// row (project_attributes) is not written here: insertEvents resolved it,
// with its id, before the row that carries it (events encoding spec D8).
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

// receivedSQL lists every (project, day, family, event, measure, key,
// value) the raw product and measure rows before ?1 carry, on their stored
// ids (events encoding spec D14): their encoded attributes, keyed by
// catalog id with value numbers, and the declarable reserved columns that
// are set, keyed by their reserved key with strings ids. Each raw row is
// read once: jsonb_patch lays the set columns over the attributes as one
// object (a null in a merge patch removes the key, so an unset column adds
// nothing) and json_each walks it; one arm per column instead re-read every
// raw page ten times (the JSONB forms skip rendering the merged text). r.
// qualifies the row's columns, since json_each has a path column too. A
// catalog id never collides with a reserved key: ids are digits, and
// ingest drops every `$` key it does not route to a column
// (server.resolveAttributes).
var receivedSQL = func() string {
	cols := []struct{ key, col string }{
		{"$host", "host"}, {"$path", "path"}, {"$referrer", "referrer_source"},
		{"$utm_source", "utm_source"}, {"$utm_medium", "utm_medium"}, {"$utm_campaign", "utm_campaign"},
		{"$os_version", "os_version"}, {"$browser_version", "browser_version"}, {"$device_model", "device_model"},
	}
	var names, pairs []string
	for _, c := range cols {
		names = append(names, c.col)
		pairs = append(pairs, "'"+c.key+"', NULLIF(r."+c.col+", 0)")
	}
	return `SELECT r.project_id, r.day, r.family, r.event_name, r.measure, j.key AS k, j.value AS v
		FROM (SELECT project_id, day, family, event_name, measure, attributes, ` + strings.Join(names, ", ") + `
		      FROM events WHERE family IN (2, 3) AND day < ?1) r,
		     json_each(jsonb_patch(r.attributes, jsonb_object(` + strings.Join(pairs, ", ") + `))) j`
}()

// countReceived rewrites usage_attributes for every day before today:
// the days whose raw product or measure rows remain get exact counts and
// max_values (the most distinct values in one (family, event, measure)
// partition, the partition ATTRIBUTE_VALUES_TOP_N caps); the rest, days
// already rolled up, are dropped. Today stays as ingest counts it. The
// partitions count on the stored ids, which are one-to-one with the text,
// and each partition's key decodes once: a catalog id to its key (one with
// no catalog row is dropped, as the raw_* views drop it), a reserved key
// as it is.
func countReceived(ctx context.Context, tx *sql.Tx, today string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_attributes WHERE day < ?1`, today); err != nil {
		return fmt.Errorf("usage attributes: %w", err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO usage_attributes (project_id, day, attr_key, events, max_values)
		SELECT project_id, day, key, SUM(n), MAX(vals) FROM (
		  SELECT project_id, day, n, vals,
		         CASE WHEN k LIKE '$%' THEN k
		              ELSE (SELECT key FROM project_attributes WHERE id = CAST(k AS INTEGER)) END AS key
		  FROM (
		    SELECT project_id, day, k, COUNT(*) AS n, COUNT(DISTINCT v) AS vals
		    FROM (`+receivedSQL+`)
		    GROUP BY project_id, day, family, event_name, measure, k
		  )
		) WHERE key IS NOT NULL GROUP BY project_id, day, key`, today)
	if err != nil {
		return fmt.Errorf("usage attributes: %w", err)
	}
	// The catalog follows usage_attributes: heal a key whose catalog row
	// was lost (only for projects that still exist: usage_attributes has no
	// foreign key, so it can hold a deleted project's rows). Dropping the
	// rows that are no breakdown and have no usage row left is the daily
	// pass's CleanDictionaries, which also waits for the raw rows that may
	// still carry the key (events encoding spec D7, D8).
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_attributes (project_id, key)
		SELECT DISTINCT project_id, attr_key FROM usage_attributes
		WHERE project_id IN (SELECT id FROM projects)`); err != nil {
		return fmt.Errorf("attribute catalog: %w", err)
	}
	return nil
}
