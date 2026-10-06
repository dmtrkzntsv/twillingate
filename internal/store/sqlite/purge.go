// Purge of archived projects, dashboards, widgets and widget shares past
// RETENTION_ARCHIVED_DAYS (spec 2026-09-25, migration 021 onward; shares
// from migration 032, spec 2026-10-05). Run by
// the daily pass (internal/jobs), never by a request handler: there is no
// tool or route for it.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// PurgeArchived deletes every project, dashboard, widget and widget share
// archived more than days ago, each in its own transaction with an audit
// row (actor "retention"). days <= 0 purges nothing. A share purged with
// its project (deleteProject) gets no widget_share.purge row of its own.
//
// A project's archived_at is written with datetime('now') ("YYYY-MM-DD
// HH:MM:SS"); dashboards', widgets' and shares' with strftime(...,'Z')
// (RFC3339).
// julianday() parses both, so one predicate shape serves every kind.
//
// Dashboards (and, transitively, widgets) are restricted to owner='user':
// a system dashboard is never archived (it is release-managed, via
// SyncReporting), so it — and anything on it — must never be selected here
// even if a row were forced into that state. Widgets are further
// restricted to those whose own dashboard is not itself being purged in
// this same pass — a purged dashboard takes its widgets by cascade
// (ON DELETE CASCADE), so purging them again here would be redundant, not
// wrong, but the audit trail would then carry two rows for one deletion.
//
// One item's failure does not stop the rest: the store has no logger to
// report it through (that belongs to the caller, internal/jobs), so a
// failed item's error is joined into the one returned, but every other
// item is still attempted and, on success, still counted in the result.
func (d *DB) PurgeArchived(ctx context.Context, days int) (store.PurgeResult, error) {
	var res store.PurgeResult
	if days <= 0 {
		return res, nil
	}
	var errs error

	projectIDs, err := d.purgeableIDs(ctx,
		`SELECT id FROM projects
		 WHERE archived_at IS NOT NULL AND julianday(archived_at) < julianday('now') - ?`, days)
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("select archived projects: %w", err))
	}
	for _, id := range projectIDs {
		if err := d.tx(ctx, func(tx *sql.Tx) error {
			if err := deleteProject(ctx, tx, id); err != nil {
				return err
			}
			return auditAndBump(ctx, tx, store.AuditEntry{
				Actor: "retention", Action: "project.purge", Subject: strconv.FormatInt(id, 10)})
		}); err != nil {
			errs = errors.Join(errs, fmt.Errorf("purge project %d: %w", id, err))
			continue
		}
		res.Projects = append(res.Projects, id)
	}

	dashboardIDs, err := d.purgeableIDs(ctx,
		`SELECT id FROM dashboards
		 WHERE owner='user' AND archived_at IS NOT NULL
		   AND julianday(archived_at) < julianday('now') - ?`, days)
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("select archived dashboards: %w", err))
	}
	for _, id := range dashboardIDs {
		if err := d.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM dashboards WHERE id=?`, id); err != nil {
				return fmt.Errorf("delete dashboard %d: %w", id, err)
			}
			return audit(ctx, tx, store.AuditEntry{
				Actor: "retention", Action: "dashboard.purge", Subject: fmt.Sprintf("dashboard/%d", id)})
		}); err != nil {
			errs = errors.Join(errs, fmt.Errorf("purge dashboard %d: %w", id, err))
			continue
		}
		res.Dashboards = append(res.Dashboards, id)
	}

	// The join with dashboards restricts every widget considered here to
	// one on a user-owned dashboard (defense in depth: production code
	// never archives a widget on a system dashboard either, but a forced
	// row must still never be selected), and d's own archived state tells
	// whether that dashboard is being purged in this same pass.
	widgetIDs, err := d.purgeableIDs(ctx,
		`SELECT w.id FROM widgets w JOIN dashboards d ON d.id = w.dashboard_id
		 WHERE d.owner='user'
		   AND w.archived_at IS NOT NULL AND julianday(w.archived_at) < julianday('now') - ?
		   AND NOT (d.archived_at IS NOT NULL AND julianday(d.archived_at) < julianday('now') - ?)`,
		days, days)
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("select archived widgets: %w", err))
	}
	for _, id := range widgetIDs {
		if err := d.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM widgets WHERE id=?`, id); err != nil {
				return fmt.Errorf("delete widget %d: %w", id, err)
			}
			return audit(ctx, tx, store.AuditEntry{
				Actor: "retention", Action: "widget.purge", Subject: fmt.Sprintf("widget/%d", id)})
		}); err != nil {
			errs = errors.Join(errs, fmt.Errorf("purge widget %d: %w", id, err))
			continue
		}
		res.Widgets = append(res.Widgets, id)
	}

	shareIDs, err := d.purgeableStrings(ctx,
		`SELECT id FROM widget_shares
		 WHERE archived_at IS NOT NULL AND julianday(archived_at) < julianday('now') - ?`, days)
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("select archived widget shares: %w", err))
	}
	for _, id := range shareIDs {
		if err := d.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM widget_shares WHERE id=?`, id); err != nil {
				return fmt.Errorf("delete widget share %s: %w", id, err)
			}
			return audit(ctx, tx, store.AuditEntry{
				Actor: "retention", Action: "widget_share.purge", Subject: "widget_share/" + id})
		}); err != nil {
			errs = errors.Join(errs, fmt.Errorf("purge widget share %s: %w", id, err))
			continue
		}
		res.WidgetShares = append(res.WidgetShares, id)
	}

	return res, errs
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

// purgeableStrings is purgeableIDs for a single string column.
func (d *DB) purgeableStrings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
