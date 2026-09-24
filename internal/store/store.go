package store

import (
	"context"
	"database/sql/driver"
	"fmt"
	"net/url"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
)

// View is one page or screen view of any kind ($page_view, $screen_view).
// Kind is the client-declared surface ("web", "app", "cli", …); only "web"
// rows are enriched from the User-Agent. ActorKind records how the actor
// was identified and is what retention cohorts on.
//
// Platform is the surface the product is used through (web, ios, electron,
// …); OS is the operating system it runs on. They coincide for a native
// app and diverge everywhere else. Both are never empty in the database --
// the server stores unknown for an undeclared value -- and OSName is the
// free-form name the client reported, kept beside an OS of other so the
// bucket stays investigable.
type View struct {
	ID                                             string
	ProjectID                                      int64
	TS, ReceivedAt                                 time.Time
	Kind                                           string
	ActorID, ActorKind, UserID, GroupID, SessionID string
	Host, Path, ReferrerSource                     string
	UTMSource, UTMMedium, UTMCampaign              string
	Platform, OS, OSVersion, OSName                string
	Browser, BrowserVersion                        string
	AppVersion, Device, DeviceModel, Locale        string
	DisplayWidth, DisplayHeight                    int
	Country                                        string
	Consent                                        Consent
}

// ProductEvent represents a custom event from any surface. Platform and OS
// are the two environment columns a product event carries; every other
// declared environment key is resolved and dropped at ingest.
type ProductEvent struct {
	ID                 string
	ProjectID          int64
	EventName          string
	TS, ReceivedAt     time.Time
	ActorID, ActorKind string
	UserID, GroupID    string
	Platform, OS       string
	AppVersion         string
	Attributes         map[string]string
	Consent            Consent
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
	WriteViews(ctx context.Context, views []View) error
	WriteProductEvents(ctx context.Context, evs []ProductEvent) error
	UpsertIdentities(ctx context.Context, ids []Identity) error
	ViewDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	ProductDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	AggregateViewDay(ctx context.Context, projectID int64, day civil.Date) error
	AggregateProductDay(ctx context.Context, projectID int64, day civil.Date, attrs []string, topN int) error
	UpsertActors(ctx context.Context, projectID int64, day civil.Date) error
	AggregateRetentionDay(ctx context.Context, projectID int64, day civil.Date) error
	PruneActors(ctx context.Context, projectID int64, before civil.Date) error
	AggregateIdentityDay(ctx context.Context, projectID int64, day civil.Date) error
	PruneIdentities(ctx context.Context, projectID int64, before civil.Date) error
	PruneAggregates(ctx context.Context, projectID int64, viewsBefore, productBefore civil.Date) error
	IncrementalVacuum(ctx context.Context) error
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
	InsertIngestKey(ctx context.Context, k RegistryKey, a AuditEntry) error
	SetIngestKeyDisabled(ctx context.Context, projectID int64, label string, disabled bool, a AuditEntry) error
	DeleteProjectData(ctx context.Context, id int64, a AuditEntry) error
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
