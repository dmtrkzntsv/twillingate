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
	created_at, draft_until, approved_at, closes_at, last_submitted_at, archived_at, seen_at`

// scanForm reads one forms row selected with formColumns; extra receives
// any columns selected after them.
func scanForm(row interface{ Scan(...any) error }, extra ...any) (store.Form, error) {
	var f store.Form
	var fields string
	var expected, created, draft, approved, closes, last, archived, seen sql.NullString
	dest := append([]any{&f.ProjectID, &f.Name, &f.Status, &f.Purpose, &f.ReturnURL, &fields, &expected,
		&created, &draft, &approved, &closes, &last, &archived, &seen}, extra...)
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
		{last, &f.LastSubmittedAt}, {archived, &f.ArchivedAt}, {seen, &f.SeenAt}} {
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
// the $form_submit event it will be written as, in one transaction. The
// event waits with the submission (pending_event) until AttributeSubmission
// writes it, on an approved form. The refusal and the duplicate id are
// decided before anything else is written, so neither changes the form; a
// refusal returns the form beside ErrFormClosed. The event's id, ts and
// project are the submission's, whatever n.Event carries, so a later delete
// finds it by (family product, project_id, day, id).
func (d *DB) WriteSubmission(ctx context.Context, n store.NewSubmission) (store.Form, bool, error) {
	sub := n.Submission
	var form store.Form
	var inserted bool
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
		var attribution any // NULL when no visit matched
		if sub.Attribution != (store.Attribution{}) {
			a, err := json.Marshal(sub.Attribution)
			if err != nil {
				return fmt.Errorf("submission %s attribution: %w", sub.ID, err)
			}
			attribution = string(a)
		}
		received := sub.ReceivedAt.UTC().Format(tsFormat)
		pending, err := json.Marshal(n.StoredEvent())
		if err != nil {
			return fmt.Errorf("submission %s event: %w", sub.ID, err)
		}
		var connection any // NULL when ingest had none
		if n.ConnectionID != "" {
			connection = n.ConnectionID
		}
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO submissions
			(project_id, id, form, received_at, fields, attribution, pending_event, pending_write, pending_connection)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			sub.ProjectID, sub.ID, sub.Form, received, string(blob), attribution, string(pending),
			form.Status == store.FormApproved, connection)
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

		return nil
	})
	if errors.Is(err, store.ErrFormClosed) {
		// The form as the transaction read it (a row it created is
		// rolled back with it): ingest reads its return_url to send the
		// visitor back with the error fragment.
		return form, false, err
	}
	if err != nil {
		return store.Form{}, false, err
	}
	return form, inserted, nil
}

// PendingSubmissions lists up to limit submissions received at or before
// `before` and not yet attributed, oldest first.
func (d *DB) PendingSubmissions(ctx context.Context, before time.Time, limit int) ([]store.PendingSubmission, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT project_id, id, received_at, pending_event, pending_write, pending_connection
		FROM submissions WHERE pending_event IS NOT NULL AND received_at <= ?
		ORDER BY received_at, project_id, id LIMIT ?`, fmtTime(before), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.PendingSubmission
	for rows.Next() {
		var p store.PendingSubmission
		var received, ev string
		var write int64
		var connection sql.NullString
		if err := rows.Scan(&p.ProjectID, &p.ID, &received, &ev, &write, &connection); err != nil {
			return nil, err
		}
		if p.ReceivedAt, err = time.Parse(tsFormat, received); err != nil {
			return nil, fmt.Errorf("submission %s received_at %q: %w", p.ID, received, err)
		}
		if err := json.Unmarshal([]byte(ev), &p.Event); err != nil {
			return nil, fmt.Errorf("submission %s pending event: %w", p.ID, err)
		}
		p.WriteEvent, p.ConnectionID = write == 1, connection.String
		out = append(out, p)
	}
	return out, rows.Err()
}

