package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const alertCols = `id, project_id, name, definition, paused, snoozed_until, revision, state, reason,
	last_value, last_evaluated_at, last_day, fails, retry_at, check_requested_at, created_at, updated_at,
	COALESCE(archived_at,'')`

func scanAlert(s rowScanner) (store.Alert, error) {
	var a store.Alert
	var def string
	var last sql.NullFloat64
	err := s.Scan(&a.ID, &a.ProjectID, &a.Name, &def, &a.Paused, &a.SnoozedUntil, &a.Revision, &a.State, &a.Reason,
		&last, &a.LastEvaluatedAt, &a.LastDay, &a.Fails, &a.RetryAt, &a.CheckRequestedAt, &a.CreatedAt, &a.UpdatedAt,
		&a.ArchivedAt)
	if err != nil {
		return a, err
	}
	if last.Valid {
		a.LastValue = &last.Float64
	}
	if err = json.Unmarshal([]byte(def), &a.Def); err != nil {
		err = fmt.Errorf("alert %d: definition: %w", a.ID, err)
	}
	return a, err
}

// defJSON is d as stored. AlertDef holds a RawMessage the caller has
// validated, so marshalling fails only on invalid JSON, which is refused.
func defJSON(d store.AlertDef) (string, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return "", store.Refuse(store.ErrInvalid, "alert definition: %v", err)
	}
	return string(raw), nil
}

func alertTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func alertSubject(id int64) string { return fmt.Sprintf("alert/%d", id) }

// projectLive reports whether the project exists and is not archived, and
// ErrNotFound when it does not exist.
func projectLive(ctx context.Context, tx *sql.Tx, id int64) (bool, error) {
	var archived sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT archived_at FROM projects WHERE id=?`, id).Scan(&archived)
	if errors.Is(err, sql.ErrNoRows) {
		return false, unknownProject(id)
	}
	return !archived.Valid, err
}

// alertEvaluable reports whether the runner would evaluate an alert with
// these fields at now: not paused, not snoozed past now, project live.
func alertEvaluable(paused bool, snoozedUntil string, live bool, now string) bool {
	return !paused && (snoozedUntil == "" || snoozedUntil <= now) && live
}

func getAlertTx(ctx context.Context, tx *sql.Tx, projectID, id int64) (store.Alert, error) {
	a, err := scanAlert(tx.QueryRowContext(ctx, `SELECT `+alertCols+` FROM alerts WHERE id=? AND project_id=?`, id, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return a, store.Refuse(store.ErrNotFound, "project %d has no alert %d", projectID, id)
	}
	return a, err
}

// checkAlertRoom refuses with ErrConflict a live alert named name besides
// the one with id exceptID, and, when max > 0 and counted, a project already
// holding max live alerts besides it.
func checkAlertRoom(ctx context.Context, tx *sql.Tx, projectID, exceptID int64, name string, max int, counted bool) error {
	var taken int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts WHERE project_id=? AND name=? AND id<>? AND archived_at IS NULL`,
		projectID, name, exceptID).Scan(&taken); err != nil {
		return err
	}
	if taken > 0 {
		return store.Refuse(store.ErrConflict, "project %d already has a live alert named %q", projectID, name)
	}
	if !counted {
		return nil
	}
	var live int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts WHERE project_id=? AND id<>? AND archived_at IS NULL`,
		projectID, exceptID).Scan(&live); err != nil {
		return err
	}
	if live >= max {
		return store.Refuse(store.ErrConflict, "project %d holds %d live alerts, the most it may; archive one first", projectID, live)
	}
	return nil
}

// CreateAlert inserts a pending alert and audits it. The name, definition
// and pause of a are used; the rest is the new row's defaults.
func (d *DB) CreateAlert(ctx context.Context, a store.Alert, max int, now time.Time, au store.AuditEntry) (store.Alert, error) {
	var out store.Alert
	def, err := defJSON(a.Def)
	if err != nil {
		return out, err
	}
	at := alertTime(now)
	err = d.tx(ctx, func(tx *sql.Tx) error {
		live, err := projectLive(ctx, tx, a.ProjectID)
		if err != nil {
			return err
		}
		if err := checkAlertRoom(ctx, tx, a.ProjectID, 0, a.Name, max, true); err != nil {
			return err
		}
		requested := ""
		if alertEvaluable(a.Paused, "", live, at) {
			requested = at
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO alerts (project_id, name, definition, paused, check_requested_at, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?)`, a.ProjectID, a.Name, def, a.Paused, requested, at, at)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		au.Subject, au.Detail = alertSubject(id), a.Name
		if err := audit(ctx, tx, au); err != nil {
			return err
		}
		out, err = getAlertTx(ctx, tx, a.ProjectID, id)
		return err
	})
	return out, err
}

