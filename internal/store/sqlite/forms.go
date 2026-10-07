package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// formColumns is the select list scanForm reads, in order.
const formColumns = `project_id, name, status, purpose, return_url, fields, expected_fields,
	created_at, draft_until, approved_at, closes_at, last_submitted_at, archived_at`

// scanForm reads one forms row selected with formColumns; extra receives
// any columns selected after them.
func scanForm(row interface{ Scan(...any) error }, extra ...any) (store.Form, error) {
	var f store.Form
	var fields string
	var expected, created, draft, approved, closes, last, archived sql.NullString
	dest := append([]any{&f.ProjectID, &f.Name, &f.Status, &f.Purpose, &f.ReturnURL, &fields, &expected,
		&created, &draft, &approved, &closes, &last, &archived}, extra...)
	if err := row.Scan(dest...); err != nil {
		return store.Form{}, err
	}
	if err := json.Unmarshal([]byte(fields), &f.Fields); err != nil {
		return store.Form{}, fmt.Errorf("form %s fields: %w", f.Name, err)
	}
	if expected.Valid {
		if err := json.Unmarshal([]byte(expected.String), &f.ExpectedFields); err != nil {
			return store.Form{}, fmt.Errorf("form %s expected_fields: %w", f.Name, err)
		}
	}
	var err error
	if f.CreatedAt, err = time.Parse(tsFormat, created.String); err != nil {
		return store.Form{}, fmt.Errorf("form %s created_at: %w", f.Name, err)
	}
	for _, t := range []struct {
		src sql.NullString
		dst **time.Time
	}{{draft, &f.DraftUntil}, {approved, &f.ApprovedAt}, {closes, &f.ClosesAt},
		{last, &f.LastSubmittedAt}, {archived, &f.ArchivedAt}} {
		if !t.src.Valid {
			continue
		}
		v, err := time.Parse(tsFormat, t.src.String)
		if err != nil {
			return store.Form{}, fmt.Errorf("form %s: %w", f.Name, err)
		}
		*t.dst = &v
	}
	return f, nil
}

// closedReason says why f refuses a submission at now, "" when it is open.
func closedReason(f store.Form, now time.Time) string {
	switch {
	case f.Open(now):
		return ""
	case f.ArchivedAt != nil:
		return "it is archived"
	case f.Status == store.FormDraft && f.DraftUntil != nil && !f.DraftUntil.After(now):
		return "its draft expired"
	default:
		return "it is closed"
	}
}

