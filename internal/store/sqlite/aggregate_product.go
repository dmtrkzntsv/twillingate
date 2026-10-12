package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// attrSource is how a rollup reads one attribute key from a raw row on its
// stored ids (events encoding spec D14): value yields the row's stored
// value, NULL where the row does not carry the key, and text decodes a
// grouped stored value, named v, into what the aggregate writes.
type attrSource struct{ value, text string }

// columnSource reads a reserved key from its strings column, where 0, the
// empty string, is unset.
func columnSource(col string) attrSource {
	return attrSource{value: `NULLIF(` + col + `, 0)`, text: `(SELECT value FROM strings WHERE id = v)`}
}

// declaredSource reads a declared custom key by its catalog id: :path is
// '$."<id>"' into the encoded attributes, whose value is the number of
// the value (attribute_values) under :kid.
var declaredSource = attrSource{value: `json_extract(attributes, :path)`,
	text: `(SELECT value FROM attribute_values WHERE key_id = :kid AND n = v)`}

// attrKey is one declared key a rollup breaks down, and how to read it.
type attrKey struct {
	key string
	src attrSource
	kid int64
}

// declaredKeys resolves the keys handed to a rollup, each once: the tail
// statements add to (measures) or replace (product) what is already there,
// and the live halves read the declaration through SELECT DISTINCT. A
// declared reserved key reads its column; one the store does not map (a
// declaration manage now refuses) has nothing to break down, nor has a
// custom key with no catalog row in the project, since no raw row can
// carry it (spec D8).
func declaredKeys(ctx context.Context, tx *sql.Tx, projectID int64, attrs []string) ([]attrKey, error) {
	var out []attrKey
	seen := map[string]bool{}
	for _, key := range attrs {
		if seen[key] {
			continue
		}
		seen[key] = true
		if strings.HasPrefix(key, "$") {
			if col, ok := store.DeclarableAttributes[key]; ok {
				out = append(out, attrKey{key: key, src: columnSource(col)})
			}
			continue
		}
		var kid int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM project_attributes WHERE project_id = ? AND key = ?`,
			projectID, key).Scan(&kid)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("attribute %s: %w", key, err)
		}
		out = append(out, attrKey{key: key, src: declaredSource, kid: kid})
	}
	return out, nil
}

// named is the key's own parameters: :key, and :kid and :path for a
// declared custom key.
func (k attrKey) named() []any {
	return []any{sql.Named("key", k.key), sql.Named("kid", k.kid), sql.Named("path", fmt.Sprintf(`$."%d"`, k.kid))}
}

// storedName is a raw day's event name (or measure): its strings id, and
// its text.
type storedName struct {
	id   int64
	text string
}

// defaultAttrsTopN is ATTRIBUTE_VALUES_TOP_N's default, and the fallback
// for a negative topN. Config refuses one, but the guard stays: the
// `rn <= :n` filter would read it as "keep nothing" and silently collapse
// every distinct value into "(other)" -- a permanent, undetected loss of
// attribute breakdowns. 0 keeps every value (capRows).
const defaultAttrsTopN = 100

// AggregateProductDay rolls one raw day of product events into
// agg_product_daily, agg_product_totals and agg_product_attrs, then
// deletes the day's raw rows, in one transaction. It reads the rows on
// their stored ids and writes text (events encoding spec D14); the rows
// written are what the live halves of v_product_daily, v_product_totals and
// v_product_attrs compute over the same raw rows.
func (d *DB) AggregateProductDay(ctx context.Context, projectID int64, day civil.Date, attrs []string, topN int) error {
	keep := capRows(topN, defaultAttrsTopN)
	return d.tx(ctx, func(tx *sql.Tx) error {
		// Blocked names (event rules D10) leave with the day before it is
		// rolled up: the same transaction deletes every raw row of the day
		// below, so this deletes nothing that would have stayed. The rows
		// hold the names' strings ids (events encoding spec D2).
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE family=2 AND project_id=? AND day=?
			AND event_name IN (SELECT s.id FROM strings s JOIN rules r ON r.name = s.value
			  WHERE r.project_id=? AND r.kind='event' AND r.family='product' AND r.action='block')`,
			projectID, day.String(), projectID); err != nil {
			return err
		}
		var raw bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM events WHERE family = 2 AND project_id = ? AND day = ?)`,
			projectID, day.String()).Scan(&raw); err != nil {
			return err
		}
		if !raw {
			return nil
		}
		if err := d.rollupProduct(ctx, tx, projectID, day, attrs, keep); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM events WHERE family=2 AND project_id=? AND day=?`, projectID, day.String())
		return err
	})
}

