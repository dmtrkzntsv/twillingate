package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// LoadEventRules is every event rule (migration 039), for the registry
// snapshot and list_event_rules.
func (d *DB) LoadEventRules(ctx context.Context) ([]store.EventRule, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT project_id, family, name, action,
		COALESCE(to_name, ''), created_at FROM event_rules ORDER BY project_id, family, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.EventRule
	for rows.Next() {
		var r store.EventRule
		var fam string
		if err := rows.Scan(&r.ProjectID, &fam, &r.Name, &r.Action, &r.To, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Family = store.Family(fam)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RenameEvent renames an event of one family in one project: the raw rows
// and the family's aggregates are rewritten from `from` to `to`, and a
// rename rule is kept so later writes land under `to` (D4, D7). Chains are
// collapsed (D5): no rule's target is itself a rename rule's name. A `to`
// that is blocked is store.ErrConflict: a rename or merge never drops what
// it moves. A `to` that already has rows is store.ErrConflict unless merge
// is set; merged aggregate rows are summed (D6). It returns the rows
// rewritten per table ("events" and the aggregate tables of the family).
func (d *DB) RenameEvent(ctx context.Context, projectID int64, family store.Family,
	from, to string, merge bool, a store.AuditEntry) (map[string]int64, error) {
	if from == to {
		return nil, fmt.Errorf("rename %s: from and to are the same name", from)
	}
	rewritten := map[string]int64{}
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var blocked bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM event_rules WHERE project_id=?1 AND family=?2 AND name=?3 AND action='block')`,
			projectID, string(family), to).Scan(&blocked); err != nil {
			return fmt.Errorf("rename: look for a block on %s: %w", to, err)
		}
		if blocked {
			return fmt.Errorf("rename %s to %s: %s is blocked; to stop receiving %s, block it: %w",
				from, to, to, from, store.ErrConflict)
		}
		if !merge {
			has, err := hasEventRows(ctx, tx, projectID, family, to)
			if err != nil {
				return err
			}
			if has {
				return fmt.Errorf("rename %s to %s: %s already has events; merge %s into it instead: %w",
					from, to, to, from, store.ErrConflict)
			}
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE events SET event_name=?3 WHERE family=?4 AND project_id=?1 AND event_name=?2`,
			projectID, from, to, string(family))
		if err != nil {
			return fmt.Errorf("rename events: %w", err)
		}
		if rewritten["events"], err = res.RowsAffected(); err != nil {
			return err
		}
		if err := renameAggregates(ctx, tx, projectID, family, from, to, rewritten); err != nil {
			return err
		}
		// D5: the target is no longer mapped away (a block on it stays);
		// every rule that pointed at `from` now points at `to`.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM event_rules WHERE project_id=?1 AND family=?2 AND name=?3 AND action='rename'`,
			projectID, string(family), to); err != nil {
			return fmt.Errorf("rename rules: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE event_rules SET to_name=?4 WHERE project_id=?1 AND family=?2 AND action='rename' AND to_name=?3`,
			projectID, string(family), from, to); err != nil {
			return fmt.Errorf("rename rules: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO event_rules (project_id, family, name, action, to_name) VALUES (?1, ?2, ?3, 'rename', ?4)
			 ON CONFLICT (project_id, family, name) DO UPDATE SET
			   action='rename', to_name=excluded.to_name, created_at=excluded.created_at`,
			projectID, string(family), from, to); err != nil {
			return fmt.Errorf("rename rules: %w", err)
		}
		return auditAndBump(ctx, tx, a)
	})
	if err != nil {
		return nil, err
	}
	return rewritten, nil
}

// hasEventRows reports whether name has raw or aggregated rows in the
// family of the project.
func hasEventRows(ctx context.Context, tx *sql.Tx, projectID int64, family store.Family, name string) (bool, error) {
	const product = `SELECT EXISTS(SELECT 1 FROM raw_product WHERE project_id=?1 AND event_name=?2)
		OR EXISTS(SELECT 1 FROM agg_product_daily WHERE project_id=?1 AND event_name=?2)
		OR EXISTS(SELECT 1 FROM agg_product_attrs WHERE project_id=?1 AND event_name=?2)`
	const measures = `SELECT EXISTS(SELECT 1 FROM raw_measures WHERE project_id=?1 AND event_name=?2)
		OR EXISTS(SELECT 1 FROM agg_measures_daily WHERE project_id=?1 AND event_name=?2)
		OR EXISTS(SELECT 1 FROM agg_measures_attrs WHERE project_id=?1 AND event_name=?2)`
	q := product
	if family == store.FamilyMeasures {
		q = measures
	}
	var has bool
	if err := tx.QueryRowContext(ctx, q, projectID, name).Scan(&has); err != nil {
		return false, fmt.Errorf("rename: look for %s: %w", name, err)
	}
	return has, nil
}

// aggregateMove is one aggregate table's rename: an upsert of the source
// rows under the new name, and a delete of the source rows. The WHERE true
// is required by SQLite's parser for an upsert from a SELECT. unique_users
// and unique_groups summed are an upper bound (D6); a NULL unique_groups on
// either side stays NULL (not tracked).
type aggregateMove struct {
	family                store.Family
	table, upsert, remove string
}

var aggregateMoves = []aggregateMove{
	{store.FamilyProduct, "agg_product_daily",
		`INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users)
		 SELECT project_id, day, ?3, count, unique_users FROM agg_product_daily
		 WHERE project_id=?1 AND event_name=?2 AND true
		 ON CONFLICT (project_id, day, event_name) DO UPDATE SET
		   count = count + excluded.count, unique_users = unique_users + excluded.unique_users`,
		`DELETE FROM agg_product_daily WHERE project_id=?1 AND event_name=?2`},
	{store.FamilyProduct, "agg_product_attrs",
		`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
		 SELECT project_id, day, ?3, attr_key, attr_value, count, unique_users, unique_groups FROM agg_product_attrs
		 WHERE project_id=?1 AND event_name=?2 AND true
		 ON CONFLICT (project_id, day, event_name, attr_key, attr_value) DO UPDATE SET
		   count = count + excluded.count, unique_users = unique_users + excluded.unique_users,
		   unique_groups = unique_groups + excluded.unique_groups`,
		`DELETE FROM agg_product_attrs WHERE project_id=?1 AND event_name=?2`},
	{store.FamilyMeasures, "agg_measures_daily",
		`INSERT INTO agg_measures_daily (project_id, day, event_name, measure, bucket, samples, weight, sum)
		 SELECT project_id, day, ?3, measure, bucket, samples, weight, sum FROM agg_measures_daily
		 WHERE project_id=?1 AND event_name=?2 AND true
		 ON CONFLICT (project_id, day, event_name, measure, bucket) DO UPDATE SET
		   samples = samples + excluded.samples, weight = weight + excluded.weight, sum = sum + excluded.sum`,
		`DELETE FROM agg_measures_daily WHERE project_id=?1 AND event_name=?2`},
	{store.FamilyMeasures, "agg_measures_attrs",
		`INSERT INTO agg_measures_attrs (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum)
		 SELECT project_id, day, ?3, measure, attr_key, attr_value, bucket, samples, weight, sum FROM agg_measures_attrs
		 WHERE project_id=?1 AND event_name=?2 AND true
		 ON CONFLICT (project_id, day, event_name, measure, attr_key, attr_value, bucket) DO UPDATE SET
		   samples = samples + excluded.samples, weight = weight + excluded.weight, sum = sum + excluded.sum`,
		`DELETE FROM agg_measures_attrs WHERE project_id=?1 AND event_name=?2`},
}

// renameAggregates moves the family's aggregate rows from one name to
// another, summing rows that meet on the primary key (a merge, D6), and
// records the upserted rows per table.
func renameAggregates(ctx context.Context, tx *sql.Tx, projectID int64, family store.Family,
	from, to string, rewritten map[string]int64) error {
	for _, m := range aggregateMoves {
		if m.family != family {
			continue
		}
		res, err := tx.ExecContext(ctx, m.upsert, projectID, from, to)
		if err != nil {
			return fmt.Errorf("rename %s: %w", m.table, err)
		}
		if rewritten[m.table], err = res.RowsAffected(); err != nil {
			return fmt.Errorf("rename %s: %w", m.table, err)
		}
		if _, err := tx.ExecContext(ctx, m.remove, projectID, from); err != nil {
			return fmt.Errorf("rename %s: %w", m.table, err)
		}
	}
	return nil
}

// BlockEvent keeps a block rule for name (D1, D9): ingest drops the name
// and the daily pass keeps it out of the aggregates. A block replaces a
// rename rule of the same name; blocking a blocked name changes nothing
// but the audit row.
func (d *DB) BlockEvent(ctx context.Context, projectID int64, family store.Family, name string, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO event_rules (project_id, family, name, action) VALUES (?,?,?,'block')
			 ON CONFLICT (project_id, family, name) DO UPDATE SET
			   action='block', to_name=NULL, created_at=excluded.created_at WHERE action <> 'block'`,
			projectID, string(family), name); err != nil {
			return fmt.Errorf("block %s: %w", name, err)
		}
		return auditAndBump(ctx, tx, a)
	})
}

