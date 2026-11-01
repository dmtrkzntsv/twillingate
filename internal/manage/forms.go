package manage

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/wire"
)

const (
	minSearchLen     = 2
	defaultFindLimit = 100
	maxFindLimit     = 500
)

// FormSpec is the caller's view of a form edit. A nil field keeps the
// current value; ClosesAt is a pointer to a pointer so that a non-nil
// pointer to nil reopens the form.
type FormSpec struct {
	Purpose        *string
	ReturnURL      *string
	ClosesAt       **time.Time
	ExpectedFields *[]string
	// AddFields are field names to record before any submission sends
	// them; an approved form also keeps them, after ExpectedFields.
	AddFields []string
}

func formSubject(projectID int64, name string) string {
	return "form/" + strconv.FormatInt(projectID, 10) + "/" + name
}

func (o *Ops) requireProject(ctx context.Context, projectID int64) error {
	if o.Reg.Snapshot(ctx).Project(projectID) == nil {
		return fmt.Errorf("unknown project %d: %w", projectID, ErrNotFound)
	}
	return nil
}

// checkExpected validates a field list kept by an approved form: every name
// non-empty, at most 64 characters, free of control characters (U+0000 to
// U+001F, U+007F, which ingest drops) and not starting with "$" (the
// context keys). Duplicates collapse. An empty list is the caller's to refuse.
func checkExpected(expected []string) ([]string, error) {
	for _, n := range expected {
		switch {
		case n == "":
			return nil, fmt.Errorf("%w: expected fields must not contain an empty name", ErrInvalid)
		case len([]rune(n)) > wire.MaxFormFieldName:
			return nil, fmt.Errorf("%w: expected field %q is longer than %d characters", ErrInvalid, n, wire.MaxFormFieldName)
		case wire.HasControl(n):
			return nil, fmt.Errorf("%w: expected field %q holds a control character; no submitted field does", ErrInvalid, n)
		case strings.HasPrefix(n, "$"):
			return nil, fmt.Errorf("%w: expected field %q starts with $, which is reserved for context keys; drop it", ErrInvalid, n)
		}
	}
	return dedupe(expected), nil
}

// draftDays is Ops.FormDraftDays, or RETENTION_FORMS_DRAFT_DAYS' default
// when the caller left it unset.
func (o *Ops) draftDays() int {
	if o.FormDraftDays < 1 {
		return config.DefaultFormDraftDays
	}
	return o.FormDraftDays
}

func (o *Ops) ListForms(ctx context.Context, projectID int64, archived bool) ([]store.Form, error) {
	if err := o.requireProject(ctx, projectID); err != nil {
		return nil, err
	}
	return o.St.ListForms(ctx, projectID, archived)
}

// ApproveForm turns a draft into an approved form that keeps the expected
// fields. It never touches closes_at: a form closed before approval stays
// closed.
func (o *Ops) ApproveForm(ctx context.Context, actor string, projectID int64, name string, expected []string) error {
	if err := o.requireProject(ctx, projectID); err != nil {
		return err
	}
	if len(expected) == 0 {
		return fmt.Errorf("%w: expected must name at least one field to keep", ErrInvalid)
	}
	expected, err := checkExpected(expected)
	if err != nil {
		return err
	}
	f, err := o.St.GetForm(ctx, projectID, name)
	if err != nil {
		return err
	}
	if f.ArchivedAt != nil {
		return fmt.Errorf("%w: form %q is archived; restore it first", ErrInvalid, name)
	}
	return o.St.ApproveForm(ctx, projectID, name, expected, o.now(), store.AuditEntry{
		Actor: actor, Action: "form.approve", Subject: formSubject(projectID, name),
		Detail: "expected: " + strings.Join(expected, ", ")})
}

