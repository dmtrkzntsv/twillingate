// Notifications, the tracker's states and the form submission bursts
// (migration 046, spec 2026-11-07-notifications-design). Every row of
// notifications is written by insertNotification.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// notificationColumns is the select list scanNotification reads, in order.
const notificationColumns = `id, type, project_id, subject, fields, created_at, archived_at,
	delivery, delivery_error, attempts, next_attempt_at`

// scanNotification reads one notifications row selected with
// notificationColumns.
func scanNotification(row rowScanner) (store.Notification, error) {
	var n store.Notification
	var project sql.NullInt64
	var fields, created string
	var archived, next sql.NullString
	if err := row.Scan(&n.ID, &n.Type, &project, &n.Subject, &fields, &created, &archived,
		&n.Delivery, &n.DeliveryError, &n.Attempts, &next); err != nil {
		return store.Notification{}, err
	}
	n.ProjectID = project.Int64
	if err := json.Unmarshal([]byte(fields), &n.Fields); err != nil {
		return store.Notification{}, fmt.Errorf("notification %d fields: %w", n.ID, err)
	}
	var err error
	if n.CreatedAt, err = time.Parse(tsFormat, created); err != nil {
		return store.Notification{}, fmt.Errorf("notification %d created_at: %w", n.ID, err)
	}
	for _, t := range []struct {
		src sql.NullString
		dst **time.Time
	}{{archived, &n.ArchivedAt}, {next, &n.NextAttemptAt}} {
		if !t.src.Valid {
			continue
		}
		v, err := time.Parse(tsFormat, t.src.String)
		if err != nil {
			return store.Notification{}, fmt.Errorf("notification %d: %w", n.ID, err)
		}
		*t.dst = &v
	}
	return n, nil
}

