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

type createProjectIn struct {
	Name           string   `json:"name" jsonschema:"display name (required); need not be unique"`
	AllowedOrigins []string `json:"allowed_origins,omitempty" jsonschema:"origins allowed to post events. * is a wildcard: https://*.example.com covers every subdomain, a bare * allows any origin"`
	// Attributes declares which product-event attribute keys are broken
	// down (v_events_flat columns, agg_product_attrs rollups). Rollups
	// always run regardless of this list.
	Attributes []string `json:"attributes,omitempty" jsonschema:"attribute keys to break down and expose as flat-view columns"`
	// SkipKey rather than IssueKey: JSON booleans have no "unset", and
	// the zero value must give the default behaviour (issue a key).
	SkipKey bool `json:"skip_key,omitempty" jsonschema:"set true to NOT issue a first ingest key"`
}

type updateProjectIn struct {
	ProjectID      int64    `json:"project_id" jsonschema:"project id; call list_projects first"`
	Name           string   `json:"name,omitempty" jsonschema:"new display name; omit to keep"`
	AllowedOrigins []string `json:"allowed_origins,omitempty" jsonschema:"replaces the whole list; an explicit [] clears it; omit to keep"`
	Attributes     []string `json:"attributes,omitempty" jsonschema:"replaces the whole list; omit to keep"`
}

type projectToolOut struct {
	ProjectID int64  `json:"project_id"`
	Key       string `json:"key,omitempty"`
	Snippet   string `json:"snippet,omitempty"`
	Note      string `json:"note,omitempty"`
}

// createProject issues the first key in the same transaction as the
// project unless skip_key is set: a refusal leaves nothing behind, so the
// caller can fix the input and retry without meeting a keyless leftover.
func (h *host) createProject(ctx context.Context, in createProjectIn) (projectToolOut, error) {
	spec := manage.ProjectSpec{Name: in.Name,
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
	out := projectToolOut{ProjectID: p.ID, Key: key}
	if key != "" {
		out.Snippet = manage.Snippet(h.publicURL, key)
		if h.publicURL == "" {
			out.Note = "PUBLIC_URL is not configured; the snippet uses the placeholder " + manage.SnippetPlaceholderBase + " — ask the operator for the collector's public URL"
		}
	}
	return out, nil
}

// updateProject passes the merge to Ops.UpdateProject: "" and nil keep,
// a non-nil slice replaces, so an explicit [] clears the origins. JSON
// decoding keeps that distinction (an omitted array is nil, [] is empty
// and non-nil), which is what lets the tool clear a list at all.
func (h *host) updateProject(ctx context.Context, in updateProjectIn) (projectToolOut, error) {
	p, err := h.ops.UpdateProject(ctx, actorFrom(ctx), manage.ProjectSpec{
		ID: in.ProjectID, Name: in.Name,
		AllowedOrigins: in.AllowedOrigins, Attributes: in.Attributes})
	if err != nil {
		return projectToolOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	return projectToolOut{ProjectID: p.ID}, nil
}

// projectErr rewrites a not-found refusal into the recoverable form
// (the valid ids and names listed, see unknownProjectErr) for tools whose
// only lookup is the project itself. Key tools keep the store's message:
// there the missing thing may be the label, and a list of projects would
// mislead.
func (h *host) projectErr(ctx context.Context, id int64, err error) error {
	if errors.Is(err, manage.ErrNotFound) {
		return h.unknownProjectErr(ctx, id)
	}
	return err
}

type idIn struct {
	ProjectID int64 `json:"project_id" jsonschema:"project id; call list_projects first"`
}
type okOut struct {
	Status string `json:"status"`
}

func (h *host) archiveProject(ctx context.Context, in idIn) (okOut, error) {
	if err := h.ops.ArchiveProject(ctx, actorFrom(ctx), in.ProjectID); err != nil {
		return okOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	return okOut{Status: "archived; ingestion rejected, data kept, reversible with restore_project"}, nil
}

func (h *host) restoreProject(ctx context.Context, in idIn) (okOut, error) {
	if err := h.ops.RestoreProject(ctx, actorFrom(ctx), in.ProjectID); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "restored"}, nil
}

type keyIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Label     string `json:"label" jsonschema:"key label, e.g. web, ios; unique per project"`
}
type keyOut struct {
	Key     string `json:"key,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	Status  string `json:"status"`
	Note    string `json:"note,omitempty"`
}

func (h *host) issueKey(ctx context.Context, in keyIn) (keyOut, error) {
	key, err := h.ops.IssueIngestKey(ctx, actorFrom(ctx), in.ProjectID, in.Label)
	if err != nil {
		return keyOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	out := keyOut{Key: key, Status: "issued"}
	// Project should still exist (the key issue above would have failed
	// otherwise), but guard the lookup anyway: don't fail an
	// already-issued key just because enrichment can't find the project.
	if p := h.reg.Snapshot(ctx).Project(in.ProjectID); p != nil {
		out.Snippet = manage.Snippet(h.publicURL, key)
		if h.publicURL == "" {
			out.Note = "PUBLIC_URL is not configured; the snippet uses the placeholder " + manage.SnippetPlaceholderBase + " — ask the operator for the collector's public URL"
		}
	}
	return out, nil
}

func (h *host) disableKey(ctx context.Context, in keyIn) (okOut, error) {
	if err := h.ops.DisableIngestKey(ctx, actorFrom(ctx), in.ProjectID, in.Label); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "disabled; reversible with enable_ingest_key"}, nil
}

func (h *host) enableKey(ctx context.Context, in keyIn) (okOut, error) {
	if err := h.ops.EnableIngestKey(ctx, actorFrom(ctx), in.ProjectID, in.Label); err != nil {
		return okOut{}, err
	}
	return okOut{Status: "enabled"}, nil
}

type listKeysIn struct {
	ProjectID int64 `json:"project_id,omitempty" jsonschema:"filter to one project"`
}
type listKeysOut struct {
	Keys []keyRow `json:"keys"`
}
type keyRow struct {
	ProjectID         int64 `json:"project_id"`
	Label, Key, State string
}

func (h *host) listKeys(ctx context.Context, in listKeysIn) (listKeysOut, error) {
	_, ks, err := h.ops.St.LoadRegistry(ctx)
	if err != nil {
		return listKeysOut{}, err
	}
	var out listKeysOut
	for _, k := range ks {
		if in.ProjectID != 0 && k.ProjectID != in.ProjectID {
			continue
		}
		state := "active"
		if k.Disabled {
			state = "disabled"
		}
		out.Keys = append(out.Keys, keyRow{ProjectID: k.ProjectID, Label: k.Label, Key: k.Key, State: state})
	}
	return out, nil
}
