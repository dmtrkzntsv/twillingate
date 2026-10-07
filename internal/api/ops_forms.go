package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Forms and their submissions (spec D8). A submission's table runs SQL
// the server builds (manage.SubmissionsQuery) on h.subs, the handle that
// may read submissions; custom SQL never reaches them.

type formIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Name      string `json:"name" jsonschema:"form name, as list_forms shows it"`
}

type listFormsIn struct {
	ProjectID int64 `json:"project_id" jsonschema:"project id; call list_projects first"`
	Archived  bool  `json:"archived,omitempty" jsonschema:"true lists the archived forms instead of the active ones"`
}

type formOut struct {
	Name            string   `json:"name"`
	Status          string   `json:"status" jsonschema:"draft or approved"`
	Purpose         string   `json:"purpose"`
	ReturnURL       string   `json:"return_url"`
	Fields          []string `json:"fields" jsonschema:"every field name submissions have sent, sorted"`
	ExpectedFields  []string `json:"expected_fields,omitempty" jsonschema:"the fields an approved form keeps; absent on a draft"`
	CreatedAt       string   `json:"created_at"`
	DraftUntil      string   `json:"draft_until,omitempty" jsonschema:"a draft is archived at this time unless approved"`
	ApprovedAt      string   `json:"approved_at,omitempty"`
	ClosesAt        string   `json:"closes_at,omitempty" jsonschema:"submissions from this time on are refused"`
	Submissions     int      `json:"submissions"`
	LastSubmittedAt string   `json:"last_submitted_at,omitempty"`
	Archived        bool     `json:"archived"`
	ArchivedAt      string   `json:"archived_at,omitempty"`
}

type listFormsOut struct {
	Forms []formOut `json:"forms"`
	// ActionBase is where a plain HTML form posts: the collector's public
	// URL (PUBLIC_URL) + /ingest/forms, so a form's action is
	// <action_base>/<name>?key=<key>. Empty when PUBLIC_URL is not set.
	ActionBase string `json:"action_base" jsonschema:"PUBLIC_URL + /ingest/forms, where a plain HTML form posts (<action_base>/<name>?key=<key>); empty when PUBLIC_URL is not configured"`
}

type approveFormIn struct {
	ProjectID      int64    `json:"project_id" jsonschema:"project id; call list_projects first"`
	Name           string   `json:"name" jsonschema:"a draft form's name"`
	ExpectedFields []string `json:"expected_fields" jsonschema:"the fields to keep, one or more; pick them from list_forms' fields"`
}

type updateFormIn struct {
	ProjectID      int64        `json:"project_id" jsonschema:"project id; call list_projects first"`
	Name           string       `json:"name" jsonschema:"form name"`
	Purpose        *string      `json:"purpose,omitempty" jsonschema:"what the form is for, shown in the console; omit to keep"`
	ReturnURL      *string      `json:"return_url,omitempty" jsonschema:"where a plain HTML form sends the visitor back when it sets no $redirect: an absolute http(s) URL whose origin is in the project's allowed_origins; empty clears it; omit to keep"`
	ClosesAt       optionalTime `json:"closes_at,omitempty" jsonschema:"RFC 3339 time from which submissions are refused; null reopens; omit to keep"`
	ExpectedFields *[]string    `json:"expected_fields,omitempty" jsonschema:"approved forms only: replaces the fields kept, one or more; omit to keep"`
}

// optionalTime is a JSON field that tells absent (set false), null (set,
// at nil) and a time apart: update_form's closes_at, where null reopens.
type optionalTime struct {
	set bool
	at  *time.Time
}

func (o *optionalTime) UnmarshalJSON(b []byte) error {
	o.set, o.at = true, nil
	if string(b) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return invalidf("closes_at must be an RFC 3339 time or null")
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return invalidf("closes_at %q is not an RFC 3339 time such as 2026-10-09T17:00:00Z", s)
	}
	t = t.UTC()
	o.at = &t
	return nil
}

type listSubmissionsIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Name      string `json:"name" jsonschema:"form name"`
	Filters   string `json:"filters,omitempty" jsonschema:"a JSON list of {column, op, value} on the table's columns; op is =, !=, <, >, in or not in; in/not in take a list. As widget_data's"`
	Sort      string `json:"sort,omitempty" jsonschema:"<column>:asc or <column>:desc; newest first without one"`
	Distinct  string `json:"distinct,omitempty" jsonschema:"return [value, rows] for this column among rows matching the other filters, most frequent first, instead of the rows"`
	Offset    int    `json:"offset,omitempty" jsonschema:"rows to skip"`
	Limit     int    `json:"limit,omitempty" jsonschema:"page size, 1 to CONSOLE_QUERY_MAX_ROWS (the default)"`
}