// queryNotifications runs a select of notificationColumns and reads every row.
func queryNotifications(ctx context.Context, db *sql.DB, q string, args ...any) ([]store.Notification, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// insertNotification inserts n within tx, pending delivery, and returns
// its id. With a project, fields["project"] is the project's name now
// unless n.Fields already has one; an unknown project is ErrNotFound.
// The caller's map is not changed.
func insertNotification(ctx context.Context, tx *sql.Tx, n store.NewNotification) (int64, error) {
	if !slices.Contains(store.NotificationTypes, n.Type) {
		return 0, store.Refuse(store.ErrInvalid, "unknown notification type %q", n.Type)
	}
	fields := maps.Clone(n.Fields)
	if fields == nil {
		fields = map[string]string{}
	}
	var project any // NULL: no project
	if n.ProjectID != 0 {
		project = n.ProjectID
		if _, ok := fields["project"]; !ok {
			var name string
			switch err := tx.QueryRowContext(ctx, `SELECT name FROM projects WHERE id=?`, n.ProjectID).Scan(&name); {
			case errors.Is(err, sql.ErrNoRows):
				return 0, fmt.Errorf("notification for project %d: %w", n.ProjectID, store.ErrNotFound)
			case err != nil:
				return 0, err
			}
			fields["project"] = name
		}
	}
	blob, err := json.Marshal(fields)
	if err != nil {
		return 0, err
	}
	at := n.At
	if at.IsZero() {
		at = time.Now()
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO notifications (type, project_id, subject, fields, created_at)
		VALUES (?,?,?,?,?)`, n.Type, project, n.Subject, string(blob), fmtTime(at))
	if err != nil {
		return 0, fmt.Errorf("insert notification: %w", err)
	}
	return res.LastInsertId()
}

// AddNotification inserts one notification, pending delivery.
func (d *DB) AddNotification(ctx context.Context, n store.NewNotification) (int64, error) {
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = insertNotification(ctx, tx, n)
		return err
	})
	return id, err
}

// ListNotifyStates reads every tracker state, by project, kind and subject.
func (d *DB) ListNotifyStates(ctx context.Context) ([]store.NotifyState, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT project_id, kind, subject, data FROM notify_states
		ORDER BY project_id, kind, subject`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.NotifyState
	for rows.Next() {
		var s store.NotifyState
		if err := rows.Scan(&s.ProjectID, &s.Kind, &s.Subject, &s.Data); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PutNotifyState upserts s and, when n is not nil, inserts n, in one
// transaction: the state never records a send whose notification is lost.
func (d *DB) PutNotifyState(ctx context.Context, s store.NotifyState, n *store.NewNotification) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO notify_states (project_id, kind, subject, data)
			VALUES (?,?,?,?) ON CONFLICT (project_id, kind, subject) DO UPDATE SET data=excluded.data`,
			s.ProjectID, s.Kind, s.Subject, s.Data); err != nil {
			return fmt.Errorf("notify state %d/%s/%s: %w", s.ProjectID, s.Kind, s.Subject, err)
		}
		if n == nil {
			return nil
		}
		_, err := insertNotification(ctx, tx, *n)
		return err
	})
}

// DeleteNotifyState deletes one tracker state; an absent one is not an
// error.
func (d *DB) DeleteNotifyState(ctx context.Context, projectID int64, kind, subject string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM notify_states WHERE project_id=? AND kind=? AND subject=?`,
		projectID, kind, subject)
	return err
}

// ListNotifications lists the active or archived notifications f selects,
// newest (highest id) first.
func (d *DB) ListNotifications(ctx context.Context, f store.NotificationFilter) ([]store.Notification, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = -1 // SQLite: no limit
	}
	return queryNotifications(ctx, d.db, `SELECT `+notificationColumns+` FROM notifications
		WHERE (archived_at IS NOT NULL) = ? AND (? = 0 OR project_id = ?) AND (? = 0 OR id < ?)
		ORDER BY id DESC LIMIT ?`, f.Archived, f.ProjectID, f.ProjectID, f.Before, f.Before, limit)
}

// CountActiveNotifications counts the notifications not archived.
func (d *DB) CountActiveNotifications(ctx context.Context) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE archived_at IS NULL`).Scan(&n)
	return n, err
}

// SetNotificationsArchived archives (archived true, stamped at) or
// restores the notifications with these ids, or with all every active one,
// in one transaction, and returns how many changed: an id that matches
// nothing or is already in the target state is skipped. all with archived
// false is ErrInvalid: there is no "restore all".
func (d *DB) SetNotificationsArchived(ctx context.Context, ids []int64, all, archived bool, at time.Time) (int, error) {
	if all && !archived {
		return 0, store.Refuse(store.ErrInvalid, "all archives every active notification; restore takes ids")
	}
	var changed int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		if all {
			res, err := tx.ExecContext(ctx, `UPDATE notifications SET archived_at=? WHERE archived_at IS NULL`, fmtTime(at))
			if err != nil {
				return err
			}
			changed, err = res.RowsAffected()
			return err
		}
		for _, id := range ids {
			var res sql.Result
			var err error
			if archived {
				res, err = tx.ExecContext(ctx, `UPDATE notifications SET archived_at=? WHERE id=? AND archived_at IS NULL`,
					fmtTime(at), id)
			} else {
				res, err = tx.ExecContext(ctx, `UPDATE notifications SET archived_at=NULL WHERE id=? AND archived_at IS NOT NULL`, id)
			}
			if err != nil {
				return fmt.Errorf("notification %d: %w", id, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			changed += n
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int(changed), nil
}

// PendingDeliveries lists up to limit notifications pending delivery whose
// next attempt is due at now (or that have none), oldest first.
func (d *DB) PendingDeliveries(ctx context.Context, now time.Time, limit int) ([]store.Notification, error) {
	return queryNotifications(ctx, d.db, `SELECT `+notificationColumns+` FROM notifications
		WHERE delivery='pending' AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY id LIMIT ?`, fmtTime(now), limit)
}

// SetDelivery records a delivery attempt's outcome: delivery (one of the
// Delivery* values; ErrInvalid otherwise), its error text, the attempts so
// far and when the next is due (nil: none). An unknown id is ErrNotFound.
func (d *DB) SetDelivery(ctx context.Context, id int64, delivery, errText string, attempts int, next *time.Time) error {
	switch delivery {
	case store.DeliveryPending, store.DeliverySent, store.DeliveryFailed, store.DeliveryNone, store.DeliverySkipped:
	default:
		return store.Refuse(store.ErrInvalid, "unknown delivery %q", delivery)
	}
	res, err := d.db.ExecContext(ctx, `UPDATE notifications SET delivery=?, delivery_error=?, attempts=?, next_attempt_at=?
		WHERE id=?`, delivery, errText, attempts, nullTime(next), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("notification %d: %w", id, store.ErrNotFound)
	}
	return nil
}

// PurgeArchivedNotifications deletes the notifications archived before
// now - days and returns how many went; days <= 0 purges nothing. Active
// ones are never purged.
func (d *DB) PurgeArchivedNotifications(ctx context.Context, now time.Time, days int) (int, error) {
	if days <= 0 {
		return 0, nil
	}
	res, err := d.db.ExecContext(ctx, `DELETE FROM notifications WHERE archived_at IS NOT NULL AND archived_at < ?`,
		fmtTime(now.AddDate(0, 0, -days)))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// notifySubmission is a submission to an approved, active form with notify
// on, received at received, within tx: with no burst row, or a window that
// ended with nothing pending, it notifies at once (count 1) and opens a
// window at received; otherwise it joins the window's pending count, which
// FlushFormBursts sends once the window ends.
func notifySubmission(ctx context.Context, tx *sql.Tx, projectID int64, form string, received time.Time) error {
	var start string
	var pending int
	switch err := tx.QueryRowContext(ctx, `SELECT window_start, pending FROM form_bursts WHERE project_id=? AND form=?`,
		projectID, form).Scan(&start, &pending); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	case pending > 0 || start > fmtTime(received.Add(-store.FormBurstWindow)):
		_, err := tx.ExecContext(ctx, `UPDATE form_bursts SET pending=pending+1 WHERE project_id=? AND form=?`,
			projectID, form)
		return err
	}
	if _, err := insertNotification(ctx, tx, store.NewNotification{Type: store.NotifyFormSubmission,
		ProjectID: projectID, Subject: form, Fields: map[string]string{"form": form, "count": "1"},
		At: received}); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO form_bursts (project_id, form, window_start, pending) VALUES (?,?,?,0)
		ON CONFLICT (project_id, form) DO UPDATE SET window_start=excluded.window_start, pending=0`,
		projectID, form, fmtTime(received))
	return err
}

