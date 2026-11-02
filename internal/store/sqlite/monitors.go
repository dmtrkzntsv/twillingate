package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/maindomain"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const monitorCols = `project_id, origin, enabled, contains, cert_expires_at, cert_issuer,
	cert_valid, cert_error, cert_checked_at, redirects,
	check_requested_at, check_finished_at, last_checked_at, last_up, last_status, last_reason, last_error, last_ms`

func scanMonitor(s rowScanner) (store.Monitor, error) {
	var m store.Monitor
	err := s.Scan(&m.ProjectID, &m.Origin, &m.Enabled, &m.Contains, &m.CertExpiresAt, &m.CertIssuer,
		&m.CertValid, &m.CertError, &m.CertCheckedAt, &m.Redirects,
		&m.CheckRequestedAt, &m.CheckFinishedAt, &m.Last.CheckedAt, &m.Last.Up, &m.Last.Status, &m.Last.Reason, &m.Last.Error, &m.Last.MS)
	return m, err
}

func scanMonitors(rows *sql.Rows) ([]store.Monitor, error) {
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

// ListMonitors lists the project's monitor rows, by origin.
func (d *DB) ListMonitors(ctx context.Context, projectID int64) ([]store.Monitor, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+monitorCols+` FROM monitors WHERE project_id=? ORDER BY origin`, projectID)
	if err != nil {
		return nil, err
	}
	return scanMonitors(rows)
}

// SetMonitor upserts a monitor's settings and audits it. Enabling a disabled
// monitor clears its certificate's check time only, so the certificate is read
// again and the known date stays until then. It leaves the domain reading alone:
// other monitors share it. It is not managed-config: no version bump.
func (d *DB) SetMonitor(ctx context.Context, projectID int64, origin string, s store.MonitorSettings, eligible func([]string) []string, a store.AuditEntry) (store.Monitor, error) {
	var m store.Monitor
	err := d.tx(ctx, func(tx *sql.Tx) error {
		ok, err := projectExists(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if !ok {
			return unknownProject(projectID)
		}
		cur, err := scanMonitor(tx.QueryRowContext(ctx,
			`SELECT `+monitorCols+` FROM monitors WHERE project_id=? AND origin=?`, projectID, origin))
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !exists {
			cur = store.Monitor{ProjectID: projectID, Origin: origin}
		}
		next := store.MonitorSettings{Enabled: &cur.Enabled, Contains: &cur.Contains}
		if s.Enabled != nil {
			next.Enabled = s.Enabled
		}
		if s.Contains != nil {
			next.Contains = s.Contains
		}
		changed := []string{"origin=" + origin}
		if *next.Enabled != cur.Enabled {
			changed = append(changed, fmt.Sprintf("enabled=%t", *next.Enabled))
		}
		if *next.Contains != cur.Contains {
			changed = append(changed, "contains")
		}
		if len(changed) == 1 {
			m = cur
			return nil
		}
		// Checked here, in the write, against the origins as they stand: an
		// origins edit may have committed since the caller looked, and the
		// upsert would bring back the row it deleted. Turning off a monitor
		// that has a row stays allowed.
		if *next.Enabled || !exists {
			var raw string
			if err := tx.QueryRowContext(ctx, `SELECT allowed_origins FROM projects WHERE id=?`, projectID).Scan(&raw); err != nil {
				return err
			}
			var allowed []string
			if err := json.Unmarshal([]byte(raw), &allowed); err != nil {
				return fmt.Errorf("project %d: allowed_origins: %w", projectID, err)
			}
			if !slices.Contains(eligible(allowed), origin) {
				return store.Refuse(store.ErrInvalid, "%s is not an origin a monitor of project %d may use", origin, projectID)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO monitors (project_id, origin, enabled, contains)
			VALUES (?,?,?,?)
			ON CONFLICT (project_id, origin) DO UPDATE SET
			  cert_checked_at = CASE WHEN enabled = 1 OR excluded.enabled = 0 THEN cert_checked_at ELSE '' END,
			  enabled = excluded.enabled, contains = excluded.contains,
			  updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')`,
			projectID, origin, *next.Enabled, *next.Contains); err != nil {
			return err
		}
		if !*next.Enabled {
			if err := finishPendingChecks(ctx, tx, projectID, origin); err != nil {
				return err
			}
		}
		a.Detail = strings.Join(changed, " ")
		if err := audit(ctx, tx, a); err != nil {
			return err
		}
		m, err = scanMonitor(tx.QueryRowContext(ctx,
			`SELECT `+monitorCols+` FROM monitors WHERE project_id=? AND origin=?`, projectID, origin))
		return err
	})
	return m, err
}

