package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// oldestRawDaySQL is the oldest day the project holds in events, NULL when
// it holds none. It reads each family's key prefix (family, project_id,
// day, ...): one probe per family, not a scan of the table.
const oldestRawDaySQL = `SELECT MIN(d) FROM (
	SELECT MIN(day) AS d FROM events WHERE family = 1 AND project_id = ?1
	UNION ALL SELECT MIN(day) FROM events WHERE family = 2 AND project_id = ?1
	UNION ALL SELECT MIN(day) FROM events WHERE family = 3 AND project_id = ?1)`

// CleanDictionaries deletes the dictionary rows no raw row can still use
// (events encoding spec D7): the rows whose last_seen_day is before the
// cutoff, which is the oldest day still held raw and never later than
// before (the day ahead of the raw window, which ingest can still write
// to, so an id cached for a day with no row held survives). The pass
// replaces the catalog prune that ran with the usage recount.
//
//   - strings: the cutoff is the oldest day across every project's rows;
//   - attribute_values: the owning project's cutoff;
//   - the catalog: the project's cutoff too, and a row also stays while it
//     is a breakdown or a usage_attributes row holds its key (the attribute
//     catalog's own rules, attributes spec D3). A dropped key's values go
//     with it (ON DELETE CASCADE).
//
// Ingest needs no lock against this: it resolves every id through an upsert
// in its own transaction, which re-inserts a deleted text under a new id
// and raises last_seen_day to the row's day, a day this cutoff does not
// pass. Run it after the rollups deleted the expired days, so it sees the
// oldest day left.
func (d *DB) CleanDictionaries(ctx context.Context, before civil.Date) error {
	b := before.String()
	return d.tx(ctx, func(tx *sql.Tx) error {
		cutoffs, oldest, err := projectCutoffs(ctx, tx, b)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM strings WHERE last_seen_day < ?1`, oldest); err != nil {
			return fmt.Errorf("clean strings: %w", err)
		}
		for _, c := range cutoffs {
			if _, err := tx.ExecContext(ctx, `DELETE FROM attribute_values WHERE last_seen_day < ?2
				AND key_id IN (SELECT id FROM project_attributes WHERE project_id = ?1)`, c.project, c.day); err != nil {
				return fmt.Errorf("clean attribute values of project %d: %w", c.project, err)
			}
			// The usage subquery is uncorrelated: SQLite reads the
			// project's usage keys once into a set, where a correlated
			// NOT EXISTS probes usage_attributes per catalog row by its
			// project_id prefix alone (the key follows day in its primary
			// key), quadratic in the key count on a database never analyzed.
			if _, err := tx.ExecContext(ctx, `DELETE FROM project_attributes
				WHERE project_id = ?1 AND breakdown = 0 AND last_seen_day < ?2
				AND key NOT IN (SELECT attr_key FROM usage_attributes WHERE project_id = ?1)`, c.project, c.day); err != nil {
				return fmt.Errorf("clean attribute catalog of project %d: %w", c.project, err)
			}
		}
		return nil
	})
}

type projectCutoff struct {
	project int64
	day     string
}

// projectCutoffs returns each project's cutoff day, the earlier of before
// and its oldest raw day, and the earliest of them all (strings are shared
// by every project, so their cutoff is the oldest day anywhere). The ids
// are read first: the transaction's one connection cannot run the oldest
// day queries while a result set is open on it. Every project with rows
// is in projects (a purge deletes its rows, ingest drops the rows of a
// project that is gone), so the projects table covers events.
func projectCutoffs(ctx context.Context, tx *sql.Tx, before string) ([]projectCutoff, string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM projects`)
	if err != nil {
		return nil, "", fmt.Errorf("clean dictionaries: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, "", fmt.Errorf("clean dictionaries: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("clean dictionaries: %w", err)
	}
	cutoffs := make([]projectCutoff, 0, len(ids))
	oldest := before
	for _, id := range ids {
		var day sql.NullString
		if err := tx.QueryRowContext(ctx, oldestRawDaySQL, id).Scan(&day); err != nil {
			return nil, "", fmt.Errorf("oldest raw day of project %d: %w", id, err)
		}
		cut := before
		if day.Valid && day.String < cut {
			cut = day.String
		}
		if cut < oldest {
			oldest = cut
		}
		cutoffs = append(cutoffs, projectCutoff{id, cut})
	}
	return cutoffs, oldest, nil
}
