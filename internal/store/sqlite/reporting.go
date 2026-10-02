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

const dashboardCols = `d.id, d.owner, d.title, d.sort_key, d.group_id, COALESCE(d.last_project_id,0),
	COALESCE(d.last_range,''), COALESCE(d.last_from,''), COALESCE(d.last_to,''),
	d.created_at, d.updated_at, COALESCE(d.archived_at,''),
	(SELECT COUNT(*) FROM widgets w WHERE w.dashboard_id=d.id AND w.archived_at IS NULL)`

func scanDashboard(s rowScanner) (store.Dashboard, error) {
	var d store.Dashboard
	err := s.Scan(&d.ID, &d.Owner, &d.Title, &d.SortKey, &d.GroupID, &d.LastProjectID,
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

// insertDashboardRow inserts dash and returns its id. dash.GroupID == 0
// means "a new group: its own id"; the id is not known until the insert,
// so migration 022's dashboards_own_group trigger sets it, in the same
// statement.
func insertDashboardRow(ctx context.Context, tx *sql.Tx, dash store.Dashboard) (int64, error) {
	var res sql.Result
	var err error
	if dash.ID != 0 {
		res, err = tx.ExecContext(ctx, `INSERT INTO dashboards
			(id, owner, title, sort_key, group_id, last_project_id, last_range, last_from, last_to)
			VALUES (?,?,?,?,?,NULLIF(?,0),?,?,?)`,
			dash.ID, dash.Owner, dash.Title, dash.SortKey, dash.GroupID, dash.LastProjectID,
			dash.LastRange, dash.LastFrom, dash.LastTo)
	} else {
		res, err = tx.ExecContext(ctx, `INSERT INTO dashboards
			(owner, title, sort_key, group_id, last_project_id, last_range, last_from, last_to)
			VALUES (?,?,?,?,NULLIF(?,0),?,?,?)`,
			dash.Owner, dash.Title, dash.SortKey, dash.GroupID, dash.LastProjectID,
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
	// An explicit id (dash.ID != 0) can collide on the primary key, which
	// SQLite reports as a UNIQUE failure too.
	if strings.Contains(err.Error(), "UNIQUE constraint failed: dashboards.id") {
		return store.Refuse(store.ErrConflict, "dashboard: id %d already exists", dash.ID)
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed: dashboards.") {
		return store.Refuse(store.ErrConflict,
			"dashboard: sort key %q already used for owner %q", dash.SortKey, dash.Owner)
	}
	return fmt.Errorf("insert dashboard %q: %w", dash.Title, err)
}

// mapMoveDashboardConflict is mapDashboardConflict's twin for
// MoveDashboards, which only has a DashboardKey (no title or owner) to
// report against.
func mapMoveDashboardConflict(k store.DashboardKey, err error) error {
	if strings.Contains(err.Error(), "UNIQUE constraint failed: dashboards.") {
		return store.Refuse(store.ErrConflict,
			"move dashboard %d: sort key %q already used", k.ID, k.SortKey)
	}
	return fmt.Errorf("move dashboard %d: %w", k.ID, err)
}

// UpdateDashboard updates title, sort_key and group_id: the caller passes
// the row it read, with any change.
func (d *DB) UpdateDashboard(ctx context.Context, dash store.Dashboard, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE dashboards SET title=?, sort_key=?, group_id=?, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
			 WHERE id=?`, dash.Title, dash.SortKey, dash.GroupID, dash.ID)
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

// SetDashboardArchived is SetDashboardsArchived for a single id.
func (d *DB) SetDashboardArchived(ctx context.Context, id int64, archived bool, a store.AuditEntry) error {
	return d.SetDashboardsArchived(ctx, []int64{id}, archived, a)
}

// SetDashboardsArchived archives (only rows currently live) or restores
// (only rows currently archived) every id in ids, in one transaction,
// one audit row per id (idempotent no-op rows are still audited, mirroring
// SetProjectArchived's shape). Every id must exist first — checked up
// front, against the whole list, so an unknown id anywhere in ids leaves
// every row untouched rather than archiving a prefix of the list.
func (d *DB) SetDashboardsArchived(ctx context.Context, ids []int64, archived bool, a store.AuditEntry) error {
	if len(ids) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id FROM dashboards WHERE id IN (`+placeholders(len(ids))+`)`, toArgs(ids)...)
		if err != nil {
			return err
		}
		exists := map[int64]bool{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			exists[id] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, id := range ids {
			if !exists[id] {
				return store.Refuse(store.ErrNotFound, "dashboard %d: not found", id)
			}
		}

		q := `UPDATE dashboards SET archived_at=strftime('%Y-%m-%dT%H:%M:%SZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id=? AND archived_at IS NULL`
		if !archived {
			q = `UPDATE dashboards SET archived_at=NULL,
				updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id=?`
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
			entry := a
			entry.Subject = fmt.Sprintf("dashboard/%d", id)
			if err := audit(ctx, tx, entry); err != nil {
				return err
			}
		}
		return nil
	})
}

// MoveDashboards rewrites group_id and sort_key of every row named in ks,
// in one transaction, with one audit row (Subject "dashboard/<ks[0].ID>").
// Sort keys are parked to '~'||id first (the same trick SyncReporting
// uses at reporting_sync.go:52), so reassigning many rows' keys in one
// pass — including swapping two rows' keys — can never collide with the
// unique (owner, sort_key) index mid-way.
func (d *DB) MoveDashboards(ctx context.Context, ks []store.DashboardKey, a store.AuditEntry) error {
	if len(ks) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		ids := make([]int64, len(ks))
		for i, k := range ks {
			ids[i] = k.ID
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE dashboards SET sort_key = '~' || id WHERE id IN (`+placeholders(len(ids))+`)`,
			toArgs(ids)...); err != nil {
			return fmt.Errorf("move dashboards: park sort keys: %w", err)
		}
		for _, k := range ks {
			res, err := tx.ExecContext(ctx,
				`UPDATE dashboards SET group_id=?, sort_key=?, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
				 WHERE id=?`, k.GroupID, k.SortKey, k.ID)
			if err != nil {
				return mapMoveDashboardConflict(k, err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return store.Refuse(store.ErrNotFound, "move dashboards: unknown id %d", k.ID)
			}
		}
		a.Subject = fmt.Sprintf("dashboard/%d", ks[0].ID)
		return audit(ctx, tx, a)
	})
}

// InsertDashboardGroup inserts ds as one new group, in one transaction:
// the first dashboard gets group_id = its own id (inserted with GroupID
// 0, which migration 022's trigger resolves), the rest get that id. ws[i] are ds[i]'s
// widgets: ws must be empty (no dashboard gets any widget) or the same
// length as ds, one slice per dashboard in order — anything else would
// either index out of range or silently drop a trailing dashboard's
// widgets, so it is refused before the transaction opens. Returns the
// new ids, in order.
func (d *DB) InsertDashboardGroup(ctx context.Context, ds []store.Dashboard, ws [][]store.Widget, a store.AuditEntry) ([]int64, error) {
	if len(ws) != 0 && len(ws) != len(ds) {
		return nil, store.Refuse(store.ErrInvalid,
			"insert dashboard group: %d widget slices for %d dashboards", len(ws), len(ds))
	}
	ids := make([]int64, len(ds))
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var groupID int64
		for i, dash := range ds {
			if i == 0 {
				dash.GroupID = 0
			} else {
				dash.GroupID = groupID
			}
			id, err := insertDashboardRow(ctx, tx, dash)
			if err != nil {
				return err
			}
			if i == 0 {
				groupID = id
			}
			ids[i] = id
			if i < len(ws) {
				for _, w := range ws[i] {
					w.DashboardID = id
					if _, err := insertWidgetRow(ctx, tx, w); err != nil {
						return err
					}
				}
			}
		}
		if len(ids) == 0 {
			return nil
		}
		a.Subject = fmt.Sprintf("dashboard/%d", ids[0])
		return audit(ctx, tx, a)
	})
	return ids, err
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
