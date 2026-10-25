package store

import (
	"context"
	"database/sql/driver"
	"fmt"
	"net/url"
	"time"
)

// Family names the kind of event a row is: a view (views_overview,
// views_breakdown), a product event (product_events, product_attributes)
// or a measure (measures). Ingest decides it from the event's family, or
// from its name when family is absent; nothing holds a list of families.
type Family string

const (
	FamilyViews    Family = "views"
	FamilyProduct  Family = "product"
	FamilyMeasures Family = "measures"
)

// The kinds a measure declares, each with a fixed unit: time in
// milliseconds, size in bytes, number unitless. The unit follows from the
// kind and is never stored.
const (
	MeasureTime   = "time"
	MeasureSize   = "size"
	MeasureNumber = "number"
)

// MeasureKinds lists every kind a measure may declare.
var MeasureKinds = []string{MeasureTime, MeasureSize, MeasureNumber}

// ReservedMetrics are the Web Vitals names, each with the one kind it may
// be sent as. Their thresholds live only in the system dashboard's SQL.
var ReservedMetrics = map[string]string{
	"$lcp":  MeasureTime,
	"$inp":  MeasureTime,
	"$cls":  MeasureNumber,
	"$fcp":  MeasureTime,
	"$ttfb": MeasureTime,
}

// Event is one row of the event files: a view ($page_view,
// $screen_view), a product event or a measure, told apart by Family.
// Kind is the client-declared surface ("web", "app", "cli", …); empty on a
// product event that declared none. ActorKind records how the actor was
// identified and is what retention cohorts on.
//
// Platform is the surface the product is used through (web, ios, electron,
// …); OS is the operating system it runs on. They coincide for a native
// app and diverge everywhere else. OSName is the free-form name the client
// reported, kept beside an OS of other so the bucket stays investigable.
// Attributes holds the custom (non-$) keys, for every family alike.
//
// Value, Measure and SampleRate belong to a measure: Value is nil on every
// other family (stored as NULL), Measure is one of MeasureKinds, and
// SampleRate is the share of occurrences the client sent, in (0, 1]; 0
// means unset and is stored as 1.
type Event struct {
	ID                                             string
	ProjectID                                      int64
	Family                                         Family
	EventName                                      string
	TS, ReceivedAt                                 time.Time
	Kind                                           string
	ActorID, ActorKind, UserID, GroupID, SessionID string
	// InstallID is the client's $install_id, also when a $user_id outranks
	// it as the actor; UserName and GroupName name UserID and GroupID, each
	// kept only beside its id (raw-first D17, D18).
	InstallID, UserName, GroupName       string
	Host, Path, ReferrerSource           string
	UTMSource, UTMMedium, UTMCampaign    string
	Platform, OS, OSVersion, OSName      string
	Browser, BrowserVersion              string
	AppVersion, AppLocale, BrowserLocale string
	Device, DeviceModel                  string
	DisplayWidth, DisplayHeight          int
	Country                              string
	Consent                              Consent
	Attributes                           map[string]string
	Value                                *float64
	Measure                              string
	SampleRate                           float64
}

// Consent is the client's answer, when it sent the row, to "may anything
// be kept on this device" ($consent). The zero value is ConsentUnknown,
// so a row built without it never claims an answer nobody recorded.
type Consent int8

const (
	ConsentUnknown Consent = iota // stored as NULL
	ConsentGiven                  // stored as 1
	ConsentNone                   // stored as 0
)

// Value implements driver.Valuer: unknown is NULL, the answers 1 and 0.
func (c Consent) Value() (driver.Value, error) {
	switch c {
	case ConsentGiven:
		return int64(1), nil
	case ConsentNone:
		return int64(0), nil
	}
	return nil, nil
}

// The usage_history keys the daily pass writes. A new stat is a new
// constant here, measured in internal/store/sqlite/usage_history.go, never
// a migration.
const (
	// Server-wide (project 0), on the day measured: the database file's
	// size, page_count × page_size.
	StatDatabaseBytes = "database_bytes"
	// Per project, on the day measured: the bytes of its event files
	// (archive.Sizes), written by the pass of the process that keeps them.
	StatFileBytes = "file_bytes"
)

