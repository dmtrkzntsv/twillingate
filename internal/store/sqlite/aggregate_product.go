package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// attrPath builds a JSON path literal for a config-supplied attribute key.
// Keys come from the operator's config, not from clients, but are still
// quoted defensively (spec §8.1 sanitization rules apply to view building;
// here the value is parameter-adjacent SQL, so escape quotes).
func attrPath(key string) string {
	return `$."` + strings.ReplaceAll(key, `"`, `\"`) + `"`
}

// defaultAttrsTopN is the fallback for a non-positive topN. Not reachable
// today (jobs.Runner always sets it from config.Config.ProductAttributesTopN,
// which parse() defaults to 50), but guarded anyway: rollupAttr's
// `rn <= topN` filter treats topN<=0 as "keep nothing", which would
// silently collapse every distinct value into "(other)" rather than erroring
// -- a permanent, undetected loss of attribute breakdowns.
const defaultAttrsTopN = 50

func (d *DB) AggregateProductDay(ctx context.Context, projectID int64, day civil.Date, attrs []string, topN int) error {
	if topN <= 0 {
		topN = defaultAttrsTopN
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM raw_product WHERE project_id=? AND day=?`,
			projectID, day.String()).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if err := d.rollupProduct(ctx, tx, projectID, day, attrs, topN); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM events WHERE family='product' AND project_id=? AND day=?`, projectID, day.String())
		return err
	})
}

func (d *DB) rollupProduct(ctx context.Context, tx *sql.Tx, projectID int64, day civil.Date, attrs []string, topN int) error {
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agg_product_daily
		(project_id, day, event_name, count, unique_users)
		SELECT project_id, ?, event_name, COUNT(*), COUNT(DISTINCT actor_id)
		FROM raw_product WHERE project_id=? AND day=?
		GROUP BY event_name`, day.String(), projectID, day.String()); err != nil {
		return fmt.Errorf("agg_product_daily: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agg_product_totals
		(project_id, day, total_events, active_users)
		SELECT project_id, ?, COUNT(*), COUNT(DISTINCT actor_id)
		FROM raw_product WHERE project_id=? AND day=?
		GROUP BY project_id`, day.String(), projectID, day.String()); err != nil {
		return fmt.Errorf("agg_product_totals: %w", err)
	}
	// Attribute breakdowns: resolve per event name present that day.
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT event_name FROM raw_product
		WHERE project_id=? AND day=?`, projectID, day.String())
	if err != nil {
		return err
	}
	var events []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, event := range events {
		for _, key := range attrs {
			expr, present := `json_extract(attributes, :path)`, `json_extract(attributes, :path) IS NOT NULL`
			if strings.HasPrefix(key, "$") {
				// A declared reserved key reads its column; the blob never
				// holds $ keys. One the store does not map (a declaration
				// manage now refuses) has nothing to break down.
				col, ok := store.DeclarableAttributes[key]
				if !ok {
					continue
				}
				expr, present = col, col+` <> ''`
			}
			named := []any{
				sql.Named("p", projectID), sql.Named("day", day.String()),
				sql.Named("event", event), sql.Named("key", key),
				sql.Named("path", attrPath(key)), sql.Named("n", topN),
			}
			if err := d.rollupAttrValue(ctx, tx, expr, present, named); err != nil {
				return fmt.Errorf("attr %s/%s: %w", event, key, err)
			}
		}
	}
	// System dimensions: platform, os, app_version, app_locale, kind,
	// browser, device and browser_locale (store.SystemAttributes) are
	// typed columns written on every event, not declared custom keys, so
	// they roll up unconditionally under $-prefixed attr_keys. $ is a safe
	// namespace: resolveAttributes routes every $-prefixed input to a
	// typed field and drops unrecognised ones, so a custom key can never
	// collide with a system one. The columns are NOT NULL DEFAULT '', so
	// empty string (not NULL) means absent.
	for _, dim := range store.SystemAttributes {
		for _, event := range events {
			named := []any{
				sql.Named("p", projectID), sql.Named("day", day.String()),
				sql.Named("event", event), sql.Named("key", dim.Key),
				sql.Named("n", topN),
			}
			expr := dim.Column
			present := dim.Column + ` <> ''`
			if err := d.rollupAttrValue(ctx, tx, expr, present, named); err != nil {
				return fmt.Errorf("system dim %s/%s: %w", event, dim.Key, err)
			}
		}
	}
	return nil
}

// rollupAttrValue writes the ranked top-N breakdown plus the "(other)"
// tail for one (event, attr_key) pair into agg_product_attrs. expr is the
// SQL expression yielding the value to group by (a json_extract path for
// declared attributes, a bare column for system dimensions); present is
// the filter identifying rows where that value counts as set (declared
// attributes use IS NOT NULL on the JSON extract, system columns use a
// not-empty-string check since they're NOT NULL DEFAULT empty-string).
// named must supply :p, :day, :event, :key, :n, and whatever expr/present
// reference (:path for the JSON case).
//
// Both statements also write unique_groups, the distinct non-empty
// group_id among the same rows, so a day rolled up after 016 carries an
// integer -- 0 when no row had a group -- and only pre-016 history is
// NULL.
func (d *DB) rollupAttrValue(ctx context.Context, tx *sql.Tx, expr, present string, named []any) error {
	// Top-N values by count. Ranking is by count alone; groups ride along.
	if _, err := tx.ExecContext(ctx, `
		WITH counted AS (
		  SELECT `+expr+` AS v, COUNT(*) AS c, COUNT(DISTINCT actor_id) AS u,
		         COUNT(DISTINCT NULLIF(group_id,'')) AS g
		  FROM raw_product
		  WHERE project_id=:p AND day=:day AND event_name=:event
		    AND `+present+`
		  GROUP BY v
		),
		ranked AS (SELECT v, c, u, g, ROW_NUMBER() OVER (ORDER BY c DESC, v) AS rn FROM counted)
		INSERT OR REPLACE INTO agg_product_attrs
		  (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
		SELECT :p, :day, :event, :key, v, c, u, g FROM ranked WHERE rn <= :n`, named...); err != nil {
		return err
	}
	// Tail -> "(other)" with correct distinct users and groups, computed
	// from raw rather than summed across the tail's values.
	_, err := tx.ExecContext(ctx, `
		WITH counted AS (
		  SELECT `+expr+` AS v, COUNT(*) AS c
		  FROM raw_product
		  WHERE project_id=:p AND day=:day AND event_name=:event
		    AND `+present+`
		  GROUP BY v
		),
		ranked AS (SELECT v, ROW_NUMBER() OVER (ORDER BY c DESC, v) AS rn FROM counted),
		keep AS (SELECT v FROM ranked WHERE rn <= :n)
		INSERT OR REPLACE INTO agg_product_attrs
		  (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
		SELECT :p, :day, :event, :key, '(other)', COUNT(*), COUNT(DISTINCT actor_id),
		       COUNT(DISTINCT NULLIF(group_id,''))
		FROM raw_product
		WHERE project_id=:p AND day=:day AND event_name=:event
		  AND `+present+`
		  AND `+expr+` NOT IN (SELECT v FROM keep)
		HAVING COUNT(*) > 0`, named...)
	return err
}
