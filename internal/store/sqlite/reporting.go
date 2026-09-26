// Reporting row access (migration 021): components, dashboards and
// widgets. Reporting writes use audit, not auditAndBump: they are not
// managed-config, so they never bump meta.config_version or ask other
// processes to reload the project registry.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// audit writes one audit_log row without bumping meta.config_version, the
// sibling of auditAndBump (registry.go) for writes that are not
// managed-config.
func audit(ctx context.Context, tx *sql.Tx, a store.AuditEntry) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO audit_log (actor, action, subject, detail) VALUES (?,?,?,?)`,
		a.Actor, a.Action, a.Subject, a.Detail)
	return err
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, so one scan
// helper serves a single-row Get and a multi-row List.
type rowScanner interface{ Scan(dest ...any) error }

const dashboardCols = `d.id, d.owner, d.title, d.sort_key, COALESCE(d.last_project_id,0),
	COALESCE(d.last_range,''), COALESCE(d.last_from,''), COALESCE(d.last_to,''),
	d.created_at, d.updated_at, COALESCE(d.archived_at,''),
	(SELECT COUNT(*) FROM widgets w WHERE w.dashboard_id=d.id AND w.archived_at IS NULL)`

func scanDashboard(s rowScanner) (store.Dashboard, error) {
	var d store.Dashboard
	err := s.Scan(&d.ID, &d.Owner, &d.Title, &d.SortKey, &d.LastProjectID,
		&d.LastRange, &d.LastFrom, &d.LastTo, &d.CreatedAt, &d.UpdatedAt,
		&d.ArchivedAt, &d.LiveWidgets)
	return d, err
}

const widgetCols = `id, dashboard_id, COALESCE(component,''), sort_key, width, height,
	name, title, props, source_type, source, created_at, updated_at, COALESCE(archived_at,'')`

func scanWidget(s rowScanner) (store.Widget, error) {
	var w store.Widget
	err := s.Scan(&w.ID, &w.DashboardID, &w.Component, &w.SortKey, &w.Width, &w.Height,
		&w.Name, &w.Title, &w.Props, &w.SourceType, &w.Source,
		&w.CreatedAt, &w.UpdatedAt, &w.ArchivedAt)
	return w, err
}

// ListComponents lists every registered component, by name.
func (d *DB) ListComponents(ctx context.Context) ([]store.Component, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT name, description, accepts, inputs, props,
		default_width, default_height FROM components ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Component
	for rows.Next() {
		var c store.Component
		if err := rows.Scan(&c.Name, &c.Description, &c.Accepts, &c.Inputs, &c.Props,
			&c.DefaultWidth, &c.DefaultHeight); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListDashboards lists every dashboard, the system group first (each group
// ordered by sort_key): `owner='user'` is 0 for the system rows and 1 for
// the user rows, so ordering by it ascending puts system first.
func (d *DB) ListDashboards(ctx context.Context) ([]store.Dashboard, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+dashboardCols+` FROM dashboards d ORDER BY d.owner='user', d.sort_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Dashboard
	for rows.Next() {
		dash, err := scanDashboard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dash)
	}
	return out, rows.Err()
}

func (d *DB) GetDashboard(ctx context.Context, id int64) (store.Dashboard, error) {
	row := d.db.QueryRowContext(ctx, `SELECT `+dashboardCols+` FROM dashboards d WHERE d.id=?`, id)
	dash, err := scanDashboard(row)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Dashboard{}, store.Refuse(store.ErrNotFound, "dashboard %d: not found", id)
	}
	return dash, err
}

