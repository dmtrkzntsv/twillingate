package api

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// ---- list_attributes ----

// attributeKeysLimit is how many received keys an answer lists, the
// busiest over the range, below CONSOLE_QUERY_ROWS_MAX' default (1000):
// a project sending ids as keys can carry more than any row cap. The keys
// a breakdown or a rule names are listed beyond it. A variable so a test
// can lower it.
var attributeKeysLimit = 500

type attributesIn struct {
	ProjectID int64 `json:"project_id" jsonschema:"project id; call list_projects first"`
	usageRangeIn
}

type attributeOut struct {
	Key       string   `json:"key"`
	Events    int64    `json:"events" jsonschema:"events and measures that carried the key over the range, as the daily pass counted them; today's are counted the night after, so a key first received today has 0"`
	MaxValues *int64   `json:"max_values" jsonschema:"the most distinct values one event (or measure) had on one day; null until the daily pass counts the day (today's), and for a key not received"`
	Received  bool     `json:"received" jsonschema:"whether any event in the range carried the key; false for a breakdown or blocked key none carried"`
	Breakdown bool     `json:"breakdown" jsonschema:"whether the project breaks the key down; switch it with update_project's attributes"`
	Blocked   bool     `json:"blocked" jsonschema:"whether block_attribute strips the key from events"`
	Aliases   []string `json:"aliases,omitempty" jsonschema:"keys renamed or merged into this one; events sent with them are stored under it"`
}

type listAttributesOut struct {
	ProjectID      int64          `json:"project_id"`
	From           string         `json:"from"`
	To             string         `json:"to"`
	Attributes     []attributeOut `json:"attributes" jsonschema:"the 500 busiest keys received in the range, every breakdown and every blocked key, busiest first; a key others are renamed to is listed too, a key renamed away only in its target's aliases"`
	KeysTotal      int64          `json:"keys_total" jsonschema:"distinct keys received over the range, keys renamed away left out; more than the received keys listed when the list is capped"`
	ValuesCap      int            `json:"values_cap" jsonschema:"ATTRIBUTE_VALUES_TOP_N; 0 means no cap"`
	BreakdownsUsed int            `json:"breakdowns_used" jsonschema:"breakdown keys across every active project"`
	BreakdownsMax  int            `json:"breakdowns_max" jsonschema:"ATTRIBUTE_BREAKDOWNS_MAX; 0 means no limit"`
}

// listAttributes answers a project's attributes over a range: the keys
// usage_attributes holds (the busiest only), the breakdown keys, and the
// keys its attribute rules name. A key renamed away is left out, even
// with a usage row (one counted before the rule): it is an alias of its
// target.
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
	out := listAttributesOut{ProjectID: p.ID, From: fromD.String(), To: toD.String(), Attributes: []attributeOut{},
		ValuesCap: h.valuesCap, BreakdownsUsed: snap.BreakdownsInUse(), BreakdownsMax: h.breakdownsMax}

	breakdown := map[string]bool{}
	for _, k := range p.Attributes {
		breakdown[k] = true
	}
	blocked := map[string]bool{}
	aliases := map[string][]string{}
	renamed := []string{}
	named := append([]string{}, p.Attributes...) // listed even past the busiest
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
			renamed = append(renamed, r.Key)
		}
	}
	renamedJSON, _ := json.Marshal(renamed)

	// The busiest keys only, so a project with more keys than the row cap
	// still answers (h.run refuses a truncated result); keys_total says
	// how many there are.
	where := `FROM usage_attributes WHERE project_id = ?1 AND day BETWEEN ?2 AND ?3
		AND attr_key NOT IN (SELECT value FROM json_each(?4))`
	total, err := h.run(ctx, `SELECT COUNT(DISTINCT attr_key) `+where, p.ID, out.From, out.To, string(renamedJSON))
	if err != nil {
		return listAttributesOut{}, err
	}
	out.KeysTotal, _ = strconv.ParseInt(total.Rows[0][0], 10, 64)
	res, err := h.run(ctx, `SELECT attr_key, SUM(events), MAX(max_values) `+where+`
		GROUP BY attr_key ORDER BY SUM(events) DESC, attr_key LIMIT ?5`,
		p.ID, out.From, out.To, string(renamedJSON), min(attributeKeysLimit, h.db.MaxRows()))
	if err != nil {
		return listAttributesOut{}, err
	}
	seen := map[string]bool{}
	entry := func(key string) attributeOut {
		return attributeOut{Key: key, Breakdown: breakdown[key], Blocked: blocked[key], Aliases: aliases[key]}
	}
	add := func(rows [][]string) {
		for _, r := range rows {
			k := entry(r[0])
			k.Events, _ = strconv.ParseInt(r[1], 10, 64)
			k.Received = true
			// A day not yet counted has a NULL max_values, which reads as no number.
			if mv, err := strconv.ParseInt(r[2], 10, 64); err == nil {
				k.MaxValues = &mv
			}
			out.Attributes = append(out.Attributes, k)
			seen[r[0]] = true
		}
	}
	add(res.Rows)
	// Named keys past the busiest: their own counts, then the ones none
	// carried, with events 0.
	var rest []string
	for _, k := range named {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	if len(rest) > 0 {
		keys, _ := json.Marshal(rest)
		res, err := h.run(ctx, `SELECT attr_key, SUM(events), MAX(max_values) `+where+`
			AND attr_key IN (SELECT value FROM json_each(?5)) GROUP BY attr_key`,
			p.ID, out.From, out.To, string(renamedJSON), string(keys))
		if err != nil {
			return listAttributesOut{}, err
		}
		add(res.Rows)
	}
	for _, k := range rest {
		if !seen[k] {
			out.Attributes = append(out.Attributes, entry(k))
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
