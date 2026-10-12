package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const tsFormat = "2006-01-02T15:04:05Z"

// WriteEvents stores a batch of raw rows, views, product events and
// measures alike, in the one raw table, encoded (insertEvents). INSERT OR
// IGNORE: with client-supplied UUIDv7 ids, a batch retried after a
// timeout that actually succeeded is a no-op —
// duplicates are detected on the whole primary key (family, project_id,
// day, id), so a retry is a no-op only because it repeats all four
// unchanged (the same UUID encodes to the same bytes). The one gap: a
// retry of an event whose ts was clamped, landing on the other side of
// midnight, clamps to a different day and is stored twice
// (deploy/UPGRADES.md).
//
// A row whose family is none of views, product and measures refuses the
// whole batch before anything is written: every raw_* view would miss it,
// so it would never be read, rolled up or pruned.
//
// A product or measures name with a rename rule is stored under its new
// name (D7 of the event rules spec), and its custom attribute keys go
// through the project's attribute rules (D9 of the project attributes
// spec), so rows queued before a rename or block are mapped too.
//
// value is NULL outside measures (a nil Value), and a SampleRate of 0
// means unset and is stored as 1.
//
// The attribute keys the inserted product and measure rows carried are
// recorded in usage_attributes in the same transaction, only those
// ingest has not written yet (receivedSeen); the daily pass counts them.
// The dictionary ids the batch resolved are cached (dict) only once it
// committed.
func (d *DB) WriteEvents(ctx context.Context, evs []store.Event) error {
	if len(evs) == 0 {
		return nil
	}
	for _, e := range evs {
		switch e.Family {
		case store.FamilyViews, store.FamilyProduct, store.FamilyMeasures:
		default:
			return fmt.Errorf("event %s: family %q is not %q, %q or %q",
				e.ID, e.Family, store.FamilyViews, store.FamilyProduct, store.FamilyMeasures)
		}
	}
	var fresh []receivedKey
	var gen uint64
	var td *txDict
	err := d.tx(ctx, func(tx *sql.Tx) error {
		td = d.dict.begin(tx)
		received, err := insertEvents(ctx, tx, td, evs)
		if err != nil {
			return err
		}
		fresh, gen = d.seen.unseen(received)
		return writeReceived(ctx, tx, fresh)
	})
	// Remembered only once committed: a rolled-back batch wrote nothing.
	if err == nil {
		d.seen.remember(fresh, gen)
		td.commit()
	}
	return err
}

