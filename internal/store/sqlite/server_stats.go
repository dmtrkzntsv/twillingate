package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// MeasureServerStats takes every server_stats measurement and replaces the
// last ones with it, in one transaction: a reader sees the old values or
// the new, never a mix, and every row carries the same measured_at.
//
// Today that is each project's estimated disk use, raw (events) and
// aggregate (every agg_* table, actors and identities; not the registry,
// the keys or server_stats itself). dbstat reads every page of the file, so
// this runs in the daily pass, never in a request. A table's bytes (its
// indexes included) are split by each project's share of the table's rows.
// A project with no rows has no size: the old rows go, and only projects
// holding rows get a new pair.
func (d *DB) MeasureServerStats(ctx context.Context, now time.Time) error {
	at := now.UTC().Format(time.RFC3339)
	return d.tx(ctx, func(tx *sql.Tx) error {
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
		if _, err := tx.ExecContext(ctx, `DELETE FROM server_stats WHERE key IN (?, ?)`,
			store.StatRawBytes, store.StatAggregateBytes); err != nil {
			return err
		}
		for id := range hasRows {
			for key, v := range map[string]int64{store.StatRawBytes: raw[id], store.StatAggregateBytes: aggregate[id]} {
				if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO server_stats (key, project_id, value, measured_at)
					VALUES (?, ?, ?, ?)`, key, id, v, at); err != nil {
					return err
				}
			}
		}
		return nil
	})
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
