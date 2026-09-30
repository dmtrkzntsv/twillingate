// Package reporting is the reporting surface (spec 2026-09-25): one
// package, at rank 1, holding the models, their validation, the
// dashboard and widget operations on top of Store, the system-definition
// migrator that keeps components and system dashboards in sync with the
// release, the cache, and the embedded UI. It is one package rather than several because those parts
// share the same types (Component, Source, Params) and the same
// refusals — splitting them would either duplicate that vocabulary or
// force an import cycle between the pieces that build it and the pieces
// that check it.
//
// This file holds the pieces every other file in the package depends on:
// the slice of the store reporting needs (Store, declared here rather
// than importing store.Store whole, per CLAUDE.md's "each consumer
// declares the slice it uses"), and Service, the composition root the
// surfaces above this package call into.
package reporting

import (
	"context"
	"sync"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Store is the slice of store.Store reporting reads and writes:
// components, dashboards and widgets, plus the sync that keeps the
// system definition current.
type Store interface {
	ListComponents(ctx context.Context) ([]store.Component, error)
	ListDashboards(ctx context.Context) ([]store.Dashboard, error)
	GetDashboard(ctx context.Context, id int64) (store.Dashboard, error)
	ListWidgets(ctx context.Context, dashboardID int64) ([]store.Widget, error)
	GetWidget(ctx context.Context, id int64) (store.Widget, error)
	InsertDashboard(ctx context.Context, d store.Dashboard, ws []store.Widget, a store.AuditEntry) (int64, error)
	UpdateDashboard(ctx context.Context, d store.Dashboard, a store.AuditEntry) error
	SetDashboardView(ctx context.Context, d store.Dashboard) error
	MoveDashboards(ctx context.Context, ks []store.DashboardKey, a store.AuditEntry) error
	InsertDashboardGroup(ctx context.Context, ds []store.Dashboard, ws [][]store.Widget, a store.AuditEntry) ([]int64, error)
	SetDashboardsArchived(ctx context.Context, ids []int64, archived bool, a store.AuditEntry) error
	InsertWidget(ctx context.Context, w store.Widget, a store.AuditEntry) (int64, error)
	UpdateWidget(ctx context.Context, w store.Widget, a store.AuditEntry) error
	SetWidgetArchived(ctx context.Context, id int64, archived bool, a store.AuditEntry) error
	ReportingHash(ctx context.Context) (string, error)
	SyncReporting(ctx context.Context, s store.ReportingSync) error
}

// Options configures a Service.
type Options struct {
	// CacheAge and RefreshAge come from REPORTING_CACHE_SECONDS and
	// REPORTING_REFRESH_SECONDS: an ordinary request reuses a sql widget's
	// loaded value up to CacheAge old; a fresh=true request instead
	// reuses one only up to the (normally shorter) RefreshAge old. There
	// is no background refresh and nothing is ever served stale past its
	// own age — a request past its age simply loads again. CacheAge 0
	// turns the cache off outright.
	CacheAge, RefreshAge time.Duration
	// Now stands in for time.Now in tests; nil means time.Now.
	Now func() time.Time
}

// Service is the reporting surface's composition root: the store slice
// it reads and writes, the read-only database sql widgets query, the
// registered source types content is checked and run against, and the
// two-age cache their loaded values are served from.
type Service struct {
	st      Store
	db      *readsql.DB
	sources map[string]SourceType
	now     func() time.Time
	cache   *cache

	parsedMu sync.Mutex
	parsed   map[store.Component]Component // Components' memo

	// placeMu serialises dashboard placement (placeDashboards in
	// place.go). Each one reads the user order, computes keys and group
	// ids from it, and writes them back in a later transaction; two
	// interleaved in this process can write from a stale read (a title
	// change putting back the key a concurrent group move just replaced)
	// and split a group without any key colliding, which retryConflict
	// cannot see. Other processes (the CLI) are still caught only by it.
	placeMu sync.Mutex
}

// New builds a Service. db is the read-only handle sql widgets run
// against (internal/shared/readsql); widgets are validated with a sample
// query, so New always registers sql with sampleRows true.
func New(st Store, db *readsql.DB, opt Options) *Service {
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		st:      st,
		db:      db,
		sources: newSources(db, true, now),
		now:     now,
		cache:   newCache(opt.CacheAge, opt.RefreshAge, now),
	}
}
