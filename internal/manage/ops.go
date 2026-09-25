package manage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Ops are the audited registry operations. Every mutation writes its
// audit row and bumps config_version in one transaction (store layer),
// then rebuilds the snapshot synchronously so in-process readers see it
// immediately (spec §3.3); if that rebuild fails, the next read retries it
// and the operation still reports the committed write as done.
type Ops struct {
	Reg *Registry
	St  Store
}

func NewOps(reg *Registry, st Store) *Ops { return &Ops{Reg: reg, St: st} }

// rebuildFlatView refreshes v_events_flat from the registry's CURRENT
// snapshot, so a config edit (a new or changed attribute list) reaches BI
// tools immediately rather than waiting for the next daily pass. Callers
// must Reg.Reload first.
//
// Errors are logged, not returned: a view rebuild is a side effect of the
// operation, not the thing the caller asked for, so it must never fail
// project creation or update over a rebuild hiccup — the nightly pass is
// the repair net.
func (o *Ops) rebuildFlatView(ctx context.Context) {
	keys := o.Reg.Snapshot(ctx).DeclaredAttributeKeys()
	if err := o.St.RebuildFlatView(ctx, keys); err != nil {
		o.Reg.logger.Warn("flat view rebuild failed", "error", err)
	}
}

// afterWrite refreshes the in-process snapshot once a write touching the
// given projects has committed and, when rebuildView is set, the flat view
// with it. A failed reload is logged rather than returned: the write
// already happened, and reporting it as failed would send the caller into
// a retry that collides with it. Instead those projects are withheld —
// their keys authorize nothing — until the next read reloads, so a revoked
// key or archived project never keeps ingesting on a stale snapshot. The
// flat view waits for the next write or the daily pass. Reports whether
// the snapshot now reflects the write.
func (o *Ops) afterWrite(ctx context.Context, rebuildView bool, ids ...int64) bool {
	if err := o.Reg.Reload(ctx); err != nil {
		o.Reg.logger.Warn("registry reload after write failed; refusing the affected projects' events until the next read reloads",
			"projects", ids, "error", err)
		o.Reg.withhold(ids...)
		return false
	}
	if rebuildView {
		o.rebuildFlatView(ctx)
	}
	return true
}

// written is the project a create or update just committed: the snapshot's
// copy when the reload succeeded, otherwise one built from the merged spec,
// keeping the archived flag the spec does not carry.
func (o *Ops) written(ctx context.Context, spec ProjectSpec, reloaded bool) *Project {
	if reloaded {
		if cur := o.Reg.Snapshot(ctx).Project(spec.ID); cur != nil {
			return cur
		}
	}
	// Read the held snapshot without polling, so the pending reload is left
	// for the caller's next read.
	cur := o.Reg.snap.Load().Project(spec.ID)
	p := &Project{ID: spec.ID, Name: spec.Name,
		AllowedOrigins: spec.AllowedOrigins, Attributes: spec.Attributes}
	if cur != nil {
		p.Archived = cur.Archived
	}
	return p
}

// ProjectSpec is the caller's view of a project. On create, Name is
// required and ID is ignored. On update, ID selects the row and every
// other field merges: an empty Name keeps the current value, a nil slice
// keeps the current list, a non-nil slice replaces it — so an empty
// non-nil AllowedOrigins clears the origins. JSON `[]` decodes to a
// non-nil empty slice and an omitted field to nil, which is what lets the
// API express both without a second field.
type ProjectSpec struct {
	ID             int64
	Name           string
	AllowedOrigins []string
	Attributes     []string
}

// validate checks a complete spec: the one a caller built for create, or
// the merged one UpdateProject built over the current row.
func (sp *ProjectSpec) validate() error {
	if strings.TrimSpace(sp.Name) == "" {
		return fmt.Errorf("%w: name must not be empty", ErrInvalid)
	}
	for _, o := range sp.AllowedOrigins {
		if o == "" {
			return fmt.Errorf("%w: allowed_origins must not contain an empty origin", ErrInvalid)
		}
	}
	for _, a := range sp.Attributes {
		if !strings.HasPrefix(a, "$") {
			continue
		}
		if _, ok := store.DeclarableAttributes[a]; !ok {
			return fmt.Errorf("%w: attribute %s cannot be declared; the reserved keys a project may declare are %s",
				ErrInvalid, a, strings.Join(store.DeclarableAttributeKeys(), ", "))
		}
	}
	return nil
}

