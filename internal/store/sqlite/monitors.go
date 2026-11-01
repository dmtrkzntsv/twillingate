package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const monitorCols = `project_id, enabled, origin, contains, cert_expires_at, cert_issuer,
	cert_valid, cert_error, cert_checked_at, domain_expires_at, domain_checked_at, redirects,
	check_requested_at, check_finished_at, check_up, check_status, check_reason, check_error, check_ms`

func scanMonitor(s rowScanner) (store.Monitor, error) {
	var m store.Monitor
	err := s.Scan(&m.ProjectID, &m.Enabled, &m.Origin, &m.Contains, &m.CertExpiresAt, &m.CertIssuer,
		&m.CertValid, &m.CertError, &m.CertCheckedAt, &m.DomainExpiresAt, &m.DomainCheckedAt, &m.Redirects,
		&m.CheckRequestedAt, &m.CheckFinishedAt, &m.CheckUp, &m.CheckStatus, &m.CheckReason, &m.CheckError, &m.CheckMS)
	return m, err
}

func unknownProject(id int64) error {
	return fmt.Errorf("project %d: %w", id, store.ErrNotFound)
}

// projectExists reports whether a project row (archived or not) has id.
func projectExists(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id int64) (bool, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id=?`, id).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// getMonitor reads project id's monitor; a project without a row reads as
// Monitor{ProjectID: id}, an unknown project as ErrNotFound.
func getMonitor(ctx context.Context, tx *sql.Tx, id int64) (store.Monitor, error) {
	m, err := scanMonitor(tx.QueryRowContext(ctx,
		`SELECT `+monitorCols+` FROM monitors WHERE project_id=?`, id))
	if err == nil {
		return m, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.Monitor{}, err
	}
	ok, err := projectExists(ctx, tx, id)
	if err != nil {
		return store.Monitor{}, err
	}
	if !ok {
		return store.Monitor{}, unknownProject(id)
	}
	return store.Monitor{ProjectID: id}, nil
}

// GetMonitor reads one project's monitor.
func (d *DB) GetMonitor(ctx context.Context, projectID int64) (store.Monitor, error) {
	var m store.Monitor
	err := d.tx(ctx, func(tx *sql.Tx) (err error) {
		m, err = getMonitor(ctx, tx, projectID)
		return err
	})
	return m, err
}

// SetMonitor upserts a project's settings and audits it. A changed origin
// clears the stored certificate and domain readings, the redirect count and the
// manual check, which described another host. Enabling a disabled monitor clears their check times only,
// so the next slot reads them again and the known dates stay until then. It
// is not managed-config: no version bump.
func (d *DB) SetMonitor(ctx context.Context, projectID int64, s store.MonitorSettings, a store.AuditEntry) (store.Monitor, error) {
	var m store.Monitor
	err := d.tx(ctx, func(tx *sql.Tx) error {
		ok, err := projectExists(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if !ok {
			return unknownProject(projectID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO monitors (project_id, enabled, origin, contains)
			VALUES (?,?,?,?)
			ON CONFLICT (project_id) DO UPDATE SET
			  cert_expires_at   = CASE WHEN origin = excluded.origin THEN cert_expires_at ELSE '' END,
			  cert_issuer       = CASE WHEN origin = excluded.origin THEN cert_issuer ELSE '' END,
			  cert_valid        = CASE WHEN origin = excluded.origin THEN cert_valid ELSE 0 END,
			  cert_error        = CASE WHEN origin = excluded.origin THEN cert_error ELSE '' END,
			  cert_checked_at   = CASE WHEN origin = excluded.origin AND (enabled = 1 OR excluded.enabled = 0) THEN cert_checked_at ELSE '' END,
			  domain_expires_at = CASE WHEN origin = excluded.origin THEN domain_expires_at ELSE '' END,
			  domain_checked_at = CASE WHEN origin = excluded.origin AND (enabled = 1 OR excluded.enabled = 0) THEN domain_checked_at ELSE '' END,
			  redirects         = CASE WHEN origin = excluded.origin THEN redirects ELSE 0 END,
			  check_requested_at = CASE WHEN origin = excluded.origin THEN check_requested_at ELSE '' END,
			  check_finished_at = CASE WHEN origin = excluded.origin THEN check_finished_at ELSE '' END,
			  check_up          = CASE WHEN origin = excluded.origin THEN check_up ELSE 0 END,
			  check_status      = CASE WHEN origin = excluded.origin THEN check_status ELSE 0 END,
			  check_reason      = CASE WHEN origin = excluded.origin THEN check_reason ELSE '' END,
			  check_error       = CASE WHEN origin = excluded.origin THEN check_error ELSE '' END,
			  check_ms          = CASE WHEN origin = excluded.origin THEN check_ms ELSE 0 END,
			  enabled = excluded.enabled, origin = excluded.origin, contains = excluded.contains,
			  updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')`,
			projectID, s.Enabled, s.Origin, s.Contains); err != nil {
			return err
		}
		if err := audit(ctx, tx, a); err != nil {
			return err
		}
		m, err = getMonitor(ctx, tx, projectID)
		return err
	})
	return m, err
}

