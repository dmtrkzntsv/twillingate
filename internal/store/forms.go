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

// Attribution is where the visit a submission arrived in came from: the
// referrer and campaign of the submitter's last non-direct visit. The
// submission keeps it, as a JSON object with empty values left out, for as
// long as it is kept, so a new key is a new field here and no migration;
// its $form_submit event carries the same values.
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
// draft_until a newly created form gets, and the event as the request
// knows it (StoredEvent gives its final shape). The store keeps it with
// the submission, pending, and AttributeSubmission writes it once the
// source is found, only on a form approved on arrival.
type NewSubmission struct {
	Submission Submission
	DraftUntil time.Time
	Event      Event
	// ConnectionID is the connection hash of the request (the actor a
	// view sent without an id gets), whatever actor Event has: the lookup
	// follows it to the views sent before consent gave the visitor an id.
	ConnectionID string
}

// StoredEvent is the $form_submit event an approved form's submission is
// stored as: the submission's id, project and instant (a later delete
// finds the event by them), the product family, and the form's name as
// its only attribute; the rest (session, environment) as ingest set it.
func (n NewSubmission) StoredEvent() Event {
	ev, sub := n.Event, n.Submission
	ev.ID, ev.ProjectID, ev.TS = sub.ID, sub.ProjectID, sub.ReceivedAt
	ev.Family, ev.EventName = FamilyProduct, FormSubmitEvent
	ev.Attributes = map[string]string{"form": sub.Form}
	if ev.ReceivedAt.IsZero() {
		ev.ReceivedAt = sub.ReceivedAt
	}
	return ev
}

// PendingSubmission is a submission waiting for its attribution: the
// $form_submit event the request built (StoredEvent's shape: the actor,
// the page, the country, what the client sent of its source), and
// whether to write it, decided by the form being approved on arrival.
type PendingSubmission struct {
	ProjectID    int64
	ID           string
	ReceivedAt   time.Time
	Event        Event
	WriteEvent   bool
	ConnectionID string // NewSubmission.ConnectionID, "" if none
	// Err is why the stored event could not be read; Event is zero and
	// WriteEvent false when it is set. The submission is still listed, so
	// the job can clear it rather than meet it again every minute.
	Err error
}