type submissionsOut struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	IDs     []string   `json:"ids,omitzero" jsonschema:"each row's submission id, in row order; absent with distinct"`
	Matched int        `json:"matched" jsonschema:"rows (with distinct, values) passing the filters"`
	Total   int        `json:"total" jsonschema:"the form's submissions before any filter"`
	Offset  int        `json:"offset"`
	Limit   int        `json:"limit"`
}

type submissionIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Name      string `json:"name" jsonschema:"form name"`
	ID        string `json:"id" jsonschema:"submission id, from list_submissions' ids"`
}

type submissionOut struct {
	ID         string            `json:"id"`
	Form       string            `json:"form"`
	ReceivedAt string            `json:"received_at"`
	Fields     map[string]string `json:"fields"`
	Host       string            `json:"host"`
	Path       string            `json:"path"`
	Via        string            `json:"via" jsonschema:"form (a plain HTML form) or json"`
	Visit      *store.Visit      `json:"visit,omitempty" jsonschema:"the session the submission came in; absent when none matched"`
}

type findSubmissionsIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Search    string `json:"search" jsonschema:"text a field value contains, at least 2 characters, ASCII case-insensitive (an email, a name)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"page size, 1 to 500; default 100"`
	Cursor    string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page; omit for the first"`
}

// foundSubmission is a submission find_submissions found, with whether
// its form is archived: a search reaches archived forms too.
type foundSubmission struct {
	submissionOut
	Archived bool `json:"archived" jsonschema:"its form is archived: hidden from the tables, purged with the form unless restored"`
}

type findSubmissionsOut struct {
	Submissions []foundSubmission `json:"submissions"`
	NextCursor  string            `json:"next_cursor,omitempty" jsonschema:"pass as cursor for the next page; absent on the last"`
}

type deleteSubmissionsIn struct {
	ProjectID int64    `json:"project_id" jsonschema:"project id; call list_projects first"`
	IDs       []string `json:"ids,omitempty" jsonschema:"submission ids to delete"`
	Form      string   `json:"form,omitempty" jsonschema:"with filters: the form whose table the filters apply to"`
	Filters   string   `json:"filters,omitempty" jsonschema:"with form: list_submissions' filters, at least one; deletes every row they match"`
	Search    string   `json:"search,omitempty" jsonschema:"as find_submissions: deletes every submission it finds"`
}

type deleteSubmissionsOut struct {
	Deleted int `json:"deleted"`
}

type exportSubmissionsIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id"`
	Name      string `json:"name" jsonschema:"form name"`
	Filters   string `json:"filters,omitempty" jsonschema:"as list_submissions"`
	Sort      string `json:"sort,omitempty" jsonschema:"as list_submissions"`
}

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func fmtOptTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return fmtTime(*t)
}

// toFormOut and toSubmissionOut carry the store's lists and maps as they
// are: forms.fields is always a JSON list and submissions.fields always an
// object, so neither is ever nil.
func toFormOut(f store.Form) formOut {
	return formOut{Name: f.Name, Status: f.Status, Purpose: f.Purpose, ReturnURL: f.ReturnURL,
		Fields: f.Fields, ExpectedFields: f.ExpectedFields, CreatedAt: fmtTime(f.CreatedAt),
		DraftUntil: fmtOptTime(f.DraftUntil), ApprovedAt: fmtOptTime(f.ApprovedAt), ClosesAt: fmtOptTime(f.ClosesAt),
		Submissions: f.Submissions, LastSubmittedAt: fmtOptTime(f.LastSubmittedAt),
		Archived: f.ArchivedAt != nil, ArchivedAt: fmtOptTime(f.ArchivedAt)}
}

func toSubmissionOut(s store.Submission) submissionOut {
	return submissionOut{ID: s.ID, Form: s.Form, ReceivedAt: fmtTime(s.ReceivedAt), Fields: s.Fields,
		Host: s.Host, Path: s.Path, Via: s.Via, Visit: s.Visit}
}

