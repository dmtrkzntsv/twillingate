package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// AggregateMeasureDay rolls one raw day of measures into histograms, then
// deletes the day's raw rows, in one transaction. Dimensions and the top-N
// cap follow the product rules: the system attributes always, plus the
// project's declared keys. It reads the rows on their stored ids and
// writes text (events encoding spec D14); the rows written are exactly
// what the live halves of v_measures_daily and v_measures_attrs
// (041_events_encoding.sql) compute over the same raw rows.
func (d *DB) AggregateMeasureDay(ctx context.Context, projectID int64, day civil.Date, attrs []string, topN int) error {
	keep := capRows(topN, defaultAttrsTopN)
	return d.tx(ctx, func(tx *sql.Tx) error {
		// Blocked names (event rules D10) leave with the day before it is
		// rolled up: the same transaction deletes every raw row of the day
		// below, so this deletes nothing that would have stayed. The rows
		// hold the names' strings ids (events encoding spec D2).
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE family=3 AND project_id=? AND day=?
			AND event_name IN (SELECT s.id FROM strings s JOIN rules r ON r.name = s.value
			  WHERE r.project_id=? AND r.kind='event' AND r.family='measures' AND r.action='block')`,
			projectID, day.String(), projectID); err != nil {
			return err
		}
		var raw bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM events WHERE family = 3 AND project_id = ? AND day = ?)`,
			projectID, day.String()).Scan(&raw); err != nil {
			return err
		}
		if !raw {
			return nil
		}
		if err := d.rollupMeasures(ctx, tx, projectID, day, attrs, keep); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM events WHERE family=3 AND project_id=? AND day=?`, projectID, day.String())
		return err
	})
}

// measureMetric is one metric of a raw day: its event name and measure.
type measureMetric struct{ event, measure storedName }

func (d *DB) rollupMeasures(ctx context.Context, tx *sql.Tx, projectID int64, day civil.Date, attrs []string, topN int64) error {
	// A row per bucket, so many grouped rows: the names decode through a
	// join, as in v_measures_daily.
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agg_measures_daily
		(project_id, day, event_name, measure, bucket, samples, weight, sum)
		SELECT ?1, ?2, COALESCE(se.value, ''), COALESCE(sm.value, ''), g.bucket, g.samples, g.weight, g.sum
		FROM (
		  SELECT event_name, measure, bucket,
		         COUNT(*) AS samples, SUM(1.0 / sample_rate) AS weight, SUM(value / sample_rate) AS sum
		  FROM events WHERE family = 3 AND project_id = ?1 AND day = ?2
		  GROUP BY event_name, measure, bucket
		) g
		LEFT JOIN strings se ON se.id = g.event_name
		LEFT JOIN strings sm ON sm.id = g.measure`, projectID, day.String()); err != nil {
		return fmt.Errorf("agg_measures_daily: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT event_name, COALESCE((SELECT value FROM strings WHERE id = event_name), ''),
		       measure, COALESCE((SELECT value FROM strings WHERE id = measure), '')
		FROM events WHERE family = 3 AND project_id = ? AND day = ? GROUP BY event_name, measure`,
		projectID, day.String())
	if err != nil {
		return err
	}
	var metrics []measureMetric
	for rows.Next() {
		var m measureMetric
		if err := rows.Scan(&m.event.id, &m.event.text, &m.measure.id, &m.measure.text); err != nil {
			rows.Close()
			return err
		}
		metrics = append(metrics, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Declared keys, each once (declaredKeys): the tail statement adds to
	// what is already there, so a key listed twice would count its tail
	// twice.
	keys, err := declaredKeys(ctx, tx, projectID, attrs)
	if err != nil {
		return err
	}
	for _, m := range metrics {
		metric := []any{
			sql.Named("p", projectID), sql.Named("day", day.String()), sql.Named("n", topN),
			sql.Named("event", m.event.id), sql.Named("name", m.event.text),
			sql.Named("measure", m.measure.id), sql.Named("mname", m.measure.text),
		}
		for _, k := range keys {
			named := append(append([]any{}, metric...), k.named()...)
			if err := rollupMeasureAttr(ctx, tx, k.src, named); err != nil {
				return fmt.Errorf("attr %s/%s/%s: %w", m.event.text, m.measure.text, k.key, err)
			}
		}
		// System dimensions roll up for every project (store.SystemAttributes).
		for _, dim := range store.SystemAttributes {
			named := append(append([]any{}, metric...), sql.Named("key", dim.Key))
			if err := rollupMeasureAttr(ctx, tx, columnSource(dim.Column), named); err != nil {
				return fmt.Errorf("system dim %s/%s/%s: %w", m.event.text, m.measure.text, dim.Key, err)
			}
		}
	}
	return nil
}

// rollupMeasureAttr writes the histogram of one metric broken down by one
// key into agg_measures_attrs: a row per (kept value, bucket) for the top n
// values by samples (ties by value text ascending, as the live half
// ranks), and a row per bucket for the rest under '(other)'. Histogram
// rows add up, so the tail is a plain sum. src reads the key's stored
// value from a raw row and decodes a grouped one: the values group on
// their stored ids, and only the kept ones decode. named supplies :p,
// :day, :event and :measure (strings ids), :name and :mname (their text),
// :key, :n and whatever src references (:path and :kid).
func rollupMeasureAttr(ctx context.Context, tx *sql.Tx, src attrSource, named []any) error {
	const scope = `project_id = :p AND day = :day AND event_name = :event AND measure = :measure`
	keep := `WITH counted AS (
		  SELECT v, COUNT(*) AS c
		  FROM (SELECT ` + src.value + ` AS v FROM events WHERE family = 3 AND ` + scope + `)
		  WHERE v IS NOT NULL
		  GROUP BY v),
		ranked AS (SELECT v, ROW_NUMBER() OVER (ORDER BY c DESC, ` + src.text + `) AS rn FROM counted),
		keep AS (SELECT v FROM ranked WHERE rn <= :n)`
	if _, err := tx.ExecContext(ctx, keep+`
		INSERT OR REPLACE INTO agg_measures_attrs
		  (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum)
		SELECT :p, :day, :name, :mname, :key, `+src.text+`, bucket,
		       COUNT(*), SUM(1.0 / sample_rate), SUM(value / sample_rate)
		FROM (SELECT `+src.value+` AS v, bucket, sample_rate, value FROM events WHERE family = 3 AND `+scope+`)
		WHERE v IN (SELECT v FROM keep)
		GROUP BY v, bucket`, named...); err != nil {
		return err
	}
	// The tail adds to an existing row rather than replacing it: a kept
	// value that is literally '(other)' was just written above, and the
	// live half's final GROUP BY merges it with the tail.
	_, err := tx.ExecContext(ctx, keep+`
		INSERT INTO agg_measures_attrs
		  (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum)
		SELECT :p, :day, :name, :mname, :key, '(other)', bucket,
		       COUNT(*), SUM(1.0 / sample_rate), SUM(value / sample_rate)
		FROM (SELECT `+src.value+` AS v, bucket, sample_rate, value FROM events WHERE family = 3 AND `+scope+`)
		WHERE v IS NOT NULL AND v NOT IN (SELECT v FROM keep)
		GROUP BY bucket
		ON CONFLICT (project_id, day, event_name, measure, attr_key, attr_value, bucket) DO UPDATE SET
		  samples = samples + excluded.samples,
		  weight = weight + excluded.weight,
		  sum = sum + excluded.sum`, named...)
	return err
}
