package store

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
)

// View is one page or screen view of any kind ($page_view, $screen_view).
// Kind is the client-declared surface ("web", "app", "cli", …); only "web"
// rows are enriched from the User-Agent. ActorKind records how the actor
// was identified and is what retention cohorts on.
type View struct {
	ID, Project                                    string
	TS, ReceivedAt                                 time.Time
	Kind                                           string
	ActorID, ActorKind, UserID, GroupID, SessionID string
	Host, Path, ReferrerSource                     string
	UTMSource, UTMMedium, UTMCampaign              string
	OS, OSVersion, Browser, BrowserVersion         string
	AppVersion, Device, DeviceModel, Locale        string
	DisplayWidth, DisplayHeight                    int
	Country                                        string
}

// ProductEvent represents a custom event from any surface.
type ProductEvent struct {
	ID, Project, EventName   string
	TS, ReceivedAt           time.Time
	ActorID, ActorKind       string
	UserID, GroupID          string
	OS, AppVersion           string
	Attributes               map[string]string
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
	Project, Kind, ID, Name string
}

// Identity kinds.
const (
	KindUser  = "user"
	KindGroup = "group"
)

// RegistryProject represents a project configuration from the registry.
type RegistryProject struct {
	Alias, Name, Identity string
	AllowedOrigins        string // JSON array, "[]" if none
	Retention             string // JSON object or ""
	Attributes            string // JSON array, "[]" if none declared
	Archived              bool
}

// RegistryKey represents an API key in the registry.
type RegistryKey struct {
	Key, Project, Label string
	Disabled            bool
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
	ViewDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error)
	ProductDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error)
	AggregateViewDay(ctx context.Context, project string, day civil.Date) error
	AggregateProductDay(ctx context.Context, project string, day civil.Date, attrs []string, topN int) error
	UpsertActors(ctx context.Context, project string, day civil.Date) error
	AggregateRetentionDay(ctx context.Context, project string, day civil.Date) error
	PruneActors(ctx context.Context, project string, before civil.Date) error
	AggregateIdentityDay(ctx context.Context, project string, day civil.Date) error
	PruneIdentities(ctx context.Context, project string, before civil.Date) error
	PruneAggregates(ctx context.Context, project string, viewsBefore, productBefore civil.Date) error
	IncrementalVacuum(ctx context.Context) error
	ProjectAliases(ctx context.Context) ([]string, error) // all rows incl. archived
	RebuildFlatView(ctx context.Context, keys []string) error
	GetMeta(ctx context.Context, key string) (string, error) // "" if absent
	SetMeta(ctx context.Context, key, value string) error
	LoadRegistry(ctx context.Context) ([]RegistryProject, []RegistryKey, error)
	ConfigVersion(ctx context.Context) (int64, error)
	CreateProject(ctx context.Context, p RegistryProject, a AuditEntry) error
	CreateProjectWithKey(ctx context.Context, p RegistryProject, k RegistryKey, projectAudit, keyAudit AuditEntry) error
	UpdateProject(ctx context.Context, p RegistryProject, a AuditEntry) error
	SetProjectArchived(ctx context.Context, alias string, archived bool, a AuditEntry) error
	InsertIngestKey(ctx context.Context, k RegistryKey, a AuditEntry) error
	SetIngestKeyDisabled(ctx context.Context, project, label string, disabled bool, a AuditEntry) error
	DeleteProjectData(ctx context.Context, alias string, a AuditEntry) error
	RenameProject(ctx context.Context, old, new string, a AuditEntry) error
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
