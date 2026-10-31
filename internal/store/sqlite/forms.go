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
// `before` and not yet attributed, oldest first. A row whose pending event
// does not read is listed with Err set (and a zero Event): it counts
// toward limit, and the job clears it, so one bad row cannot hold the
// queue.
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
		p.ConnectionID = connection.String
		if err := json.Unmarshal([]byte(ev), &p.Event); err != nil {
			p.Event, p.Err = store.Event{}, fmt.Errorf("submission %s pending event: %w", p.ID, err)
		} else {
			p.WriteEvent = write == 1
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// AttributeSubmission stores ev's referrer and UTMs as p's attribution
// and, when p.WriteEvent, leaves ev in the outbox (unlogged_event) for
// the job to append to the event files, in one statement. The event is
// never a row of SQLite's events. A submission no longer pending changes
// nothing.
func (d *DB) AttributeSubmission(ctx context.Context, p store.PendingSubmission, ev store.Event) error {
	var attribution any // NULL: nothing found
	if a := (store.Attribution{Referrer: ev.ReferrerSource, UTMSource: ev.UTMSource,
		UTMMedium: ev.UTMMedium, UTMCampaign: ev.UTMCampaign}); a != (store.Attribution{}) {
		b, err := json.Marshal(a)
		if err != nil {
			return err
		}
		attribution = string(b)
	}
	var unlogged any // NULL: no event to log
	if p.WriteEvent {
		b, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("submission %s event: %w", p.ID, err)
		}
		unlogged = string(b)
	}
	_, err := d.db.ExecContext(ctx, `UPDATE submissions SET attribution=?, unlogged_event=?, pending_event=NULL,
		pending_write=NULL, pending_connection=NULL WHERE project_id=? AND id=? AND pending_event IS NOT NULL`,
		attribution, unlogged, p.ProjectID, p.ID)
	return err
}

// UnloggedEvents lists up to limit events AttributeSubmission wrote and
// MarkLogged has not cleared, oldest first. A row whose event does not
// read is listed with Err set and a zero Event, so the job can mark it.
func (d *DB) UnloggedEvents(ctx context.Context, limit int) ([]store.UnloggedEvent, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT project_id, id, unlogged_event FROM submissions
		WHERE unlogged_event IS NOT NULL ORDER BY received_at, project_id, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.UnloggedEvent
	for rows.Next() {
		var u store.UnloggedEvent
		var ev string
		if err := rows.Scan(&u.ProjectID, &u.ID, &ev); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(ev), &u.Event); err != nil {
			u.Event, u.Err = store.Event{}, fmt.Errorf("submission %s unlogged event: %w", u.ID, err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// UnloggedCount is how many events wait in the outbox, counted over its
// partial index.
func (d *DB) UnloggedCount(ctx context.Context) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM submissions WHERE unlogged_event IS NOT NULL`).Scan(&n)
	return n, err
}

// MarkLogged takes these (ProjectID, ID) events out of the outbox, in one
// transaction. A key no longer in it changes nothing.
func (d *DB) MarkLogged(ctx context.Context, keys []store.UnloggedEvent) error {
	if len(keys) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		for _, k := range keys {
			if _, err := tx.ExecContext(ctx, `UPDATE submissions SET unlogged_event=NULL WHERE project_id=? AND id=?`,
				k.ProjectID, k.ID); err != nil {
				return fmt.Errorf("mark event of submission %s logged: %w", k.ID, err)
			}
		}
		return nil
	})
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
// then by name, with their active, new and archived submission counts.
func (d *DB) ListForms(ctx context.Context, projectID int64, archived bool) ([]store.Form, error) {
	where := `f.archived_at IS NULL`
	if archived {
		where = `f.archived_at IS NOT NULL`
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+formColumns+`,
		(SELECT COUNT(*) FROM submissions s WHERE s.project_id=f.project_id AND s.form=f.name
		   AND s.archived_at IS NULL),
		(SELECT COUNT(*) FROM submissions s WHERE s.project_id=f.project_id AND s.form=f.name
		   AND s.archived_at IS NULL AND s.received_at > COALESCE(f.seen_at, '')),
		(SELECT COUNT(*) FROM submissions s WHERE s.project_id=f.project_id AND s.form=f.name
		   AND s.archived_at IS NOT NULL)
		FROM forms f WHERE f.project_id=? AND `+where+`
		ORDER BY f.status='draft' DESC, f.name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Form
	for rows.Next() {
		var n, fresh, archived int
		f, err := scanForm(rows, &n, &fresh, &archived)
		if err != nil {
			return nil, err
		}
		f.Submissions, f.NewSubmissions, f.ArchivedSubmissions = n, fresh, archived
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
// set, draft_until cleared, and an expected name nothing sent yet joins
// fields.
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
		if err := addFormFields(ctx, tx, projectID, name, expected); err != nil {
			return err
		}
		return audit(ctx, tx, a)
	})
}

// UpdateForm writes the form's purpose, return URL and closing time as
// given, and its expected fields when f.ExpectedFields is non-nil; nil
// leaves the stored list alone, so a caller that read the form before an
// approval cannot wipe the list the approval set. Every expected name and
// every name in add joins fields, and add joins an approved form's
// expected fields, in the same transaction.
func (d *DB) UpdateForm(ctx context.Context, f store.Form, add []string, a store.AuditEntry) error {
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
		if names := append(slices.Clone(f.ExpectedFields), add...); len(names) > 0 {
			if err := addFormFields(ctx, tx, f.ProjectID, f.Name, names); err != nil {
				return err
			}
		}
		return audit(ctx, tx, a)
	})
}

