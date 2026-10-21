package api

import (
	"context"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Attribute rules (spec 2026-10-11 project attributes): rename, merge and
// block the custom attribute keys of a project's product and measures
// events. The rules, their audit and their refusals are manage's; these
// tools carry the typed refusals through unchanged (ruleErr), so REST
// answers 400, 404 and 409 and MCP tells the model why. list_attributes
// shows the rules on the keys they touch.

type attributeRuleOut struct {
	Key       string `json:"key"`
	Action    string `json:"action" jsonschema:"rename or block"`
	To        string `json:"to,omitempty" jsonschema:"the key a rename stores the attribute under; absent for a block"`
	CreatedAt string `json:"created_at,omitempty"`
}

func attrRuleOut(r store.AttributeRule) attributeRuleOut {
	return attributeRuleOut{Key: r.Key, Action: r.Action, To: r.To, CreatedAt: r.CreatedAt}
}

type renameAttributeIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	From      string `json:"from" jsonschema:"the attribute key to rename; not a $ key"`
	To        string `json:"to" jsonschema:"the new key, with no data yet; not a $ key"`
}

type mergeAttributeIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	From      string `json:"from" jsonschema:"the attribute key merged away; not a $ key"`
	To        string `json:"to" jsonschema:"the key it merges into; not a $ key, not blocked"`
}

type attributeKeyIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Key       string `json:"key" jsonschema:"the attribute key; not a $ key"`
}

type renameAttributeOut struct {
	Rule attributeRuleOut `json:"rule"`
}

type blockAttributeOut struct {
	Rule attributeRuleOut `json:"rule"`
}

func (h *host) renameAttribute(ctx context.Context, in renameAttributeIn) (renameAttributeOut, error) {
	rule, err := h.ops.RenameAttribute(ctx, actorFrom(ctx), in.ProjectID, in.From, in.To)
	return h.movedAttributeOut(ctx, in.ProjectID, rule, err)
}

func (h *host) mergeAttribute(ctx context.Context, in mergeAttributeIn) (renameAttributeOut, error) {
	rule, err := h.ops.MergeAttribute(ctx, actorFrom(ctx), in.ProjectID, in.From, in.To)
	return h.movedAttributeOut(ctx, in.ProjectID, rule, err)
}

// movedAttributeOut answers a rename or a merge: the rule.
func (h *host) movedAttributeOut(ctx context.Context, id int64, rule store.AttributeRule, err error) (renameAttributeOut, error) {
	if err != nil {
		return renameAttributeOut{}, h.ruleErr(ctx, id, err)
	}
	return renameAttributeOut{Rule: attrRuleOut(rule)}, nil
}

func (h *host) blockAttribute(ctx context.Context, in attributeKeyIn) (blockAttributeOut, error) {
	rule, err := h.ops.BlockAttribute(ctx, actorFrom(ctx), in.ProjectID, in.Key)
	if err != nil {
		return blockAttributeOut{}, h.ruleErr(ctx, in.ProjectID, err)
	}
	return blockAttributeOut{Rule: attrRuleOut(rule)}, nil
}

func (h *host) removeAttributeRule(ctx context.Context, in attributeKeyIn) (okOut, error) {
	if err := h.ops.RemoveAttributeRule(ctx, actorFrom(ctx), in.ProjectID, in.Key); err != nil {
		return okOut{}, h.ruleErr(ctx, in.ProjectID, err)
	}
	return okOut{Status: "removed"}, nil
}