// UpdateForm merges spec over the stored form and writes the result.
func (o *Ops) UpdateForm(ctx context.Context, actor string, projectID int64, name string, spec FormSpec) error {
	snap := o.Reg.Snapshot(ctx)
	if snap.Project(projectID) == nil {
		return fmt.Errorf("unknown project %d: %w", projectID, ErrNotFound)
	}
	f, err := o.St.GetForm(ctx, projectID, name)
	if err != nil {
		return err
	}
	// Nil leaves the stored list alone, so an approval that lands between
	// the read above and the write below is not overwritten.
	f.ExpectedFields = nil
	var changed []string
	if spec.Purpose != nil {
		f.Purpose = *spec.Purpose
		changed = append(changed, "purpose")
	}
	if spec.ReturnURL != nil {
		if u := *spec.ReturnURL; u != "" && !snap.RedirectAllowed(projectID, u, "") {
			return fmt.Errorf("%w: return_url must be an absolute http(s) URL whose origin is in the project's allowed_origins (a bare * does not count)", ErrInvalid)
		}
		f.ReturnURL = *spec.ReturnURL
		changed = append(changed, "return_url")
	}
	if spec.ClosesAt != nil {
		f.ClosesAt = *spec.ClosesAt
		changed = append(changed, "closes_at")
	}
	if spec.ExpectedFields != nil {
		if f.Status == store.FormDraft {
			return fmt.Errorf("%w: form %q is a draft and has no expected fields; approve it instead", ErrInvalid, name)
		}
		if len(*spec.ExpectedFields) == 0 {
			return fmt.Errorf("%w: expected_fields must name at least one field; archive the form to stop it taking submissions", ErrInvalid)
		}
		exp, err := checkExpected(*spec.ExpectedFields)
		if err != nil {
			return err
		}
		f.ExpectedFields = exp
		changed = append(changed, "expected_fields")
	}
	var add []string
	if len(spec.AddFields) > 0 {
		if add, err = checkExpected(spec.AddFields); err != nil {
			return err
		}
		changed = append(changed, "added: "+strings.Join(add, ", "))
	}
	return o.St.UpdateForm(ctx, f, add, store.AuditEntry{
		Actor: actor, Action: "form.update", Subject: formSubject(projectID, name),
		Detail: strings.Join(changed, ", ")})
}

// MarkFormSeen moves how far the console has read a form forward to until
// (spec 2026-10-08 D5). Console state, not a change to the project: no
// actor, no audit. A mark past now is clamped to now: the store only moves
// seen_at forward, so a far-future time would otherwise hide every later
// submission for good.
func (o *Ops) MarkFormSeen(ctx context.Context, projectID int64, name string, until time.Time) error {
	if err := o.requireProject(ctx, projectID); err != nil {
		return err
	}
	if now := o.now().UTC(); until.After(now) {
		until = now
	}
	return o.St.MarkFormSeen(ctx, projectID, name, until)
}

func (o *Ops) ArchiveForm(ctx context.Context, actor string, projectID int64, name string) error {
	if err := o.requireProject(ctx, projectID); err != nil {
		return err
	}
	return o.St.SetFormArchived(ctx, projectID, name, true, time.Time{}, store.AuditEntry{
		Actor: actor, Action: "form.archive", Subject: formSubject(projectID, name)})
}

// RestoreForm un-archives a form. A draft gets a fresh draft_until of now
// plus RETENTION_FORMS_DRAFT_DAYS.
func (o *Ops) RestoreForm(ctx context.Context, actor string, projectID int64, name string) error {
	if err := o.requireProject(ctx, projectID); err != nil {
		return err
	}
	until := o.now().Add(time.Duration(o.draftDays()) * 24 * time.Hour)
	return o.St.SetFormArchived(ctx, projectID, name, false, until, store.AuditEntry{
		Actor: actor, Action: "form.restore", Subject: formSubject(projectID, name)})
}

