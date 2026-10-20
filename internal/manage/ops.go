package manage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/sortkey"
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
	// moveMu keeps two moves in this process from placing against the
	// same snapshot.
	moveMu sync.Mutex
	// FormDraftDays is RETENTION_FORMS_DRAFT_DAYS: how long a restored draft form
	// accepts submissions. Set by the caller after NewOps; below 1 means
	// the default of 7.
	FormDraftDays int
	// Presence tells a rename whether its target already has events or
	// values in the event files. Set by the caller after NewOps; nil (a
	// process with no engine) skips the check.
	Presence Presence

	now func() time.Time
}

func NewOps(reg *Registry, st Store) *Ops { return &Ops{Reg: reg, St: st, now: time.Now} }

// afterWrite refreshes the in-process snapshot once a write touching the
// given projects has committed. A failed reload is logged rather than returned: the write
// already happened, and reporting it as failed would send the caller into
// a retry that collides with it. Instead those projects are withheld —
// their keys authorize nothing — until the next read reloads, so a revoked
// key or archived project never keeps ingesting on a stale snapshot. Reports whether
// the snapshot now reflects the write.
func (o *Ops) afterWrite(ctx context.Context, ids ...int64) bool {
	if err := o.Reg.Reload(ctx); err != nil {
		o.Reg.logger.Warn("registry reload after write failed; refusing the affected projects' events until the next read reloads",
			"projects", ids, "error", err)
		o.Reg.withhold(ids...)
		return false
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
		AllowedOrigins: spec.AllowedOrigins}
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
	return nil
}

// dedupe returns keys without repeats, first occurrences in order. It
// returns keys itself when there are none, and never writes to it: the
// slice may be a snapshot's.
func dedupe(keys []string) []string {
	seen := make(map[string]bool, len(keys))
	for i, k := range keys {
		if !seen[k] {
			seen[k] = true
			continue
		}
		out := append([]string(nil), keys[:i]...)
		for _, k := range keys[i+1:] {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
		return out
	}
	return keys
}

func (sp *ProjectSpec) row() (store.RegistryProject, error) {
	origins, err := json.Marshal(sp.AllowedOrigins)
	if sp.AllowedOrigins == nil {
		origins, err = []byte("[]"), nil
	}
	if err != nil {
		return store.RegistryProject{}, err
	}
	return store.RegistryProject{ID: sp.ID, Name: sp.Name,
		AllowedOrigins: string(origins)}, nil
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
	return o.written(ctx, spec, o.afterWrite(ctx, id)), nil
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
	return o.written(ctx, spec, o.afterWrite(ctx, spec.ID)), nil
}

func idSubject(id int64) string { return strconv.FormatInt(id, 10) }

func keySubject(id int64, label string) string { return strconv.FormatInt(id, 10) + "/" + label }

func (o *Ops) ArchiveProject(ctx context.Context, actor string, id int64) error {
	if err := o.St.SetProjectArchived(ctx, id, true, store.AuditEntry{
		Actor: actor, Action: "project.archive", Subject: idSubject(id)}); err != nil {
		return err
	}
	o.afterWrite(ctx, id)
	return nil
}

func (o *Ops) RestoreProject(ctx context.Context, actor string, id int64) error {
	if err := o.St.SetProjectArchived(ctx, id, false, store.AuditEntry{
		Actor: actor, Action: "project.restore", Subject: idSubject(id)}); err != nil {
		return err
	}
	o.afterWrite(ctx, id)
	return nil
}

// MoveProject puts project id right after project after in the order
// every listing shows, archived projects included (0: first). Moving a
// project after itself changes nothing. The project takes a fractional key
// between its new neighbours, so one row changes.
func (o *Ops) MoveProject(ctx context.Context, actor string, id, after int64) error {
	o.moveMu.Lock()
	defer o.moveMu.Unlock()
	snap := o.Reg.Snapshot(ctx)
	if snap.Project(id) == nil {
		return store.Refuse(ErrNotFound, "unknown project %d", id)
	}
	if after == id {
		return nil
	}
	if after != 0 && snap.Project(after) == nil {
		return store.Refuse(ErrNotFound, "unknown project %d to put project %d after", after, id)
	}
	rest := slices.DeleteFunc(slices.Clone(snap.Projects()), func(p *Project) bool { return p.ID == id })
	i := 0 // the project goes before rest[i]
	if after != 0 {
		i = slices.IndexFunc(rest, func(p *Project) bool { return p.ID == after }) + 1
	}
	keys, err := moveKeys(rest, i, id)
	if err != nil {
		return err
	}
	if err := o.St.SetProjectSortKeys(ctx, keys, store.AuditEntry{Actor: actor, Action: "project.move",
		Subject: idSubject(id), Detail: fmt.Sprintf("after %d", after)}); err != nil {
		return err
	}
	o.afterWrite(ctx)
	return nil
}

// moveKeys is the keys that put project id before rest[i]: one key
// between its neighbours or, when they leave none between them (equal
// keys, written by hand or by two processes at once), fresh keys for the
// whole new order.
func moveKeys(rest []*Project, i int, id int64) ([]store.ProjectSortKey, error) {
	var prev, next string
	if i > 0 {
		prev = rest[i-1].SortKey
	}
	if i < len(rest) {
		next = rest[i].SortKey
	}
	if k, err := sortkey.Between(prev, next); err == nil {
		return []store.ProjectSortKey{{ID: id, SortKey: k}}, nil
	}
	order := make([]int64, 0, len(rest)+1)
	for j, p := range rest {
		if j == i {
			order = append(order, id)
		}
		order = append(order, p.ID)
	}
	if i == len(rest) {
		order = append(order, id)
	}
	fresh, err := sortkey.Spread("", "", len(order))
	if err != nil {
		return nil, err
	}
	keys := make([]store.ProjectSortKey, len(order))
	for j, pid := range order {
		keys[j] = store.ProjectSortKey{ID: pid, SortKey: fresh[j]}
	}
	return keys, nil
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
	o.afterWrite(ctx, projectID)
	return key, nil
}

func (o *Ops) DisableIngestKey(ctx context.Context, actor string, projectID int64, label string) error {
	if err := o.St.SetIngestKeyDisabled(ctx, projectID, label, true, store.AuditEntry{
		Actor: actor, Action: "key.disable", Subject: keySubject(projectID, label)}); err != nil {
		return err
	}
	o.afterWrite(ctx, projectID)
	return nil
}

func (o *Ops) EnableIngestKey(ctx context.Context, actor string, projectID int64, label string) error {
	if err := o.St.SetIngestKeyDisabled(ctx, projectID, label, false, store.AuditEntry{
		Actor: actor, Action: "key.enable", Subject: keySubject(projectID, label)}); err != nil {
		return err
	}
	o.afterWrite(ctx, projectID)
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
	o.afterWrite(ctx, id)
	return nil
}

// MintIngestKey mints "ak_" + 128 bits hex. Ingest keys are public by
// design (they ship in page source); 128 bits makes guessing infeasible.
func MintIngestKey() (string, error) { return mint("ak_", 16) }

// MintConsoleToken mints "ar_" + 256 bits hex. Unlike ingest keys this is a
// true secret: it reads every project and authorizes management.
func MintConsoleToken() (string, error) { return mint("ar_", 32) }

func mint(prefix string, n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("entropy: %w", err)
	}
	return prefix + hex.EncodeToString(buf), nil
}

// SnippetPlaceholderBase is what Snippet falls back to when INGEST_URL is
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
