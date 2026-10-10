package store

import (
	"context"
	"database/sql/driver"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// Family names the aggregate family a raw row feeds: views (v_views_*,
// agg_views_*, views_overview, views_breakdown), product (v_product_*,
// agg_product_*, product_events, product_attributes) or measures
// (v_measures_*, agg_measures_*). Ingest decides it from the event's
// family, or from its name when family is absent; the database holds no
// list of families.
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

// Event is one row of the raw events table: a view ($page_view,
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
	Host, Path, ReferrerSource                     string
	UTMSource, UTMMedium, UTMCampaign              string
	Platform, OS, OSVersion, OSName                string
	Browser, BrowserVersion                        string
	AppVersion, AppLocale, BrowserLocale           string
	Device, DeviceModel                            string
	DisplayWidth, DisplayHeight                    int
	Country                                        string
	Consent                                        Consent
	Attributes                                     map[string]string
	Value                                          *float64
	Measure                                        string
	SampleRate                                     float64
}

// SystemAttribute is a reserved key that product_attributes always breaks
// down, and the events column it reads.
type SystemAttribute struct{ Key, Column string }

// SystemAttributes are rolled up for every project, declared or not:
// low-cardinality environment keys. The live halves of v_product_attrs
// and v_measures_attrs (025_live_halves.sql) list every entry in their
// keys and their CASE.
var SystemAttributes = []SystemAttribute{
	{"$platform", "platform"},
	{"$os", "os"},
	{"$app_version", "app_version"},
	{"$app_locale", "app_locale"},
	{"$kind", "kind"},
	{"$browser", "browser"},
	{"$device", "device"},
	{"$browser_locale", "browser_locale"},
}

// DeclarableAttributes are the reserved keys a project may declare in its
// attributes to get a per-value breakdown, mapped to the events column
// each reads. Any other $ key is refused (manage.ErrInvalid): always-on
// ones need no declaration, and identity, session, consent, os_name and
// display sizes are unbounded or not breakdowns.
var DeclarableAttributes = map[string]string{
	"$host":            "host",
	"$path":            "path",
	"$referrer":        "referrer_source",
	"$utm_source":      "utm_source",
	"$utm_medium":      "utm_medium",
	"$utm_campaign":    "utm_campaign",
	"$os_version":      "os_version",
	"$browser_version": "browser_version",
	"$device_model":    "device_model",
}

// DeclarableAttributeKeys lists DeclarableAttributes' keys, sorted.
func DeclarableAttributeKeys() []string {
	keys := make([]string, 0, len(DeclarableAttributes))
	for k := range DeclarableAttributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
	// Per project, on the day measured: its estimated disk use in raw rows
	// (events) and in everything rolled up (agg_*, actors, identities).
	StatRawBytes       = "raw_bytes"
	StatAggregateBytes = "aggregate_bytes"
	// Per project, for the day counted (every day before the pass's): its
	// views, product events and measure samples.
	StatViews    = "views"
	StatEvents   = "events"
	StatMeasures = "measures"
	// Server-wide (project 0), on the day measured: the database file's
	// size, page_count × page_size.
	StatDatabaseBytes = "database_bytes"
	// Per project, on the day measured: how many attributes it declares.
	StatDeclaredAttributes = "declared_attributes"
	// Per project, for the day counted while its rows are raw (kept once
	// they are rolled up, which keeps only what the caps let through): the
	// distinct attribute keys and key/value pairs received, declared or
	// not, and the values the attribute views fold into (other), summed
	// over every event (and measure) and aggregated key.
	StatAttributeKeys         = "attribute_keys"
	StatAttributeValues       = "attribute_values"
	StatAttributeValuesFolded = "attribute_values_folded"
	// Server-wide (project 0), on the day measured: each cap in force, as
	// its meta row names it (0 = no cap), so a day's folds can be read
	// against the cap that made them. ATTRIBUTE_VALUES_TOP_N's row keeps
	// its earlier name, attributes_top_n.
	StatCapAttributes = "attributes_top_n"
	StatCapIdentities = "identities_top_n"
	StatCapBreakdowns = "attribute_breakdowns_max"
)

// Actor kinds: how an actor id was derived. Only user and install actors
// are stable enough to cohort; a connection hash rotates with the salt.
const (
	ActorUser       = "user"
	ActorInstall    = "install"
	ActorConnection = "connection"
)

// Identity is a display name for a user or group. Names live in their own
// table rather than on event rows: a name repeated on every row could never
// be updated, and names change.
type Identity struct {
	ProjectID      int64
	Kind, ID, Name string
}

// Identity kinds.
const (
	KindUser  = "user"
	KindGroup = "group"
)

// RegistryProject represents a project configuration from the registry.
type RegistryProject struct {
	ID             int64 // 0 on create; assigned by the store
	Name           string
	AllowedOrigins string // JSON array, "[]" if none
	Attributes     string // JSON array, "[]" if none declared
	Archived       bool
	SortKey        string // its place in the order (migration 034)
}

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
	WriteEvents(ctx context.Context, evs []Event) error
	UpsertIdentities(ctx context.Context, ids []Identity) error
	ViewDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	ProductDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	MeasureDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	AggregateViewDay(ctx context.Context, projectID int64, day civil.Date, topN int) error
	AggregateProductDay(ctx context.Context, projectID int64, day civil.Date, attrs []string, topN int) error
	AggregateMeasureDay(ctx context.Context, projectID int64, day civil.Date, attrs []string, topN int) error
	UpsertActors(ctx context.Context, projectID int64, day civil.Date) error
	AggregateRetentionDay(ctx context.Context, projectID int64, day civil.Date) error
	PruneActors(ctx context.Context, projectID int64, before civil.Date) error
	AggregateIdentityDay(ctx context.Context, projectID int64, day civil.Date, topN int) error
	PruneIdentities(ctx context.Context, projectID int64, before civil.Date) error
	PruneAggregates(ctx context.Context, projectID int64, before civil.Date) error
	IncrementalVacuum(ctx context.Context) error
	// RecordUsageHistory writes now's UTC day of usage_history, replacing
	// that day's rows and keeping earlier days as history.
	RecordUsageHistory(ctx context.Context, now time.Time) error
	ProjectIDs(ctx context.Context) ([]int64, error) // all rows incl. archived, ascending
	RebuildFlatView(ctx context.Context, keys []string) error
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
	UpdateForm(ctx context.Context, f Form, a AuditEntry) error
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
	// and, in the same transaction, their raw $form_submit events, and
	// returns how many submissions it deleted. Ids that match nothing are
	// skipped. Aggregates are not touched. The audit row's detail is
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
	// SessionAt reads the actor's session at `at` (views gapped by at most
	// 30 minutes, looking back at most a day) as the event a form
	// submission writes: the newest view's kind, group, session id,
	// environment and consent, with the session's first view's referrer and
	// UTMs. Only those are set; nil when no view matches.
	SessionAt(ctx context.Context, projectID int64, actorKind, actorID string, at time.Time) (*Event, error)
	// ReportingHash is the hash of the latest reporting_migrations row, ""
	// if none has run yet. SyncReporting makes components and system
	// dashboards (with their widgets) match s in one transaction.
	ReportingHash(ctx context.Context) (string, error)
	SyncReporting(ctx context.Context, s ReportingSync) error
	// PurgeArchived deletes every project, dashboard, widget and widget
	// share archived more than days ago, each in its own transaction with
	// an audit row (actor "retention"). days <= 0 purges nothing. An
	// archived form goes with its submissions and their raw events.
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