func (h *host) listForms(ctx context.Context, in listFormsIn) (listFormsOut, error) {
	fs, err := h.ops.ListForms(ctx, in.ProjectID, in.Archived)
	if err != nil {
		return listFormsOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	out := listFormsOut{Forms: []formOut{}}
	if h.publicURL != "" {
		out.ActionBase = h.publicURL + "/ingest/forms"
	}
	for _, f := range fs {
		out.Forms = append(out.Forms, toFormOut(f))
	}
	return out, nil
}

func (h *host) approveForm(ctx context.Context, in approveFormIn) (okOut, error) {
	if err := h.ops.ApproveForm(ctx, actorFrom(ctx), in.ProjectID, in.Name, in.ExpectedFields); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "approved; new submissions keep only the expected fields and each writes a $form_submit event"}, nil
}

func (h *host) updateForm(ctx context.Context, in updateFormIn) (okOut, error) {
	spec := manage.FormSpec{Purpose: in.Purpose, ReturnURL: in.ReturnURL, ExpectedFields: in.ExpectedFields}
	if in.ClosesAt.set {
		spec.ClosesAt = &in.ClosesAt.at
	}
	if err := h.ops.UpdateForm(ctx, actorFrom(ctx), in.ProjectID, in.Name, spec); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "updated"}, nil
}

func (h *host) archiveForm(ctx context.Context, in formIn) (okOut, error) {
	if err := h.ops.ArchiveForm(ctx, actorFrom(ctx), in.ProjectID, in.Name); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "archived; submissions refused and hidden, reversible with restore_form, purged after RETENTION_ARCHIVED_DAYS"}, nil
}

func (h *host) restoreForm(ctx context.Context, in formIn) (okOut, error) {
	if err := h.ops.RestoreForm(ctx, actorFrom(ctx), in.ProjectID, in.Name); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "restored"}, nil
}

// subsErr words a submissions-table failure as the caller's to fix where
// it is: a column the table does not have, a slow query.
func (h *host) subsErr(err error) error {
	switch {
	case errors.Is(err, readsql.ErrTimeout):
		return invalidf("the submissions query exceeded %s; add a filter", h.subs.Timeout())
	case errors.Is(err, readsql.ErrRefused):
		return invalidf("%s", strings.TrimPrefix(err.Error(), readsql.ErrRefused.Error()+": "))
	}
	return err
}

// formTable is an active form's submissions table, run by page.
type formTable struct {
	h       *host
	form    store.Form
	columns []string // the display columns, without id
}

// submissionsTable reads the form once and builds its query.
func (h *host) submissionsTable(ctx context.Context, projectID int64, name string) (formTable, error) {
	f, err := h.ops.ActiveForm(ctx, projectID, name)
	if err != nil {
		return formTable{}, err
	}
	_, cols := manage.SubmissionsQuery(f)
	return formTable{h: h, form: f, columns: cols}, nil
}

// page runs one page of the table; its first column is the id.
func (t formTable) page(ctx context.Context, pg readsql.Page) (readsql.PageResult, error) {
	res, err := manage.QuerySubmissions(ctx, t.h.subs, t.form, pg)
	if err != nil {
		return readsql.PageResult{}, t.h.subsErr(err)
	}
	return res, nil
}

// all returns the ids and the rows (without them) of every row pg's
// filters match, newest first unless pg sorts (manage.AllSubmissions).
func (t formTable) all(ctx context.Context, pg readsql.Page) ([]string, [][]string, error) {
	ids, _, rows, err := manage.AllSubmissions(ctx, t.h.subs, t.form, pg)
	if err != nil {
		return nil, nil, t.h.subsErr(err)
	}
	return ids, rows, nil
}

func (h *host) listSubmissions(ctx context.Context, in listSubmissionsIn) (submissionsOut, error) {
	pg, echo, err := reporting.ParsePage(in.Filters, in.Sort, in.Distinct, in.Offset, in.Limit, h.subs.MaxRows())
	if err != nil {
		return submissionsOut{}, err
	}
	t, err := h.submissionsTable(ctx, in.ProjectID, in.Name)
	if err != nil {
		return submissionsOut{}, err
	}
	res, err := t.page(ctx, pg)
	if err != nil {
		return submissionsOut{}, err
	}
	out := submissionsOut{Columns: res.Columns, Rows: [][]string{}, Matched: res.Matched, Total: res.Total,
		Offset: echo.Offset, Limit: echo.Limit}
	if pg.Distinct != "" {
		out.Rows = append(out.Rows, res.Rows...)
		return out, nil
	}
	out.Columns, out.IDs = res.Columns[1:], []string{}
	for _, r := range res.Rows {
		out.IDs = append(out.IDs, r[0])
		out.Rows = append(out.Rows, r[1:])
	}
	return out, nil
}

