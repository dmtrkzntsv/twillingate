// Package sqlite provides SQLite backend for the store interface.
// Registry row access (managed-config spec §3). Every write bumps
// meta.config_version and inserts its audit row in the same transaction,
// which is what lets other processes notice changes by polling one row.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func auditAndBump(ctx context.Context, tx *sql.Tx, a store.AuditEntry) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO audit_log (actor, action, subject, detail) VALUES (?,?,?,?)`,
		a.Actor, a.Action, a.Subject, a.Detail); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE meta SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT)
		 WHERE key = 'config_version'`)
	return err
}

func (d *DB) ConfigVersion(ctx context.Context) (int64, error) {
	var v int64
	err := d.db.QueryRowContext(ctx,
		`SELECT CAST(value AS INTEGER) FROM meta WHERE key='config_version'`).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return v, err
}

func (d *DB) LoadRegistry(ctx context.Context) ([]store.RegistryProject, []store.RegistryKey, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, name,
		allowed_origins, attributes, archived_at IS NOT NULL FROM projects ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ps []store.RegistryProject
	for rows.Next() {
		var p store.RegistryProject
		if err := rows.Scan(&p.ID, &p.Name,
			&p.AllowedOrigins, &p.Attributes, &p.Archived); err != nil {
			return nil, nil, err
		}
		ps = append(ps, p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	krows, err := d.db.QueryContext(ctx, `SELECT key, project_id, label,
		disabled_at IS NOT NULL FROM ingest_keys ORDER BY project_id, label`)
	if err != nil {
		return nil, nil, err
	}
	defer krows.Close()
	var ks []store.RegistryKey
	for krows.Next() {
		var k store.RegistryKey
		if err := krows.Scan(&k.Key, &k.ProjectID, &k.Label, &k.Disabled); err != nil {
			return nil, nil, err
		}
		ks = append(ks, k)
	}
	return ks2(ps, ks, krows.Err())
}

// ks2 keeps the happy-path return on one line above.
func ks2(ps []store.RegistryProject, ks []store.RegistryKey, err error) ([]store.RegistryProject, []store.RegistryKey, error) {
	if err != nil {
		return nil, nil, err
	}
	return ps, ks, nil
}

// CreateProject lets SQLite assign the id and returns it. The audit subject
// is the new id, written here because only the store knows it.
func (d *DB) CreateProject(ctx context.Context, p store.RegistryProject, a store.AuditEntry) (int64, error) {
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if id, err = insertProject(ctx, tx, p); err != nil {
			return err
		}
		a.Subject = strconv.FormatInt(id, 10)
		return auditAndBump(ctx, tx, a)
	})
	return id, err
}

// CreateProjectWithKey creates a project and its first ingest key in one
// transaction: if the key cannot be inserted, the project is not created
// either, so a retry does not collide with a keyless leftover. k.ProjectID
// and both audit subjects are filled from the id SQLite assigns.
func (d *DB) CreateProjectWithKey(ctx context.Context, p store.RegistryProject, k store.RegistryKey, projectAudit, keyAudit store.AuditEntry) (int64, error) {
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if id, err = insertProject(ctx, tx, p); err != nil {
			return err
		}
		projectAudit.Subject = strconv.FormatInt(id, 10)
		if err := auditAndBump(ctx, tx, projectAudit); err != nil {
			return err
		}
		k.ProjectID = id
		if err := insertKey(ctx, tx, k); err != nil {
			return err
		}
		keyAudit.Subject = keySubject(id, k.Label)
		return auditAndBump(ctx, tx, keyAudit)
	})
	return id, err
}

// keySubject is the audit_log subject for a key: "<project id>/<label>".
func keySubject(projectID int64, label string) string {
	return strconv.FormatInt(projectID, 10) + "/" + label
}

func insertProject(ctx context.Context, tx *sql.Tx, p store.RegistryProject) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO projects
		(name, allowed_origins, attributes) VALUES (?,?,?)`,
		p.Name, p.AllowedOrigins, p.Attributes)
	if err != nil {
		return 0, fmt.Errorf("create project %q: %w", p.Name, err)
	}
	return res.LastInsertId()
}

