// Sync of a release's system dashboards and components (spec D23):
// SyncReporting makes components, system dashboards and their widgets
// match a release's manifest in one transaction, recording the sync in
// reporting_migrations and audit_log. ReportingHash reads the recorded
// hash back, so a caller can skip the sync when nothing changed.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// ReportingHash returns the hash of the latest reporting_migrations row,
// "" if none has been recorded yet.
func (d *DB) ReportingHash(ctx context.Context) (string, error) {
	var hash string
	err := d.db.QueryRowContext(ctx,
		`SELECT hash FROM reporting_migrations ORDER BY id DESC LIMIT 1`).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return hash, err
}

// SyncReporting makes components and system dashboards (with their
// widgets) match s in one transaction: components and system dashboards
// missing from s are deleted (a dropped component leaves any widget still
// naming it with Component=="", via ON DELETE SET NULL; a dropped system
// dashboard takes its widgets with it, via ON DELETE CASCADE); listed
// components, dashboards and widgets are upserted. User dashboards and
// their widgets are never touched: every statement here filters on
// owner='system' or joins through a system dashboard.
//
// Dashboard and widget sort keys are parked to '~' || id first (a value
// that sorts after every base-62 key and is unique by id, since ids are
// unique), so re-assigning many rows' keys in one pass — including
// swapping two rows' keys — can never collide with the unique
// (owner, sort_key) / (dashboard_id, sort_key) indexes mid-way.
func (d *DB) SyncReporting(ctx context.Context, s store.ReportingSync) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		removedComponents, err := syncComponents(ctx, tx, s.Components)
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE dashboards SET sort_key = '~' || id WHERE owner=?`, store.OwnerSystem); err != nil {
			return fmt.Errorf("park dashboard sort keys: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE widgets SET sort_key = '~' || id
			 WHERE dashboard_id IN (SELECT id FROM dashboards WHERE owner=?)`, store.OwnerSystem); err != nil {
			return fmt.Errorf("park widget sort keys: %w", err)
		}

		removedDashboards, err := syncDashboards(ctx, tx, s.Dashboards)
		if err != nil {
			return err
		}

		var widgetCount, removedWidgets int
		for _, dash := range s.Dashboards {
			widgetCount += len(dash.Widgets)
			removed, err := syncWidgets(ctx, tx, dash.ID, dash.Widgets)
			if err != nil {
				return err
			}
			removedWidgets += removed
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO reporting_migrations (hash, version) VALUES (?,?)`,
			s.Hash, s.Version); err != nil {
			return fmt.Errorf("insert reporting_migrations: %w", err)
		}

		subject := s.Hash
		if len(subject) > 12 {
			subject = subject[:12]
		}
		return audit(ctx, tx, store.AuditEntry{
			Actor: "release", Action: "reporting.migrate", Subject: subject,
			Detail: fmt.Sprintf(
				"%d components (%d removed), %d system dashboards (%d removed), %d widgets (%d removed)",
				len(s.Components), removedComponents,
				len(s.Dashboards), removedDashboards,
				widgetCount, removedWidgets),
		})
	})
}

// syncComponents upserts every component by name, then deletes any
// component row not named in the list (all of them, if the list is
// empty), returning the number removed.
func syncComponents(ctx context.Context, tx *sql.Tx, components []store.Component) (int, error) {
	for _, c := range components {
		if _, err := tx.ExecContext(ctx, `INSERT INTO components
			(name, description, accepts, inputs, props, default_width, default_height)
			VALUES (?,?,?,?,?,?,?)
			ON CONFLICT(name) DO UPDATE SET description=excluded.description,
				accepts=excluded.accepts, inputs=excluded.inputs, props=excluded.props,
				default_width=excluded.default_width, default_height=excluded.default_height`,
			c.Name, c.Description, c.Accepts, c.Inputs, c.Props, c.DefaultWidth, c.DefaultHeight,
		); err != nil {
			return 0, fmt.Errorf("sync component %q: %w", c.Name, err)
		}
	}
	names := make([]string, len(components))
	for i, c := range components {
		names[i] = c.Name
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM components WHERE name NOT IN (`+placeholders(len(names))+`)`, toArgs(names)...)
	if err != nil {
		return 0, fmt.Errorf("delete dropped components: %w", err)
	}
	removed, err := res.RowsAffected()
	return int(removed), err
}