// addFormFields records names as sent to the form (fields stays sorted)
// and appends the ones an approved form does not keep yet to its expected
// fields, in the order given. fields thus always holds every expected
// name, also one no submission has sent.
func addFormFields(ctx context.Context, tx *sql.Tx, projectID int64, name string, add []string) error {
	form, err := scanForm(tx.QueryRowContext(ctx, `SELECT `+formColumns+` FROM forms WHERE project_id=? AND name=?`,
		projectID, name))
	if err != nil {
		return err
	}
	names, expected := slices.Clone(form.Fields), form.ExpectedFields
	for _, n := range add {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
		if form.Status == store.FormApproved && !slices.Contains(expected, n) {
			expected = append(expected, n)
		}
	}
	sort.Strings(names)
	nb, err := json.Marshal(names)
	if err != nil {
		return err
	}
	var eb any // NULL while draft
	if form.Status == store.FormApproved {
		b, err := json.Marshal(expected)
		if err != nil {
			return err
		}
		eb = string(b)
	}
	_, err = tx.ExecContext(ctx, `UPDATE forms SET fields=?, expected_fields=? WHERE project_id=? AND name=?`,
		string(nb), eb, projectID, name)
	return err
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

// SetSubmissionsArchived archives or restores the submissions with these
// ids in one transaction, and audits it with a.Detail plus the count it
// changed, " (<n> archived)" or " (<n> restored)", 0 included. Their
// $form_submit events stay in the event files, which hold no field.
func (d *DB) SetSubmissionsArchived(ctx context.Context, projectID int64, ids []string, archived bool, a store.AuditEntry) (int, error) {
	q, verb := `UPDATE submissions SET archived_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
		WHERE project_id=? AND id=? AND archived_at IS NULL`, "archived"
	if !archived {
		q, verb = `UPDATE submissions SET archived_at=NULL WHERE project_id=? AND id=? AND archived_at IS NOT NULL`, "restored"
	}
	var changed int
	err := d.tx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			res, err := tx.ExecContext(ctx, q, projectID, id)
			if err != nil {
				return fmt.Errorf("%s submission %s: %w", verb, id, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			changed += int(n)
		}
		a.Detail += fmt.Sprintf(" (%d %s)", changed, verb)
		return audit(ctx, tx, a)
	})
	if err != nil {
		return 0, err
	}
	return changed, nil
}

// deleteFormData deletes the form's submissions, then the form row,
// within tx. The caller audits.
func deleteFormData(ctx context.Context, tx *sql.Tx, projectID int64, name string) error {
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
// submissions with a field value containing search, archived ones and
// archived forms' too: an erasure request must reach every copy of a
// person's data.
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
const submissionColumns = `s.project_id, s.id, s.form, s.received_at, s.fields, s.attribution, s.archived_at`

// scanSubmission reads one row selected with submissionColumns, then
// into extra any columns selected after them.
func scanSubmission(row interface{ Scan(...any) error }, extra ...any) (store.Submission, error) {
	var s store.Submission
	var received, fields string
	var attribution, archived sql.NullString
	if err := row.Scan(append([]any{&s.ProjectID, &s.ID, &s.Form, &received, &fields,
		&attribution, &archived}, extra...)...); err != nil {
		return store.Submission{}, err
	}
	var err error
	if s.ReceivedAt, err = time.Parse(tsFormat, received); err != nil {
		return store.Submission{}, fmt.Errorf("submission %s received_at: %w", s.ID, err)
	}
	if archived.Valid {
		at, err := time.Parse(tsFormat, archived.String)
		if err != nil {
			return store.Submission{}, fmt.Errorf("submission %s archived_at: %w", s.ID, err)
		}
		s.ArchivedAt = &at
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

// GetSubmission reads one submission of an active form, archived or not.
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
// archived ones and archived forms' included, each with ArchivedAt and
// FormArchived.
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
		var formArchived bool
		s, err := scanSubmission(rows, &formArchived)
		if err != nil {
			return nil, "", err
		}
		s.FormArchived = formArchived
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
	return queryColumn[string](ctx, d.db, `SELECT s.id `+from+` ORDER BY s.received_at DESC, s.id DESC`, args...)
}