func (d *DB) UpdateProject(ctx context.Context, p store.RegistryProject, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE projects SET name=?,
			allowed_origins=?, attributes=? WHERE id=?`,
			p.Name, p.AllowedOrigins, p.Attributes, p.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("update project: unknown id %d: %w", p.ID, store.ErrNotFound)
		}
		return auditAndBump(ctx, tx, a)
	})
}

func (d *DB) SetProjectArchived(ctx context.Context, id int64, archived bool, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		q := `UPDATE projects SET archived_at=datetime('now') WHERE id=? AND archived_at IS NULL`
		if !archived {
			q = `UPDATE projects SET archived_at=NULL WHERE id=?`
		}
		res, err := tx.ExecContext(ctx, q, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 && archived {
			// restore of a non-archived project is a no-op, archive of an
			// unknown id is an error; check existence to distinguish.
			var c int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM projects WHERE id=?`, id).Scan(&c); err != nil {
				return err
			}
			if c == 0 {
				return fmt.Errorf("archive: unknown id %d: %w", id, store.ErrNotFound)
			}
		}
		return auditAndBump(ctx, tx, a)
	})
}

func (d *DB) InsertIngestKey(ctx context.Context, k store.RegistryKey, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if err := insertKey(ctx, tx, k); err != nil {
			return err
		}
		return auditAndBump(ctx, tx, a)
	})
}

// insertKey relies on UNIQUE (project_id, label): the constraint error is
// mapped to store.ErrConflict rather than pre-checked with a count.
func insertKey(ctx context.Context, tx *sql.Tx, k store.RegistryKey) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO ingest_keys (key, project_id, label) VALUES (?,?,?)`,
		k.Key, k.ProjectID, k.Label)
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed: ingest_keys.project_id, ingest_keys.label") {
		return fmt.Errorf("key label %q for project %d: %w", k.Label, k.ProjectID, store.ErrConflict)
	}
	return fmt.Errorf("issue key for project %d: %w", k.ProjectID, err)
}

func (d *DB) SetIngestKeyDisabled(ctx context.Context, projectID int64, label string, disabled bool, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		q := `UPDATE ingest_keys SET disabled_at=datetime('now') WHERE project_id=? AND label=?`
		if !disabled {
			q = `UPDATE ingest_keys SET disabled_at=NULL WHERE project_id=? AND label=?`
		}
		res, err := tx.ExecContext(ctx, q, projectID, label)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("key %s: %w", keySubject(projectID, label), store.ErrNotFound)
		}
		return auditAndBump(ctx, tx, a)
	})
}

// projectTables is every table carrying a per-project `project_id` column.
// Kept in one place so a future migration adding a table has one list to
// extend; TestProjectTablesMatchesSchema (registry_test.go) cross-checks
// this list against the live schema (sqlite_master + pragma_table_info) in
// both directions, so a forgotten addition or a stale entry fails loudly
// instead of silently orphaning rows on DeleteProjectData.
var projectTables = []string{
	"views", "events",
	"agg_views_daily", "agg_views_paths", "agg_views_hosts", "agg_views_referrers",
	"agg_views_utm", "agg_views_countries", "agg_views_platforms", "agg_views_os",
	"agg_views_browsers", "agg_views_app_versions", "agg_views_devices", "agg_views_displays",
	"agg_product_daily", "agg_product_totals", "agg_product_attrs",
	"actors", "agg_retention", "identities", "agg_identity_daily",
	"ingest_keys",
}

// DeleteProjectData hard-deletes the project and every row keyed by its
// id, in one transaction (spec §7.3). The audit row is written in the
// same transaction and survives — audit_log has no project column.
// Page reclamation is the caller's job (IncrementalVacuum), because a
// vacuum inside the tx would deadlock the single connection. The id is
// never reissued (AUTOINCREMENT), so a stale reference to it stays dead.
func (d *DB) DeleteProjectData(ctx context.Context, id int64, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("delete: unknown id %d: %w", id, store.ErrNotFound)
		}
		for _, table := range projectTables {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM `+table+` WHERE project_id=?`, id); err != nil {
				return fmt.Errorf("delete %s: %w", table, err)
			}
		}
		return auditAndBump(ctx, tx, a)
	})
}
