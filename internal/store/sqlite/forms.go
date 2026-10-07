package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// formColumns is the select list scanForm reads, in order.
const formColumns = `project_id, name, status, purpose, return_url, fields, expected_fields,
	created_at, draft_until, approved_at, closes_at, last_submitted_at, archived_at`

// scanForm reads one forms row selected with formColumns.
func scanForm(row interface{ Scan(...any) error }) (store.Form, error) {
	var f store.Form
	var fields string
	var expected, created, draft, approved, closes, last, archived sql.NullString
	if err := row.Scan(&f.ProjectID, &f.Name, &f.Status, &f.Purpose, &f.ReturnURL, &fields, &expected,
		&created, &draft, &approved, &closes, &last, &archived); err != nil {
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