// Actor kinds: how an actor id was derived. Only user and install actors
// are stable enough to cohort; a connection hash rotates with the salt.
const (
	ActorUser       = "user"
	ActorInstall    = "install"
	ActorConnection = "connection"
)

// RegistryProject represents a project configuration from the registry.
type RegistryProject struct {
	ID             int64 // 0 on create; assigned by the store
	Name           string
	AllowedOrigins string // JSON array, "[]" if none
	Archived       bool
	ArchivedAt     string // RFC 3339 UTC, "" = live
	SortKey        string // its place in the order (migration 034)
}

// EventRule is one event rule (rules with kind 'event', migration 040): a rename (To set)
// or a block of one event name in one project and family.
type EventRule struct {
	ProjectID int64
	Family    Family // FamilyProduct or FamilyMeasures
	Name      string
	Action    string // EventRuleRename or EventRuleBlock
	To        string // the new name; "" for a block
	CreatedAt string // RFC 3339 UTC
}

const (
	EventRuleRename = "rename"
	EventRuleBlock  = "block"
)

// AttributeRule is an attribute rule (rules.kind = 'attribute', family
// 'event'): a rename of Key to To, or a block of Key (To "").
type AttributeRule struct {
	ProjectID int64
	Key       string
	Action    string // EventRuleRename or EventRuleBlock: the same two actions
	To        string
	CreatedAt string // RFC 3339 UTC
}

// AttributeRuleSet is one project's attribute rules as the write path
// applies them: Renamed maps a key to its target, Blocked holds the
// blocked keys. The zero value maps nothing.
type AttributeRuleSet struct {
	Renamed map[string]string
	Blocked map[string]bool
}

// Empty reports whether the set maps nothing, the fast path.
func (r AttributeRuleSet) Empty() bool { return len(r.Renamed) == 0 && len(r.Blocked) == 0 }

// ProjectSortKey is one project's place in the order (migration 034).
type ProjectSortKey struct {
	ID      int64
	SortKey string
}

// RegistryKey represents an API key in the registry.
type RegistryKey struct {
	Key       string
	ProjectID int64
	Label     string
	Disabled  bool
}

// AuditEntry represents a single audit log entry.
type AuditEntry struct {
	Actor, Action, Subject, Detail string
}

