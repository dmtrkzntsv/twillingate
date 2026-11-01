// Purge of archived projects, dashboards, widgets, widget shares, forms
// and submissions past RETENTION_ARCHIVED_DAYS (spec 2026-09-25, migration
// 021 onward; shares from migration 032, spec 2026-10-05; forms from
// migration 035; submissions from migration 043). Run by
// the daily pass (internal/jobs), never by a request handler: there is no
// tool or route for it.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// PurgeArchived deletes every project, dashboard, widget, widget share,
// form and submission archived more than days ago, each in its own
// transaction with an audit row (actor "retention"). days <= 0 purges
// nothing. A share or form purged with its project (deleteProject) gets no row of its own. A form
// goes with its submissions (deleteFormData); submissions archived on
// their own are deleted per project, one audit row each with the count.
// Their $form_submit events stay in the event files.
//
// A project's archived_at is written with datetime('now') ("YYYY-MM-DD
// HH:MM:SS"); dashboards', widgets' and shares' with strftime(...,'Z')
// (RFC3339).
// julianday() parses both, so one predicate shape serves every kind.
//
// Dashboards (and, transitively, widgets) are restricted to owner='user':
// a system dashboard is never archived (it is release-managed, via
// SyncReporting), so it — and anything on it — must never be selected here
// even if a row were forced into that state. Widgets are further
// restricted to those whose own dashboard is not itself being purged in
// this same pass — a purged dashboard takes its widgets by cascade
// (ON DELETE CASCADE), so purging them again here would be redundant, not
// wrong, but the audit trail would then carry two rows for one deletion.
//
// One item's failure does not stop the rest: the store has no logger to
// report it through (that belongs to the caller, internal/jobs), so a
// failed item's error is joined into the one returned, but every other
// item is still attempted and, on success, still counted in the result.
func (d *DB) PurgeArchived(ctx context.Context, days int) (store.PurgeResult, error) {
	var res store.PurgeResult
	if days <= 0 {
		return res, nil
	}
	var errs error

	projectIDs, err := queryColumn[int64](ctx, d.db,
		`SELECT id FROM projects
		 WHERE archived_at IS NOT NULL AND julianday(archived_at) < julianday('now') - ?`, days)
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("select archived projects: %w", err))
	}
	res.Projects = purgeEach(ctx, d, &errs, "project", projectIDs, func(tx *sql.Tx, id int64) error {
		if err := deleteProject(ctx, tx, id); err != nil {
			return err
		}
		return auditAndBump(ctx, tx, store.AuditEntry{
			Actor: "retention", Action: "project.purge", Subject: strconv.FormatInt(id, 10)})
	})

	dashboardIDs, err := queryColumn[int64](ctx, d.db,
		`SELECT id FROM dashboards
		 WHERE owner='user' AND archived_at IS NOT NULL
		   AND julianday(archived_at) < julianday('now') - ?`, days)
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("select archived dashboards: %w", err))
	}
	res.Dashboards = purgeEach(ctx, d, &errs, "dashboard", dashboardIDs, func(tx *sql.Tx, id int64) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM dashboards WHERE id=?`, id); err != nil {
			return fmt.Errorf("delete dashboard %d: %w", id, err)
		}
		return audit(ctx, tx, store.AuditEntry{
			Actor: "retention", Action: "dashboard.purge", Subject: fmt.Sprintf("dashboard/%d", id)})
	})

	// The join with dashboards restricts every widget considered here to
	// one on a user-owned dashboard (defense in depth: production code
	// never archives a widget on a system dashboard either, but a forced
	// row must still never be selected), and d's own archived state tells
	// whether that dashboard is being purged in this same pass.
	widgetIDs, err := queryColumn[int64](ctx, d.db,
		`SELECT w.id FROM widgets w JOIN dashboards d ON d.id = w.dashboard_id
		 WHERE d.owner='user'
		   AND w.archived_at IS NOT NULL AND julianday(w.archived_at) < julianday('now') - ?
		   AND NOT (d.archived_at IS NOT NULL AND julianday(d.archived_at) < julianday('now') - ?)`,
		days, days)
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("select archived widgets: %w", err))
	}
	res.Widgets = purgeEach(ctx, d, &errs, "widget", widgetIDs, func(tx *sql.Tx, id int64) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM widgets WHERE id=?`, id); err != nil {
			return fmt.Errorf("delete widget %d: %w", id, err)
		}
		return audit(ctx, tx, store.AuditEntry{
			Actor: "retention", Action: "widget.purge", Subject: fmt.Sprintf("widget/%d", id)})
	})

	shareIDs, err := queryColumn[string](ctx, d.db,
		`SELECT id FROM widget_shares
		 WHERE archived_at IS NOT NULL AND julianday(archived_at) < julianday('now') - ?`, days)
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("select archived widget shares: %w", err))
	}
	res.WidgetShares = purgeEach(ctx, d, &errs, "widget share", shareIDs, func(tx *sql.Tx, id string) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM widget_shares WHERE id=?`, id); err != nil {
			return fmt.Errorf("delete widget share %s: %w", id, err)
		}
		return audit(ctx, tx, store.AuditEntry{
			Actor: "retention", Action: "widget_share.purge", Subject: "widget_share/" + id})
	})

	formRows, err := d.db.QueryContext(ctx,
		`SELECT project_id, name FROM forms
		 WHERE archived_at IS NOT NULL AND julianday(archived_at) < julianday('now') - ?
		 ORDER BY project_id, name`, days)
	var forms []formKey
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("select archived forms: %w", err))
	} else {
		for formRows.Next() {
			var k formKey
			if err := formRows.Scan(&k.project, &k.name); err != nil {
				errs = errors.Join(errs, fmt.Errorf("select archived forms: %w", err))
				break
			}
			forms = append(forms, k)
		}
		errs = errors.Join(errs, formRows.Err(), formRows.Close())
	}
	// A form purged with its project above is already gone; its rows
	// were deleted with the project's, so it is not selected here.
	for _, k := range purgeEach(ctx, d, &errs, "form", forms, func(tx *sql.Tx, k formKey) error {
		if err := deleteFormData(ctx, tx, k.project, k.name); err != nil {
			return err
		}
		return audit(ctx, tx, store.AuditEntry{
			Actor: "retention", Action: "form.purge", Subject: formSubject(k.project, k.name)})
	}) {
		res.Forms = append(res.Forms, k.String())
	}

	// Submissions archived on their own, one transaction and audit row per
	// project. Those of a form or project purged above went with it. One
	// whose $form_submit event still waits (pending_event, unlogged_event)
	// holds its only copy: it waits for a later pass, once the attribution
	// job has written it to the event files, so the conversion stays.
	const archivedSubmissions = `archived_at IS NOT NULL AND julianday(archived_at) < julianday('now') - ?
		AND pending_event IS NULL AND unlogged_event IS NULL`
	subProjects, err := queryColumn[int64](ctx, d.db,
		`SELECT DISTINCT project_id FROM submissions WHERE `+archivedSubmissions+` ORDER BY project_id`, days)
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("select archived submissions: %w", err))
	}
	purged := map[int64]int64{}
	for _, id := range purgeEach(ctx, d, &errs, "archived submissions of project", subProjects, func(tx *sql.Tx, id int64) error {
		r, err := tx.ExecContext(ctx, `DELETE FROM submissions WHERE project_id=? AND `+archivedSubmissions, id, days)
		if err != nil {
			return fmt.Errorf("delete submissions: %w", err)
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		purged[id] = n
		return audit(ctx, tx, store.AuditEntry{Actor: "retention", Action: "submission.purge",
			Subject: "project/" + strconv.FormatInt(id, 10), Detail: fmt.Sprintf("%d purged", n)})
	}) {
		res.Submissions += int(purged[id])
	}

	return res, errs
}

// formKey names one form; it prints as project/name.
type formKey struct {
	project int64
	name    string
}

func (k formKey) String() string { return fmt.Sprintf("%d/%s", k.project, k.name) }

// purgeEach runs del on each of keys, each in its own transaction, and
// returns the keys that went, in order. A failure is joined into errs as
// "purge <what> <key>: ..." and the rest are still attempted.
func purgeEach[K any](ctx context.Context, d *DB, errs *error, what string, keys []K, del func(*sql.Tx, K) error) []K {
	var done []K
	for _, k := range keys {
		if err := d.tx(ctx, func(tx *sql.Tx) error { return del(tx, k) }); err != nil {
			*errs = errors.Join(*errs, fmt.Errorf("purge %s %v: %w", what, k, err))
			continue
		}
		done = append(done, k)
	}
	return done
}
