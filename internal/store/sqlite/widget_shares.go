// Widget shares (migration 032): a frozen picture of one widget, two PNGs
// and the text they show, served publicly by id. A share lives as long as
// its project (projectTables) and outlives its widget (widget_id goes
// NULL). Like the other reporting writers these audit without bumping
// config_version.
package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const shareCols = `s.id, COALESCE(s.widget_id,0), COALESCE(w.dashboard_id,0), COALESCE(d.title,''),
	s.project_id, s.project_name, s.range_from, s.range_to, s.title, s.caption_project, s.caption_range, s.created_at,
	COALESCE(s.archive_at,''), COALESCE(s.archived_at,'')`

const shareFrom = ` FROM widget_shares s
	LEFT JOIN widgets w ON w.id = s.widget_id
	LEFT JOIN dashboards d ON d.id = w.dashboard_id`

func scanShare(r rowScanner) (store.WidgetShare, error) {
	var s store.WidgetShare
	err := r.Scan(&s.ID, &s.WidgetID, &s.DashboardID, &s.DashboardTitle, &s.ProjectID, &s.ProjectName,
		&s.From, &s.To, &s.Title, &s.CaptionProject, &s.CaptionRange, &s.CreatedAt, &s.ArchiveAt, &s.ArchivedAt)
	return s, err
}

func shareNotFound(id string) error {
	return store.Refuse(store.ErrNotFound, "widget share %s: not found", id)
}

// getShare reads one share through q, a *sql.DB or the *sql.Tx of a write.
func getShare(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (store.WidgetShare, error) {
	s, err := scanShare(q.QueryRowContext(ctx, `SELECT `+shareCols+shareFrom+` WHERE s.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return s, shareNotFound(id)
	}
	return s, err
}

func (d *DB) InsertWidgetShare(ctx context.Context, n store.NewWidgetShare, a store.AuditEntry) (store.WidgetShare, error) {
	var out store.WidgetShare
	err := d.tx(ctx, func(tx *sql.Tx) error {
		// widget_id 0 would break the foreign key; a share is always taken
		// of a widget that exists, so it is passed as-is.
		res, err := tx.ExecContext(ctx, `INSERT INTO widget_shares
			(id, widget_id, project_id, range_from, range_to, title, project_name, caption_project, caption_range,
			 image, image_2x, archive_at)
			SELECT ?, ?, id, ?, ?, ?, name, ?, ?, ?, ?, NULLIF(?, '') FROM projects WHERE id = ?`,
			n.ID, n.WidgetID, n.From, n.To, n.Title, n.CaptionProject, n.CaptionRange,
			n.Image, n.Image2x, n.ArchiveAt, n.ProjectID)
		if err != nil {
			return err
		}
		if k, _ := res.RowsAffected(); k == 0 {
			return store.Refuse(store.ErrNotFound, "project %d: not found", n.ProjectID)
		}
		a.Subject = "widget_share/" + n.ID
		if err := audit(ctx, tx, a); err != nil {
			return err
		}
		out, err = getShare(ctx, tx, n.ID)
		return err
	})
	return out, err
}

func (d *DB) GetWidgetShare(ctx context.Context, id string) (store.WidgetShare, error) {
	return getShare(ctx, d.db, id)
}

func (d *DB) WidgetShareImage(ctx context.Context, id string, twoX bool) ([]byte, error) {
	col := "image"
	if twoX {
		col = "image_2x"
	}
	var b []byte
	err := d.db.QueryRowContext(ctx, `SELECT `+col+` FROM widget_shares WHERE id = ?`, id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, shareNotFound(id)
	}
	return b, err
}

func (d *DB) ListWidgetShares(ctx context.Context, f store.WidgetShareFilter) ([]store.WidgetShare, error) {
	switch f.State {
	case "", "live", "archived":
	default:
		return nil, store.Refuse(store.ErrInvalid, "state %q: want live or archived", f.State)
	}
	// Live shares sort by created_at; archived ones by when they were
	// archived (by hand) or came due (archive_at passed, daily pass not
	// yet run), most recent first.
	rows, err := d.db.QueryContext(ctx, `SELECT `+shareCols+shareFrom+`
		WHERE (?1 = 0 OR s.widget_id = ?1)
		  AND (?2 = '' OR (?2 = 'live') = (s.archived_at IS NULL AND (s.archive_at IS NULL OR s.archive_at > ?3)))
		ORDER BY COALESCE(s.archived_at, CASE WHEN s.archive_at <= ?3 THEN s.archive_at END, s.created_at) DESC, s.id DESC`,
		f.WidgetID, f.State, f.Now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.WidgetShare
	for rows.Next() {
		s, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) SetWidgetShareArchiveAt(ctx context.Context, id, archiveAt, now string, a store.AuditEntry) (store.WidgetShare, error) {
	var out store.WidgetShare
	err := d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE widget_shares SET archive_at = NULLIF(?, '')
			WHERE id = ? AND archived_at IS NULL AND (archive_at IS NULL OR archive_at > ?)`,
			archiveAt, id, now)
		if err != nil {
			return err
		}
		if k, _ := res.RowsAffected(); k == 0 {
			if _, err := getShare(ctx, tx, id); err != nil {
				return err // unknown id: not found
			}
			return store.Refuse(store.ErrConflict, "widget share %s is archived; restore_widget_share instead", id)
		}
		a.Subject = "widget_share/" + id
		if err := audit(ctx, tx, a); err != nil {
			return err
		}
		out, err = getShare(ctx, tx, id)
		return err
	})
	return out, err
}

func (d *DB) SetWidgetShareArchived(ctx context.Context, id string, archived bool, archiveAt string, a store.AuditEntry) (store.WidgetShare, error) {
	var out store.WidgetShare
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var res sql.Result
		var err error
		if archived {
			res, err = tx.ExecContext(ctx, `UPDATE widget_shares
				SET archived_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
				WHERE id = ? AND archived_at IS NULL`, id)
		} else {
			res, err = tx.ExecContext(ctx, `UPDATE widget_shares
				SET archived_at = NULL, archive_at = NULLIF(?, '') WHERE id = ?`, archiveAt, id)
		}
		if err != nil {
			return err
		}
		if k, _ := res.RowsAffected(); k == 0 {
			// Unknown id is not found; an archived share archived again is
			// idempotent: no change, no audit row.
			out, err = getShare(ctx, tx, id)
			return err
		}
		a.Subject = "widget_share/" + id
		if err := audit(ctx, tx, a); err != nil {
			return err
		}
		out, err = getShare(ctx, tx, id)
		return err
	})
	return out, err
}

func (d *DB) ArchiveDueWidgetShares(ctx context.Context, now string) (int, error) {
	var n int
	err := d.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM widget_shares
			WHERE archived_at IS NULL AND archive_at IS NOT NULL AND archive_at <= ?`, now)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE widget_shares SET archived_at = archive_at WHERE id = ?`, id); err != nil {
				return err
			}
			if err := audit(ctx, tx, store.AuditEntry{
				Actor: "retention", Action: "widget_share.archive", Subject: "widget_share/" + id}); err != nil {
				return err
			}
		}
		n = len(ids)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
