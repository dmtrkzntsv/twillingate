package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// LoadAttributeRules is every attribute rule (rules with kind 'attribute'),
// by project and key: the registry snapshot loads them (list_attributes
// reads its rules from there), and manage reads a written rule's
// created_at back.
func (d *DB) LoadAttributeRules(ctx context.Context) ([]store.AttributeRule, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT project_id, name, action, COALESCE(to_name, ''), created_at
		FROM rules WHERE kind='attribute' AND family='event' ORDER BY project_id, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.AttributeRule
	for rows.Next() {
		var r store.AttributeRule
		if err := rows.Scan(&r.ProjectID, &r.Key, &r.Action, &r.To, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RenameAttribute keeps a rename rule from `from` to `to` on one project's
// product and measures events, which the event files apply on read (spec
// D6, D7): no event is rewritten. The catalog row moves with it. A `to`
// that is blocked is store.ErrConflict; whether `to` already has data is
// manage's to ask the files.
func (d *DB) RenameAttribute(ctx context.Context, projectID int64, from, to string, a store.AuditEntry) error {
	if from == to {
		return fmt.Errorf("rename attribute %s: from and to are the same key: %w", from, store.ErrInvalid)
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		var blocked bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM rules WHERE project_id=?1 AND kind='attribute' AND family='event' AND name=?2 AND action='block')`,
			projectID, to).Scan(&blocked); err != nil {
			return fmt.Errorf("rename attribute: look for a block on %s: %w", to, err)
		}
		if blocked {
			return fmt.Errorf("rename attribute %s to %s: %s is blocked; to stop receiving %s, block it: %w",
				from, to, to, from, store.ErrConflict)
		}
		if err := renameCatalog(ctx, tx, projectID, from, to); err != nil {
			return err
		}
		// The target is no longer mapped away (a block on it was refused
		// above); every rule that pointed at `from` now points at `to`; a
		// block or rename on `from` becomes this rename.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM rules WHERE project_id=?1 AND kind='attribute' AND family='event' AND name=?2 AND action='rename'`,
			projectID, to); err != nil {
			return fmt.Errorf("rename attribute rules: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE rules SET to_name=?3 WHERE project_id=?1 AND kind='attribute' AND family='event' AND action='rename' AND to_name=?2`,
			projectID, from, to); err != nil {
			return fmt.Errorf("rename attribute rules: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO rules (project_id, kind, family, name, action, to_name) VALUES (?1, 'attribute', 'event', ?2, 'rename', ?3)
			 ON CONFLICT (project_id, kind, family, name) DO UPDATE SET
			   action='rename', to_name=excluded.to_name, created_at=excluded.created_at`,
			projectID, from, to); err != nil {
			return fmt.Errorf("rename attribute rules: %w", err)
		}
		return auditAndBump(ctx, tx, a)
	})
}

// renameCatalog moves `from`'s catalog row to `to` (D7): `to` is a
// breakdown after if either was, so a rename or merge never adds to the
// breakdown budget. A `from` with no row (an alias for keys not sent yet)
// adds nothing.
func renameCatalog(ctx context.Context, tx *sql.Tx, projectID int64, from, to string) error {
	var had, breakdown bool
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) > 0, COALESCE(MAX(breakdown), 0) FROM project_attributes WHERE project_id=? AND key=?`,
		projectID, from).Scan(&had, &breakdown); err != nil {
		return fmt.Errorf("rename attribute catalog: %w", err)
	}
	if !had {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_attributes WHERE project_id=? AND key=?`, projectID, from); err != nil {
		return fmt.Errorf("rename attribute catalog: %w", err)
	}
	q := `INSERT OR IGNORE INTO project_attributes (project_id, key) VALUES (?, ?)`
	if breakdown {
		q = `INSERT INTO project_attributes (project_id, key, breakdown) VALUES (?, ?, 1)
			ON CONFLICT (project_id, key) DO UPDATE SET breakdown = 1`
	}
	if _, err := tx.ExecContext(ctx, q, projectID, to); err != nil {
		return fmt.Errorf("rename attribute catalog: %w", err)
	}
	return nil
}

// BlockAttribute keeps a block rule for key (D10): ingest strips the key
// from later events, the event files read none of its values, and it is
// no longer a breakdown. A block replaces a rename rule of
// the same key; a rename pointing at the key stays. Blocking a blocked key
// changes nothing but the audit row.
func (d *DB) BlockAttribute(ctx context.Context, projectID int64, key string, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO rules (project_id, kind, family, name, action) VALUES (?, 'attribute', 'event', ?, 'block')
			 ON CONFLICT (project_id, kind, family, name) DO UPDATE SET
			   action='block', to_name=NULL, created_at=excluded.created_at WHERE action <> 'block'`,
			projectID, key); err != nil {
			return fmt.Errorf("block attribute %s: %w", key, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE project_attributes SET breakdown = 0 WHERE project_id=? AND key=?`, projectID, key); err != nil {
			return fmt.Errorf("block attribute %s: %w", key, err)
		}
		return auditAndBump(ctx, tx, a)
	})
}

// RemoveAttributeRule deletes the rule for key (D14): the event files
// stop applying it on read, and ingest stops stripping it. It is store.ErrNotFound when the key has no rule.
func (d *DB) RemoveAttributeRule(ctx context.Context, projectID int64, key string, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM rules WHERE project_id=? AND kind='attribute' AND family='event' AND name=?`,
			projectID, key)
		if err != nil {
			return fmt.Errorf("remove attribute rule %s: %w", key, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("attribute rule %s: %w", key, store.ErrNotFound)
		}
		return auditAndBump(ctx, tx, a)
	})
}

// attributeRules reads the batch's projects' attribute rules in the
// flush's transaction (spec D9): one primary-key range query per product
// or measures project, so a rule committed before the flush applies to
// rows queued earlier. A project is in the result only when it has rules;
// a batch of views only queries nothing, and a batch with no rules returns
// nil.
func attributeRules(ctx context.Context, tx *sql.Tx, evs []store.Event) (map[int64]store.AttributeRuleSet, error) {
	var buf [8]int64
	projects := buf[:0]
	for i := range evs {
		e := &evs[i]
		if e.Family == store.FamilyViews {
			continue
		}
		if n := len(projects); n > 0 && projects[n-1] == e.ProjectID {
			continue
		}
		seen := false
		for _, p := range projects {
			if p == e.ProjectID {
				seen = true
				break
			}
		}
		if !seen {
			projects = append(projects, e.ProjectID)
		}
	}
	var out map[int64]store.AttributeRuleSet
	for _, p := range projects {
		rs, err := projectAttributeRules(ctx, tx, p)
		if err != nil {
			return nil, err
		}
		if rs.Empty() {
			continue
		}
		if out == nil {
			out = map[int64]store.AttributeRuleSet{}
		}
		out[p] = rs
	}
	return out, nil
}

// projectAttributeRules is one project's attribute rules, empty when it
// has none.
func projectAttributeRules(ctx context.Context, tx *sql.Tx, projectID int64) (store.AttributeRuleSet, error) {
	var rs store.AttributeRuleSet
	rows, err := tx.QueryContext(ctx,
		`SELECT name, action, COALESCE(to_name, '') FROM rules WHERE project_id=? AND kind='attribute' AND family='event'`,
		projectID)
	if err != nil {
		return rs, fmt.Errorf("attribute rules: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, action, to string
		if err := rows.Scan(&name, &action, &to); err != nil {
			return rs, fmt.Errorf("attribute rules: %w", err)
		}
		switch action {
		case store.EventRuleRename:
			if rs.Renamed == nil {
				rs.Renamed = map[string]string{}
			}
			rs.Renamed[name] = to
		case store.EventRuleBlock:
			if rs.Blocked == nil {
				rs.Blocked = map[string]bool{}
			}
			rs.Blocked[name] = true
		}
	}
	return rs, rows.Err()
}