// Store defines the interface for analytics data storage.
type Store interface {
	Migrate(ctx context.Context) error
	// Event rules (migration 039): LoadEventRules reads them all; the
	// three mutations are audited from a in the same transaction.
	LoadEventRules(ctx context.Context) ([]EventRule, error)
	RenameEvent(ctx context.Context, projectID int64, family Family, from, to string, a AuditEntry) error
	BlockEvent(ctx context.Context, projectID int64, family Family, name string, a AuditEntry) error
	RemoveEventRule(ctx context.Context, projectID int64, family Family, name string, a AuditEntry) error
	// Attribute rules: the same two actions on custom attribute keys,
	// audited the same way.
	LoadAttributeRules(ctx context.Context) ([]AttributeRule, error)
	RenameAttribute(ctx context.Context, projectID int64, from, to string, a AuditEntry) error
	BlockAttribute(ctx context.Context, projectID int64, key string, a AuditEntry) error
	RemoveAttributeRule(ctx context.Context, projectID int64, key string, a AuditEntry) error
	IncrementalVacuum(ctx context.Context) error
	// RecordUsageHistory writes now's UTC day of usage_history, replacing
	// that day's rows and keeping earlier days as history: the database's
	// size, and each project's file bytes when fileBytes is non-nil (a
	// process with no event files passes nil and leaves them alone).
	RecordUsageHistory(ctx context.Context, now time.Time, fileBytes map[int64]int64) error
	GetMeta(ctx context.Context, key string) (string, error) // "" if absent
	SetMeta(ctx context.Context, key, value string) error
	LoadRegistry(ctx context.Context) ([]RegistryProject, []RegistryKey, error)
	ConfigVersion(ctx context.Context) (int64, error)
	// CreateProject and CreateProjectWithKey return the id SQLite assigned.
	// The store fills the audit subjects itself (the id, and id/label),
	// because the caller cannot know the id before the insert.
	CreateProject(ctx context.Context, p RegistryProject, a AuditEntry) (int64, error)
	CreateProjectWithKey(ctx context.Context, p RegistryProject, k RegistryKey, projectAudit, keyAudit AuditEntry) (int64, error)
	UpdateProject(ctx context.Context, p RegistryProject, a AuditEntry) error // p.ID selects the row
	SetProjectArchived(ctx context.Context, id int64, archived bool, a AuditEntry) error
	// SetProjectSortKeys writes each project's sort key in one
	// transaction. An unknown id is ErrNotFound and changes nothing.
	SetProjectSortKeys(ctx context.Context, keys []ProjectSortKey, a AuditEntry) error
	InsertIngestKey(ctx context.Context, k RegistryKey, a AuditEntry) error
	SetIngestKeyDisabled(ctx context.Context, projectID int64, label string, disabled bool, a AuditEntry) error
	DeleteProjectData(ctx context.Context, id int64, a AuditEntry) error

	// Reporting rows (migration 021): components, dashboards and widgets.
	ListComponents(ctx context.Context) ([]Component, error)
	ListDashboards(ctx context.Context) ([]Dashboard, error)
	GetDashboard(ctx context.Context, id int64) (Dashboard, error)
	ListWidgets(ctx context.Context, dashboardID int64) ([]Widget, error)
	GetWidget(ctx context.Context, id int64) (Widget, error)
	InsertDashboard(ctx context.Context, d Dashboard, ws []Widget, a AuditEntry) (int64, error)
	// UpdateDashboard writes d's title, sort_key, group_id and sidebar: a
	// row read from the store keeps its sidebar.
	UpdateDashboard(ctx context.Context, d Dashboard, a AuditEntry) error
	SetDashboardView(ctx context.Context, d Dashboard) error
	SetDashboardArchived(ctx context.Context, id int64, archived bool, a AuditEntry) error
	// MoveDashboards rewrites group_id and sort_key of every row in ks in
	// one transaction, with one audit row a (Subject "dashboard/<first
	// id>"). A non-zero rekey first moves the group-name row from
	// rekey.From to rekey.To, in the same transaction.
	MoveDashboards(ctx context.Context, ks []DashboardKey, rekey GroupRekey, a AuditEntry) error
	// SetGroupTitle names group groupID (upsert) and audits it under
	// Subject "group/<groupID>". The caller has validated the title.
	SetGroupTitle(ctx context.Context, groupID int64, title string, a AuditEntry) error
	// InsertDashboardGroup inserts dashboards as one new group, in one
	// transaction: the first gets group_id = its own id, the rest that
	// id. ws[i] are dashboards[i]'s widgets. Returns the new ids in
	// order.
	InsertDashboardGroup(ctx context.Context, ds []Dashboard, ws [][]Widget, a AuditEntry) ([]int64, error)
	// SetDashboardsArchived archives (only rows currently live) or
	// restores (only rows currently archived, a user row back in the
	// sidebar) ids in one transaction,
	// one audit row per id. Unknown ids are ErrNotFound and nothing is
	// written.
	SetDashboardsArchived(ctx context.Context, ids []int64, archived bool, a AuditEntry) error
	InsertWidget(ctx context.Context, w Widget, a AuditEntry) (int64, error)
	// UpdateWidget writes only the columns cols names (and updated_at), so
	// an update never puts back a column another writer changed since w
	// was read. An empty selection is ErrInvalid, a name or sort key
	// taken on the dashboard ErrConflict, an unknown id ErrNotFound.
	UpdateWidget(ctx context.Context, w Widget, cols WidgetColumns, a AuditEntry) error
	SetWidgetArchived(ctx context.Context, id int64, archived bool, a AuditEntry) error
	// Project tabs and dashboard placement (migration 032).
	// ListProjectTabs is ErrNotFound for an unknown project.
	// SetDashboardsSidebar writes one audit row per id; unknown ids are
	// ErrNotFound and nothing is written.
	ListProjectTabs(ctx context.Context, projectID int64) ([]ProjectTabRow, error)
	InsertProjectTab(ctx context.Context, r ProjectTabRow, a AuditEntry) error
	DeleteProjectTab(ctx context.Context, projectID, dashboardID int64, a AuditEntry) error
	MoveProjectTab(ctx context.Context, r ProjectTabRow, a AuditEntry) error
	SetDashboardsSidebar(ctx context.Context, ids []int64, sidebar bool, a AuditEntry) error
	// InsertWidgetShare writes a share, copying the project's name, and
	// audits it under Subject "widget_share/<id>". An unknown project is
	// ErrNotFound and nothing is written.
	InsertWidgetShare(ctx context.Context, n NewWidgetShare, a AuditEntry) (WidgetShare, error)
	GetWidgetShare(ctx context.Context, id string) (WidgetShare, error)
	// WidgetShareImage is the 1x (or, with twoX, the 2x) PNG. Unknown id
	// is ErrNotFound.
	WidgetShareImage(ctx context.Context, id string, twoX bool) ([]byte, error)
	// ListWidgetShares: live shares newest created first, archived ones
	// most recently archived first. An unknown State is ErrInvalid.
	ListWidgetShares(ctx context.Context, f WidgetShareFilter) ([]WidgetShare, error)
	// SetWidgetShareArchiveAt changes when a live share archives ("" =
	// project lifetime). An archived or already due share (archive_at at
	// or before now) is ErrConflict.
	SetWidgetShareArchiveAt(ctx context.Context, id, archiveAt, now string, a AuditEntry) (WidgetShare, error)
	// SetWidgetShareArchived archives (idempotent, no audit row when
	// already archived) or restores a share; a restore also sets
	// archive_at to archiveAt ("" = project lifetime).
	SetWidgetShareArchived(ctx context.Context, id string, archived bool, archiveAt string, a AuditEntry) (WidgetShare, error)
	// ArchiveDueWidgetShares archives every share whose archive_at is at
	// or before now, one widget_share.archive audit row each (actor
	// "retention"), and returns how many.
	ArchiveDueWidgetShares(ctx context.Context, now string) (int, error)
	// WriteSubmission records a form submission, creating the form as a
	// draft on its first one, and on an approved form writes n.Event in
	// the same transaction. It returns the form as it stands after the
	// write and whether a row was inserted (false for an id already
	// stored: nothing changes, no second event). A form that is archived,
	// a draft past draft_until or past closes_at at the submission's
	// ReceivedAt refuses with ErrFormClosed, returned beside the form as
	// it stands, and writes nothing.
	WriteSubmission(ctx context.Context, n NewSubmission) (Form, bool, error)
	// ListForms lists a project's active forms (archived false) or its
	// archived ones (true), drafts first, then by name, each with its
	// submission count.
	ListForms(ctx context.Context, projectID int64, archived bool) ([]Form, error)
	// GetForm reads one form (Submissions is not filled); ErrNotFound for
	// an unknown (project, name).
	GetForm(ctx context.Context, projectID int64, name string) (Form, error)
	// MarkFormSeen moves a form's seen_at forward to until (never back);
	// ErrNotFound for an unknown form. Not audited.
	MarkFormSeen(ctx context.Context, projectID int64, name string, until time.Time) error
	// ApproveForm turns a draft into an approved form keeping expected,
	// stamped now, and clears draft_until. ErrConflict when it is already
	// approved, ErrNotFound when unknown.
	ApproveForm(ctx context.Context, projectID int64, name string, expected []string, now time.Time, a AuditEntry) error
	// UpdateForm writes f's Purpose, ReturnURL and ClosesAt exactly as
	// given (merging is the caller's), and ExpectedFields when non-nil
	// (nil keeps the stored list); ErrNotFound when the row is absent.
	// add are field names recorded as if a submission had sent them; on an
	// approved form each one not yet kept is appended to the expected
	// fields, after ExpectedFields is written. Expected names always join
	// Fields, here and in ApproveForm.
	UpdateForm(ctx context.Context, f Form, add []string, a AuditEntry) error
	// SetFormArchived archives or restores a form. Archiving an archived
	// form and restoring an active one change nothing and write no audit
	// row. Restoring a draft sets draft_until to draftUntil; an approved
	// form ignores it.
	SetFormArchived(ctx context.Context, projectID int64, name string, archived bool, draftUntil time.Time, a AuditEntry) error
	// ExpireDrafts archives every active draft whose draft_until is at or
	// before now, one audit row each (actor "retention", action
	// "form.expire", subject "form/<project_id>/<name>"), and returns how
	// many.
	ExpireDrafts(ctx context.Context, now time.Time) (int, error)
	// DeleteSubmissions deletes the project's submissions with these ids
	// and returns how many it deleted; their $form_submit events stay in
	// the event files. Ids that match nothing are skipped. The audit row's
	// detail is
	// a.Detail (how the ids were chosen) followed by " (<n> deleted)", the
	// count actually deleted, written even when it is 0.
	DeleteSubmissions(ctx context.Context, projectID int64, ids []string, a AuditEntry) (int, error)
	// FindSubmissions returns, newest first, the project's submissions with
	// a field value containing search, archived forms' included and marked
	// Archived (an erasure request must reach them) (case-insensitive;
	// % _ and \ are literal). after is the cursor a previous page returned
	// ("" starts); the cursor returned is the last id of a page that has
	// more after it, "" at the end. limit <= 0 means 100. An empty search
	// is ErrInvalid; an unknown cursor is ErrNotFound.
	FindSubmissions(ctx context.Context, projectID int64, search string, limit int, after string) ([]Submission, string, error)
	// GetSubmission reads one submission of the project's form, every
	// stored field and its attribution; ErrNotFound when absent or when
	// the form is archived.
	GetSubmission(ctx context.Context, projectID int64, form, id string) (Submission, error)
	// SubmissionIDsMatching returns every id FindSubmissions would, newest
	// first, for delete-by-search.
	SubmissionIDsMatching(ctx context.Context, projectID int64, search string) ([]string, error)
	// PendingSubmissions lists up to limit submissions received at or
	// before `before` and not yet attributed, oldest first. A row whose
	// stored event does not read is listed with Err set and a zero Event,
	// and counts toward limit; only a failed read is an error.
	PendingSubmissions(ctx context.Context, before time.Time, limit int) ([]PendingSubmission, error)
	// AttributeSubmission stores ev's referrer and UTMs as p's attribution
	// and, when p.WriteEvent, leaves ev in the outbox (UnloggedEvents),
	// all in one transaction; wrote reports the outbox filled. A submission no
	// longer pending (deleted, or attributed already) changes nothing.
	AttributeSubmission(ctx context.Context, p PendingSubmission, ev Event) (wrote bool, err error)
	// UnloggedEvents lists up to limit events AttributeSubmission wrote
	// and MarkLogged has not cleared yet, oldest first. A row that does
	// not read is listed with Err set and a zero Event.
	UnloggedEvents(ctx context.Context, limit int) ([]UnloggedEvent, error)
	// UnloggedCount is how many events wait in the outbox: the backlog
	// a failed log write reports.
	UnloggedCount(ctx context.Context) (int, error)
	// MarkLogged takes these (ProjectID, ID) events out of the outbox, in
	// one transaction, once they are in the event files.
	MarkLogged(ctx context.Context, keys []UnloggedEvent) error
	// ReportingHash is the hash of the latest reporting_migrations row, ""
	// if none has run yet. SyncReporting makes components and system
	// dashboards (with their widgets) match s in one transaction.
	ReportingHash(ctx context.Context) (string, error)
	SyncReporting(ctx context.Context, s ReportingSync) error
	// PurgeArchived deletes every project, dashboard, widget and widget
	// share archived more than days ago, each in its own transaction with
	// an audit row (actor "retention"). days <= 0 purges nothing. An
	// archived form goes with its submissions.
	PurgeArchived(ctx context.Context, days int) (PurgeResult, error)

	Close() error
}

var registry = map[string]func(string) (Store, error){}

// Register registers a backend factory for the given DSN scheme.
func Register(scheme string, fn func(string) (Store, error)) {
	registry[scheme] = fn
}

// Open selects a backend by DSN scheme. sqlite:// is registered by the
// sqlite package's init via Register.
func Open(dsn string) (Store, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		return nil, fmt.Errorf("store: invalid DSN %q", dsn)
	}
	fn, ok := registry[u.Scheme]
	if !ok {
		return nil, fmt.Errorf("store: unknown backend %q (supported: sqlite)", u.Scheme)
	}
	return fn(dsn)
}