// syncDashboards upserts every system dashboard by id (title and sort_key
// only; last_range is written on insert alone, so a viewer's later
// SetDashboardView survives a resync), then deletes any system dashboard
// not named in the list (cascading to its widgets), returning the number
// removed. The upsert's WHERE clause keeps it from ever touching a row
// whose owner is not 'system'.
func syncDashboards(ctx context.Context, tx *sql.Tx, dashboards []store.SystemDashboard) (int, error) {
	for _, dash := range dashboards {
		if _, err := tx.ExecContext(ctx, `INSERT INTO dashboards (id, owner, title, sort_key, last_range)
			VALUES (?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET title=excluded.title, sort_key=excluded.sort_key,
				updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
			WHERE dashboards.owner=?`,
			dash.ID, store.OwnerSystem, dash.Title, dash.SortKey, dash.Range, store.OwnerSystem,
		); err != nil {
			return 0, fmt.Errorf("sync dashboard %d: %w", dash.ID, err)
		}
	}
	ids := make([]int64, len(dashboards))
	for i, dash := range dashboards {
		ids[i] = dash.ID
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM dashboards WHERE owner=? AND id NOT IN (`+placeholders(len(ids))+`)`,
		append([]any{store.OwnerSystem}, toArgs(ids)...)...)
	if err != nil {
		return 0, fmt.Errorf("delete dropped system dashboards: %w", err)
	}
	removed, err := res.RowsAffected()
	return int(removed), err
}

// syncWidgets upserts dashboardID's widgets by (dashboard_id, name), then
// deletes any of its widgets not named in the list (all of them, if the
// list is empty), returning the number removed.
func syncWidgets(ctx context.Context, tx *sql.Tx, dashboardID int64, widgets []store.Widget) (int, error) {
	for _, w := range widgets {
		if _, err := tx.ExecContext(ctx, `INSERT INTO widgets
			(dashboard_id, component, sort_key, width, height, name, title, props, source_type, source)
			VALUES (?,NULLIF(?,''),?,?,?,?,?,?,?,?)
			ON CONFLICT(dashboard_id, name) DO UPDATE SET
				component=excluded.component, sort_key=excluded.sort_key,
				width=excluded.width, height=excluded.height, title=excluded.title,
				props=excluded.props, source_type=excluded.source_type, source=excluded.source,
				archived_at=NULL, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')`,
			dashboardID, w.Component, w.SortKey, w.Width, w.Height,
			w.Name, w.Title, w.Props, w.SourceType, w.Source,
		); err != nil {
			return 0, fmt.Errorf("sync widget %q on dashboard %d: %w", w.Name, dashboardID, err)
		}
	}
	names := make([]string, len(widgets))
	for i, w := range widgets {
		names[i] = w.Name
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM widgets WHERE dashboard_id=? AND name NOT IN (`+placeholders(len(names))+`)`,
		append([]any{dashboardID}, toArgs(names)...)...)
	if err != nil {
		return 0, fmt.Errorf("delete dropped widgets on dashboard %d: %w", dashboardID, err)
	}
	removed, err := res.RowsAffected()
	return int(removed), err
}

// placeholders returns n comma-joined "?" placeholders (empty for n==0,
// which reads as "IN ()" — SQLite accepts the empty list, always false,
// so "NOT IN ()" is always true and matches every row).
func placeholders(n int) string {
	if n == 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// toArgs widens a typed slice to the []any ExecContext wants.
func toArgs[T any](vs []T) []any {
	args := make([]any, len(vs))
	for i, v := range vs {
		args[i] = v
	}
	return args
}
