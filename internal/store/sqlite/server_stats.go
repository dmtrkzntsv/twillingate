package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// MeasureServerStats writes every server_stats measurement in one
// transaction, so a reader sees the old values or the new, never a mix:
//
//   - sizes, for now's UTC day: each project's estimated disk use, and the
//     database file's (project 0). A second run the same day (the pass
//     also runs at start) replaces the day's rows; earlier days stay.
//   - counts, for every day before now's: each project's views, product
//     events and measure samples per day, from the same rows the views
//     read (aggregates for days rolled up, raw rows for the rest). A raw
//     day can still gain a late event, so every day the raw rows or the
//     aggregates still hold is counted again; a day both have dropped keeps
//     the count it had, which is the point: the history outlives the
//     aggregates' retention.
//
// dbstat reads every page of the file, so this runs in the daily pass,
// never in a request.
func (d *DB) MeasureServerStats(ctx context.Context, now time.Time) error {
	day := civil.DateOf(now).String()
	return d.tx(ctx, func(tx *sql.Tx) error {
		if err := measureSizes(ctx, tx, day); err != nil {
			return err
		}
		return countDays(ctx, tx, day)
	})
}

// measureSizes writes day's size rows: each project's raw and aggregate
// bytes, and the database file's. Every data table's bytes (indexes
// included) are split by each project's share of the table's rows; a
// project with no rows has no size that day, so only projects holding rows
// get a pair.
func measureSizes(ctx context.Context, tx *sql.Tx, day string) error {
	bytes, err := tableBytes(ctx, tx)
	if err != nil {
		return fmt.Errorf("table sizes: %w", err)
	}
	tables, err := dataTables(ctx, tx)
	if err != nil {
		return fmt.Errorf("data tables: %w", err)
	}
	raw, aggregate := map[int64]int64{}, map[int64]int64{}
	hasRows := map[int64]bool{}
	for _, table := range tables {
		// The raw rows are read through the family views (never the
		// events table itself, see TestRawTableIsReadOnlyThroughFamilyViews).
		q := `SELECT project_id, COUNT(*) FROM "` + table + `" GROUP BY project_id`
		into := aggregate
		if table == "events" {
			q = `SELECT project_id, SUM(n) FROM (
				SELECT project_id, COUNT(*) AS n FROM raw_views GROUP BY project_id
				UNION ALL SELECT project_id, COUNT(*) FROM raw_product GROUP BY project_id
				UNION ALL SELECT project_id, COUNT(*) FROM raw_measures GROUP BY project_id
			) GROUP BY project_id`
			into = raw
		}
		per, total, err := rowsPerProject(ctx, tx, q)
		if err != nil {
			return fmt.Errorf("count %s: %w", table, err)
		}
		for id, n := range per {
			into[id] += shareOfBytes(bytes[table], n, total)
			hasRows[id] = true
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM server_stats WHERE key IN (?, ?, ?) AND measured_at = ?`,
		store.StatRawBytes, store.StatAggregateBytes, store.StatDatabaseBytes, day); err != nil {
		return err
	}
	for id := range hasRows {
		for key, v := range map[string]int64{store.StatRawBytes: raw[id], store.StatAggregateBytes: aggregate[id]} {
			if err := putStat(ctx, tx, key, id, day, v); err != nil {
				return err
			}
		}
	}
	var file int64
	if err := tx.QueryRowContext(ctx, `SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`).Scan(&file); err != nil {
		return fmt.Errorf("database size: %w", err)
	}
	return putStat(ctx, tx, store.StatDatabaseBytes, 0, day, file)
}

// dailyCounts are the per-day counts, each the same sum project_stats and
// the v_* views make: a day is rolled up or raw, never both, and each row
// counts once either way.
var dailyCounts = []struct{ key, agg, raw string }{
	{store.StatViews, `SELECT project_id, day, SUM(views) AS n FROM agg_views_daily WHERE day < ?1 GROUP BY project_id, day`,
		`SELECT project_id, day, COUNT(*) FROM raw_views WHERE day < ?1 GROUP BY project_id, day`},
	{store.StatEvents, `SELECT project_id, day, SUM(total_events) AS n FROM agg_product_totals WHERE day < ?1 GROUP BY project_id, day`,
		`SELECT project_id, day, COUNT(*) FROM raw_product WHERE day < ?1 GROUP BY project_id, day`},
	{store.StatMeasures, `SELECT project_id, day, SUM(samples) AS n FROM agg_measures_daily WHERE day < ?1 GROUP BY project_id, day`,
		`SELECT project_id, day, COUNT(*) FROM raw_measures WHERE day < ?1 GROUP BY project_id, day`},
}

// countDays writes each project's counts for every day before today that
// the aggregates or the raw rows still hold, replacing what an earlier run
// wrote for those days. A day with nothing has no row.
func countDays(ctx context.Context, tx *sql.Tx, today string) error {
	for _, c := range dailyCounts {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO server_stats (key, project_id, measured_at, value)
			SELECT ?2, project_id, day, SUM(n) FROM (`+c.agg+` UNION ALL `+c.raw+`) GROUP BY project_id, day`,
			today, c.key); err != nil {
			return fmt.Errorf("count %s: %w", c.key, err)
		}
	}
	return nil
}

func putStat(ctx context.Context, tx *sql.Tx, key string, project int64, day string, v int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO server_stats (key, project_id, measured_at, value) VALUES (?, ?, ?, ?)`,
		key, project, day, v)
	return err
}

// shareOfBytes is n of total rows' share of a table's bytes. Floating point
// because bytes * n overflows int64 on a table of gigabytes with millions of
// rows; the estimate is no finer than a row anyway.
func shareOfBytes(bytes, n, total int64) int64 {
	if total == 0 {
		return 0
	}
	return int64(float64(bytes) * float64(n) / float64(total))
}

// tableBytes reads each table's bytes, its indexes included.
func tableBytes(ctx context.Context, tx *sql.Tx) (map[string]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT s.tbl_name, SUM(d.pgsize)
		FROM (SELECT name, pgsize FROM dbstat WHERE aggregate = TRUE) d
		JOIN sqlite_schema s ON s.name = d.name
		GROUP BY s.tbl_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}

// dataTables lists the tables holding a project's data: its raw rows
// (events) and every rollup keyed by project_id. The registry, the keys and
// server_stats are not data.
func dataTables(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT m.name FROM sqlite_schema m, pragma_table_info(m.name) c
		WHERE m.type = 'table' AND c.name = 'project_id'
		  AND (m.name = 'events' OR m.name LIKE 'agg\_%' ESCAPE '\' OR m.name IN ('actors', 'identities'))
		ORDER BY m.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// rowsPerProject runs a query of (project_id, rows) and answers the map and
// the sum of the rows.
func rowsPerProject(ctx context.Context, tx *sql.Tx, q string) (map[int64]int64, int64, error) {
	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	var total int64
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, 0, err
		}
		out[id] = n
		total += n
	}
	return out, total, rows.Err()
}