// finishPendingChecks ends the manual check pending on the project's monitor
// origin (every monitor of the project when origin is empty) as one that did
// not run: the runner only serves enabled monitors of live projects, so a
// monitor turned off or a project archived would leave the request pending for
// good, and a client polling for its end would never stop.
func finishPendingChecks(ctx context.Context, tx *sql.Tx, projectID int64, origin string) error {
	_, err := tx.ExecContext(ctx, `UPDATE monitors SET
		check_finished_at = MAX(strftime('%Y-%m-%dT%H:%M:%SZ','now'), check_requested_at)
		WHERE project_id=? AND (?='' OR origin=?)
		AND check_requested_at != '' AND (check_finished_at = '' OR check_finished_at < check_requested_at)`,
		projectID, origin, origin)
	return err
}

// ListEnabledMonitors lists the enabled monitors of live (not archived)
// projects, by project id and then origin.
func (d *DB) ListEnabledMonitors(ctx context.Context) ([]store.Monitor, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT m.project_id, m.origin, m.enabled, m.contains,
		m.cert_expires_at, m.cert_issuer, m.cert_valid, m.cert_error, m.cert_checked_at, m.redirects,
		m.check_requested_at, m.check_finished_at, m.last_checked_at, m.last_up, m.last_status, m.last_reason, m.last_error, m.last_ms
		FROM monitors m JOIN projects p ON p.id = m.project_id
		WHERE m.enabled = 1 AND p.archived_at IS NULL ORDER BY m.project_id, m.origin`)
	if err != nil {
		return nil, err
	}
	return scanMonitors(rows)
}

// SetMonitorCert stores a certificate reading. A monitor no longer there (the
// check raced an origin's removal) is left out. Not audited.
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

// SetMonitorRedirects stores the redirect count of the last check that got
// an answer, guarded like SetMonitorCert. It writes only when the stored
// count differs, so a caller may call it on every answered check.
func (d *DB) SetMonitorRedirects(ctx context.Context, projectID int64, origin string, n int) error {
	_, err := d.db.ExecContext(ctx, `UPDATE monitors SET redirects=? WHERE project_id=? AND origin=? AND redirects<>?`,
		n, projectID, origin, n)
	return err
}

// SetMonitorLast stores the result of an availability check, scheduled or manual,
// guarded like SetMonitorCert: a check that raced its origin's removal leaves no
// row behind. Not audited.
func (d *DB) SetMonitorLast(ctx context.Context, projectID int64, origin string, r store.MonitorResult) error {
	_, err := d.db.ExecContext(ctx, `UPDATE monitors SET last_checked_at=?, last_up=?, last_status=?,
		last_reason=?, last_error=?, last_ms=? WHERE project_id=? AND origin=?`,
		r.CheckedAt, r.Up, r.Status, r.Reason, r.Error, r.MS, projectID, origin)
	return err
}

// ListMonitorDomains lists the project's domain readings, by domain.
func (d *DB) ListMonitorDomains(ctx context.Context, projectID int64) ([]store.MonitorDomain, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT project_id, domain, expires_at, checked_at
		FROM monitor_domains WHERE project_id=? ORDER BY domain`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.MonitorDomain
	for rows.Next() {
		var m store.MonitorDomain
		if err := rows.Scan(&m.ProjectID, &m.Domain, &m.ExpiresAt, &m.CheckedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// monitorDomainsInUse is the main domains of the project's monitor rows.
func monitorDomainsInUse(ctx context.Context, tx *sql.Tx, projectID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT origin FROM monitors WHERE project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var origin string
		if err := rows.Scan(&origin); err != nil {
			return nil, err
		}
		out = append(out, maindomain.OfOrigin(origin))
	}
	return out, rows.Err()
}

// SetMonitorDomain stores a domain reading. One no monitor of the project uses
// any more (the lookup raced an origin's removal) is left out, so that it does
// not come back. Not audited.
func (d *DB) SetMonitorDomain(ctx context.Context, projectID int64, domain string, s store.DomainState) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		used, err := monitorDomainsInUse(ctx, tx, projectID)
		if err != nil || !slices.Contains(used, domain) {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO monitor_domains (project_id, domain, expires_at, checked_at)
			VALUES (?,?,?,?)
			ON CONFLICT (project_id, domain) DO UPDATE SET expires_at = excluded.expires_at, checked_at = excluded.checked_at`,
			projectID, domain, s.ExpiresAt, s.CheckedAt)
		return err
	})
}

// removeMonitorsOffOrigins deletes the project's monitors whose origin is not
// among eligible, each audited, and the domain readings no monitor left uses.
func removeMonitorsOffOrigins(ctx context.Context, tx *sql.Tx, projectID int64, eligible []string, a store.AuditEntry) error {
	removed, err := deleteMonitorsOffOrigins(ctx, tx, projectID, eligible)
	if err != nil {
		return err
	}
	for _, origin := range removed {
		if err := audit(ctx, tx, store.AuditEntry{Actor: a.Actor, Action: "monitor.remove",
			Subject: a.Subject, Detail: "origin removed: " + origin}); err != nil {
			return err
		}
	}
	used, err := monitorDomainsInUse(ctx, tx, projectID)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT domain FROM monitor_domains WHERE project_id=?`, projectID)
	if err != nil {
		return err
	}
	var unused []string
	for rows.Next() {
		var domain string
		if err := rows.Scan(&domain); err != nil {
			rows.Close()
			return err
		}
		if !slices.Contains(used, domain) {
			unused = append(unused, domain)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, domain := range unused {
		if _, err := tx.ExecContext(ctx, `DELETE FROM monitor_domains WHERE project_id=? AND domain=?`, projectID, domain); err != nil {
			return err
		}
	}
	return nil
}

// deleteMonitorsOffOrigins deletes the project's monitors whose origin is not
// among eligible and returns their origins.
func deleteMonitorsOffOrigins(ctx context.Context, tx *sql.Tx, projectID int64, eligible []string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `DELETE FROM monitors
		WHERE project_id=? AND origin NOT IN (`+placeholders(len(eligible))+`) RETURNING origin`,
		append([]any{projectID}, toArgs(eligible)...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var removed []string
	for rows.Next() {
		var origin string
		if err := rows.Scan(&origin); err != nil {
			return nil, err
		}
		removed = append(removed, origin)
	}
	slices.Sort(removed)
	return removed, rows.Err()
}

// RequestMonitorCheck records a manual check of one monitor for the runner to
// serve and audits it, clearing the finish time so the request reads as pending
// even in the second the last check finished. The write itself refuses a monitor not enabled, one
// with a request pending and one requested less than cooldown before at, so
// two callers cannot both pass.
func (d *DB) RequestMonitorCheck(ctx context.Context, projectID int64, origin, at string, cooldown time.Duration, a store.AuditEntry) error {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return fmt.Errorf("request monitor check: at %q: %w", at, err)
	}
	cutoff := t.Add(-cooldown).UTC().Format(time.RFC3339)
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE monitors SET check_requested_at=?, check_finished_at=''
			WHERE project_id=? AND origin=? AND enabled=1
			AND EXISTS (SELECT 1 FROM projects WHERE id = monitors.project_id AND archived_at IS NULL)
			AND (check_requested_at = '' OR (check_finished_at != '' AND check_finished_at >= check_requested_at))
			AND check_requested_at <= ?`, at, projectID, origin, cutoff)
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
		ok, err := projectExists(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if !ok {
			return unknownProject(projectID)
		}
		var archived int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id=? AND archived_at IS NOT NULL`, projectID).Scan(&archived); err != nil {
			return err
		}
		if archived > 0 {
			return store.Refuse(store.ErrInvalid, "project %d is archived; nothing serves a check of %s", projectID, origin)
		}
		m, err := scanMonitor(tx.QueryRowContext(ctx,
			`SELECT `+monitorCols+` FROM monitors WHERE project_id=? AND origin=?`, projectID, origin))
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !m.Enabled) {
			return store.Refuse(store.ErrInvalid, "%s is not monitored in project %d", origin, projectID)
		}
		if err != nil {
			return err
		}
		if m.CheckPending() {
			return store.Refuse(store.ErrBusy, "a check of %s is already running", origin)
		}
		return store.Refuse(store.ErrBusy, "%s was checked moments ago; try again later", origin)
	})
}

// FinishMonitorCheck ends a manual check's pending state, guarded like
// SetMonitorCert, and only for the request it served: requestedAt is the
// request's time, so a late finish of an older request, after the monitor was
// turned off and on and asked again, writes nothing. Its result is SetMonitorLast's.
// Not audited.
func (d *DB) FinishMonitorCheck(ctx context.Context, projectID int64, origin, requestedAt, finishedAt string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE monitors SET check_finished_at=?
		WHERE project_id=? AND origin=? AND check_requested_at=?`,
		finishedAt, projectID, origin, requestedAt)
	return err
}