// ArchiveSubmissions archives the submissions with these ids: they leave
// their form's table and are purged RETENTION_ARCHIVED_DAYS later, an
// erasure request included. selector says how the caller chose them
// ("ids", "search", "filters: <form>"), never what it matched on; the
// store adds the count it archived to the audit detail. Archiving nothing
// (no ids, or ids already archived or gone) is audited too, with 0: it is
// still an erasure request answered.
func (o *Ops) ArchiveSubmissions(ctx context.Context, actor string, projectID int64, ids []string, selector string) (int, error) {
	return o.setSubmissionsArchived(ctx, actor, projectID, ids, selector, true)
}

// RestoreSubmissions brings archived submissions back into their form's
// table; audited as ArchiveSubmissions is.
func (o *Ops) RestoreSubmissions(ctx context.Context, actor string, projectID int64, ids []string, selector string) (int, error) {
	return o.setSubmissionsArchived(ctx, actor, projectID, ids, selector, false)
}

func (o *Ops) setSubmissionsArchived(ctx context.Context, actor string, projectID int64, ids []string, selector string, archived bool) (int, error) {
	if err := o.requireProject(ctx, projectID); err != nil {
		return 0, err
	}
	action := "submission.archive"
	if !archived {
		action = "submission.restore"
	}
	return o.St.SetSubmissionsArchived(ctx, projectID, ids, archived, store.AuditEntry{
		Actor: actor, Action: action, Subject: "project/" + idSubject(projectID), Detail: selector})
}

// checkSearch trims search and refuses one shorter than two characters;
// the trimmed value is what reaches the store.
func checkSearch(search string) (string, error) {
	search = strings.TrimSpace(search)
	if len([]rune(search)) < minSearchLen {
		return "", fmt.Errorf("%w: search needs at least %d characters", ErrInvalid, minSearchLen)
	}
	return search, nil
}

// FindSubmissions pages the project's submissions whose field values
// contain search, archived ones and archived forms' included. limit is
// clamped to 1..500 (100 when unset).
func (o *Ops) FindSubmissions(ctx context.Context, projectID int64, search string, limit int, after string) ([]store.Submission, string, error) {
	if err := o.requireProject(ctx, projectID); err != nil {
		return nil, "", err
	}
	search, err := checkSearch(search)
	if err != nil {
		return nil, "", err
	}
	switch {
	case limit <= 0:
		limit = defaultFindLimit
	case limit > maxFindLimit:
		limit = maxFindLimit
	}
	return o.St.FindSubmissions(ctx, projectID, search, limit, after)
}

// SubmissionIDsMatching is every id FindSubmissions would return, for
// archive-by-search.
func (o *Ops) SubmissionIDsMatching(ctx context.Context, projectID int64, search string) ([]string, error) {
	if err := o.requireProject(ctx, projectID); err != nil {
		return nil, err
	}
	search, err := checkSearch(search)
	if err != nil {
		return nil, err
	}
	return o.St.SubmissionIDsMatching(ctx, projectID, search)
}

// ActiveForm reads one form that is not archived: an unknown or archived
// form is ErrNotFound, since its submissions are hidden everywhere.
func (o *Ops) ActiveForm(ctx context.Context, projectID int64, name string) (store.Form, error) {
	if err := o.requireProject(ctx, projectID); err != nil {
		return store.Form{}, err
	}
	f, err := o.St.GetForm(ctx, projectID, name)
	if err != nil {
		return store.Form{}, err
	}
	if f.ArchivedAt != nil {
		return store.Form{}, fmt.Errorf("form %q is archived; restore it first: %w", name, ErrNotFound)
	}
	return f, nil
}

// GetSubmission reads one submission of an active form, archived or not,
// every stored field included (also ones the form no longer expects).
func (o *Ops) GetSubmission(ctx context.Context, projectID int64, form, id string) (store.Submission, error) {
	if err := o.requireProject(ctx, projectID); err != nil {
		return store.Submission{}, err
	}
	return o.St.GetSubmission(ctx, projectID, form, id)
}
