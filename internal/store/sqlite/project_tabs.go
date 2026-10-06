// Project tab rows (migration 032, spec 2026-10-05 D2): which dashboards
// a project page shows, and the placement flags on dashboards. Writes
// use audit, not auditAndBump: they are not managed-config.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func (d *DB) ListProjectTabs(ctx context.Context, projectID int64) ([]store.ProjectTabRow, error) {
	var one int
	err := d.db.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id=?`, projectID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.Refuse(store.ErrNotFound, "project %d: not found", projectID)
	}
	if err != nil {
		return nil, err
	}
	rows, err := d.db.QueryContext(ctx, `SELECT project_id, dashboard_id, sort_key FROM project_tabs
		WHERE project_id=? ORDER BY sort_key, dashboard_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.ProjectTabRow{}
	for rows.Next() {
		var r store.ProjectTabRow
		if err := rows.Scan(&r.ProjectID, &r.DashboardID, &r.SortKey); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) ListDashboardProjects(ctx context.Context, dashboardID int64) ([]int64, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT project_id FROM project_tabs WHERE dashboard_id=? ORDER BY project_id`, dashboardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *DB) InsertProjectTab(ctx context.Context, r store.ProjectTabRow, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO project_tabs (project_id, dashboard_id, sort_key) VALUES (?,?,?)`,
			r.ProjectID, r.DashboardID, r.SortKey)
		switch {
		case err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: project_tabs"):
			return store.Refuse(store.ErrConflict, "project %d already has dashboard %d as a tab", r.ProjectID, r.DashboardID)
		case err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed"):
			return store.Refuse(store.ErrNotFound, "project %d or dashboard %d: not found", r.ProjectID, r.DashboardID)
		case err != nil:
			return fmt.Errorf("insert project tab: %w", err)
		}
		a.Subject = fmt.Sprintf("project/%d/tab/%d", r.ProjectID, r.DashboardID)
		return audit(ctx, tx, a)
	})
}

func (d *DB) DeleteProjectTab(ctx context.Context, projectID, dashboardID int64, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM project_tabs WHERE project_id=? AND dashboard_id=?`, projectID, dashboardID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.Refuse(store.ErrNotFound, "project %d has no tab for dashboard %d", projectID, dashboardID)
		}
		a.Subject = fmt.Sprintf("project/%d/tab/%d", projectID, dashboardID)
		return audit(ctx, tx, a)
	})
}

func (d *DB) MoveProjectTab(ctx context.Context, r store.ProjectTabRow, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE project_tabs SET sort_key=? WHERE project_id=? AND dashboard_id=?`,
			r.SortKey, r.ProjectID, r.DashboardID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.Refuse(store.ErrNotFound, "project %d has no tab for dashboard %d", r.ProjectID, r.DashboardID)
		}
		a.Subject = fmt.Sprintf("project/%d/tab/%d", r.ProjectID, r.DashboardID)
		return audit(ctx, tx, a)
	})
}

// SetDashboardsSidebar shows or hides dashboards in the sidebar; a.Action
// says which. Unknown ids are ErrNotFound and nothing is written; one
// audit row per id.
func (d *DB) SetDashboardsSidebar(ctx context.Context, ids []int64, sidebar bool, a store.AuditEntry) error {
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
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx,
				`UPDATE dashboards SET sidebar=?, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id=?`,
				sidebar, id); err != nil {
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

func (d *DB) SetDashboardProjectTab(ctx context.Context, id int64, on bool, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE dashboards SET project_tab=?, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id=?`,
			on, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.Refuse(store.ErrNotFound, "set project tab: unknown dashboard %d", id)
		}
		a.Subject = fmt.Sprintf("dashboard/%d", id)
		return audit(ctx, tx, a)
	})
}