// AttributeSubmission stores ev's referrer and UTMs as p's attribution
// and, when p.WriteEvent, writes ev, in one transaction; wrote reports
// the event written. A submission no longer pending changes nothing.
func (d *DB) AttributeSubmission(ctx context.Context, p store.PendingSubmission, ev store.Event) (bool, error) {
	var attribution any // NULL: nothing found
	if a := (store.Attribution{Referrer: ev.ReferrerSource, UTMSource: ev.UTMSource,
		UTMMedium: ev.UTMMedium, UTMCampaign: ev.UTMCampaign}); a != (store.Attribution{}) {
		b, err := json.Marshal(a)
		if err != nil {
			return false, err
		}
		attribution = string(b)
	}
	var wrote bool
	var fresh []receivedKey
	var gen uint64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE submissions SET attribution=?, pending_event=NULL, pending_write=NULL,
			pending_connection=NULL WHERE project_id=? AND id=? AND pending_event IS NOT NULL`, attribution, p.ProjectID, p.ID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return err // deleted or attributed already
		}
		if !p.WriteEvent {
			return nil
		}
		keys, err := insertEvents(ctx, tx, []store.Event{ev})
		if err != nil {
			return err
		}
		fresh, gen = d.seen.unseen(keys)
		wrote = true
		return writeReceived(ctx, tx, fresh)
	})
	if err != nil {
		return false, err
	}
	if wrote {
		d.seen.remember(fresh, gen)
	}
	return wrote, nil
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
		(SELECT COUNT(*) FROM submissions s WHERE s.project_id=f.project_id AND s.form=f.name),
		(SELECT COUNT(*) FROM submissions s WHERE s.project_id=f.project_id AND s.form=f.name
		   AND s.received_at > COALESCE(f.seen_at, ''))
		FROM forms f WHERE f.project_id=? AND `+where+`
		ORDER BY f.status='draft' DESC, f.name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Form
	for rows.Next() {
		var n, fresh int
		f, err := scanForm(rows, &n, &fresh)
		if err != nil {
			return nil, err
		}
		f.Submissions, f.NewSubmissions = n, fresh
		out = append(out, f)
	}
	return out, rows.Err()
}

// MarkFormSeen records that the console has read a form's submissions up
// to until (spec 2026-10-08 D5). seen_at only moves forward, so a page
// opened earlier can't unread what a later one read. Console state: not
// audited.
func (d *DB) MarkFormSeen(ctx context.Context, projectID int64, name string, until time.Time) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE forms SET seen_at = MAX(COALESCE(seen_at, ''), ?) WHERE project_id=? AND name=?`,
		until.UTC().Format(tsFormat), projectID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return unknownForm(projectID, name)
	}
	return nil
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

// UpdateForm writes the form's purpose, return URL and closing time as
// given, and its expected fields when f.ExpectedFields is non-nil; nil
// leaves the stored list alone, so a caller that read the form before an
// approval cannot wipe the list the approval set.
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
		res, err := tx.ExecContext(ctx, `UPDATE forms SET purpose=?, return_url=?, closes_at=?, expected_fields=COALESCE(?, expected_fields)
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
// events in one transaction, and audits it with a.Detail plus the count
// it deleted, " (<n> deleted)", 0 included.
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
		a.Detail += fmt.Sprintf(" (%d deleted)", deleted)
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
// submissions with a field value containing search, archived forms' too:
// an erasure request must reach every copy of a person's data.
func matchSubmissions(projectID int64, search string) (string, []any, error) {
	if search == "" {
		return "", nil, store.Refuse(store.ErrInvalid, "search must not be empty")
	}
	return `FROM submissions s JOIN forms f ON f.project_id=s.project_id AND f.name=s.form
		WHERE s.project_id=?
		  AND EXISTS (SELECT 1 FROM json_each(s.fields) j WHERE j.value LIKE ? ESCAPE '\')`,
		[]any{projectID, "%" + likeEscaper.Replace(search) + "%"}, nil
}

const defaultFindLimit = 100

// submissionColumns is the select list scanSubmission reads, from
// submissions aliased s.
const submissionColumns = `s.project_id, s.id, s.form, s.received_at, s.fields, s.attribution`

// scanSubmission reads one row selected with submissionColumns, then
// into extra any columns selected after them.
func scanSubmission(row interface{ Scan(...any) error }, extra ...any) (store.Submission, error) {
	var s store.Submission
	var received, fields string
	var attribution sql.NullString
	if err := row.Scan(append([]any{&s.ProjectID, &s.ID, &s.Form, &received, &fields,
		&attribution}, extra...)...); err != nil {
		return store.Submission{}, err
	}
	var err error
	if s.ReceivedAt, err = time.Parse(tsFormat, received); err != nil {
		return store.Submission{}, fmt.Errorf("submission %s received_at: %w", s.ID, err)
	}
	if err := json.Unmarshal([]byte(fields), &s.Fields); err != nil {
		return store.Submission{}, fmt.Errorf("submission %s fields: %w", s.ID, err)
	}
	if attribution.Valid {
		if err := json.Unmarshal([]byte(attribution.String), &s.Attribution); err != nil {
			return store.Submission{}, fmt.Errorf("submission %s attribution: %w", s.ID, err)
		}
	}
	return s, nil
}

// GetSubmission reads one submission of an active form.
func (d *DB) GetSubmission(ctx context.Context, projectID int64, form, id string) (store.Submission, error) {
	s, err := scanSubmission(d.db.QueryRowContext(ctx, `SELECT `+submissionColumns+`
		FROM submissions s JOIN forms f ON f.project_id=s.project_id AND f.name=s.form
		WHERE s.project_id=? AND s.form=? AND s.id=? AND f.archived_at IS NULL`, projectID, form, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Submission{}, store.Refuse(store.ErrNotFound,
			"no submission %q in form %q of project %d (an archived form's submissions are hidden)", id, form, projectID)
	}
	return s, err
}

// FindSubmissions pages the submissions matching search, newest first,
// archived forms' included and marked Archived.
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
	rows, err := d.db.QueryContext(ctx, `SELECT `+submissionColumns+`, f.archived_at IS NOT NULL `+from+`
		ORDER BY s.received_at DESC, s.id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []store.Submission
	for rows.Next() {
		var archived bool
		s, err := scanSubmission(rows, &archived)
		if err != nil {
			return nil, "", err
		}
		s.Archived = archived
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
