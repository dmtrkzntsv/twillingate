package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// LoadEventRules is every event rule (rules with kind 'event', migration
// 040), for the registry
// snapshot and list_event_rules.
func (d *DB) LoadEventRules(ctx context.Context) ([]store.EventRule, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT project_id, family, name, action,
		COALESCE(to_name, ''), created_at FROM rules WHERE kind='event' ORDER BY project_id, family, name`)
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

// RenameEvent keeps a rename rule from `from` to `to` in one family of one
// project, which the event files apply on read (raw-first D6): no event is
// rewritten. Chains are collapsed (D5): no rule's target is itself a
// rename rule's name. A `to` that is blocked is store.ErrConflict: a
// rename or merge never drops what it moves. A `to` that is itself
// renamed to a third name is store.ErrConflict too: the collapse would
// delete that rule and bring its history back under `to`. Whether `to`
// already has events is manage's to ask the files.
func (d *DB) RenameEvent(ctx context.Context, projectID int64, family store.Family,
	from, to string, a store.AuditEntry) error {
	if from == to {
		return fmt.Errorf("rename %s: from and to are the same name", from)
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		var blocked bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM rules WHERE project_id=?1 AND kind='event' AND family=?2 AND name=?3 AND action='block')`,
			projectID, string(family), to).Scan(&blocked); err != nil {
			return fmt.Errorf("rename: look for a block on %s: %w", to, err)
		}
		if blocked {
			return fmt.Errorf("rename %s to %s: %s is blocked; to stop receiving %s, block it: %w",
				from, to, to, from, store.ErrConflict)
		}
		// A target renamed to a third name would lose that rule below,
		// and the history stored before it would read under the target
		// again: refused. Renaming back (to → from) is allowed.
		var onward string
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(to_name), '') FROM rules WHERE project_id=?1 AND kind='event' AND family=?2 AND name=?3 AND action='rename'`,
			projectID, string(family), to).Scan(&onward); err != nil {
			return fmt.Errorf("rename: look for a rename of %s: %w", to, err)
		}
		if onward != "" && onward != from {
			return fmt.Errorf("rename %s to %s: %s is renamed to %s: rename into %s, or remove that rule first: %w",
				from, to, to, onward, onward, store.ErrConflict)
		}
		// D5: the target is no longer mapped away (a block on it stays);
		// every rule that pointed at `from` now points at `to`.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM rules WHERE project_id=?1 AND kind='event' AND family=?2 AND name=?3 AND action='rename'`,
			projectID, string(family), to); err != nil {
			return fmt.Errorf("rename rules: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE rules SET to_name=?4 WHERE project_id=?1 AND kind='event' AND family=?2 AND action='rename' AND to_name=?3`,
			projectID, string(family), from, to); err != nil {
			return fmt.Errorf("rename rules: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO rules (project_id, kind, family, name, action, to_name) VALUES (?1, 'event', ?2, ?3, 'rename', ?4)
			 ON CONFLICT (project_id, kind, family, name) DO UPDATE SET
			   action='rename', to_name=excluded.to_name, created_at=excluded.created_at`,
			projectID, string(family), from, to); err != nil {
			return fmt.Errorf("rename rules: %w", err)
		}
		return auditAndBump(ctx, tx, a)
	})
}

// BlockEvent keeps a block rule for name (D1, D9): ingest drops the name
// and the event files read none of it. A block replaces a
// rename rule of the same name; blocking a blocked name changes nothing
// but the audit row.
func (d *DB) BlockEvent(ctx context.Context, projectID int64, family store.Family, name string, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO rules (project_id, kind, family, name, action) VALUES (?,'event',?,?,'block')
			 ON CONFLICT (project_id, kind, family, name) DO UPDATE SET
			   action='block', to_name=NULL, created_at=excluded.created_at WHERE action <> 'block'`,
			projectID, string(family), name); err != nil {
			return fmt.Errorf("block %s: %w", name, err)
		}
		return auditAndBump(ctx, tx, a)
	})
}

// RemoveEventRule deletes the rule for name: the event files stop
// applying it on read, and a removed block resumes storing. It is
// store.ErrNotFound when no such rule exists.
func (d *DB) RemoveEventRule(ctx context.Context, projectID int64, family store.Family, name string, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM rules WHERE project_id=? AND kind='event' AND family=? AND name=?`,
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
