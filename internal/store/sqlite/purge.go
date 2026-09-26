// Purge of archived projects, dashboards and widgets past
// RETENTION_ARCHIVED_DAYS (spec 2026-09-26, migration 021 onward). Run by
// the daily pass (internal/jobs), never by a request handler: there is no
// tool or route for it.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// PurgeArchived deletes every project, dashboard and widget archived more
// than days ago, each in its own transaction with an audit row (actor
// "retention"). days <= 0 purges nothing.
//
// A project's archived_at is written with datetime('now') ("YYYY-MM-DD
// HH:MM:SS"); dashboards' and widgets' with strftime(...,'Z') (RFC3339).
// julianday() parses both, so one predicate shape serves every kind.
//
// Dashboards are restricted to owner='user': a system dashboard is never
// archived (it is release-managed, via SyncReporting), so it must never be
// selected here even if a row were forced into that state. Widgets are
// selected only when their own dashboard is not itself being purged in
// this same pass — a purged dashboard takes its widgets by cascade
// (ON DELETE CASCADE), so purging them again here would be redundant, not
// wrong, but the audit trail would then carry two rows for one deletion.
func (d *DB) PurgeArchived(ctx context.Context, days int) (store.PurgeResult, error) {
	var res store.PurgeResult
	if days <= 0 {
		return res, nil
	}

	projectIDs, err := d.purgeableIDs(ctx,
		`SELECT id FROM projects
		 WHERE archived_at IS NOT NULL AND julianday(archived_at) < julianday('now') - ?`, days)
	if err != nil {
		return res, fmt.Errorf("select archived projects: %w", err)
	}
	for _, id := range projectIDs {
		if err := d.tx(ctx, func(tx *sql.Tx) error {
			if err := deleteProject(ctx, tx, id); err != nil {
				return err
			}
			return auditAndBump(ctx, tx, store.AuditEntry{
				Actor: "retention", Action: "project.purge", Subject: strconv.FormatInt(id, 10)})
		}); err != nil {
			return res, fmt.Errorf("purge project %d: %w", id, err)
		}
		res.Projects = append(res.Projects, id)
	}

	dashboardIDs, err := d.purgeableIDs(ctx,
		`SELECT id FROM dashboards
		 WHERE owner='user' AND archived_at IS NOT NULL
		   AND julianday(archived_at) < julianday('now') - ?`, days)
	if err != nil {
		return res, fmt.Errorf("select archived dashboards: %w", err)
	}
	for _, id := range dashboardIDs {
		if err := d.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM dashboards WHERE id=?`, id); err != nil {
				return fmt.Errorf("delete dashboard %d: %w", id, err)
			}
			return audit(ctx, tx, store.AuditEntry{
				Actor: "retention", Action: "dashboard.purge", Subject: fmt.Sprintf("dashboard/%d", id)})
		}); err != nil {
			return res, fmt.Errorf("purge dashboard %d: %w", id, err)
		}
		res.Dashboards = append(res.Dashboards, id)
	}

	widgetIDs, err := d.purgeableIDs(ctx,
		`SELECT w.id FROM widgets w
		 WHERE w.archived_at IS NOT NULL AND julianday(w.archived_at) < julianday('now') - ?
		   AND w.dashboard_id NOT IN (
		       SELECT id FROM dashboards
		       WHERE owner='user' AND archived_at IS NOT NULL
		         AND julianday(archived_at) < julianday('now') - ?)`, days, days)
	if err != nil {
		return res, fmt.Errorf("select archived widgets: %w", err)
	}
	for _, id := range widgetIDs {
		if err := d.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM widgets WHERE id=?`, id); err != nil {
				return fmt.Errorf("delete widget %d: %w", id, err)
			}
			return audit(ctx, tx, store.AuditEntry{
				Actor: "retention", Action: "widget.purge", Subject: fmt.Sprintf("widget/%d", id)})
		}); err != nil {
			return res, fmt.Errorf("purge widget %d: %w", id, err)
		}
		res.Widgets = append(res.Widgets, id)
	}

	return res, nil
}

// purgeableIDs runs a SELECT of a single int64 column outside any
// transaction: the deletes that follow each open and commit their own.
func (d *DB) purgeableIDs(ctx context.Context, q string, args ...any) ([]int64, error) {
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
