package api

import (
	"context"
	"errors"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

// Management tools (managed-config spec §5). One authorization tier: the
// same token that reads. The guardrails against prompt injection from
// attacker-writable analytics data (spec §6): every write is annotated so
// clients interpose the operator, nothing here is irreversible, and every
// operation lands in audit_log as actor 'mcp' or 'api', by transport.

type projectIn struct {
	Alias          string   `json:"alias" jsonschema:"project alias (immutable)"`
	Name           string   `json:"name,omitempty" jsonschema:"display name, defaults to alias"`
	Identity       string   `json:"identity,omitempty" jsonschema:"anonymous (default) or identified. identified stores user ids and names as given — a privacy-significant setting; see the GDPR docs"`
	AllowedOrigins []string `json:"allowed_origins,omitempty" jsonschema:"origins allowed to post events. * is a wildcard: https://*.example.com covers every subdomain, a bare * allows any origin"`
	// Attributes declares which product-event attribute keys are broken
	// down (v_events_flat columns, agg_product_attrs rollups). Rollups
	// always run regardless of this list; nil (omitted) keeps the current
	// setting on update_project.
	Attributes []string `json:"attributes,omitempty" jsonschema:"attribute keys to break down and expose as flat-view columns"`
	// SkipKey rather than IssueKey: JSON booleans have no "unset", and
	// the zero value must give the default behaviour (issue a key).
	SkipKey bool `json:"skip_key,omitempty" jsonschema:"create_project only: set true to NOT issue a first ingest key"`
}

type projectToolOut struct {
	Alias    string `json:"alias"`
	Identity string `json:"identity"`
	Key      string `json:"key,omitempty"`
	Snippet  string `json:"snippet,omitempty"`
	Note     string `json:"note,omitempty"`
}

// createProject issues the first key in the same transaction as the
// project unless skip_key is set: a refusal leaves nothing behind, so the
// caller can fix the input and retry without meeting a keyless leftover.
func (h *host) createProject(ctx context.Context, in projectIn) (projectToolOut, error) {
	spec := manage.ProjectSpec{
		Alias: in.Alias, Name: in.Name, Identity: in.Identity,
		AllowedOrigins: in.AllowedOrigins, Attributes: in.Attributes}
	var p *manage.Project
	var key string
	var err error
	if in.SkipKey {
		p, err = h.ops.CreateProject(ctx, actorFrom(ctx), spec)
	} else {
		// by default the quickstart story is one round trip to paste-ready
		p, key, err = h.ops.CreateProjectWithKey(ctx, actorFrom(ctx), spec, "default")
	}
	if err != nil {
		return projectToolOut{}, err
	}
	out := projectToolOut{Alias: p.Alias, Identity: p.Identity, Key: key}
	if key != "" {
		out.Snippet = manage.Snippet(h.publicURL, key, p.Identity)
		if h.publicURL == "" {
			out.Note = "PUBLIC_URL is not configured; the snippet uses the placeholder " + manage.SnippetPlaceholderBase + " — ask the operator for the collector's public URL"
		}
	}
	return out, nil
}

// updateProject merges rather than replaces (binding ruling, Task 9):
// Ops.UpdateProject itself is full-replace, so the tool starts from the
// project's current values — including Retention and Attributes, which
// this tool has no fields for and must not silently clear — and overlays
// only what the caller actually provided. AllowedOrigins is the one field
// that is not field-merged: when the caller supplies a non-empty list it
// replaces the origin list wholesale (a caller cannot add one origin to
// an existing list without repeating the others). An empty list is
// treated the same as omitted (JSON has no way to distinguish "not
// provided" from "explicitly empty" once decoded into a nil-or-empty
// slice via omitempty), so there is no way to clear origins to empty
// through this tool — see the tool description.
func (h *host) updateProject(ctx context.Context, in projectIn) (projectToolOut, error) {
	cur := h.reg.Snapshot(ctx).Project(in.Alias)
	if cur == nil {
		return projectToolOut{}, h.unknownProjectErr(ctx, in.Alias)
	}
	spec := manage.ProjectSpec{
		Alias:          in.Alias,
		Name:           cur.Name,
		Identity:       cur.Identity,
		AllowedOrigins: cur.AllowedOrigins,
		Retention:      cur.Retention,
		Attributes:     cur.Attributes,
	}
	if in.Attributes != nil {
		spec.Attributes = in.Attributes
	}
	if in.Name != "" {
		spec.Name = in.Name
	}
	if in.Identity != "" {
		spec.Identity = in.Identity
	}
	if len(in.AllowedOrigins) > 0 {
		spec.AllowedOrigins = in.AllowedOrigins
	}
	p, err := h.ops.UpdateProject(ctx, actorFrom(ctx), spec)
	if err != nil {
		return projectToolOut{}, h.projectErr(ctx, in.Alias, err)
	}
	return projectToolOut{Alias: p.Alias, Identity: p.Identity}, nil
}

// projectErr rewrites a not-found refusal into the recoverable form
// (the valid aliases listed, see unknownProjectErr) for tools whose only
// lookup is the project itself. Key tools keep the store's message: there
// the missing thing may be the label, and a list of aliases would mislead.
func (h *host) projectErr(ctx context.Context, alias string, err error) error {
	if errors.Is(err, manage.ErrNotFound) {
		return h.unknownProjectErr(ctx, alias)
	}
	return err
}

type aliasIn struct {
	Alias string `json:"alias" jsonschema:"project alias"`
}
type okOut struct {
	Status string `json:"status"`
}

func (h *host) archiveProject(ctx context.Context, in aliasIn) (okOut, error) {
	if err := h.ops.ArchiveProject(ctx, actorFrom(ctx), in.Alias); err != nil {
		return okOut{}, h.projectErr(ctx, in.Alias, err)
	}
	return okOut{Status: "archived; ingestion rejected, data kept, reversible with restore_project"}, nil
}

func (h *host) restoreProject(ctx context.Context, in aliasIn) (okOut, error) {
	if err := h.ops.RestoreProject(ctx, actorFrom(ctx), in.Alias); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "restored"}, nil
}