func (d *DB) rollupProduct(ctx context.Context, tx *sql.Tx, projectID int64, day civil.Date, attrs []string, topN int64) error {
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agg_product_daily
		(project_id, day, event_name, count, unique_users)
		SELECT ?1, ?2, COALESCE((SELECT value FROM strings WHERE id = event_name), ''), COUNT(*), COUNT(DISTINCT actor_id)
		FROM events WHERE family = 2 AND project_id = ?1 AND day = ?2
		GROUP BY event_name`, projectID, day.String()); err != nil {
		return fmt.Errorf("agg_product_daily: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agg_product_totals
		(project_id, day, total_events, active_users)
		SELECT ?1, ?2, COUNT(*), COUNT(DISTINCT actor_id)
		FROM events WHERE family = 2 AND project_id = ?1 AND day = ?2
		GROUP BY project_id`, projectID, day.String()); err != nil {
		return fmt.Errorf("agg_product_totals: %w", err)
	}
	// Attribute breakdowns: resolve per event name present that day.
	rows, err := tx.QueryContext(ctx, `SELECT event_name, COALESCE((SELECT value FROM strings WHERE id = event_name), '')
		FROM events WHERE family = 2 AND project_id = ? AND day = ? GROUP BY event_name`, projectID, day.String())
	if err != nil {
		return err
	}
	var events []storedName
	for rows.Next() {
		var e storedName
		if err := rows.Scan(&e.id, &e.text); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	keys, err := declaredKeys(ctx, tx, projectID, attrs)
	if err != nil {
		return err
	}
	for _, event := range events {
		for _, k := range keys {
			named := append([]any{
				sql.Named("p", projectID), sql.Named("day", day.String()),
				sql.Named("event", event.id), sql.Named("name", event.text), sql.Named("n", topN),
			}, k.named()...)
			if err := d.rollupAttrValue(ctx, tx, k.src, named); err != nil {
				return fmt.Errorf("attr %s/%s: %w", event.text, k.key, err)
			}
		}
	}
	// System dimensions: platform, os, app_version, app_locale, kind,
	// browser, device, browser_locale and country (store.SystemAttributes)
	// are typed columns written on every event, not declared custom keys, so
	// they roll up unconditionally under $-prefixed attr_keys. $ is a safe
	// namespace: resolveAttributes routes every $-prefixed input to a
	// typed field and drops unrecognised ones, so a custom key can never
	// collide with a system one. The columns hold strings ids, 0 for the
	// empty string, which means absent.
	for _, dim := range store.SystemAttributes {
		for _, event := range events {
			named := []any{
				sql.Named("p", projectID), sql.Named("day", day.String()),
				sql.Named("event", event.id), sql.Named("name", event.text),
				sql.Named("key", dim.Key), sql.Named("n", topN),
			}
			if err := d.rollupAttrValue(ctx, tx, columnSource(dim.Column), named); err != nil {
				return fmt.Errorf("system dim %s/%s: %w", event.text, dim.Key, err)
			}
		}
	}
	return nil
}

// rollupAttrValue writes the ranked top-N breakdown plus the "(other)"
// tail for one (event, attr_key) pair into agg_product_attrs. src reads
// the key's stored value from a raw row and decodes a grouped one: the
// values group and count on their stored ids, rank by count with ties by
// their text, as the live half ranks, and only the written ones decode.
// named must supply :p, :day, :event (the name's strings id), :name (its
// text), :key, :n, and whatever src references (:path and :kid for a
// declared custom key).
//
// Both statements also write unique_groups, the distinct non-empty
// group_id among the same rows, so a day rolled up after 016 carries an
// integer -- 0 when no row had a group -- and only pre-016 history is
// NULL. The actor and group ids count as stored: the encoding is
// one-to-one, and a tagged blob never equals a text.
func (d *DB) rollupAttrValue(ctx context.Context, tx *sql.Tx, src attrSource, named []any) error {
	// Top-N values by count. Ranking is by count alone; groups ride along.
	if _, err := tx.ExecContext(ctx, `
		WITH counted AS (
		  SELECT v, COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
		         COUNT(DISTINCT NULLIF(group_id, '')) AS g
		  FROM (SELECT `+src.value+` AS v, actor_id, group_id FROM events
		        WHERE family = 2 AND project_id = :p AND day = :day AND event_name = :event)
		  WHERE v IS NOT NULL
		  GROUP BY v
		),
		ranked AS (SELECT t, c, u, g, ROW_NUMBER() OVER (ORDER BY c DESC, t) AS rn
		           FROM (SELECT `+src.text+` AS t, c, u, g FROM counted))
		INSERT OR REPLACE INTO agg_product_attrs
		  (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
		SELECT :p, :day, :name, :key, t, c, u, g FROM ranked WHERE rn <= :n`, named...); err != nil {
		return err
	}
	// Tail -> "(other)" with correct distinct users and groups, computed
	// from raw rather than summed across the tail's values.
	_, err := tx.ExecContext(ctx, `
		WITH counted AS (
		  SELECT v, COUNT(*) AS c
		  FROM (SELECT `+src.value+` AS v FROM events
		        WHERE family = 2 AND project_id = :p AND day = :day AND event_name = :event)
		  WHERE v IS NOT NULL
		  GROUP BY v
		),
		ranked AS (SELECT v, ROW_NUMBER() OVER (ORDER BY c DESC, `+src.text+`) AS rn FROM counted),
		keep AS (SELECT v FROM ranked WHERE rn <= :n)
		INSERT OR REPLACE INTO agg_product_attrs
		  (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
		SELECT :p, :day, :name, :key, '(other)', COUNT(*), COUNT(DISTINCT actor_id),
		       COUNT(DISTINCT NULLIF(group_id, ''))
		FROM (SELECT `+src.value+` AS v, actor_id, group_id FROM events
		      WHERE family = 2 AND project_id = :p AND day = :day AND event_name = :event)
		WHERE v IS NOT NULL AND v NOT IN (SELECT v FROM keep)
		HAVING COUNT(*) > 0`, named...)
	return err
}