// ListWidgets lists a dashboard's widgets by sort_key, archived ones
// included; dashboardID 0 lists every widget, by dashboard_id then
// sort_key.
func (d *DB) ListWidgets(ctx context.Context, dashboardID int64) ([]store.Widget, error) {
	q := `SELECT ` + widgetCols + ` FROM widgets`
	var args []any
	if dashboardID != 0 {
		q += ` WHERE dashboard_id=? ORDER BY sort_key`
		args = append(args, dashboardID)
	} else {
		q += ` ORDER BY dashboard_id, sort_key`
	}
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Widget
	for rows.Next() {
		w, err := scanWidget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (d *DB) GetWidget(ctx context.Context, id int64) (store.Widget, error) {
	row := d.db.QueryRowContext(ctx, `SELECT `+widgetCols+` FROM widgets WHERE id=?`, id)
	w, err := scanWidget(row)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Widget{}, store.Refuse(store.ErrNotFound, "widget %d: not found", id)
	}
	return w, err
}

// InsertDashboard inserts a dashboard and its widgets in one transaction,
// the widgets getting DashboardID set from the id the dashboard gets. a's
// Subject becomes "dashboard/<id>"; the widgets carry no audit entries of
// their own (InsertWidget is what audits a widget added later).
func (d *DB) InsertDashboard(ctx context.Context, dash store.Dashboard, ws []store.Widget, a store.AuditEntry) (int64, error) {
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if id, err = insertDashboardRow(ctx, tx, dash); err != nil {
			return err
		}
		for _, w := range ws {
			w.DashboardID = id
			if _, err := insertWidgetRow(ctx, tx, w); err != nil {
				return err
			}
		}
		a.Subject = fmt.Sprintf("dashboard/%d", id)
		return audit(ctx, tx, a)
	})
	return id, err
}

func insertDashboardRow(ctx context.Context, tx *sql.Tx, dash store.Dashboard) (int64, error) {
	var res sql.Result
	var err error
	if dash.ID != 0 {
		res, err = tx.ExecContext(ctx, `INSERT INTO dashboards
			(id, owner, title, sort_key, last_project_id, last_range, last_from, last_to)
			VALUES (?,?,?,?,NULLIF(?,0),?,?,?)`,
			dash.ID, dash.Owner, dash.Title, dash.SortKey, dash.LastProjectID,
			dash.LastRange, dash.LastFrom, dash.LastTo)
	} else {
		res, err = tx.ExecContext(ctx, `INSERT INTO dashboards
			(owner, title, sort_key, last_project_id, last_range, last_from, last_to)
			VALUES (?,?,?,NULLIF(?,0),?,?,?)`,
			dash.Owner, dash.Title, dash.SortKey, dash.LastProjectID,
			dash.LastRange, dash.LastFrom, dash.LastTo)
	}
	if err != nil {
		return 0, mapDashboardConflict(dash, err)
	}
	if dash.ID != 0 {
		return dash.ID, nil
	}
	return res.LastInsertId()
}

func mapDashboardConflict(dash store.Dashboard, err error) error {
	if strings.Contains(err.Error(), "UNIQUE constraint failed: dashboards.") {
		return store.Refuse(store.ErrConflict,
			"dashboard: sort key %q already used for owner %q", dash.SortKey, dash.Owner)
	}
	return fmt.Errorf("insert dashboard %q: %w", dash.Title, err)
}

// UpdateDashboard updates the two editable columns: title and sort_key.
func (d *DB) UpdateDashboard(ctx context.Context, dash store.Dashboard, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE dashboards SET title=?, sort_key=?, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
			 WHERE id=?`, dash.Title, dash.SortKey, dash.ID)
		if err != nil {
			return mapDashboardConflict(dash, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.Refuse(store.ErrNotFound, "update dashboard: unknown id %d", dash.ID)
		}
		a.Subject = fmt.Sprintf("dashboard/%d", dash.ID)
		return audit(ctx, tx, a)
	})
}

// SetDashboardView stores the viewer's last-selection columns. It writes
// no audit row: a viewer switching project or date range is not an
// auditable configuration change.
func (d *DB) SetDashboardView(ctx context.Context, dash store.Dashboard) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE dashboards SET last_project_id=NULLIF(?,0), last_range=?, last_from=?, last_to=?,
			 updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id=?`,
			dash.LastProjectID, dash.LastRange, dash.LastFrom, dash.LastTo, dash.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.Refuse(store.ErrNotFound, "set dashboard view: unknown id %d", dash.ID)
		}
		return nil
	})
}