// RemoveEventRule deletes the rule for name: a removed rename stops the
// mapping (history stays renamed), a removed block resumes storing. It is
// store.ErrNotFound when no such rule exists.
func (d *DB) RemoveEventRule(ctx context.Context, projectID int64, family store.Family, name string, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM event_rules WHERE project_id=? AND family=? AND name=?`,
			projectID, string(family), name)
		if err != nil {
			return fmt.Errorf("remove event rule %s: %w", name, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("event rule %s/%s: %w", family, name, store.ErrNotFound)
		}
		return auditAndBump(ctx, tx, a)
	})
}

// dropBlockedAggregates delete, from each per-event aggregate table, the
// rows of the names the family blocks. agg_product_totals is left out on
// purpose (D11): it is the day's true DAU and total, which a name cannot be
// subtracted from.
var dropBlockedAggregates = [...]string{
	`DELETE FROM agg_product_daily WHERE project_id=?1 AND event_name IN
		(SELECT name FROM event_rules WHERE project_id=?1 AND family='product' AND action='block')`,
	`DELETE FROM agg_product_attrs WHERE project_id=?1 AND event_name IN
		(SELECT name FROM event_rules WHERE project_id=?1 AND family='product' AND action='block')`,
	`DELETE FROM agg_measures_daily WHERE project_id=?1 AND event_name IN
		(SELECT name FROM event_rules WHERE project_id=?1 AND family='measures' AND action='block')`,
	`DELETE FROM agg_measures_attrs WHERE project_id=?1 AND event_name IN
		(SELECT name FROM event_rules WHERE project_id=?1 AND family='measures' AND action='block')`,
}

// DropBlockedAggregates deletes the project's aggregate rows of every
// blocked name (D11), for the daily pass: a block set after a day was
// rolled up takes the name out of the aggregates at the next pass. The
// totals keep the day's true counts (see dropBlockedAggregates). Nothing
// to drop is a no-op.
func (d *DB) DropBlockedAggregates(ctx context.Context, projectID int64) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		for _, q := range dropBlockedAggregates {
			if _, err := tx.ExecContext(ctx, q, projectID); err != nil {
				return fmt.Errorf("drop blocked aggregates: %w", err)
			}
		}
		return nil
	})
}
