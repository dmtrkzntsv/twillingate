package manage

import (
	"context"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Store is the slice of store.Store this package uses: the registry rows,
// their audited mutations, and the two maintenance calls an edit triggers
// (the flat-view rebuild and the post-delete vacuum). Declared here so a
// new aggregation method on the store never touches manage, and so a test
// can hand Ops a fake that implements only this.
type Store interface {
	LoadRegistry(ctx context.Context) ([]store.RegistryProject, []store.RegistryKey, error)
	ConfigVersion(ctx context.Context) (int64, error)
	CreateProject(ctx context.Context, p store.RegistryProject, a store.AuditEntry) (int64, error)
	CreateProjectWithKey(ctx context.Context, p store.RegistryProject, k store.RegistryKey, projectAudit, keyAudit store.AuditEntry) (int64, error)
	UpdateProject(ctx context.Context, p store.RegistryProject, a store.AuditEntry) error
	SetProjectArchived(ctx context.Context, id int64, archived bool, a store.AuditEntry) error
	InsertIngestKey(ctx context.Context, k store.RegistryKey, a store.AuditEntry) error
	SetIngestKeyDisabled(ctx context.Context, projectID int64, label string, disabled bool, a store.AuditEntry) error
	DeleteProjectData(ctx context.Context, id int64, a store.AuditEntry) error
	RebuildFlatView(ctx context.Context, keys []string) error
	IncrementalVacuum(ctx context.Context) error
}