func (sp *ProjectSpec) row() (store.RegistryProject, error) {
	origins, err := json.Marshal(sp.AllowedOrigins)
	if sp.AllowedOrigins == nil {
		origins, err = []byte("[]"), nil
	}
	if err != nil {
		return store.RegistryProject{}, err
	}
	attrs, err := json.Marshal(sp.Attributes)
	if sp.Attributes == nil {
		attrs, err = []byte("[]"), nil
	}
	if err != nil {
		return store.RegistryProject{}, err
	}
	return store.RegistryProject{ID: sp.ID, Name: sp.Name,
		AllowedOrigins: string(origins), Attributes: string(attrs)}, nil
}

func (o *Ops) CreateProject(ctx context.Context, actor string, spec ProjectSpec) (*Project, error) {
	return o.create(ctx, actor, spec, func(row store.RegistryProject, audit store.AuditEntry) (int64, error) {
		return o.St.CreateProject(ctx, row, audit)
	})
}

// CreateProjectWithKey is CreateProject plus a first ingest key under
// label, committed together: either both exist afterwards or neither does.
func (o *Ops) CreateProjectWithKey(ctx context.Context, actor string, spec ProjectSpec, label string) (*Project, string, error) {
	key, err := MintIngestKey()
	if err != nil {
		return nil, "", err
	}
	p, err := o.create(ctx, actor, spec, func(row store.RegistryProject, audit store.AuditEntry) (int64, error) {
		return o.St.CreateProjectWithKey(ctx, row,
			store.RegistryKey{Key: key, Label: label}, audit,
			store.AuditEntry{Actor: actor, Action: "key.issue"})
	})
	if err != nil {
		return nil, "", err
	}
	return p, key, nil
}

// create validates spec, hands its row and audit entry to write, and
// reloads the registry once the write has committed. The store fills the
// audit subject with the id it assigns.
func (o *Ops) create(ctx context.Context, actor string, spec ProjectSpec, write func(store.RegistryProject, store.AuditEntry) (int64, error)) (*Project, error) {
	spec.ID = 0
	if err := spec.validate(); err != nil {
		return nil, err
	}
	row, err := spec.row()
	if err != nil {
		return nil, err
	}
	id, err := write(row, store.AuditEntry{Actor: actor, Action: "project.create"})
	if err != nil {
		return nil, err
	}
	spec.ID = id
	return o.written(ctx, spec, o.afterWrite(ctx, true, id)), nil
}

// UpdateProject merges spec over the current row (see ProjectSpec) and
// writes the result whole.
func (o *Ops) UpdateProject(ctx context.Context, actor string, spec ProjectSpec) (*Project, error) {
	cur := o.Reg.Snapshot(ctx).Project(spec.ID)
	if cur == nil {
		return nil, fmt.Errorf("update project: unknown id %d: %w", spec.ID, ErrNotFound)
	}
	if spec.Name == "" {
		spec.Name = cur.Name
	}
	if spec.AllowedOrigins == nil {
		spec.AllowedOrigins = cur.AllowedOrigins
	}
	if spec.Attributes == nil {
		spec.Attributes = cur.Attributes
	}
	if err := spec.validate(); err != nil {
		return nil, err
	}
	row, err := spec.row()
	if err != nil {
		return nil, err
	}
	if err := o.St.UpdateProject(ctx, row, store.AuditEntry{
		Actor: actor, Action: "project.update", Subject: idSubject(spec.ID)}); err != nil {
		return nil, err
	}
	return o.written(ctx, spec, o.afterWrite(ctx, true, spec.ID)), nil
}

func idSubject(id int64) string { return strconv.FormatInt(id, 10) }

func keySubject(id int64, label string) string { return strconv.FormatInt(id, 10) + "/" + label }

func (o *Ops) ArchiveProject(ctx context.Context, actor string, id int64) error {
	if err := o.St.SetProjectArchived(ctx, id, true, store.AuditEntry{
		Actor: actor, Action: "project.archive", Subject: idSubject(id)}); err != nil {
		return err
	}
	o.afterWrite(ctx, false, id)
	return nil
}

