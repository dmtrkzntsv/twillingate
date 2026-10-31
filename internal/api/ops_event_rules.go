package api

import (
	"cmp"
	"context"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Event rules (spec 2026-10-11): rename, merge and block product and measures
// events per project. The rules, their audit and their refusals are
// manage's; these tools carry the typed refusals through unchanged, so
// REST answers 400, 404 and 409 and MCP tells the model why.

type eventRuleOut struct {
	Family    string `json:"family"`
	Name      string `json:"name"`
	Action    string `json:"action" jsonschema:"rename or block"`
	To        string `json:"to,omitempty" jsonschema:"the name a rename stores events under; absent for a block"`
	CreatedAt string `json:"created_at,omitempty"`
}

func ruleOut(r store.EventRule) eventRuleOut {
	return eventRuleOut{Family: string(r.Family), Name: r.Name, Action: r.Action, To: r.To, CreatedAt: r.CreatedAt}
}

type listEventRulesOut struct {
	Rules []eventRuleOut `json:"rules"`
}

type renameEventIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Family    string `json:"family" jsonschema:"product or measures"`
	From      string `json:"from" jsonschema:"the event name to rename; not a $ name"`
	To        string `json:"to" jsonschema:"the new name, with no events yet; not a $ name"`
}

type mergeEventIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Family    string `json:"family" jsonschema:"product or measures"`
	From      string `json:"from" jsonschema:"the event name merged away; not a $ name"`
	To        string `json:"to" jsonschema:"the event it merges into; not a $ name, not blocked"`
}

type eventNameIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Family    string `json:"family" jsonschema:"product or measures"`
	Name      string `json:"name" jsonschema:"the event name; not a $ name"`
}

// eventRuleResultOut answers a rename, a merge or a block: the rule.
type eventRuleResultOut struct {
	Rule eventRuleOut `json:"rule"`
}

type eventNameOut struct {
	Family string `json:"family"`
	Name   string `json:"name"`
	Events int64  `json:"events" jsonschema:"product events, or measure samples, in the range"`
}

type eventNamesOut struct {
	Events []eventNameOut `json:"events"`
}

// ruleErr is projectErr for tools where the missing thing may be the rule,
// not the project: only an unknown project gets the list of valid ones, a
// missing rule keeps manage's message, which names it. Both are 404.
func (h *host) ruleErr(ctx context.Context, id int64, err error) error {
	if err == nil {
		return nil
	}
	if h.reg.Snapshot(ctx).Project(id) == nil {
		return h.unknownProjectErr(ctx, id)
	}
	return err
}

func (h *host) listEventRules(ctx context.Context, in idIn) (listEventRulesOut, error) {
	rules, err := h.ops.ListEventRules(ctx, in.ProjectID)
	if err != nil {
		return listEventRulesOut{}, h.ruleErr(ctx, in.ProjectID, err)
	}
	out := listEventRulesOut{Rules: make([]eventRuleOut, 0, len(rules))}
	for _, r := range rules {
		out.Rules = append(out.Rules, ruleOut(r))
	}
	return out, nil
}

func (h *host) renameEvent(ctx context.Context, in renameEventIn) (eventRuleResultOut, error) {
	rule, err := h.ops.RenameEvent(ctx, actorFrom(ctx), in.ProjectID, in.Family, in.From, in.To)
	return h.movedOut(ctx, in.ProjectID, rule, err)
}

func (h *host) mergeEvent(ctx context.Context, in mergeEventIn) (eventRuleResultOut, error) {
	rule, err := h.ops.MergeEvent(ctx, actorFrom(ctx), in.ProjectID, in.Family, in.From, in.To)
	return h.movedOut(ctx, in.ProjectID, rule, err)
}

// movedOut answers a rename, a merge or a block: the rule.
func (h *host) movedOut(ctx context.Context, id int64, rule store.EventRule, err error) (eventRuleResultOut, error) {
	if err != nil {
		return eventRuleResultOut{}, h.ruleErr(ctx, id, err)
	}
	return eventRuleResultOut{Rule: ruleOut(rule)}, nil
}

func (h *host) blockEvent(ctx context.Context, in eventNameIn) (eventRuleResultOut, error) {
	rule, err := h.ops.BlockEvent(ctx, actorFrom(ctx), in.ProjectID, in.Family, in.Name)
	return h.movedOut(ctx, in.ProjectID, rule, err)
}

func (h *host) removeEventRule(ctx context.Context, in eventNameIn) (okOut, error) {
	err := h.ops.RemoveEventRule(ctx, actorFrom(ctx), in.ProjectID, in.Family, in.Name)
	return ok("removed", h.ruleErr(ctx, in.ProjectID, err))
}

// eventNamesMax is how many names event_names lists: a project sending
// generated names would otherwise exceed the row cap.
const eventNamesMax = 200

// eventNames feeds the console's Settings page: every product and
// measures name of the range in the event files, rules applied, the
// busiest eventNamesMax first, ties by name. Product counts events;
// measures count samples. $ names are left out: rules refuse them. A
// result past the row cap is refused, never a partial list. Should more
// than 1,000 names tie with the 200th, the ones kept are the engine's
// first 1,000 sorted (topRows), not the first 200 by name over them all.
func (h *host) eventNames(ctx context.Context, in rangeIn) (eventNamesOut, error) {
	if err := h.checkRange(ctx, in); err != nil {
		return eventNamesOut{}, err
	}
	out := eventNamesOut{Events: []eventNameOut{}}
	p, ok := rangeParams(in)
	if !ok {
		return out, nil
	}
	res, _, _, err := h.topRows(ctx, native.Query{Kind: "breakdown", Table: "events", Dimensions: []string{"family", "event_name"},
		Order: "count", Metrics: []native.Metric{{Op: "count"}}, Where: []native.Filter{
			{Column: "family", Op: "in", Values: []string{"product", "measures"}},
			{Column: "event_name", Op: "not_prefix", Values: []string{"$"}}}},
		p, eventNamesMax, func(a, b []string) int {
			if c := cmp.Compare(cellInt(b[2]), cellInt(a[2])); c != 0 {
				return c
			}
			return cmp.Compare(a[1], b[1])
		})
	if err != nil {
		return eventNamesOut{}, err
	}
	if res.Truncated || len(res.Rows) > h.rowCap() {
		return eventNamesOut{}, invalidf("too many rows; narrow the date range")
	}
	for _, row := range res.Rows {
		out.Events = append(out.Events, eventNameOut{Family: row[0], Name: row[1], Events: cellInt(row[2])})
	}
	return out, nil
}