type keyIn struct {
	Project string `json:"project" jsonschema:"project alias"`
	Label   string `json:"label" jsonschema:"key label, e.g. web, ios; unique per project"`
}
type keyOut struct {
	Key     string `json:"key,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	Status  string `json:"status"`
	Note    string `json:"note,omitempty"`
}

func (h *host) issueKey(ctx context.Context, in keyIn) (keyOut, error) {
	key, err := h.ops.IssueIngestKey(ctx, actorFrom(ctx), in.Project, in.Label)
	if err != nil {
		return keyOut{}, h.projectErr(ctx, in.Project, err)
	}
	out := keyOut{Key: key, Status: "issued"}
	// Project should still exist (the key issue above would have failed
	// otherwise), but guard the lookup anyway: don't fail an
	// already-issued key just because enrichment can't find the project.
	if p := h.reg.Snapshot(ctx).Project(in.Project); p != nil {
		out.Snippet = manage.Snippet(h.publicURL, key, p.Identity)
		if h.publicURL == "" {
			out.Note = "PUBLIC_URL is not configured; the snippet uses the placeholder " + manage.SnippetPlaceholderBase + " — ask the operator for the collector's public URL"
		}
	}
	return out, nil
}

func (h *host) disableKey(ctx context.Context, in keyIn) (okOut, error) {
	if err := h.ops.DisableIngestKey(ctx, actorFrom(ctx), in.Project, in.Label); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "disabled; reversible with enable_ingest_key"}, nil
}

func (h *host) enableKey(ctx context.Context, in keyIn) (okOut, error) {
	if err := h.ops.EnableIngestKey(ctx, actorFrom(ctx), in.Project, in.Label); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "enabled"}, nil
}

type listKeysIn struct {
	Project string `json:"project,omitempty" jsonschema:"filter to one project"`
}
type listKeysOut struct {
	Keys []keyRow `json:"keys"`
}
type keyRow struct {
	Project, Label, Key, State string
}

func (h *host) listKeys(ctx context.Context, in listKeysIn) (listKeysOut, error) {
	_, ks, err := h.ops.St.LoadRegistry(ctx)
	if err != nil {
		return listKeysOut{}, err
	}
	var out listKeysOut
	for _, k := range ks {
		if in.Project != "" && k.Project != in.Project {
			continue
		}
		state := "active"
		if k.Disabled {
			state = "disabled"
		}
		out.Keys = append(out.Keys, keyRow{k.Project, k.Label, k.Key, state})
	}
	return out, nil
}
