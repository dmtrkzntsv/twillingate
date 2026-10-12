package sqlite

import (
	"context"
	"database/sql"
	"sync"
)

// dict resolves text to its dictionary id (events encoding spec D2, D6,
// D8) inside ingest's write transaction and remembers, per id, the newest
// day it raised last_seen_day to (D7), so a known id costs one upsert per
// day, not per event. An id learned in a transaction reaches the shared
// maps only after that transaction committed (txDict.commit): a
// rolled-back insert would otherwise leave a cached id with no row.
//
// The maps only grow while the process runs: one entry per distinct text,
// key and value ingest has written, which the raw window bounds as it
// bounds the dictionaries themselves (on prod, a few thousand entries).
type dict struct {
	mu   sync.Mutex
	strs map[string]seenID
	keys map[keyRef]seenID
	vals map[valRef]seenID
}

// seenID is an id and the newest day ingest raised its last_seen_day to.
type seenID struct {
	id  int64
	day string
}

type keyRef struct {
	project int64
	key     string
}

type valRef struct {
	key   int64
	value string
}

func newDict() *dict {
	return &dict{strs: map[string]seenID{}, keys: map[keyRef]seenID{}, vals: map[valRef]seenID{}}
}

// txDict is one write transaction's resolver: the transaction, and what
// it learned, kept apart from the shared maps until commit. It runs the
// upserts as plain statements, not prepared ones: it reaches the database
// only for a text, key or value not yet touched that day, a handful per
// batch once the maps are warm.
type txDict struct {
	d    *dict
	tx   *sql.Tx
	strs map[string]seenID
	keys map[keyRef]seenID
	vals map[valRef]seenID
}

// Each upsert resolves and touches in one statement (D7.4): it returns the
// id of the text, inserting it again under a new id if the cleanup deleted
// it, and raises last_seen_day to the row's day. Ingest always writes the
// id an upsert returned, never one cached for an earlier day.
const (
	dictStrSQL = `INSERT INTO strings (value, last_seen_day) VALUES (?1, ?2)
		ON CONFLICT (value) DO UPDATE SET last_seen_day = max(last_seen_day, excluded.last_seen_day)
		RETURNING id`
	dictKeySQL = `INSERT INTO project_attributes (project_id, key, last_seen_day) VALUES (?1, ?2, ?3)
		ON CONFLICT (project_id, key) DO UPDATE SET last_seen_day = max(last_seen_day, excluded.last_seen_day)
		RETURNING id`
	// A new value takes the key's counter (values_next), which the
	// attribute_values_next trigger raises past it: a number is never
	// reused within a key. A key with no catalog row inserts nothing and
	// returns no row.
	dictValSQL = `INSERT INTO attribute_values (key_id, n, value, last_seen_day)
		SELECT id, values_next, ?2, ?3 FROM project_attributes WHERE id = ?1
		ON CONFLICT (key_id, value) DO UPDATE SET last_seen_day = max(last_seen_day, excluded.last_seen_day)
		RETURNING n`
)

// begin starts tx's resolver. The caller calls commit once tx has
// committed, and drops it otherwise.
func (d *dict) begin(tx *sql.Tx) *txDict {
	return &txDict{d: d, tx: tx, strs: map[string]seenID{}, keys: map[keyRef]seenID{}, vals: map[valRef]seenID{}}
}

// cached returns the id known for ref if it was already touched for day or
// a later one, from this transaction first, then the shared map.
func cached[K comparable](local, shared map[K]seenID, mu *sync.Mutex, ref K, day string) (int64, bool) {
	if s, ok := local[ref]; ok && s.day >= day {
		return s.id, true
	}
	mu.Lock()
	s, ok := shared[ref]
	mu.Unlock()
	if ok && s.day >= day {
		return s.id, true
	}
	return 0, false
}

// str is value's strings id, 0 for "" (which has no row).
func (t *txDict) str(ctx context.Context, value, day string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	if id, ok := cached(t.strs, t.d.strs, &t.d.mu, value, day); ok {
		return id, nil
	}
	var id int64
	if err := t.tx.QueryRowContext(ctx, dictStrSQL, value, day).Scan(&id); err != nil {
		return 0, err
	}
	t.strs[value] = seenID{id, day}
	return id, nil
}

// key is the catalog id of the project's key, adding its row when new.
func (t *txDict) key(ctx context.Context, projectID int64, key, day string) (int64, error) {
	ref := keyRef{projectID, key}
	if id, ok := cached(t.keys, t.d.keys, &t.d.mu, ref, day); ok {
		return id, nil
	}
	var id int64
	if err := t.tx.QueryRowContext(ctx, dictKeySQL, projectID, key, day).Scan(&id); err != nil {
		return 0, err
	}
	t.keys[ref] = seenID{id, day}
	return id, nil
}

// value is value's number within the key keyID, numbering it when new.
func (t *txDict) value(ctx context.Context, keyID int64, value, day string) (int64, error) {
	ref := valRef{keyID, value}
	if n, ok := cached(t.vals, t.d.vals, &t.d.mu, ref, day); ok {
		return n, nil
	}
	var n int64
	if err := t.tx.QueryRowContext(ctx, dictValSQL, keyID, value, day).Scan(&n); err != nil {
		return 0, err
	}
	t.vals[ref] = seenID{n, day}
	return n, nil
}

// commit merges what the committed transaction learned. Where both know
// an entry, the transaction's wins when its id differs (the upsert's
// answer is the current row; the shared one may predate a cleanup) or
// when it touched a later day.
func (t *txDict) commit() {
	t.d.mu.Lock()
	defer t.d.mu.Unlock()
	merge(t.d.strs, t.strs)
	merge(t.d.keys, t.keys)
	merge(t.d.vals, t.vals)
}

func merge[K comparable](shared, local map[K]seenID) {
	for k, v := range local {
		if cur, ok := shared[k]; !ok || cur.day < v.day || cur.id != v.id {
			shared[k] = v
		}
	}
}