func (h *host) getSubmission(ctx context.Context, in submissionIn) (submissionOut, error) {
	s, err := h.ops.GetSubmission(ctx, in.ProjectID, in.Name, in.ID)
	if err != nil {
		return submissionOut{}, err
	}
	return toSubmissionOut(s), nil
}

func (h *host) findSubmissions(ctx context.Context, in findSubmissionsIn) (findSubmissionsOut, error) {
	subs, next, err := h.ops.FindSubmissions(ctx, in.ProjectID, in.Search, in.Limit, in.Cursor)
	if err != nil {
		return findSubmissionsOut{}, err
	}
	out := findSubmissionsOut{Submissions: []foundSubmission{}, NextCursor: next}
	for _, s := range subs {
		out.Submissions = append(out.Submissions, foundSubmission{toSubmissionOut(s), s.Archived})
	}
	return out, nil
}

// deleteSubmissions resolves exactly one selector to ids and deletes
// them. Filters are a form table's: every row they match, paged through,
// so the delete removes exactly what the table showed. The audit names
// the selector's kind ("ids", "search", "filters: <form>"), never the
// search text or filter values: they are usually the erased person's
// email.
func (h *host) deleteSubmissions(ctx context.Context, in deleteSubmissionsIn) (deleteSubmissionsOut, error) {
	given := 0
	for _, g := range []bool{len(in.IDs) > 0, in.Form != "" || in.Filters != "", in.Search != ""} {
		if g {
			given++
		}
	}
	if given != 1 {
		return deleteSubmissionsOut{}, invalidf("give exactly one of ids, form with filters, or search")
	}
	var ids []string
	var selector string
	var err error
	switch {
	case len(in.IDs) > 0:
		ids, selector = in.IDs, "ids"
	case in.Search != "":
		search := strings.TrimSpace(in.Search)
		if ids, err = h.ops.SubmissionIDsMatching(ctx, in.ProjectID, search); err != nil {
			return deleteSubmissionsOut{}, err
		}
		selector = "search"
	default:
		if in.Form == "" || in.Filters == "" {
			return deleteSubmissionsOut{}, invalidf("form and filters go together: the filters of that form's table")
		}
		pg, _, err := reporting.ParsePage(in.Filters, "", "", 0, 0, h.subs.MaxRows())
		if err != nil {
			return deleteSubmissionsOut{}, err
		}
		if len(pg.Filters) == 0 {
			return deleteSubmissionsOut{}, invalidf("filters must hold at least one filter; to remove a whole form, archive it (purged after RETENTION_ARCHIVED_DAYS)")
		}
		t, err := h.submissionsTable(ctx, in.ProjectID, in.Form)
		if err != nil {
			return deleteSubmissionsOut{}, err
		}
		if ids, _, err = t.all(ctx, pg); err != nil {
			return deleteSubmissionsOut{}, err
		}
		selector = "filters: " + in.Form
	}
	n, err := h.ops.DeleteSubmissions(ctx, actorFrom(ctx), in.ProjectID, ids, selector)
	if err != nil {
		return deleteSubmissionsOut{}, err
	}
	return deleteSubmissionsOut{Deleted: n}, nil
}

func (h *host) exportSubmissions(ctx context.Context, in exportSubmissionsIn) (csvFile, error) {
	pg, _, err := reporting.ParsePage(in.Filters, in.Sort, "", 0, 0, h.subs.MaxRows())
	if err != nil {
		return csvFile{}, err
	}
	t, err := h.submissionsTable(ctx, in.ProjectID, in.Name)
	if err != nil {
		return csvFile{}, err
	}
	_, rows, err := t.all(ctx, pg)
	if err != nil {
		return csvFile{}, err
	}
	header, rows := manage.CSVSafeTable(append([]string(nil), t.columns...), rows)
	return csvFile{Name: in.Name + "-submissions.csv", Header: header, Rows: rows}, nil
}

