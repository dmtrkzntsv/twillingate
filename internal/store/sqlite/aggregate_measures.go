package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// AggregateMeasureDay rolls one raw day of measures into histograms, then
// deletes the day's raw rows, in one transaction. Dimensions and the top-N
// cap follow the product rules: the system attributes always, plus the
// project's declared keys. The rows written are exactly what the live
// halves of v_measures_daily (024_measures.sql) and v_measures_attrs
// (025_live_halves.sql) compute over the same raw rows.
func (d *DB) AggregateMeasureDay(ctx context.Context, projectID int64, day civil.Date, attrs []string, topN int) error {
	topN = capRows(topN, defaultAttrsTopN)
	return d.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM raw_measures WHERE project_id=? AND day=?`,
			projectID, day.String()).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if err := d.rollupMeasures(ctx, tx, projectID, day, attrs, topN); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM events WHERE family='measures' AND project_id=? AND day=?`, projectID, day.String())
		return err
	})
}

type measureMetric struct{ event, measure string }

func (d *DB) rollupMeasures(ctx context.Context, tx *sql.Tx, projectID int64, day civil.Date, attrs []string, topN int) error {
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agg_measures_daily
		(project_id, day, event_name, measure, bucket, samples, weight, sum)
		SELECT project_id, ?, event_name, measure, bucket,
		       COUNT(*), SUM(1.0 / sample_rate), SUM(value / sample_rate)
		FROM raw_measures WHERE project_id=? AND day=?
		GROUP BY event_name, measure, bucket`, day.String(), projectID, day.String()); err != nil {
		return fmt.Errorf("agg_measures_daily: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT event_name, measure FROM raw_measures
		WHERE project_id=? AND day=?`, projectID, day.String())
	if err != nil {
		return err
	}
	var metrics []measureMetric
	for rows.Next() {
		var m measureMetric
		if err := rows.Scan(&m.event, &m.measure); err != nil {
			rows.Close()
			return err
		}
		metrics = append(metrics, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Declared keys, each once: the tail statement adds to what is
	// already there, so a key listed twice would count its tail twice.
	// The live half reads the declaration through SELECT DISTINCT too.
	var keys []string
	seen := map[string]bool{}
	for _, key := range attrs {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	for _, m := range metrics {
		for _, key := range keys {
			expr, present := `json_extract(attributes, :path)`, `json_extract(attributes, :path) IS NOT NULL`
			if strings.HasPrefix(key, "$") {
				// As in rollupProduct: a declared reserved key reads its
				// column, and one the store does not map has nothing to
				// break down (the live half's CASE yields NULL for it).
				col, ok := store.DeclarableAttributes[key]
				if !ok {
					continue
				}
				expr, present = col, col+` <> ''`
			}
			named := []any{
				sql.Named("p", projectID), sql.Named("day", day.String()),
				sql.Named("event", m.event), sql.Named("measure", m.measure),
				sql.Named("key", key), sql.Named("path", attrPath(key)), sql.Named("n", topN),
			}
			if err := rollupMeasureAttr(ctx, tx, expr, present, named); err != nil {
				return fmt.Errorf("attr %s/%s/%s: %w", m.event, m.measure, key, err)
			}
		}
		// System dimensions roll up for every project (store.SystemAttributes).
		for _, dim := range store.SystemAttributes {
			named := []any{
				sql.Named("p", projectID), sql.Named("day", day.String()),
				sql.Named("event", m.event), sql.Named("measure", m.measure),
				sql.Named("key", dim.Key), sql.Named("n", topN),
			}
			if err := rollupMeasureAttr(ctx, tx, dim.Column, dim.Column+` <> ''`, named); err != nil {
				return fmt.Errorf("system dim %s/%s/%s: %w", m.event, m.measure, dim.Key, err)
			}
		}
	}
	return nil
}

// rollupMeasureAttr writes the histogram of one metric broken down by one
// key into agg_measures_attrs: a row per (kept value, bucket) for the top n
// values by samples (ties by value ascending, as the live half ranks), and
// a row per bucket for the rest under '(other)'. Histogram rows add up, so
// the tail is a plain sum. expr yields the value, present filters the rows
// where it is set; named supplies :p, :day, :event, :measure, :key, :n and
// whatever expr references (:path).
func rollupMeasureAttr(ctx context.Context, tx *sql.Tx, expr, present string, named []any) error {
	const scope = `project_id=:p AND day=:day AND event_name=:event AND measure=:measure`
	keep := `WITH counted AS (
		  SELECT ` + expr + ` AS v, COUNT(*) AS c FROM raw_measures
		  WHERE ` + scope + ` AND ` + present + `
		  GROUP BY v),
		ranked AS (SELECT v, ROW_NUMBER() OVER (ORDER BY c DESC, v) AS rn FROM counted),
		keep AS (SELECT v FROM ranked WHERE rn <= :n)`
	if _, err := tx.ExecContext(ctx, keep+`
		INSERT OR REPLACE INTO agg_measures_attrs
		  (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum)
		SELECT :p, :day, :event, :measure, :key, `+expr+`, bucket,
		       COUNT(*), SUM(1.0 / sample_rate), SUM(value / sample_rate)
		FROM raw_measures
		WHERE `+scope+` AND `+present+` AND `+expr+` IN (SELECT v FROM keep)
		GROUP BY `+expr+`, bucket`, named...); err != nil {
		return err
	}
	// The tail adds to an existing row rather than replacing it: a kept
	// value that is literally '(other)' was just written above, and the
	// live half's final GROUP BY merges it with the tail.
	_, err := tx.ExecContext(ctx, keep+`
		INSERT INTO agg_measures_attrs
		  (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum)
		SELECT :p, :day, :event, :measure, :key, '(other)', bucket,
		       COUNT(*), SUM(1.0 / sample_rate), SUM(value / sample_rate)
		FROM raw_measures
		WHERE `+scope+` AND `+present+` AND `+expr+` NOT IN (SELECT v FROM keep)
		GROUP BY bucket
		ON CONFLICT (project_id, day, event_name, measure, attr_key, attr_value, bucket) DO UPDATE SET
		  samples = samples + excluded.samples,
		  weight = weight + excluded.weight,
		  sum = sum + excluded.sum`, named...)
	return err
}