func (o *Ops) RestoreProject(ctx context.Context, actor string, id int64) error {
	if err := o.St.SetProjectArchived(ctx, id, false, store.AuditEntry{
		Actor: actor, Action: "project.restore", Subject: idSubject(id)}); err != nil {
		return err
	}
	o.afterWrite(ctx, false, id)
	return nil
}

func (o *Ops) IssueIngestKey(ctx context.Context, actor string, projectID int64, label string) (string, error) {
	if o.Reg.Snapshot(ctx).Project(projectID) == nil {
		return "", fmt.Errorf("unknown project %d: %w", projectID, ErrNotFound)
	}
	key, err := MintIngestKey()
	if err != nil {
		return "", err
	}
	if err := o.St.InsertIngestKey(ctx, store.RegistryKey{
		Key: key, ProjectID: projectID, Label: label}, store.AuditEntry{
		Actor: actor, Action: "key.issue", Subject: keySubject(projectID, label)}); err != nil {
		return "", err
	}
	o.afterWrite(ctx, false, projectID)
	return key, nil
}

func (o *Ops) DisableIngestKey(ctx context.Context, actor string, projectID int64, label string) error {
	if err := o.St.SetIngestKeyDisabled(ctx, projectID, label, true, store.AuditEntry{
		Actor: actor, Action: "key.disable", Subject: keySubject(projectID, label)}); err != nil {
		return err
	}
	o.afterWrite(ctx, false, projectID)
	return nil
}

func (o *Ops) EnableIngestKey(ctx context.Context, actor string, projectID int64, label string) error {
	if err := o.St.SetIngestKeyDisabled(ctx, projectID, label, false, store.AuditEntry{
		Actor: actor, Action: "key.enable", Subject: keySubject(projectID, label)}); err != nil {
		return err
	}
	o.afterWrite(ctx, false, projectID)
	return nil
}

// DeleteProject is exposed by the CLI only — never as an MCP tool
// (spec §7.3: irreversible operations require a shell). Reclaims pages
// afterwards; the tx cannot (single connection).
func (o *Ops) DeleteProject(ctx context.Context, actor string, id int64) error {
	if err := o.St.DeleteProjectData(ctx, id, store.AuditEntry{
		Actor: actor, Action: "project.delete", Subject: idSubject(id)}); err != nil {
		return err
	}
	// Reclaiming pages is housekeeping: the delete has committed, so a
	// failed vacuum is logged and left to the daily pass's own vacuum.
	if err := o.St.IncrementalVacuum(ctx); err != nil {
		o.Reg.logger.Warn("vacuum after project delete failed", "project_id", id, "error", err)
	}
	o.afterWrite(ctx, false, id)
	return nil
}

// MintIngestKey mints "ak_" + 128 bits hex. Ingest keys are public by
// design (they ship in page source); 128 bits makes guessing infeasible.
func MintIngestKey() (string, error) { return mint("ak_", 16) }

// MintAPIToken mints "ar_" + 256 bits hex. Unlike ingest keys this is a
// true secret: it reads every project and authorizes management.
func MintAPIToken() (string, error) { return mint("ar_", 32) }

func mint(prefix string, n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("entropy: %w", err)
	}
	return prefix + hex.EncodeToString(buf), nil
}

// SnippetPlaceholderBase is what Snippet falls back to when PUBLIC_URL is
// not configured. Callers that surface a snippet built on it should say so
// (the operator must substitute the real collector URL).
const SnippetPlaceholderBase = "https://twillingate.example.com"

// Snippet renders the paste-ready embed tag returned by create_project,
// issue_ingest_key and `twillingate key issue`. base is the COLLECTOR's
// public URL (twillingate.js and /ingest/events live there) — never the
// customer's site origin. The tag is anonymous by default; a signed-in app
// adds data-identity="identified" itself (docs/twillingate.md, Identity).
func Snippet(base, key string) string {
	if base == "" {
		base = SnippetPlaceholderBase
	}
	return fmt.Sprintf(`<script defer src="%s/js/twillingate.js"
        data-key=%q></script>`, base, key)
}