// GetAlert reads one alert of the project, archived or not.
func (d *DB) GetAlert(ctx context.Context, projectID, id int64) (store.Alert, error) {
	a, err := scanAlert(d.db.QueryRowContext(ctx, `SELECT `+alertCols+` FROM alerts WHERE id=? AND project_id=?`, id, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return a, store.Refuse(store.ErrNotFound, "project %d has no alert %d", projectID, id)
	}
	return a, err
}

// ListAlerts lists the project's live alerts, or its archived ones, by name.
func (d *DB) ListAlerts(ctx context.Context, projectID int64, archived bool) ([]store.Alert, error) {
	q := `SELECT ` + alertCols + ` FROM alerts WHERE project_id=? AND archived_at IS NULL ORDER BY name, id`
	if archived {
		q = `SELECT ` + alertCols + ` FROM alerts WHERE project_id=? AND archived_at IS NOT NULL ORDER BY name, id`
	}
	rows, err := d.db.QueryContext(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Alert
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// updateAlertRow writes every column an edit or a restore touches, resetting
// what the evaluations left when reset is set: last_evaluated_at too, so a
// check requested in the very second of the last evaluation is pending
// (CheckPending compares whole seconds).
const updateAlertRow = `UPDATE alerts SET name=?, definition=?, paused=?, snoozed_until=?,
	revision = revision + ?,
	state = CASE WHEN ? THEN 'pending' ELSE state END,
	reason = CASE WHEN ? THEN '' ELSE reason END,
	last_value = CASE WHEN ? THEN NULL ELSE last_value END,
	last_day = CASE WHEN ? THEN '' ELSE last_day END,
	last_evaluated_at = CASE WHEN ? THEN '' ELSE last_evaluated_at END,
	fails = CASE WHEN ? THEN 0 ELSE fails END,
	retry_at = CASE WHEN ? THEN '' ELSE retry_at END,
	check_requested_at=?, updated_at=?, archived_at=?
	WHERE id=? AND project_id=?`

// UpdateAlert merges the edit into the alert and audits what changed. A
// change to the definition, pause or snooze resets the alert to pending and
// bumps its revision; a rename alone does neither.
func (d *DB) UpdateAlert(ctx context.Context, projectID, id int64, e store.AlertEdit, now time.Time, au store.AuditEntry) (store.Alert, error) {
	var out store.Alert
	at := alertTime(now)
	err := d.tx(ctx, func(tx *sql.Tx) error {
		cur, err := getAlertTx(ctx, tx, projectID, id)
		if err != nil {
			return err
		}
		out = cur
		if cur.ArchivedAt != "" {
			return store.Refuse(store.ErrInvalid, "alert %d is archived; restore it to edit it", id)
		}
		next := cur
		var changed []string
		if e.Name != nil && *e.Name != cur.Name {
			next.Name = *e.Name
			changed = append(changed, "name")
		}
		curDef, err := defJSON(cur.Def)
		if err != nil {
			return err
		}
		nextDef := curDef
		if e.Def != nil {
			if nextDef, err = defJSON(*e.Def); err != nil {
				return err
			}
			if nextDef != curDef {
				next.Def = *e.Def
				changed = append(changed, "definition")
			}
		}
		var reset, request bool
		reset = nextDef != curDef
		request = reset
		if e.Paused != nil && *e.Paused != cur.Paused {
			next.Paused, reset = *e.Paused, true
			request = request || !next.Paused
			changed = append(changed, fmt.Sprintf("paused=%t", next.Paused))
		}
		if e.SnoozedUntil != nil && *e.SnoozedUntil != cur.SnoozedUntil {
			next.SnoozedUntil, reset = *e.SnoozedUntil, true
			request = request || next.SnoozedUntil == ""
			changed = append(changed, "snoozed_until="+next.SnoozedUntil)
		}
		if len(changed) == 0 {
			return nil
		}
		if next.Name != cur.Name {
			if err := checkAlertRoom(ctx, tx, projectID, id, next.Name, 0, false); err != nil {
				return err
			}
		}
		requested := cur.CheckRequestedAt
		if reset {
			live, err := projectLive(ctx, tx, projectID)
			if err != nil {
				return err
			}
			requested = ""
			if request && alertEvaluable(next.Paused, next.SnoozedUntil, live, at) {
				requested = at
			}
		}
		bump := 0
		if reset {
			bump = 1
		}
		if _, err := tx.ExecContext(ctx, updateAlertRow, next.Name, nextDef, next.Paused, next.SnoozedUntil,
			bump, reset, reset, reset, reset, reset, reset, reset, requested, at, nil, id, projectID); err != nil {
			return err
		}
		au.Subject, au.Detail = alertSubject(id), strings.Join(changed, " ")
		if err := audit(ctx, tx, au); err != nil {
			return err
		}
		out, err = getAlertTx(ctx, tx, projectID, id)
		return err
	})
	return out, err
}

// SetAlertArchived archives the alert or restores it and audits it. Doing
// what is already done writes nothing. Archiving ends its check request; a
// restore refuses like a create (a live alert of the name, max live alerts)
// and brings the alert back pending.
func (d *DB) SetAlertArchived(ctx context.Context, projectID, id int64, archived bool, max int, now time.Time, au store.AuditEntry) (store.Alert, error) {
	var out store.Alert
	at := alertTime(now)
	err := d.tx(ctx, func(tx *sql.Tx) error {
		cur, err := getAlertTx(ctx, tx, projectID, id)
		if err != nil {
			return err
		}
		out = cur
		if (cur.ArchivedAt != "") == archived {
			return nil
		}
		def, err := defJSON(cur.Def)
		if err != nil {
			return err
		}
		var requested string
		var archivedAt any
		if archived {
			archivedAt = at
		} else {
			if err := checkAlertRoom(ctx, tx, projectID, id, cur.Name, max, true); err != nil {
				return err
			}
			live, err := projectLive(ctx, tx, projectID)
			if err != nil {
				return err
			}
			if alertEvaluable(cur.Paused, cur.SnoozedUntil, live, at) {
				requested = at
			}
		}
		if _, err := tx.ExecContext(ctx, updateAlertRow, cur.Name, def, cur.Paused, cur.SnoozedUntil,
			1, true, true, true, true, true, true, true, requested, at, archivedAt, id, projectID); err != nil {
			return err
		}
		au.Subject, au.Detail = alertSubject(id), cur.Name
		if err := audit(ctx, tx, au); err != nil {
			return err
		}
		out, err = getAlertTx(ctx, tx, projectID, id)
		return err
	})
	return out, err
}

// RequestAlertCheck asks for a check of the alert at now and audits it,
// clearing last_evaluated_at so that a request in the very second of the
// last evaluation is pending (CheckPending compares whole seconds). It
// reports false, writing nothing, when a request is already pending. An alert
// the runner would not evaluate (archived, paused, snoozed, of an archived
// project) is ErrInvalid: the request would never be served.
func (d *DB) RequestAlertCheck(ctx context.Context, projectID, id int64, now time.Time, au store.AuditEntry) (store.Alert, bool, error) {
	var out store.Alert
	var requested bool
	at := alertTime(now)
	err := d.tx(ctx, func(tx *sql.Tx) error {
		cur, err := getAlertTx(ctx, tx, projectID, id)
		if err != nil {
			return err
		}
		out = cur
		if cur.ArchivedAt != "" {
			return store.Refuse(store.ErrInvalid, "alert %d is archived; restore it to check it", id)
		}
		live, err := projectLive(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if !alertEvaluable(cur.Paused, cur.SnoozedUntil, live, at) {
			return store.Refuse(store.ErrInvalid, "alert %d is paused, snoozed or of an archived project, so a check would not run", id)
		}
		if cur.CheckPending() {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alerts SET check_requested_at=?, last_evaluated_at='' WHERE id=?`, at, id); err != nil {
			return err
		}
		au.Subject, au.Detail = alertSubject(id), cur.Name
		if err := audit(ctx, tx, au); err != nil {
			return err
		}
		requested = true
		out, err = getAlertTx(ctx, tx, projectID, id)
		return err
	})
	return out, requested, err
}

// RecordAlertEvaluation stores the evaluation and writes its outcome to the
// alert's row, in one transaction. A State of "" leaves the state, reason and
// last value alone, and a LastDay of "" leaves last_day. It reports false,
// writing nothing, when the alert was edited (another revision), archived or
// is of an archived project since the evaluation began.
func (d *DB) RecordAlertEvaluation(ctx context.Context, r store.AlertResult) (bool, error) {
	var ok bool
	err := d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE alerts SET
			state = CASE WHEN ?1 <> '' THEN ?1 ELSE state END,
			reason = CASE WHEN ?1 <> '' THEN ?2 ELSE reason END,
			last_value = CASE WHEN ?1 <> '' THEN ?3 ELSE last_value END,
			last_evaluated_at = ?4,
			last_day = CASE WHEN ?5 <> '' THEN ?5 ELSE last_day END,
			fails = ?6, retry_at = ?7
			WHERE id = ?8 AND revision = ?9 AND archived_at IS NULL
			AND project_id IN (SELECT id FROM projects WHERE archived_at IS NULL)`,
			r.State, r.Reason, r.Eval.Value, r.Eval.At, r.LastDay, r.Fails, r.RetryAt, r.AlertID, r.Revision)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		e := r.Eval
		if _, err := tx.ExecContext(ctx, `INSERT INTO alert_evaluations (alert_id, at, day_from, day_to, value, compare, volume, outcome, error)
			VALUES (?,?,?,?,?,?,?,?,?)`, r.AlertID, e.At, e.From, e.To, e.Value, e.Compare, e.Volume, e.Outcome, e.Error); err != nil {
			return err
		}
		ok = true
		return nil
	})
	return ok, err
}

const evalCols = `id, alert_id, at, day_from, day_to, value, compare, volume, outcome, error`

func scanEvaluation(s rowScanner) (store.AlertEvaluation, error) {
	var e store.AlertEvaluation
	var v, c, vol sql.NullFloat64
	if err := s.Scan(&e.ID, &e.AlertID, &e.At, &e.From, &e.To, &v, &c, &vol, &e.Outcome, &e.Error); err != nil {
		return e, err
	}
	for _, p := range []struct {
		n   sql.NullFloat64
		dst **float64
	}{{v, &e.Value}, {c, &e.Compare}, {vol, &e.Volume}} {
		if p.n.Valid {
			f := p.n.Float64
			*p.dst = &f
		}
	}
	return e, nil
}

func scanEvaluations(rows *sql.Rows) ([]store.AlertEvaluation, error) {
	defer rows.Close()
	var out []store.AlertEvaluation
	for rows.Next() {
		e, err := scanEvaluation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListAlertEvaluations lists the alert's evaluations newest first, at most
// limit, those with an id below before when it is > 0.
func (d *DB) ListAlertEvaluations(ctx context.Context, alertID int64, limit int, before int64) ([]store.AlertEvaluation, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+evalCols+` FROM alert_evaluations
		WHERE alert_id=? AND (?<=0 OR id<?) ORDER BY id DESC LIMIT ?`, alertID, before, before, limit)
	if err != nil {
		return nil, err
	}
	return scanEvaluations(rows)
}

// RecentAlertEvaluations maps each of the alerts to its n newest
// evaluations, newest first. An alert with none is absent.
func (d *DB) RecentAlertEvaluations(ctx context.Context, ids []int64, n int) (map[int64][]store.AlertEvaluation, error) {
	out := map[int64][]store.AlertEvaluation{}
	if len(ids) == 0 {
		return out, nil
	}
	args := append(toArgs(ids), n)
	rows, err := d.db.QueryContext(ctx, `SELECT `+evalCols+` FROM (
		SELECT `+evalCols+`, ROW_NUMBER() OVER (PARTITION BY alert_id ORDER BY id DESC) AS rn
		FROM alert_evaluations WHERE alert_id IN (`+placeholders(len(ids))+`))
		WHERE rn <= ? ORDER BY alert_id, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	list, err := scanEvaluations(rows)
	for _, e := range list {
		out[e.AlertID] = append(out[e.AlertID], e)
	}
	return out, err
}

// ListEvaluableAlerts lists the live alerts of live projects, paused and
// snoozed ones included: the runner decides what to do with those.
func (d *DB) ListEvaluableAlerts(ctx context.Context) ([]store.Alert, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+alertCols+` FROM alerts WHERE archived_at IS NULL
		AND project_id IN (SELECT id FROM projects WHERE archived_at IS NULL) ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return scanAlerts(rows)
}

// ListCheckPendingAlerts lists the live alerts of live projects whose
// check is pending (store.Alert.CheckPending), by id: what the runner
// reads again between the evaluations of a long tick.
func (d *DB) ListCheckPendingAlerts(ctx context.Context) ([]store.Alert, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+alertCols+` FROM alerts WHERE archived_at IS NULL
		AND check_requested_at <> '' AND (last_evaluated_at = '' OR last_evaluated_at < check_requested_at)
		AND project_id IN (SELECT id FROM projects WHERE archived_at IS NULL) ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return scanAlerts(rows)
}

func scanAlerts(rows *sql.Rows) ([]store.Alert, error) {
	defer rows.Close()
	var out []store.Alert
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// resetProjectAlerts returns every live alert of the project to pending
// at a new revision, as an edit does, and ends its check request:
// archiving the project does, so a restore starts clean and a running
// evaluation is not recorded. Unaudited: the project's archive is.
func resetProjectAlerts(ctx context.Context, tx *sql.Tx, projectID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE alerts SET revision = revision + 1, state='pending', reason='', last_value=NULL,
		last_day='', last_evaluated_at='', fails=0, retry_at='', check_requested_at=''
		WHERE project_id=? AND archived_at IS NULL`, projectID)
	return err
}

// PruneAlertEvaluations deletes the evaluations made before the given time.
func (d *DB) PruneAlertEvaluations(ctx context.Context, before time.Time) (int, error) {
	res, err := d.db.ExecContext(ctx, `DELETE FROM alert_evaluations WHERE at < ?`, alertTime(before))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
