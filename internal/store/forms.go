package store

import (
	"errors"
	"slices"
	"time"
)

// Form statuses.
const (
	FormDraft    = "draft"
	FormApproved = "approved"
)

// FormSubmitEvent is the product event an approved form's submission writes.
const FormSubmitEvent = "$form_submit"

// ErrFormClosed is the refusal of a submission to an archived form, a
// draft past draft_until, or a form past closes_at.
var ErrFormClosed = errors.New("form closed")

// Form is a row of forms: a named place a project's pages post to.
type Form struct {
	ProjectID       int64
	Name            string
	Status          string // FormDraft | FormApproved
	Purpose         string
	ReturnURL       string
	Fields          []string // every field name seen, sorted
	ExpectedFields  []string // nil while draft
	CreatedAt       time.Time
	DraftUntil      *time.Time // nil once approved
	ApprovedAt      *time.Time
	ClosesAt        *time.Time
	LastSubmittedAt *time.Time
	ArchivedAt      *time.Time
	SeenAt          *time.Time // how far the console has read; nil: never
	Submissions     int        // filled by ListForms only
	NewSubmissions  int        // received after SeenAt; filled by ListForms only
}

// Open reports whether the form accepts a submission at now: not archived,
// a draft not yet past draft_until, and not past closes_at.
func (f Form) Open(now time.Time) bool {
	if f.ArchivedAt != nil {
		return false
	}
	if f.Status == FormDraft && f.DraftUntil != nil && !f.DraftUntil.After(now) {
		return false
	}
	return f.ClosesAt == nil || f.ClosesAt.After(now)
}

// KeepFields returns the fields a submission to f stores: all of them on
// a draft, only the expected ones on an approved form.
func KeepFields(f Form, fields map[string]string) map[string]string {
	if f.Status != FormApproved {
		return fields
	}
	kept := make(map[string]string, len(f.ExpectedFields))
	for k, v := range fields {
		if slices.Contains(f.ExpectedFields, k) {
			kept[k] = v
		}
	}
	return kept
}

// Attribution is where the visit a submission arrived in came from: its
// first view's referrer and campaign. The submission keeps it, as a JSON
// object with empty values left out, for as long as it is kept, so a new
// key is a new field here and no migration; its $form_submit event carries
// the same values.
type Attribution struct {
	Referrer    string `json:"referrer,omitempty"`
	UTMSource   string `json:"utm_source,omitempty"`
	UTMMedium   string `json:"utm_medium,omitempty"`
	UTMCampaign string `json:"utm_campaign,omitempty"`
}

// Submission is a row of submissions: what the visitor typed and where the
// visit came from. Everything else about it (the page, the visitor, the
// environment) is on its $form_submit event, which has the same id.
type Submission struct {
	ProjectID   int64
	ID          string
	Form        string
	ReceivedAt  time.Time
	Fields      map[string]string // as sent (after flattening); WriteSubmission applies KeepFields
	Attribution Attribution
	Archived    bool // its form is archived; set by FindSubmissions only
}

// NewSubmission is what ingest hands the store: the submission, the
// draft_until a newly created form gets, and the event to write when the
// form turns out approved (its ID, TS, ProjectID, Family product,
// EventName "$form_submit", Attributes {"form": name} already set; the
// store writes it only on an approved form).
type NewSubmission struct {
	Submission Submission
	DraftUntil time.Time
	Event      Event
}