// WriteSubmission records a submission, creating its form as a draft, and
// on an approved form its $form_submit event, in one transaction. The
// refusal and the duplicate id are decided before anything else is
// written, so neither changes the form. The event's id, ts and project
// are the submission's, whatever n.Event carries, so a later delete finds
// it by (family product, project_id, day, id).
func (d *DB) WriteSubmission(ctx context.Context, n store.NewSubmission) (store.Form, bool, error) {
	sub := n.Submission
	var form store.Form
	var inserted bool
	var fresh []receivedKey
	err := d.tx(ctx, func(tx *sql.Tx) error {
		// A retry of a stored id is a success whatever the form's state now:
		// the first attempt already got its answer. It changes nothing.
		var stored string
		switch err := tx.QueryRowContext(ctx, `SELECT form FROM submissions WHERE project_id=? AND id=?`,
			sub.ProjectID, sub.ID).Scan(&stored); {
		case err == nil:
			form, err = scanForm(tx.QueryRowContext(ctx, `SELECT `+formColumns+` FROM forms WHERE project_id=? AND name=?`,
				sub.ProjectID, stored))
			return err
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO forms (project_id, name, created_at, draft_until)
			VALUES (?,?,?,?) ON CONFLICT DO NOTHING`,
			sub.ProjectID, sub.Form, sub.ReceivedAt.UTC().Format(tsFormat), n.DraftUntil.UTC().Format(tsFormat)); err != nil {
			return err
		}
		var err error
		form, err = scanForm(tx.QueryRowContext(ctx, `SELECT `+formColumns+` FROM forms WHERE project_id=? AND name=?`,
			sub.ProjectID, sub.Form))
		if err != nil {
			return err
		}
		if why := closedReason(form, sub.ReceivedAt); why != "" {
			return fmt.Errorf("%w: form %q: %s", store.ErrFormClosed, sub.Form, why)
		}
		kept := store.KeepFields(form, sub.Fields)
		if kept == nil {
			kept = map[string]string{} // fields is always a JSON object, never null
		}
		blob, err := json.Marshal(kept)
		if err != nil {
			return fmt.Errorf("submission %s fields: %w", sub.ID, err)
		}
		var visit any
		if sub.Visit != nil {
			v, err := json.Marshal(sub.Visit)
			if err != nil {
				return fmt.Errorf("submission %s visit: %w", sub.ID, err)
			}
			visit = string(v)
		}
		received := sub.ReceivedAt.UTC().Format(tsFormat)
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO submissions
			(project_id, id, form, received_at, fields, actor_kind, actor_id, host, path, via, visit)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			sub.ProjectID, sub.ID, sub.Form, received, string(blob),
			sub.ActorKind, sub.ActorID, sub.Host, sub.Path, sub.Via, visit)
		if err != nil {
			return fmt.Errorf("submission %s: %w", sub.ID, err)
		}
		if c, err := res.RowsAffected(); err != nil || c != 1 {
			return err // a retried id: nothing changes
		}
		inserted = true

		// Every name sent is recorded, the dropped ones too (spec D3).
		names := slices.Clone(form.Fields)
		for k := range sub.Fields {
			if !slices.Contains(names, k) {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		nb, err := json.Marshal(names)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE forms SET fields=?, last_submitted_at=? WHERE project_id=? AND name=?`,
			string(nb), received, sub.ProjectID, sub.Form); err != nil {
			return err
		}
		form.Fields = names
		last := sub.ReceivedAt.UTC().Truncate(time.Second)
		form.LastSubmittedAt = &last

		if form.Status != store.FormApproved {
			return nil
		}
		ev := n.Event
		// The event's shape is the store's, not the caller's: the same id,
		// project and instant as the submission (a later delete finds it
		// by them), and nothing but the form's name as attribute.
		ev.ID, ev.ProjectID, ev.TS = sub.ID, sub.ProjectID, sub.ReceivedAt
		ev.Family, ev.EventName = store.FamilyProduct, store.FormSubmitEvent
		ev.Attributes = map[string]string{"form": sub.Form}
		if ev.ReceivedAt.IsZero() {
			ev.ReceivedAt = sub.ReceivedAt
		}
		keys, err := insertEvents(ctx, tx, []store.Event{ev})
		if err != nil {
			return err
		}
		fresh = d.seen.unseen(keys)
		return writeReceived(ctx, tx, fresh)
	})
	if err != nil {
		return store.Form{}, false, err
	}
	d.seen.remember(fresh)
	return form, inserted, nil
}

// sessionGap is the idle time that ends a session, as in the session views.
const sessionGap = 30 * time.Minute

// SessionVisit snapshots the actor's current session at `at` from the raw
// views of at's day and the day before: the views, newest first, are
// walked back while each is within sessionGap of the next newer one, and
// the oldest reached is the landing view. A session whose newest view is
// more than sessionGap before `at` has ended and is nil.
func (d *DB) SessionVisit(ctx context.Context, projectID int64, actorKind, actorID string, at time.Time) (*store.Visit, error) {
	at = at.UTC()
	rows, err := d.db.QueryContext(ctx, `SELECT ts, path, referrer_source, utm_source, utm_medium, utm_campaign
		FROM raw_views
		WHERE project_id=? AND actor_kind=? AND actor_id=?
		  AND day IN (?, ?) AND ts <= ?
		ORDER BY ts DESC`,
		projectID, actorKind, actorID,
		at.Format("2006-01-02"), at.AddDate(0, 0, -1).Format("2006-01-02"), at.Format(tsFormat))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var v *store.Visit
	var newer time.Time
	for rows.Next() {
		var ts, path, ref, src, med, camp string
		if err := rows.Scan(&ts, &path, &ref, &src, &med, &camp); err != nil {
			return nil, err
		}
		t, err := time.Parse(tsFormat, ts)
		if err != nil {
			return nil, fmt.Errorf("view ts %q: %w", ts, err)
		}
		if v == nil {
			newer = at // the newest view must be within the gap of `at` too
		}
		if newer.Sub(t) > sessionGap {
			break
		}
		newer = t
		if v == nil {
			v = &store.Visit{}
		}
		v.Views++
		v.LandingPath, v.Referrer, v.UTMSource, v.UTMMedium, v.UTMCampaign = path, ref, src, med, camp
	}
	return v, rows.Err()
}

