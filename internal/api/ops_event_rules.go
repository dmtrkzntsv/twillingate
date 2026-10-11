package api

import (
	"context"
	"strconv"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Event rules (spec 2026-10-11): rename and block product and measures
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
	To        string `json:"to" jsonschema:"the new name; not a $ name"`
	Merge     bool   `json:"merge,omitempty" jsonschema:"set true to rename onto a name that already has events: the two merge and cannot be split again; merged days' unique counts become an upper bound"`
}

type eventNameIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Family    string `json:"family" jsonschema:"product or measures"`
	Name      string `json:"name" jsonschema:"the event name; not a $ name"`
}

type renameEventOut struct {
	Rule      eventRuleOut     `json:"rule"`
	Rewritten map[string]int64 `json:"rewritten" jsonschema:"rows rewritten per table"`
}

type blockEventOut struct {
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

func (h *host) renameEvent(ctx context.Context, in renameEventIn) (renameEventOut, error) {
	rule, rewritten, err := h.ops.RenameEvent(ctx, actorFrom(ctx), in.ProjectID, in.Family, in.From, in.To, in.Merge)
	if err != nil {
		return renameEventOut{}, h.ruleErr(ctx, in.ProjectID, err)
	}
	if rewritten == nil {
		rewritten = map[string]int64{}
	}
	return renameEventOut{Rule: ruleOut(rule), Rewritten: rewritten}, nil
}

func (h *host) blockEvent(ctx context.Context, in eventNameIn) (blockEventOut, error) {
	rule, err := h.ops.BlockEvent(ctx, actorFrom(ctx), in.ProjectID, in.Family, in.Name)
	if err != nil {
		return blockEventOut{}, h.ruleErr(ctx, in.ProjectID, err)
	}
	return blockEventOut{Rule: ruleOut(rule)}, nil
}

func (h *host) removeEventRule(ctx context.Context, in eventNameIn) (okOut, error) {
	if err := h.ops.RemoveEventRule(ctx, actorFrom(ctx), in.ProjectID, in.Family, in.Name); err != nil {
		return okOut{}, h.ruleErr(ctx, in.ProjectID, err)
	}
	return okOut{Status: "removed"}, nil
}

// eventNames feeds the console's Settings page: every product and measures
// name with events in the range, busiest first. Product counts events;
// measures count samples. $ names are left out: rules refuse them.
func (h *host) eventNames(ctx context.Context, in rangeIn) (eventNamesOut, error) {
	if err := h.checkRange(ctx, in); err != nil {
		return eventNamesOut{}, err
	}
	res, err := h.run(ctx, `SELECT family, name, events FROM (
		SELECT 'product' AS family, event_name AS name, SUM(count) AS events
		  FROM v_product_daily WHERE project_id=?1 AND day BETWEEN ?2 AND ?3 AND event_name NOT LIKE '$%' GROUP BY event_name
		UNION ALL
		SELECT 'measures', event_name, SUM(samples)
		  FROM v_measures_daily WHERE project_id=?1 AND day BETWEEN ?2 AND ?3 AND event_name NOT LIKE '$%' GROUP BY event_name)
		ORDER BY events DESC, name`, in.ProjectID, in.From, in.To)
	if err != nil {
		return eventNamesOut{}, err
	}
	out := eventNamesOut{Events: make([]eventNameOut, 0, len(res.Rows))}
	for _, row := range res.Rows {
		n, _ := strconv.ParseInt(row[2], 10, 64)
		out.Events = append(out.Events, eventNameOut{Family: row[0], Name: row[1], Events: n})
	}
	return out, nil
}
