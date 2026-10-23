package api

import (
	"context"
	"sort"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// ---- list_attributes ----

// attributeKeysLimit is how many received keys an answer lists, the
// busiest over the range, below CONSOLE_QUERY_ROWS_MAX' default (1000):
// a project sending ids as keys can carry more than any row cap. The keys
// a rule names are listed beyond it. A variable so a test can lower it.
var attributeKeysLimit = 500

type attributesIn struct {
	ProjectID int64 `json:"project_id" jsonschema:"project id; call list_projects first"`
	usageRangeIn
}

type attributeOut struct {
	Key      string   `json:"key"`
	Events   int64    `json:"events" jsonschema:"product events and measures that carried the key over the range, rules applied"`
	Values   int64    `json:"values" jsonschema:"distinct values the key had over the range"`
	Received bool     `json:"received" jsonschema:"whether any event in the range carried the key; false for a blocked key or a rename target none carried"`
	Blocked  bool     `json:"blocked" jsonschema:"whether block_attribute hides the key from every read"`
	Aliases  []string `json:"aliases,omitempty" jsonschema:"keys renamed or merged into this one; they read as this key"`
}

type listAttributesOut struct {
	ProjectID  int64          `json:"project_id"`
	From       string         `json:"from"`
	To         string         `json:"to"`
	Attributes []attributeOut `json:"attributes" jsonschema:"the 500 busiest keys received in the range and every key a rule names, busiest first; a key renamed away is only in its target's aliases"`
	KeysTotal  int64          `json:"keys_total" jsonschema:"distinct keys received over the range, rules applied; more than the received keys listed when the list is capped"`
}

// listAttributes answers a project's attribute keys over a range, from
// the event files: each key its product events and measures carried
// (the busiest only, and those its attribute rules name), with how many
// carried it and its distinct values, and the keys only a rule names. The
// reader applies the rules, so a key renamed away reads as its target and
// a blocked key reads as none.
func (h *host) listAttributes(ctx context.Context, in attributesIn) (listAttributesOut, error) {
	snap := h.reg.Snapshot(ctx)
	fromD, toD, err := usageRange(in.usageRangeIn, time.Now())
	if err != nil {
		return listAttributesOut{}, err
	}
	p := snap.Project(in.ProjectID)
	if p == nil {
		return listAttributesOut{}, h.unknownProjectErr(ctx, in.ProjectID)
	}
	out := listAttributesOut{ProjectID: p.ID, From: fromD.String(), To: toD.String(), Attributes: []attributeOut{}}

	blocked := map[string]bool{}
	aliases := map[string][]string{}
	var named []string                               // listed even past the busiest
	for _, r := range snap.AttributeRuleList(p.ID) { // ordered by key, so are the aliases
		switch r.Action {
		case store.EventRuleBlock:
			blocked[r.Key] = true
			named = append(named, r.Key)
		case store.EventRuleRename:
			if len(aliases[r.To]) == 0 {
				named = append(named, r.To)
			}
			aliases[r.To] = append(aliases[r.To], r.Key)
		}
	}

	// The keys of the range's product and measures rows, natively: the
	// busiest and those a rule names (native.MaxKeep a query, so a longer
	// list asks again with the next names), keys_total over every key. A
	// key's values past 1,024 are estimated.
	limit := min(attributeKeysLimit, h.rowCap())
	params := native.Params{Project: p.ID, From: fromD, To: toD}
	seen := map[string]bool{}
	for i := 0; i == 0 || i < len(named); i += native.MaxKeep {
		res, err := h.answer(ctx, native.Query{Kind: "attributes", Table: "events", Output: "keys", Limit: limit,
			Keep: named[i:min(i+native.MaxKeep, len(named))]}, params)
		if err != nil {
			return listAttributesOut{}, err
		}
		if res.Truncated {
			return listAttributesOut{}, invalidf("too many attribute keys and values to count in one pass; narrow the date range")
		}
		for _, r := range res.Rows {
			out.KeysTotal = cellInt(r[3])
			if seen[r[0]] {
				continue
			}
			k := attributeOut{Key: r[0], Received: true, Blocked: blocked[r[0]], Aliases: aliases[r[0]], Events: cellInt(r[1]), Values: cellInt(r[2])}
			out.Attributes = append(out.Attributes, k)
			seen[r[0]] = true
		}
	}
	// The keys only a rule names, with nothing in the range.
	for _, k := range named {
		if !seen[k] {
			out.Attributes = append(out.Attributes, attributeOut{Key: k, Blocked: blocked[k], Aliases: aliases[k]})
			seen[k] = true
		}
	}
	sort.SliceStable(out.Attributes, func(i, j int) bool {
		a, b := out.Attributes[i], out.Attributes[j]
		if a.Events != b.Events {
			return a.Events > b.Events
		}
		if a.Received != b.Received {
			return a.Received
		}
		return a.Key < b.Key
	})
	return out, nil
}
