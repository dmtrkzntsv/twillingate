// Package reporting is the reporting surface (spec 2026-09-26): one
// package, at rank 1, holding the models, the validation the rest of
// this file's siblings implement, the operations a later task adds on
// top of Store, the system-definition migrator that keeps components and
// system dashboards in sync with the release, the cache, and the
// embedded UI. It is one package rather than several because those parts
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
	SetDashboardArchived(ctx context.Context, id int64, archived bool, a store.AuditEntry) error
	InsertWidget(ctx context.Context, w store.Widget, a store.AuditEntry) (int64, error)
	UpdateWidget(ctx context.Context, w store.Widget, a store.AuditEntry) error
	SetWidgetArchived(ctx context.Context, id int64, archived bool, a store.AuditEntry) error
	ReportingHash(ctx context.Context) (string, error)
	SyncReporting(ctx context.Context, s store.ReportingSync) error
}

// Options configures a Service.
type Options struct {
	// CacheAge and RefreshAge come from REPORTING_CACHE_SECONDS and
	// REPORTING_REFRESH_SECONDS: how long a sql widget's loaded value is
	// served as-is, and how long past that it is still served while a
	// fresh load runs in the background.
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
