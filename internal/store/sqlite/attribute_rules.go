package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// jsonPath is the SQLite JSON path of one top-level key, quoted so that a
// dot, a slash or a space in the key is part of its name, never a step
// into a nested object. manage refuses a key containing `"` (spec D5),
// which the quoting cannot carry. The path is always bound as a parameter.
func jsonPath(key string) string { return `$."` + key + `"` }

// LoadAttributeRules is every attribute rule (rules with kind 'attribute'),
// for the registry snapshot and list_attribute_rules, by project and key.
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

// RenameAttribute renames an attribute key of one project's product and
// measures events (spec D6, D7): in one transaction the raw rows, the
// attribute aggregates, usage_attributes, the catalog and the rules move
// from `from` to `to`. A raw row carrying both keeps `to`'s value (the
// target wins). A `to` that is blocked is store.ErrConflict, merge or not;
// a `to` with stored data is store.ErrConflict unless merge is set, and
// merged rows are summed (D8). It returns the rows rewritten per table.
// After the commit the project's remembered usage triples are forgotten,
// the usage rows having moved.
func (d *DB) RenameAttribute(ctx context.Context, projectID int64, from, to string, merge bool,
	a store.AuditEntry) (map[string]int64, error) {
	if from == to {
		return nil, fmt.Errorf("rename attribute %s: from and to are the same key: %w", from, store.ErrInvalid)
	}
	rewritten := map[string]int64{}
	err := d.tx(ctx, func(tx *sql.Tx) error {
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
		if !merge {
			var has bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_attributes WHERE project_id=?1 AND attr_key=?2)
				OR EXISTS(SELECT 1 FROM agg_product_attrs WHERE project_id=?1 AND attr_key=?2)
				OR EXISTS(SELECT 1 FROM agg_measures_attrs WHERE project_id=?1 AND attr_key=?2)`,
				projectID, to).Scan(&has); err != nil {
				return fmt.Errorf("rename attribute: look for %s: %w", to, err)
			}
			if has {
				return fmt.Errorf("rename attribute %s to %s: %s already has data; merge %s into it instead: %w",
					from, to, to, from, store.ErrConflict)
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE events SET attributes = CASE
			  WHEN json_type(attributes, ?3) IS NULL
			    THEN json_set(json_remove(attributes, ?2), ?3, json_extract(attributes, ?2))
			  ELSE json_remove(attributes, ?2) END
			WHERE project_id = ?1 AND family IN ('product', 'measures') AND json_type(attributes, ?2) IS NOT NULL`,
			projectID, jsonPath(from), jsonPath(to))
		if err != nil {
			return fmt.Errorf("rename attribute events: %w", err)
		}
		if rewritten["events"], err = res.RowsAffected(); err != nil {
			return err
		}
		for _, m := range attributeMoves {
			res, err := tx.ExecContext(ctx, m.upsert, projectID, from, to)
			if err != nil {
				return fmt.Errorf("rename attribute %s: %w", m.table, err)
			}
			if rewritten[m.table], err = res.RowsAffected(); err != nil {
				return fmt.Errorf("rename attribute %s: %w", m.table, err)
			}
			if _, err := tx.ExecContext(ctx, m.remove, projectID, from); err != nil {
				return fmt.Errorf("rename attribute %s: %w", m.table, err)
			}
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
	if err != nil {
		return nil, err
	}
	d.seen.forget(projectID)
	return rewritten, nil
}

// attributeMoves are the per-key tables a rename moves, as aggregateMoves
// for event names but keyed on attr_key: an upsert of the source rows
// under the new key, summing the rows that meet (a merge, D7, D8), then a
// delete of the source rows. The WHERE true is required by SQLite's parser
// for an upsert from a SELECT. In usage_attributes a NULL max_values (not
// yet counted) yields to a number; the next daily pass recounts the day.
var attributeMoves = []struct{ table, upsert, remove string }{
	{"agg_product_attrs",
		`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
		 SELECT project_id, day, event_name, ?3, attr_value, count, unique_users, unique_groups FROM agg_product_attrs
		 WHERE project_id=?1 AND attr_key=?2 AND true
		 ON CONFLICT (project_id, day, event_name, attr_key, attr_value) DO UPDATE SET
		   count = count + excluded.count, unique_users = unique_users + excluded.unique_users,
		   unique_groups = unique_groups + excluded.unique_groups`,
		`DELETE FROM agg_product_attrs WHERE project_id=?1 AND attr_key=?2`},
	{"agg_measures_attrs",
		`INSERT INTO agg_measures_attrs (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum)
		 SELECT project_id, day, event_name, measure, ?3, attr_value, bucket, samples, weight, sum FROM agg_measures_attrs
		 WHERE project_id=?1 AND attr_key=?2 AND true
		 ON CONFLICT (project_id, day, event_name, measure, attr_key, attr_value, bucket) DO UPDATE SET
		   samples = samples + excluded.samples, weight = weight + excluded.weight, sum = sum + excluded.sum`,
		`DELETE FROM agg_measures_attrs WHERE project_id=?1 AND attr_key=?2`},
	{"usage_attributes",
		`INSERT INTO usage_attributes (project_id, day, attr_key, events, max_values)
		 SELECT project_id, day, ?3, events, max_values FROM usage_attributes
		 WHERE project_id=?1 AND attr_key=?2 AND true
		 ON CONFLICT (project_id, day, attr_key) DO UPDATE SET
		   events = events + excluded.events,
		   max_values = CASE WHEN max_values IS NULL THEN excluded.max_values
		                     WHEN excluded.max_values IS NULL THEN max_values
		                     ELSE max(max_values, excluded.max_values) END`,
		`DELETE FROM usage_attributes WHERE project_id=?1 AND attr_key=?2`},
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

// BlockAttribute keeps a block rule for key (D10): ingest and the store
// strip the key from later events, the daily pass drops its aggregates
// (D12), and it is no longer a breakdown. A block replaces a rename rule of
// the same key; a rename pointing at the key stays. Blocking a blocked key
// changes nothing but the audit row.
func (d *DB) BlockAttribute(ctx context.Context, projectID int64, key string, a store.AuditEntry) error {
	err := d.tx(ctx, func(tx *sql.Tx) error {
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
	if err != nil {
		return err
	}
	d.seen.forget(projectID)
	return nil
}

// RemoveAttributeRule deletes the rule for key (D14): a removed rename
// stops mapping (history stays where it went), a removed block stops
// stripping. It is store.ErrNotFound when the key has no rule.
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