func (h *host) registerForms(r *registrar) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	no, yes := false, true
	write := &mcp.ToolAnnotations{DestructiveHint: &no}
	idem := &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}
	destroy := &mcp.ToolAnnotations{DestructiveHint: &yes}
	const p = "/api/projects/{project_id}"
	const f = p + "/forms/{name}"

	expose(r, spec{Name: "list_forms", Annotations: ro, Method: "GET", Path: p + "/forms",
		Description: "A project's forms, drafts first (archived: true lists the archived ones instead): name, status (draft or approved), purpose, return_url, fields (every field name sent), expected_fields (what an approved form keeps), draft_until, approved_at, closes_at, submissions (count), last_submitted_at, archived; and action_base, PUBLIC_URL + /ingest/forms (empty when PUBLIC_URL is not configured), so a plain HTML form's action is <action_base>/<name>?key=<key>. A form is created as a draft by its first submission to POST /ingest/forms/{name}; a draft keeps every field for FORMS_DRAFT_DAYS, then is archived unless approved, and its submissions never count as conversions. Approve one with approve_form."},
		h.listForms)
	expose(r, spec{Name: "approve_form", Annotations: write, Method: "POST", Path: f + "/approve",
		Description: "Approve a draft form with expected_fields (one or more, from list_forms' fields): from now on its submissions keep only those fields, and each writes a $form_submit product event (attribute form = the name), its conversion. Submissions taken while it was a draft never count as conversions. Approving an approved form is refused (conflict; change its fields with update_form), and approval does not reopen a closed form."},
		h.approveForm)
	expose(r, spec{Name: "update_form", Annotations: write, Method: "PATCH", Path: f,
		Description: "Change a form: purpose; return_url (where a plain HTML form sends the visitor back without $redirect: an absolute http(s) URL whose origin is in the project's allowed_origins; empty clears it); closes_at (RFC 3339; submissions from then on are refused; null reopens); and, on an approved form, expected_fields (never empty). Fields you omit are kept. expected_fields on a draft is refused: approve it instead."},
		h.updateForm)
	expose(r, spec{Name: "archive_form", Annotations: idem, Method: "POST", Path: f + "/archive",
		Description: "Archive a form: it refuses submissions, and it and its submissions leave every list, table and export (find_submissions still finds them, marked archived, for an erasure request). Reversible with restore_form; purged with its submissions and their conversions still in the raw window RETENTION_ARCHIVED_DAYS (default 30) after archiving unless restored."},
		h.archiveForm)
	expose(r, spec{Name: "restore_form", Annotations: idem, Method: "POST", Path: f + "/restore",
		Description: "Restore an archived form with its submissions. A restored draft accepts submissions for another FORMS_DRAFT_DAYS."},
		h.restoreForm)
	expose(r, spec{Name: "list_submissions", Annotations: ro, Method: "GET", Path: f + "/submissions",
		Description: "One active form's submissions as a table. Submissions hold personal data visitors typed. Columns: Received, one per field (an approved form's expected fields in order, a draft's every field), Page, Referrer, UTM source, UTM medium, UTM campaign; a field named like one of those or id is shown as \"<name> (field)\". ids are the rows' submission ids, in order, for get_submission and delete_submissions. Takes widget_data's remote-table arguments (filters, sort, distinct, offset, limit), applied in SQL: matched counts the rows passing the filters, total the form's submissions. Newest first without a sort; every submission regardless of date (filter Received to narrow). A draft's submissions never count as conversions."},
		h.listSubmissions)
	expose(r, spec{Name: "get_submission", Annotations: ro, Method: "GET", Path: f + "/submissions/{id}",
		Description: "One submission of an active form: every stored field (also ones the form no longer expects), received_at, host, path, via (form or json) and visit (the session it arrived in: landing_path, referrer, UTM source, medium and campaign, views; absent when none matched). Personal data."},
		h.getSubmission)
	expose(r, spec{Name: "find_submissions", Annotations: ro, Method: "GET", Path: p + "/submissions",
		Description: "Find one person's submissions across every form of a project, archived ones included, for an access or erasure request: each submission with a field value containing search (at least 2 characters; ASCII case-insensitive, so É and é differ), newest first, with its form, fields, visit and archived (true when its form is archived). Pages of limit (default 100, at most 500); pass next_cursor back as cursor. Personal data. delete_submissions with the same search deletes exactly these."},
		h.findSubmissions)
	expose(r, spec{Name: "delete_submissions", Annotations: destroy, Method: "POST", Path: p + "/submissions/delete",
		Description: "Permanently delete submissions chosen by exactly one of: ids; form with filters (list_submissions' filters, deleting every row that table shows); or search (as find_submissions, across every form, archived ones included). Irreversible. A deleted submission's $form_submit conversion is removed from the raw window only: days already rolled up keep their counts. A draft's submissions never had one. The audit log records the selector's kind (ids, search, or filters with the form's name) and the number actually deleted (0 included), never the search text, the filter values or the submissions' contents. Returns how many were deleted."},
		h.deleteSubmissions)
	restCSV(r, spec{Name: "export_submissions", Method: "GET", Path: f + "/submissions.csv",
		Description: "One active form's submissions table as CSV: list_submissions' columns without ids, with its filters and sort, every matching row."},
		h.exportSubmissions)
}