// SetDashboardArchived sets archived_at only when it is currently NULL
// (idempotent archive), or clears it (idempotent restore); an unknown id
// is refused either way. Mirrors SetProjectArchived's shape.
func (d *DB) SetDashboardArchived(ctx context.Context, id int64, archived bool, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		q := `UPDATE dashboards SET archived_at=strftime('%Y-%m-%dT%H:%M:%SZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id=? AND archived_at IS NULL`
		if !archived {
			q = `UPDATE dashboards SET archived_at=NULL,
				updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id=?`
		}
		res, err := tx.ExecContext(ctx, q, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var exists int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM dashboards WHERE id=?`, id).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return store.Refuse(store.ErrNotFound, "dashboard %d: not found", id)
			}
			// already in the requested state: idempotent no-op, still audited.
		}
		a.Subject = fmt.Sprintf("dashboard/%d", id)
		return audit(ctx, tx, a)
	})
}

// InsertWidget inserts one widget onto an existing dashboard. a's Subject
// becomes "widget/<id>".
func (d *DB) InsertWidget(ctx context.Context, w store.Widget, a store.AuditEntry) (int64, error) {
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if id, err = insertWidgetRow(ctx, tx, w); err != nil {
			return err
		}
		a.Subject = fmt.Sprintf("widget/%d", id)
		return audit(ctx, tx, a)
	})
	return id, err
}

func insertWidgetRow(ctx context.Context, tx *sql.Tx, w store.Widget) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO widgets
		(dashboard_id, component, sort_key, width, height, name, title, props, source_type, source)
		VALUES (?,NULLIF(?,''),?,?,?,?,?,?,?,?)`,
		w.DashboardID, w.Component, w.SortKey, w.Width, w.Height,
		w.Name, w.Title, w.Props, w.SourceType, w.Source)
	if err != nil {
		return 0, mapWidgetConflict(w, err)
	}
	return res.LastInsertId()
}

func mapWidgetConflict(w store.Widget, err error) error {
	if strings.Contains(err.Error(), "UNIQUE constraint failed: widgets.") {
		return store.Refuse(store.ErrConflict,
			"widget: name %q or sort key %q already used on dashboard %d", w.Name, w.SortKey, w.DashboardID)
	}
	return fmt.Errorf("insert widget %q: %w", w.Name, err)
}

// UpdateWidget updates every editable column (not dashboard_id: a widget
// does not move between dashboards).
func (d *DB) UpdateWidget(ctx context.Context, w store.Widget, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE widgets SET component=NULLIF(?,''), sort_key=?, width=?, height=?,
			 name=?, title=?, props=?, source_type=?, source=?,
			 updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
			 WHERE id=?`,
			w.Component, w.SortKey, w.Width, w.Height, w.Name, w.Title, w.Props,
			w.SourceType, w.Source, w.ID)
		if err != nil {
			return mapWidgetConflict(w, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.Refuse(store.ErrNotFound, "update widget: unknown id %d", w.ID)
		}
		a.Subject = fmt.Sprintf("widget/%d", w.ID)
		return audit(ctx, tx, a)
	})
}

// SetWidgetArchived is SetDashboardArchived's twin for widgets.
func (d *DB) SetWidgetArchived(ctx context.Context, id int64, archived bool, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		q := `UPDATE widgets SET archived_at=strftime('%Y-%m-%dT%H:%M:%SZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id=? AND archived_at IS NULL`
		if !archived {
			q = `UPDATE widgets SET archived_at=NULL,
				updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id=?`
		}
		res, err := tx.ExecContext(ctx, q, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var exists int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM widgets WHERE id=?`, id).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return store.Refuse(store.ErrNotFound, "widget %d: not found", id)
			}
		}
		a.Subject = fmt.Sprintf("widget/%d", id)
		return audit(ctx, tx, a)
	})
}