// fmtTime is t as the text the forms tables store.
func fmtTime(t time.Time) string { return t.UTC().Format(tsFormat) }

// nullTime is t as a text column value, NULL for nil.
func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

func unknownForm(projectID int64, name string) error {
	return fmt.Errorf("form %d/%s: %w", projectID, name, store.ErrNotFound)
}

func formSubject(projectID int64, name string) string {
	return fmt.Sprintf("form/%d/%s", projectID, name)
}

// ListForms lists the project's active or archived forms, drafts first,
// then by name, with their submission counts.
func (d *DB) ListForms(ctx context.Context, projectID int64, archived bool) ([]store.Form, error) {
	where := `f.archived_at IS NULL`
	if archived {
		where = `f.archived_at IS NOT NULL`
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+formColumns+`,
		(SELECT COUNT(*) FROM submissions s WHERE s.project_id=f.project_id AND s.form=f.name)
		FROM forms f WHERE f.project_id=? AND `+where+`
		ORDER BY f.status='draft' DESC, f.name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Form
	for rows.Next() {
		var n int
		f, err := scanForm(rows, &n)
		if err != nil {
			return nil, err
		}
		f.Submissions = n
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetForm reads one form, active or archived.
func (d *DB) GetForm(ctx context.Context, projectID int64, name string) (store.Form, error) {
	f, err := scanForm(d.db.QueryRowContext(ctx,
		`SELECT `+formColumns+` FROM forms WHERE project_id=? AND name=?`, projectID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Form{}, unknownForm(projectID, name)
	}
	return f, err
}

// ApproveForm makes a draft approved: expected_fields and approved_at
// set, draft_until cleared.
func (d *DB) ApproveForm(ctx context.Context, projectID int64, name string, expected []string, now time.Time, a store.AuditEntry) error {
	if expected == nil {
		expected = []string{}
	}
	blob, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE forms SET status='approved', expected_fields=?, approved_at=?, draft_until=NULL
			WHERE project_id=? AND name=? AND status='draft'`, string(blob), fmtTime(now), projectID, name)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			var status string
			switch err := tx.QueryRowContext(ctx, `SELECT status FROM forms WHERE project_id=? AND name=?`,
				projectID, name).Scan(&status); {
			case errors.Is(err, sql.ErrNoRows):
				return unknownForm(projectID, name)
			case err != nil:
				return err
			}
			return fmt.Errorf("form %d/%s is already approved: %w", projectID, name, store.ErrConflict)
		}
		return audit(ctx, tx, a)
	})
}

