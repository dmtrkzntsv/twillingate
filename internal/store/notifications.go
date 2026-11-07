package store

import "time"

// Notification types: the closed vocabulary a notification's Type is one
// of (spec 2026-11-07-notifications-design).
const (
	NotifyUptimeDown         = "uptime_down"
	NotifyUptimeStillDown    = "uptime_still_down"
	NotifyUptimeUp           = "uptime_up"
	NotifyCertFailing        = "cert_failing"
	NotifyCertStillFailing   = "cert_still_failing"
	NotifyCertRecovered      = "cert_recovered"
	NotifyDomainFailing      = "domain_failing"
	NotifyDomainStillFailing = "domain_still_failing"
	NotifyDomainRecovered    = "domain_recovered"
	NotifyCertExpiring       = "cert_expiring"
	NotifyDomainExpiring     = "domain_expiring"
	NotifyFormDraftCreated   = "form_draft_created"
	NotifyFormDraftDiscarded = "form_draft_discarded"
	NotifyFormSubmission     = "form_submission"
)

// NotificationTypes is every notification type, in the order above.
var NotificationTypes = []string{
	NotifyUptimeDown, NotifyUptimeStillDown, NotifyUptimeUp,
	NotifyCertFailing, NotifyCertStillFailing, NotifyCertRecovered,
	NotifyDomainFailing, NotifyDomainStillFailing, NotifyDomainRecovered,
	NotifyCertExpiring, NotifyDomainExpiring,
	NotifyFormDraftCreated, NotifyFormDraftDiscarded, NotifyFormSubmission,
}

// Delivery outcomes of a notification on the channel: pending until the
// dispatcher tries it, then sent or failed; none when no channel is
// configured, skipped when the dispatcher chose not to send it.
const (
	DeliveryPending = "pending"
	DeliverySent    = "sent"
	DeliveryFailed  = "failed"
	DeliveryNone    = "none"
	DeliverySkipped = "skipped"
)

// FormBurstWindow is how long after a form's immediate submission
// notification later submissions fold into one follow-up.
const FormBurstWindow = 15 * time.Minute

// Notification is a row of notifications: one inbox item, and what goes
// to the channel.
type Notification struct {
	ID        int64
	Type      string // one of NotificationTypes
	ProjectID int64  // 0: no project
	Subject   string // origin, domain or form
	// Fields are the rendering facts; always has "project" (the name at
	// creation) when ProjectID != 0.
	Fields        map[string]string
	CreatedAt     time.Time
	ArchivedAt    *time.Time
	Delivery      string // one of the Delivery* values
	DeliveryError string
	Attempts      int
	NextAttemptAt *time.Time
}

// NewNotification is what a caller hands AddNotification (and
// PutNotifyState): the store fills Fields["project"] from the project's
// name when ProjectID != 0 and it is not set. At is the creation time.
type NewNotification struct {
	Type      string
	ProjectID int64
	Subject   string
	Fields    map[string]string
	At        time.Time
}

// NotifyState is a row of notify_states: what the notification tracker
// remembers about one (project, kind, subject), so a restart does not
// repeat a send. Data is JSON owned by internal/inbox.
type NotifyState struct {
	ProjectID int64
	Kind      string
	Subject   string
	Data      string
}

// NotificationFilter selects what ListNotifications returns: the archived
// or the active ones, of one project (0: every one), at most Limit (<= 0:
// no limit), with an id below Before (0: from the newest).
type NotificationFilter struct {
	Archived  bool
	ProjectID int64
	Limit     int
	Before    int64
}