// insertEventsSQL inserts one encoded row (events encoding spec D1), the
// columns in events' order.
const insertEventsSQL = `INSERT OR IGNORE INTO events
	(family, project_id, day, id, event_name, ts, received_at, kind,
	 actor_id, actor_kind, user_id, group_id, session_id,
	 host, path, referrer_source, utm_source, utm_medium, utm_campaign,
	 platform, os, os_version, os_name, browser, browser_version, browser_locale,
	 app_version, app_locale, device, device_model, display_width, display_height,
	 country, consent, attributes, value, measure, sample_rate)
	VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// insertEvents inserts evs, INSERT OR IGNORE, into the events table within
// tx and returns the attribute keys the inserted rows carried. The caller
// has checked the families, and records the keys (writeReceived) in the
// same transaction: WriteEvents and WriteSubmission share this body.
//
// Each row is stored encoded (events encoding spec D16): the UUIDs as
// bytes and the other ids tagged (encodeEventID, encodeID), ts and
// received_at as Unix seconds (an unset received_at as 0, which decodes to
// the empty string), every text column as its strings id, and the
// attributes as {"<catalog id>": <value number>}, each id resolved through
// td in this transaction. Product and measure rows also resolve the catalog row of
// each declarable reserved key they carry (usage_attributes records them),
// so those keep their rows as they did (spec D8).
//
// The event name goes through the event rules here, not only at ingest (which
// maps from the registry snapshot), so a row queued before a rename
// commits is stored under the new name: the rules are read in the flush's
// own transaction (batchRules), after or before the rename's, never
// between its rewrite and a snapshot reload, and a second serve process on
// the file sees them too. The name is renamed before it is interned.
//
// The custom attribute keys go through the attribute rules here for the
// same reason (spec D9), before the insert and before the received keys
// are recorded, so usage_attributes and the catalog see the mapped keys.
// The rules are read once per product or measures project in the batch
// (attributeRules); a project without any costs one map lookup per event.
//
// A row of a project that no longer exists (purged while its events were
// buffered) is skipped: its keys cannot get catalog rows, and no view
// scoped to a project would read it. The rest of the batch is stored.
func insertEvents(ctx context.Context, tx *sql.Tx, td *txDict, evs []store.Event) (receivedKeys, error) {
	stmt, err := tx.PrepareContext(ctx, insertEventsSQL)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	br, err := batchRules(ctx, tx, evs)
	if err != nil {
		return nil, err
	}
	received := receivedKeys{}
	var args [38]any
	for _, e := range evs {
		if !br.live[e.ProjectID] {
			continue
		}
		if rs, ok := br.attrs[e.ProjectID]; ok && e.Family != store.FamilyViews {
			// e is the loop's copy: the caller's event keeps its map, and
			// received.add below records the mapped keys.
			e.Attributes, _ = store.MapAttributes(e.Attributes, rs)
		}
		if to, ok := br.renames[eventRef{e.ProjectID, e.Family, e.EventName}]; ok {
			e.EventName = to
		}
		day := e.TS.UTC().Format("2006-01-02")
		if err := encodeEvent(ctx, td, e, day, &args); err != nil {
			return nil, fmt.Errorf("event %s: %w", e.ID, err)
		}
		res, err := stmt.ExecContext(ctx, args[:]...)
		if err != nil {
			return nil, fmt.Errorf("event %s: %w", e.ID, err)
		}
		// INSERT OR IGNORE: a duplicate id (a retried batch) inserts
		// nothing and so records nothing.
		if n, err := res.RowsAffected(); err == nil && n == 1 {
			received.add(e, day)
		}
	}
	return received, nil
}

// encodeEvent fills args with e's encoded columns, in insertEventsSQL's
// order, resolving its texts, keys and values through td for day.
func encodeEvent(ctx context.Context, td *txDict, e store.Event, day string, args *[38]any) error {
	texts := [...]struct {
		i int
		v string
	}{
		{4, e.EventName}, {7, e.Kind}, {9, e.ActorKind},
		{13, e.Host}, {14, e.Path}, {15, e.ReferrerSource},
		{16, e.UTMSource}, {17, e.UTMMedium}, {18, e.UTMCampaign},
		{19, e.Platform}, {20, e.OS}, {21, e.OSVersion}, {22, e.OSName},
		{23, e.Browser}, {24, e.BrowserVersion}, {25, e.BrowserLocale},
		{26, e.AppVersion}, {27, e.AppLocale}, {28, e.Device}, {29, e.DeviceModel},
		{32, e.Country}, {36, e.Measure},
	}
	for _, t := range texts {
		id, err := td.str(ctx, t.v, day)
		if err != nil {
			return fmt.Errorf("strings: %w", err)
		}
		args[t.i] = id
	}
	attrs := make(map[string]int64, len(e.Attributes))
	for k, v := range e.Attributes {
		keyID, n, err := td.attribute(ctx, e.ProjectID, k, v, day)
		if err != nil {
			return fmt.Errorf("attribute %q: %w", k, err)
		}
		attrs[strconv.FormatInt(keyID, 10)] = n
	}
	if e.Family != store.FamilyViews {
		for _, kv := range declarableValues(e) {
			if kv.value == "" {
				continue
			}
			if _, err := td.key(ctx, e.ProjectID, kv.key, day); err != nil {
				return fmt.Errorf("attribute key %q: %w", kv.key, err)
			}
		}
	}
	blob, err := json.Marshal(attrs)
	if err != nil {
		return fmt.Errorf("attributes: %w", err)
	}
	var received int64
	if !e.ReceivedAt.IsZero() {
		received = unixSeconds(e.ReceivedAt)
	}
	rate := e.SampleRate
	if rate == 0 {
		rate = 1
	}
	args[0], args[1], args[2], args[3] = familyCode(e.Family), e.ProjectID, day, encodeEventID(e.ID)
	args[5], args[6] = unixSeconds(e.TS), received
	args[8], args[10], args[11], args[12] = encodeID(e.ActorID), encodeID(e.UserID), encodeID(e.GroupID), encodeID(e.SessionID)
	args[30], args[31], args[33] = e.DisplayWidth, e.DisplayHeight, e.Consent
	args[34], args[35], args[37] = string(blob), e.Value, rate
	return nil
}

// eventRef names an event of a project's family, for the rename rules.
type eventRef struct {
	project int64
	family  store.Family
	name    string
}

// batchRuleSet is what a batch's projects need in the flush's transaction:
// which still exist, their event renames, and their attribute rules.
type batchRuleSet struct {
	live    map[int64]bool
	renames map[eventRef]string
	attrs   map[int64]store.AttributeRuleSet
}

// batchRules reads, for each project of the batch, whether it exists and
// its event rename rules (a primary-key range per project), and the
// attribute rules of its product and measures projects (attributeRules).
func batchRules(ctx context.Context, tx *sql.Tx, evs []store.Event) (batchRuleSet, error) {
	br := batchRuleSet{live: map[int64]bool{}}
	for i := range evs {
		p := evs[i].ProjectID
		if _, done := br.live[p]; done {
			continue
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id=?`, p).Scan(&n); err != nil {
			return br, fmt.Errorf("projects: %w", err)
		}
		br.live[p] = n > 0
		if n == 0 {
			continue
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT family, name, to_name FROM rules WHERE project_id=? AND kind='event' AND action='rename'`, p)
		if err != nil {
			return br, fmt.Errorf("event rules: %w", err)
		}
		for rows.Next() {
			var family, name, to string
			if err := rows.Scan(&family, &name, &to); err != nil {
				rows.Close()
				return br, fmt.Errorf("event rules: %w", err)
			}
			if br.renames == nil {
				br.renames = map[eventRef]string{}
			}
			br.renames[eventRef{p, store.Family(family), name}] = to
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return br, fmt.Errorf("event rules: %w", err)
		}
	}
	var err error
	br.attrs, err = attributeRules(ctx, tx, evs)
	return br, err
}

// UpsertIdentities records display names, latest write wins.
// identities.last_seen_day is maintained by the daily pass, not here.
func (d *DB) UpsertIdentities(ctx context.Context, ids []store.Identity) error {
	if len(ids) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO identities
			(project_id, kind, id, name, updated_at)
			VALUES (?,?,?,?,datetime('now'))
			ON CONFLICT(project_id, kind, id) DO UPDATE SET
			  name=excluded.name, updated_at=excluded.updated_at`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, i := range ids {
			if i.ID == "" || i.Name == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx, i.ProjectID, i.Kind, i.ID, i.Name); err != nil {
				return fmt.Errorf("identity %s/%s: %w", i.Kind, i.ID, err)
			}
		}
		return nil
	})
}

func (d *DB) ProjectIDs(ctx context.Context) ([]int64, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *DB) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := d.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (d *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// tx runs fn in a transaction with commit/rollback handling; shared by all
// sqlite write paths.
func (d *DB) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