// UpdateForm writes the form's purpose, return URL, closing time and
// expected fields as given.
func (d *DB) UpdateForm(ctx context.Context, f store.Form, a store.AuditEntry) error {
	var expected any
	if f.ExpectedFields != nil {
		b, err := json.Marshal(f.ExpectedFields)
		if err != nil {
			return err
		}
		expected = string(b)
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE forms SET purpose=?, return_url=?, closes_at=?, expected_fields=?
			WHERE project_id=? AND name=?`,
			f.Purpose, f.ReturnURL, nullTime(f.ClosesAt), expected, f.ProjectID, f.Name)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return unknownForm(f.ProjectID, f.Name)
		}
		return audit(ctx, tx, a)
	})
}

// SetFormArchived archives or restores a form. A call that changes
// nothing (archiving an archived form, restoring an active one) is not
// an error and writes no audit row.
func (d *DB) SetFormArchived(ctx context.Context, projectID int64, name string, archived bool, draftUntil time.Time, a store.AuditEntry) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		var res sql.Result
		var err error
		if archived {
			res, err = tx.ExecContext(ctx, `UPDATE forms SET archived_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
				WHERE project_id=? AND name=? AND archived_at IS NULL`, projectID, name)
		} else {
			res, err = tx.ExecContext(ctx, `UPDATE forms SET archived_at=NULL,
				draft_until = CASE WHEN status='draft' THEN ? END
				WHERE project_id=? AND name=? AND archived_at IS NOT NULL`, fmtTime(draftUntil), projectID, name)
		}
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			var c int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM forms WHERE project_id=? AND name=?`,
				projectID, name).Scan(&c); err != nil {
				return err
			}
			if c == 0 {
				return unknownForm(projectID, name)
			}
			return nil
		}
		return audit(ctx, tx, a)
	})
}