// ListEnabledMonitors lists the enabled monitors of live (not archived)
// projects, by project id.
func (d *DB) ListEnabledMonitors(ctx context.Context) ([]store.Monitor, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT m.project_id, m.enabled, m.origin, m.contains,
		m.cert_expires_at, m.cert_issuer, m.cert_valid, m.cert_error, m.cert_checked_at,
		m.domain_expires_at, m.domain_checked_at, m.redirects,
		m.check_requested_at, m.check_finished_at, m.check_up, m.check_status, m.check_reason, m.check_error, m.check_ms
		FROM monitors m JOIN projects p ON p.id = m.project_id
		WHERE m.enabled = 1 AND p.archived_at IS NULL ORDER BY m.project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMonitorCert stores a certificate reading unless the row's origin is no
// longer origin (the check raced an edit). Not audited.
func (d *DB) SetMonitorCert(ctx context.Context, projectID int64, origin string, c store.CertState) error {
	_, err := d.db.ExecContext(ctx, `UPDATE monitors SET cert_expires_at=?, cert_issuer=?, cert_valid=?,
		cert_error=?, cert_checked_at=? WHERE project_id=? AND origin=?`,
		c.ExpiresAt, c.Issuer, c.Valid, c.Error, c.CheckedAt, projectID, origin)
	return err
}

// SetMonitorCertError stores why a certificate could not be read, keeping
// the last reading and its check time, guarded like SetMonitorCert.
func (d *DB) SetMonitorCertError(ctx context.Context, projectID int64, origin, msg string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE monitors SET cert_error=? WHERE project_id=? AND origin=?`,
		msg, projectID, origin)
	return err
}

// SetMonitorDomain stores a domain-expiry reading, guarded like SetMonitorCert.
func (d *DB) SetMonitorDomain(ctx context.Context, projectID int64, origin string, s store.DomainState) error {
	_, err := d.db.ExecContext(ctx, `UPDATE monitors SET domain_expires_at=?, domain_checked_at=?
		WHERE project_id=? AND origin=?`, s.ExpiresAt, s.CheckedAt, projectID, origin)
	return err
}

// SetMonitorRedirects stores the redirect count of the last check that got
// an answer, guarded like SetMonitorCert.
func (d *DB) SetMonitorRedirects(ctx context.Context, projectID int64, origin string, n int) error {
	_, err := d.db.ExecContext(ctx, `UPDATE monitors SET redirects=? WHERE project_id=? AND origin=?`,
		n, projectID, origin)
	return err
}

// RequestMonitorCheck records a manual check for the runner to serve and
// audits it. The write itself refuses a monitor not enabled and one with a
// request pending, so two callers cannot both pass.
func (d *DB) RequestMonitorCheck(ctx context.Context, projectID int64, at string, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE monitors SET check_requested_at=?
			WHERE project_id=? AND enabled=1 AND check_requested_at <= check_finished_at`, at, projectID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 1 {
			return audit(ctx, tx, a)
		}
		m, err := getMonitor(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if !m.Enabled {
			return store.Refuse(store.ErrInvalid, "project %d's monitor is not on", projectID)
		}
		return store.Refuse(store.ErrBusy, "a check of project %d is already running", projectID)
	})
}

// FinishMonitorCheck stores how a manual check ended, guarded like
// SetMonitorCert. Not audited.
func (d *DB) FinishMonitorCheck(ctx context.Context, projectID int64, origin string, r store.MonitorCheckResult) error {
	_, err := d.db.ExecContext(ctx, `UPDATE monitors SET check_finished_at=?, check_up=?, check_status=?,
		check_reason=?, check_error=?, check_ms=? WHERE project_id=? AND origin=?`,
		r.FinishedAt, r.Up, r.Status, r.Reason, r.Error, r.MS, projectID, origin)
	return err
}