// FlushFormBursts sends every burst whose window ended at now with
// submissions pending: one form_submission notification each, count the
// pending number, stamped now, and a new window from now with nothing
// pending. A row whose form no longer exists sends nothing. Rows with
// nothing pending whose window started over a day before now, and rows
// whose form no longer exists, are deleted. One transaction; returns the notifications
// added.
func (d *DB) FlushFormBursts(ctx context.Context, now time.Time) (int, error) {
	type burst struct {
		project int64
		form    string
		pending int
	}
	var sent int
	err := d.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT b.project_id, b.form, b.pending FROM form_bursts b
			WHERE b.pending > 0 AND b.window_start <= ?
			  AND EXISTS (SELECT 1 FROM forms f WHERE f.project_id=b.project_id AND f.name=b.form)
			ORDER BY b.project_id, b.form`,
			fmtTime(now.Add(-store.FormBurstWindow)))
		if err != nil {
			return err
		}
		var due []burst
		for rows.Next() {
			var b burst
			if err := rows.Scan(&b.project, &b.form, &b.pending); err != nil {
				rows.Close()
				return err
			}
			due = append(due, b)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, b := range due {
			if _, err := insertNotification(ctx, tx, store.NewNotification{Type: store.NotifyFormSubmission,
				ProjectID: b.project, Subject: b.form,
				Fields: map[string]string{"form": b.form, "count": strconv.Itoa(b.pending)}, At: now}); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE form_bursts SET window_start=?, pending=0 WHERE project_id=? AND form=?`,
				fmtTime(now), b.project, b.form); err != nil {
				return err
			}
		}
		sent = len(due)
		_, err = tx.ExecContext(ctx, `DELETE FROM form_bursts WHERE (pending = 0 AND window_start < ?)
			OR NOT EXISTS (SELECT 1 FROM forms f WHERE f.project_id=form_bursts.project_id AND f.name=form_bursts.form)`,
			fmtTime(now.Add(-24*time.Hour)))
		return err
	})
	if err != nil {
		return 0, err
	}
	return sent, nil
}