// ExpireDrafts archives every active draft past its draft_until, in one
// transaction, with one audit row each.
func (d *DB) ExpireDrafts(ctx context.Context, now time.Time) (int, error) {
	var expired int
	err := d.tx(ctx, func(tx *sql.Tx) error {
		type key struct {
			project int64
			name    string
		}
		rows, err := tx.QueryContext(ctx, `SELECT project_id, name FROM forms
			WHERE status='draft' AND archived_at IS NULL AND draft_until <= ?
			ORDER BY project_id, name`, fmtTime(now))
		if err != nil {
			return err
		}
		var due []key
		for rows.Next() {
			var k key
			if err := rows.Scan(&k.project, &k.name); err != nil {
				rows.Close()
				return err
			}
			due = append(due, k)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, k := range due {
			if _, err := tx.ExecContext(ctx, `UPDATE forms SET archived_at=? WHERE project_id=? AND name=?`,
				fmtTime(now), k.project, k.name); err != nil {
				return err
			}
			if err := audit(ctx, tx, store.AuditEntry{
				Actor: "retention", Action: "form.expire", Subject: formSubject(k.project, k.name)}); err != nil {
				return err
			}
		}
		expired = len(due)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return expired, nil
}

// deleteEvent deletes the raw $form_submit event of the submission id
// received on day (its key is family, project, day, id), if it is still
// raw: a day already rolled up keeps its aggregates.
func deleteEvent(ctx context.Context, tx *sql.Tx, projectID int64, day, id string) error {
	_, err := tx.ExecContext(ctx,
		`DELETE FROM events WHERE family='product' AND project_id=? AND day=? AND id=? AND event_name=?`,
		projectID, day, id, store.FormSubmitEvent)
	return err
}

// DeleteSubmissions deletes the submissions with these ids and their raw
// events in one transaction.
func (d *DB) DeleteSubmissions(ctx context.Context, projectID int64, ids []string, a store.AuditEntry) (int, error) {
	var deleted int
	err := d.tx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			var day string
			switch err := tx.QueryRowContext(ctx, `SELECT substr(received_at,1,10) FROM submissions WHERE project_id=? AND id=?`,
				projectID, id).Scan(&day); {
			case errors.Is(err, sql.ErrNoRows):
				continue
			case err != nil:
				return err
			}
			if err := deleteEvent(ctx, tx, projectID, day, id); err != nil {
				return fmt.Errorf("delete event of submission %s: %w", id, err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM submissions WHERE project_id=? AND id=?`, projectID, id); err != nil {
				return fmt.Errorf("delete submission %s: %w", id, err)
			}
			deleted++
		}
		return audit(ctx, tx, a)
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// deleteFormData deletes the form's submissions with their raw events,
// then the form row, within tx. The caller audits.
func deleteFormData(ctx context.Context, tx *sql.Tx, projectID int64, name string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, substr(received_at,1,10) FROM submissions WHERE project_id=? AND form=?`,
		projectID, name)
	if err != nil {
		return err
	}
	type sub struct{ id, day string }
	var subs []sub
	for rows.Next() {
		var s sub
		if err := rows.Scan(&s.id, &s.day); err != nil {
			rows.Close()
			return err
		}
		subs = append(subs, s)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, s := range subs {
		if err := deleteEvent(ctx, tx, projectID, s.day, s.id); err != nil {
			return fmt.Errorf("delete event of submission %s: %w", s.id, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM submissions WHERE project_id=? AND form=?`, projectID, name); err != nil {
		return fmt.Errorf("delete submissions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM forms WHERE project_id=? AND name=?`, projectID, name); err != nil {
		return fmt.Errorf("delete form: %w", err)
	}
	return nil
}

// likeEscaper makes a search string literal inside LIKE ... ESCAPE '\'.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// matchSubmissions is the FROM/WHERE that selects the project's
// submissions of active forms with a field value containing search.
func matchSubmissions(projectID int64, search string) (string, []any, error) {
	if search == "" {
		return "", nil, store.Refuse(store.ErrInvalid, "search must not be empty")
	}
	return `FROM submissions s JOIN forms f ON f.project_id=s.project_id AND f.name=s.form
		WHERE s.project_id=? AND f.archived_at IS NULL
		  AND EXISTS (SELECT 1 FROM json_each(s.fields) j WHERE j.value LIKE ? ESCAPE '\')`,
		[]any{projectID, "%" + likeEscaper.Replace(search) + "%"}, nil
}

const defaultFindLimit = 100

// FindSubmissions pages the submissions matching search, newest first.
func (d *DB) FindSubmissions(ctx context.Context, projectID int64, search string, limit int, after string) ([]store.Submission, string, error) {
	from, args, err := matchSubmissions(projectID, search)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 {
		limit = defaultFindLimit
	}
	if after != "" {
		var at string
		switch err := d.db.QueryRowContext(ctx, `SELECT received_at FROM submissions WHERE project_id=? AND id=?`,
			projectID, after).Scan(&at); {
		case errors.Is(err, sql.ErrNoRows):
			return nil, "", store.Refuse(store.ErrNotFound, "unknown cursor %q", after)
		case err != nil:
			return nil, "", err
		}
		from += ` AND (s.received_at < ? OR (s.received_at = ? AND s.id < ?))`
		args = append(args, at, at, after)
	}
	rows, err := d.db.QueryContext(ctx, `SELECT s.project_id, s.id, s.form, s.received_at, s.fields,
		s.actor_kind, s.actor_id, s.host, s.path, s.via, s.visit `+from+`
		ORDER BY s.received_at DESC, s.id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []store.Submission
	for rows.Next() {
		var s store.Submission
		var received, fields string
		var visit sql.NullString
		if err := rows.Scan(&s.ProjectID, &s.ID, &s.Form, &received, &fields,
			&s.ActorKind, &s.ActorID, &s.Host, &s.Path, &s.Via, &visit); err != nil {
			return nil, "", err
		}
		if s.ReceivedAt, err = time.Parse(tsFormat, received); err != nil {
			return nil, "", fmt.Errorf("submission %s received_at: %w", s.ID, err)
		}
		if err := json.Unmarshal([]byte(fields), &s.Fields); err != nil {
			return nil, "", fmt.Errorf("submission %s fields: %w", s.ID, err)
		}
		if visit.Valid {
			s.Visit = &store.Visit{}
			if err := json.Unmarshal([]byte(visit.String), s.Visit); err != nil {
				return nil, "", fmt.Errorf("submission %s visit: %w", s.ID, err)
			}
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var next string
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}

// SubmissionIDsMatching returns every id FindSubmissions would, newest first.
func (d *DB) SubmissionIDsMatching(ctx context.Context, projectID int64, search string) ([]string, error) {
	from, args, err := matchSubmissions(projectID, search)
	if err != nil {
		return nil, err
	}
	rows, err := d.db.QueryContext(ctx, `SELECT s.id `+from+` ORDER BY s.received_at DESC, s.id DESC`, args...)
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
