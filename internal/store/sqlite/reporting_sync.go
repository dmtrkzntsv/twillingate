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
			return fmt.Errorf("reporting sync: park dashboard sort keys: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE widgets SET sort_key = '~' || id
			 WHERE dashboard_id IN (SELECT id FROM dashboards WHERE owner=?)`, store.OwnerSystem); err != nil {
			return fmt.Errorf("reporting sync: park widget sort keys: %w", err)
		}

		// removedWidgets starts from the ones a dropped dashboard took with
		// it by ON DELETE CASCADE inside syncDashboards, which never reach
		// the per-dashboard syncWidgets loop below.
		removedDashboards, removedWidgets, err := syncDashboards(ctx, tx, s.Dashboards)
		if err != nil {
			return err
		}

		var widgetCount int
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
			return fmt.Errorf("reporting sync: insert reporting_migrations: %w", err)
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
			return 0, fmt.Errorf("reporting sync: component %q: %w", c.Name, err)
		}
	}
	names := make([]string, len(components))
	for i, c := range components {
		names[i] = c.Name
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM components WHERE name NOT IN (`+placeholders(len(names))+`)`, toArgs(names)...)
	if err != nil {
		return 0, fmt.Errorf("reporting sync: delete dropped components: %w", err)
	}
	removed, err := res.RowsAffected()
	return int(removed), err
}

// syncDashboards upserts every system dashboard by id (title and sort_key
// only; last_range is written on insert alone, so a viewer's later
// SetDashboardView survives a resync), then deletes any system dashboard
// not named in the list. A manifest id that already names a row owned by
// someone other than 'system' is refused outright — the upsert's WHERE
// clause would otherwise silently no-op the update and leave the row
// exactly as a plain INSERT ... ON CONFLICT DO UPDATE ... WHERE false
// does (verified by hand against SQLite: no error, no change), which
// would then let the per-dashboard widget sync attach the release's
// widgets onto that unrelated row.
//
// Deleting a dropped system dashboard cascades to its widgets (ON DELETE
// CASCADE), so those never reach the per-dashboard syncWidgets loop; this
// counts them itself, before the delete, so the caller can fold them into
// the overall removed-widgets count.
//
// Returns (removed dashboards, widgets removed by cascade, error).
func syncDashboards(ctx context.Context, tx *sql.Tx, dashboards []store.SystemDashboard) (int, int, error) {
	var newIDs []int64
	for _, dash := range dashboards {
		var existingOwner string
		err := tx.QueryRowContext(ctx,
			`SELECT owner FROM dashboards WHERE id=?`, dash.ID).Scan(&existingOwner)
		switch {
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return 0, 0, fmt.Errorf("reporting sync: check dashboard %d owner: %w", dash.ID, err)
		case err == nil && existingOwner != store.OwnerSystem:
			return 0, 0, fmt.Errorf(
				"reporting sync: dashboard id %d is owned by %q, not %q",
				dash.ID, existingOwner, store.OwnerSystem)
		}
		if err != nil {
			newIDs = append(newIDs, dash.ID)
		}

		// A manifest dashboard with GroupID 0 is its own group, same as
		// insertDashboardRow's rule. Migration 022's trigger only covers
		// the insert; the ON CONFLICT update would write the 0 as given,
		// so it's resolved here, before the statement (the id is the
		// manifest's own, known up front).
		groupID := dash.GroupID
		if groupID == 0 {
			groupID = dash.ID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO dashboards (id, owner, title, sort_key, group_id, last_range)
			VALUES (?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET title=excluded.title, sort_key=excluded.sort_key,
				group_id=excluded.group_id, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
			WHERE dashboards.owner=?`,
			dash.ID, store.OwnerSystem, dash.Title, dash.SortKey, groupID, dash.Range, store.OwnerSystem,
		); err != nil {
			return 0, 0, fmt.Errorf("reporting sync: system dashboard %d: %w", dash.ID, err)
		}
	}

	// D3: a dashboard a release adds to a group whose pre-existing members
	// are all archived arrives archived itself, rather than resurrecting a
	// group the user archived whole. This runs once, after every upsert
	// above, and judges each new id (one that didn't exist before this
	// sync) by its final group_id: manifest rows arrive sorted by id
	// (internal/reporting/files.go), not grouped by leader, so a new
	// group's leader can have a higher id than a pre-existing member that
	// is joining it, or a lower one than a sibling that already existed —
	// a per-row check during the loop above can't see the whole group.
	// Other rows that are themselves new in this sync never count as
	// "pre-existing members": a brand-new group (leader and all tabs new)
	// always arrives live, even if a new tab happens to be upserted first.
	if len(newIDs) > 0 {
		args := []any{store.OwnerSystem}
		args = append(args, toArgs(newIDs)...)
		args = append(args, store.OwnerSystem)
		args = append(args, toArgs(newIDs)...)
		args = append(args, store.OwnerSystem)
		args = append(args, toArgs(newIDs)...)
		if _, err := tx.ExecContext(ctx, `UPDATE dashboards
			SET archived_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
			WHERE owner=? AND id IN (`+placeholders(len(newIDs))+`)
			  AND EXISTS (SELECT 1 FROM dashboards other WHERE other.group_id=dashboards.group_id
					AND other.owner=? AND other.id<>dashboards.id AND other.id NOT IN (`+placeholders(len(newIDs))+`))
			  AND NOT EXISTS (SELECT 1 FROM dashboards other WHERE other.group_id=dashboards.group_id
					AND other.owner=? AND other.id<>dashboards.id AND other.id NOT IN (`+placeholders(len(newIDs))+`)
					AND other.archived_at IS NULL)`,
			args...); err != nil {
			return 0, 0, fmt.Errorf("reporting sync: archive new dashboards of an archived group: %w", err)
		}
	}

	ids := make([]int64, len(dashboards))
	for i, dash := range dashboards {
		ids[i] = dash.ID
	}

	var cascadedWidgets int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM widgets WHERE dashboard_id IN (
			SELECT id FROM dashboards WHERE owner=? AND id NOT IN (`+placeholders(len(ids))+`))`,
		append([]any{store.OwnerSystem}, toArgs(ids)...)...).Scan(&cascadedWidgets); err != nil {
		return 0, 0, fmt.Errorf("reporting sync: count cascaded widgets: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		`DELETE FROM dashboards WHERE owner=? AND id NOT IN (`+placeholders(len(ids))+`)`,
		append([]any{store.OwnerSystem}, toArgs(ids)...)...)
	if err != nil {
		return 0, 0, fmt.Errorf("reporting sync: delete dropped system dashboards: %w", err)
	}
	removed, err := res.RowsAffected()
	return int(removed), cascadedWidgets, err
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
			return 0, fmt.Errorf("reporting sync: system dashboard %d widget %q: %w", dashboardID, w.Name, err)
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
		return 0, fmt.Errorf("reporting sync: delete dropped widgets on dashboard %d: %w", dashboardID, err)
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
